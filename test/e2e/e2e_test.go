//go:build e2e

package e2e

import (
	"testing"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

const (
	canaryVariable = "E2E_CANARY_API_KEY"
	canaryValue    = "e2e-canary-from-the-developer-shell"
)

func TestEndToEnd(t *testing.T) {
	t.Setenv(canaryVariable, canaryValue)
	if cfg, ok := liveConfig(t); ok {
		t.Skipf("the live GitHub variant is configured for %s, so TestLiveGitHub runs instead of the fake GitHub", cfg.Org)
	}
	tools := []v1.Tool{v1.ToolTerraform, v1.ToolTofu}
	missing := map[v1.Tool]error{}
	for _, tool := range tools {
		if _, err := tf.Detect(tool); err != nil {
			missing[tool] = err
		}
	}
	if len(missing) == len(tools) {
		t.Skipf("neither terraform nor tofu is installed: %v", missing)
	}
	requireDocker(t)
	ls := sharedLocalStack(t)
	cliBin := stackorderBinary(t)
	src := exampleSource(t)
	for _, tool := range tools {
		t.Run(string(tool), func(t *testing.T) {
			if err := missing[tool]; err != nil {
				t.Skipf("%s is not installed: %v", tool, err)
			}
			newStory(t, tool, ls, cliBin, src).run(t)
		})
	}
}
