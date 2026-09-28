//go:build integration

package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestDefaultGraph(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.s.GetDefaultGraph(f.ctx, f.repo.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Nil(t, f.repo.DefaultGraphID)

	id, _ := f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "s1"))
	f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "s2"))
	require.NoError(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, id))

	g, gotID, err := f.s.GetDefaultGraph(f.ctx, f.repo.ID)
	require.NoError(t, err)
	assert.Equal(t, id, gotID)
	assert.Equal(t, "s1", g.SHA, "the default graph wins over the newer one")
	repo, err := f.s.GetRepo(f.ctx, f.repo.ID)
	require.NoError(t, err)
	require.NotNil(t, repo.DefaultGraphID)
	assert.Equal(t, id, *repo.DefaultGraphID)

	other := f.addRepo(2, 200, "other", "other/infra")
	foreign, _ := f.saveGraph(other.ID, sampleGraph(other.FullName, "o1"))
	require.ErrorIs(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, foreign), store.ErrNotFound,
		"a graph of another repository cannot be the default")
	require.ErrorIs(t, f.s.SetDefaultGraph(f.ctx, 999, id), store.ErrNotFound)
}

func TestRunCheckRuns(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{})
	assert.Empty(t, r.CheckRuns)
	require.NoError(t, f.s.SetRunCheckRun(f.ctx, r.ID, "stackorder/plan", 11))
	require.NoError(t, f.s.SetRunCheckRun(f.ctx, r.ID, "stackorder/plan: stacks/a", 12))
	require.NoError(t, f.s.SetRunCheckRun(f.ctx, r.ID, "stackorder/plan", 13))
	got, err := f.s.GetRun(f.ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"stackorder/plan": 13, "stackorder/plan: stacks/a": 12}, got.CheckRuns)

	require.ErrorIs(t, f.s.SetRunCheckRun(f.ctx, r.ID, "", 1), store.ErrInvalid)
	require.ErrorIs(t, f.s.SetRunCheckRun(f.ctx, r.ID, "x", 0), store.ErrInvalid)
	require.ErrorIs(t, f.s.SetRunCheckRun(f.ctx, uuid.New(), "x", 1), store.ErrNotFound)
}

func TestSetRunCheckRunConcurrent(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{})
	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() { errs[i] = f.s.SetRunCheckRun(f.ctx, r.ID, "check-"+string(rune('a'+i)), int64(i+1)) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	got, err := f.s.GetRun(f.ctx, r.ID)
	require.NoError(t, err)
	assert.Len(t, got.CheckRuns, writers, "no concurrent write is lost")
}

func TestRunStackServiceFields(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b")
	r := f.run(store.CreateRunParams{Mode: v1.ModeApply})
	summary := &v1.PlanSummary{Adds: 2, Changes: 1, Added: []string{"a.b"}}
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{
		{
			StackID: ids["stacks/a"], Status: v1.StackPlanned, PlanOutput: "summary", Summary: summary,
			Adds: 2, Changes: 1, HasChanges: true, PlanArtifact: "art-a", PlanRunID: 77,
		},
		{StackID: ids["stacks/b"], Wave: 1},
	}))
	a, err := f.s.GetRunStack(f.ctx, r.ID, ids["stacks/a"])
	require.NoError(t, err)
	assert.Equal(t, v1.StackPlanned, a.Status)
	assert.Equal(t, "summary", a.PlanOutput)
	assert.Equal(t, summary, a.Summary)
	assert.Equal(t, 2, a.Adds)
	assert.True(t, a.HasChanges)
	assert.Equal(t, "art-a", a.PlanArtifact)
	assert.Equal(t, int64(77), a.PlanRunID)
	assert.Empty(t, a.BlockedBy)
	assert.Nil(t, a.DispatchID)

	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{
		{StackID: ids["stacks/a"], Status: v1.StackPending, PlanOutput: "full", PlanArtifact: "other"},
	}))
	a, err = f.s.GetRunStack(f.ctx, r.ID, ids["stacks/a"])
	require.NoError(t, err)
	assert.Equal(t, v1.StackPlanned, a.Status, "an existing row keeps its status")
	assert.Equal(t, "art-a", a.PlanArtifact, "an existing row keeps its plan")
	assert.Equal(t, "full", a.PlanOutput, "plan output is refreshed")

	d, _, err := f.s.CreateDispatch(f.ctx, r.ID, 1, "", v1.ModeApply)
	require.NoError(t, err)
	started := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	b, err := f.s.UpdateRunStack(f.ctx, r.ID, ids["stacks/b"], store.RunStackPatch{
		Status:     ptr(v1.StackBlocked),
		BlockedBy:  &[]string{"stacks/a"},
		DispatchID: &d.ID,
		PlanURL:    ptr("https://bucket/plan.txt"),
		StartedAt:  &started,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"stacks/a"}, b.BlockedBy)
	require.NotNil(t, b.DispatchID)
	assert.Equal(t, d.ID, *b.DispatchID)
	assert.Equal(t, "https://bucket/plan.txt", b.PlanURL)
	require.NotNil(t, b.StartedAt)
	assert.True(t, started.Equal(*b.StartedAt))

	later := started.Add(time.Hour)
	b, err = f.s.UpdateRunStack(f.ctx, r.ID, ids["stacks/b"], store.RunStackPatch{StartedAt: &later})
	require.NoError(t, err)
	assert.True(t, started.Equal(*b.StartedAt), "an existing start time is kept")

	v := b.ToV1()
	assert.Equal(t, []string{"stacks/a"}, v.BlockedBy)
	assert.Equal(t, "https://bucket/plan.txt", v.PlanURL)

	rows, err := f.s.GetRunStacks(f.ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "full", rows[0].ToV1().PlanOutput)
}

