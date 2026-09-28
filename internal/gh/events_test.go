package gh_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "events", name+".json"))
	require.NoError(t, err)
	return data
}

func parse[T any](t *testing.T, event, file string) *T {
	t.Helper()
	ev, err := gh.ParseEvent(event, fixture(t, file))
	require.NoError(t, err)
	typed, ok := ev.(*T)
	require.True(t, ok, "ParseEvent(%s) returned %T", event, ev)
	return typed
}

func TestParseEventCommonFields(t *testing.T) {
	tests := []struct {
		event, file    string
		action, repo   string
		sender         string
		installationID int64
	}{
		{gh.EventInstallation, "installation", "created", "", "octocat", 51234567},
		{gh.EventInstallationRepositories, "installation_repositories", "added", "", "octocat", 51234567},
		{gh.EventPullRequest, "pull_request", "synchronize", "acme/infra", "alice", 51234567},
		{gh.EventPullRequest, "pull_request_fork", "opened", "acme/infra", "mallory", 51234567},
		{gh.EventPullRequest, "pull_request_closed", "closed", "acme/infra", "bob", 51234567},
		{gh.EventPullRequestReview, "pull_request_review", "submitted", "acme/infra", "bob", 51234567},
		{gh.EventIssueComment, "issue_comment", "created", "acme/infra", "bob", 51234567},
		{gh.EventIssueComment, "issue_comment_issue", "created", "acme/infra", "carol", 51234567},
		{gh.EventPush, "push", "", "acme/infra", "alice", 51234567},
		{gh.EventPush, "push_tag", "", "acme/modules", "release-bot", 51234567},
		{gh.EventCheckRun, "check_run", "requested_action", "acme/infra", "bob", 51234567},
		{gh.EventCheckSuite, "check_suite", "rerequested", "acme/infra", "bob", 51234567},
		{gh.EventWorkflowRun, "workflow_run", "completed", "acme/infra", "stackorder[bot]", 51234567},
		{gh.EventWorkflowJob, "workflow_job", "completed", "acme/infra", "stackorder[bot]", 51234567},
		{gh.EventDeploymentProtectionRule, "deployment_protection_rule", "requested", "acme/infra", "stackorder[bot]", 51234567},
		{gh.EventPing, "ping", "", "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			ev, err := gh.ParseEvent(tt.event, fixture(t, tt.file))
			require.NoError(t, err)
			typed, ok := ev.(gh.Event)
			require.True(t, ok)
			c := typed.Common()
			assert.Equal(t, tt.action, c.Action)
			assert.Equal(t, tt.repo, c.RepoFullName())
			assert.Equal(t, tt.sender, c.Sender.Login)
			assert.Equal(t, tt.installationID, c.InstallationID())
		})
	}
}

func TestParseInstallationEvents(t *testing.T) {
	inst := parse[gh.InstallationEvent](t, gh.EventInstallation, "installation")
	require.NotNil(t, inst.Installation)
	assert.Equal(t, "acme", inst.Installation.Account.Login)
	assert.Equal(t, "Organization", inst.Installation.Account.Type)
	assert.Equal(t, int64(385163), inst.Installation.AppID)
	assert.Equal(t, "write", inst.Installation.Permissions["checks"])
	assert.Nil(t, inst.Installation.SuspendedAt)
	require.Len(t, inst.Repositories, 2)
	assert.Equal(t, "acme/network-infra", inst.Repositories[1].FullName)
	assert.Equal(t, int64(700002), inst.Repositories[1].ID)

	repos := parse[gh.InstallationRepositoriesEvent](t, gh.EventInstallationRepositories, "installation_repositories")
	assert.Equal(t, "selected", repos.RepositorySelection)
	require.Len(t, repos.RepositoriesAdded, 1)
	assert.Equal(t, "acme/modules", repos.RepositoriesAdded[0].FullName)
	assert.Empty(t, repos.RepositoriesRemoved)
}

