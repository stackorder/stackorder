package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

func changesSummary() v1.PlanSummary {
	return v1.PlanSummary{
		Adds: 1, Changes: 1, Replaces: 1, OutputChanges: 1,
		Added:    []string{"aws_s3_bucket.logs"},
		Changed:  []string{"aws_iam_role.app"},
		Replaced: []string{"aws_instance.web"},
	}
}

func TestPlanReportsTheResult(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "pull_request", prPayload())

	r := h.run("plan", "--stack", "./stacks/app/", "--run-id", "run-1")
	require.Equal(t, 0, r.code, r.stderr)

	artifact := v1.PlanArtifactName("stacks/app", headSHA)
	planFile := filepath.Join(h.root, ".stackorder", "plans", artifact+".tfplan")
	posted := fs.lastResult()
	assert.Equal(t, "run-1", posted.RunID)
	assert.Equal(t, "stacks/app", posted.Key)
	got := posted.Result
	assert.Positive(t, got.DurationMS+1)
	got.DurationMS = 0
	summary := changesSummary()
	want := v1.StackResult{
		Mode:        v1.ModePlan,
		Status:      v1.ResultSuccess,
		ExitCode:    2,
		HasChanges:  true,
		Summary:     &summary,
		PlanText:    "  # aws_s3_bucket.logs will be created\n  + password = \"***\"\n",
		Artifact:    artifact,
		JobURL:      "https://github.example/acme/infra/actions/runs/4242",
		Tool:        v1.ToolTerraform,
		ToolVersion: "1.14.4",
	}
	assert.Empty(t, gocmp.Diff(want, got))

	assert.Equal(t, []string{"Bearer oidc-token"}, fs.auth["result"])
	assert.Equal(t, []string{fs.url()}, fs.audiences)
	assert.Empty(t, fs.neutralChecks())

	outs := h.outputs()
	assert.Equal(t, "true", outs["has-changes"])
	assert.Equal(t, planFile, outs["plan-file"])
	assert.Equal(t, artifact, outs["artifact"])
	assert.Equal(t, "false", outs["unconfirmed"])
	var gotSummary v1.PlanSummary
	require.NoError(t, json.Unmarshal([]byte(outs["summary"]), &gotSummary))
	assert.Equal(t, summary, gotSummary)

	assert.FileExists(t, planFile)
	planJSON, err := os.ReadFile(strings.TrimSuffix(planFile, ".tfplan") + ".json")
	require.NoError(t, err)
	fixture, err := os.ReadFile(h.tf.ShowJSON)
	require.NoError(t, err)
	assert.Equal(t, fixture, planJSON)

	calls := h.tfCalls()
	require.Len(t, calls, 5)
	assert.Equal(t, []string{"version", "-json"}, calls[0])
	assert.Equal(t, []string{"init", "-input=false", "-no-color"}, calls[1])
	assert.Equal(t, []string{"plan", "-input=false", "-no-color", "-detailed-exitcode", "-out=" + planFile}, calls[2])
	assert.Equal(t, []string{"show", "-json", "-no-color", planFile}, calls[3])
	assert.Equal(t, []string{"show", "-no-color", planFile}, calls[4])

	assert.Contains(t, r.stdout, "Terraform has been successfully initialized!")
	assert.Contains(t, r.stdout, "stacks/app: 1 to add, 1 to change, 0 to destroy, 1 to replace")
	summaryMD := h.stepSummary()
	assert.Contains(t, summaryMD, "### stackorder plan: `stacks/app`")
	assert.Contains(t, summaryMD, "- `aws_instance.web`")
	assert.NotContains(t, summaryMD, "Unconfirmed")
}

