package sched

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
)

type call struct {
	kind     string
	payload  string
	runAfter time.Time
	key      string
}

type recorder struct {
	mu    sync.Mutex
	calls []call
	fail  func(key string) error
}

func (r *recorder) Enqueue(_ context.Context, kind string, payload any, runAfter time.Time, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(key); err != nil {
			return err
		}
	}
	raw := ""
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	r.calls = append(r.calls, call{kind: kind, payload: raw, runAfter: runAfter, key: key})
	return nil
}

func (r *recorder) keys(kind string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if c.kind == kind {
			out = append(out, c.key)
		}
	}
	return out
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type repos struct {
	list []store.Repo
	err  error
}

func (r *repos) ListRepos(context.Context, ...string) ([]store.Repo, error) {
	return r.list, r.err
}

func repo(id int64, schedule string) store.Repo {
	return store.Repo{ID: id, FullName: fmt.Sprintf("acme/r%d", id), Config: &v1.RepoConfig{Drift: v1.DriftConfig{Schedule: schedule}}}
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func newTestScheduler(rs *repos, q Enqueuer, logger *slog.Logger) *Scheduler {
	return newScheduler(rs, func(context.Context) (leaderLock, bool, error) { return nil, false, nil }, q, Options{Logger: logger})
}

func tickEvery(t *testing.T, s *Scheduler, from, to time.Time, step time.Duration) {
	t.Helper()
	last := from
	for now := from.Add(step); !now.After(to); now = now.Add(step) {
		require.NoError(t, s.enqueueDue(t.Context(), last, now))
		last = now
	}
}

func unix(ts ...string) []string {
	out := make([]string, len(ts))
	for i, s := range ts {
		out[i] = fmt.Sprint(at(s).Unix())
	}
	return out
}

func prefixed(prefix string, vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = prefix + v
	}
	return out
}

func TestHousekeepingSlots(t *testing.T) {
	q := &recorder{}
	s := newTestScheduler(&repos{}, q, nil)
	tickEvery(t, s, at("2026-09-28T07:58:10Z"), at("2026-09-28T09:01:10Z"), 30*time.Second)

	reconcile := q.keys(runs.JobReconcile)
	assert.Len(t, reconcile, 63, "one reconcile per minute from 07:59 to 09:01")
	assert.Equal(t, "reconcile:"+fmt.Sprint(at("2026-09-28T07:59:00Z").Unix()), reconcile[0])
	assert.Equal(t, prefixed("prune:", unix("2026-09-28T08:00:00Z", "2026-09-28T09:00:00Z")), q.keys(runs.JobPrune))
	assert.Equal(t, prefixed("stale_locks:", unix("2026-09-28T08:00:00Z")), q.keys(runs.JobStaleLocks))
	assert.Empty(t, q.keys(runs.JobSyncInstallations), "installations are synced at 04:00 only")
	seen := map[string]bool{}
	for _, c := range q.calls {
		assert.False(t, seen[c.key], "slot %s enqueued twice", c.key)
		seen[c.key] = true
		assert.Empty(t, c.payload)
	}
}

func TestInstallationsSyncDaily(t *testing.T) {
	q := &recorder{}
	s := newTestScheduler(&repos{}, q, nil)
	tickEvery(t, s, at("2026-09-27T03:58:40Z"), at("2026-09-28T04:01:10Z"), 30*time.Second)

	assert.Equal(t, prefixed("sync_installations:", unix("2026-09-27T04:00:00Z", "2026-09-28T04:00:00Z")),
		q.keys(runs.JobSyncInstallations))
	for _, c := range q.calls {
		if c.kind == runs.JobSyncInstallations {
			assert.Empty(t, c.payload)
		}
	}
}

func TestDriftFiresOncePerScheduledMinute(t *testing.T) {
	q := &recorder{}
	s := newTestScheduler(&repos{list: []store.Repo{repo(7, "*/5 * * * *")}}, q, nil)
	tickEvery(t, s, at("2026-09-28T10:00:15Z"), at("2026-09-28T10:30:15Z"), 30*time.Second)

	want := prefixed("schedule_drift:7:", unix(
		"2026-09-28T10:05:00Z", "2026-09-28T10:10:00Z", "2026-09-28T10:15:00Z",
		"2026-09-28T10:20:00Z", "2026-09-28T10:25:00Z", "2026-09-28T10:30:00Z"))
	assert.Equal(t, want, q.keys(runs.JobScheduleDrift))
	for _, c := range q.calls {
		if c.kind == runs.JobScheduleDrift {
			assert.JSONEq(t, `{"repo_id":7}`, c.payload)
			assert.Equal(t, c.key, fmt.Sprintf("schedule_drift:7:%d", c.runAfter.Unix()), "runAfter is the fire time")
		}
	}
}

func TestDriftWindowBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		schedule string
		last     string
		now      string
		want     []string
	}{
		{"fire on now is due", "0 6 * * *", "2026-09-28T05:59:30Z", "2026-09-28T06:00:00Z", unix("2026-09-28T06:00:00Z")},
		{"fire on last tick was already due", "0 6 * * *", "2026-09-28T06:00:00Z", "2026-09-28T06:00:30Z", nil},
		{"not yet", "0 6 * * *", "2026-09-28T05:59:00Z", "2026-09-28T05:59:30Z", nil},
		{"weekday schedule skips sunday", "0 6 * * 1-5", "2026-09-27T05:59:30Z", "2026-09-27T06:00:30Z", nil},
		{"weekday schedule on monday", "0 6 * * 1-5", "2026-09-28T05:59:30Z", "2026-09-28T06:00:30Z", unix("2026-09-28T06:00:00Z")},
		{"missed tick is caught up", "0 6 * * *", "2026-09-28T05:59:40Z", "2026-09-28T06:03:10Z", unix("2026-09-28T06:00:00Z")},
		{"missed fires coalesce into the latest", "* * * * *", "2026-09-28T06:00:30Z", "2026-09-28T06:05:10Z", unix("2026-09-28T06:05:00Z")},
		{"catch up looks back one hour at most", "0 * * * *", "2026-09-28T01:00:30Z", "2026-09-28T06:30:00Z", unix("2026-09-28T06:00:00Z")},
		{"a fire older than an hour is dropped", "0 6 * * *", "2026-09-28T05:30:00Z", "2026-09-28T07:30:00Z", nil},
		{"CRON_TZ is honoured", "CRON_TZ=Europe/Paris 0 6 * * *", "2026-09-28T03:59:30Z", "2026-09-28T04:00:30Z", unix("2026-09-28T04:00:00Z")},
		{"impossible date never fires", "0 0 30 2 *", "2026-09-28T05:00:00Z", "2026-09-28T06:00:00Z", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &recorder{}
			s := newTestScheduler(&repos{list: []store.Repo{repo(3, tc.schedule)}}, q, nil)
			require.NoError(t, s.enqueueDue(t.Context(), at(tc.last), at(tc.now)))
			var want []string
			if tc.want != nil {
				want = prefixed("schedule_drift:3:", tc.want)
			}
			assert.Equal(t, want, q.keys(runs.JobScheduleDrift))
		})
	}
}

func TestReposWithoutUsableSchedule(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	suspended := repo(4, "* * * * *")
	suspended.Suspended = true
	rs := &repos{list: []store.Repo{
		{ID: 1, FullName: "acme/no-config"},
		repo(2, ""),
		repo(3, "61 * * * *"),
		suspended,
		repo(5, "* * * * *"),
	}}
	q := &recorder{}
	s := newTestScheduler(rs, q, logger)
	tickEvery(t, s, at("2026-09-28T10:00:15Z"), at("2026-09-28T10:03:15Z"), 30*time.Second)

	keys := q.keys(runs.JobScheduleDrift)
	assert.Len(t, keys, 3)
	for _, k := range keys {
		assert.True(t, strings.HasPrefix(k, "schedule_drift:5:"), k)
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "ignoring invalid drift.schedule"), "an invalid schedule is reported once, not every tick")
	assert.Contains(t, logs.String(), "acme/r3")

	rs.list[2] = repo(3, "30 10 * * *")
	require.NoError(t, s.enqueueDue(t.Context(), at("2026-09-28T10:29:45Z"), at("2026-09-28T10:30:15Z")))
	assert.Contains(t, q.keys(runs.JobScheduleDrift), fmt.Sprintf("schedule_drift:3:%d", at("2026-09-28T10:30:00Z").Unix()),
		"a corrected schedule is picked up")
}

