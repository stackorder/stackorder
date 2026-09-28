package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestLoadContext(t *testing.T) {
	base := map[string]string{
		"GITHUB_ACTIONS":     "true",
		"GITHUB_REPOSITORY":  "acme/infra",
		"GITHUB_SHA":         mergeSHA,
		"GITHUB_REF":         "refs/pull/7/merge",
		"GITHUB_RUN_ID":      "4242",
		"GITHUB_RUN_ATTEMPT": "3",
		"GITHUB_SERVER_URL":  "https://ghe.example/",
		"GITHUB_API_URL":     "https://ghe.example/api/v3/",
		"GITHUB_WORKSPACE":   "/work",
		"GITHUB_TOKEN":       "ghs_token",
		"GITHUB_ACTOR":       "octocat",
	}
	common := Context{
		CI: true, Repository: "acme/infra", Ref: "refs/pull/7/merge", RunID: 4242, RunAttempt: 3,
		ServerURL: "https://ghe.example", APIURL: "https://ghe.example/api/v3", Workspace: "/work",
		Token: "ghs_token", Actor: "octocat", PRNumber: 7,
	}
	head := func(fullName string, fork bool) map[string]any {
		return map[string]any{"sha": headSHA, "repo": map[string]any{"full_name": fullName, "fork": fork}}
	}
	pr := func(h any, baseRepo any) map[string]any {
		return map[string]any{"pull_request": map[string]any{
			"number": 9, "head": h, "base": map[string]any{"sha": baseSHA, "repo": baseRepo},
		}, "repository": map[string]any{"full_name": "acme/infra", "default_branch": "trunk"}}
	}
	tests := []struct {
		name    string
		event   string
		payload any
		env     map[string]string
		want    func(c Context) Context
	}{
		{
			name:    "pull request uses the head commit",
			event:   "pull_request",
			payload: pr(head("acme/infra", false), map[string]any{"full_name": "acme/infra"}),
			want: func(c Context) Context {
				c.EventName, c.SHA, c.HeadSHA, c.BaseSHA, c.PRNumber, c.DefaultBranch = "pull_request", headSHA, headSHA, baseSHA, 9, "trunk"
				return c
			},
		},
		{
			name:    "fork by repository name",
			event:   "pull_request",
			payload: pr(head("stranger/infra", true), map[string]any{"full_name": "acme/infra"}),
			want: func(c Context) Context {
				c.EventName, c.SHA, c.HeadSHA, c.BaseSHA, c.PRNumber, c.DefaultBranch, c.IsFork = "pull_request", headSHA, headSHA, baseSHA, 9, "trunk", true
				return c
			},
		},
		{
			name:    "a fork repository's own branch is not a fork pull request",
			event:   "pull_request",
			payload: pr(head("acme/infra", true), map[string]any{"full_name": "ACME/infra"}),
			want: func(c Context) Context {
				c.EventName, c.SHA, c.HeadSHA, c.BaseSHA, c.PRNumber, c.DefaultBranch = "pull_request", headSHA, headSHA, baseSHA, 9, "trunk"
				return c
			},
		},
		{
			name:    "deleted fork",
			event:   "pull_request",
			payload: pr(map[string]any{"sha": headSHA, "repo": nil}, map[string]any{"full_name": "acme/infra"}),
			want: func(c Context) Context {
				c.EventName, c.SHA, c.HeadSHA, c.BaseSHA, c.PRNumber, c.DefaultBranch, c.IsFork = "pull_request", headSHA, headSHA, baseSHA, 9, "trunk", true
				return c
			},
		},
		{
			name:    "fork flag without a base repository",
			event:   "pull_request_target",
			payload: pr(map[string]any{"sha": headSHA, "repo": map[string]any{"fork": true}}, nil),
			want: func(c Context) Context {
				c.EventName, c.SHA, c.HeadSHA, c.BaseSHA, c.PRNumber, c.DefaultBranch, c.IsFork = "pull_request_target", headSHA, headSHA, baseSHA, 9, "trunk", true
				return c
			},
		},
		{
			name:    "push",
			event:   "push",
			payload: map[string]any{"before": baseSHA, "after": mergeSHA},
			want: func(c Context) Context {
				c.EventName, c.SHA, c.BaseSHA, c.HeadSHA = "push", mergeSHA, baseSHA, mergeSHA
				return c
			},
		},
		{
			name:    "push creating a branch",
			event:   "push",
			payload: map[string]any{"before": "0000000000000000000000000000000000000000", "after": mergeSHA},
			want: func(c Context) Context {
				c.EventName, c.SHA, c.HeadSHA = "push", mergeSHA, mergeSHA
				return c
			},
		},
		{
			name:    "workflow dispatch inputs",
			event:   "workflow_dispatch",
			payload: map[string]any{"inputs": map[string]any{"run_id": " run-5 ", "sha": headSHA, "wave": 2}},
			env:     map[string]string{"GITHUB_REF": "refs/heads/main"},
			want: func(c Context) Context {
				c.EventName, c.SHA, c.DispatchRunID, c.Ref, c.PRNumber = "workflow_dispatch", headSHA, "run-5", "refs/heads/main", 0
				return c
			},
		},
		{
			name:    "dispatch without a sha input",
			event:   "workflow_dispatch",
			payload: map[string]any{"inputs": map[string]any{"run_id": "run-5", "sha": ""}},
			env:     map[string]string{"GITHUB_REF": "refs/heads/main"},
			want: func(c Context) Context {
				c.EventName, c.SHA, c.DispatchRunID, c.Ref, c.PRNumber = "workflow_dispatch", mergeSHA, "run-5", "refs/heads/main", 0
				return c
			},
		},
		{
			name:  "no payload",
			event: "schedule",
			env:   map[string]string{"GITHUB_SERVER_URL": "", "GITHUB_API_URL": "", "GITHUB_RUN_ID": "x"},
			want: func(c Context) Context {
				c.EventName, c.SHA, c.ServerURL, c.APIURL, c.RunID = "schedule", mergeSHA, "https://github.com", "https://api.github.com", 0
				return c
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range base {
				t.Setenv(k, v)
			}
			t.Setenv("GITHUB_EVENT_NAME", tt.event)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if tt.payload != nil {
				data, err := json.Marshal(tt.payload)
				require.NoError(t, err)
				t.Setenv("GITHUB_EVENT_PATH", writeFile(t, filepath.Join(t.TempDir(), "event.json"), string(data)))
			}
			got, err := LoadContext(context.Background(), t.TempDir())
			require.NoError(t, err)
			assert.Equal(t, tt.want(common), *got)
		})
	}
}

func TestLoadContextErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_EVENT_PATH", filepath.Join(t.TempDir(), "missing.json"))
	_, err := LoadContext(context.Background(), t.TempDir())
	require.ErrorIs(t, err, os.ErrNotExist)

	t.Setenv("GITHUB_EVENT_PATH", writeFile(t, filepath.Join(t.TempDir(), "bad.json"), "[1,"))
	_, err = LoadContext(context.Background(), t.TempDir())
	require.ErrorContains(t, err, "decoding the event payload")
}

func TestLoadContextFromGit(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README"), "x")
	sha := initGit(t, dir)
	gitRun(t, dir, "remote", "add", "origin", "https://github.com/acme/infra.git")
	gitRun(t, dir, "update-ref", "refs/remotes/origin/develop", sha)
	gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	t.Setenv("GITHUB_SHA", mergeSHA)

	got, err := LoadContext(context.Background(), dir)
	require.NoError(t, err)
	assert.False(t, got.CI)
	assert.Equal(t, sha, got.SHA)
	assert.Equal(t, "acme/infra", got.Repository)
	assert.Equal(t, "develop", got.DefaultBranch)
	assert.Empty(t, got.JobURL())
	assert.Equal(t, v1.TriggerManual, got.Trigger())

	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_REPOSITORY", "acme/other")
	got, err = LoadContext(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, mergeSHA, got.SHA)
	assert.Equal(t, "acme/other", got.Repository)
}

func TestParseRemote(t *testing.T) {
	tests := map[string]string{
		"https://github.com/acme/infra.git":            "acme/infra",
		"https://github.com/acme/infra":                "acme/infra",
		"https://token@github.com/acme/infra.git/":     "acme/infra",
		"http://ghe.example/scm/acme/infra.git":        "acme/infra",
		"git@github.com:acme/infra.git":                "acme/infra",
		"git@github.com:acme/infra":                    "acme/infra",
		"ssh://git@github.com/acme/infra.git":          "acme/infra",
		"ssh://git@ghe.example:2222/acme/infra.git":    "acme/infra",
		"  git@github.com:acme/infra.git\n":            "acme/infra",
		"/srv/git/infra.git":                           "",
		"https://github.com/infra":                     "",
		"git@github.com:infra.git":                     "",
		"://bad":                                       "",
		"https://example.com/%zz/x":                    "",
		"file:///srv/git/acme/infra.git":               "acme/infra",
		"https://gitlab.example/group/sub/acme/infra/": "acme/infra",
	}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, parseRemote(in))
		})
	}
}

func TestContextHelpers(t *testing.T) {
	triggers := map[string]v1.Trigger{
		"pull_request":        v1.TriggerPullRequest,
		"pull_request_target": v1.TriggerPullRequest,
		"push":                v1.TriggerPush,
		"schedule":            v1.TriggerSchedule,
		"issue_comment":       v1.TriggerComment,
		"check_run":           v1.TriggerRerequest,
		"check_suite":         v1.TriggerRerequest,
		"workflow_dispatch":   v1.TriggerManual,
		"":                    v1.TriggerManual,
	}
	for event, want := range triggers {
		assert.Equal(t, want, (&Context{EventName: event}).Trigger(), event)
	}
	refs := map[string]int{"refs/pull/12/merge": 12, "refs/pull/x/merge": 0, "refs/heads/main": 0, "refs/pull/3": 3}
	for ref, want := range refs {
		assert.Equal(t, want, prFromRef(ref), ref)
	}
	c := &Context{CI: true, Repository: "acme/infra", RunID: 9, ServerURL: "https://github.com"}
	assert.Equal(t, "https://github.com/acme/infra/actions/runs/9", c.JobURL())
	c.RunID = 0
	assert.Empty(t, c.JobURL())
}
