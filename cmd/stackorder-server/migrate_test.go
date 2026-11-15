package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/store"
)

func TestMigrateUsageErrors(t *testing.T) {
	dsn := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:5432/db"}
	tests := []struct {
		name string
		args []string
		env  map[string]string
		code int
		err  string
	}{
		{"status with to", []string{"migrate", "--status", "--to", "3"}, dsn, 1, "--status and --to cannot be combined"},
		{"status with down", []string{"migrate", "--status", "--down"}, dsn, 1, "--status and --down cannot be combined"},
		{"down without to", []string{"migrate", "--down"}, dsn, 1, "--down requires --to N"},
		{"yes without down", []string{"migrate", "--yes", "--to", "3"}, dsn, 1, "--yes applies only to --down"},
		{"down to zero", []string{"migrate", "--down", "--to", "0"}, dsn, 1, "no migration numbered 0; the latest is"},
		{"status with force", []string{"migrate", "--status", "--force", "3"}, dsn, 1, "--status and --force cannot be combined"},
		{"to with force", []string{"migrate", "--to", "3", "--force", "2"}, dsn, 1, "--to and --force cannot be combined"},
		{"negative to", []string{"migrate", "--to", "-1"}, dsn, 1, "must not be negative"},
		{"negative force", []string{"migrate", "--force", "-1"}, dsn, 1, "--force must not be negative"},
		{"zero to", []string{"migrate", "--to", "0"}, dsn, 1, "no migration numbered 0; the latest is"},
		{"unexpected argument", []string{"migrate", "extra"}, dsn, 1, `unexpected argument "extra"`},
		{"unknown flag", []string{"migrate", "--nope"}, dsn, 1, "flag provided but not defined"},
		{"missing database url", []string{"migrate"}, nil, 1, "DATABASE_URL is required"},
		{"missing database url status", []string{"migrate", "--status"}, nil, 1, "DATABASE_URL is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			assert.Equal(t, tt.code, run(tt.args, envOf(tt.env), &stdout, &stderr))
			assert.Contains(t, stderr.String(), tt.err)
		})
	}
}

func TestMigrationLossCoversEveryMigration(t *testing.T) {
	ms, err := store.Migrations()
	require.NoError(t, err)
	fallback := migrationLoss(0)
	for _, m := range ms {
		got := migrationLoss(m.Version)
		assert.NotEqualf(t, fallback, got,
			"migration %04d_%s has no loss description in the migrate --down plan; add one to migrationLoss", m.Version, m.Name)
		assert.NotEmptyf(t, got, "loss description for migration %d is empty", m.Version)
	}
	assert.Contains(t, migrationLoss(16), "api-key",
		"the API-key-scopes revert must warn about the loss of key restrictions")
	assert.Contains(t, migrationLoss(16), "administrator",
		"the API-key-scopes revert must warn that keys become full administrators")
}

func TestMigrateHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{"migrate", "--help"}, envOf(nil), &stdout, &stderr))
	assert.Contains(t, stderr.String()+stdout.String(), "usage: stackorder-server migrate")
}

func TestMigrateListedInHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{"help"}, envOf(nil), &stdout, &stderr))
	assert.Contains(t, stdout.String(), "migrate")
}

func TestRollbackTableMatchesSchemaVersion(t *testing.T) {
	latest, err := store.LatestMigration()
	require.NoError(t, err)

	guide, err := os.ReadFile("../../docs/operations/upgrades-and-backups.md")
	require.NoError(t, err)
	doc := string(guide)

	release := regexp.MustCompile(`(?m)^\| This release \| (\d+) \|`).FindStringSubmatch(doc)
	require.NotNil(t, release, "rollback table has no `| This release | N |` row")
	assert.Equal(t, strconv.FormatUint(uint64(latest), 10), release[1],
		"rollback table says this release is schema %s but LatestMigration is %d", release[1], latest)

	heading := regexp.MustCompile(`Reverting down from version (\d+) drops`).FindStringSubmatch(doc)
	require.NotNil(t, heading, "rollback loss table has no `Reverting down from version N` heading")
	assert.Equal(t, strconv.FormatUint(uint64(latest), 10), heading[1],
		"loss-table heading says version %s but LatestMigration is %d", heading[1], latest)

	for v := uint(5); v < latest; v++ {
		assert.Regexp(t, regexp.MustCompile(fmt.Sprintf(`(?m)^\| %d \|`, v)), doc,
			"loss table has no `| %d |` row for reverting to version %d", v, v)
	}
}