func TestPlanDegradesWhenTheServerIsUnreachable(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, fs *fakeServer)
		reason string
		hits   int
	}{
		{
			name:   "connection refused",
			setup:  func(t *testing.T, _ *fakeServer) { t.Setenv(EnvServerURL, deadURL(t)) },
			reason: "unreachable",
		},
		{
			name:   "5xx through every retry",
			setup:  func(_ *testing.T, fs *fakeServer) { fs.failWith("result", 503, "internal") },
			reason: "unreachable",
			hits:   4,
		},
		{
			name:   "no run id from resolve",
			setup:  func(*testing.T, *fakeServer) {},
			reason: "no run id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			tt.setup(t, fs)
			args := []string{"plan", "--stack", "stacks/app"}
			if tt.name != "no run id from resolve" {
				args = append(args, "--run-id", "run-1")
			}
			r := h.run(args...)
			require.Equal(t, 0, r.code, r.stderr)
			assert.Equal(t, tt.hits, fs.hitCount("result"))

			outs := h.outputs()
			assert.Equal(t, "true", outs["unconfirmed"])
			assert.Equal(t, "true", outs["has-changes"])
			checks := fs.neutralChecks()
			require.Len(t, checks, 1)
			c := checks[0]
			assert.Equal(t, "stackorder/plan: stacks/app", c.Name)
			assert.Equal(t, headSHA, c.HeadSHA)
			assert.Equal(t, "completed", c.Status)
			assert.Equal(t, "neutral", c.Conclusion)
			assert.Equal(t, "https://github.example/acme/infra/actions/runs/4242", c.DetailsURL)
			assert.Equal(t, "Unconfirmed: 1 to add, 1 to change, 0 to destroy, 1 to replace, 1 output changes", c.Output.Title)
			assert.Contains(t, c.Output.Summary, tt.reason)
			assert.Contains(t, c.Output.Summary, "Applies are refused")
			assert.Contains(t, r.stderr, "::warning::the plan of stacks/app is unconfirmed")
			assert.Contains(t, h.stepSummary(), "Unconfirmed")
		})
	}
}

func TestPlanFailures(t *testing.T) {
	tests := []struct {
		name       string
		tf         func(*fakeTFConfig)
		env        map[string]string
		wantStatus v1.ResultStatus
		wantExit   int
		wantError  string
		hasChanges string
	}{
		{
			name:       "plan fails",
			tf:         func(c *fakeTFConfig) { c.PlanExit = 1 },
			wantStatus: v1.ResultFailure,
			wantExit:   1,
			wantError:  "Error: Invalid provider configuration",
		},
		{
			name:       "init fails",
			tf:         func(c *fakeTFConfig) { c.InitExit = 1 },
			wantStatus: v1.ResultFailure,
			wantExit:   1,
			wantError:  "init exited with code 1",
		},
		{
			name:       "show fails",
			tf:         func(c *fakeTFConfig) { c.ShowExit = 3 },
			wantStatus: v1.ResultFailure,
			wantExit:   3,
			wantError:  "show exited with code 3",
			hasChanges: "true",
		},
		{
			name:       "tool missing",
			env:        map[string]string{tf.EnvTerraformBin: filepath.Join(os.TempDir(), "no-such-terraform")},
			wantStatus: v1.ResultError,
			wantExit:   1,
			wantError:  "tool binary not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			if tt.tf != nil {
				tt.tf(&h.tf)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
			require.Equal(t, ExitFailure, r.code, r.stderr)
			assert.Contains(t, r.stderr, "::error::plan of stacks/app failed")
			got := fs.lastResult().Result
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantExit, got.ExitCode)
			assert.Contains(t, got.ErrorText, tt.wantError)
			assert.Empty(t, got.PlanText)
			assert.Equal(t, cmp.Or(tt.hasChanges, "false"), h.outputs()["has-changes"])
			assert.Equal(t, "false", h.outputs()["unconfirmed"])
			assert.Contains(t, h.stepSummary(), "**"+string(tt.wantStatus)+"**")
		})
	}
}

func TestPlanServerAnswers(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
		want   int
	}{
		{name: "conflict is a refusal", status: 409, code: "conflict", want: ExitRefused},
		{name: "locked is a refusal", status: 423, code: "locked", want: ExitRefused},
		{name: "invalid is an error", status: 400, code: "invalid", want: ExitFailure},
		{name: "unauthorized is an error", status: 401, code: "unauthorized", want: ExitFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			fs.failWith("result", tt.status, tt.code)
			r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
			assert.Equal(t, tt.want, r.code, r.stderr)
			assert.Contains(t, r.stderr, "injected "+tt.code)
			assert.Empty(t, fs.neutralChecks())
			assert.Equal(t, "true", h.outputs()["unconfirmed"])
		})
	}
}

