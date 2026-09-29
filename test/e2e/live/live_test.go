package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestFromEnv(t *testing.T) {
	full := map[string]string{
		EnvToken:     "ghp_example",
		EnvOrg:       "stackorder-e2e",
		EnvServerURL: "https://stackorder.example.com/",
	}
	tests := []struct {
		name    string
		vars    map[string]string
		ok      bool
		wantErr error
	}{
		{name: "unset", vars: map[string]string{}},
		{name: "token only", vars: map[string]string{EnvToken: "ghp_example"}, wantErr: ErrPartialConfig},
		{name: "no server", vars: map[string]string{EnvToken: "ghp_example", EnvOrg: "o"}, wantErr: ErrPartialConfig},
		{name: "complete", vars: full, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, ok, err := FromEnv(env(tt.vars), "abc123")
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.ok, ok)
			if !tt.ok {
				assert.Zero(t, cfg)
				return
			}
			assert.Equal(t, "https://stackorder.example.com", cfg.ServerURL)
			assert.Equal(t, DefaultAPIURL, cfg.APIURL)
			assert.Equal(t, DefaultWebURL, cfg.WebURL)
			assert.Equal(t, "stackorder-e2e/stackorder-e2e-abc123", cfg.FullName())
			assert.Equal(t, "https://github.com/stackorder-e2e/stackorder-e2e-abc123.git", cfg.CloneURL())
			assert.False(t, cfg.KeepRepo)
		})
	}

	badKeep := map[string]string{EnvToken: "t", EnvOrg: "o", EnvServerURL: "https://s.example", EnvKeepRepo: "sometimes"}
	_, _, err := FromEnv(env(badKeep), "x")
	require.ErrorContains(t, err, EnvKeepRepo)

	insecure := map[string]string{EnvToken: "t", EnvOrg: "o", EnvServerURL: "http://10.0.0.1:8080"}
	_, ok, err := FromEnv(env(insecure), "x")
	require.Error(t, err, "GitHub delivers webhooks only to a reachable https URL")
	assert.False(t, ok)

	keep := map[string]string{EnvToken: "t", EnvOrg: "o", EnvServerURL: "https://s.example", EnvKeepRepo: "true",
		EnvAPIURL: "https://ghe.example/api/v3/", EnvWebURL: "https://ghe.example", EnvPlanRoleARN: "arn:aws:iam::1:role/plan"}
	cfg, ok, err := FromEnv(env(keep), "x")
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, cfg.KeepRepo)
	assert.Equal(t, "https://ghe.example/api/v3", cfg.APIURL)
	assert.Equal(t, "arn:aws:iam::1:role/plan", cfg.PlanRoleARN)
}

func testConfig() Config {
	return Config{
		Token: "ghp_secret", Org: "acme", ServerURL: "https://stackorder.example.com",
		APIURL: DefaultAPIURL, WebURL: DefaultWebURL, Repo: "stackorder-e2e-1",
	}
}

var testChange = Change{
	BaseSHA: "1111111111111111111111111111111111111111",
	HeadSHA: "2222222222222222222222222222222222222222",
	Branch:  "e2e-vpc",
	Title:   "feat(vpc): e2e change",
	Checks:  []string{"stackorder/resolve", "stackorder/plan"},
}

