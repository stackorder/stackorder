//go:build integration

package store_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestOverviewAndRepoSummaries(t *testing.T) {
	f := newFixture(t)
	other := f.addRepo(2, 200, "globex", "globex/platform")
	ids := f.stacks("stacks/a", "stacks/b", "stacks/c")
	_, otherIDs := f.saveGraph(other.ID, &v1.Graph{SHA: "g", Stacks: []v1.Stack{{Key: "stacks/x", Path: "stacks/x"}}})
	_, err := f.s.MarkStacksRemoved(f.ctx, f.repo.ID, []string{"stacks/a", "stacks/b"})
	require.NoError(t, err)

	old := f.run(store.CreateRunParams{SHA: "s1", PRNumber: 1, Status: v1.RunApplied})
	cur := f.run(store.CreateRunParams{SHA: "s2", PRNumber: 1, Status: v1.RunPlanning})
	otherRun := f.run(store.CreateRunParams{RepoID: other.ID, SHA: "g"})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, old.ID, []store.RunStack{{StackID: ids["stacks/a"], Status: v1.StackApplied}}))
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, cur.ID, []store.RunStack{
		{StackID: ids["stacks/a"], Status: v1.StackPlanning}, {StackID: ids["stacks/b"], Status: v1.StackPlanned},
	}))
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, otherRun.ID, []store.RunStack{{StackID: otherIDs["stacks/x"]}}))

	for _, d := range []store.Drift{
		{StackID: ids["stacks/a"], Drifted: false},
		{StackID: ids["stacks/b"], Drifted: true},
		{StackID: ids["stacks/c"], Drifted: true},
		{StackID: otherIDs["stacks/x"], Drifted: true},
	} {
		_, err := f.s.RecordDrift(f.ctx, d)
		require.NoError(t, err)
	}
	conflicts, err := f.s.TryLockStacks(f.ctx, []uuid.UUID{ids["stacks/a"]}, cur.ID, 1, "apply")
	require.NoError(t, err)
	require.Empty(t, conflicts)
	closedPR := f.run(store.CreateRunParams{SHA: "s3", PRNumber: 3, Status: v1.RunApplied})
	conflicts, err = f.s.TryLockStacks(f.ctx, []uuid.UUID{ids["stacks/c"]}, closedPR.ID, 3, "apply")
	require.NoError(t, err)
	require.Empty(t, conflicts, "a removed stack can still hold a lock that needs an unlock")

	cases := []struct {
		name     string
		accounts []string
		want     v1.Overview
		recent   int
	}{
		{
			name: "everything",
			want: v1.Overview{
				Repos: 2, Stacks: 3, Drifted: 2, LocksHeld: 2,
				RunsByStatus:   map[v1.RunStatus]int{v1.RunApplied: 2, v1.RunPlanning: 1, v1.RunPending: 1},
				StacksByStatus: map[v1.StackStatus]int{v1.StackPlanning: 1, v1.StackPlanned: 1, v1.StackPending: 1},
			},
			recent: 4,
		},
		{
			name:     "one account",
			accounts: []string{"Globex"},
			want: v1.Overview{
				Repos: 1, Stacks: 1, Drifted: 1,
				RunsByStatus:   map[v1.RunStatus]int{v1.RunPending: 1},
				StacksByStatus: map[v1.StackStatus]int{v1.StackPending: 1},
			},
			recent: 1,
		},
		{
			name:     "unknown account",
			accounts: []string{"initech"},
			want: v1.Overview{
				RunsByStatus:   map[v1.RunStatus]int{},
				StacksByStatus: map[v1.StackStatus]int{},
			},
		},
		{
			name:     "empty account name",
			accounts: []string{""},
			want: v1.Overview{
				RunsByStatus:   map[v1.RunStatus]int{},
				StacksByStatus: map[v1.StackStatus]int{},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.s.Overview(f.ctx, tc.accounts...)
			require.NoError(t, err)
			assert.Len(t, got.RecentRuns, tc.recent)
			got.RecentRuns = nil
			assert.Equal(t, tc.want, got)
		})
	}

	recent, err := f.s.Overview(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, closedPR.ID.String(), recent.RecentRuns[0].ID, "recent runs are newest first")

	summaries, err := f.s.RepoSummaries(f.ctx)
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	require.NotNil(t, summaries[0].LastRunAt)
	summaries[0].LastRunAt, summaries[1].LastRunAt = nil, nil
	assert.Equal(t, []v1.RepoSummary{
		{ID: f.repo.ID, FullName: "acme/infra", DefaultBranch: "main", Stacks: 2, Drifted: 1, LocksHeld: 2},
		{ID: other.ID, FullName: "globex/platform", DefaultBranch: "main", Stacks: 1, Drifted: 1},
	}, summaries)

	var summed int
	for _, r := range summaries {
		summed += r.LocksHeld
	}
	everything, err := f.s.Overview(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, everything.LocksHeld, summed, "the overview and the repository list agree on held locks")

	scoped, err := f.s.RepoSummaries(f.ctx, "acme")
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, "acme/infra", scoped[0].FullName)

	none, err := f.s.RepoSummaries(f.ctx, "")
	require.NoError(t, err)
	assert.Empty(t, none, "an empty account name must not widen the scope to every repository")
}

