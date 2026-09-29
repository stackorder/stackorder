package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func applyRun(sha string, status v1.RunStatus, row v1.RunStack) v1.Run {
	return v1.Run{ID: "run-1", SHA: sha, Status: status, Mode: v1.ModeApply, Stacks: []v1.RunStack{row}}
}

func plannedRow() v1.RunStack {
	s := changesSummary()
	return v1.RunStack{Key: "stacks/app", Path: "stacks/app", Status: v1.StackApplying, Summary: &s,
		Lock: &v1.LockInfo{StackKey: "stacks/app", RunID: "run-1", PRNumber: 7}}
}

func (h *harness) savedPlan(sha string) string {
	h.t.Helper()
	return writeFile(h.t, filepath.Join(h.root, ".stackorder", "plans", v1.PlanArtifactName("stacks/app", sha)+".tfplan"), "saved plan")
}

func newApplyHarness(t *testing.T) (*harness, *fakeServer) {
	t.Helper()
	h := newHarness(t)
	h.sha = initGit(t, h.root)
	fs := newFakeServer(t)
	h.ci(fs, "workflow_dispatch", dispatchPayload("run-1", h.sha))
	fs.setRun(applyRun(h.sha, v1.RunApplying, plannedRow()))
	return h, fs
}

func TestApplySavedPlan(t *testing.T) {
	h, fs := newApplyHarness(t)
	planFile := h.savedPlan(h.sha)

	r := h.run("apply", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)

	assert.Equal(t, []string{"version", "init", "show", "apply"}, h.tfCommands())
	calls := h.tfCalls()
	assert.Equal(t, []string{"apply", "-input=false", "-no-color", planFile}, calls[3])
	assert.Equal(t, []string{"Bearer oidc-token"}, fs.auth["run"])

	posted := fs.lastResult()
	assert.Equal(t, "run-1", posted.RunID)
	got := posted.Result
	assert.Equal(t, v1.ModeApply, got.Mode)
	assert.Equal(t, v1.ResultSuccess, got.Status)
	assert.Equal(t, 0, got.ExitCode)
	assert.True(t, got.HasChanges)
	assert.Equal(t, changesSummary(), *got.Summary)
	assert.Equal(t, v1.PlanArtifactName("stacks/app", h.sha), got.Artifact)
	assert.Equal(t, "https://github.example/acme/infra/actions/runs/4242", got.JobURL)
	assert.Empty(t, got.ErrorText)
	assert.Contains(t, h.outputs()["summary"], `"added":["aws_s3_bucket.logs"]`)
	assert.Contains(t, r.stdout, "Apply complete!")
	assert.Contains(t, r.stdout, "stacks/app: applied: 1 to add")
	assert.Contains(t, h.stepSummary(), "### stackorder apply: `stacks/app`")
}

func TestApplyRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, h *harness, fs *fakeServer)
		want  string
	}{
		{
			name:  "server unreachable",
			setup: func(t *testing.T, _ *harness, _ *fakeServer) { t.Setenv(EnvServerURL, deadURL(t)) },
			want:  "fail closed",
		},
		{
			name:  "server failing",
			setup: func(_ *testing.T, _ *harness, fs *fakeServer) { fs.failWith("run", 502, "internal") },
			want:  "fail closed",
		},
		{
			name:  "server forbids",
			setup: func(_ *testing.T, _ *harness, fs *fakeServer) { fs.failWith("run", 403, "forbidden") },
			want:  "injected forbidden",
		},
		{
			name:  "no server",
			setup: func(t *testing.T, _ *harness, _ *fakeServer) { t.Setenv(EnvServerURL, "") },
			want:  "no server is configured",
		},
		{
			name: "run not applying",
			setup: func(_ *testing.T, h *harness, fs *fakeServer) {
				fs.setRun(applyRun(h.sha, v1.RunFailed, plannedRow()))
			},
			want: "run run-1 is failed",
		},
		{
			name: "stack not in the run",
			setup: func(_ *testing.T, h *harness, fs *fakeServer) {
				row := plannedRow()
				row.Key = "stacks/other"
				fs.setRun(applyRun(h.sha, v1.RunApplying, row))
			},
			want: "stack stacks/app is not part of run run-1",
		},
		{
			name: "stack blocked",
			setup: func(_ *testing.T, h *harness, fs *fakeServer) {
				row := plannedRow()
				row.Status = v1.StackBlocked
				fs.setRun(applyRun(h.sha, v1.RunApplying, row))
			},
			want: "stack stacks/app is blocked in run run-1",
		},
		{
			name: "stack not locked",
			setup: func(_ *testing.T, h *harness, fs *fakeServer) {
				row := plannedRow()
				row.Lock = nil
				fs.setRun(applyRun(h.sha, v1.RunApplying, row))
			},
			want: "run run-1 does not hold the lock on stacks/app",
		},
		{
			name: "stack locked by another run",
			setup: func(_ *testing.T, h *harness, fs *fakeServer) {
				row := plannedRow()
				row.Lock = &v1.LockInfo{StackKey: "stacks/app", RunID: "run-2", PRNumber: 8}
				fs.setRun(applyRun(h.sha, v1.RunApplying, row))
			},
			want: "stacks/app is locked by run run-2 of #8",
		},
		{
			name: "run for another commit",
			setup: func(_ *testing.T, _ *harness, fs *fakeServer) {
				fs.setRun(applyRun(baseSHA, v1.RunApplying, plannedRow()))
			},
			want: "is for commit \"" + baseSHA + "\"",
		},
		{
			name: "checkout at another commit",
			setup: func(t *testing.T, h *harness, _ *fakeServer) {
				gitRun(t, h.root, "commit", "-q", "--allow-empty", "-m", "moved on")
			},
			want: "but the checkout is at",
		},
		{
			name: "checkout without git",
			setup: func(t *testing.T, h *harness, _ *fakeServer) {
				require.NoError(t, os.RemoveAll(filepath.Join(h.root, ".git")))
			},
			want: "cannot read the commit of the checkout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := newApplyHarness(t)
			h.savedPlan(h.sha)
			tt.setup(t, h, fs)
			r := h.run("apply", "--stack", "stacks/app", "--run-id", "run-1")
			assert.Equal(t, ExitRefused, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.want)
			assert.Empty(t, h.tfCalls())
			assert.Empty(t, fs.postedResults())
		})
	}
}

func TestApplyUsage(t *testing.T) {
	tests := []struct {
		name  string
		ci    bool
		args  []string
		setup func(t *testing.T)
		want  string
	}{
		{name: "no run id", ci: true, args: []string{"apply", "--stack", "stacks/app"}, want: "apply needs --run-id"},
		{name: "outside actions without --local", args: []string{"apply", "--stack", "stacks/app", "--run-id", "run-1"}, want: "needs --local"},
		{name: "--local inside actions", ci: true, args: []string{"apply", "--stack", "stacks/app", "--local"}, want: "--local applies from outside GitHub Actions"},
		{name: "--local without a server", args: []string{"apply", "--stack", "stacks/app", "--local"}, want: "no server is configured"},
		{
			name:  "--local without an api key",
			args:  []string{"apply", "--stack", "stacks/app", "--local"},
			setup: func(t *testing.T) { t.Setenv(EnvServerURL, "http://127.0.0.1:1") },
			want:  "needs an automation API key",
		},
		{
			name: "--local with --plan-file",
			args: []string{"apply", "--stack", "stacks/app", "--local", "--plan-file", "x.tfplan"},
			setup: func(t *testing.T) {
				t.Setenv(EnvServerURL, "http://127.0.0.1:1")
				t.Setenv(EnvAPIKey, "sk_test")
			},
			want: "--plan-file cannot be combined with --local",
		},
		{
			name: "--local with --run-id",
			args: []string{"apply", "--stack", "stacks/app", "--local", "--run-id", "run-1"},
			setup: func(t *testing.T) {
				t.Setenv(EnvServerURL, "http://127.0.0.1:1")
				t.Setenv(EnvAPIKey, "sk_test")
			},
			want: "--run-id cannot be combined with --local",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			if tt.ci {
				h.ci(fs, "workflow_dispatch", map[string]any{})
			}
			if tt.setup != nil {
				tt.setup(t)
			}
			r := h.run(tt.args...)
			assert.Equal(t, ExitFailure, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.want)
			assert.Empty(t, h.tfCalls())
			assert.Zero(t, fs.hitCount("run"))
		})
	}
}

