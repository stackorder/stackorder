//go:build integration

package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestDrift(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b", "stacks/gone")
	a, b := ids["stacks/a"], ids["stacks/b"]
	r := f.run(store.CreateRunParams{Mode: v1.ModeDrift, Trigger: v1.TriggerSchedule})
	now := time.Now().UTC()

	records := []store.Drift{
		{StackID: a, CheckedAt: now.Add(-100 * 24 * time.Hour), Drifted: true},
		{StackID: a, CheckedAt: now.Add(-95 * 24 * time.Hour), Drifted: false},
		{StackID: a, CheckedAt: now.Add(-time.Hour), Drifted: true, RunID: &r.ID, Summary: &v1.PlanSummary{Changes: 1}},
		{StackID: b, CheckedAt: now.Add(-120 * 24 * time.Hour), Drifted: true},
		{StackID: ids["stacks/gone"], Drifted: true},
	}
	var latestA store.Drift
	for _, d := range records {
		got, err := f.s.RecordDrift(f.ctx, d)
		require.NoError(t, err)
		if d.RunID != nil {
			latestA = got
		}
	}
	assert.Equal(t, "stacks/a", latestA.StackKey)
	require.NoError(t, f.s.SetDriftIssue(f.ctx, latestA.ID, 12))
	require.ErrorIs(t, f.s.SetDriftIssue(f.ctx, uuid.New(), 1), store.ErrNotFound)

	got, err := f.s.LatestDrift(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, latestA.ID, got.ID)
	status := got.ToV1()
	assert.True(t, status.Drifted)
	assert.Equal(t, 12, status.IssueNumber)
	assert.Equal(t, 1, status.Summary.Changes)
	_, err = f.s.LatestDrift(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)

	_, err = f.s.MarkStacksRemoved(f.ctx, f.repo.ID, []string{"stacks/a", "stacks/b"})
	require.NoError(t, err)
	forRepo, err := f.s.LatestDriftForRepo(f.ctx, f.repo.ID)
	require.NoError(t, err)
	require.Len(t, forRepo, 2, "removed stacks are left out")
	assert.Equal(t, []string{"stacks/a", "stacks/b"}, []string{forRepo[0].StackKey, forRepo[1].StackKey})
	assert.Equal(t, latestA.ID, forRepo[0].ID)

	pruned, err := f.s.PruneDrift(f.ctx, 90*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), pruned, "old history goes, the newest row of each stack stays")
	stillB, err := f.s.LatestDrift(f.ctx, b)
	require.NoError(t, err)
	assert.True(t, stillB.Drifted)

	_, err = f.s.RecordDrift(f.ctx, store.Drift{StackID: uuid.New()})
	require.ErrorIs(t, err, store.ErrNotFound)
}
