package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

type scanCall struct {
	root, repo, sha string
	cfg             *v1.RepoConfig
}

type resolveCall struct {
	changed   []string
	requested []string
}

type fakeDeps struct {
	graph      *v1.Graph
	scanErr    error
	changed    []string
	changedErr error
	resolve    *v1.ResolveResponse
	resolveErr error
	dot        string

	scans    []scanCall
	diffs    [][2]string
	resolves []resolveCall
	dots     []map[string]int
}

func installDeps(t *testing.T, d *fakeDeps) *fakeDeps {
	t.Helper()
	oldScan, oldChanged, oldResolve, oldDOT := scanRepo, changedPaths, resolveLocal, renderDOT
	t.Cleanup(func() {
		scanRepo, changedPaths, resolveLocal, renderDOT = oldScan, oldChanged, oldResolve, oldDOT
	})
	Bind(Deps{
		ScanRepo: func(_ context.Context, root, repo, sha string, cfg *v1.RepoConfig) (*v1.Graph, error) {
			d.scans = append(d.scans, scanCall{root: root, repo: repo, sha: sha, cfg: cfg})
			return d.graph, d.scanErr
		},
		ChangedPaths: func(_ context.Context, _, base, head string) ([]string, error) {
			d.diffs = append(d.diffs, [2]string{base, head})
			return d.changed, d.changedErr
		},
		ResolveLocal: func(_ *v1.Graph, changed []string, _ *v1.RepoConfig, requested []string) (*v1.ResolveResponse, error) {
			d.resolves = append(d.resolves, resolveCall{changed: changed, requested: requested})
			return d.resolve, d.resolveErr
		},
		RenderDOT: func(_ *v1.Graph, highlight map[string]int) string {
			d.dots = append(d.dots, highlight)
			return d.dot
		},
	})
	return d
}

func TestResolveWithTheServer(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "pull_request", prPayload())
	d := installDeps(t, &fakeDeps{graph: sampleGraph(), changed: []string{"stacks/a/main.tf"}, resolveErr: errors.New("must not resolve locally")})
	want := sampleResolve()
	fs.setResolve(*want)

	r := h.run("resolve", "--stacks", "./stacks/a/, stacks/b,stacks/a")
	require.Equal(t, 0, r.code, r.stderr)

	require.Len(t, d.scans, 1)
	assert.Equal(t, scanCall{root: h.root, repo: "acme/infra", sha: headSHA, cfg: d.scans[0].cfg}, d.scans[0])
	assert.Equal(t, 1, d.scans[0].cfg.Version)
	assert.Equal(t, [][2]string{{baseSHA, "HEAD"}}, d.diffs)
	assert.Empty(t, d.resolves)

	require.Len(t, fs.creates, 1)
	assert.Equal(t, v1.CreateRunRequest{
		Repo: "acme/infra", SHA: headSHA, BaseSHA: baseSHA, PRNumber: 7, Mode: v1.ModePlan,
		Trigger: v1.TriggerPullRequest, WorkflowRunID: 4242, Attempt: 2,
	}, fs.creates[0])
	require.Len(t, fs.graphs, 1)
	up := fs.graphs[0]
	assert.Equal(t, *sampleGraph(), up.Graph)
	assert.Equal(t, []string{"stacks/a/main.tf"}, up.ChangedPaths)
	assert.Equal(t, baseSHA, up.BaseSHA)
	assert.Equal(t, []string{"stacks/a", "stacks/b"}, up.Stacks)
	require.NotNil(t, up.Config)
	assert.Equal(t, []string{"Bearer oidc-token"}, fs.auth["create"])
	assert.Equal(t, []string{"Bearer oidc-token"}, fs.auth["graph"])

	outs := h.outputs()
	assert.Equal(t, "run-1", outs["run-id"])
	assert.Equal(t, jsonLine(want.Matrix), outs["matrix"])
	assert.Equal(t, `[["stacks/a"],["stacks/b"]]`, outs["waves"])
	assert.Equal(t, `["stacks/a","stacks/b"]`, outs["affected"])
	assert.Equal(t, "2", outs["count"])
	assert.Equal(t, "false", outs["unconfirmed"])
	assert.NotContains(t, outs["matrix"], "\n")

	assert.Contains(t, r.stdout, "run run-1\n")
	assert.Contains(t, r.stdout, "WAVE  STACK")
	assert.Regexp(t, `0\s+stacks/a\s+changed,module\s+production`, r.stdout)
	assert.Regexp(t, `1\s+stacks/b\s+dependent\s+default\s+locked by PR #3`, r.stdout)
	assert.Contains(t, r.stderr, "::warning::stacks/c: backend bucket is not a literal")
	assert.Contains(t, r.stderr, "::warning::stacks/b is locked by PR #3")
	assert.Empty(t, fs.neutralChecks())
	assert.Contains(t, h.stepSummary(), "| 0 | `stacks/a` | changed,module | production |")
}

