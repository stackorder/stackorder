//go:build integration

package store_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func tableCount(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.Pool().QueryRow(t.Context(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'schema_migrations'`).Scan(&n))
	return n
}

func TestMigrateUpDownUp(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	steps := []struct {
		name       string
		apply      func(context.Context) error
		wantTables int
		wantVer    uint
	}{
		{"up", s.Migrate, 23, 14},
		{"up again is a no-op", s.Migrate, 23, 14},
		{"down", s.MigrateDown, 0, 0},
		{"down again is a no-op", s.MigrateDown, 0, 0},
		{"up after down", s.Migrate, 23, 14},
	}
	for _, step := range steps {
		require.NoError(t, step.apply(ctx), step.name)
		assert.Equal(t, step.wantTables, tableCount(t, s), step.name)
		ver, dirty, err := s.SchemaVersion(ctx)
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantVer, ver, step.name)
		assert.False(t, dirty, step.name)
	}
}

func TestDefaultGraphFKRestorableInOneTransaction(t *testing.T) {
	ctx := t.Context()
	s := pgtest.New(t)

	var deferrable, deferred bool
	require.NoError(t, s.Pool().QueryRow(ctx, `
		SELECT condeferrable, condeferred FROM pg_constraint
		WHERE conname = 'repos_default_graph_id_fkey'`).Scan(&deferrable, &deferred))
	assert.True(t, deferrable, "repos_default_graph_id_fkey is deferrable")
	assert.True(t, deferred, "repos_default_graph_id_fkey is initially deferred")

	const graphID = "11111111-1111-1111-1111-111111111111"
	tx, err := s.Pool().Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `INSERT INTO installations (id, account) VALUES (1, 'acme')`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		INSERT INTO repos (id, installation_id, full_name, default_graph_id)
		VALUES (100, 1, 'acme/web', $1)`, graphID)
	require.NoError(t, err, "a repo may reference a graph not yet loaded while the constraint is deferred")
	_, err = tx.Exec(ctx, `INSERT INTO graphs (id, repo_id, sha) VALUES ($1, 100, 'abc')`, graphID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx), "the cycle is satisfied at commit")
}

func TestMigrateUp(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	applied, err := s.MigrateUp(ctx, 3)
	require.NoError(t, err)
	assert.True(t, applied)
	ver, dirty, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(3), ver)
	assert.False(t, dirty)

	applied, err = s.MigrateUp(ctx, 3)
	require.NoError(t, err, "to the current version is a no-op")
	assert.False(t, applied, "a no-op applies nothing")

	_, err = s.MigrateUp(ctx, 2)
	require.ErrorContains(t, err, "above the requested version 2",
		"it refuses to migrate down instead of reverting")
	var ahead *store.SchemaAheadError
	require.ErrorAs(t, err, &ahead, "the refusal is a typed SchemaAheadError")
	assert.Equal(t, uint(3), ahead.Current)
	assert.Equal(t, uint(2), ahead.Target)
	ver, _, err = s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(3), ver, "a refused down leaves the schema where it was")

	applied, err = s.MigrateUp(ctx, 9)
	require.NoError(t, err)
	assert.True(t, applied)
	ver, dirty, err = s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(9), ver)
	assert.False(t, dirty)
}

func TestSchemaVersionDoesNotCreateTable(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	ver, dirty, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(0), ver)
	assert.False(t, dirty)

	var present bool
	require.NoError(t, s.Pool().QueryRow(ctx,
		`SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&present))
	assert.False(t, present, "reporting the version must not create schema_migrations on a fresh database")
}

func TestMigrateUpRefusesDirty(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	require.NoError(t, s.Migrate(ctx))
	_, err = s.Pool().Exec(ctx, `UPDATE schema_migrations SET dirty = true`)
	require.NoError(t, err)

	_, err = s.MigrateUp(ctx, 9)
	require.ErrorContains(t, err, "dirty")
}

func TestMigrateConcurrentServers(t *testing.T) {
	ctx := t.Context()
	dsn := pgtest.DSN(t)
	const servers = 4
	stores := make([]*store.Store, servers)
	for i := range stores {
		s, err := store.Open(ctx, dsn)
		require.NoError(t, err)
		t.Cleanup(s.Close)
		stores[i] = s
	}
	var wg sync.WaitGroup
	errs := make([]error, servers)
	for i, s := range stores {
		wg.Go(func() { errs[i] = s.Migrate(ctx) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 23, tableCount(t, stores[0]))
}

func TestMigrateWithSingleConnectionPool(t *testing.T) {
	dsn := pgtest.DSN(t)
	switch {
	case !strings.Contains(dsn, "://"):
		dsn += " pool_max_conns=1"
	case strings.Contains(dsn, "?"):
		dsn += "&pool_max_conns=1"
	default:
		dsn += "?pool_max_conns=1"
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s, err := store.Open(ctx, dsn)
	require.NoError(t, err)
	require.Equal(t, int32(1), s.Pool().Config().MaxConns)

	done := make(chan error, 1)
	go func() { done <- s.Migrate(ctx) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("migrate did not finish with a pool of one connection")
	}
	t.Cleanup(s.Close)
	ver, dirty, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(14), ver)
	assert.False(t, dirty)
}

func withDSNParam(dsn, param string) string {
	switch {
	case !strings.Contains(dsn, "://"):
		return dsn + " " + param
	case strings.Contains(dsn, "?"):
		return dsn + "&" + param
	}
	return dsn + "?" + param
}

func TestOpenSizesThePool(t *testing.T) {
	ctx := t.Context()
	dsn := pgtest.DSN(t)
	sized, err := store.Open(ctx, dsn, store.WithDefaultMaxConns(12))
	require.NoError(t, err)
	t.Cleanup(sized.Close)
	assert.Equal(t, int32(12), sized.Pool().Config().MaxConns)

	pinned, err := store.Open(ctx, withDSNParam(dsn, "pool_max_conns=3"), store.WithDefaultMaxConns(12))
	require.NoError(t, err)
	t.Cleanup(pinned.Close)
	assert.Equal(t, int32(3), pinned.Pool().Config().MaxConns, "the DSN's pool_max_conns wins over the default")
	require.NoError(t, pinned.Ping(ctx))
}

func TestPingAndInTx(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.s.Ping(f.ctx))

	cases := []struct {
		name     string
		fail     bool
		wantRepo bool
	}{
		{"commit", false, true},
		{"rollback", true, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := int64(900 + i)
			err := f.s.InTx(f.ctx, func(tx *store.Store) error {
				if _, err := tx.UpsertRepo(f.ctx, store.RepoParams{ID: id, InstallationID: 1, FullName: "acme/tx-" + tc.name}); err != nil {
					return err
				}
				return tx.InTx(f.ctx, func(inner *store.Store) error {
					if _, err := inner.GetRepo(f.ctx, id); err != nil {
						return err
					}
					if tc.fail {
						return assert.AnError
					}
					return nil
				})
			})
			if tc.fail {
				require.ErrorIs(t, err, assert.AnError)
			} else {
				require.NoError(t, err)
			}
			_, err = f.s.GetRepo(f.ctx, id)
			if tc.wantRepo {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, store.ErrNotFound)
			}
		})
	}
}
