package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
	"github.com/stackorder/stackorder/internal/worker"
)

const (
	jobDispatchWave  = "dispatch_wave"
	jobDriftStack    = "drift_stack"
	jobScheduleDrift = "schedule_drift"
	jobCrossRepoPlan = "cross_repo_plan"
	jobReconcile     = "reconcile"
	jobPrune         = "prune"
	jobStaleLocks    = "stale_locks"
)

const eventually = 5 * time.Second

func fastBackoff(int) time.Duration { return time.Millisecond }

func start(t *testing.T, p *worker.Pool) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(time.Minute):
				t.Fatal("pool did not stop")
			}
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func TestDefaultBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{-1, time.Minute},
		{0, time.Minute},
		{1, time.Minute},
		{2, 5 * time.Minute},
		{3, 30 * time.Minute},
		{4, 2 * time.Hour},
		{5, 6 * time.Hour},
		{6, 6 * time.Hour},
		{100, 6 * time.Hour},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, worker.DefaultBackoff(tc.attempt), "attempt %d", tc.attempt)
	}
}

func TestDispatchByKind(t *testing.T) {
	q := &fakeQueue{}
	m := metrics.New()
	p := worker.New(q, worker.Options{Workers: 2, PollInterval: 10 * time.Millisecond, Metrics: m})

	var mu sync.Mutex
	seen := map[string][]string{}
	record := func(kind, id string) {
		mu.Lock()
		defer mu.Unlock()
		seen[kind] = append(seen[kind], id)
	}
	p.OnEvent("push", func(_ context.Context, ev store.Event) error { record("push", ev.ID); return nil })
	p.OnEvent("pull_request", func(_ context.Context, ev store.Event) error { record("pull_request", ev.ID); return nil })
	for _, kind := range []string{jobDispatchWave, jobDriftStack, jobScheduleDrift, jobCrossRepoPlan, jobReconcile, jobPrune, jobStaleLocks} {
		p.OnJob(kind, func(_ context.Context, job store.Job) error { record(job.Kind, job.ID.String()); return nil })
	}

	q.addEvent("e-push", "push")
	q.addEvent("e-pr", "pull_request")
	q.addEvent("e-unknown", "sponsorship")
	for _, kind := range []string{jobDispatchWave, jobReconcile, "mystery"} {
		require.NoError(t, p.Enqueue(t.Context(), kind, nil, time.Time{}, ""))
	}
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"e-push"}, seen["push"])
	assert.Equal(t, []string{"e-pr"}, seen["pull_request"])
	assert.Equal(t, []string{q.job(jobDispatchWave).ID.String()}, seen[jobDispatchWave])
	assert.Equal(t, []string{q.job(jobReconcile).ID.String()}, seen[jobReconcile])
	assert.NotContains(t, seen, "sponsorship")
	assert.NotContains(t, seen, "mystery")
	assert.Zero(t, q.event("e-unknown").Attempts, "an unhandled kind completes without an attempt")
	assert.Empty(t, q.event("e-unknown").LastError)

	samples := metricstest.Scrape(t, m)
	assert.InDelta(t, 1, samples[`stackorder_events_processed_total{kind="push",result="ok"}`], 0)
	assert.InDelta(t, 1, samples[`stackorder_events_processed_total{kind="sponsorship",result="ignored"}`], 0)
	assert.InDelta(t, 1, samples[`stackorder_jobs_processed_total{kind="reconcile",result="ok"}`], 0)
	assert.InDelta(t, 1, samples[`stackorder_jobs_processed_total{kind="mystery",result="ignored"}`], 0)
}

func TestRetryWithBackoff(t *testing.T) {
	q := &fakeQueue{}
	var attempts []int
	var mu sync.Mutex
	p := worker.New(q, worker.Options{
		Workers:      1,
		PollInterval: 5 * time.Millisecond,
		Backoff: func(attempt int) time.Duration {
			mu.Lock()
			defer mu.Unlock()
			attempts = append(attempts, attempt)
			return time.Duration(attempt) * time.Millisecond
		},
	})
	var calls atomic.Int32
	p.OnJob(jobDispatchWave, func(context.Context, store.Job) error {
		if calls.Add(1) < 3 {
			return errors.New("github 502")
		}
		return nil
	})
	require.NoError(t, p.Enqueue(t.Context(), jobDispatchWave, map[string]string{"run_id": "r"}, time.Time{}, ""))
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	job := q.job(jobDispatchWave)
	assert.Equal(t, int32(3), calls.Load())
	assert.Equal(t, 2, job.Attempts, "two failed attempts are recorded")
	assert.Equal(t, "github 502", job.LastError)
	mu.Lock()
	assert.Equal(t, []int{1, 2}, attempts, "backoff is asked with the failed attempt number")
	mu.Unlock()
	assert.Equal(t, []time.Duration{time.Millisecond, 2 * time.Millisecond}, q.backoffs())
}

