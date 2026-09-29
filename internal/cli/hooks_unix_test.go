//go:build unix

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

const recordingHook = `#!/usr/bin/env bash
set -eu
{
  echo "hook=$(basename "$0" .sh)"
  echo "stack=$STACKORDER_STACK"
  echo "run=$STACKORDER_RUN_ID"
  echo "plan_file=$STACKORDER_PLAN_FILE"
  echo "plan_json=$STACKORDER_PLAN_JSON"
  if [ -n "$STACKORDER_PLAN_JSON" ] && [ -f "$STACKORDER_PLAN_JSON" ]; then echo "json_exists=yes"; fi
  echo "cwd=$PWD"
  echo "--"
} >> "$HOOK_LOG"
echo "hook $(basename "$0" .sh) ran"
exit "${HOOK_EXIT:-0}"
`

func installHooks(t *testing.T, root string, mode os.FileMode, names ...string) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "hooks.log")
	t.Setenv("HOOK_LOG", log)
	for _, n := range names {
		p := filepath.Join(root, ".stackorder", "hooks", n+".sh")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(recordingHook), mode))
	}
	return log
}

func hookRecords(t *testing.T, log string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []map[string]string
	cur := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "--" {
			out = append(out, cur)
			cur = map[string]string{}
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		cur[k] = v
	}
	return out
}

func sameDir(t *testing.T, want, got string) {
	t.Helper()
	w, err := filepath.EvalSymlinks(want)
	require.NoError(t, err)
	g, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	assert.Equal(t, w, g)
}

func TestPlanRunsHooks(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "pull_request", prPayload())
	log := installHooks(t, h.root, 0o755, "pre-plan", "post-plan")

	r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "hook pre-plan ran")
	assert.Contains(t, r.stdout, "hook post-plan ran")

	planFile := filepath.Join(h.root, ".stackorder", "plans", v1.PlanArtifactName("stacks/app", headSHA)+".tfplan")
	recs := hookRecords(t, log)
	require.Len(t, recs, 2)
	assert.Equal(t, "pre-plan", recs[0]["hook"])
	assert.Equal(t, "stacks/app", recs[0]["stack"])
	assert.Equal(t, "run-1", recs[0]["run"])
	assert.Empty(t, recs[0]["plan_file"])
	assert.Empty(t, recs[0]["plan_json"])
	sameDir(t, h.root, recs[0]["cwd"])
	assert.Equal(t, "post-plan", recs[1]["hook"])
	assert.Equal(t, planFile, recs[1]["plan_file"])
	assert.Equal(t, strings.TrimSuffix(planFile, ".tfplan")+".json", recs[1]["plan_json"])
	assert.Equal(t, "yes", recs[1]["json_exists"])
}

func TestApplyRunsHooks(t *testing.T) {
	h, fs := newApplyHarness(t)
	planFile := h.savedPlan(h.sha)
	log := installHooks(t, h.root, 0o755, "pre-apply", "post-apply")

	r := h.run("apply", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)
	recs := hookRecords(t, log)
	require.Len(t, recs, 2)
	for i, name := range []string{"pre-apply", "post-apply"} {
		assert.Equal(t, name, recs[i]["hook"])
		assert.Equal(t, "stacks/app", recs[i]["stack"])
		assert.Equal(t, "run-1", recs[i]["run"])
		assert.Equal(t, planFile, recs[i]["plan_file"])
		assert.Equal(t, "yes", recs[i]["json_exists"])
	}
	assert.Equal(t, v1.ResultSuccess, fs.lastResult().Result.Status)
}

