//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
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
		{"up", s.Migrate, 23, 16},
		{"up again is a no-op", s.Migrate, 23, 16},
		{"down", s.MigrateDown, 0, 0},
		{"down again is a no-op", s.MigrateDown, 0, 0},
		{"up after down", s.Migrate, 23, 16},
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

func populateForDowngrade(f *fixture) {
	t := f.t
	ctx := f.ctx
	s := f.s

	g := &v1.Graph{
		Repo: f.repo.FullName, SHA: "sha-pop",
		Stacks: []v1.Stack{{Key: "a", Path: "a"}, {Key: "b", Path: "b", Environment: "prod"}},
	}
	graphID, ids := f.saveGraph(f.repo.ID, g)
	stackA, stackB := ids["a"], ids["b"]
	require.NoError(t, s.SetDefaultGraph(ctx, f.repo.ID, graphID))

	apply := f.run(store.CreateRunParams{Mode: v1.ModeApply, SHA: "sha-apply", PRNumber: 7})
	plan := f.run(store.CreateRunParams{Mode: v1.ModePlan, SHA: "sha-plan", PRNumber: 7})
	require.NoError(t, s.SetRunComment(ctx, plan.ID, 4242))

	require.NoError(t, s.UpsertRunStacks(ctx, apply.ID, []store.RunStack{
		{StackID: stackA, Status: v1.StackApplied, Via: []string{"a"}, PlanURL: "s3://plans/a", PlanOutput: "changes", BlockedBy: []string{"x"}},
		{StackID: stackB, Wave: 1, Status: v1.StackApplied},
	}))

	_, _, err := s.CreateDispatchChunk(ctx, apply.ID, 0, "prod", v1.ModeApply, 0)
	require.NoError(t, err)
	_, _, err = s.CreateDispatchChunk(ctx, apply.ID, 0, "prod", v1.ModeApply, 1)
	require.NoError(t, err, "a second chunk shares the pre-0005 base key; its down must delete it before re-adding the unique constraint")

	require.NoError(t, s.SetStackStateObject(ctx, stackA, v1.StateObject{Bucket: "state", Key: "a.tfstate"}, apply.ID))
	require.NoError(t, s.SetStackResources(ctx, stackA, apply.ID, resourceList("aws_vpc.main"), store.ObservedNow))

	_, err = s.UpsertCheck(ctx, store.Check{RunID: apply.ID, StackID: stackA, Name: "plan", Status: v1.CheckPass, Summary: "ok"})
	require.NoError(t, err)
	_, err = s.TryLockStacks(ctx, []uuid.UUID{stackA}, apply.ID, 7, "apply")
	require.NoError(t, err)
	_, err = s.RecordDrift(ctx, store.Drift{StackID: stackB, RunID: &apply.ID, Drifted: true})
	require.NoError(t, err)

	_, _, err = s.CreateSession(ctx, store.NewSession{Login: "alice", TTL: time.Hour})
	require.NoError(t, err)
	_, _, err = s.CreateAPIKey(ctx, store.NewAPIKey{Name: "ci", CreatedBy: "alice"})
	require.NoError(t, err)
	scopedInstallation := int64(1)
	scopedExpiry := time.Now().Add(time.Hour)
	_, _, err = s.CreateAPIKey(ctx, store.NewAPIKey{
		Name:           "dashboard",
		CreatedBy:      "alice",
		ReadOnly:       true,
		InstallationID: &scopedInstallation,
		ExpiresAt:      &scopedExpiry,
	})
	require.NoError(t, err, "a scoped key exercises the 0016 down migration against the columns it drops")
	_, err = s.RecordAudit(ctx, store.AuditEntry{Actor: "alice", Action: "lock.release", Target: "stacks/a"})
	require.NoError(t, err)
	_, err = s.SeenJTI(ctx, "jti-1", time.Now().Add(time.Hour))
	require.NoError(t, err)

	traceCtx := json.RawMessage(`{"traceparent":"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}`)
	_, err = s.InsertEvent(ctx, "evt-1", "push", json.RawMessage(`{}`), traceCtx)
	require.NoError(t, err)
	_, _, err = s.EnqueueJob(ctx, "plan", json.RawMessage(`{}`), time.Now(), "dedupe-1", traceCtx)
	require.NoError(t, err)
}

func TestMigrateDownToPopulatedRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx
	latest, err := store.LatestMigration()
	require.NoError(t, err)

	populateForDowngrade(f)
	require.Equal(t, 23, tableCount(t, f.s))

	for v := latest; v > 0; v-- {
		reverted, err := f.s.MigrateDownTo(ctx, v-1)
		require.NoErrorf(t, err, "down to %d with data present", v-1)
		assert.Truef(t, reverted, "down to %d reverted a migration", v-1)
		got, dirty, err := f.s.SchemaVersion(ctx)
		require.NoError(t, err)
		assert.Equal(t, v-1, got)
		assert.False(t, dirty)
	}
	assert.Equal(t, 0, tableCount(t, f.s))

	require.NoError(t, f.s.Migrate(ctx))
	got, _, err := f.s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, latest, got)
	assert.Equal(t, 23, tableCount(t, f.s), "every table returns after migrating up again")
}

