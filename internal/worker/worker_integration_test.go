//go:build integration

package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
	"github.com/stackorder/stackorder/internal/worker"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

type countingStore struct {
	*store.Store
	claims atomic.Int32
}

func (c *countingStore) ClaimEvents(ctx context.Context, worker string, n int) ([]store.Event, error) {
	c.claims.Add(1)
	return c.Store.ClaimEvents(ctx, worker, n)
}

func insertEvents(t *testing.T, st *store.Store, kind string, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("%s-%03d", kind, i)
		ok, err := st.InsertEvent(t.Context(), ids[i], kind, json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)))
		require.NoError(t, err)
		require.True(t, ok)
	}
	return ids
}

func pending(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	require.NoError(t, st.Pool().QueryRow(t.Context(),
		`SELECT (SELECT count(*) FROM events WHERE done_at IS NULL) + (SELECT count(*) FROM jobs WHERE done_at IS NULL)`).Scan(&n))
	return n
}

func drained(t *testing.T, st *store.Store) func() bool {
	return func() bool { return pending(t, st) == 0 }
}

func TestPGEachEventHandledExactlyOnce(t *testing.T) {
	st := pgtest.New(t)
	ids := insertEvents(t, st, "pull_request", 100)
	p := worker.New(st, worker.Options{Workers: 4, ClaimBatch: 7, PollInterval: 10 * time.Millisecond})

	var mu sync.Mutex
	calls := map[string]int{}
	workers := map[string]bool{}
	p.OnEvent("pull_request", func(_ context.Context, ev store.Event) error {
		mu.Lock()
		defer mu.Unlock()
		calls[ev.ID]++
		workers[ev.ClaimedBy] = true
		return nil
	})
	start(t, p)
	require.Eventually(t, drained(t, st), 30*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, calls, len(ids))
	for _, id := range ids {
		assert.Equal(t, 1, calls[id], "event %s", id)
	}
	assert.Greater(t, len(workers), 1, "the work is spread over several workers")
}

func TestPGRetryWithBackoff(t *testing.T) {
	st := pgtest.New(t)
	p := worker.New(st, worker.Options{Workers: 2, PollInterval: 10 * time.Millisecond, Backoff: func(attempt int) time.Duration {
		return time.Duration(attempt) * 20 * time.Millisecond
	}})
	var calls atomic.Int32
	var times []time.Time
	var mu sync.Mutex
	p.OnJob(jobDispatchWave, func(context.Context, store.Job) error {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if calls.Add(1) < 3 {
			return errors.New("dispatch: github 502")
		}
		return nil
	})
	require.NoError(t, p.Enqueue(t.Context(), jobDispatchWave, map[string]any{"run_id": "r1", "wave": 0}, time.Time{}, "dispatch_wave:r1:0"))
	start(t, p)
	require.Eventually(t, drained(t, st), 10*time.Second, 10*time.Millisecond)

	jobs, err := st.ClaimJobs(t.Context(), "probe", 10)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	var attempts int
	var lastError string
	require.NoError(t, st.Pool().QueryRow(t.Context(), `SELECT attempts, last_error FROM jobs WHERE dedupe_key = 'dispatch_wave:r1:0'`).Scan(&attempts, &lastError))
	assert.Equal(t, 2, attempts)
	assert.Equal(t, "dispatch: github 502", lastError)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, times, 3)
	assert.GreaterOrEqual(t, times[1].Sub(times[0]), 20*time.Millisecond, "the first retry waits Backoff(1)")
	assert.GreaterOrEqual(t, times[2].Sub(times[1]), 40*time.Millisecond, "the second retry waits Backoff(2)")
}

