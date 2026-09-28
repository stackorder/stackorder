package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestDrift(t *testing.T) {
	tests := []struct {
		name        string
		planExit    int
		showJSON    string
		wantCode    int
		wantDrifted string
		wantStatus  v1.ResultStatus
		wantLine    string
	}{
		{name: "drifted", planExit: 2, showJSON: "plan_changes.json", wantCode: ExitChanges, wantDrifted: "true", wantStatus: v1.ResultSuccess, wantLine: "stacks/app: drifted: 1 to add"},
		{name: "clean", planExit: 0, showJSON: "plan_noop.json", wantCode: 0, wantDrifted: "false", wantStatus: v1.ResultSuccess, wantLine: "stacks/app: no drift"},
		{name: "plan fails", planExit: 1, showJSON: "plan_noop.json", wantCode: ExitFailure, wantDrifted: "false", wantStatus: v1.ResultFailure, wantLine: "stacks/app: drift check failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "workflow_dispatch", dispatchPayload("run-drift", headSHA))
			h.tf.PlanExit = tt.planExit
			h.tf.ShowJSON = filepath.Join(filepath.Dir(h.tf.ShowJSON), tt.showJSON)

			r := h.run("drift", "--stack", "stacks/app")
			require.Equal(t, tt.wantCode, r.code, r.stderr)
			assert.Contains(t, r.stdout, tt.wantLine)

			posted := fs.lastResult()
			assert.Equal(t, "run-drift", posted.RunID)
			assert.Equal(t, v1.ModeDrift, posted.Result.Mode)
			assert.Equal(t, tt.wantStatus, posted.Result.Status)
			assert.Equal(t, tt.planExit, posted.Result.ExitCode)
			assert.Equal(t, tt.wantDrifted == "true", posted.Result.HasChanges)
			outs := h.outputs()
			assert.Equal(t, tt.wantDrifted, outs["drifted"])
			if tt.wantStatus == v1.ResultSuccess {
				var s v1.PlanSummary
				require.NoError(t, json.Unmarshal([]byte(outs["summary"]), &s))
				assert.Equal(t, *posted.Result.Summary, s)
			} else {
				assert.Equal(t, "null", outs["summary"])
			}

			for _, args := range h.tfCalls() {
				if args[0] != "plan" {
					continue
				}
				assert.Contains(t, args, "-detailed-exitcode")
				out := ""
				for _, a := range args {
					if v, ok := strings.CutPrefix(a, "-out="); ok {
						out = v
					}
				}
				require.NotEmpty(t, out)
				assert.False(t, strings.HasPrefix(out, h.root), out)
				assert.NoFileExists(t, out)
			}
		})
	}
}

func TestDriftDegrades(t *testing.T) {
	t.Run("unreachable server keeps the drift exit code", func(t *testing.T) {
		h := newHarness(t)
		fs := newFakeServer(t)
		h.ci(fs, "workflow_dispatch", dispatchPayload("run-drift", headSHA))
		t.Setenv(EnvServerURL, deadURL(t))
		r := h.run("drift", "--stack", "stacks/app")
		assert.Equal(t, ExitChanges, r.code, r.stderr)
		assert.Contains(t, r.stderr, "::warning::the drift check of stacks/app is unconfirmed")
		assert.Equal(t, "true", h.outputs()["drifted"])
	})
	t.Run("refused result", func(t *testing.T) {
		h := newHarness(t)
		fs := newFakeServer(t)
		h.ci(fs, "workflow_dispatch", dispatchPayload("run-drift", headSHA))
		fs.failWith("result", 404, "not_found")
		r := h.run("drift", "--stack", "stacks/app")
		assert.Equal(t, ExitFailure, r.code, r.stderr)
		assert.Contains(t, r.stderr, "injected not_found")
	})
	t.Run("local run with a server but no api key keeps the drift exit code", func(t *testing.T) {
		h := newHarness(t)
		fs := newFakeServer(t)
		r := h.run("--server", fs.url(), "drift", "--stack", "stacks/app", "--run-id", "run-drift")
		assert.Equal(t, ExitChanges, r.code, r.stderr)
		assert.Zero(t, fs.hitCount("result"))
		assert.Contains(t, r.stdout, "stacks/app: drifted: 1 to add")
	})
	t.Run("local json output", func(t *testing.T) {
		h := newHarness(t)
		h.tf.PlanExit = 0
		h.tf.ShowJSON = filepath.Join(filepath.Dir(h.tf.ShowJSON), "plan_noop.json")
		r := h.run("--format", "json", "drift", "--stack", "stacks/app")
		require.Equal(t, 0, r.code, r.stderr)
		var out stackOutput
		require.NoError(t, json.Unmarshal([]byte(r.stdout), &out))
		assert.Equal(t, v1.ModeDrift, out.Result.Mode)
		assert.False(t, out.Result.HasChanges)
	})
}