func TestApplyReplansAMissingPlan(t *testing.T) {
	tests := []struct {
		name        string
		showJSON    string
		recorded    *v1.PlanSummary
		fromPlan    string
		saved       bool
		wantCode    int
		wantApplied bool
		wantError   string
	}{
		{name: "same addresses", showJSON: "plan_changes.json", recorded: ptr(changesSummary()), wantCode: 0, wantApplied: true},
		{name: "different addresses", showJSON: "plan_other.json", recorded: ptr(changesSummary()), wantCode: ExitRefused, wantError: "not in the recorded plan: aws_db_instance.main; missing from the new plan: aws_iam_role.app, aws_instance.web"},
		{name: "no recorded summary", showJSON: "plan_changes.json", wantCode: ExitRefused, wantError: "recorded no plan summary"},
		{name: "from_plan disabled", showJSON: "plan_changes.json", recorded: ptr(changesSummary()), fromPlan: "false", saved: true, wantCode: 0, wantApplied: true},
		{name: "empty plans match", showJSON: "plan_noop.json", recorded: &v1.PlanSummary{}, wantCode: 0, wantApplied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := newApplyHarness(t)
			h.tf.ShowJSON = filepath.Join(filepath.Dir(h.tf.ShowJSON), tt.showJSON)
			row := plannedRow()
			row.Summary = tt.recorded
			fs.setRun(applyRun(h.sha, v1.RunApplying, row))
			if tt.fromPlan != "" {
				writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "version: 1\napply:\n  from_plan: "+tt.fromPlan+"\n")
			}
			if tt.saved {
				h.savedPlan(h.sha)
			}
			r := h.run("apply", "--stack", "stacks/app")
			require.Equal(t, tt.wantCode, r.code, r.stderr)
			cmds := h.tfCommands()
			assert.Equal(t, tt.recorded != nil, strings.Contains(strings.Join(cmds, " "), "plan"))
			assert.Equal(t, tt.wantApplied, strings.Contains(strings.Join(cmds, " "), "apply"))
			got := fs.lastResult().Result
			if tt.wantApplied {
				assert.Equal(t, v1.ResultSuccess, got.Status)
				return
			}
			assert.Equal(t, v1.ResultFailure, got.Status)
			assert.Equal(t, ExitRefused, got.ExitCode)
			assert.Contains(t, got.ErrorText, tt.wantError)
			assert.Contains(t, r.stderr, tt.wantError)
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestApplyFromPlanDisabledIgnoresTheDownloadedPlan(t *testing.T) {
	tests := []struct {
		name     string
		showJSON string
		wantCode int
	}{
		{name: "new plan matches the recorded one", showJSON: "plan_changes.json", wantCode: 0},
		{name: "new plan differs", showJSON: "plan_other.json", wantCode: ExitRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := newApplyHarness(t)
			writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "version: 1\napply:\n  from_plan: false\n")
			h.tf.ShowJSON = filepath.Join(filepath.Dir(h.tf.ShowJSON), tt.showJSON)
			planFile := writeFile(t, filepath.Join(t.TempDir(), "downloaded.tfplan"), "downloaded plan")
			r := h.run("apply", "--stack", "stacks/app", "--plan-file", planFile)
			require.Equal(t, tt.wantCode, r.code, r.stderr)
			cmds := h.tfCommands()
			assert.Contains(t, cmds, "plan")
			assert.Contains(t, r.stderr, "::warning::apply.from_plan is false, so stacks/app is planned again")
			if tt.wantCode != 0 {
				assert.NotContains(t, cmds, "apply")
				assert.Equal(t, ExitRefused, fs.lastResult().Result.ExitCode)
				return
			}
			assert.Equal(t, []string{"version", "init", "plan", "show", "apply"}, cmds)
			assert.Equal(t, v1.ResultSuccess, fs.lastResult().Result.Status)
		})
	}
}