func TestPGDeadLetter(t *testing.T) {
	st := pgtest.New(t)
	m := metrics.New()
	p := worker.New(st, worker.Options{Workers: 1, PollInterval: 10 * time.Millisecond, MaxAttempts: 3, Backoff: fastBackoff, Metrics: m})
	var calls atomic.Int32
	p.OnEvent("workflow_job", func(context.Context, store.Event) error {
		calls.Add(1)
		return errors.New("unexpected payload")
	})
	insertEvents(t, st, "workflow_job", 1)
	start(t, p)
	require.Eventually(t, drained(t, st), 10*time.Second, 10*time.Millisecond)

	ev, err := st.GetEvent(t.Context(), "workflow_job-000")
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
	assert.Equal(t, 3, ev.Attempts)
	assert.Equal(t, "unexpected payload", ev.LastError)
	assert.NotNil(t, ev.DoneAt)
	assert.InDelta(t, 1, metricstest.Value(t, m, `stackorder_events_dead_total{kind="workflow_job"}`), 0)
}

func TestPGPanicRecovery(t *testing.T) {
	st := pgtest.New(t)
	p := worker.New(st, worker.Options{Workers: 1, PollInterval: 10 * time.Millisecond, Backoff: fastBackoff})
	var calls atomic.Int32
	p.OnEvent("push", func(context.Context, store.Event) error {
		if calls.Add(1) == 1 {
			panic("nil graph")
		}
		return nil
	})
	insertEvents(t, st, "push", 1)
	start(t, p)
	require.Eventually(t, drained(t, st), 10*time.Second, 10*time.Millisecond)

	ev, err := st.GetEvent(t.Context(), "push-000")
	require.NoError(t, err)
	assert.Equal(t, 1, ev.Attempts)
	assert.Equal(t, "worker: push handler panicked: nil graph", ev.LastError)
}

func TestPGUnknownKindCompletes(t *testing.T) {
	st := pgtest.New(t)
	m := metrics.New()
	p := worker.New(st, worker.Options{Workers: 1, PollInterval: 10 * time.Millisecond, Metrics: m})
	insertEvents(t, st, "sponsorship", 2)
	require.NoError(t, p.Enqueue(t.Context(), "retired_kind", nil, time.Time{}, ""))
	start(t, p)
	require.Eventually(t, drained(t, st), 10*time.Second, 10*time.Millisecond)

	ev, err := st.GetEvent(t.Context(), "sponsorship-001")
	require.NoError(t, err)
	assert.Zero(t, ev.Attempts)
	assert.Empty(t, ev.LastError)
	assert.InDelta(t, 2, metricstest.Value(t, m, `stackorder_events_processed_total{kind="sponsorship",result="ignored"}`), 0)
	assert.InDelta(t, 1, metricstest.Value(t, m, `stackorder_jobs_processed_total{kind="retired_kind",result="ignored"}`), 0)
}

