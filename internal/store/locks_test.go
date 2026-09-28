//go:build integration

package store_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func lockedStacks(t *testing.T, f *fixture) map[uuid.UUID]uuid.UUID {
	t.Helper()
	locks, err := f.s.ListLocks(f.ctx, f.repo.ID)
	require.NoError(t, err)
	out := map[uuid.UUID]uuid.UUID{}
	for _, l := range locks {
		out[l.StackID] = l.RunID
	}
	return out
}

func TestTryLockStacksAtomic(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b", "stacks/c")
	a, b, c := ids["stacks/a"], ids["stacks/b"], ids["stacks/c"]
	pr1 := f.run(store.CreateRunParams{PRNumber: 1})
	pr1Again := f.run(store.CreateRunParams{PRNumber: 1, SHA: "new"})
	pr2 := f.run(store.CreateRunParams{PRNumber: 2})
	manual := f.run(store.CreateRunParams{Trigger: v1.TriggerManual, Mode: v1.ModeApply})

	conflicts, err := f.s.TryLockStacks(f.ctx, []uuid.UUID{b}, pr1.ID, 1, "apply")
	require.NoError(t, err)
	require.Empty(t, conflicts)

	cases := []struct {
		name          string
		stacks        []uuid.UUID
		run           store.Run
		pr            int
		wantConflicts []string
		wantLocks     map[uuid.UUID]uuid.UUID
	}{
		{
			name:          "other pr conflicts and locks nothing",
			stacks:        []uuid.UUID{a, b, c},
			run:           pr2,
			pr:            2,
			wantConflicts: []string{"stacks/b"},
			wantLocks:     map[uuid.UUID]uuid.UUID{b: pr1.ID},
		},
		{
			name:          "run without pr conflicts with a pr lock",
			stacks:        []uuid.UUID{b},
			run:           manual,
			wantConflicts: []string{"stacks/b"},
			wantLocks:     map[uuid.UUID]uuid.UUID{b: pr1.ID},
		},
		{
			name:      "same run is idempotent",
			stacks:    []uuid.UUID{a, b, a},
			run:       pr1,
			pr:        1,
			wantLocks: map[uuid.UUID]uuid.UUID{a: pr1.ID, b: pr1.ID},
		},
		{
			name:      "same pr moves the locks to the new run",
			stacks:    []uuid.UUID{b, c},
			run:       pr1Again,
			pr:        1,
			wantLocks: map[uuid.UUID]uuid.UUID{a: pr1.ID, b: pr1Again.ID, c: pr1Again.ID},
		},
		{
			name:      "empty set is a no-op",
			run:       pr2,
			pr:        2,
			wantLocks: map[uuid.UUID]uuid.UUID{a: pr1.ID, b: pr1Again.ID, c: pr1Again.ID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conflicts, err := f.s.TryLockStacks(f.ctx, tc.stacks, tc.run.ID, tc.pr, "apply")
			require.NoError(t, err)
			var keys []string
			for _, c := range conflicts {
				keys = append(keys, c.StackKey)
			}
			assert.Equal(t, tc.wantConflicts, keys)
			assert.Equal(t, tc.wantLocks, lockedStacks(t, f))
		})
	}

	_, err = f.s.TryLockStacks(f.ctx, []uuid.UUID{uuid.New()}, pr2.ID, 2, "apply")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.TryLockStacks(f.ctx, []uuid.UUID{a}, uuid.New(), 2, "apply")
	require.ErrorIs(t, err, store.ErrNotFound)

	other := f.addRepo(1, 101, "acme", "acme/apps")
	otherPR1 := f.run(store.CreateRunParams{RepoID: other.ID, PRNumber: 1})
	_, err = f.s.TryLockStacks(f.ctx, []uuid.UUID{b}, otherPR1.ID, 1, "apply")
	require.ErrorIs(t, err, store.ErrInvalid, "pull request 1 of another repository must not take over the lock")
	assert.Len(t, lockedStacks(t, f), 3)
}

func TestTryLockStacksConcurrentContenders(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b", "stacks/c", "stacks/d")
	all := []uuid.UUID{ids["stacks/a"], ids["stacks/b"], ids["stacks/c"], ids["stacks/d"]}
	const contenders = 8
	runs := make([]store.Run, contenders)
	for i := range runs {
		runs[i] = f.run(store.CreateRunParams{PRNumber: 100 + i})
	}

	var (
		wg      sync.WaitGroup
		winners atomic.Int32
		winner  atomic.Value
		start   = make(chan struct{})
	)
	for i, r := range runs {
		wg.Go(func() {
			<-start
			order := append([]uuid.UUID(nil), all...)
			if i%2 == 1 {
				for l, h := 0, len(order)-1; l < h; l, h = l+1, h-1 {
					order[l], order[h] = order[h], order[l]
				}
			}
			conflicts, err := f.s.TryLockStacks(f.ctx, order, r.ID, r.PRNumber, "apply")
			if !assert.NoError(t, err) {
				return
			}
			if len(conflicts) == 0 {
				winners.Add(1)
				winner.Store(r.ID)
			} else {
				assert.Len(t, conflicts, len(all), "a loser sees every stack held")
			}
		})
	}
	close(start)
	wg.Wait()

	require.Equal(t, int32(1), winners.Load(), "exactly one contender wins")
	held := lockedStacks(t, f)
	require.Len(t, held, len(all))
	for _, runID := range held {
		assert.Equal(t, winner.Load(), runID)
	}
}