func TestPlanText(t *testing.T) {
	big := strings.Repeat("  # null_resource.item will be created\n", 8000)
	tests := []struct {
		name          string
		stackConfig   string
		showText      string
		wantText      func(t *testing.T, text string)
		wantTruncated bool
		wantShowText  bool
	}{
		{
			name:         "full output is redacted",
			showText:     "token = \"abcd1234efgh\"\nAKIAABCDEFGHIJKLMNOP\n",
			wantText:     func(t *testing.T, text string) { assert.Equal(t, "token = \"***\"\n***\n", text) },
			wantShowText: true,
		},
		{
			name:        "summary output sends no text",
			stackConfig: "plan_output: summary\n",
			showText:    "anything\n",
			wantText:    func(t *testing.T, text string) { assert.Empty(t, text) },
		},
		{
			name:     "long output is truncated",
			showText: big,
			wantText: func(t *testing.T, text string) {
				assert.LessOrEqual(t, len(text), tf.MaxPlanText)
				assert.Contains(t, text, "output truncated")
			},
			wantTruncated: true,
			wantShowText:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			if tt.stackConfig != "" {
				writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), tt.stackConfig)
			}
			h.tf.ShowText = writeFile(t, filepath.Join(t.TempDir(), "show.txt"), tt.showText)
			r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
			require.Equal(t, 0, r.code, r.stderr)
			got := fs.lastResult().Result
			tt.wantText(t, got.PlanText)
			assert.Equal(t, tt.wantTruncated, got.Truncated)
			assert.Equal(t, tt.wantShowText, strings.Count(strings.Join(h.tfCommands(), " "), "show") == 2)
		})
	}
}

func TestPlanStackSettings(t *testing.T) {
	tests := []struct {
		name        string
		stack       string
		stackConfig string
		rootConfig  string
		env         map[string]string
		tofu        bool
		wantKey     string
		wantTool    v1.Tool
		wantCalls   [][]string
		wantWarning string
	}{
		{
			name:        "workspace and tool from the stack config",
			stack:       "stacks/app",
			stackConfig: "workspace: blue\ntool: tofu\n",
			tofu:        true,
			wantKey:     "stacks/app:blue",
			wantTool:    v1.ToolTofu,
			wantCalls:   [][]string{{"workspace", "select", "-or-create", "blue"}},
		},
		{
			name:     "workspace from the key",
			stack:    "stacks/app:green",
			wantKey:  "stacks/app:green",
			wantTool: v1.ToolTerraform,
			wantCalls: [][]string{
				{"workspace", "select", "-or-create", "green"},
			},
		},
		{
			name:     "tool override and backend config",
			stack:    "stacks/app",
			env:      map[string]string{EnvTool: "TOFU", EnvBackendConfig: "bucket=state, key=app.tfstate,"},
			tofu:     true,
			wantKey:  "stacks/app",
			wantTool: v1.ToolTofu,
			wantCalls: [][]string{
				{"init", "-input=false", "-no-color", "-backend-config=bucket=state", "-backend-config=key=app.tfstate"},
			},
		},
		{
			name:        "binary does not match the configured tool",
			stack:       "stacks/app",
			rootConfig:  "version: 1\ntool: tofu\n",
			wantKey:     "stacks/app",
			wantTool:    v1.ToolTerraform,
			wantWarning: "is configured for tofu but",
		},
		{
			name:        "version does not match",
			stack:       "stacks/app",
			rootConfig:  "version: 1\ntool_version: \"1.9\"\n",
			wantKey:     "stacks/app",
			wantTool:    v1.ToolTerraform,
			wantWarning: "wants terraform 1.9 but",
		},
		{
			name:       "version override matches",
			stack:      "stacks/app",
			rootConfig: "version: 1\ntool_version: \"1.9\"\n",
			env:        map[string]string{EnvToolVersion: "1.14"},
			wantKey:    "stacks/app",
			wantTool:   v1.ToolTerraform,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			h.tf.Tofu = tt.tofu
			h.tf.Backend = json.RawMessage(`{"version":3,"backend":{"type":"s3","config":{"bucket":"state","key":"app.tfstate","region":"eu-west-1","use_lockfile":true,"workspace_key_prefix":"env"}}}`)
			if tt.stackConfig != "" {
				writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), tt.stackConfig)
			}
			if tt.rootConfig != "" {
				writeFile(t, filepath.Join(h.root, "stackorder.yaml"), tt.rootConfig)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			r := h.run("plan", "--stack", tt.stack, "--run-id", "run-1")
			require.Equal(t, 0, r.code, r.stderr)
			posted := fs.lastResult()
			assert.Equal(t, tt.wantKey, posted.Key)
			assert.Equal(t, tt.wantTool, posted.Result.Tool)
			assert.Equal(t, v1.PlanArtifactName(tt.wantKey, headSHA), posted.Result.Artifact)
			assert.Equal(t, &v1.Backend{Type: "s3", Bucket: "state", Key: "app.tfstate", Region: "eu-west-1", UseLockfile: true, WorkspaceKeyPrefix: "env"}, posted.Result.Backend)
			calls := h.tfCalls()
			for _, want := range tt.wantCalls {
				assert.Contains(t, calls, want)
			}
			if tt.wantWarning != "" {
				assert.Contains(t, r.stderr, "::warning::")
				assert.Contains(t, r.stderr, tt.wantWarning)
			} else {
				assert.NotContains(t, r.stderr, "::warning::")
			}
		})
	}
}