func TestPlan(t *testing.T) {
	steps := Plan(testConfig(), testChange)
	type summary struct {
		Kind   Kind
		Method string
		Path   string
		Git    string
	}
	got := make([]summary, 0, len(steps))
	for _, st := range steps {
		got = append(got, summary{st.Kind, st.Method, st.Path, strings.Join(st.Git, " ")})
	}
	assert.Equal(t, []summary{
		{Kind: KindAPI, Method: http.MethodPost, Path: "/orgs/acme/repos"},
		{Kind: KindAPI, Method: http.MethodPost, Path: "/repos/acme/stackorder-e2e-1/actions/variables"},
		{Kind: KindGit, Git: "push --quiet https://github.com/acme/stackorder-e2e-1.git 1111111111111111111111111111111111111111:refs/heads/main"},
		{Kind: KindGit, Git: "push --quiet https://github.com/acme/stackorder-e2e-1.git 2222222222222222222222222222222222222222:refs/heads/e2e-vpc"},
		{Kind: KindAPI, Method: http.MethodPost, Path: "/repos/acme/stackorder-e2e-1/pulls"},
		{Kind: KindWaitChecks},
	}, got)

	assert.Equal(t, map[string]any{
		"name": "stackorder-e2e-1", "private": true, "auto_init": false, "has_issues": true,
		"description": "Throwaway repository of the Stackorder end-to-end suite",
	}, steps[0].Body)
	assert.Equal(t, map[string]any{"name": "STACKORDER_SERVER_URL", "value": "https://stackorder.example.com"}, steps[1].Body)
	assert.Equal(t, "e2e-vpc", steps[4].Body["head"])
	assert.Equal(t, MainBranch, steps[4].Body["base"])
	assert.Equal(t, testChange.HeadSHA, steps[5].SHA)
	assert.Equal(t, testChange.Checks, steps[5].Checks)
	for _, st := range steps {
		assert.NotContains(t, fmt.Sprint(st), "ghp_secret", "a plan never carries the token")
	}

	withRole := testConfig()
	withRole.PlanRoleARN = "arn:aws:iam::123456789012:role/plan"
	roleSteps := Plan(withRole, testChange)
	require.Len(t, roleSteps, len(steps)+1)
	assert.Equal(t, map[string]any{"name": "STACKORDER_PLAN_ROLE_ARN", "value": withRole.PlanRoleARN}, roleSteps[2].Body)

	del := Cleanup(testConfig())
	assert.Equal(t, http.MethodDelete, del.Method)
	assert.Equal(t, "/repos/acme/stackorder-e2e-1", del.Path)
	assert.ElementsMatch(t, []int{http.StatusNoContent, http.StatusNotFound}, del.Want)
}

type fakeGitHub struct {
	mu       sync.Mutex
	requests []string
	polls    int
	auth     []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/stackorder-e2e-1/pulls":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number": 7}`))
	case r.Method == http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet:
		f.polls++
		plan := CheckRun{Name: "stackorder/plan", Status: "in_progress"}
		if f.polls > 1 {
			plan = CheckRun{Name: "stackorder/plan", Status: "completed", Conclusion: "success"}
		}
		runs := []CheckRun{
			{Name: "stackorder/resolve", Status: "completed", Conclusion: "success"},
			plan,
			{Name: "other", Status: "completed", Conclusion: "failure"},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(runs), "check_runs": runs})
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestRunnerRunsThePlan(t *testing.T) {
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg := testConfig()
	cfg.APIURL = srv.URL
	var gitCalls, gitEnvs [][]string
	r := &Runner{
		Config: cfg, Dir: "/work", HTTP: srv.Client(), Poll: time.Millisecond, Timeout: 5 * time.Second,
		Git: func(_ context.Context, dir string, env []string, args ...string) error {
			assert.Equal(t, "/work", dir)
			gitCalls = append(gitCalls, args)
			gitEnvs = append(gitEnvs, env)
			return nil
		},
	}
	res, err := r.Run(t.Context(), append(Plan(cfg, testChange), Cleanup(cfg)))
	require.NoError(t, err)
	assert.Equal(t, 7, res.PullNumber)
	assert.Equal(t, map[string]CheckRun{
		"stackorder/resolve": {Name: "stackorder/resolve", Status: "completed", Conclusion: "success"},
		"stackorder/plan":    {Name: "stackorder/plan", Status: "completed", Conclusion: "success"},
	}, res.Checks)
	assert.Equal(t, 2, fake.polls, "the runner polls until every named check completed")
	assert.Equal(t, []string{
		"POST /orgs/acme/repos",
		"POST /repos/acme/stackorder-e2e-1/actions/variables",
		"POST /repos/acme/stackorder-e2e-1/pulls",
		"GET /repos/acme/stackorder-e2e-1/commits/2222222222222222222222222222222222222222/check-runs",
		"GET /repos/acme/stackorder-e2e-1/commits/2222222222222222222222222222222222222222/check-runs",
		"DELETE /repos/acme/stackorder-e2e-1",
	}, fake.requests)
	for _, a := range fake.auth {
		assert.Equal(t, "Bearer ghp_secret", a)
	}
	require.Len(t, gitCalls, 2)
	auth := "AUTHORIZATION: basic eC1hY2Nlc3MtdG9rZW46Z2hwX3NlY3JldA=="
	for i, args := range gitCalls {
		assert.Equal(t, "push", args[0])
		assert.NotContains(t, strings.Join(args, " "), "eC1hY2Nlc3MtdG9rZW46Z2hwX3NlY3JldA==", "the token never reaches git's command line")
		assert.Equal(t, []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0=" + auth,
		}, gitEnvs[i])
	}
}

func TestRunnerStopsAtTheFirstFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"name already exists on this account"}`))
	}))
	t.Cleanup(srv.Close)
	cfg := testConfig()
	cfg.APIURL = srv.URL
	gitRan := false
	r := &Runner{Config: cfg, HTTP: srv.Client(), Git: func(context.Context, string, []string, ...string) error {
		gitRan = true
		return nil
	}}
	_, err := r.Run(t.Context(), Plan(cfg, testChange))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create acme/stackorder-e2e-1")
	assert.Contains(t, err.Error(), "name already exists")
	assert.False(t, gitRan, "nothing is pushed when the repository cannot be created")
}