func TestApplyFailures(t *testing.T) {
	t.Run("apply fails", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		h.savedPlan(h.sha)
		h.tf.ApplyExit = 1
		h.tf.FailOutput = "creating S3 bucket: AccessDenied"
		r := h.run("apply", "--stack", "stacks/app")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		got := fs.lastResult().Result
		assert.Equal(t, v1.ResultFailure, got.Status)
		assert.Equal(t, 1, got.ExitCode)
		assert.Contains(t, got.ErrorText, "Error: creating S3 bucket: AccessDenied")
		assert.Contains(t, r.stderr, "::error::apply of stacks/app failed")
	})
	t.Run("explicit plan file whose artifact expired is planned again", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		planFile := filepath.Join(t.TempDir(), "not-downloaded", "stackorder-plan.tfplan")
		r := h.run("apply", "--stack", "stacks/app", "--plan-file", planFile)
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, []string{"version", "init", "plan", "show", "apply"}, h.tfCommands())
		calls := h.tfCalls()
		assert.Contains(t, calls[2], "-out="+planFile)
		assert.Equal(t, []string{"apply", "-input=false", "-no-color", planFile}, calls[4])
		assert.Contains(t, r.stderr, "::warning::the plan file for stacks/app is not available")
		assert.Equal(t, v1.ResultSuccess, fs.lastResult().Result.Status)
	})
	t.Run("explicit plan file whose artifact expired and whose new plan differs", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		h.tf.ShowJSON = filepath.Join(filepath.Dir(h.tf.ShowJSON), "plan_other.json")
		r := h.run("apply", "--stack", "stacks/app", "--plan-file", filepath.Join(t.TempDir(), "gone.tfplan"))
		require.Equal(t, ExitRefused, r.code, r.stderr)
		assert.NotContains(t, h.tfCommands(), "apply")
		got := fs.lastResult().Result
		assert.Equal(t, v1.ResultFailure, got.Status)
		assert.Equal(t, ExitRefused, got.ExitCode)
		assert.Contains(t, got.ErrorText, "does not match the plan recorded in run run-1")
	})
	t.Run("explicit plan file", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		planFile := writeFile(t, filepath.Join(t.TempDir(), "downloaded.tfplan"), "plan")
		r := h.run("apply", "--stack", "stacks/app", "--plan-file", planFile)
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, v1.ResultSuccess, fs.lastResult().Result.Status)
		assert.FileExists(t, filepath.Join(filepath.Dir(planFile), "downloaded.json"))
	})
	t.Run("reporting fails after the apply", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		h.savedPlan(h.sha)
		fs.failWith("result", 503, "internal")
		r := h.run("apply", "--stack", "stacks/app")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		assert.Contains(t, r.stderr, "applied stacks/app, but reporting the result failed")
		assert.Contains(t, h.tfCommands(), "apply")
	})
	t.Run("stack config is invalid", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), "depends_on: [\"\"]\n")
		r := h.run("apply", "--stack", "stacks/app")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		assert.Zero(t, fs.hitCount("run"))
	})
}

