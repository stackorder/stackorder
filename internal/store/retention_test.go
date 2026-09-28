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

func TestPrunePlanText(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b", "stacks/c")
	r := f.run(store.CreateRunParams{})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{
		{StackID: ids["stacks/a"]}, {StackID: ids["stacks/b"]}, {StackID: ids["stacks/c"]},
	}))
	planned := v1.StackPlanned
	for _, key := range []string{"stacks/a", "stacks/b"} {
		_, err := f.s.UpdateRunStack(f.ctx, r.ID, ids[key], store.RunStackPatch{
			Status: &planned, PlanText: ptr("+ resource"), Summary: &v1.PlanSummary{Adds: 1},
		})
		require.NoError(t, err)
	}
	f.exec(`UPDATE run_stacks SET updated_at = now() - interval '31 days' WHERE stack_id = ANY($1::uuid[])`,
		[]uuid.UUID{ids["stacks/a"], ids["stacks/c"]})

	n, err := f.s.PrunePlanText(f.ctx, 30*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only old rows that still hold text")

	a, err := f.s.GetRunStack(f.ctx, r.ID, ids["stacks/a"])
	require.NoError(t, err)
	assert.Empty(t, a.PlanText)
	require.NotNil(t, a.Summary, "summaries are kept")
	assert.Equal(t, 1, a.Adds)
	b, err := f.s.GetRunStack(f.ctx, r.ID, ids["stacks/b"])
	require.NoError(t, err)
	assert.Equal(t, "+ resource", b.PlanText)

	n, err = f.s.PrunePlanText(f.ctx, 30*24*time.Hour)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestPrune(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a")
	r := f.run(store.CreateRunParams{})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{{StackID: ids["stacks/a"]}}))
	_, err := f.s.UpdateRunStack(f.ctx, r.ID, ids["stacks/a"], store.RunStackPatch{PlanText: ptr("text")})
	require.NoError(t, err)
	for _, at := range []time.Duration{-100 * 24 * time.Hour, -95 * 24 * time.Hour, -time.Hour} {
		_, err := f.s.RecordDrift(f.ctx, store.Drift{StackID: ids["stacks/a"], CheckedAt: time.Now().Add(at)})
		require.NoError(t, err)
	}
	_, err = f.s.InsertEvent(f.ctx, "old", "push", nil)
	require.NoError(t, err)
	job, _, err := f.s.EnqueueJob(f.ctx, "k", nil, time.Time{}, "")
	require.NoError(t, err)
	require.NoError(t, f.s.CompleteJob(f.ctx, job.ID))
	_, err = f.s.SeenJTI(f.ctx, "expired", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	_, _, err = f.s.CreateSession(f.ctx, store.NewSession{Login: "gone", TTL: time.Hour})
	require.NoError(t, err)
	f.exec(`UPDATE run_stacks SET updated_at = now() - interval '31 days'`)
	f.exec(`UPDATE events SET received_at = now() - interval '8 days'`)
	f.exec(`UPDATE jobs SET done_at = now() - interval '8 days'`)
	f.exec(`UPDATE sessions SET expires_at = now() - interval '1 hour'`)

	cases := []struct {
		name   string
		policy store.Retention
		want   store.PruneResult
	}{
		{"disabled keeps history", store.Retention{}, store.PruneResult{JTIs: 1, Sessions: 1}},
		{"defaults", store.DefaultRetention, store.PruneResult{PlanText: 1, Events: 1, Jobs: 1, Drift: 2}},
		{"second pass finds nothing", store.DefaultRetention, store.PruneResult{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.s.Prune(f.ctx, tc.policy)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
