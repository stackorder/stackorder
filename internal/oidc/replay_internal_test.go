package oidc

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestMemoryJTIStore(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	s := NewMemoryJTIStore(clock.Now)
	ctx := t.Context()
	seen := func(jti string, ttl time.Duration) bool {
		t.Helper()
		got, err := s.SeenJTI(ctx, jti, clock.Now().Add(ttl))
		require.NoError(t, err)
		return got
	}

	assert.False(t, seen("a", 5*time.Minute))
	assert.True(t, seen("a", 5*time.Minute))
	assert.False(t, seen("b", 10*time.Minute))
	assert.Equal(t, 2, s.Len())

	clock.Advance(5 * time.Minute)
	assert.True(t, seen("a", time.Minute), "an entry is kept up to and including its expiry")

	clock.Advance(time.Second)
	assert.Equal(t, 2, s.Len(), "pruning waits for the prune interval")
	assert.False(t, seen("a", time.Minute), "an expired entry no longer counts")
	assert.True(t, seen("a", time.Minute))

	clock.Advance(pruneInterval)
	assert.False(t, seen("c", time.Minute))
	assert.Equal(t, 3, s.Len())

	clock.Advance(10 * time.Minute)
	assert.False(t, seen("d", time.Minute))
	assert.Equal(t, 1, s.Len(), "expired entries are pruned")
}

func TestMemoryJTIStoreZeroValueUsesWallClock(t *testing.T) {
	var s MemoryJTIStore
	ctx := t.Context()
	first, err := s.SeenJTI(ctx, "a", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, first)
	again, err := s.SeenJTI(ctx, "a", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, again)
	expired, err := s.SeenJTI(ctx, "b", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	assert.False(t, expired)
	expiredAgain, err := s.SeenJTI(ctx, "b", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, expiredAgain, "an entry recorded already expired is not a replay")
}

func TestMemoryJTIStoreConcurrentReplay(t *testing.T) {
	s := NewMemoryJTIStore(nil)
	exp := time.Now().Add(time.Minute)
	for round := range 10 {
		jti := "jti-" + strconv.Itoa(round)
		var fresh atomic.Int32
		var wg sync.WaitGroup
		for range 32 {
			wg.Go(func() {
				seen, err := s.SeenJTI(t.Context(), jti, exp)
				assert.NoError(t, err)
				if !seen {
					fresh.Add(1)
				}
			})
		}
		wg.Wait()
		assert.Equal(t, int32(1), fresh.Load(), "exactly one concurrent caller may use %s", jti)
	}
}
