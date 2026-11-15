package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/stackorder/stackorder/migrations"
)

// Migrate applies every pending up migration. It holds the session-level
// advisory lock MigrationLockKey on a dedicated connection outside the pool
// for the duration, so servers starting at the same time run the migrations
// one after the other and a pool of one connection is enough.
func (s *Store) Migrate(ctx context.Context) error {
	return s.withMigrator(ctx, "migrate up", func(m *migrate.Migrate) error {
		return m.Up()
	})
}

// SchemaAheadError reports that the database is at a migration version above
// the one MigrateUp was asked to reach. MigrateUp never reverts a migration,
// so it returns this rather than migrating down. Target is the version asked
// for, normally LatestMigration; a caller that requested no particular version
// can report it as the latest version the binary embeds.
type SchemaAheadError struct {
	Current uint
	Target  uint
}

func (e *SchemaAheadError) Error() string {
	return fmt.Sprintf("schema is at version %d, above the requested version %d; migrate does not revert migrations", e.Current, e.Target)
}

// MigrateUp applies the pending up migrations until the schema is at version,
// holding MigrationLockKey as Migrate does. It reads the applied version under
// the lock and never reverts a migration: it refuses a dirty schema, and a
// schema already above version (with a *SchemaAheadError), rather than
// migrating down. version is normally LatestMigration; passing it the current
// version is a no-op. applied reports whether a migration ran, so concurrent
// callers can tell the one that did the work from those that found the
// database already migrated.
func (s *Store) MigrateUp(ctx context.Context, version uint) (applied bool, err error) {
	err = s.withMigrator(ctx, fmt.Sprintf("migrate up to %d", version), func(m *migrate.Migrate) error {
		current, dirty, verr := m.Version()
		switch {
		case errors.Is(verr, migrate.ErrNilVersion):
			current = 0
		case verr != nil:
			return verr
		}
		if dirty {
			return fmt.Errorf("schema is dirty at version %d; repair it before migrating", current)
		}
		if current > version {
			return &SchemaAheadError{Current: current, Target: version}
		}
		if merr := m.Migrate(version); merr != nil {
			return merr
		}
		applied = true
		return nil
	})
	return applied, err
}

// LatestMigration returns the highest version embedded in migrations.FS: the
// version a full up migration reaches, and the target an operator compares the
// applied version against. It needs no database connection.
func LatestMigration() (uint, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return 0, fmt.Errorf("store: read embedded migrations: %w", err)
	}
	var (
		latest uint
		found  bool
	)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		i := strings.IndexByte(name, '_')
		if i <= 0 {
			return 0, fmt.Errorf("store: migration %q does not start with NNNN_", name)
		}
		n, err := strconv.ParseUint(name[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("store: parse version in %q: %w", name, err)
		}
		if uint(n) > latest {
			latest = uint(n)
		}
		found = true
	}
	if !found {
		return 0, fmt.Errorf("store: no up migrations embedded")
	}
	return latest, nil
}

// MigrateDown reverts every applied migration. It exists for tests and for
// operators tearing a database down; the server never calls it.
func (s *Store) MigrateDown(ctx context.Context) error {
	return s.withMigrator(ctx, "migrate down", func(m *migrate.Migrate) error {
		return m.Down()
	})
}

// SchemaVersion returns the applied migration version and whether the last
// migration failed half way. Version 0 means no migration has been applied. It
// reads schema_migrations directly under MigrationLockKey and never creates
// it, so reporting the version of an unmigrated database changes nothing.
func (s *Store) SchemaVersion(ctx context.Context) (version uint, dirty bool, err error) {
	err = s.withLock(ctx, "schema version", func(ctx context.Context, conn *pgx.Conn) error {
		var present bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&present); err != nil {
			return wrap("schema version", err)
		}
		if !present {
			version, dirty = 0, false
			return nil
		}
		var v int64
		switch err := conn.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&v, &dirty); {
		case errors.Is(err, pgx.ErrNoRows):
			version, dirty = 0, false
		case err != nil:
			return wrap("schema version", err)
		default:
			version = uint(v)
		}
		return nil
	})
	return version, dirty, err
}

func (s *Store) withLock(ctx context.Context, op string, fn func(context.Context, *pgx.Conn) error) (err error) {
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return wrap(op+": connect", err)
	}
	defer func() {
		if cerr := closeConn(conn); cerr != nil {
			err = errors.Join(err, wrap(op+": close lock connection", cerr))
		}
	}()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, MigrationLockKey); err != nil {
		return wrap(op+": lock", err)
	}
	defer func() {
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, MigrationLockKey); uerr != nil {
			err = errors.Join(err, wrap(op+": unlock", uerr))
		}
	}()
	return fn(ctx, conn)
}

func (s *Store) withMigrator(ctx context.Context, op string, fn func(*migrate.Migrate) error) error {
	return s.withLock(ctx, op, func(_ context.Context, _ *pgx.Conn) (err error) {
		src, err := iofs.New(migrations.FS, ".")
		if err != nil {
			return wrap(op+": source", err)
		}
		drv, err := migratepgx.WithInstance(stdlib.OpenDBFromPool(s.pool), &migratepgx.Config{})
		if err != nil {
			return errors.Join(wrap(op+": driver", err), src.Close())
		}
		m, err := migrate.NewWithInstance("iofs", src, "pgx5", drv)
		if err != nil {
			return errors.Join(wrap(op, err), src.Close(), drv.Close())
		}
		defer func() {
			srcErr, dbErr := m.Close()
			if srcErr != nil || dbErr != nil {
				err = errors.Join(err, fmt.Errorf("store: %s: close: %w", op, errors.Join(srcErr, dbErr)))
			}
		}()
		if ferr := fn(m); ferr != nil && !errors.Is(ferr, migrate.ErrNoChange) {
			return wrap(op, ferr)
		}
		return nil
	})
}
