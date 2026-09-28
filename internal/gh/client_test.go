package gh_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

const repo = "acme/infra"

func setup(t *testing.T) (*ghfake.Server, *gh.Client) {
	t.Helper()
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	c, err := fake.NewApp().Client(context.Background(), 1)
	require.NoError(t, err)
	return fake, c
}

func boolPtr(b bool) *bool { return &b }

func TestInvalidRepositoryName(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	for _, bad := range []string{"", "acme", "/infra", "acme/", "acme/infra/extra"} {
		_, err := c.GetRepository(ctx, bad)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "owner/name")
	}
}

func TestInvalidRepositoryNameEveryMethod(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	const bad = "no-slash"
	calls := map[string]func() error{
		"CreateCheckRun": func() error { _, err := c.CreateCheckRun(ctx, bad, gh.CheckRunParams{}); return err },
		"UpdateCheckRun": func() error { _, err := c.UpdateCheckRun(ctx, bad, 1, gh.CheckRunParams{}); return err },
		"ListCheckRuns":  func() error { _, err := c.ListCheckRunsForRef(ctx, bad, "x", ""); return err },
		"GetPull":        func() error { _, err := c.GetPull(ctx, bad, 1); return err },
		"ListReviews":    func() error { _, err := c.ListReviews(ctx, bad, 1); return err },
		"ListPullFiles":  func() error { _, err := c.ListPullFiles(ctx, bad, 1); return err },
		"ListComments":   func() error { _, err := c.ListIssueComments(ctx, bad, 1); return err },
		"CreateComment":  func() error { _, err := c.CreateIssueComment(ctx, bad, 1, "x"); return err },
		"UpdateComment":  func() error { _, err := c.UpdateIssueComment(ctx, bad, 1, "x"); return err },
		"DeleteComment":  func() error { return c.DeleteIssueComment(ctx, bad, 1) },
		"Sticky":         func() error { _, err := c.UpsertStickyComment(ctx, bad, 1, "<!-- m -->", "x"); return err },
		"Reaction":       func() error { return c.CreateReaction(ctx, bad, 1, gh.ReactionEyes) },
		"CreateIssue":    func() error { _, err := c.CreateIssue(ctx, bad, gh.IssueParams{}); return err },
		"UpdateIssue":    func() error { _, err := c.UpdateIssue(ctx, bad, 1, gh.IssueParams{}); return err },
		"ListIssues":     func() error { _, err := c.ListIssues(ctx, bad, nil, ""); return err },
		"Permission":     func() error { _, err := c.CollaboratorPermission(ctx, bad, "u"); return err },
		"Dispatch":       func() error { return c.DispatchWorkflow(ctx, bad, "w.yml", "main", nil) },
		"ListRuns":       func() error { _, err := c.ListWorkflowRuns(ctx, bad, gh.ListWorkflowRunsParams{}); return err },
		"GetRun":         func() error { _, err := c.GetWorkflowRun(ctx, bad, 1); return err },
		"ListJobs":       func() error { _, err := c.ListWorkflowJobs(ctx, bad, 1); return err },
		"ListArtifacts":  func() error { _, err := c.ListArtifacts(ctx, bad, 1); return err },
		"ReviewRule":     func() error { return c.ReviewDeploymentProtectionRule(ctx, bad, 1, "e", "approved", "") },
		"Pending":        func() error { _, err := c.ListPendingDeployments(ctx, bad, 1); return err },
		"GetContents":    func() error { _, err := c.GetContents(ctx, bad, "p", ""); return err },
		"ListTags":       func() error { _, err := c.ListTags(ctx, bad); return err },
		"GetRef":         func() error { _, err := c.GetRef(ctx, bad, "heads/main"); return err },
		"Codeowners":     func() error { _, err := c.CodeownersFor(ctx, bad, ""); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "owner/name")
		})
	}
}

