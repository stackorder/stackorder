//go:build integration

package store_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestTryAdvisoryLockExclusive(t *testing.T) {
	ctx := t.Context()
	first := pgtest.New(t)
	second, err := store.Open(ctx, first.Pool().Config().ConnString())
	require.NoError(t, err)
	t.Cleanup(second.Close)

	lock, ok, err := first.TryAdvisoryLock(ctx, store.SchedulerLockKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, lock.Held(ctx))

	steps := []struct {
		name string
		s    *store.Store
		key  int64
		want bool
	}{
		{"other server is refused", second, store.SchedulerLockKey, false},
		{"same server, second connection, is refused", first, store.SchedulerLockKey, false},
		{"another key is free", second, store.SchedulerLockKey + 1, true},
	}
	for _, st := range steps {
		l, got, err := st.s.TryAdvisoryLock(ctx, st.key)
		require.NoError(t, err, st.name)
		assert.Equal(t, st.want, got, st.name)
		assert.Equal(t, st.want, l.Held(ctx), st.name)
		l.Release()
	}
	assert.True(t, lock.Held(ctx), "refused attempts leave the holder in place")

	lock.Release()
	lock.Release()
	assert.False(t, lock.Held(ctx), "a released lock is not held")
	next, ok, err := second.TryAdvisoryLock(ctx, store.SchedulerLockKey)
	require.NoError(t, err)
	assert.True(t, ok, "released lock can be taken over")
	next.Release()
}

func TestTryAdvisoryLockLostConnection(t *testing.T) {
	ctx := t.Context()
	s := pgtest.New(t)
	lock, ok, err := s.TryAdvisoryLock(ctx, store.SchedulerLockKey)
	require.NoError(t, err)
	require.True(t, ok)
	t.Cleanup(lock.Release)

	var terminated int
	require.NoError(t, s.Pool().QueryRow(ctx, `
		SELECT count(pg_terminate_backend(pid)) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND ((classid::bigint << 32) | objid::bigint) = $1
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`,
		store.SchedulerLockKey).Scan(&terminated))
	require.Equal(t, 1, terminated)

	require.Eventually(t, func() bool { return !lock.Held(ctx) }, 10*time.Second, 50*time.Millisecond,
		"a leader whose session died must learn that it lost the lock")
	next, ok, err := s.TryAdvisoryLock(ctx, store.SchedulerLockKey)
	require.NoError(t, err)
	assert.True(t, ok, "another server can lead once the session is gone")
	assert.True(t, next.Held(ctx))
	next.Release()
}

func TestTryAdvisoryLockSingleLeader(t *testing.T) {
	ctx := t.Context()
	s := pgtest.New(t)
	const candidates = 6
	var (
		wg       sync.WaitGroup
		leaders  atomic.Int32
		releases = make(chan *store.AdvisoryLock, candidates)
	)
	for range candidates {
		wg.Go(func() {
			lock, ok, err := s.TryAdvisoryLock(ctx, store.SchedulerLockKey)
			if !assert.NoError(t, err) {
				return
			}
			if ok {
				leaders.Add(1)
			}
			releases <- lock
		})
	}
	wg.Wait()
	close(releases)
	for lock := range releases {
		lock.Release()
	}
	assert.Equal(t, int32(1), leaders.Load())
}