func TestReleaseLocks(t *testing.T) {
	f := newFixture(t)
	other := f.addRepo(1, 101, "acme", "acme/apps")
	ids := f.stacks("stacks/a", "stacks/b", "stacks/c")
	_, otherIDs := f.saveGraph(other.ID, &v1.Graph{SHA: "o", Stacks: []v1.Stack{{Key: "stacks/x", Path: "stacks/x"}}})
	r1 := f.run(store.CreateRunParams{PRNumber: 1})
	r2 := f.run(store.CreateRunParams{PRNumber: 2})
	r3 := f.run(store.CreateRunParams{RepoID: other.ID, PRNumber: 1})
	lock := func(run store.Run, stacks ...uuid.UUID) {
		conflicts, err := f.s.TryLockStacks(f.ctx, stacks, run.ID, run.PRNumber, "apply")
		require.NoError(t, err)
		require.Empty(t, conflicts)
	}
	lock(r1, ids["stacks/a"], ids["stacks/b"])
	lock(r2, ids["stacks/c"])
	lock(r3, otherIDs["stacks/x"])

	got, err := f.s.GetLock(f.ctx, ids["stacks/c"])
	require.NoError(t, err)
	assert.Equal(t, r2.ID, got.RunID)
	assert.Equal(t, 2, got.PRNumber)
	info := got.ToV1()
	assert.Equal(t, "stacks/c", info.StackKey)
	assert.Equal(t, r2.ID.String(), info.RunID)

	everywhere, err := f.s.ListLocks(f.ctx, 0)
	require.NoError(t, err)
	assert.Len(t, everywhere, 4)

	released, err := f.s.ReleaseLocksForPR(f.ctx, f.repo.ID, 1)
	require.NoError(t, err)
	require.Len(t, released, 2, "only the repository's locks of that pr")
	assert.Equal(t, "stacks/a", released[0].StackKey)
	_, err = f.s.GetLock(f.ctx, otherIDs["stacks/x"])
	require.NoError(t, err)

	released, err = f.s.ReleaseLocksForRun(f.ctx, r2.ID)
	require.NoError(t, err)
	require.Len(t, released, 1)

	one, err := f.s.ReleaseLock(f.ctx, otherIDs["stacks/x"])
	require.NoError(t, err)
	assert.Equal(t, r3.ID, one.RunID)
	_, err = f.s.ReleaseLock(f.ctx, otherIDs["stacks/x"])
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.GetLock(f.ctx, ids["stacks/a"])
	require.ErrorIs(t, err, store.ErrNotFound)

	none, err := f.s.ReleaseLocksForPR(f.ctx, f.repo.ID, 0)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestTryLockStacksHeldWhenGrantedUnderChurn(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b")
	stacks := []uuid.UUID{ids["stacks/a"], ids["stacks/b"]}
	holder := f.run(store.CreateRunParams{PRNumber: 1})
	contender := f.run(store.CreateRunParams{PRNumber: 2})
	const rounds = 400

	var (
		wg        sync.WaitGroup
		holderErr error
		stop      = make(chan struct{})
	)
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, holderErr = f.s.TryLockStacks(f.ctx, stacks, holder.ID, 1, "apply"); holderErr != nil {
				return
			}
			if _, holderErr = f.s.ReleaseLocksForRun(f.ctx, holder.ID); holderErr != nil {
				return
			}
		}
	})

	var (
		granted   int
		violation map[uuid.UUID]uuid.UUID
		err       error
	)
	for range rounds {
		var conflicts []store.Lock
		if conflicts, err = f.s.TryLockStacks(f.ctx, stacks, contender.ID, 2, "apply"); err != nil {
			break
		}
		if len(conflicts) > 0 {
			continue
		}
		granted++
		held := lockedStacks(t, f)
		if held[stacks[0]] != contender.ID || held[stacks[1]] != contender.ID {
			violation = held
			break
		}
		if _, err = f.s.ReleaseLocksForRun(f.ctx, contender.ID); err != nil {
			break
		}
	}
	close(stop)
	wg.Wait()

	require.NoError(t, err)
	require.NoError(t, holderErr)
	require.Nil(t, violation, "a granted lock must be held by the caller on every stack")
	require.Positive(t, granted)
	t.Logf("granted %d of %d rounds", granted, rounds)
}
