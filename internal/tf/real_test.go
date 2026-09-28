package tf

import (
	"bytes"
	"context"
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

output "value" {
  value = terraform_data.example.output
}
`

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
			assert.Equal(t, v1.PlanSummary{Adds: 1, Added: []string{"terraform_data.example"}, OutputChanges: 1}, s)
			assert.Equal(t, []string{"terraform_data.example"}, AddressSet(p))

			text, err := r.ShowText(ctx, "plan.tfplan")
			require.NoError(t, err)
			assert.Contains(t, text, "terraform_data.example will be created")

			applied, err := r.Apply(ctx, "plan.tfplan", ApplyOptions{LockTimeout: 10 * time.Second})
			require.NoError(t, err, log.String())
			assert.Equal(t, 0, applied.ExitCode)
			assert.Contains(t, applied.Output, "Apply complete!")

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