func TestPlanStackErrors(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		setup func(t *testing.T, h *harness)
		want  string
	}{
		{name: "missing --stack", args: []string{"plan"}, want: `required flag(s) "stack" not set`},
		{name: "unknown stack", args: []string{"plan", "--stack", "stacks/nope"}, want: "stacks/nope"},
		{name: "outside the repository", args: []string{"plan", "--stack", "../elsewhere"}, want: "not a repository relative directory"},
		{name: "stack is a file", args: []string{"plan", "--stack", "stacks/app/main.tf"}, want: "is not a directory"},
		{
			name: "invalid root config",
			args: []string{"plan", "--stack", "stacks/app"},
			setup: func(t *testing.T, h *harness) {
				writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "version: 2\n")
			},
			want: "unsupported value 2",
		},
		{
			name: "invalid stack config",
			args: []string{"plan", "--stack", "stacks/app"},
			setup: func(t *testing.T, h *harness) {
				writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), "tool: pulumi\n")
			},
			want: "is not one of terraform, tofu",
		},
		{name: "dot format", args: []string{"--format", "dot", "plan", "--stack", "stacks/app"}, want: "--format dot is only supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if tt.setup != nil {
				tt.setup(t, h)
			}
			r := h.run(tt.args...)
			assert.Equal(t, ExitFailure, r.code)
			assert.Contains(t, r.stderr, tt.want)
			assert.Empty(t, h.tfCalls())
		})
	}
}