func TestApplyLocal(t *testing.T) {
	newLocal := func(t *testing.T) (*harness, *fakeServer, string) {
		h := newHarness(t)
		fs := newFakeServer(t)
		fs.runID = "run-local"
		sha := initGit(t, h.root)
		gitRun(t, h.root, "remote", "add", "origin", "git@github.com:acme/infra.git")
		t.Setenv(EnvServerURL, fs.url())
		t.Setenv(EnvAPIKey, "sk_live_key")
		return h, fs, sha
	}
	t.Run("takes the lock through a manual run", func(t *testing.T) {
		h, fs, sha := newLocal(t)
		h.savedPlan(sha)
		r := h.run("apply", "--stack", "stacks/app", "--local")
		require.Equal(t, 0, r.code, r.stderr)
		require.Len(t, fs.creates, 1)
		assert.Equal(t, v1.CreateRunRequest{Repo: "acme/infra", SHA: sha, Mode: v1.ModeApply, Trigger: v1.TriggerManual, Stacks: []string{"stacks/app"}}, fs.creates[0])
		assert.Equal(t, []string{"Bearer sk_live_key"}, fs.auth["create"])
		assert.Equal(t, []string{"Bearer sk_live_key"}, fs.auth["result"])
		assert.Equal(t, []string{"version", "init", "plan", "show", "apply"}, h.tfCommands())
		posted := fs.lastResult()
		assert.Equal(t, "run-local", posted.RunID)
		assert.Equal(t, v1.ResultSuccess, posted.Result.Status)
		assert.Empty(t, posted.Result.JobURL)
		assert.Zero(t, fs.hitCount("run"))
	})
	t.Run("warns about a dirty tree", func(t *testing.T) {
		h, _, _ := newLocal(t)
		writeFile(t, filepath.Join(h.root, "stacks", "app", "extra.tf"), "")
		r := h.run("apply", "--stack", "stacks/app", "--local")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Contains(t, r.stderr, "uncommitted changes")
	})
	tests := []struct {
		name  string
		setup func(t *testing.T, fs *fakeServer)
		want  int
		msg   string
	}{
		{name: "locked", setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("create", 423, "locked") }, want: ExitRefused, msg: "injected locked"},
		{name: "unreachable", setup: func(t *testing.T, _ *fakeServer) { t.Setenv(EnvServerURL, deadURL(t)) }, want: ExitRefused, msg: "fail closed"},
		{name: "rejected key", setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("create", 401, "unauthorized") }, want: ExitFailure, msg: "injected unauthorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs, _ := newLocal(t)
			tt.setup(t, fs)
			r := h.run("apply", "--stack", "stacks/app", "--local")
			assert.Equal(t, tt.want, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.msg)
			assert.Empty(t, h.tfCalls())
		})
	}
	t.Run("no repository", func(t *testing.T) {
		h := newHarness(t)
		initGit(t, h.root)
		t.Setenv(EnvServerURL, "http://127.0.0.1:1")
		t.Setenv(EnvAPIKey, "sk_live_key")
		r := h.run("apply", "--stack", "stacks/app", "--local")
		assert.Equal(t, ExitFailure, r.code)
		assert.Contains(t, r.stderr, "cannot tell the repository")
	})
	t.Run("no commit", func(t *testing.T) {
		h := newHarness(t)
		t.Setenv("GITHUB_REPOSITORY", "acme/infra")
		t.Setenv(EnvServerURL, "http://127.0.0.1:1")
		t.Setenv(EnvAPIKey, "sk_live_key")
		r := h.run("apply", "--stack", "stacks/app", "--local")
		assert.Equal(t, ExitFailure, r.code)
		assert.Contains(t, r.stderr, "cannot tell the commit")
	})
}

func TestApplyKeepsThePlanDirectoryOverride(t *testing.T) {
	h, fs := newApplyHarness(t)
	dir := t.TempDir()
	t.Setenv(EnvPlanDir, dir)
	writeFile(t, filepath.Join(dir, v1.PlanArtifactName("stacks/app", h.sha)+".tfplan"), "saved")
	r := h.run("apply", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)
	assert.NotContains(t, h.tfCommands(), "plan")
	assert.Equal(t, v1.ResultSuccess, fs.lastResult().Result.Status)
	_, err := os.Stat(filepath.Join(h.root, ".stackorder"))
	assert.True(t, os.IsNotExist(err))
}
