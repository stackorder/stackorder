//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/cli"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/scan"
	"github.com/stackorder/stackorder/internal/testutil/faketf"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
)

const jobTimeout = 2 * time.Minute

var (
	bindOnce sync.Once
	hostPath = os.Getenv("PATH")
	hostVars = []string{"HOME", "TMPDIR", "TMP", "TEMP", "USER", "LOGNAME", "LANG", "LC_ALL", "SYSTEMROOT", "GOCOVERDIR"}
)

func bindCLI() {
	bindOnce.Do(func() {
		cli.Bind(cli.Deps{
			ScanRepo: func(ctx context.Context, root, repo, sha string, cfg *v1.RepoConfig) (*v1.Graph, error) {
				return scan.Scan(ctx, root, scan.Options{Repo: repo, SHA: sha, Config: cfg})
			},
			ChangedPaths: scan.ChangedPaths,
			ResolveLocal: func(g *v1.Graph, changed []string, cfg *v1.RepoConfig, requested []string) (*v1.ResolveResponse, error) {
				return graph.Resolve(g, graph.Input{ChangedPaths: changed, Config: cfg, Requested: requested})
			},
			RenderDOT: graph.ToDOT,
		})
	})
}

type jobResult struct {
	code    int
	err     error
	stdout  string
	stderr  string
	outputs map[string]string
	summary string
}

type workflow struct {
	f      *fixture
	claims oidc.Claims
	ts     *oidcfake.TokenServer
	event  string
	sha    string
	server string
}

func mergeSHA(head string) string {
	sum := sha1.Sum([]byte("merge of " + head))
	return hex.EncodeToString(sum[:])
}

func (f *fixture) planWorkflow(ev *gh.PullRequestEvent) *workflow {
	f.t.Helper()
	pr := ev.PullRequest
	claims := f.e.OIDC.PlanClaims(f.name, f.repoID(), pr.Number, mergeSHA(pr.HeadSHA))
	claims.HeadRef, claims.Actor = pr.HeadRef, ev.Sender.Login
	return f.workflow(claims, ev, pr.HeadSHA)
}

func (f *fixture) dispatchWorkflow(d ghfake.Dispatch, environment string) *workflow {
	f.t.Helper()
	claims := f.e.OIDC.DispatchClaims(f.name, f.repoID(), d.RunID, environment, "main", f.main)
	payload := map[string]any{
		"inputs":     d.Inputs,
		"ref":        "refs/heads/main",
		"workflow":   ".github/workflows/" + d.Workflow,
		"repository": map[string]any{"id": f.id, "full_name": f.name, "default_branch": "main", "private": true},
		"sender":     map[string]any{"login": oidcfake.Actor, "type": "User"},
	}
	return f.workflow(claims, payload, d.Inputs["sha"])
}

func (f *fixture) workflow(claims oidc.Claims, payload any, sha string) *workflow {
	f.t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(f.t, err)
	event := filepath.Join(f.t.TempDir(), "event.json")
	require.NoError(f.t, os.WriteFile(event, data, 0o600))
	ts := f.e.OIDC.TokenRequestServer(f.t)
	ts.SetClaims(claims)
	return &workflow{f: f, claims: claims, ts: ts, event: event, sha: sha, server: f.e.BaseURL}
}

func (w *workflow) runID() int64 {
	id, err := strconv.ParseInt(w.claims.RunID, 10, 64)
	require.NoError(w.f.t, err)
	return id
}

func (w *workflow) run(args ...string) jobResult {
	t := w.f.t
	t.Helper()
	w.f.co.at(w.sha)
	vars := w.ts.Env()
	vars["GITHUB_EVENT_PATH"] = w.event
	vars["GITHUB_WORKSPACE"] = w.f.co.dir
	vars["GITHUB_API_URL"] = w.f.e.GH.URL()
	vars["GITHUB_TOKEN"] = w.f.e.GH.InstallationToken(w.f.inst)
	vars[cli.EnvServerURL] = w.server
	return w.f.exec(vars, args...)
}

func (f *fixture) exec(vars map[string]string, args ...string) jobResult {
	f.t.Helper()
	outputs, summary := f.prepare(vars)
	return f.cli(outputs, summary, args...)
}

func (f *fixture) prepare(vars map[string]string) (outputs, summary string) {
	t := f.t
	t.Helper()
	bindCLI()
	syncIssuer(f.e)
	isolateEnv(t)
	if vars["GITHUB_ACTIONS"] == "true" {
		dir := t.TempDir()
		outputs, summary = filepath.Join(dir, "output"), filepath.Join(dir, "summary")
		vars["GITHUB_OUTPUT"], vars["GITHUB_STEP_SUMMARY"] = outputs, summary
	}
	vars[cli.EnvPlanDir] = f.planDir
	vars[faketf.EnvConfig] = faketf.WriteConfig(t, f.tf)
	vars["PATH"] = suite.tfDir + string(os.PathListSeparator) + hostPath
	vars["GIT_CONFIG_GLOBAL"] = os.DevNull
	vars["GIT_CONFIG_NOSYSTEM"] = "1"
	for k, v := range vars {
		t.Setenv(k, v)
	}
	return outputs, summary
}

func (f *fixture) cli(outputs, summary string, args ...string) jobResult {
	t := f.t
	if outputs == "" {
		args = append([]string{"--repo-root", f.co.dir}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	err := cli.Run(ctx, args, &stdout, &stderr)
	res := jobResult{code: cli.ExitCode(err), err: err, stdout: stdout.String(), stderr: stderr.String(), outputs: map[string]string{}}
	if outputs != "" {
		res.outputs = readOutputs(t, outputs)
		if data, err := os.ReadFile(summary); err == nil {
			res.summary = string(data)
		}
	}
	t.Logf("stackorder %s: exit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), res.code, res.stdout, res.stderr)
	return res
}

func isolateEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "" || slices.Contains(hostVars, strings.ToUpper(name)) {
			continue
		}
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}

func syncIssuer(e *Env) {
	if lag := time.Since(e.OIDC.Now()); lag > 0 {
		e.OIDC.Advance(lag)
	}
}

func readOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out
	}
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if k, delim, ok := strings.Cut(lines[i], "<<"); ok && !strings.Contains(k, "=") {
			var value []string
			for i++; i < len(lines) && lines[i] != delim; i++ {
				value = append(value, lines[i])
			}
			out[k] = strings.Join(value, "\n")
			continue
		}
		if k, v, ok := strings.Cut(lines[i], "="); ok {
			out[k] = v
		}
	}
	return out
}

func requireExit(t *testing.T, want int, res jobResult) {
	t.Helper()
	require.Equal(t, want, res.code, "exit code; stderr:\n%s", res.stderr)
}
