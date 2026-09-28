package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/stackorder/stackorder/migrations"
)

// Migrate applies every pending up migration. It holds the session-level
// advisory lock MigrationLockKey for the duration, so servers starting at
// the same time run the migrations one after the other.
func (s *Store) Migrate(ctx context.Context) error {
	return s.withMigrator(ctx, "migrate up", func(m *migrate.Migrate) error {
		return m.Up()
	})
}

// MigrateDown reverts every applied migration. It exists for tests and for
// operators tearing a database down; the server never calls it.
func (s *Store) MigrateDown(ctx context.Context) error {
	return s.withMigrator(ctx, "migrate down", func(m *migrate.Migrate) error {
		return m.Down()
	})
}

// SchemaVersion returns the applied migration version and whether the last
// migration failed half way. Version 0 means no migration has been applied.
func (s *Store) SchemaVersion(ctx context.Context) (version uint, dirty bool, err error) {
	err = s.withMigrator(ctx, "schema version", func(m *migrate.Migrate) error {
		var verr error
		version, dirty, verr = m.Version()
		if errors.Is(verr, migrate.ErrNilVersion) {
			return nil
		}
		return verr
	})
	return version, dirty, err
}

func (s *Store) withMigrator(ctx context.Context, op string, fn func(*migrate.Migrate) error) (err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return wrap(op, err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, MigrationLockKey); err != nil {
		return wrap(op+": lock", err)
	}
	defer func() {
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, MigrationLockKey); uerr != nil {
			err = errors.Join(err, wrap(op+": unlock", uerr))
		}
	}()

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
	if err := fn(m); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return wrap(op, err)
	}
	return nil
}
