package oidc

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const pruneInterval = time.Minute

// JTIStore remembers the jti of every token accepted by VerifyOnce.
type JTIStore interface {
	// SeenJTI records jti as used until exp and reports whether it had
	// already been recorded and not yet expired. The check and the record
	// must be atomic, so that two concurrent calls with the same jti never
	// both return false. The store may forget jti once exp has passed.
	SeenJTI(ctx context.Context, jti string, exp time.Time) (bool, error)
}

// VerifyOnce is Verify followed by a replay check: a token without a jti is
// rejected with ErrMalformed, and one whose jti store has already seen is
// rejected with ErrReplay. The jti is retained until the last instant at
// which Verify would still accept the token, so a runner must request a
// fresh token for every call it makes.
func (v *Verifier) VerifyOnce(ctx context.Context, raw string, store JTIStore) (*Claims, error) {
	c, err := v.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	if c.ID == "" {
		return nil, fmt.Errorf("%w: missing jti", ErrMalformed)
	}
	until := c.ExpiresAt.Add(ClockSkew)
	if limit := c.IssuedAt.Add(v.cfg.MaxAge); limit.Before(until) {
		until = limit
	}
	seen, err := store.SeenJTI(ctx, c.ID, until)
	if err != nil {
		return nil, fmt.Errorf("oidc: recording jti: %w", err)
	}
	if seen {
		return nil, fmt.Errorf("%w: jti %q was already used", ErrReplay, c.ID)
	}
	return c, nil
}

// MemoryJTIStore is an in-process JTIStore for tests and single-instance
// servers. Expired entries are pruned at most once a minute, during
// SeenJTI. The zero value is ready to use with the wall clock.
type MemoryJTIStore struct {
	clock func() time.Time

	mu        sync.Mutex
	seen      map[string]time.Time
	nextPrune time.Time
}

// NewMemoryJTIStore returns an empty store that reads the time from clock,
// or from time.Now when clock is nil.
func NewMemoryJTIStore(clock func() time.Time) *MemoryJTIStore {
	return &MemoryJTIStore{clock: clock}
}

// SeenJTI implements JTIStore.
func (s *MemoryJTIStore) SeenJTI(_ context.Context, jti string, exp time.Time) (bool, error) {
	now := time.Now()
	if s.clock != nil {
		now = s.clock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[string]time.Time)
	}
	if !now.Before(s.nextPrune) {
		for k, until := range s.seen {
			if until.Before(now) {
				delete(s.seen, k)
			}
		}
		s.nextPrune = now.Add(pruneInterval)
	}
	if until, ok := s.seen[jti]; ok && !until.Before(now) {
		return true, nil
	}
	s.seen[jti] = exp
	return false, nil
}

// Len returns the number of jtis currently retained, expired ones not yet
// pruned included.
func (s *MemoryJTIStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}
