//go:build integration

package store_test

import (
	"sync"
	"sync/atomic"
	"testing"

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

	release, ok, err := first.TryAdvisoryLock(ctx, store.SchedulerLockKey)
	require.NoError(t, err)
	require.True(t, ok)

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
		rel, got, err := st.s.TryAdvisoryLock(ctx, st.key)
		require.NoError(t, err, st.name)
		assert.Equal(t, st.want, got, st.name)
		rel()
	}

	release()
	release()
	rel, ok, err := second.TryAdvisoryLock(ctx, store.SchedulerLockKey)
	require.NoError(t, err)
	assert.True(t, ok, "released lock can be taken over")
	rel()
}

func TestTryAdvisoryLockSingleLeader(t *testing.T) {
	ctx := t.Context()
	s := pgtest.New(t)
	const candidates = 6
	var (
		wg       sync.WaitGroup
		leaders  atomic.Int32
		releases = make(chan func(), candidates)
	)
	for range candidates {
		wg.Go(func() {
			rel, ok, err := s.TryAdvisoryLock(ctx, store.SchedulerLockKey)
			if !assert.NoError(t, err) {
				return
			}
			if ok {
				leaders.Add(1)
			}
			releases <- rel
		})
	}
	wg.Wait()
	close(releases)
	for rel := range releases {
		rel()
	}
	assert.Equal(t, int32(1), leaders.Load())
}