func TestDeadLetterAfterMaxAttempts(t *testing.T) {
	q := &fakeQueue{}
	m := metrics.New()
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: 5 * time.Millisecond, MaxAttempts: 3, Backoff: fastBackoff, Metrics: m})
	var calls atomic.Int32
	p.OnEvent("check_run", func(context.Context, store.Event) error {
		calls.Add(1)
		return errors.New("malformed payload")
	})
	q.addEvent("poison", "check_run")
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	ev := q.event("poison")
	assert.Equal(t, int32(3), calls.Load(), "no attempt after the last one")
	assert.Equal(t, 3, ev.Attempts)
	assert.Equal(t, "malformed payload", ev.LastError, "the dead letter keeps its last error")
	assert.NotNil(t, ev.DoneAt)
	samples := metricstest.Scrape(t, m)
	assert.InDelta(t, 1, samples[`stackorder_events_dead_total{kind="check_run"}`], 0)
	assert.InDelta(t, 2, samples[`stackorder_events_processed_total{kind="check_run",result="error"}`], 0)
	assert.InDelta(t, 1, samples[`stackorder_events_processed_total{kind="check_run",result="dead"}`], 0)
}

func TestExhaustedRowIsGivenUpWithoutRunning(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: 5 * time.Millisecond, MaxAttempts: 2})
	var calls atomic.Int32
	p.OnEvent("push", func(context.Context, store.Event) error { calls.Add(1); return nil })
	q.addEvent("crashy", "push", func(ev *store.Event) { ev.Attempts, ev.LastError = 2, "claim expired" })
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	assert.Zero(t, calls.Load(), "a row whose claims kept expiring is not run again")
	ev := q.event("crashy")
	assert.Equal(t, 3, ev.Attempts)
	assert.Contains(t, ev.LastError, "gave up after 2 attempts: claim expired")
}

func TestPanicRecovery(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: 5 * time.Millisecond, Backoff: fastBackoff})
	var calls atomic.Int32
	p.OnEvent("issue_comment", func(context.Context, store.Event) error {
		if calls.Add(1) == 1 {
			panic(errors.New("comment body is nil"))
		}
		return nil
	})
	q.addEvent("c1", "issue_comment")
	q.addEvent("c2", "issue_comment")
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	assert.Equal(t, int32(3), calls.Load(), "the pool survives and retries the panicking row")
	var panicked []store.Event
	for _, id := range []string{"c1", "c2"} {
		if ev := q.event(id); ev.Attempts > 0 {
			panicked = append(panicked, ev)
		}
	}
	require.Len(t, panicked, 1)
	assert.Contains(t, panicked[0].LastError, "worker: issue_comment handler panicked: comment body is nil")
}

func TestHandlerTimeout(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: 5 * time.Millisecond, HandlerTimeout: 30 * time.Millisecond, MaxAttempts: 1})
	var deadline atomic.Bool
	p.OnJob(jobPrune, func(ctx context.Context, _ store.Job) error {
		_, ok := ctx.Deadline()
		deadline.Store(ok)
		<-ctx.Done()
		return ctx.Err()
	})
	require.NoError(t, p.Enqueue(t.Context(), jobPrune, nil, time.Time{}, ""))
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	assert.True(t, deadline.Load())
	assert.Equal(t, context.DeadlineExceeded.Error(), q.job(jobPrune).LastError)
}

func TestOutcomeIsSettledAfterTheHandlerReturns(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: 5 * time.Millisecond})
	const busy = 600 * time.Millisecond
	p.OnJob(jobDispatchWave, func(context.Context, store.Job) error {
		time.Sleep(busy)
		return nil
	})
	require.NoError(t, p.Enqueue(t.Context(), jobDispatchWave, nil, time.Time{}, ""))
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	budgets := q.settleBudgets()
	require.Len(t, budgets, 1)
	assert.Greater(t, budgets[0], worker.SettleTimeout-busy/2,
		"recording the outcome gets its own timeout, not what is left of one started with the handler")
}

