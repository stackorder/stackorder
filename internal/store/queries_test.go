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

func TestCountAudit(t *testing.T) {
	f := newFixture(t)
	before := time.Now().Add(-time.Minute)
	for _, target := range []string{"pr:acme/infra#1", "pr:acme/infra#1", "pr:acme/infra#2"} {
		_, err := f.s.RecordAudit(f.ctx, store.AuditEntry{Actor: "octocat", Action: "command", Target: target})
		require.NoError(t, err)
	}
	_, err := f.s.RecordAudit(f.ctx, store.AuditEntry{Actor: "octocat", Action: "unlock", Target: "pr:acme/infra#1"})
	require.NoError(t, err)
	n, err := f.s.CountAudit(f.ctx, "command", "pr:acme/infra#1", before)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	n, err = f.s.CountAudit(f.ctx, "command", "pr:acme/infra#1", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "entries before since are not counted")
}

func TestLockKeySerialisesTransactions(t *testing.T) {
	f := newFixture(t)
	require.ErrorIs(t, f.s.LockKey(f.ctx, ""), store.ErrInvalid)
	var (
		mu     sync.Mutex
		inside int
		most   int
		wg     sync.WaitGroup
	)
	for range 4 {
		wg.Go(func() {
			err := f.s.InTx(f.ctx, func(tx *store.Store) error {
				if err := tx.LockKey(f.ctx, "run:1"); err != nil {
					return err
				}
				mu.Lock()
				inside++
				most = max(most, inside)
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, 1, most, "only one transaction holds the key at a time")
}

func TestExternalDependents(t *testing.T) {
	f := newFixture(t)
	network := f.addRepo(1, 300, "acme", "acme/network")
	elsewhere := f.addRepo(2, 400, "other", "other/infra")
	f.saveGraph(network.ID, &v1.Graph{Repo: network.FullName, SHA: "n1", Stacks: []v1.Stack{{Key: "stacks/prod/tgw", Path: "stacks/prod/tgw"}}})

	dependent := func(repo store.Repo, sha, target string) *v1.Graph {
		return &v1.Graph{
			Repo: repo.FullName, SHA: sha,
			Stacks: []v1.Stack{
				{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc"},
				{Key: target, Path: "stacks/prod/tgw", Repo: "acme/network", External: true},
			},
			Edges: []v1.Edge{
				{From: v1.StackRef("stacks/prod/vpc"), To: v1.StackRef(target), Type: v1.EdgeDependsOn},
			},
		}
	}
	oldID, _ := f.saveGraph(f.repo.ID, dependent(f.repo, "i1", "acme/network//stacks/prod/tgw"))
	f.saveGraph(f.repo.ID, &v1.Graph{Repo: f.repo.FullName, SHA: "i2", Stacks: []v1.Stack{{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc"}}})
	f.saveGraph(elsewhere.ID, dependent(elsewhere, "e1", "ACME/Network//stacks/prod/tgw"))

	got, err := f.s.ExternalDependents(f.ctx, network.ID)
	require.NoError(t, err)
	assert.Empty(t, got, "the latest graph of acme/infra no longer depends on acme/network, and other/infra is another installation")

	require.NoError(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, oldID))
	got, err = f.s.ExternalDependents(f.ctx, network.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.ExternalDependent{{
		RepoID: f.repo.ID, Repo: "acme/infra", FromKey: "stacks/prod/vpc", ToKey: "stacks/prod/tgw", Type: v1.EdgeDependsOn,
	}}, got, "the default graph is read in preference to the latest")
}

func TestAdvanceRunWaveAndWarnings(t *testing.T) {
	f := newFixture(t)
	r := f.run(store.CreateRunParams{Mode: v1.ModeApply})
	moved, err := f.s.AdvanceRunWave(f.ctx, r.ID, 2)
	require.NoError(t, err)
	assert.True(t, moved)
	moved, err = f.s.AdvanceRunWave(f.ctx, r.ID, 1)
	require.NoError(t, err)
	assert.False(t, moved, "a run never moves back")
	moved, err = f.s.AdvanceRunWave(f.ctx, r.ID, 2)
	require.NoError(t, err)
	assert.False(t, moved)
	got, err := f.s.GetRun(f.ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.CurrentWave)
	_, err = f.s.AdvanceRunWave(f.ctx, uuid.New(), 1)
	require.ErrorIs(t, err, store.ErrNotFound)

	require.NoError(t, f.s.AddRunWarning(f.ctx, r.ID, "first"))
	require.NoError(t, f.s.AddRunWarning(f.ctx, r.ID, "second"))
	require.NoError(t, f.s.AddRunWarning(f.ctx, r.ID, "first"))
	got, err = f.s.GetRun(f.ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, got.Warnings)
	require.ErrorIs(t, f.s.AddRunWarning(f.ctx, r.ID, ""), store.ErrInvalid)
	require.ErrorIs(t, f.s.AddRunWarning(f.ctx, uuid.New(), "x"), store.ErrNotFound)
}

func TestGetRunStacksWithText(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("stacks/a", "stacks/b")
	r := f.run(store.CreateRunParams{})
	require.NoError(t, f.s.UpsertRunStacks(f.ctx, r.ID, []store.RunStack{{StackID: ids["stacks/a"]}, {StackID: ids["stacks/b"], Wave: 1}}))
	_, err := f.s.UpdateRunStack(f.ctx, r.ID, ids["stacks/b"], store.RunStackPatch{PlanText: ptr("plan b")})
	require.NoError(t, err)
	rows, err := f.s.GetRunStacksWithText(f.ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, []string{"stacks/a", "stacks/b"}, []string{rows[0].Key, rows[1].Key})
	assert.Equal(t, []string{"", "plan b"}, []string{rows[0].PlanText, rows[1].PlanText})
	plain, err := f.s.GetRunStacks(f.ctx, r.ID)
	require.NoError(t, err)
	assert.Empty(t, plain[1].PlanText)
}

func TestFindRunByWorkflowRun(t *testing.T) {
	f := newFixture(t)
	other := f.addRepo(2, 200, "other", "other/infra")
	first := f.run(store.CreateRunParams{WorkflowRunID: 77, WorkflowRunAttempt: 1})
	f.run(store.CreateRunParams{RepoID: other.ID, WorkflowRunID: 77})
	got, err := f.s.FindRunByWorkflowRun(f.ctx, f.repo.ID, 77)
	require.NoError(t, err)
	assert.Equal(t, first.ID, got.ID)

	second := f.run(store.CreateRunParams{WorkflowRunID: 77, WorkflowRunAttempt: 2})
	got, err = f.s.FindRunByWorkflowRun(f.ctx, f.repo.ID, 77)
	require.NoError(t, err)
	assert.Equal(t, second.ID, got.ID, "the newest attempt wins")

	for _, id := range []int64{0, 78} {
		_, err = f.s.FindRunByWorkflowRun(f.ctx, f.repo.ID, id)
		require.ErrorIs(t, err, store.ErrNotFound, "workflow run %d", id)
	}
}