func TestParsePullRequestEvents(t *testing.T) {
	ev := parse[gh.PullRequestEvent](t, gh.EventPullRequest, "pull_request")
	pr := ev.PullRequest
	assert.Equal(t, 42, ev.Number)
	assert.Equal(t, "6dcb09b5b57875f334f61aebed695e2e4193db5e", ev.After)
	assert.Equal(t, "1d3fbd3b8f2a44a07d3c1c3b8e0f67c2a1d9b0aa", ev.Before)
	assert.Equal(t, "6dcb09b5b57875f334f61aebed695e2e4193db5e", pr.HeadSHA)
	assert.Equal(t, "widen-vpc", pr.HeadRef)
	assert.Equal(t, "9049f1265b7d61be4a8904a9a27120d2064dab3b", pr.BaseSHA)
	assert.Equal(t, "main", pr.BaseRef)
	assert.Equal(t, "alice", pr.User.Login)
	assert.Equal(t, gh.Labels{"infra", "prod"}, pr.Labels)
	assert.Nil(t, pr.Mergeable)
	assert.Equal(t, "unknown", pr.MergeableState)
	assert.False(t, pr.Merged)
	assert.False(t, pr.IsFork())
	assert.Equal(t, "acme/infra", pr.Head.Repo.FullName)
	assert.Equal(t, "https://github.com/acme/infra/pull/42", pr.HTMLURL)
	require.NotNil(t, ev.Repository)
	assert.Equal(t, "main", ev.Repository.DefaultBranch)
	assert.True(t, ev.Repository.Private)

	fork := parse[gh.PullRequestEvent](t, gh.EventPullRequest, "pull_request_fork")
	assert.True(t, fork.PullRequest.Head.Repo.Fork)
	assert.Equal(t, "mallory/infra", fork.PullRequest.Head.Repo.FullName)
	assert.True(t, fork.PullRequest.IsFork())
	require.NotNil(t, fork.PullRequest.Mergeable)
	assert.True(t, *fork.PullRequest.Mergeable)

	closed := parse[gh.PullRequestEvent](t, gh.EventPullRequest, "pull_request_closed")
	assert.True(t, closed.PullRequest.Merged)
	assert.Equal(t, "e5bd3914e2e596debea16f433f57875b5b90bcd6", closed.PullRequest.MergeCommitSHA)
	require.NotNil(t, closed.PullRequest.MergedAt)
	assert.Equal(t, time.Date(2026, 9, 28, 9, 15, 0, 0, time.UTC), closed.PullRequest.MergedAt.UTC())
}

func TestParseReviewAndCommentEvents(t *testing.T) {
	rev := parse[gh.PullRequestReviewEvent](t, gh.EventPullRequestReview, "pull_request_review")
	assert.Equal(t, int64(2233445566), rev.Review.ID)
	assert.Equal(t, "bob", rev.Review.User.Login)
	assert.Equal(t, "approved", rev.Review.State)
	assert.True(t, rev.Review.Approved())
	assert.Equal(t, "6dcb09b5b57875f334f61aebed695e2e4193db5e", rev.Review.CommitID)
	assert.Equal(t, time.Date(2026, 9, 28, 8, 5, 44, 0, time.UTC), rev.Review.SubmittedAt)
	assert.Equal(t, 42, rev.PullRequest.Number)

	cm := parse[gh.IssueCommentEvent](t, gh.EventIssueComment, "issue_comment")
	assert.True(t, cm.IsPullRequest())
	assert.Equal(t, 42, cm.Issue.Number)
	assert.Equal(t, int64(2378901234), cm.Comment.ID)
	assert.Equal(t, "stackorder apply stacks/prod/vpc", cm.Comment.Body)
	assert.Equal(t, "bob", cm.Comment.User.Login)
	assert.Equal(t, "MEMBER", cm.Comment.AuthorAssociation)
	assert.Equal(t, gh.Labels{"infra"}, cm.Issue.Labels)

	issue := parse[gh.IssueCommentEvent](t, gh.EventIssueComment, "issue_comment_issue")
	assert.False(t, issue.IsPullRequest())
	assert.Equal(t, "NONE", issue.Comment.AuthorAssociation)
}

func TestParsePushEvents(t *testing.T) {
	push := parse[gh.PushEvent](t, gh.EventPush, "push")
	assert.False(t, push.IsTag())
	assert.Equal(t, "main", push.Branch())
	assert.Empty(t, push.TagName())
	assert.Equal(t, "e5bd3914e2e596debea16f433f57875b5b90bcd6", push.After)
	assert.False(t, push.Created)
	assert.False(t, push.Deleted)
	assert.Equal(t, []string{"stacks/prod/vpc/subnets.tf", "stacks/prod/vpc/main.tf", "stackorder.yaml", "stacks/prod/legacy/main.tf"}, push.Paths())
	assert.Equal(t, "alice", push.Pusher.Name)
	require.NotNil(t, push.HeadCommit)
	assert.Equal(t, push.After, push.HeadCommit.ID)

	tag := parse[gh.PushEvent](t, gh.EventPush, "push_tag")
	assert.True(t, tag.IsTag())
	assert.Equal(t, "v1.3.0", tag.TagName())
	assert.Empty(t, tag.Branch())
	assert.True(t, tag.Created)
	assert.Equal(t, "refs/heads/main", tag.BaseRef)
	assert.Empty(t, tag.Paths())
}

