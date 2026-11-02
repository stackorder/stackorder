package tf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const realConfig = `terraform {
  backend "local" {}
}

resource "terraform_data" "example" {
  input = "hello"
}

resource "terraform_data" "keyed" {
  for_each = toset(["y.z/w"])
  input    = "hunter2hunter2"
}

data "terraform_remote_state" "self" {
  backend = "local"
  config = {
    path = "${path.module}/remote.tfstate"
  }
  defaults = {
    token = "hunter2hunter2"
  }
}

output "value" {
  value = terraform_data.example.output
}
`

const emptyRemoteState = `{"version":4,"terraform_version":"1.10.0","serial":1,"lineage":"acme","outputs":{},"resources":[]}`

func TestRealBinaries(t *testing.T) {
	for _, tool := range []v1.Tool{v1.ToolTerraform, v1.ToolTofu} {
		t.Run(string(tool), func(t *testing.T) {
			t.Setenv(EnvTerraformBin, "")
			t.Setenv(EnvTofuBin, "")
			bin, err := Detect(tool)
			if errors.Is(err, ErrToolNotFound) {
				t.Skipf("%s is not on PATH", tool)
			}
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			version, isTofu, err := Version(ctx, bin)
			require.NoError(t, err)
			assert.NotEmpty(t, version)
			assert.Equal(t, tool == v1.ToolTofu, isTofu)

			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "main.tf"), []byte(realConfig), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "remote.tfstate"), []byte(emptyRemoteState), 0o600))
			var log bytes.Buffer
			r := &Runner{Bin: bin, Dir: dir, Stdout: &log, Stderr: &log, Workspace: "blue"}

			statePath := filepath.Join(t.TempDir(), "default.tfstate")
			require.NoError(t, r.Init(ctx, []string{"path=" + statePath}, InitOptions{PluginCacheDir: filepath.Join(t.TempDir(), "cache")}), log.String())
			require.NoError(t, r.SelectWorkspace(ctx), log.String())
			require.NoError(t, r.SelectWorkspace(ctx), "selecting an existing workspace must succeed")

			plan, err := r.Plan(ctx, PlanOptions{Out: "plan.tfplan", DetailedExitCode: true})
			require.NoError(t, err, log.String())
			assert.Equal(t, 2, plan.ExitCode)
			assert.True(t, plan.HasChanges)
			assert.Contains(t, plan.Output, "terraform_data.example")

			p, err := r.ShowJSON(ctx, "plan.tfplan")
			require.NoError(t, err)
			s := Summarize(p)
			added := []string{"terraform_data.example", `terraform_data.keyed["y.z/w"]`}
			assert.Equal(t, v1.PlanSummary{Adds: 2, Added: added, OutputChanges: 1}, s)
			assert.Equal(t, added, AddressSet(p))

			text, err := r.ShowText(ctx, "plan.tfplan")
			require.NoError(t, err)
			assert.Contains(t, text, "terraform_data.example will be created")

			applied, err := r.Apply(ctx, "plan.tfplan", ApplyOptions{LockTimeout: 10 * time.Second})
			require.NoError(t, err, log.String())
			assert.Equal(t, 0, applied.ExitCode)
			assert.Contains(t, applied.Output, "Apply complete!")

			logged := log.Len()
			raw, err := r.ShowStateJSONRaw(ctx)
			require.NoError(t, err, log.String())
			assert.Equal(t, logged, log.Len(), "the state must not be streamed to the log")
			assert.Contains(t, string(raw), "hunter2hunter2", "the state must hold the value the list leaves out")
			state, err := ParseStateJSON(raw)
			require.NoError(t, err)
			require.NotNil(t, state.Values)
			resources := Resources(state.Values.RootModule)
			builtin := "terraform.io/builtin/terraform"
			assert.Equal(t, []v1.StackResource{
				{Address: "terraform_data.example", Type: "terraform_data", Provider: builtin},
				{Address: `terraform_data.keyed["y.z/w"]`, Type: "terraform_data", Provider: builtin},
			}, resources)
			encoded, err := json.Marshal(resources)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "hunter2hunter2")
			assert.NotContains(t, string(encoded), "hello")

			outputs, err := r.Output(ctx)
			require.NoError(t, err)
			assert.Equal(t, "hello", outputs["value"].Value)

			again, err := r.Plan(ctx, PlanOptions{DetailedExitCode: true, Lock: Bool(false)})
			require.NoError(t, err, log.String())
			assert.Equal(t, 0, again.ExitCode)
			assert.False(t, again.HasChanges)
		})
	}
}