func TestHookFailures(t *testing.T) {
	t.Run("pre-plan fails the plan", func(t *testing.T) {
		h := newHarness(t)
		fs := newFakeServer(t)
		h.ci(fs, "pull_request", prPayload())
		installHooks(t, h.root, 0o755, "pre-plan")
		t.Setenv("HOOK_EXIT", "4")
		r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		got := fs.lastResult().Result
		assert.Equal(t, v1.ResultFailure, got.Status)
		assert.Equal(t, 4, got.ExitCode)
		assert.Contains(t, got.ErrorText, "pre-plan hook: hook pre-plan exited with code 4")
		assert.Equal(t, []string{"version"}, h.tfCommands())
	})
	t.Run("pre-apply stops the apply", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		h.savedPlan(h.sha)
		installHooks(t, h.root, 0o755, "pre-apply")
		t.Setenv("HOOK_EXIT", "1")
		r := h.run("apply", "--stack", "stacks/app")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		assert.NotContains(t, h.tfCommands(), "apply")
		assert.Equal(t, v1.ResultFailure, fs.lastResult().Result.Status)
	})
	t.Run("post-apply failure after a successful apply", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		h.savedPlan(h.sha)
		installHooks(t, h.root, 0o755, "post-apply")
		t.Setenv("HOOK_EXIT", "2")
		r := h.run("apply", "--stack", "stacks/app")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		assert.Contains(t, r.stderr, "applied stacks/app, but post-apply hook")
		got := fs.lastResult().Result
		assert.Equal(t, v1.ResultSuccess, got.Status)
		assert.Contains(t, got.ErrorText, "post-apply hook")
	})
	t.Run("hook without the executable bit", func(t *testing.T) {
		h := newHarness(t)
		installHooks(t, h.root, 0o644, "pre-plan")
		r := h.run("plan", "--stack", "stacks/app")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		assert.Contains(t, r.stderr, "hook is not executable")
	})
}

const envHook = `#!/usr/bin/env bash
set -eu
{
  echo "hook=$(basename "$0" .sh)"
  env | grep -E '^(TF_VAR_|STACKORDER_)' | grep -v '^STACKORDER_TEST_FAKE_TF=' | sort
  echo "--"
} >> "$HOOK_LOG"
`

func TestHookEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		command string
		hooks   []string
		want    map[string]string
		absent  []string
	}{
		{
			name: "plan", command: "plan", hooks: []string{"pre-plan", "post-plan"},
			want: map[string]string{"TF_VAR_role": "reader", "TF_VAR_scan": "planning", "TF_VAR_only_apply": "outer"},
		},
		{
			name: "apply", command: "apply", hooks: []string{"pre-apply", "post-apply"},
			want:   map[string]string{"TF_VAR_role": "deployer", "TF_VAR_only_apply": "from-config", "STACKORDER_RUN_ID": "run-1"},
			absent: []string{"TF_VAR_scan"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h *harness
			if tt.command == "apply" {
				h, _ = instanceApplyHarness(t, true)
			} else {
				h = newHarness(t)
				writeInstanceStack(t, h.root)
			}
			log := filepath.Join(t.TempDir(), "hooks.log")
			t.Setenv("HOOK_LOG", log)
			for _, n := range tt.hooks {
				writeFile(t, filepath.Join(h.root, ".stackorder", "hooks", n+".sh"), envHook)
				require.NoError(t, os.Chmod(filepath.Join(h.root, ".stackorder", "hooks", n+".sh"), 0o755)) //nolint:gosec
			}
			t.Setenv("TF_VAR_only_apply", "outer")
			r := h.run(tt.command, "--stack", "stacks/app:prod")
			require.Equal(t, 0, r.code, r.stderr)
			recs := hookRecords(t, log)
			require.Len(t, recs, len(tt.hooks))
			for i, rec := range recs {
				assert.Equal(t, tt.hooks[i], rec["hook"])
				assert.Equal(t, "stacks/app:prod", rec["STACKORDER_STACK"])
				assert.Equal(t, "stacks/app", rec["STACKORDER_STACK_PATH"])
				assert.Equal(t, "prod", rec["STACKORDER_INSTANCE"])
				assert.Equal(t, "prod", rec["TF_VAR_environment"])
				for k, v := range tt.want {
					assert.Equal(t, v, rec[k], "%s: %s", rec["hook"], k)
				}
				for _, k := range tt.absent {
					assert.NotContains(t, rec, k)
				}
			}
			assert.NotEmpty(t, recs[len(recs)-1]["STACKORDER_PLAN_FILE"])
		})
	}
}

func TestPrePlanHookCanWriteAVarFile(t *testing.T) {
	h := newHarness(t)
	writeInstanceStack(t, h.root)
	generated := filepath.Join(h.root, "stacks", "app", "prod-extra.tfvars")
	require.NoError(t, os.Remove(generated))
	hook := writeFile(t, filepath.Join(h.root, ".stackorder", "hooks", "pre-plan.sh"), "#!/bin/sh\nprintf '{}' > '"+generated+"'\n")
	require.NoError(t, os.Chmod(hook, 0o755)) //nolint:gosec
	r := h.run("plan", "--stack", "stacks/app:prod")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, prodVarFiles, varFileArgs(h.tfCall("plan").args))
}
