package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const releaseTimeout = 5 * time.Second

// TryAdvisoryLock tries to take the session-level advisory lock key on a
// dedicated connection outside the pool, without waiting. When acquired,
// the lock is held until release is called, which unlocks and closes the
// connection; if the connection dies, Postgres releases the lock and
// another server can take over. When not acquired, release is a no-op.
func (s *Store) TryAdvisoryLock(ctx context.Context, key int64) (release func(), acquired bool, err error) {
	const op = "try advisory lock"
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return func() {}, false, wrap(op, err)
	}
	configureConn(conn)
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil {
		return func() {}, false, errors.Join(wrap(op, err), closeConn(conn))
	}
	if !ok {
		return func() {}, false, wrap(op, closeConn(conn))
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
			defer cancel()
			_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key)
			_ = conn.Close(ctx)
		})
	}
	return release, true, nil
}

func closeConn(conn *pgx.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	return conn.Close(ctx)
}