func TestGetRepository(t *testing.T) {
	fake, c := setup(t)
	fake.SetRepo(repo, gh.Repository{ID: 77, DefaultBranch: "trunk", Private: true})
	r, err := c.GetRepository(context.Background(), repo)
	require.NoError(t, err)
	assert.Equal(t, int64(77), r.ID)
	assert.Equal(t, "infra", r.Name)
	assert.Equal(t, "acme", r.Owner.Login)
	assert.Equal(t, "trunk", r.DefaultBranch)
	assert.True(t, r.Private)
	assert.False(t, r.Fork)

	_, err = c.GetRepository(context.Background(), "acme/missing")
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestInstallationTokenCannotReachOtherInstallation(t *testing.T) {
	fake, c := setup(t)
	fake.AddInstallation(2, "globex", "globex/platform")
	_, err := c.GetRepository(context.Background(), "globex/platform")
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestCheckRuns(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	started := time.Date(2026, 9, 28, 10, 0, 0, 123456789, time.FixedZone("CEST", 2*3600))

	run, err := c.CreateCheckRun(ctx, repo, gh.CheckRunParams{
		Name:       "stackorder/plan: stacks/prod/vpc",
		HeadSHA:    "abc123",
		Status:     gh.CheckRunInProgress,
		DetailsURL: "https://stackorder.example.com/runs/r1",
		ExternalID: "r1",
		Output:     gh.CheckRunOutput{Title: "Planning", Summary: "Planning 1 stack"},
		StartedAt:  started,
		Actions:    []gh.CheckRunAction{{Label: "Apply", Description: "Apply this stack", Identifier: "apply"}},
	})
	require.NoError(t, err)
	assert.NotZero(t, run.ID)
	assert.Equal(t, gh.CheckRunInProgress, run.Status)
	assert.Equal(t, "r1", run.ExternalID)
	assert.Equal(t, time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC), run.StartedAt)
	require.NotNil(t, run.App)
	assert.Equal(t, "stackorder-test", run.App.Slug)

	completed := started.Add(time.Minute)
	updated, err := c.UpdateCheckRun(ctx, repo, run.ID, gh.CheckRunParams{
		HeadSHA:     "ignored-on-update",
		Conclusion:  gh.ConclusionSuccess,
		CompletedAt: completed,
		Output:      gh.CheckRunOutput{Title: "1 to add", Summary: "Plan: 1 to add", Text: "```\n+ aws_vpc.main\n```"},
	})
	require.NoError(t, err)
	assert.Equal(t, gh.CheckRunCompleted, updated.Status)
	assert.Equal(t, gh.ConclusionSuccess, updated.Conclusion)
	assert.Equal(t, "Plan: 1 to add", updated.Output.Summary)

	_, err = c.CreateCheckRun(ctx, repo, gh.CheckRunParams{Name: "stackorder/plan", HeadSHA: "abc123", Status: gh.CheckRunQueued})
	require.NoError(t, err)
	_, err = c.CreateCheckRun(ctx, repo, gh.CheckRunParams{Name: "stackorder/plan", HeadSHA: "other", Status: gh.CheckRunQueued})
	require.NoError(t, err)

	stored := fake.CheckRuns(repo)
	require.Len(t, stored, 3)
	require.Len(t, stored[0].History, 2)
	assert.Equal(t, "abc123", stored[0].History[0].HeadSHA)
	assert.Empty(t, stored[0].History[1].HeadSHA)
	assert.Equal(t, time.Date(2026, 9, 28, 8, 1, 0, 0, time.UTC), stored[0].History[1].CompletedAt)
	assert.Equal(t, "apply", stored[0].History[0].Actions[0].Identifier)

	byName, err := c.ListCheckRunsForRef(ctx, repo, "abc123", "stackorder/plan: stacks/prod/vpc")
	require.NoError(t, err)
	require.Len(t, byName, 1)
	assert.Equal(t, run.ID, byName[0].ID)
	assert.Equal(t, gh.ConclusionSuccess, byName[0].Conclusion)

	all, err := c.ListCheckRunsForRef(ctx, repo, "abc123", "")
	require.NoError(t, err)
	assert.Len(t, all, 2)

	_, err = c.UpdateCheckRun(ctx, repo, 999999, gh.CheckRunParams{Status: gh.CheckRunQueued})
	require.ErrorIs(t, err, gh.ErrNotFound)

	_, err = c.CreateCheckRun(ctx, repo, gh.CheckRunParams{HeadSHA: "abc123"})
	var apiErr *gh.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
	assert.Contains(t, apiErr.Error(), "name and head_sha are required")
}

func TestCheckRunOutputIsTruncated(t *testing.T) {
	fake, c := setup(t)
	long := strings.Repeat("é", gh.MaxCheckOutput)
	_, err := c.CreateCheckRun(context.Background(), repo, gh.CheckRunParams{
		Name: "n", HeadSHA: "s", Output: gh.CheckRunOutput{Title: "t", Summary: long, Text: long},
	})
	require.NoError(t, err)
	sent := fake.CheckRuns(repo)[0].History[0].Output
	assert.LessOrEqual(t, len(sent.Summary), gh.MaxCheckOutput)
	assert.Greater(t, len(sent.Summary), gh.MaxCheckOutput-2)
	assert.True(t, strings.HasPrefix(long, sent.Summary))
	assert.Equal(t, sent.Summary, sent.Text)
}

func TestGetPull(t *testing.T) {
	fake, c := setup(t)
	fork := &gh.Repository{ID: 900, FullName: "someone/infra", Fork: true}
	fake.SetPull(repo, gh.PullRequest{
		Number:         7,
		Title:          "Bump VPC",
		HeadSHA:        "head7",
		HeadRef:        "bump-vpc",
		BaseSHA:        "base7",
		Mergeable:      boolPtr(true),
		MergeableState: "clean",
		Draft:          true,
		User:           gh.User{Login: "alice"},
		Labels:         gh.Labels{"infra", "prod"},
	})
	fake.SetPull(repo, gh.PullRequest{Number: 8, HeadSHA: "head8", HeadRef: "patch-1", Head: gh.PullBranch{Repo: fork}, User: gh.User{Login: "mallory"}})
	ctx := context.Background()

	pr, err := c.GetPull(ctx, repo, 7)
	require.NoError(t, err)
	assert.Equal(t, 7, pr.Number)
	assert.Equal(t, "head7", pr.HeadSHA)
	assert.Equal(t, "bump-vpc", pr.HeadRef)
	assert.Equal(t, "base7", pr.BaseSHA)
	assert.Equal(t, "main", pr.BaseRef)
	assert.Equal(t, gh.IssueOpen, pr.State)
	require.NotNil(t, pr.Mergeable)
	assert.True(t, *pr.Mergeable)
	assert.Equal(t, "clean", pr.MergeableState)
	assert.True(t, pr.Draft)
	assert.Equal(t, "alice", pr.User.Login)
	assert.Equal(t, gh.Labels{"infra", "prod"}, pr.Labels)
	require.NotNil(t, pr.Head.Repo)
	assert.Equal(t, repo, pr.Head.Repo.FullName)
	assert.False(t, pr.Head.Repo.Fork)
	assert.False(t, pr.IsFork())
	assert.Equal(t, "https://github.com/acme/infra/pull/7", pr.HTMLURL)

	forked, err := c.GetPull(ctx, repo, 8)
	require.NoError(t, err)
	assert.Equal(t, "someone/infra", forked.Head.Repo.FullName)
	assert.True(t, forked.Head.Repo.Fork)
	assert.True(t, forked.IsFork())
	assert.Nil(t, forked.Mergeable)

	_, err = c.GetPull(ctx, repo, 99)
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestIsFork(t *testing.T) {
	base := &gh.Repository{FullName: "acme/infra"}
	tests := []struct {
		name string
		pr   gh.PullRequest
		want bool
	}{
		{"same repo", gh.PullRequest{Head: gh.PullBranch{Repo: base}, Base: gh.PullBranch{Repo: base}}, false},
		{"other repo", gh.PullRequest{Head: gh.PullBranch{Repo: &gh.Repository{FullName: "x/infra", Fork: true}}, Base: gh.PullBranch{Repo: base}}, true},
		{"case insensitive", gh.PullRequest{Head: gh.PullBranch{Repo: &gh.Repository{FullName: "ACME/Infra"}}, Base: gh.PullBranch{Repo: base}}, false},
		{"deleted fork", gh.PullRequest{Base: gh.PullBranch{Repo: base}}, true},
		{"fork flag without base", gh.PullRequest{Head: gh.PullBranch{Repo: &gh.Repository{FullName: "x/infra", Fork: true}}}, true},
		{"no base repo", gh.PullRequest{Head: gh.PullBranch{Repo: base}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.pr.IsFork())
		})
	}
}

func TestPullRequestJSONRoundTrip(t *testing.T) {
	in := gh.PullRequest{
		Number: 3, State: "open", HeadSHA: "h", HeadRef: "hr", BaseSHA: "b", BaseRef: "br",
		Head: gh.PullBranch{Repo: &gh.Repository{FullName: "f/r", Fork: true}}, Labels: gh.Labels{"a"},
	}
	data, err := json.Marshal(in)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	head := wire["head"].(map[string]any)
	assert.Equal(t, "h", head["sha"])
	assert.Equal(t, "hr", head["ref"])
	assert.Equal(t, "f/r", head["repo"].(map[string]any)["full_name"])
	assert.Equal(t, []any{map[string]any{"name": "a"}}, wire["labels"])
	assert.NotContains(t, wire, "HeadSHA")

	var out gh.PullRequest
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, in, out)
	require.Error(t, json.Unmarshal([]byte(`{"number":"x"}`), &out))
}

