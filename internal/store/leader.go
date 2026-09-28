package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const releaseTimeout = 5 * time.Second

// AdvisoryLock is a session-level Postgres advisory lock held on a
// dedicated connection. It is safe for concurrent use.
type AdvisoryLock struct {
	mu   sync.Mutex
	conn *pgx.Conn
	key  int64
}

// TryAdvisoryLock tries to take the session-level advisory lock key on a
// dedicated connection outside the pool, without waiting. When acquired,
// the lock is held until Release; if the connection dies, Postgres frees
// the lock and another server can take it, which Held then reports. When
// not acquired, the returned lock is never held and Release is a no-op.
func (s *Store) TryAdvisoryLock(ctx context.Context, key int64) (lock *AdvisoryLock, acquired bool, err error) {
	const op = "try advisory lock"
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return &AdvisoryLock{}, false, wrap(op, err)
	}
	configureConn(conn)
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil {
		return &AdvisoryLock{}, false, errors.Join(wrap(op, err), closeConn(conn))
	}
	if !ok {
		return &AdvisoryLock{}, false, wrap(op, closeConn(conn))
	}
	return &AdvisoryLock{conn: conn, key: key}, true, nil
}

// Held reports whether this session still holds the lock. It returns false
// once the lock was released, when its connection is lost, or when the
// check itself fails; a leader must then stop acting as one and call
// Release before trying to take the lock again.
func (l *AdvisoryLock) Held(ctx context.Context) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return false
	}
	var held bool
	err := l.conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND granted AND objsubid = 1 AND pid = pg_backend_pid()
			  AND ((classid::bigint << 32) | objid::bigint) = $1)`, l.key).Scan(&held)
	return err == nil && held
}

// Release unlocks and closes the connection. Calling it again, or on a lock
// that was not acquired, does nothing.
func (l *AdvisoryLock) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	_, _ = l.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, l.key)
	_ = l.conn.Close(ctx)
	l.conn = nil
}

func closeConn(conn *pgx.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	return conn.Close(ctx)
}

// LockKey takes the transaction-level advisory lock named by key, waiting
// for it, so concurrent transactions that lock the same key run one after
// the other. The lock is released when the transaction ends; it is only
// meaningful on a Store handed to an InTx callback.
func (s *Store) LockKey(ctx context.Context, key string) error {
	if key == "" {
		return invalid("lock key", "key is required")
	}
	_, err := s.db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key)
	return wrap("lock key", err)
}
