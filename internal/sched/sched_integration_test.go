//go:build integration

package sched_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/sched"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
	"github.com/stackorder/stackorder/internal/worker"
)

const (
	tick    = 50 * time.Millisecond
	waitFor = 10 * time.Second
)

var _ sched.Enqueuer = (*worker.Pool)(nil)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

type instance struct {
	st      *store.Store
	s       *sched.Scheduler
	metrics *metrics.Registry
	stop    func()
}

func newInstance(t *testing.T, dsn string) *instance {
	t.Helper()
	st, err := store.Open(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(st.Close)
	m := metrics.New()
	return &instance{st: st, metrics: m, s: sched.New(st, worker.New(st, worker.Options{}), sched.Options{Tick: tick, Metrics: m})}
}

func (in *instance) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- in.s.Run(ctx) }()
	var once sync.Once
	in.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				assert.NoError(t, err)
			case <-time.After(waitFor):
				t.Error("scheduler did not stop")
			}
		})
	}
	t.Cleanup(in.stop)
}

func leaderPID(t *testing.T, st *store.Store) int {
	t.Helper()
	var pid int
	err := st.Pool().QueryRow(t.Context(), `
		SELECT COALESCE(max(pid), 0) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND ((classid::bigint << 32) | objid::bigint) = $1`,
		store.SchedulerLockKey).Scan(&pid)
	require.NoError(t, err)
	return pid
}

func TestPGOneLeaderAndTakeover(t *testing.T) {
	dsn := pgtest.New(t).Pool().Config().ConnString()
	first, second := newInstance(t, dsn), newInstance(t, dsn)

	first.start(t)
	require.Eventually(t, first.s.Leader, waitFor, 5*time.Millisecond)
	second.start(t)
	for range 10 {
		time.Sleep(tick)
		require.True(t, first.s.Leader())
		require.False(t, second.s.Leader(), "only one instance leads")
	}
	assert.InDelta(t, 1, metricstest.Value(t, first.metrics, "stackorder_scheduler_leader"), 0)
	assert.InDelta(t, 0, metricstest.Value(t, second.metrics, "stackorder_scheduler_leader"), 0)

	first.stop()
	assert.False(t, first.s.Leader())
	require.Eventually(t, second.s.Leader, waitFor, 5*time.Millisecond, "the follower takes over")
	assert.InDelta(t, 1, metricstest.Value(t, second.metrics, "stackorder_scheduler_leader"), 0)

	var jobs, keys int
	require.NoError(t, second.st.Pool().QueryRow(t.Context(),
		`SELECT count(*), count(DISTINCT dedupe_key) FROM jobs WHERE kind = 'reconcile'`).Scan(&jobs, &keys))
	assert.Positive(t, jobs)
	assert.Equal(t, keys, jobs, "the handover enqueued no slot twice")
	assert.LessOrEqual(t, jobs, 2, "one reconcile per minute")
}

func TestPGLostLockIsRetaken(t *testing.T) {
	dsn := pgtest.New(t).Pool().Config().ConnString()
	in := newInstance(t, dsn)
	in.start(t)
	require.Eventually(t, in.s.Leader, waitFor, 5*time.Millisecond)
	before := leaderPID(t, in.st)
	require.NotZero(t, before)

	_, err := in.st.Pool().Exec(t.Context(), `SELECT pg_terminate_backend($1)`, before)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		pid := leaderPID(t, in.st)
		return pid != 0 && pid != before && in.s.Leader()
	}, waitFor, 5*time.Millisecond, "the lead is dropped with the connection and taken again")
}

func TestPGDriftScheduleFromStoredConfig(t *testing.T) {
	st := pgtest.New(t)
	ctx := t.Context()
	_, err := st.UpsertInstallation(ctx, store.Installation{ID: 1, Account: "acme", AccountType: "Organization"})
	require.NoError(t, err)
	r, err := st.UpsertRepo(ctx, store.RepoParams{ID: 42, InstallationID: 1, FullName: "acme/infra", DefaultBranch: "main"})
	require.NoError(t, err)
	require.NoError(t, st.UpdateRepoConfig(ctx, r.ID, &v1.RepoConfig{Version: 1, Drift: v1.DriftConfig{Schedule: "* * * * *"}}, "sha"))
	_, err = st.UpsertRepo(ctx, store.RepoParams{ID: 43, InstallationID: 1, FullName: "acme/no-drift", DefaultBranch: "main"})
	require.NoError(t, err)

	in := newInstance(t, st.Pool().Config().ConnString())
	in.start(t)
	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, st.Pool().QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'schedule_drift'`).Scan(&n))
		return n >= 1
	}, waitFor, 10*time.Millisecond)
	in.stop()

	jobs, err := st.ClaimJobs(ctx, "probe", 100)
	require.NoError(t, err)
	var drift []store.Job
	for _, j := range jobs {
		if j.Kind == "schedule_drift" {
			drift = append(drift, j)
		}
	}
	require.NotEmpty(t, drift)
	for _, j := range drift {
		assert.JSONEq(t, `{"repo_id":42}`, string(j.Payload))
		assert.Regexp(t, `^schedule_drift:42:\d+$`, j.DedupeKey)
		assert.Zero(t, j.RunAfter.Second(), "the job is due at the fire time")
	}
}
