//go:build integration

package store_test

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
	"github.com/stackorder/stackorder/migrations"
)

func newMigrator(t *testing.T, s *store.Store, src fs.FS) *migrate.Migrate {
	t.Helper()
	source, err := iofs.New(src, ".")
	require.NoError(t, err)
	drv, err := migratepgx.WithInstance(stdlib.OpenDBFromPool(s.Pool()), &migratepgx.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", drv)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	return m
}

func migrationsThrough(t *testing.T, lastVersion int) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	out := fstest.MapFS{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		ver, err := strconv.Atoi(name[:4])
		require.NoError(t, err)
		if ver > lastVersion {
			continue
		}
		b, err := fs.ReadFile(migrations.FS, name)
		require.NoError(t, err)
		out[name] = &fstest.MapFile{Data: b}
	}
	return out
}

func TestMigrate0015OnPopulatedQueue(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	require.NoError(t, newMigrator(t, s, migrations.FS).Migrate(9), "reach the v0.5.0 schema")

	_, err = s.Pool().Exec(ctx,
		`INSERT INTO events (id, kind, payload) VALUES ('delivery-1', 'push', '{}'::jsonb)`)
	require.NoError(t, err)
	var jobID string
	require.NoError(t, s.Pool().QueryRow(ctx,
		`INSERT INTO jobs (kind, payload) VALUES ('reconcile', '{}'::jsonb) RETURNING id`).Scan(&jobID))

	require.NoError(t, s.Migrate(ctx), "the server startup migration runs 0015 on the populated tables")

	ver, dirty, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(16), ver)
	assert.False(t, dirty)

	ev, err := s.GetEvent(ctx, "delivery-1")
	require.NoError(t, err)
	assert.Nil(t, ev.TraceContext, "a row queued before the upgrade has no trace context")

	jobs, err := s.ClaimJobs(ctx, "w", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, jobID, jobs[0].ID.String())
	assert.Nil(t, jobs[0].TraceContext)

	tc := []byte(`{"traceparent":"00-00000000000000000000000000000001-0000000000000002-01"}`)
	inserted, err := s.InsertEvent(ctx, "delivery-2", "push", []byte(`{}`), tc)
	require.NoError(t, err)
	require.True(t, inserted)
	ev2, err := s.GetEvent(ctx, "delivery-2")
	require.NoError(t, err)
	assert.JSONEq(t, string(tc), string(ev2.TraceContext))
}

func TestOldServerCannotStartAfter0015(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	require.NoError(t, s.Migrate(ctx), "migrate to the current schema, 0015 included")

	err = newMigrator(t, s, migrationsThrough(t, 9)).Up()
	require.Error(t, err, "a server whose migration source ends at 0009 cannot start once a newer schema is applied")
	assert.NotErrorIs(t, err, migrate.ErrNoChange)
	assert.Contains(t, err.Error(), "16")
}
