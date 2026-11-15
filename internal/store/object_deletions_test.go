//go:build integration

package store_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func (f *fixture) planned(runID uuid.UUID, stackID uuid.UUID, planURL string) {
	f.t.Helper()
	status := v1.StackPlanned
	_, err := f.s.UpdateRunStack(f.ctx, runID, stackID, store.RunStackPatch{
		Status: &status, PlanText: ptr("+ resource"), PlanURL: &planURL,
	})
	require.NoError(f.t, err)
}

func TestObjectDeletionLifecycle(t *testing.T) {
	f := newFixture(t)
	run := uuid.New()

	require.NoError(t, f.s.FailObjectDeletion(f.ctx, run, "stacks/a", errors.New("access denied")),
		"failing an absent row is a no-op")
	pending, err := f.s.ClaimObjectDeletions(f.ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, pending)

	f.exec(`INSERT INTO object_deletions (run_id, stack_key) VALUES ($1, 'stacks/a'), ($1, 'stacks/b')`, run)

	n, err := f.s.CountObjectDeletions(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	require.NoError(t, f.s.FailObjectDeletion(f.ctx, run, "stacks/a", errors.New("boom")))
	pending, err = f.s.ClaimObjectDeletions(f.ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	for _, d := range pending {
		if d.StackKey == "stacks/a" {
			assert.Equal(t, 1, d.Attempts)
			assert.Contains(t, d.LastError, "boom")
		}
	}

	require.NoError(t, f.s.CompleteObjectDeletion(f.ctx, run, "stacks/a"))
	require.NoError(t, f.s.CompleteObjectDeletion(f.ctx, run, "stacks/a"), "completing twice is a no-op")
	n, err = f.s.CountObjectDeletions(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestDeleteRepoQueuesPlanObjects(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b")
	run := f.run(store.CreateRunParams{})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, run.ID, []store.RunStack{
		{StackID: ids["stacks/a"]}, {StackID: ids["stacks/b"]},
	}))
	f.planned(run.ID, ids["stacks/a"], "s3://bucket/runs/"+run.ID.String()+"/stacks-a/plan.txt")
	f.planned(run.ID, ids["stacks/b"], "")

	require.NoError(t, f.s.DeleteRepo(f.ctx, f.repo.ID))

	_, err := f.s.GetRepo(f.ctx, f.repo.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	pending, err := f.s.ClaimObjectDeletions(f.ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "only the stack that kept an object is queued")
	assert.Equal(t, run.ID, pending[0].RunID)
	assert.Equal(t, "stacks/a", pending[0].StackKey)
}

func TestDeleteInstallationQueuesPlanObjects(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a")
	run := f.run(store.CreateRunParams{})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, run.ID, []store.RunStack{{StackID: ids["stacks/a"]}}))
	f.planned(run.ID, ids["stacks/a"], "s3://bucket/runs/"+run.ID.String()+"/stacks-a/plan.txt")

	require.NoError(t, f.s.DeleteInstallation(f.ctx, f.repo.InstallationID))

	_, err := f.s.GetRepo(f.ctx, f.repo.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	pending, err := f.s.ClaimObjectDeletions(f.ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "removing the installation queues its repositories' plan objects")
	assert.Equal(t, "stacks/a", pending[0].StackKey)
}
