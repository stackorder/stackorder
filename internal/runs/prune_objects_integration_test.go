//go:build integration

package runs_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (e *env) hasArtifact(key string) bool {
	e.artifacts.mu.Lock()
	defer e.artifacts.mu.Unlock()
	_, ok := e.artifacts.items[key]
	return ok
}

func storePlanWithArtifact(t *testing.T, e *env, pr int, sha string) (runID, txtKey, jsonKey, stackID string) {
	t.Helper()
	e.openPull(pr, sha)
	job, id, _ := e.startPlan(pr, sha)
	res := planResult(vpc, sha, 1)
	res.PlanText = strings.Repeat("resource change line\n", 1000)
	row, err := e.svc.RecordResult(e.ctx, job.p, id, vpc, res)
	require.NoError(t, err)
	require.NotEmpty(t, row.PlanURL)
	dir := "runs/" + id + "/stacks-prod-vpc-69df0ef0/"
	require.True(t, e.hasArtifact(dir+"plan.txt"))
	require.True(t, e.hasArtifact(dir+"plan.json"))
	return id, dir + "plan.txt", dir + "plan.json", row.StackID
}

func TestPruneDeletesPlanObjects(t *testing.T) {
	e := newEnv(t, baseConfig(), withArtifacts())
	runID, txtKey, jsonKey, stackID := storePlanWithArtifact(t, e, 7, headSHA)

	_, err := e.st.Pool().Exec(e.ctx, `UPDATE run_stacks SET updated_at = now() - interval '40 days'`)
	require.NoError(t, err)

	require.NoError(t, e.svc.Prune(e.ctx, 30*24*time.Hour, 0, 0))

	assert.False(t, e.hasArtifact(txtKey), "the plan text object is deleted")
	assert.False(t, e.hasArtifact(jsonKey), "the plan summary object is deleted")

	full, err := e.st.GetRunStack(e.ctx, uuid.MustParse(runID), uuid.MustParse(stackID))
	require.NoError(t, err)
	assert.Empty(t, full.PlanURL, "the link to the deleted object is cleared")

	n, err := e.st.CountObjectDeletions(e.ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "the queue drains")
	assert.Equal(t, 0, e.m.pendingDeletions())
}

func TestRepositoryDeletionDeletesPlanObjects(t *testing.T) {
	e := newEnv(t, baseConfig(), withArtifacts())
	_, txtKey, jsonKey, _ := storePlanWithArtifact(t, e, 7, headSHA)

	require.NoError(t, e.st.DeleteRepo(e.ctx, repoID))

	require.NoError(t, e.svc.Prune(e.ctx, 0, 0, 0))

	assert.False(t, e.hasArtifact(txtKey), "deleting the repository deletes its plan text objects")
	assert.False(t, e.hasArtifact(jsonKey))
	n, err := e.st.CountObjectDeletions(e.ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestPruneKeepsObjectsWhenDeleteIsRefused(t *testing.T) {
	e := newEnv(t, baseConfig(), withArtifacts())
	_, txtKey, _, _ := storePlanWithArtifact(t, e, 7, headSHA)
	e.artifacts.mu.Lock()
	e.artifacts.deleteErr = errors.New("AccessDenied: s3:DeleteObject")
	e.artifacts.mu.Unlock()

	_, err := e.st.Pool().Exec(e.ctx, `UPDATE run_stacks SET updated_at = now() - interval '40 days'`)
	require.NoError(t, err)

	require.NoError(t, e.svc.Prune(e.ctx, 30*24*time.Hour, 0, 0), "a refused delete does not fail pruning")

	assert.True(t, e.hasArtifact(txtKey), "the object stays until the delete succeeds")
	n, err := e.st.CountObjectDeletions(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the object is still tracked for retry")
	assert.Equal(t, 1, e.m.pendingDeletions(), "the backlog is visible")

	e.artifacts.mu.Lock()
	e.artifacts.deleteErr = nil
	e.artifacts.mu.Unlock()
	require.NoError(t, e.svc.Prune(e.ctx, 30*24*time.Hour, 0, 0))
	assert.False(t, e.hasArtifact(txtKey), "once the permission is granted the retry removes it")
	assert.Equal(t, 0, e.m.pendingDeletions())
}