func TestMigrateDownTo(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx
	latest, err := store.LatestMigration()
	require.NoError(t, err)
	require.GreaterOrEqual(t, latest, uint(3))
	populateForDowngrade(f)

	reverted, err := f.s.MigrateDownTo(ctx, latest-2)
	require.NoError(t, err)
	assert.True(t, reverted)
	got, _, err := f.s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, latest-2, got)

	reverted, err = f.s.MigrateDownTo(ctx, latest-2)
	require.NoError(t, err)
	assert.False(t, reverted, "already at the target is a no-op")

	_, err = f.s.MigrateDownTo(ctx, latest)
	var behind *store.SchemaBehindError
	require.ErrorAs(t, err, &behind, "a target above the applied version is refused, not migrated up")
	assert.Equal(t, latest-2, behind.Current)
	assert.Equal(t, latest, behind.Target)
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

func TestMigrateRefusesDirty(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	require.NoError(t, s.Migrate(ctx))
	_, err = s.Pool().Exec(ctx, `UPDATE schema_migrations SET dirty = true`)
	require.NoError(t, err)

	target, err := store.LatestMigration()
	require.NoError(t, err)

	_, upErr := s.MigrateUp(ctx, target)
	var upDirty *store.SchemaDirtyError
	require.ErrorAs(t, upErr, &upDirty, "MigrateUp refuses a dirty schema with a typed error")
	assert.Equal(t, target, upDirty.Version)

	startErr := s.Migrate(ctx)
	var startDirty *store.SchemaDirtyError
	require.ErrorAs(t, startErr, &startDirty, "Migrate refuses a dirty schema with a typed error")
	assert.Equal(t, target, startDirty.Version)
}

func TestForceVersionRecoversDirtyRollback(t *testing.T) {
	ctx := t.Context()
	target, err := store.LatestMigration()
	require.NoError(t, err)
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	applied, err := s.MigrateUp(ctx, 2)
	require.NoError(t, err)
	require.True(t, applied)
	_, err = s.Pool().Exec(ctx, `UPDATE schema_migrations SET version = 3, dirty = true`)
	require.NoError(t, err)

	var dirtyErr *store.SchemaDirtyError
	require.ErrorAs(t, s.Migrate(ctx), &dirtyErr)
	assert.Equal(t, uint(3), dirtyErr.Version)

	previous, err := s.ForceVersion(ctx, 2)
	require.NoError(t, err, "record the version the rolled-back schema is really at")
	assert.Equal(t, uint(3), previous, "it reports the version the schema was dirty at")
	ver, dirty, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(2), ver)
	assert.False(t, dirty)

	require.NoError(t, s.Migrate(ctx), "re-applies the migration that failed, and the rest")
	assert.Equal(t, 23, tableCount(t, s))
	ver, dirty, err = s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, target, ver)
	assert.False(t, dirty)
}

func TestForceVersionZeroResetsToPristine(t *testing.T) {
	ctx := t.Context()
	target, err := store.LatestMigration()
	require.NoError(t, err)
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	_, err = s.Pool().Exec(ctx, `
		CREATE TABLE schema_migrations (version bigint not null primary key, dirty boolean not null);
		INSERT INTO schema_migrations (version, dirty) VALUES (1, true)`)
	require.NoError(t, err, "simulate the first migration failing and rolling back, leaving only the dirty flag")

	var dirtyErr *store.SchemaDirtyError
	require.ErrorAs(t, s.Migrate(ctx), &dirtyErr)
	assert.Equal(t, uint(1), dirtyErr.Version)

	previous, err := s.ForceVersion(ctx, 0)
	require.NoError(t, err, "no migration is applied after the first one rolled back")
	assert.Equal(t, uint(1), previous, "it reports the version the schema was dirty at")
	ver, dirty, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(0), ver)
	assert.False(t, dirty)

	require.NoError(t, s.Migrate(ctx), "migrate runs from the first migration")
	assert.Equal(t, 23, tableCount(t, s))
	ver, _, err = s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, target, ver)
}

func TestForceVersionRefusesCleanSchema(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, pgtest.DSN(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	applied, err := s.MigrateUp(ctx, 2)
	require.NoError(t, err)
	require.True(t, applied)

	_, err = s.ForceVersion(ctx, 1)
	var notDirty *store.SchemaNotDirtyError
	require.ErrorAs(t, err, &notDirty, "forcing a clean schema is refused with a typed error")
	assert.Equal(t, uint(2), notDirty.Version)

	ver, dirty, err := s.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(2), ver, "a refused force leaves the recorded version untouched")
	assert.False(t, dirty)
}

func TestForceVersionConcurrentRecoversOnce(t *testing.T) {
	ctx := t.Context()
	dsn := pgtest.DSN(t)
	seed, err := store.Open(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(seed.Close)

	applied, err := seed.MigrateUp(ctx, 2)
	require.NoError(t, err)
	require.True(t, applied)
	_, err = seed.Pool().Exec(ctx, `UPDATE schema_migrations SET version = 3, dirty = true`)
	require.NoError(t, err)

	const recoveries = 8
	stores := make([]*store.Store, recoveries)
	for i := range stores {
		st, err := store.Open(ctx, dsn)
		require.NoError(t, err)
		t.Cleanup(st.Close)
		stores[i] = st
	}
	var wg sync.WaitGroup
	errs := make([]error, recoveries)
	for i, st := range stores {
		wg.Go(func() { _, errs[i] = st.ForceVersion(ctx, 2) })
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		var notDirty *store.SchemaNotDirtyError
		require.ErrorAs(t, err, &notDirty, "every losing recovery refuses the already-clean schema")
	}
	assert.Equal(t, 1, succeeded, "exactly one concurrent recovery clears the dirty flag")

	ver, dirty, err := seed.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint(2), ver)
	assert.False(t, dirty)
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
	assert.Equal(t, uint(16), ver)
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
