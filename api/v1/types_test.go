package v1_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestPlanArtifactNameIsDistinctPerStack(t *testing.T) {
	const sha = "4f1c0de2b9a8e7d6c5b4a3928170f6e5d4c3b2a1"
	keys := []string{"stacks/a-b", "stacks/a/b", "stacks/a:b", "stacks-a/b", "stacks/a-b:c", "stacks/a/b:c", "stacks/a/b/c"}
	seen := map[string]string{}
	for _, key := range keys {
		name := v1.PlanArtifactName(key, sha)
		if other, dup := seen[name]; dup {
			t.Errorf("%s and %s share the artifact name %s", other, key, name)
		}
		seen[name] = key
		assert.True(t, strings.HasPrefix(name, "stackorder-plan-"), name)
		assert.True(t, strings.HasSuffix(name, "-"+sha), name)
		assert.NotContains(t, name, "/")
		assert.NotContains(t, name, ":")
		assert.Equal(t, name, v1.PlanArtifactName(key, sha), "stable")
	}
}