func TestParseCheckEvents(t *testing.T) {
	cr := parse[gh.CheckRunEvent](t, gh.EventCheckRun, "check_run")
	assert.Equal(t, "stackorder/plan: stacks/prod/vpc", cr.CheckRun.Name)
	assert.Equal(t, "6dcb09b5b57875f334f61aebed695e2e4193db5e", cr.CheckRun.HeadSHA)
	assert.Equal(t, "8b0f0a3e-3c5e-4bd2-9f5e-2a1c9a4e7d11", cr.CheckRun.ExternalID)
	assert.Equal(t, gh.ConclusionSuccess, cr.CheckRun.Conclusion)
	assert.Equal(t, "Plan: 1 to add, 1 to change, 0 to destroy.", cr.CheckRun.Output.Summary)
	require.NotNil(t, cr.RequestedAction)
	assert.Equal(t, "apply", cr.RequestedAction.Identifier)
	require.NotNil(t, cr.CheckRun.App)
	assert.Equal(t, int64(385163), cr.CheckRun.App.ID)
	require.Len(t, cr.CheckRun.PullRequests, 1)
	assert.Equal(t, 42, cr.CheckRun.PullRequests[0].Number)
	assert.Equal(t, "widen-vpc", cr.CheckRun.PullRequests[0].Head.Ref)
	require.NotNil(t, cr.CheckRun.CheckSuite)
	assert.Equal(t, int64(118578147), cr.CheckRun.CheckSuite.ID)

	cs := parse[gh.CheckSuiteEvent](t, gh.EventCheckSuite, "check_suite")
	assert.Equal(t, "6dcb09b5b57875f334f61aebed695e2e4193db5e", cs.CheckSuite.HeadSHA)
	assert.Equal(t, "widen-vpc", cs.CheckSuite.HeadBranch)
	assert.Equal(t, "failure", cs.CheckSuite.Conclusion)
	require.Len(t, cs.CheckSuite.PullRequests, 1)
	assert.Equal(t, 42, cs.CheckSuite.PullRequests[0].Number)
	assert.Equal(t, "stackorder", cs.CheckSuite.App.Slug)
}

func TestParseWorkflowEvents(t *testing.T) {
	wr := parse[gh.WorkflowRunEvent](t, gh.EventWorkflowRun, "workflow_run")
	run := wr.WorkflowRun
	assert.Equal(t, int64(11223344556), run.ID)
	assert.Equal(t, "stackorder run", run.Name)
	assert.Equal(t, ".github/workflows/stackorder-run.yml", run.Path)
	assert.Equal(t, "workflow_dispatch", run.Event)
	assert.Equal(t, gh.RunStatusCompleted, run.Status)
	assert.Equal(t, gh.ConclusionFailure, run.Conclusion)
	assert.Equal(t, 2, run.RunAttempt)
	assert.Equal(t, "https://github.com/acme/infra/actions/runs/11223344556", run.HTMLURL)
	assert.Equal(t, "stackorder apply 8b0f0a3e wave 0", run.DisplayTitle)
	assert.Equal(t, time.Date(2026, 9, 28, 9, 24, 0, 0, time.UTC), run.RunStartedAt)
	require.NotNil(t, wr.Workflow)
	assert.Equal(t, int64(99887766), wr.Workflow.ID)

	wj := parse[gh.WorkflowJobEvent](t, gh.EventWorkflowJob, "workflow_job")
	job := wj.WorkflowJob
	assert.Equal(t, int64(31415926535), job.ID)
	assert.Equal(t, int64(11223344556), job.RunID)
	assert.Equal(t, "run / apply (stacks/prod/vpc)", job.Name)
	assert.Equal(t, gh.ConclusionFailure, job.Conclusion)
	assert.Equal(t, "GitHub Actions 12", job.RunnerName)
	assert.Equal(t, []string{"ubuntu-latest"}, job.Labels)
	require.Len(t, job.Steps, 2)
	assert.Equal(t, 4, job.Steps[1].Number)
	assert.Equal(t, "failure", job.Steps[1].Conclusion)
	assert.Equal(t, 157*time.Second, job.CompletedAt.Sub(job.StartedAt))
}

func TestParseDeploymentProtectionRuleEvent(t *testing.T) {
	ev := parse[gh.DeploymentProtectionRuleEvent](t, gh.EventDeploymentProtectionRule, "deployment_protection_rule")
	assert.Equal(t, "production", ev.Environment)
	assert.Equal(t, "workflow_dispatch", ev.Event)
	require.NotNil(t, ev.Deployment)
	assert.Equal(t, int64(1777888999), ev.Deployment.ID)
	assert.Equal(t, "e5bd3914e2e596debea16f433f57875b5b90bcd6", ev.Deployment.SHA)
	assert.Equal(t, "main", ev.Deployment.Ref)
	runID, err := ev.RunID()
	require.NoError(t, err)
	assert.Equal(t, int64(11223344556), runID)
}

func TestParsePingEvent(t *testing.T) {
	ping := parse[gh.PingEvent](t, gh.EventPing, "ping")
	assert.Equal(t, "Keep it logically awesome.", ping.Zen)
	assert.Equal(t, int64(109948940), ping.HookID)
}

func TestParseEventErrors(t *testing.T) {
	_, err := gh.ParseEvent("star", []byte(`{}`))
	require.ErrorIs(t, err, gh.ErrUnsupportedEvent)

	_, err = gh.ParseEvent(gh.EventPush, []byte(`{"ref": 5}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode push event")
}

func TestEventCommonWithoutInstallationOrRepository(t *testing.T) {
	var c gh.EventCommon
	assert.Zero(t, c.InstallationID())
	assert.Empty(t, c.RepoFullName())
	assert.Same(t, &c, c.Common())
}