func TestResolveFallsBackLocally(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, fs *fakeServer)
	}{
		{name: "connection refused", setup: func(t *testing.T, _ *fakeServer) { t.Setenv(EnvServerURL, deadURL(t)) }},
		{name: "create fails", setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("create", 503, "internal") }},
		{name: "graph upload fails", setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("graph", 500, "internal") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			tt.setup(t, fs)
			local := sampleResolve()
			local.RunID = "ignored"
			d := installDeps(t, &fakeDeps{graph: sampleGraph(), changed: []string{"modules/vpc/main.tf"}, resolve: local})

			r := h.run("resolve", "--stacks", "stacks/a")
			require.Equal(t, 0, r.code, r.stderr)
			assert.Equal(t, []resolveCall{{changed: []string{"modules/vpc/main.tf"}, requested: []string{"stacks/a"}}}, d.resolves)

			outs := h.outputs()
			assert.Empty(t, outs["run-id"])
			assert.Equal(t, "true", outs["unconfirmed"])
			assert.Equal(t, "2", outs["count"])
			checks := fs.neutralChecks()
			require.Len(t, checks, 1)
			assert.Equal(t, "stackorder/resolve", checks[0].Name)
			assert.Equal(t, headSHA, checks[0].HeadSHA)
			assert.Equal(t, "neutral", checks[0].Conclusion)
			assert.Equal(t, "Unconfirmed: 2 stacks affected", checks[0].Output.Title)
			assert.Contains(t, checks[0].Output.Summary, "server is unreachable")
			assert.Contains(t, checks[0].Output.Summary, "| 1 | `stacks/b` | dependent | default |")
			assert.Contains(t, r.stderr, "::warning::the resolution is unconfirmed")
			assert.Contains(t, h.stepSummary(), "Unconfirmed")
		})
	}
}

func TestResolveNeutralCheckProblems(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, fs *fakeServer)
		want  string
	}{
		{name: "no token", setup: func(t *testing.T, _ *fakeServer) { t.Setenv("GITHUB_TOKEN", "") }, want: "GITHUB_TOKEN is not set"},
		{name: "token cannot write checks", setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("check-runs", 403, "") }, want: "GitHub returned 403"},
		{name: "no repository", setup: func(t *testing.T, _ *fakeServer) { t.Setenv("GITHUB_REPOSITORY", "") }, want: "the repository and commit are unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", map[string]any{"pull_request": prPayload()["pull_request"]})
			t.Setenv(EnvServerURL, deadURL(t))
			tt.setup(t, fs)
			installDeps(t, &fakeDeps{graph: sampleGraph(), resolve: &v1.ResolveResponse{}})
			r := h.run("resolve")
			require.Equal(t, 0, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.want)
			assert.Equal(t, `{"include":[]}`, h.outputs()["matrix"])
			assert.Equal(t, `[]`, h.outputs()["waves"])
			assert.Equal(t, "0", h.outputs()["count"])
		})
	}
}

