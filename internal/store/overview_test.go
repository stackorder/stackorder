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

	cases := []struct {
		name     string
		accounts []string
		want     v1.Overview
		recent   int
	}{
		{
			name: "everything",
			want: v1.Overview{
				Repos: 2, Stacks: 3, Drifted: 2, LocksHeld: 1,
				RunsByStatus:   map[v1.RunStatus]int{v1.RunApplied: 1, v1.RunPlanning: 1, v1.RunPending: 1},
				StacksByStatus: map[v1.StackStatus]int{v1.StackPlanning: 1, v1.StackPlanned: 1, v1.StackPending: 1},
			},
			recent: 3,
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
	assert.Equal(t, otherRun.ID.String(), recent.RecentRuns[0].ID, "recent runs are newest first")

	summaries, err := f.s.RepoSummaries(f.ctx)
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	require.NotNil(t, summaries[0].LastRunAt)
	summaries[0].LastRunAt, summaries[1].LastRunAt = nil, nil
	assert.Equal(t, []v1.RepoSummary{
		{ID: f.repo.ID, FullName: "acme/infra", DefaultBranch: "main", Stacks: 2, Drifted: 1, LocksHeld: 1},
		{ID: other.ID, FullName: "globex/platform", DefaultBranch: "main", Stacks: 1, Drifted: 1},
	}, summaries)

	scoped, err := f.s.RepoSummaries(f.ctx, "acme")
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, "acme/infra", scoped[0].FullName)
}
