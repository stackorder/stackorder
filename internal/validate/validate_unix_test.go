//go:build unix

package validate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestSymlinkedRootValidatesAllFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackorder.yaml", "version: 1\n")
	write(t, dir, "stacks/app/main.tf", backend)
	write(t, dir, "modules/vpc/.stackorder.yaml", "bogus: 1\n")

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))

	direct := run(t, dir)
	through := run(t, link)

	assert.False(t, through.Valid, "a symlinked root must validate every configuration file")
	assert.Equal(t, direct.Errors, through.Errors)
	assert.Equal(t, messages(direct, v1.SeverityError), messages(through, v1.SeverityError))
}
