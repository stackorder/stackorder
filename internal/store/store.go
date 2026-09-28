package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound is returned when the addressed row does not exist, or when
	// a write references a row that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a write conflicts with the current state,
	// such as a unique key taken by another row or a failed status guard.
	ErrConflict = errors.New("conflict")
	// ErrInvalid is returned for input the store refuses, such as a
	// malformed cursor, a duplicate key inside one graph or invalid JSON.
	ErrInvalid = errors.New("invalid input")
)

const (
	// MigrationLockKey is the advisory lock key held while migrations run.
	MigrationLockKey int64 = 0x73_6f_6d_69_67_72_61_74
	// SchedulerLockKey is the advisory lock key the scheduler leader holds.
	SchedulerLockKey int64 = 0x73_6f_73_63_68_65_64_31
)

type dbtx interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
}

// Store is the server's handle on Postgres. The zero value is not usable;
// create one with Open.
type Store struct {
	pool *pgxpool.Pool
	db   dbtx
}

// Open parses dsn, connects a pool and verifies that the database answers.
// Sessions run in UTC and timestamps are returned in UTC.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse dsn: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.AfterConnect = func(_ context.Context, c *pgx.Conn) error {
		configureConn(c)
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool, db: pool}, nil
}

func configureConn(c *pgx.Conn) {
	c.TypeMap().RegisterType(&pgtype.Type{
		Name:  "timestamptz",
		OID:   pgtype.TimestamptzOID,
		Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
	})
}

// Close closes every connection of the pool. It must not be called on a
// Store handed to an InTx callback.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping reports whether the database is reachable, for readiness probes.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Pool exposes the underlying pool for diagnostics and tests.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// InTx runs fn with a Store whose methods all execute inside one
// transaction. The transaction commits when fn returns nil and rolls back
// otherwise. Calls nest: inside fn, InTx and methods that need their own
// transaction use savepoints.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		return fn(&Store{pool: s.pool, db: tx})
	})
}

func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

func queryOne[T any](ctx context.Context, db dbtx, sql string, args ...any) (T, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		var zero T
		return zero, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
}

func queryAll[T any](ctx context.Context, db dbtx, sql string, args ...any) ([]T, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[T])
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("store: %s: %w", op, ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return fmt.Errorf("store: %s: %w: %w", op, ErrNotFound, err)
		case "23505", "23P01":
			return fmt.Errorf("store: %s: %w: %w", op, ErrConflict, err)
		case "22P02", "22P05", "23514", "22023":
			return fmt.Errorf("store: %s: %w: %w", op, ErrInvalid, err)
		}
	}
	return fmt.Errorf("store: %s: %w", op, err)
}

func notFound(op string) error {
	return fmt.Errorf("store: %s: %w", op, ErrNotFound)
}

func invalid(op, reason string) error {
	return fmt.Errorf("store: %s: %s: %w", op, reason, ErrInvalid)
}

func micros(d time.Duration) int64 {
	return d.Microseconds()
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func strs[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