func TestNotifyWakesBeforePollInterval(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 3, PollInterval: time.Hour})
	handled := make(chan string, 1)
	p.OnEvent("push", func(_ context.Context, ev store.Event) error { handled <- ev.ID; return nil })
	start(t, p)
	require.Eventually(t, func() bool { return q.claimCount() >= 3 }, eventually, time.Millisecond, "every worker went idle")

	q.addEvent("late", "push")
	p.Notify()
	select {
	case id := <-handled:
		assert.Equal(t, "late", id)
	case <-time.After(eventually):
		t.Fatal("Notify did not wake the pool")
	}
}

func TestEnqueueWakesWorkers(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: time.Hour})
	handled := make(chan struct{}, 1)
	p.OnJob(jobReconcile, func(context.Context, store.Job) error { handled <- struct{}{}; return nil })
	start(t, p)
	require.Eventually(t, func() bool { return q.claimCount() >= 1 }, eventually, time.Millisecond)

	require.NoError(t, p.Enqueue(t.Context(), jobReconcile, nil, time.Time{}, "reconcile:1"))
	select {
	case <-handled:
	case <-time.After(eventually):
		t.Fatal("Enqueue did not wake the pool")
	}
}

func TestEnqueuePayload(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{})
	ctx := t.Context()

	require.NoError(t, p.Enqueue(ctx, jobScheduleDrift, map[string]int64{"repo_id": 42}, time.Time{}, "schedule_drift:42:1"))
	require.NoError(t, p.Enqueue(ctx, jobScheduleDrift, map[string]int64{"repo_id": 43}, time.Time{}, "schedule_drift:42:1"))
	require.NoError(t, p.Enqueue(ctx, jobCrossRepoPlan, json.RawMessage(`{"stack":"a"}`), time.Time{}, ""))
	require.NoError(t, p.Enqueue(ctx, jobStaleLocks, nil, time.Time{}, ""))
	later := time.Now().Add(time.Hour)
	require.NoError(t, p.Enqueue(ctx, jobDriftStack, struct {
		StackID string `json:"stack_id"`
	}{"s"}, later, ""))

	require.Len(t, q.jobs, 4, "the duplicate dedupe key is dropped")
	assert.JSONEq(t, `{"repo_id":42}`, string(q.job(jobScheduleDrift).Payload))
	assert.JSONEq(t, `{"stack":"a"}`, string(q.job(jobCrossRepoPlan).Payload))
	assert.Nil(t, q.job(jobStaleLocks).Payload, "nil payloads are stored as the store's default")
	assert.JSONEq(t, `{"stack_id":"s"}`, string(q.job(jobDriftStack).Payload))
	assert.True(t, q.job(jobDriftStack).RunAfter.Equal(later))

	err := p.Enqueue(ctx, jobPrune, make(chan int), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker: enqueue prune: marshal payload")
}

func TestGracefulShutdownWaitsForInFlightHandler(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, ClaimBatch: 2, PollInterval: time.Hour})
	started := make(chan struct{})
	var finished atomic.Bool
	var calls atomic.Int32
	p.OnEvent("workflow_run", func(ctx context.Context, _ store.Event) error {
		calls.Add(1)
		close(started)
		time.Sleep(200 * time.Millisecond)
		finished.Store(ctx.Err() == nil)
		return nil
	})
	q.addEvent("in-flight", "workflow_run")
	q.addEvent("not-started", "workflow_run")
	stop := start(t, p)
	<-started
	require.NoError(t, stop(), "a drain within the timeout is clean")

	assert.True(t, finished.Load(), "the in-flight handler ran to completion with a live context")
	assert.Equal(t, int32(1), calls.Load())
	assert.NotNil(t, q.event("in-flight").DoneAt)
	rest := q.event("not-started")
	assert.Nil(t, rest.ClaimedAt, "the unstarted row of the batch is handed back")
	assert.Nil(t, rest.DoneAt)
	assert.Zero(t, rest.Attempts, "handing it back costs no attempt")
}

func TestDrainTimeoutCancelsHandlers(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: time.Hour, DrainTimeout: 50 * time.Millisecond})
	started := make(chan struct{})
	p.OnJob(jobDispatchWave, func(ctx context.Context, _ store.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	require.NoError(t, p.Enqueue(t.Context(), jobDispatchWave, nil, time.Time{}, ""))
	stop := start(t, p)
	<-started
	err := stop()
	require.ErrorIs(t, err, worker.ErrDrainTimeout)

	job := q.job(jobDispatchWave)
	assert.Nil(t, job.DoneAt)
	assert.Equal(t, 1, job.Attempts)
	assert.Equal(t, []time.Duration{0}, q.backoffs(), "a row cancelled by shutdown is due again at once")
}

func TestRunTwice(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1})
	stop := start(t, p)
	require.Eventually(t, func() bool { return q.claimCount() >= 1 }, eventually, time.Millisecond)
	require.ErrorIs(t, p.Run(t.Context()), worker.ErrRunning)
	require.NoError(t, stop())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, p.Run(ctx), "a stopped pool can run again")
}