func TestLabelsJSON(t *testing.T) {
	var l gh.Labels
	require.NoError(t, json.Unmarshal([]byte(`[{"name":"a","color":"fff"},"b"]`), &l))
	assert.Equal(t, gh.Labels{"a", "b"}, l)
	require.Error(t, json.Unmarshal([]byte(`{"name":"a"}`), &l))
	require.Error(t, json.Unmarshal([]byte(`[1]`), &l))
}

func TestReviewsPaginateAcrossThreePages(t *testing.T) {
	fake, c := setup(t)
	fake.SetPageSize(2)
	fake.SetPull(repo, gh.PullRequest{Number: 5, HeadSHA: "h"})
	submitted := time.Date(2026, 9, 28, 11, 0, 0, 0, time.UTC)
	logins := []string{"a", "b", "c", "d", "e"}
	reviews := make([]gh.Review, 0, len(logins))
	for i, login := range logins {
		reviews = append(reviews, gh.Review{ID: int64(i + 1), User: gh.User{Login: login}, State: gh.ReviewApproved, CommitID: "h", SubmittedAt: submitted})
	}
	reviews[2].State = gh.ReviewChangesRequested
	fake.SetReviews(repo, 5, reviews)

	got, err := c.ListReviews(context.Background(), repo, 5)
	require.NoError(t, err)
	assert.Equal(t, reviews, got)
	assert.Equal(t, 3, countRequests(fake, "GET /repos/{owner}/{repo}/pulls/{pull_number}/reviews"))
	assert.True(t, got[0].Approved())
	assert.False(t, got[2].Approved())
	assert.True(t, gh.Review{State: "approved"}.Approved())

	_, err = c.ListReviews(context.Background(), repo, 6)
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestListPullFiles(t *testing.T) {
	fake, c := setup(t)
	fake.SetPull(repo, gh.PullRequest{Number: 5})
	fake.SetFiles(repo, 5, []string{"stacks/prod/vpc/main.tf", "modules/vpc/variables.tf", "stacks/prod/vpc/main.tf"})
	got, err := c.ListPullFiles(context.Background(), repo, 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"stacks/prod/vpc/main.tf", "modules/vpc/variables.tf"}, got)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"filename":"stacks/new/main.tf","previous_filename":"stacks/old/main.tf","status":"renamed"},{"filename":"README.md","status":"modified"}]`))
	}))
	defer srv.Close()
	tc, err := gh.NewTokenClient(gh.Config{BaseURL: srv.URL}, "tok")
	require.NoError(t, err)
	renamed, err := tc.ListPullFiles(context.Background(), repo, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"stacks/new/main.tf", "stacks/old/main.tf", "README.md"}, renamed)

	_, err = c.ListPullFiles(context.Background(), repo, 404)
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestComments(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()

	cm, err := c.CreateIssueComment(ctx, repo, 3, "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello", cm.Body)
	assert.Equal(t, "stackorder-test[bot]", cm.User.Login)
	assert.Equal(t, "Bot", cm.User.Type)

	up, err := c.UpdateIssueComment(ctx, repo, cm.ID, "hello again")
	require.NoError(t, err)
	assert.Equal(t, "hello again", up.Body)

	require.NoError(t, c.CreateReaction(ctx, repo, cm.ID, gh.ReactionEyes))
	require.NoError(t, c.CreateReaction(ctx, repo, cm.ID, gh.ReactionRocket))
	require.NoError(t, c.CreateReaction(ctx, repo, cm.ID, gh.ReactionEyes))
	assert.Equal(t, []string{"eyes", "rocket"}, fake.Reactions(cm.ID))
	var apiErr *gh.APIError
	require.ErrorAs(t, c.CreateReaction(ctx, repo, cm.ID, "thumbs"), &apiErr)
	assert.Equal(t, 422, apiErr.Status)

	listed, err := c.ListIssueComments(ctx, repo, 3)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "hello again", listed[0].Body)

	require.NoError(t, c.DeleteIssueComment(ctx, repo, cm.ID))
	assert.Empty(t, fake.Comments(repo, 3))
	require.ErrorIs(t, c.DeleteIssueComment(ctx, repo, cm.ID), gh.ErrNotFound)
	_, err = c.UpdateIssueComment(ctx, repo, cm.ID, "x")
	require.ErrorIs(t, err, gh.ErrNotFound)
	require.ErrorIs(t, c.CreateReaction(ctx, repo, cm.ID, gh.ReactionEyes), gh.ErrNotFound)
}

func TestUpsertStickyComment(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	const marker = "<!-- stackorder:sticky -->"
	human := fake.AddComment(repo, 4, "alice", marker+"\nquoting the bot")

	created, err := c.UpsertStickyComment(ctx, repo, 4, marker, "## Plan\n1 stack")
	require.NoError(t, err)
	assert.Equal(t, marker+"\n## Plan\n1 stack", created.Body)
	assert.NotEqual(t, human.ID, created.ID)

	updated, err := c.UpsertStickyComment(ctx, repo, 4, marker, marker+"\n## Plan\n2 stacks")
	require.NoError(t, err)
	assert.Equal(t, created.ID, updated.ID)
	assert.Equal(t, marker+"\n## Plan\n2 stacks", updated.Body)

	patches := countRequests(fake, "PATCH /repos/{owner}/{repo}/issues/comments/{comment_id}")
	same, err := c.UpsertStickyComment(ctx, repo, 4, marker, "## Plan\n2 stacks")
	require.NoError(t, err)
	assert.Equal(t, created.ID, same.ID)
	assert.Equal(t, patches, countRequests(fake, "PATCH /repos/{owner}/{repo}/issues/comments/{comment_id}"))

	comments := fake.Comments(repo, 4)
	require.Len(t, comments, 2)
	assert.Equal(t, marker+"\nquoting the bot", comments[0].Body)
	assert.Equal(t, marker+"\n## Plan\n2 stacks", comments[1].Body)
	assert.Equal(t, 1, countRequests(fake, "POST /repos/{owner}/{repo}/issues/{issue_number}/comments"))
}

func TestUpsertStickyCommentRemovesDuplicates(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	const marker = "<!-- stackorder:sticky -->"
	first, err := c.CreateIssueComment(ctx, repo, 9, marker+"\nold")
	require.NoError(t, err)
	_, err = c.CreateIssueComment(ctx, repo, 9, "unrelated bot comment")
	require.NoError(t, err)
	_, err = c.CreateIssueComment(ctx, repo, 9, "\n"+marker+"\nrace duplicate")
	require.NoError(t, err)

	got, err := c.UpsertStickyComment(ctx, repo, 9, marker, "new")
	require.NoError(t, err)
	assert.Equal(t, first.ID, got.ID)
	comments := fake.Comments(repo, 9)
	bodies := make([]string, 0, len(comments))
	for _, cm := range comments {
		bodies = append(bodies, cm.Body)
	}
	assert.Equal(t, []string{marker + "\nnew", "unrelated bot comment"}, bodies)
}

func TestUpsertStickyCommentPropagatesErrors(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	const marker = "<!-- m -->"
	fake.FailNext("GET /repos/{owner}/{repo}/issues/{issue_number}/comments", 404, 1)
	_, err := c.UpsertStickyComment(ctx, repo, 1, marker, "x")
	require.ErrorIs(t, err, gh.ErrNotFound)

	_, err = c.CreateIssueComment(ctx, repo, 1, marker+"\na")
	require.NoError(t, err)
	_, err = c.CreateIssueComment(ctx, repo, 1, marker+"\nb")
	require.NoError(t, err)
	fake.FailNext("DELETE /repos/{owner}/{repo}/issues/comments/{comment_id}", 403, 1)
	_, err = c.UpsertStickyComment(ctx, repo, 1, marker, "c")
	var apiErr *gh.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Status)
}

func TestCollaboratorPermission(t *testing.T) {
	fake, c := setup(t)
	for login, perm := range map[string]string{"root": "admin", "dev": "write", "lead": "maintain", "tri": "triage", "ro": "read"} {
		fake.SetCollaboratorPermission(repo, login, perm)
	}
	tests := []struct {
		login string
		want  string
		push  bool
	}{
		{"root", gh.PermissionAdmin, true},
		{"dev", gh.PermissionWrite, true},
		{"lead", gh.PermissionWrite, true},
		{"tri", gh.PermissionRead, false},
		{"ro", gh.PermissionRead, false},
		{"stranger", gh.PermissionNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			got, err := c.CollaboratorPermission(context.Background(), repo, tt.login)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.push, gh.HasPushPermission(got))
		})
	}
	assert.True(t, gh.HasPushPermission("maintain"))
	assert.False(t, gh.HasPushPermission(""))

	fake.FailNext("GET /repos/{owner}/{repo}/collaborators/{username}/permission", 404, 1)
	got, err := c.CollaboratorPermission(context.Background(), repo, "ghost")
	require.NoError(t, err)
	assert.Equal(t, gh.PermissionNone, got)

	fake.FailNext("GET /repos/{owner}/{repo}/collaborators/{username}/permission", 403, 1)
	_, err = c.CollaboratorPermission(context.Background(), repo, "ghost")
	require.Error(t, err)
}

func TestCollaboratorPermissionLegacyValues(t *testing.T) {
	tests := []struct{ body, want string }{
		{`{"permission":"maintain"}`, gh.PermissionWrite},
		{`{"permission":"triage"}`, gh.PermissionRead},
		{`{"permission":"weird"}`, gh.PermissionNone},
	}
	for _, tt := range tests {
		t.Run(tt.want+tt.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			defer srv.Close()
			c, err := gh.NewTokenClient(gh.Config{BaseURL: srv.URL}, "t")
			require.NoError(t, err)
			got, err := c.CollaboratorPermission(context.Background(), repo, "u")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTeamMembership(t *testing.T) {
	fake, c := setup(t)
	fake.SetTeamMembership("acme", "platform-prod", "alice", gh.MembershipActive)
	fake.SetTeamMembership("acme", "platform-prod", "bob", gh.MembershipPending)
	fake.SetTeamMembership("acme", "platform-prod", "carol", gh.MembershipActive)
	fake.SetTeamMembership("acme", "platform-prod", "carol", gh.MembershipNone)
	tests := []struct{ login, want string }{
		{"alice", gh.MembershipActive},
		{"Alice", gh.MembershipActive},
		{"bob", gh.MembershipPending},
		{"carol", gh.MembershipNone},
		{"dave", gh.MembershipNone},
	}
	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			got, err := c.TeamMembership(context.Background(), "acme", "platform-prod", tt.login)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	fake.FailNext("GET /orgs/{org}/teams/{team_slug}/memberships/{username}", 500, 5)
	_, err := c.TeamMembership(context.Background(), "acme", "platform-prod", "alice")
	require.Error(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	tc, err := gh.NewTokenClient(gh.Config{BaseURL: srv.URL}, "t")
	require.NoError(t, err)
	got, err := tc.TeamMembership(context.Background(), "o", "t", "u")
	require.NoError(t, err)
	assert.Equal(t, gh.MembershipNone, got)
}

func TestDispatchAndWorkflowRuns(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	fake.SetRef(repo, "heads/main", "mainsha")
	var hooked []ghfake.Dispatch
	fake.OnDispatch(func(d ghfake.Dispatch) { hooked = append(hooked, d) })

	inputs := map[string]string{"run_id": "r-1", "mode": "apply", "wave": "0", "sha": "abc", "stacks": `[{"key":"stacks/prod/vpc"}]`}
	before := time.Now().Add(-time.Second)
	require.NoError(t, c.DispatchWorkflow(ctx, repo, "stackorder-run.yml", "main", inputs))

	ds := fake.Dispatches()
	require.Len(t, ds, 1)
	assert.Equal(t, repo, ds[0].Repo)
	assert.Equal(t, "stackorder-run.yml", ds[0].Workflow)
	assert.Equal(t, "main", ds[0].Ref)
	assert.Equal(t, inputs, ds[0].Inputs)
	assert.Equal(t, ds, hooked)

	runs, err := c.ListWorkflowRuns(ctx, repo, gh.ListWorkflowRunsParams{Workflow: "stackorder-run.yml", Event: "workflow_dispatch", CreatedAfter: before})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, ds[0].RunID, runs[0].ID)
	assert.Equal(t, "mainsha", runs[0].HeadSHA)
	assert.Equal(t, ".github/workflows/stackorder-run.yml", runs[0].Path)
	assert.Equal(t, gh.RunStatusQueued, runs[0].Status)
	assert.Equal(t, 1, runs[0].RunAttempt)

	var apiErr *gh.APIError
	require.ErrorAs(t, c.DispatchWorkflow(ctx, repo, "stackorder-run.yml", "nope", nil), &apiErr)
	assert.Equal(t, 422, apiErr.Status)
	require.Error(t, c.DispatchWorkflow(ctx, repo, "", "main", nil))
	require.Error(t, c.DispatchWorkflow(ctx, repo, "stackorder-run.yml", "", nil))

	fake.CompleteWorkflowRun(repo, runs[0].ID, gh.ConclusionSuccess)
	got, err := c.GetWorkflowRun(ctx, repo, runs[0].ID)
	require.NoError(t, err)
	assert.Equal(t, gh.RunStatusCompleted, got.Status)
	assert.Equal(t, gh.ConclusionSuccess, got.Conclusion)

	_, err = c.GetWorkflowRun(ctx, repo, 1)
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestListWorkflowRunsFilters(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	add := func(path, event, branch, sha, status, conclusion string, at time.Time) int64 {
		return fake.AddWorkflowRun(repo, gh.WorkflowRun{Path: path, Event: event, HeadBranch: branch, HeadSHA: sha, Status: status, Conclusion: conclusion, CreatedAt: at}).ID
	}
	plan1 := add(".github/workflows/stackorder-plan.yml", "pull_request", "feat", "s1", "completed", "success", t0)
	run1 := add(".github/workflows/stackorder-run.yml", "workflow_dispatch", "main", "s1", "in_progress", "", t0.Add(time.Minute))
	run2 := add(".github/workflows/stackorder-run.yml", "workflow_dispatch", "main", "s2", "completed", "failure", t0.Add(2*time.Minute))
	ci := add(".github/workflows/ci.yml", "push", "main", "s2", "completed", "success", t0.Add(3*time.Minute))

	ids := func(runs []gh.WorkflowRun) []int64 {
		out := make([]int64, 0, len(runs))
		for _, r := range runs {
			out = append(out, r.ID)
		}
		return out
	}
	tests := []struct {
		name string
		p    gh.ListWorkflowRunsParams
		want []int64
	}{
		{"all newest first", gh.ListWorkflowRunsParams{}, []int64{ci, run2, run1, plan1}},
		{"workflow", gh.ListWorkflowRunsParams{Workflow: "stackorder-run.yml"}, []int64{run2, run1}},
		{"event", gh.ListWorkflowRunsParams{Event: "pull_request"}, []int64{plan1}},
		{"branch", gh.ListWorkflowRunsParams{Branch: "main"}, []int64{ci, run2, run1}},
		{"head sha", gh.ListWorkflowRunsParams{HeadSHA: "s1"}, []int64{run1, plan1}},
		{"status", gh.ListWorkflowRunsParams{Status: "in_progress"}, []int64{run1}},
		{"conclusion as status", gh.ListWorkflowRunsParams{Status: "failure"}, []int64{run2}},
		{"created after", gh.ListWorkflowRunsParams{CreatedAfter: t0.Add(2 * time.Minute)}, []int64{ci, run2}},
		{"limit", gh.ListWorkflowRunsParams{Limit: 2}, []int64{ci, run2}},
		{"combined", gh.ListWorkflowRunsParams{Workflow: "stackorder-run.yml", HeadSHA: "s2", Status: "completed"}, []int64{run2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs, err := c.ListWorkflowRuns(ctx, repo, tt.p)
			require.NoError(t, err)
			assert.Equal(t, tt.want, ids(runs))
		})
	}
}

func TestJobsArtifactsAndPendingDeployments(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	run := fake.AddWorkflowRun(repo, gh.WorkflowRun{Path: ".github/workflows/stackorder-run.yml", Event: "workflow_dispatch"})
	started := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	fake.SetJobs(repo, run.ID, []gh.WorkflowJob{{
		Name: "apply (stacks/prod/vpc)", Status: "completed", Conclusion: "success", StartedAt: started, CompletedAt: started.Add(time.Minute),
		RunnerName: "GitHub Actions 2", Steps: []gh.WorkflowStep{{Number: 1, Name: "Set up job", Status: "completed", Conclusion: "success"}},
	}})
	expires := started.Add(90 * 24 * time.Hour)
	fake.SetArtifacts(repo, run.ID, []gh.Artifact{{Name: "stackorder-plan-stacks-prod-vpc-abc", SizeInBytes: 2048, ExpiresAt: expires}})
	fake.SetPendingDeployments(repo, run.ID, []gh.PendingDeployment{{
		Environment:           gh.Environment{ID: 3, Name: "production", HTMLURL: "https://github.com/acme/infra/deployments/activity_log?environments_filter=production"},
		CurrentUserCanApprove: false,
		Reviewers:             []gh.DeploymentReviewer{{Type: "Team", Reviewer: gh.Reviewer{Slug: "platform-prod"}}},
	}})

	jobs, err := c.ListWorkflowJobs(ctx, repo, run.ID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, run.ID, jobs[0].RunID)
	assert.Equal(t, "GitHub Actions 2", jobs[0].RunnerName)
	assert.Equal(t, "Set up job", jobs[0].Steps[0].Name)
	assert.Equal(t, started, jobs[0].StartedAt)

	arts, err := c.ListArtifacts(ctx, repo, run.ID)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Equal(t, int64(2048), arts[0].SizeInBytes)
	assert.False(t, arts[0].Expired)
	assert.Equal(t, expires, arts[0].ExpiresAt)

	pending, err := c.ListPendingDeployments(ctx, repo, run.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "production", pending[0].Environment.Name)
	assert.Equal(t, "platform-prod", pending[0].Reviewers[0].Reviewer.Slug)

	for _, f := range []func() error{
		func() error { _, err := c.ListWorkflowJobs(ctx, repo, 1); return err },
		func() error { _, err := c.ListArtifacts(ctx, repo, 1); return err },
		func() error { _, err := c.ListPendingDeployments(ctx, repo, 1); return err },
	} {
		require.ErrorIs(t, f(), gh.ErrNotFound)
	}
}

func TestReviewDeploymentProtectionRule(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	require.NoError(t, c.ReviewDeploymentProtectionRule(ctx, repo, 42, "production", gh.DeploymentApproved, "approved by @acme/platform-prod"))
	require.NoError(t, c.ReviewDeploymentProtectionRule(ctx, repo, 43, "production", gh.DeploymentRejected, ""))
	ds := fake.ProtectionRuleDecisions()
	require.Len(t, ds, 2)
	assert.Equal(t, ghfake.ProtectionRuleDecision{Repo: repo, RunID: 42, EnvironmentName: "production", State: "approved", Comment: "approved by @acme/platform-prod", At: ds[0].At}, ds[0])
	assert.Equal(t, "rejected", ds[1].State)

	require.Error(t, c.ReviewDeploymentProtectionRule(ctx, repo, 1, "production", "maybe", ""))
	require.Error(t, c.ReviewDeploymentProtectionRule(ctx, repo, 1, "", gh.DeploymentApproved, ""))
	assert.Len(t, fake.ProtectionRuleDecisions(), 2)
}

func TestRunIDFromCallbackURL(t *testing.T) {
	tests := []struct {
		url  string
		want int64
		ok   bool
	}{
		{"https://api.github.com/repos/acme/infra/actions/runs/1234/deployment_protection_rule", 1234, true},
		{"https://ghe.example.com/api/v3/repos/acme/infra/actions/runs/9/deployment_protection_rule", 9, true},
		{"https://api.github.com/repos/acme/infra/actions/runs/abc/deployment_protection_rule", 0, false},
		{"https://api.github.com/repos/acme/infra/actions/runs/12x/deployment_protection_rule", 0, false},
		{"https://api.github.com/repos/acme/infra", 0, false},
		{"://bad", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got, err := gh.RunIDFromCallbackURL(tt.url)
			if !tt.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIssues(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	fake.SetPull(repo, gh.PullRequest{Number: 1, Labels: gh.Labels{"stackorder:drift"}})

	is, err := c.CreateIssue(ctx, repo, gh.IssueParams{Title: "Drift in stacks/prod/vpc", Body: "1 change", Labels: []string{"stackorder:drift", "stack:stacks/prod/vpc"}})
	require.NoError(t, err)
	assert.Equal(t, 2, is.Number)
	assert.Equal(t, gh.IssueOpen, is.State)
	assert.Equal(t, gh.Labels{"stackorder:drift", "stack:stacks/prod/vpc"}, is.Labels)

	other, err := c.CreateIssue(ctx, repo, gh.IssueParams{Title: "Drift in stacks/staging/vpc", Labels: []string{"stackorder:drift"}})
	require.NoError(t, err)

	open, err := c.ListIssues(ctx, repo, []string{"stackorder:drift"}, "")
	require.NoError(t, err)
	assert.Len(t, open, 2)
	for _, i := range open {
		assert.Nil(t, i.PullRequest)
	}

	one, err := c.ListIssues(ctx, repo, []string{"stackorder:drift", "stack:stacks/prod/vpc"}, gh.IssueOpen)
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, is.Number, one[0].Number)

	closed, err := c.UpdateIssue(ctx, repo, other.Number, gh.IssueParams{State: gh.IssueClosed, Body: "resolved"})
	require.NoError(t, err)
	assert.Equal(t, gh.IssueClosed, closed.State)
	assert.NotNil(t, closed.ClosedAt)
	assert.Equal(t, "resolved", closed.Body)

	open, err = c.ListIssues(ctx, repo, []string{"stackorder:drift"}, gh.IssueOpen)
	require.NoError(t, err)
	assert.Len(t, open, 1)
	all, err := c.ListIssues(ctx, repo, nil, gh.IssueAll)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	reopened, err := c.UpdateIssue(ctx, repo, other.Number, gh.IssueParams{State: gh.IssueOpen, Title: "Drift again", Labels: []string{"x"}})
	require.NoError(t, err)
	assert.Nil(t, reopened.ClosedAt)
	assert.Equal(t, gh.Labels{"x"}, reopened.Labels)
	assert.Len(t, fake.Issues(repo), 2)

	_, err = c.UpdateIssue(ctx, repo, 99, gh.IssueParams{State: gh.IssueClosed})
	require.ErrorIs(t, err, gh.ErrNotFound)
	_, err = c.CreateIssue(ctx, repo, gh.IssueParams{})
	require.Error(t, err)
	fake.FailNext("GET /repos/{owner}/{repo}/issues", 500, 5)
	_, err = c.ListIssues(ctx, repo, nil, "")
	require.Error(t, err)
}

func TestContents(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	cfg := []byte("version: 1\napply:\n  allowed_teams: [platform-eng]\n" + strings.Repeat("# padding line\n", 20))
	fake.SetContents(repo, "", "stackorder.yaml", cfg)
	fake.SetContents(repo, "feature", "stackorder.yaml", []byte("version: 1\n"))
	fake.SetContents(repo, "", "stacks/prod/vpc/main.tf", []byte("terraform {}"))
	fake.SetRef(repo, "heads/main", "mainsha")

	got, err := c.GetContents(ctx, repo, "stackorder.yaml", "")
	require.NoError(t, err)
	assert.Equal(t, cfg, got)

	got, err = c.GetContents(ctx, repo, "/stackorder.yaml", "feature")
	require.NoError(t, err)
	assert.Equal(t, "version: 1\n", string(got))

	got, err = c.GetContents(ctx, repo, "stackorder.yaml", "mainsha")
	require.NoError(t, err)
	assert.Equal(t, cfg, got)

	_, err = c.GetContents(ctx, repo, "missing.yaml", "")
	require.ErrorIs(t, err, gh.ErrNotFound)

	_, err = c.GetContents(ctx, repo, "stacks/prod", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "directory")
}

func TestContentsDecodingErrors(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"symlink", `{"type":"symlink","encoding":"base64","content":""}`, "not a file"},
		{"large file", `{"type":"file","encoding":"none","content":""}`, "unsupported encoding"},
		{"bad base64", `{"type":"file","encoding":"base64","content":"!!!"}`, "illegal base64"},
		{"bad json", `{`, "decode response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			defer srv.Close()
			c, err := gh.NewTokenClient(gh.Config{BaseURL: srv.URL}, "t")
			require.NoError(t, err)
			_, err = c.GetContents(context.Background(), repo, "x", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestTagsAndRefs(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	fake.SetPageSize(1)
	tags := []gh.Tag{{Name: "v1.2.0", SHA: "c120"}, {Name: "v1.1.0", SHA: "c110"}, {Name: "v1.0.0", SHA: "c100"}}
	fake.SetTags(repo, tags)
	fake.SetRef(repo, "refs/heads/main", "mainsha")

	got, err := c.ListTags(ctx, repo)
	require.NoError(t, err)
	assert.Equal(t, tags, got)

	tests := []struct{ ref, want string }{
		{"heads/main", "mainsha"},
		{"refs/heads/main", "mainsha"},
		{"tags/v1.1.0", "c110"},
	}
	for _, tt := range tests {
		sha, err := c.GetRef(ctx, repo, tt.ref)
		require.NoError(t, err, tt.ref)
		assert.Equal(t, tt.want, sha, tt.ref)
	}
	_, err = c.GetRef(ctx, repo, "heads/missing")
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestGetRefPeelsAnnotatedTags(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/repos/acme/modules/git/ref/tags/v2.0.0":
			_, _ = w.Write([]byte(`{"ref":"refs/tags/v2.0.0","object":{"type":"tag","sha":"tagobj1"}}`))
		case "/repos/acme/modules/git/tags/tagobj1":
			_, _ = w.Write([]byte(`{"sha":"tagobj1","object":{"type":"tag","sha":"tagobj2"}}`))
		case "/repos/acme/modules/git/tags/tagobj2":
			_, _ = w.Write([]byte(`{"sha":"tagobj2","object":{"type":"commit","sha":"commit9"}}`))
		case "/repos/acme/modules/git/ref/tags/loop":
			_, _ = w.Write([]byte(`{"object":{"type":"tag","sha":"self"}}`))
		case "/repos/acme/modules/git/tags/self":
			_, _ = w.Write([]byte(`{"object":{"type":"tag","sha":"self"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := gh.NewTokenClient(gh.Config{BaseURL: srv.URL}, "t")
	require.NoError(t, err)
	sha, err := c.GetRef(context.Background(), "acme/modules", "tags/v2.0.0")
	require.NoError(t, err)
	assert.Equal(t, "commit9", sha)
	assert.Len(t, paths, 3)

	_, err = c.GetRef(context.Background(), "acme/modules", "tags/loop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many nested tags")

	_, err = c.GetRef(context.Background(), "acme/modules", "tags/gone")
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestGetRefPeelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/ref/") {
			_, _ = w.Write([]byte(`{"object":{"type":"tag","sha":"t"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, err := gh.NewTokenClient(gh.Config{BaseURL: srv.URL}, "t")
	require.NoError(t, err)
	_, err = c.GetRef(context.Background(), "acme/modules", "tags/v1")
	require.ErrorIs(t, err, gh.ErrNotFound)
}

func TestTagJSONRoundTrip(t *testing.T) {
	data, err := json.Marshal(gh.Tag{Name: "v1", SHA: "abc"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"v1","commit":{"sha":"abc"}}`, string(data))
	var tag gh.Tag
	require.Error(t, json.Unmarshal([]byte(`[]`), &tag))
}

func TestCodeownersFor(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		files    map[string]string
		wantPath string
		owners   []string
	}{
		{
			name: ".github wins",
			files: map[string]string{
				".github/CODEOWNERS": "/stacks/prod/ @acme/platform-prod",
				"CODEOWNERS":         "* @acme/root",
				"docs/CODEOWNERS":    "* @acme/docs",
			},
			wantPath: ".github/CODEOWNERS",
			owners:   []string{"@acme/platform-prod"},
		},
		{
			name:     "root before docs",
			files:    map[string]string{"CODEOWNERS": "* @acme/root", "docs/CODEOWNERS": "* @acme/docs"},
			wantPath: "CODEOWNERS",
			owners:   []string{"@acme/root"},
		},
		{
			name:     "docs last",
			files:    map[string]string{"docs/CODEOWNERS": "stacks/ @acme/docs"},
			wantPath: "docs/CODEOWNERS",
			owners:   []string{"@acme/docs"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, c := setup(t)
			for p, content := range tt.files {
				fake.SetContents(repo, "headsha", p, []byte(content))
			}
			co, err := c.CodeownersFor(ctx, repo, "headsha")
			require.NoError(t, err)
			assert.Equal(t, tt.wantPath, co.Path)
			assert.Equal(t, tt.owners, co.OwnersFor("stacks/prod/vpc/main.tf"))
		})
	}

	fake, c := setup(t)
	_, err := c.CodeownersFor(ctx, repo, "headsha")
	require.ErrorIs(t, err, gh.ErrNotFound)

	fake.FailNext("GET /repos/{owner}/{repo}/contents/{path...}", 403, 1)
	_, err = c.CodeownersFor(ctx, repo, "headsha")
	var apiErr *gh.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Status)
}

func TestUserFlows(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddUser(gh.User{Login: "alice", ID: 501, Name: "Alice"})
	fake.SetOrgMembership("acme", "alice", gh.MembershipActive, "admin")
	fake.SetOrgMembership("globex", "alice", gh.MembershipActive, "member")
	fake.SetOrgMembership("initech", "alice", gh.MembershipPending, "member")
	c, err := gh.NewTokenClient(gh.Config{BaseURL: fake.URL(), HTTPClient: fake.HTTPClient()}, fake.UserToken("alice"))
	require.NoError(t, err)
	assert.Zero(t, c.InstallationID())
	ctx := context.Background()

	u, err := c.AuthenticatedUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "alice", u.Login)
	assert.Equal(t, int64(501), u.ID)
	assert.NotEmpty(t, u.AvatarURL)

	orgs, err := c.UserOrgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme", "globex"}, orgs)

	state, role, err := c.UserOrgMembership(ctx, "acme")
	require.NoError(t, err)
	assert.Equal(t, gh.MembershipActive, state)
	assert.Equal(t, "admin", role)

	state, role, err = c.UserOrgMembership(ctx, "umbrella")
	require.NoError(t, err)
	assert.Equal(t, gh.MembershipNone, state)
	assert.Empty(t, role)

	fake.FailNext("GET /user/memberships/orgs/{org}", 403, 1)
	_, _, err = c.UserOrgMembership(ctx, "acme")
	require.Error(t, err)
	fake.FailNext("GET /user/orgs", 403, 1)
	_, err = c.UserOrgs(ctx)
	require.Error(t, err)
	fake.FailNext("GET /user", 401, 1)
	_, err = c.AuthenticatedUser(ctx)
	require.Error(t, err)

	_, err = gh.NewTokenClient(gh.Config{}, "")
	require.Error(t, err)
	_, err = gh.NewTokenClient(gh.Config{BaseURL: "::"}, "t")
	require.Error(t, err)
}