func TestDispatchChunks(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{Mode: v1.ModeApply})
	c0, created, err := f.s.CreateDispatchChunk(f.ctx, r.ID, 0, "production", v1.ModeApply, 0)
	require.NoError(t, err)
	assert.True(t, created)
	c1, created, err := f.s.CreateDispatchChunk(f.ctx, r.ID, 0, "production", v1.ModeApply, 1)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, c0.ID, c1.ID)
	assert.Equal(t, 1, c1.Chunk)
	again, created, err := f.s.CreateDispatch(f.ctx, r.ID, 0, "production", v1.ModeApply)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, c0.ID, again.ID, "CreateDispatch is chunk 0")
	_, _, err = f.s.CreateDispatchChunk(f.ctx, r.ID, 0, "production", v1.ModeApply, -1)
	require.ErrorIs(t, err, store.ErrInvalid)

	assert.Nil(t, c1.SentAt)
	sent, err := f.s.MarkDispatchSent(f.ctx, c1.ID)
	require.NoError(t, err)
	require.NotNil(t, sent.SentAt)
	resent, err := f.s.MarkDispatchSent(f.ctx, c1.ID)
	require.NoError(t, err)
	assert.True(t, sent.SentAt.Equal(*resent.SentAt), "the first send time is kept")
	got, err := f.s.GetDispatch(f.ctx, c1.ID)
	require.NoError(t, err)
	assert.Equal(t, resent, got)
	_, err = f.s.GetDispatch(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.MarkDispatchSent(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	list, err := f.s.ListDispatches(f.ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, []int{0, 1}, []int{list[0].Chunk, list[1].Chunk})
}

func TestFindRunForStack(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b")
	old := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, old.ID, []store.RunStack{{StackID: ids["stacks/a"]}}))
	f.exec(`UPDATE runs SET created_at = created_at - interval '2 hours' WHERE id = $1`, old.ID)
	fresh := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, fresh.ID, []store.RunStack{{StackID: ids["stacks/a"]}}))

	got, err := f.s.FindRunForStack(f.ctx, f.repo.ID, ids["stacks/a"], v1.ModeDrift, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, fresh.ID, got.ID)
	got, err = f.s.FindRunForStack(f.ctx, f.repo.ID, ids["stacks/a"], v1.ModeDrift, time.Now().Add(-3*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, fresh.ID, got.ID, "the newest run wins")
	_, err = f.s.FindRunForStack(f.ctx, f.repo.ID, ids["stacks/b"], v1.ModeDrift, time.Now().Add(-3*time.Hour))
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.FindRunForStack(f.ctx, f.repo.ID, ids["stacks/a"], v1.ModePlan, time.Now().Add(-3*time.Hour))
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestReleaseLockOfPR(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a")
	r := f.run(store.CreateRunParams{PRNumber: 2})
	conflicts, err := f.s.TryLockStacks(f.ctx, []uuid.UUID{ids["stacks/a"]}, r.ID, 2, "apply of #2")
	require.NoError(t, err)
	require.Empty(t, conflicts)
	for _, pr := range []int{0, 1} {
		_, err = f.s.ReleaseLockOfPR(f.ctx, ids["stacks/a"], pr)
		require.ErrorIs(t, err, store.ErrNotFound, "pull request %d does not hold the lock", pr)
	}
	held, err := f.s.GetLock(f.ctx, ids["stacks/a"])
	require.NoError(t, err)
	assert.Equal(t, 2, held.PRNumber)

	got, err := f.s.ReleaseLockOfPR(f.ctx, ids["stacks/a"], 2)
	require.NoError(t, err)
	assert.Equal(t, r.ID, got.RunID)
	assert.Equal(t, "stacks/a", got.StackKey)
	_, err = f.s.GetLock(f.ctx, ids["stacks/a"])
	require.ErrorIs(t, err, store.ErrNotFound)
}
