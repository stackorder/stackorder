package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ObjectDeletion is a plan-text or plan-summary object of a run's stack
// that is no longer referenced in Postgres and waits to be removed from the
// artifact store. Rows outlive the run and repository they belonged to, so
// the table has no foreign key to them.
type ObjectDeletion struct {
	RunID      uuid.UUID `db:"run_id"`
	StackKey   string    `db:"stack_key"`
	EnqueuedAt time.Time `db:"enqueued_at"`
	Attempts   int       `db:"attempts"`
	LastError  string    `db:"last_error"`
}

const objectDeletionCols = `run_id, stack_key, enqueued_at, attempts, last_error`

// ClaimObjectDeletions returns up to n objects waiting to be removed from
// the artifact store, oldest first. It takes no lock: the sweep deletes the
// object, which is idempotent, and then clears the row with
// CompleteObjectDeletion, so two instances sweeping at once only repeat a
// harmless delete.
func (s *Store) ClaimObjectDeletions(ctx context.Context, n int) ([]ObjectDeletion, error) {
	if n <= 0 {
		return nil, nil
	}
	out, err := queryAll[ObjectDeletion](ctx, s.db, `
		SELECT `+objectDeletionCols+` FROM object_deletions
		ORDER BY enqueued_at LIMIT $1`, n)
	return out, wrap("claim object deletions", err)
}

// CompleteObjectDeletion forgets a waiting object once it is gone from the
// artifact store. Forgetting a row another instance already cleared is not
// an error.
func (s *Store) CompleteObjectDeletion(ctx context.Context, runID uuid.UUID, stackKey string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM object_deletions WHERE run_id = $1 AND stack_key = $2`, runID, stackKey)
	return wrap("complete object deletion", err)
}

// FailObjectDeletion records a failed attempt to remove an object, such as
// when the server's role lacks s3:DeleteObject, so the row is retried and
// the failure stays visible.
func (s *Store) FailObjectDeletion(ctx context.Context, runID uuid.UUID, stackKey string, cause error) error {
	_, err := s.db.Exec(ctx, `
		UPDATE object_deletions SET attempts = attempts + 1, last_error = $3
		WHERE run_id = $1 AND stack_key = $2`, runID, stackKey, errorText(cause))
	return wrap("fail object deletion", err)
}

// CountObjectDeletions counts the objects waiting to be removed, for the
// pending-deletions gauge.
func (s *Store) CountObjectDeletions(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM object_deletions`).Scan(&n)
	return n, wrap("count object deletions", err)
}