func TestResolveForkPullRequest(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	payload := prPayload()
	payload["pull_request"].(map[string]any)["head"] = map[string]any{"sha": headSHA, "repo": map[string]any{"full_name": "stranger/infra", "fork": true}}
	h.ci(fs, "pull_request", payload)
	d := installDeps(t, &fakeDeps{graph: sampleGraph()})

	r := h.run("resolve")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Empty(t, d.scans)
	assert.Zero(t, fs.hitCount("create"))
	outs := h.outputs()
	assert.Equal(t, "0", outs["count"])
	assert.Equal(t, `{"include":[]}`, outs["matrix"])
	checks := fs.neutralChecks()
	require.Len(t, checks, 1)
	assert.Equal(t, "Fork pull request: not planned", checks[0].Output.Title)
	assert.Contains(t, r.stderr, "::notice::Fork pull requests")
	assert.Contains(t, r.stdout, "no stacks affected")
}

func TestResolveCycles(t *testing.T) {
	cyclic := &v1.ResolveResponse{Cycles: [][]string{{"stacks/a", "stacks/b"}, {"stacks/c", "stacks/d", "stacks/c"}}}
	t.Run("from the server", func(t *testing.T) {
		h := newHarness(t)
		fs := newFakeServer(t)
		h.ci(fs, "pull_request", prPayload())
		fs.setResolve(*cyclic)
		installDeps(t, &fakeDeps{graph: sampleGraph()})
		r := h.run("resolve")
		assert.Equal(t, ExitFailure, r.code)
		assert.Equal(t, "cycle: stacks/a -> stacks/b -> stacks/a\ncycle: stacks/c -> stacks/d -> stacks/c\n", r.stdout)
		assert.Contains(t, r.stderr, "::error::dependency cycle")
		assert.Empty(t, h.outputs())
	})
	t.Run("computed locally", func(t *testing.T) {
		h := newHarness(t)
		installDeps(t, &fakeDeps{graph: sampleGraph(), resolve: cyclic, resolveErr: errors.New("resolve: dependency cycle")})
		r := h.run("--format", "json", "resolve", "--base", "main")
		assert.Equal(t, ExitFailure, r.code)
		var resp v1.ResolveResponse
		require.NoError(t, json.Unmarshal([]byte(r.stdout), &resp))
		assert.Equal(t, cyclic.Cycles, resp.Cycles)
		assert.True(t, resp.Unconfirmed)
	})
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, h *harness, fs *fakeServer, d *fakeDeps)
		args  []string
		want  int
		msg   string
	}{
		{name: "server refuses", setup: func(_ *testing.T, _ *harness, fs *fakeServer, _ *fakeDeps) { fs.failWith("graph", 403, "forbidden") }, want: ExitRefused, msg: "injected forbidden"},
		{name: "server rejects", setup: func(_ *testing.T, _ *harness, fs *fakeServer, _ *fakeDeps) { fs.failWith("create", 422, "invalid") }, want: ExitFailure, msg: "injected invalid"},
		{name: "no OIDC token", setup: func(t *testing.T, _ *harness, _ *fakeServer, _ *fakeDeps) {
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
		}, want: ExitFailure, msg: "id-token: write"},
		{name: "scan fails", setup: func(_ *testing.T, _ *harness, _ *fakeServer, d *fakeDeps) { d.scanErr = errors.New("bad hcl") }, want: ExitFailure, msg: "scanning"},
		{name: "diff fails", setup: func(_ *testing.T, _ *harness, _ *fakeServer, d *fakeDeps) { d.changedErr = errors.New("bad object") }, want: ExitFailure, msg: "listing the paths changed since " + baseSHA},
		{name: "invalid config", setup: func(t *testing.T, h *harness, _ *fakeServer, _ *fakeDeps) {
			writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "tool: pulumi\n")
		}, want: ExitFailure, msg: "stackorder.yaml"},
		{name: "invalid base", args: []string{"--base=-x"}, want: ExitFailure, msg: "invalid git revision"},
		{name: "invalid event", setup: func(t *testing.T, _ *harness, _ *fakeServer, _ *fakeDeps) {
			t.Setenv("GITHUB_EVENT_PATH", writeFile(t, filepath.Join(t.TempDir(), "event.json"), "{"))
		}, want: ExitFailure, msg: "decoding the event payload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			d := installDeps(t, &fakeDeps{graph: sampleGraph(), changed: []string{"x"}})
			if tt.setup != nil {
				tt.setup(t, h, fs, d)
			}
			r := h.run(append([]string{"resolve"}, tt.args...)...)
			assert.Equal(t, tt.want, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.msg)
			assert.Empty(t, fs.neutralChecks())
		})
	}
}

