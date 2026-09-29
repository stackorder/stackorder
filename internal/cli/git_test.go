package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBaseSHATakesAFullSHAWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	a := &app{root: t.TempDir()}
	for _, sha := range []string{strings.Repeat("ab", 20), strings.Repeat("c", 64)} {
		assert.Equal(t, sha, a.baseSHA(t.Context(), sha))
	}
	assert.Empty(t, a.baseSHA(t.Context(), "main"), "a branch name needs git, which is not on PATH here")
	assert.Empty(t, a.baseSHA(t.Context(), strings.Repeat("A", 40)), "only lower-case hex is taken as is")
}
