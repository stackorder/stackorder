//go:build e2e

package e2e

import (
	"testing"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

func TestEndToEnd(t *testing.T) {
	requireDocker(t)
	ls := sharedLocalStack(t)
	cliBin := stackorderBinary(t)
	src := exampleSource(t)
	for _, tool := range []v1.Tool{v1.ToolTerraform, v1.ToolTofu} {
		t.Run(string(tool), func(t *testing.T) {
			if _, err := tf.Detect(tool); err != nil {
				t.Skipf("%s is not installed: %v", tool, err)
			}
			newStory(t, tool, ls, cliBin, src).run(t)
		})
	}
}
