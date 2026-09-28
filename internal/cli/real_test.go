package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

const realStack = `terraform {
  backend "local" {}
}

variable "db_password" {
  type    = string
  default = "not-sensitive"
}

resource "terraform_data" "app" {
  input = "password = \"${var.db_password}\""
}

output "value" {
  value = terraform_data.app.output
}
`

func TestRealToolPlanApplyAndDrift(t *testing.T) {
	for _, tool := range []v1.Tool{v1.ToolTerraform, v1.ToolTofu} {
		t.Run(string(tool), func(t *testing.T) {
			h := newHarness(t)
			t.Setenv(tf.EnvTerraformBin, "")
			t.Setenv(tf.EnvTofuBin, "")
			if _, err := tf.Detect(tool); errors.Is(err, tf.ErrToolNotFound) {
				t.Skipf("%s is not on PATH", tool)
			}
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "version: 1\ntool: "+string(tool)+"\n")
			writeFile(t, filepath.Join(h.root, "stacks", "app", "main.tf"), realStack)
			t.Setenv("TF_VAR_db_password", "hunter2hunter2")

			r := h.run("plan", "--stack", "stacks/app", "--run-id", "run-1")
			require.Equal(t, 0, r.code, r.stdout+r.stderr)
			planned := fs.lastResult().Result
			assert.Equal(t, v1.ResultSuccess, planned.Status)
			assert.True(t, planned.HasChanges)
			assert.Equal(t, 2, planned.ExitCode)
			assert.Equal(t, tool, planned.Tool)
			assert.NotEmpty(t, planned.ToolVersion)
			assert.Equal(t, &v1.Backend{Type: "local"}, planned.Backend)
			require.NotNil(t, planned.Summary)
			assert.Equal(t, []string{"terraform_data.app"}, planned.Summary.Added)
			assert.Contains(t, planned.PlanText, "terraform_data.app will be created")
			assert.NotContains(t, planned.PlanText, "hunter2hunter2")
			assert.Contains(t, r.stdout, "::add-mask::hunter2hunter2")
			assert.NotContains(t, stripMaskCommands(r.stdout), "hunter2hunter2")
			planFile := h.outputs()["plan-file"]
			require.FileExists(t, planFile)
			var planJSON map[string]any
			data, err := os.ReadFile(planJSONPath(planFile))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &planJSON))
			assert.Contains(t, planJSON, "resource_changes")

			h.ci(fs, "workflow_dispatch", dispatchPayload("run-1", headSHA))
			fs.setRun(v1.Run{SHA: headSHA, Status: v1.RunApplying, Stacks: []v1.RunStack{{Key: "stacks/app", Status: v1.StackApplying, Summary: planned.Summary}}})
			r = h.run("apply", "--stack", "stacks/app")
			require.Equal(t, 0, r.code, r.stdout+r.stderr)
			applied := fs.lastResult().Result
			assert.Equal(t, v1.ModeApply, applied.Mode)
			assert.Equal(t, v1.ResultSuccess, applied.Status)
			assert.Contains(t, r.stdout, "Apply complete!")
			assert.FileExists(t, filepath.Join(h.root, "stacks", "app", "terraform.tfstate"))

			r = h.run("drift", "--stack", "stacks/app", "--run-id", "run-2")
			require.Equal(t, 0, r.code, r.stdout+r.stderr)
			drift := fs.lastResult()
			assert.Equal(t, "run-2", drift.RunID)
			assert.Equal(t, v1.ModeDrift, drift.Result.Mode)
			assert.False(t, drift.Result.HasChanges)
			assert.Equal(t, "false", h.outputs()["drifted"])
		})
	}
}

func stripMaskCommands(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		if !strings.HasPrefix(line, "::add-mask::") {
			b.WriteString(line)
		}
	}
	return b.String()
}
