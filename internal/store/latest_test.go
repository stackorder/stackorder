package store

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/migrations"
)

func TestLatestMigration(t *testing.T) {
	got, err := LatestMigration()
	require.NoError(t, err)

	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	var (
		want  uint
		pairs int
	)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		pairs++
		n, err := strconv.ParseUint(name[:strings.IndexByte(name, '_')], 10, 64)
		require.NoError(t, err)
		if uint(n) > want {
			want = uint(n)
		}
		down := strings.TrimSuffix(name, ".up.sql") + ".down.sql"
		_, err = fs.Stat(migrations.FS, down)
		assert.NoErrorf(t, err, "every up migration needs a down: %s", down)
	}
	require.Positive(t, pairs, "expected embedded up migrations")
	assert.Equal(t, want, got)
}