func TestResolveOutsideActions(t *testing.T) {
	setupRepo := func(t *testing.T, h *harness) (first, second string) {
		first = initGit(t, h.root)
		gitRun(t, h.root, "update-ref", "refs/remotes/origin/trunk", first)
		gitRun(t, h.root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
		writeFile(t, filepath.Join(h.root, "stacks", "app", "extra.tf"), "")
		gitRun(t, h.root, "add", "-A")
		gitRun(t, h.root, "commit", "-q", "-m", "second")
		gitRun(t, h.root, "remote", "add", "origin", "ssh://git@github.com/acme/infra.git")
		return first, gitRun(t, h.root, "rev-parse", "HEAD")
	}
	t.Run("merge base with the default branch and no server", func(t *testing.T) {
		h := newHarness(t)
		first, second := setupRepo(t, h)
		d := installDeps(t, &fakeDeps{graph: sampleGraph(), changed: []string{"stacks/app/extra.tf"}, resolve: sampleResolve()})
		r := h.run("--format", "json", "resolve")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, [][2]string{{first, "HEAD"}}, d.diffs)
		assert.Equal(t, "acme/infra", d.scans[0].repo)
		assert.Equal(t, second, d.scans[0].sha)
		var resp v1.ResolveResponse
		require.NoError(t, json.Unmarshal([]byte(r.stdout), &resp))
		assert.True(t, resp.Unconfirmed)
		assert.Len(t, resp.Affected, 2)
		assert.NotContains(t, r.stderr, "::")
	})
	t.Run("explicit base with an api key reports to the server", func(t *testing.T) {
		h := newHarness(t)
		first, second := setupRepo(t, h)
		fs := newFakeServer(t)
		fs.setResolve(*sampleResolve())
		t.Setenv(EnvAPIKey, "sk_dev")
		d := installDeps(t, &fakeDeps{graph: sampleGraph(), changed: []string{"stacks/app/extra.tf"}})
		r := h.run("--server", fs.url(), "resolve", "--base", "HEAD~1")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, [][2]string{{"HEAD~1", "HEAD"}}, d.diffs)
		require.Len(t, fs.creates, 1)
		assert.Equal(t, v1.CreateRunRequest{Repo: "acme/infra", SHA: second, BaseSHA: first, Mode: v1.ModePlan, Trigger: v1.TriggerManual}, fs.creates[0])
		assert.Equal(t, []string{"Bearer sk_dev"}, fs.auth["create"])
	})
	t.Run("server without an api key resolves locally", func(t *testing.T) {
		h := newHarness(t)
		setupRepo(t, h)
		fs := newFakeServer(t)
		d := installDeps(t, &fakeDeps{graph: sampleGraph(), resolve: &v1.ResolveResponse{}})
		r := h.run("--server", fs.url(), "resolve")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Len(t, d.resolves, 1)
		assert.Zero(t, fs.hitCount("create"))
		assert.Equal(t, "no stacks affected\n", r.stdout)
	})
	t.Run("no base", func(t *testing.T) {
		h := newHarness(t)
		installDeps(t, &fakeDeps{graph: sampleGraph()})
		r := h.run("resolve")
		assert.Equal(t, ExitFailure, r.code)
		assert.Contains(t, r.stderr, "pass --base")
	})
}

