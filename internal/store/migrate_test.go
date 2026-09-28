//go:build integration

package store_test

import (
	"context"
	"sync"
	"testing"

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
		{"up", s.Migrate, 21, 4},
		{"up again is a no-op", s.Migrate, 21, 4},
		{"down", s.MigrateDown, 0, 0},
		{"down again is a no-op", s.MigrateDown, 0, 0},
		{"up after down", s.Migrate, 21, 4},
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
	assert.Equal(t, 21, tableCount(t, stores[0]))
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
