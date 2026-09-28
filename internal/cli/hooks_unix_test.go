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
	planFile := h.savedPlan(headSHA)
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
		h.savedPlan(headSHA)
		installHooks(t, h.root, 0o755, "pre-apply")
		t.Setenv("HOOK_EXIT", "1")
		r := h.run("apply", "--stack", "stacks/app")
		require.Equal(t, ExitFailure, r.code, r.stderr)
		assert.NotContains(t, h.tfCommands(), "apply")
		assert.Equal(t, v1.ResultFailure, fs.lastResult().Result.Status)
	})
	t.Run("post-apply failure after a successful apply", func(t *testing.T) {
		h, fs := newApplyHarness(t)
		h.savedPlan(headSHA)
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
