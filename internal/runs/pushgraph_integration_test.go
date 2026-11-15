//go:build integration

package runs_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func (e *env) pushJob(branch, sha string) principal.Principal {
	c := e.oidc.PushClaims(repoName, fmt.Sprint(repoID), branch, sha)
	return principal.Principal{Kind: principal.OIDC, Login: c.Actor, Claims: &c}
}

func hasStack(stacks []store.Stack, key string) bool {
	return slices.ContainsFunc(stacks, func(s store.Stack) bool { return s.Key == key })
}

func TestRecordDefaultGraphOnPush(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRef(repoName, "heads/main", newHeadSHA)

	resp, err := e.svc.RecordDefaultGraph(e.ctx, e.pushJob("main", newHeadSHA), repoName, v1.RecordGraphRequest{Graph: testGraph(newHeadSHA)})
	require.NoError(t, err)
	require.True(t, resp.Recorded, "a push to the default-branch head records its graph")
	assert.Equal(t, newHeadSHA, resp.SHA)

	g, _, err := e.st.GetDefaultGraph(e.ctx, repoID)
	require.NoError(t, err)
	assert.Equal(t, newHeadSHA, g.SHA, "the pushed graph becomes the default-branch graph")

	live, err := e.st.ListStacks(e.ctx, repoID, false)
	require.NoError(t, err)
	assert.True(t, hasStack(live, vpc), "the pushed graph's stacks are live")
}

func TestRecordDefaultGraphSupersededByNewerHead(t *testing.T) {
	e := newEnv(t, baseConfig())
	// The default branch has already moved past the commit the token is for.
	e.gh.SetRef(repoName, "heads/main", newHeadSHA)

	resp, err := e.svc.RecordDefaultGraph(e.ctx, e.pushJob("main", headSHA), repoName, v1.RecordGraphRequest{Graph: testGraph(headSHA)})
	require.NoError(t, err)
	assert.False(t, resp.Recorded, "a graph for a commit that is no longer the head is not recorded")
	assert.Equal(t, newHeadSHA, resp.Head)
	assert.NotEmpty(t, resp.Reason)

	_, _, err = e.st.GetDefaultGraph(e.ctx, repoID)
	require.ErrorIs(t, err, store.ErrNotFound, "no default graph is recorded when the push is superseded")
}

func TestRecordDefaultGraphRejectsNonPushEvent(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRef(repoName, "heads/main", newHeadSHA)

	job := e.planJob(7)
	_, err := e.svc.RecordDefaultGraph(e.ctx, job.p, repoName, v1.RecordGraphRequest{Graph: testGraph(newHeadSHA)})
	require.ErrorIs(t, err, principal.ErrForbidden, "a pull_request token cannot record the default-branch graph")
}

func TestRecordDefaultGraphRejectsNonDefaultBranch(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRef(repoName, "heads/main", newHeadSHA)

	_, err := e.svc.RecordDefaultGraph(e.ctx, e.pushJob("feature", newHeadSHA), repoName, v1.RecordGraphRequest{Graph: testGraph(newHeadSHA)})
	require.ErrorIs(t, err, principal.ErrForbidden, "a push to another branch cannot record the default-branch graph")
}

func TestRecordDefaultGraphRejectsForeignRepository(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRef(repoName, "heads/main", newHeadSHA)

	c := e.oidc.PushClaims("evil/fork", "999", "main", newHeadSHA)
	p := principal.Principal{Kind: principal.OIDC, Login: c.Actor, Claims: &c}
	_, err := e.svc.RecordDefaultGraph(e.ctx, p, repoName, v1.RecordGraphRequest{Graph: testGraph(newHeadSHA)})
	require.ErrorIs(t, err, principal.ErrForbidden, "a token for another repository is refused")
}

func TestRecordDefaultGraphRejectsMismatchedSHA(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRef(repoName, "heads/main", newHeadSHA)

	_, err := e.svc.RecordDefaultGraph(e.ctx, e.pushJob("main", newHeadSHA), repoName, v1.RecordGraphRequest{Graph: testGraph(headSHA)})
	require.ErrorIs(t, err, principal.ErrInvalid, "the graph must be for the commit the token is for")
}