func TestResolvePushBase(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	fs.setResolve(*sampleResolve())
	h.ci(fs, "push", map[string]any{"before": baseSHA, "after": mergeSHA, "repository": map[string]any{"default_branch": "main"}})
	d := installDeps(t, &fakeDeps{graph: sampleGraph()})
	r := h.run("resolve")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, [][2]string{{baseSHA, "HEAD"}}, d.diffs)
	assert.Equal(t, v1.TriggerPush, fs.creates[0].Trigger)
	assert.Equal(t, mergeSHA, fs.creates[0].SHA)
}

func TestAffected(t *testing.T) {
	tests := []struct {
		name     string
		format   string
		resolve  *v1.ResolveResponse
		wantCode int
		check    func(t *testing.T, r result, d *fakeDeps)
	}{
		{
			name:     "text with affected stacks",
			resolve:  sampleResolve(),
			wantCode: ExitChanges,
			check: func(t *testing.T, r result, _ *fakeDeps) {
				lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
				require.Len(t, lines, 3)
				assert.True(t, strings.HasPrefix(lines[1], "0 "))
				assert.Contains(t, lines[2], "stacks/b")
			},
		},
		{
			name:     "nothing affected",
			resolve:  &v1.ResolveResponse{Affected: []v1.AffectedStack{}},
			wantCode: 0,
			check: func(t *testing.T, r result, _ *fakeDeps) {
				assert.Equal(t, "no stacks affected\n", r.stdout)
			},
		},
		{
			name:     "json",
			format:   "json",
			resolve:  sampleResolve(),
			wantCode: ExitChanges,
			check: func(t *testing.T, r result, _ *fakeDeps) {
				var resp v1.ResolveResponse
				require.NoError(t, json.Unmarshal([]byte(r.stdout), &resp))
				assert.Equal(t, *sampleResolve(), resp)
			},
		},
		{
			name:     "dot highlights waves",
			format:   "dot",
			resolve:  sampleResolve(),
			wantCode: ExitChanges,
			check: func(t *testing.T, r result, d *fakeDeps) {
				assert.Equal(t, "digraph {}\n", r.stdout)
				assert.Equal(t, []map[string]int{{"stacks/a": 0, "stacks/b": 1}}, d.dots)
			},
		},
		{
			name:     "cycle",
			resolve:  &v1.ResolveResponse{Cycles: [][]string{{"stacks/a", "stacks/b"}}},
			wantCode: ExitFailure,
			check: func(t *testing.T, r result, _ *fakeDeps) {
				assert.Equal(t, "cycle: stacks/a -> stacks/b -> stacks/a\n", r.stdout)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			d := installDeps(t, &fakeDeps{graph: sampleGraph(), changed: []string{"stacks/a/main.tf"}, resolve: tt.resolve, dot: "digraph {}\n"})
			args := []string{"affected", "--base", "origin/main"}
			if tt.format != "" {
				args = append([]string{"--format", tt.format}, args...)
			}
			r := h.run(args...)
			require.Equal(t, tt.wantCode, r.code, r.stderr)
			assert.Equal(t, [][2]string{{"origin/main", "HEAD"}}, d.diffs)
			tt.check(t, r, d)
		})
	}
	t.Run("resolution error", func(t *testing.T) {
		h := newHarness(t)
		installDeps(t, &fakeDeps{graph: sampleGraph(), resolveErr: errors.New("invalid graph")})
		r := h.run("affected", "--base", "main")
		assert.Equal(t, ExitFailure, r.code)
		assert.Contains(t, r.stderr, "invalid graph")
	})
}