func TestPlanOutsideActions(t *testing.T) {
	t.Run("local mode", func(t *testing.T) {
		h := newHarness(t)
		sha := initGit(t, h.root)
		r := h.run("plan", "--stack", "stacks/app")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Contains(t, r.stdout, "stacks/app: 1 to add")
		assert.NotContains(t, r.stdout, "::add-mask::")
		assert.NotContains(t, r.stderr, "::warning::")
		assert.FileExists(t, filepath.Join(h.root, ".stackorder", "plans", v1.PlanArtifactName("stacks/app", sha)+".tfplan"))
		assert.NoFileExists(t, h.output)
	})
	t.Run("json output keeps stdout clean", func(t *testing.T) {
		h := newHarness(t)
		r := h.run("--format", "json", "plan", "--stack", "stacks/app")
		require.Equal(t, 0, r.code, r.stderr)
		var out stackOutput
		require.NoError(t, json.Unmarshal([]byte(r.stdout), &out), r.stdout)
		assert.Equal(t, "stacks/app", out.Stack)
		assert.True(t, out.Unconfirmed)
		assert.Equal(t, v1.PlanArtifactName("stacks/app", "local"), out.Result.Artifact)
		assert.Equal(t, filepath.Join(h.root, ".stackorder", "plans", out.Result.Artifact+".tfplan"), out.PlanFile)
		assert.Contains(t, r.stderr, "Terraform has been successfully initialized!")
	})
	t.Run("api key and run id report the result", func(t *testing.T) {
		h := newHarness(t)
		fs := newFakeServer(t)
		t.Setenv(EnvAPIKey, "sk_test_key")
		t.Setenv(EnvRunID, "run-9")
		t.Setenv(EnvPlanDir, filepath.Join(t.TempDir(), "plans"))
		out := filepath.Join(t.TempDir(), "custom.tfplan")
		r := h.run("--server", fs.url(), "plan", "--stack", "stacks/app", "--out", out)
		require.Equal(t, 0, r.code, r.stderr)
		posted := fs.lastResult()
		assert.Equal(t, "run-9", posted.RunID)
		assert.Empty(t, posted.Result.JobURL)
		assert.Equal(t, []string{"Bearer sk_test_key"}, fs.auth["result"])
		assert.FileExists(t, out)
		assert.FileExists(t, strings.TrimSuffix(out, ".tfplan")+".json")
	})
}

func TestPlanMasksSecrets(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "pull_request", prPayload())
	t.Setenv("TF_VAR_db_password", "correct-horse-battery")
	t.Setenv("TF_VAR_enable_secret_rotation", "true")
	h.tf.PlanOutput = "  + tags = { owner = \"platform\" }\n  + token = \"abcd1234efgh\"\n  + note = \"correct-horse-battery\"\n"
	h.tf.ShowText = writeFile(t, filepath.Join(t.TempDir(), "show.txt"), "value = \"correct-horse-battery\"\n")

	r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
	require.Equal(t, 0, r.code, r.stderr)

	lines := strings.Split(r.stdout, "\n")
	firstOther := -1
	for i, l := range lines {
		if !strings.HasPrefix(l, "::add-mask::") {
			firstOther = i
			break
		}
	}
	require.Positive(t, firstOther)
	assert.Contains(t, lines[:firstOther], "::add-mask::correct-horse-battery")
	assert.Contains(t, lines[:firstOther], "::add-mask::"+os.Getenv("GITHUB_TOKEN"))
	assert.NotContains(t, lines[:firstOther], "::add-mask::true")
	maskAt := strings.Index(r.stdout, "::add-mask::abcd1234efgh")
	lineAt := strings.Index(r.stdout, "  + token = \"***\"")
	require.GreaterOrEqual(t, maskAt, 0)
	assert.Less(t, maskAt, lineAt)
	assert.Contains(t, r.stdout, "  + note = \"***\"")
	assert.NotContains(t, strings.ReplaceAll(r.stdout, "::add-mask::correct-horse-battery", ""), "correct-horse-battery")
	assert.Contains(t, r.stdout, "  + tags = { owner = \"platform\" }")
	assert.Equal(t, "value = \"***\"\n", fs.lastResult().Result.PlanText)
}

func TestVerboseLogging(t *testing.T) {
	h := newHarness(t)
	r := h.run("--verbose", "--format", "json", "plan", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stderr, "level=DEBUG msg=\"detected tool\"")
	assert.NotContains(t, r.stderr, "time=")
}

func TestJSONLogFormat(t *testing.T) {
	h := newHarness(t)
	t.Setenv(EnvLogFormat, "json")
	r := h.run("--verbose", "--format", "json", "plan", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stderr, `"msg":"detected tool"`)
}

func TestInterruptedCommand(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	err := Run(ctx, []string{"--repo-root", h.root, "plan", "--stack", "stacks/app"}, &stdout, &stderr)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, ExitFailure, ExitCode(err))
	assert.Contains(t, stderr.String(), "stackorder: interrupted: ")
}