func TestRecordDefaultGraphReconcilesRemovedStacks(t *testing.T) {
	const gone = "stacks/prod/legacy"
	e := newEnv(t, baseConfig())

	onMain := testGraph(mainSHA)
	onMain.Stacks = append(onMain.Stacks, v1.Stack{Key: gone, Path: gone, Backend: &v1.Backend{Type: "s3", Bucket: "state", Key: gone + ".tfstate"}})
	id, _, err := e.st.SaveGraph(e.ctx, repoID, &onMain)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, id))

	before, err := e.st.ListStacks(e.ctx, repoID, false)
	require.NoError(t, err)
	require.True(t, hasStack(before, gone), "the stack is live before it is dropped")

	e.gh.SetRef(repoName, "heads/main", newHeadSHA)
	resp, err := e.svc.RecordDefaultGraph(e.ctx, e.pushJob("main", newHeadSHA), repoName, v1.RecordGraphRequest{Graph: testGraph(newHeadSHA)})
	require.NoError(t, err)
	require.True(t, resp.Recorded)

	live, err := e.st.ListStacks(e.ctx, repoID, false)
	require.NoError(t, err)
	assert.False(t, hasStack(live, gone), "a stack the pushed graph no longer lists is dropped from the live list")
	all, err := e.st.ListStacks(e.ctx, repoID, true)
	require.NoError(t, err)
	for _, s := range all {
		if s.Key == gone {
			assert.NotNil(t, s.RemovedAt, "the dropped stack is stamped removed")
		}
	}
	ov, err := e.st.Overview(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, len(live), ov.Stacks, "the overview counts only live stacks")
}

func TestRecordDefaultGraphSurvivesDelayedMerge(t *testing.T) {
	e := newEnv(t, baseConfig())
	// An earlier pull request planned a graph at headSHA and is about to merge.
	e.planned(7, headSHA)

	// A direct push moves the default branch to newHeadSHA and records its graph.
	e.gh.SetRef(repoName, "heads/main", newHeadSHA)
	resp, err := e.svc.RecordDefaultGraph(e.ctx, e.pushJob("main", newHeadSHA), repoName, v1.RecordGraphRequest{Graph: testGraph(newHeadSHA)})
	require.NoError(t, err)
	require.True(t, resp.Recorded)
	g, _, err := e.st.GetDefaultGraph(e.ctx, repoID)
	require.NoError(t, err)
	require.Equal(t, newHeadSHA, g.SHA, "the pushed graph is the default-branch graph")

	// The earlier pull request's merge webhook arrives late, after the push.
	merged := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueClosed, Merged: true, MergeCommitSHA: mergeSHA, HeadSHA: headSHA, BaseSHA: baseSHA, User: gh.User{Login: author},
	})
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))

	after, _, err := e.st.GetDefaultGraph(e.ctx, repoID)
	require.NoError(t, err)
	assert.Equal(t, newHeadSHA, after.SHA, "a delayed merge must not replace the newer pushed graph")
}

func TestMergeReleasesLocksWhenHeadReadFails(t *testing.T) {
	e := newEnv(t, baseConfig())
	planRun := e.planned(7, headSHA)
	e.stalledApply(7, planRun)
	require.NotEmpty(t, e.locks(), "the before_merge apply holds the pull request's locks")

	e.gh.SetRef(repoName, "heads/main", mergeSHA)
	// The default-branch head read is down; recording the merged graph is retried
	// on webhook redelivery, but releasing the locks must not wait for it.
	e.gh.FailNext("GET /repos/{owner}/{repo}/git/ref/{ref...}", http.StatusInternalServerError, 20)

	merged := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueClosed, Merged: true, MergeCommitSHA: mergeSHA, HeadSHA: headSHA, BaseSHA: baseSHA, User: gh.User{Login: author},
	})
	err := e.svc.HandlePullRequest(e.ctx, merged)
	require.Error(t, err, "the handler reports the failed head read so the merge webhook is redelivered")
	assert.Empty(t, e.locks(), "the merge releases the pull request's locks even when the default-graph head read fails")
}
