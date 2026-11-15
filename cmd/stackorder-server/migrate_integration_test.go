//go:build integration

package main

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestMigrateCommand(t *testing.T) {
	target, err := store.LatestMigration()
	require.NoError(t, err)
	dsn := pgtest.DSN(t)
	env := envOf(map[string]string{"DATABASE_URL": dsn})

	t.Run("status on a fresh database is pending", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 2, run([]string{"migrate", "--status"}, env, &stdout, &stderr))
		assert.Equal(t, fmt.Sprintf("current=0\ntarget=%d\ndirty=false\n", target), stdout.String())
		assert.Empty(t, stderr.String())
	})

	t.Run("migrate applies up to the latest", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 0, run([]string{"migrate"}, env, &stdout, &stderr))
		assert.Equal(t, fmt.Sprintf("migrated database from version 0 to version %d\n", target), stdout.String())
		assert.Empty(t, stderr.String())
	})

	t.Run("migrate again is a no-op", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 0, run([]string{"migrate"}, env, &stdout, &stderr))
		assert.Equal(t, fmt.Sprintf("database already at version %d\n", target), stdout.String())
	})

	t.Run("status when up to date", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 0, run([]string{"migrate", "--status"}, env, &stdout, &stderr))
		assert.Equal(t, fmt.Sprintf("current=%d\ntarget=%d\ndirty=false\n", target, target), stdout.String())
	})
}

func TestMigrateStatusDoesNotMutate(t *testing.T) {
	dsn := pgtest.DSN(t)
	env := envOf(map[string]string{"DATABASE_URL": dsn})
	s, err := store.Open(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(s.Close)

	var stdout, stderr bytes.Buffer
	require.Equal(t, 2, run([]string{"migrate", "--status"}, env, &stdout, &stderr))

	var present bool
	require.NoError(t, s.Pool().QueryRow(t.Context(),
		`SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&present))
	assert.False(t, present, "migrate --status must not create schema_migrations on a fresh database")
}

func TestMigrateCommandToVersion(t *testing.T) {
	target, err := store.LatestMigration()
	require.NoError(t, err)
	require.GreaterOrEqual(t, target, uint(3), "test assumes at least three migrations")
	env := envOf(map[string]string{"DATABASE_URL": pgtest.DSN(t)})

	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{"migrate", "--to", "3"}, env, &stdout, &stderr))
	assert.Equal(t, "migrated database from version 0 to version 3\n", stdout.String())

	stdout.Reset()
	assert.Equal(t, 2, run([]string{"migrate", "--status"}, env, &stdout, &stderr))
	assert.Equal(t, fmt.Sprintf("current=3\ntarget=%d\ndirty=false\n", target), stdout.String())

	stderr.Reset()
	assert.Equal(t, 1, run([]string{"migrate", "--to", "2"}, env, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "above the requested version 2")

	stderr.Reset()
	assert.Equal(t, 1, run([]string{"migrate", "--to", fmt.Sprint(target + 1)}, env, &stdout, &stderr))
	assert.Contains(t, stderr.String(), fmt.Sprintf("no migration numbered %d; the latest is %d", target+1, target))

	stdout.Reset()
	assert.Equal(t, 0, run([]string{"migrate"}, env, &stdout, &stderr))
	assert.Equal(t, fmt.Sprintf("migrated database from version 3 to version %d\n", target), stdout.String())
}