func TestRunsToV1SumsUpStacks(t *testing.T) {
	f := newFixture(t)
	keys := []string{"stacks/a", "stacks/b", "stacks/c", "stacks/d", "stacks/e", "stacks/f", "stacks/g"}
	ids := f.stacks(keys...)

	planned := f.run(store.CreateRunParams{SHA: "s1", PRNumber: 1, Status: v1.RunPlanned})
	var rows []store.RunStack
	for i, k := range keys {
		rows = append(rows, store.RunStack{StackID: ids[k], Wave: len(keys) - 1 - i, Status: v1.StackPlanned})
	}
	rows[0].Summary = &v1.PlanSummary{Adds: 2, Destroys: 1, Imports: 1}
	rows[0].Adds, rows[0].Destroys = 2, 1
	rows[1].Summary = &v1.PlanSummary{Changes: 3, Replaces: 1, OutputChanges: 2}
	rows[1].Changes, rows[1].Replaces = 3, 1
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, planned.ID, rows))

	apply := f.run(store.CreateRunParams{SHA: "s1", PRNumber: 1, Mode: v1.ModeApply, Status: v1.RunApplying})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, apply.ID, []store.RunStack{
		{StackID: ids["stacks/a"], Mode: v1.ModeApply, Status: v1.StackApplying},
		{StackID: ids["stacks/b"], Mode: v1.ModeApply, Status: v1.StackSkipped, Summary: &v1.PlanSummary{Adds: 9}, Adds: 9},
	}))

	empty := f.run(store.CreateRunParams{SHA: "s2", PRNumber: 2})

	got, err := f.s.RunsToV1(f.ctx, []store.Run{planned, apply, empty})
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, planned.ID.String(), got[0].ID)
	assert.Equal(t, 7, got[0].StackCount)
	assert.Equal(t, []string{"stacks/g", "stacks/f", "stacks/e", "stacks/d", "stacks/c"}, got[0].StackKeys,
		"keys are listed in wave order, up to the cap")
	assert.Equal(t, &v1.PlanSummary{Adds: 2, Changes: 3, Destroys: 1, Replaces: 1, Imports: 1, OutputChanges: 2}, got[0].Summary)

	assert.Equal(t, 1, got[1].StackCount, "the stacks an apply skips are left out")
	assert.Equal(t, []string{"stacks/a"}, got[1].StackKeys)
	assert.Nil(t, got[1].Summary, "no summary until a stack has one")

	assert.Zero(t, got[2].StackCount)
	assert.Nil(t, got[2].StackKeys)
	assert.Nil(t, got[2].Summary)

	none, err := f.s.RunsToV1(f.ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}