func TestGraph(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		h := newHarness(t)
		d := installDeps(t, &fakeDeps{graph: sampleGraph()})
		r := h.run("graph")
		require.Equal(t, 0, r.code, r.stderr)
		want := "3 stacks, 1 modules, 3 edges\n" +
			"\nacme/network//stacks/tgw (external)\n" +
			"\nstacks/a\n  uses_module  acme/infra//modules/vpc @v1.2.0\n" +
			"\nstacks/b\n  depends_on   stacks/a\n  reads_state  stacks/a (inferred)\n" +
			"\nacme/infra//modules/vpc (local module)\n" +
			"\nwarning: stacks/c: backend bucket is not a literal\n"
		assert.Equal(t, want, r.stdout)
		assert.Empty(t, d.diffs)
		assert.Contains(t, r.stderr, "backend bucket is not a literal")
	})
	t.Run("json", func(t *testing.T) {
		h := newHarness(t)
		installDeps(t, &fakeDeps{graph: sampleGraph()})
		r := h.run("--format", "json", "graph")
		require.Equal(t, 0, r.code, r.stderr)
		var g v1.Graph
		require.NoError(t, json.Unmarshal([]byte(r.stdout), &g))
		assert.Equal(t, *sampleGraph(), g)
	})
	t.Run("dot", func(t *testing.T) {
		h := newHarness(t)
		d := installDeps(t, &fakeDeps{graph: sampleGraph(), dot: "digraph g {}\n"})
		r := h.run("--format", "dot", "graph")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, "digraph g {}\n", r.stdout)
		assert.Equal(t, []map[string]int{nil}, d.dots)
	})
}

func TestUnlinkedDependencies(t *testing.T) {
	tests := []struct {
		name string
		args []string
		bind Deps
		want error
	}{
		{name: "graph without scan", args: []string{"graph"}, want: ErrScanNotLinked},
		{name: "affected without scan", args: []string{"affected", "--base", "main"}, want: ErrScanNotLinked},
		{
			name: "affected without graph",
			args: []string{"affected", "--base", "main"},
			bind: Deps{
				ScanRepo: func(context.Context, string, string, string, *v1.RepoConfig) (*v1.Graph, error) {
					return sampleGraph(), nil
				},
				ChangedPaths: func(context.Context, string, string, string) ([]string, error) { return nil, nil },
			},
			want: ErrGraphNotLinked,
		},
		{
			name: "dot without graph",
			args: []string{"--format", "dot", "graph"},
			bind: Deps{ScanRepo: func(context.Context, string, string, string, *v1.RepoConfig) (*v1.Graph, error) {
				return sampleGraph(), nil
			}},
			want: ErrGraphNotLinked,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			oldScan, oldChanged, oldResolve, oldDOT := scanRepo, changedPaths, resolveLocal, renderDOT
			t.Cleanup(func() {
				scanRepo, changedPaths, resolveLocal, renderDOT = oldScan, oldChanged, oldResolve, oldDOT
			})
			Bind(tt.bind)
			r := h.run(tt.args...)
			assert.Equal(t, ExitFailure, r.code)
			assert.ErrorIs(t, r.err, tt.want)
			assert.Contains(t, r.stderr, tt.want.Error())
		})
	}
}

func TestResolveTitle(t *testing.T) {
	for n, want := range map[int]string{0: "Unconfirmed: no stacks affected", 1: "Unconfirmed: 1 stack affected", 5: "Unconfirmed: 5 stacks affected"} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			assert.Equal(t, want, resolveTitle(&v1.ResolveResponse{Affected: make([]v1.AffectedStack, n)}))
		})
	}
}

func TestBaseSHA(t *testing.T) {
	h := newHarness(t)
	sha := initGit(t, h.root)
	a := &app{root: h.root}
	assert.Equal(t, sha, a.baseSHA(context.Background(), "HEAD"))
	assert.Equal(t, baseSHA, a.baseSHA(context.Background(), baseSHA))
	assert.Empty(t, a.baseSHA(context.Background(), "no-such-branch"))
	assert.Empty(t, a.baseSHA(context.Background(), "-x"))
	_, err := os.Stat(filepath.Join(h.root, ".git"))
	require.NoError(t, err)
}

func TestRepositoryRoot(t *testing.T) {
	h := newHarness(t)
	r := h.run("--repo-root", "/nonexistent/stackorder", "graph")
	assert.Equal(t, ExitFailure, r.code)
	assert.Contains(t, r.stderr, "repository root")

	r = h.run("--repo-root", h.root+"/stackorder.yaml", "graph")
	assert.Equal(t, ExitFailure, r.code)
	assert.Contains(t, r.stderr, "is not a directory")
}