func TestPGGracefulShutdown(t *testing.T) {
	st := pgtest.New(t)
	p := worker.New(st, worker.Options{Workers: 1, ClaimBatch: 5, PollInterval: time.Hour})
	started := make(chan struct{})
	var once sync.Once
	var completed atomic.Bool
	p.OnEvent("check_suite", func(ctx context.Context, _ store.Event) error {
		once.Do(func() { close(started) })
		select {
		case <-time.After(300 * time.Millisecond):
			completed.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	insertEvents(t, st, "check_suite", 3)
	stop := start(t, p)
	<-started
	begin := time.Now()
	require.NoError(t, stop())
	assert.GreaterOrEqual(t, time.Since(begin), 200*time.Millisecond, "Run waited for the handler")
	assert.True(t, completed.Load())

	first, err := st.GetEvent(t.Context(), "check_suite-000")
	require.NoError(t, err)
	assert.NotNil(t, first.DoneAt, "the in-flight event was completed")
	for _, id := range []string{"check_suite-001", "check_suite-002"} {
		ev, err := st.GetEvent(t.Context(), id)
		require.NoError(t, err)
		assert.Nil(t, ev.ClaimedAt, "%s was handed back", id)
		assert.Zero(t, ev.Attempts)
	}
	again, err := st.ClaimEvents(t.Context(), "next-instance", 10)
	require.NoError(t, err)
	assert.Len(t, again, 2, "another instance can take the rest at once")
}

func TestPGNotifyWakesBeforePollInterval(t *testing.T) {
	st := &countingStore{Store: pgtest.New(t)}
	p := worker.New(st, worker.Options{Workers: 2, PollInterval: time.Hour})
	handled := make(chan string, 1)
	p.OnEvent("push", func(_ context.Context, ev store.Event) error { handled <- ev.ID; return nil })
	start(t, p)
	require.Eventually(t, func() bool { return st.claims.Load() >= 2 }, eventually, time.Millisecond, "the workers went idle")

	ok, err := st.InsertEvent(t.Context(), "fresh", "push", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, ok)
	p.Notify()
	select {
	case id := <-handled:
		assert.Equal(t, "fresh", id)
	case <-time.After(eventually):
		t.Fatal("Notify did not wake the pool")
	}
}

func TestPGStaleClaimRelease(t *testing.T) {
	st := pgtest.New(t)
	insertEvents(t, st, "push", 1)
	claimed, err := st.ClaimEvents(t.Context(), "crashed-host:1:0", 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = st.Pool().Exec(t.Context(), `UPDATE events SET claimed_at = now() - interval '1 hour'`)
	require.NoError(t, err)

	p := worker.New(st, worker.Options{Workers: 1, PollInterval: 10 * time.Millisecond})
	var by atomic.Value
	p.OnEvent("push", func(_ context.Context, ev store.Event) error { by.Store(ev.ClaimedBy); return nil })
	start(t, p)
	require.Eventually(t, drained(t, st), 10*time.Second, 10*time.Millisecond)

	ev, err := st.GetEvent(t.Context(), "push-000")
	require.NoError(t, err)
	assert.Equal(t, 1, ev.Attempts, "the expired claim counts as an attempt")
	assert.NotEqual(t, "crashed-host:1:0", by.Load())
}

func TestPGEnqueueDedupe(t *testing.T) {
	st := pgtest.New(t)
	p := worker.New(st, worker.Options{Workers: 1, PollInterval: 10 * time.Millisecond})
	var mu sync.Mutex
	var payloads []string
	p.OnJob(jobScheduleDrift, func(_ context.Context, job store.Job) error {
		mu.Lock()
		defer mu.Unlock()
		payloads = append(payloads, string(job.Payload))
		return nil
	})
	for range 3 {
		require.NoError(t, p.Enqueue(t.Context(), jobScheduleDrift, map[string]int64{"repo_id": 7}, time.Time{}, "schedule_drift:7:1790000000"))
	}
	start(t, p)
	require.Eventually(t, drained(t, st), 10*time.Second, 10*time.Millisecond)
	require.NoError(t, p.Enqueue(t.Context(), jobScheduleDrift, map[string]int64{"repo_id": 7}, time.Time{}, "schedule_drift:7:1790000000"))
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, payloads, 1, "a dedupe key stays taken after the job is done")
	assert.JSONEq(t, `{"repo_id":7}`, payloads[0])
}

func TestPGMetrics(t *testing.T) {
	st := pgtest.New(t)
	insertEvents(t, st, "push", 3)
	_, err := st.Pool().Exec(t.Context(), `UPDATE events SET received_at = now() - interval '3 seconds'`)
	require.NoError(t, err)
	m := metrics.New()
	p := worker.New(st, worker.Options{Workers: 1, PollInterval: 10 * time.Millisecond, Metrics: m})
	p.OnEvent("push", func(context.Context, store.Event) error { return nil })
	start(t, p)
	require.Eventually(t, drained(t, st), 10*time.Second, 10*time.Millisecond)

	samples := metricstest.Scrape(t, m)
	assert.InDelta(t, 3, samples["stackorder_webhook_lag_seconds_count"], 0)
	assert.GreaterOrEqual(t, samples["stackorder_webhook_lag_seconds_sum"], 9.0, "lag runs from received_at to claimed_at")
	assert.InDelta(t, 3, samples[`stackorder_events_processed_total{kind="push",result="ok"}`], 0)
	require.Eventually(t, func() bool {
		_, ok := metricstest.Scrape(t, m)[`stackorder_queue_depth{queue="events"}`]
		return ok
	}, eventually, 5*time.Millisecond, "queue depth is published when the pool starts")
}