func TestInstallationTokenCannotCallUserEndpoints(t *testing.T) {
	_, c := setup(t)
	_, err := c.AuthenticatedUser(context.Background())
	var apiErr *gh.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Status)
}

func TestClientRetriesThroughFake(t *testing.T) {
	fake, c := setup(t)
	ctx := context.Background()
	fake.SetPull(repo, gh.PullRequest{Number: 1, HeadSHA: "h"})

	fake.FailNext("GET /repos/{owner}/{repo}/pulls/{pull_number}", 502, 2)
	pr, err := c.GetPull(ctx, repo, 1)
	require.NoError(t, err)
	assert.Equal(t, "h", pr.HeadSHA)
	assert.Equal(t, 3, countRequests(fake, "GET /repos/{owner}/{repo}/pulls/{pull_number}"))

	fake.FailNext("GET /repos/acme/infra/pulls/1", 0, 1)
	_, err = c.GetPull(ctx, repo, 1)
	require.NoError(t, err)

	fake.SecondaryRateLimitNext(0)
	_, err = c.GetPull(ctx, repo, 1)
	require.NoError(t, err)

	fake.RateLimitNext(time.Now().Add(time.Hour))
	_, err = c.GetPull(ctx, repo, 1)
	require.ErrorIs(t, err, gh.ErrRateLimited)
}