func TestTickErrors(t *testing.T) {
	q := &recorder{fail: func(key string) error {
		if strings.HasPrefix(key, "prune:") {
			return errors.New("database is down")
		}
		return nil
	}}
	s := newTestScheduler(&repos{list: []store.Repo{repo(1, "0 * * * *")}}, q, nil)
	err := s.enqueueDue(t.Context(), at("2026-09-28T09:59:50Z"), at("2026-09-28T10:00:20Z"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sched: enqueue prune:")
	assert.Len(t, q.keys(runs.JobScheduleDrift), 1, "one failure does not stop the other slots")

	listErr := errors.New("connection reset")
	s = newTestScheduler(&repos{err: listErr}, &recorder{}, nil)
	require.ErrorIs(t, s.enqueueDue(t.Context(), at("2026-09-28T09:59:50Z"), at("2026-09-28T10:00:20Z")), listErr)
}

type fakeLock struct {
	held     atomic.Bool
	released atomic.Int32
}

func (l *fakeLock) Held(context.Context) bool { return l.held.Load() }

func (l *fakeLock) Release() {
	l.held.Store(false)
	l.released.Add(1)
}

type fakeElection struct {
	mu       sync.Mutex
	free     bool
	err      error
	attempts int
	locks    []*fakeLock
}

func (e *fakeElection) try(context.Context) (leaderLock, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attempts++
	if e.err != nil {
		return nil, false, e.err
	}
	if !e.free {
		return nil, false, nil
	}
	l := &fakeLock{}
	l.held.Store(true)
	e.locks = append(e.locks, l)
	e.free = false
	return l, true, nil
}

func (e *fakeElection) set(free bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.free, e.err = free, err
}

func (e *fakeElection) snapshot() (attempts int, locks []*fakeLock) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempts, append([]*fakeLock(nil), e.locks...)
}

func runScheduler(t *testing.T, s *Scheduler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("scheduler did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestLeadership(t *testing.T) {
	election := &fakeElection{}
	q := &recorder{}
	m := metrics.New()
	var clock atomic.Int64
	clock.Store(at("2026-09-28T10:00:30Z").UnixNano())
	s := newScheduler(&repos{}, election.try, q, Options{
		Tick:    5 * time.Millisecond,
		Clock:   func() time.Time { return time.Unix(0, clock.Load()) },
		Metrics: m,
	})
	stop := runScheduler(t, s)

	require.Eventually(t, func() bool { a, _ := election.snapshot(); return a >= 3 }, 5*time.Second, time.Millisecond)
	assert.False(t, s.Leader())
	assert.Zero(t, q.count(), "a follower enqueues nothing")

	election.set(true, nil)
	require.Eventually(t, s.Leader, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return len(q.keys(runs.JobReconcile)) == 1 }, 5*time.Second, time.Millisecond,
		"a new leader catches up at once")
	assert.Equal(t, prefixed("reconcile:", unix("2026-09-28T10:00:00Z")), q.keys(runs.JobReconcile))
	assert.Equal(t, prefixed("prune:", unix("2026-09-28T10:00:00Z")), q.keys(runs.JobPrune))
	assert.InDelta(t, 1, metricstest.Value(t, m, "stackorder_scheduler_leader"), 0)

	clock.Store(at("2026-09-28T10:01:05Z").UnixNano())
	require.Eventually(t, func() bool { return len(q.keys(runs.JobReconcile)) == 2 }, 5*time.Second, time.Millisecond)

	_, locks := election.snapshot()
	require.Len(t, locks, 1)
	locks[0].held.Store(false)
	require.Eventually(t, func() bool { return !s.Leader() }, 5*time.Second, time.Millisecond, "a lost lock ends the lead")
	assert.InDelta(t, 0, metricstest.Value(t, m, "stackorder_scheduler_leader"), 0)
	assert.Equal(t, int32(1), locks[0].released.Load(), "the dead lock is released before trying again")

	election.set(true, nil)
	require.Eventually(t, s.Leader, 5*time.Second, time.Millisecond, "the lead is taken again")
	stop()
	assert.False(t, s.Leader())
	_, locks = election.snapshot()
	require.Len(t, locks, 2)
	assert.Equal(t, int32(1), locks[1].released.Load(), "stopping releases the lock")
	distinct := map[string]bool{}
	for _, k := range q.keys(runs.JobReconcile) {
		distinct[k] = true
	}
	assert.Len(t, distinct, 2, "a new lead offers slots again under the same keys, which the queue deduplicates")
}

func TestLockErrorsAreRetried(t *testing.T) {
	election := &fakeElection{err: errors.New("dial tcp: connection refused")}
	var logs bytes.Buffer
	s := newScheduler(&repos{}, election.try, &recorder{}, Options{
		Tick:   5 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	runScheduler(t, s)
	require.Eventually(t, func() bool { a, _ := election.snapshot(); return a >= 3 }, 5*time.Second, time.Millisecond)
	assert.False(t, s.Leader())
	election.set(true, nil)
	require.Eventually(t, s.Leader, 5*time.Second, time.Millisecond)
}

func TestFailedTickIsRetried(t *testing.T) {
	election := &fakeElection{free: true}
	var failures atomic.Int32
	q := &recorder{fail: func(key string) error {
		if strings.HasPrefix(key, "reconcile:") && failures.Add(1) <= 2 {
			return errors.New("database is down")
		}
		return nil
	}}
	s := newScheduler(&repos{}, election.try, q, Options{
		Tick:  5 * time.Millisecond,
		Clock: func() time.Time { return at("2026-09-28T10:00:30Z") },
	})
	runScheduler(t, s)
	require.Eventually(t, func() bool { return len(q.keys(runs.JobReconcile)) == 1 }, 5*time.Second, time.Millisecond,
		"the window is kept until every slot in it was enqueued")
}

func TestRunTwice(t *testing.T) {
	election := &fakeElection{}
	s := newScheduler(&repos{}, election.try, &recorder{}, Options{Tick: 5 * time.Millisecond})
	stop := runScheduler(t, s)
	require.Eventually(t, func() bool { a, _ := election.snapshot(); return a >= 1 }, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, s.Run(t.Context()), ErrRunning)
	stop()
}