func TestStaleBatchRemainderIsReleased(t *testing.T) {
	q := &fakeQueue{}
	var offset atomic.Int64
	clock := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	p := worker.New(q, worker.Options{
		Workers:        1,
		ClaimBatch:     3,
		PollInterval:   5 * time.Millisecond,
		HandlerTimeout: time.Minute,
		StaleClaimAge:  3 * time.Minute,
		Clock:          clock,
	})
	var mu sync.Mutex
	var order []string
	p.OnEvent("push", func(_ context.Context, ev store.Event) error {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, ev.ID)
		if ev.ID == "slow" {
			offset.Add(int64(2*time.Minute + time.Second))
		}
		return nil
	})
	q.addEvent("slow", "push")
	q.addEvent("second", "push")
	q.addEvent("third", "push")
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"slow", "second", "third"}, order, "each row runs exactly once")
	assert.NotEmpty(t, q.releases(), "rows whose claim would outlive the stale age are handed back, not started")
	assert.Zero(t, q.event("second").Attempts)
}

func TestMaintenance(t *testing.T) {
	q := &fakeQueue{}
	m := metrics.New()
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: time.Hour, StaleClaimAge: time.Minute, Metrics: m})
	handled := make(chan string, 1)
	p.OnEvent("push", func(_ context.Context, ev store.Event) error { handled <- ev.ID; return nil })
	q.addEvent("orphan", "push", func(ev *store.Event) {
		claimed := time.Now().Add(-time.Hour)
		ev.ClaimedBy, ev.ClaimedAt = "dead-host:1:0", &claimed
	})
	start(t, p)

	select {
	case id := <-handled:
		assert.Equal(t, "orphan", id, "a claim left by a dead worker is released and run")
	case <-time.After(eventually):
		t.Fatal("stale claim was not released")
	}
	assert.Equal(t, 1, q.event("orphan").Attempts, "the lost claim counts as an attempt")
	require.Eventually(t, func() bool {
		samples := metricstest.Scrape(t, m)
		_, events := samples[`stackorder_queue_depth{queue="events"}`]
		_, jobs := samples[`stackorder_queue_depth{queue="jobs"}`]
		return events && jobs
	}, eventually, 5*time.Millisecond, "queue depth gauges are published")
}

func TestWebhookLag(t *testing.T) {
	q := &fakeQueue{}
	m := metrics.New()
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: 5 * time.Millisecond, Backoff: fastBackoff, Metrics: m})
	var calls atomic.Int32
	p.OnEvent("push", func(context.Context, store.Event) error {
		if calls.Add(1) == 1 {
			return errors.New("retry me")
		}
		return nil
	})
	q.addEvent("lagging", "push", func(ev *store.Event) { ev.ReceivedAt = ev.ReceivedAt.Add(-2 * time.Second) })
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)

	samples := metricstest.Scrape(t, m)
	assert.InDelta(t, 1, samples["stackorder_webhook_lag_seconds_count"], 0, "only the first claim measures lag")
	assert.InDelta(t, 2, samples["stackorder_webhook_lag_seconds_sum"], 0.5)
}

func TestNilHandlerPanics(t *testing.T) {
	p := worker.New(&fakeQueue{}, worker.Options{})
	assert.PanicsWithValue(t, "worker: nil event handler for push", func() { p.OnEvent("push", nil) })
	assert.PanicsWithValue(t, "worker: nil job handler for prune", func() { p.OnJob("prune", nil) })
}

func TestHandlerReplacement(t *testing.T) {
	q := &fakeQueue{}
	p := worker.New(q, worker.Options{Workers: 1, PollInterval: 5 * time.Millisecond})
	var got atomic.Value
	p.OnJob(jobPrune, func(context.Context, store.Job) error { got.Store("first"); return nil })
	p.OnJob(jobPrune, func(context.Context, store.Job) error { got.Store("second"); return nil })
	require.NoError(t, p.Enqueue(t.Context(), jobPrune, nil, time.Time{}, ""))
	start(t, p)
	require.Eventually(t, q.allDone, eventually, 5*time.Millisecond)
	assert.Equal(t, "second", got.Load())
	assert.Regexp(t, `^.+:\d+:\d+$`, q.job(jobPrune).ClaimedBy, "workers claim as host:pid:n")
}