func TestRunnerRedactsTheTokenFromGitErrors(t *testing.T) {
	cfg := testConfig()
	r := &Runner{Config: cfg, Git: func(_ context.Context, _ string, env []string, _ ...string) error {
		return errors.New("git push with " + strings.Join(env, " ") + ": rejected")
	}}
	_, err := r.Run(t.Context(), []Step{{Kind: KindGit, Name: "push main", Git: []string{"push"}}})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "eC1hY2Nlc3MtdG9rZW46Z2hwX3NlY3JldA==")
	assert.Contains(t, err.Error(), "AUTHORIZATION: basic ***")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@example.invalid", "GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@example.invalid")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func TestRunnerPushesWithTheGitBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	root := t.TempDir()
	cfg := testConfig()
	cfg.WebURL = "file://" + filepath.ToSlash(root)
	remote := filepath.Join(root, cfg.Org, cfg.Repo+".git")
	require.NoError(t, os.MkdirAll(remote, 0o750))
	gitIn(t, remote, "init", "--quiet", "--bare")
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(work, 0o750))
	gitIn(t, work, "init", "--quiet")
	gitIn(t, work, "commit", "--quiet", "--allow-empty", "--message", "e2e")
	sha := gitIn(t, work, "rev-parse", "HEAD")

	r := &Runner{Config: cfg, Dir: work}
	_, err := r.Run(t.Context(), []Step{{Kind: KindGit, Name: "push main", Git: []string{"push", "--quiet", cfg.CloneURL(), sha + ":refs/heads/" + MainBranch}}})
	require.NoError(t, err)
	assert.Equal(t, sha, gitIn(t, remote, "rev-parse", "refs/heads/"+MainBranch))
}

func TestRunnerTimesOutWaitingForChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
	}))
	t.Cleanup(srv.Close)
	cfg := testConfig()
	cfg.APIURL = srv.URL
	r := &Runner{Config: cfg, HTTP: srv.Client(), Poll: time.Millisecond, Timeout: 50 * time.Millisecond}
	_, err := r.Run(t.Context(), []Step{{Kind: KindWaitChecks, Name: "wait", SHA: "abc", Checks: []string{"stackorder/plan"}}})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "stackorder/plan")
}
