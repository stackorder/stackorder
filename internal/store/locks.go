package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Lock is an orchestration lock on a stack, distinct from the Terraform
// state lock in S3.
type Lock struct {
	StackID  uuid.UUID `db:"stack_id"`
	StackKey string    `db:"stack_key"`
	RepoID   int64     `db:"repo_id"`
	RunID    uuid.UUID `db:"run_id"`
	PRNumber int       `db:"pr_number"`
	TakenAt  time.Time `db:"taken_at"`
	Reason   string    `db:"reason"`
}

// ToV1 converts the row.
func (l Lock) ToV1() v1.LockInfo {
	return v1.LockInfo{
		StackID:  l.StackID.String(),
		StackKey: l.StackKey,
		RunID:    l.RunID.String(),
		PRNumber: l.PRNumber,
		TakenAt:  l.TakenAt.UTC(),
		Reason:   l.Reason,
	}
}

const lockCols = `l.stack_id, s.key AS stack_key, s.repo_id, l.run_id, l.pr_number, l.taken_at, l.reason`

const lockSelect = `SELECT ` + lockCols + ` FROM locks l JOIN stacks s ON s.id = l.stack_id`

const lockFromCTE = ` SELECT ` + lockCols + ` FROM l JOIN stacks s ON s.id = l.stack_id ORDER BY s.key`

var errLocked = errors.New("locked")

// TryLockStacks takes the orchestration lock on every stack for a run, or
// on none of them. A lock already held by the same run, or by another run
// of the same pull request (prNumber > 0), is compatible and moves to this
// run. When any stack is held by someone else nothing is locked and the
// conflicting locks are returned with a nil error. The stacks must belong
// to the run's repository. Stack ids are locked in a fixed order so
// concurrent callers cannot deadlock.
func (s *Store) TryLockStacks(ctx context.Context, stackIDs []uuid.UUID, runID uuid.UUID, prNumber int, reason string) ([]Lock, error) {
	ids := slices.Clone(stackIDs)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	var conflicts []Lock
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var known, foreign int
		if err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE s.repo_id <> r.repo_id)
			FROM stacks s CROSS JOIN runs r
			WHERE s.id = ANY($1::uuid[]) AND r.id = $2`, ids, runID).Scan(&known, &foreign); err != nil {
			return err
		}
		switch {
		case known < len(ids):
			return fmt.Errorf("unknown run or stack: %w", ErrNotFound)
		case foreign > 0:
			return fmt.Errorf("stack outside the run's repository: %w", ErrInvalid)
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO locks (stack_id, run_id, pr_number, reason)
			SELECT t.id, $2, $3, $4 FROM unnest($1::uuid[]) AS t(id) ORDER BY t.id
			ON CONFLICT (stack_id) DO UPDATE SET run_id = EXCLUDED.run_id, reason = EXCLUDED.reason
			WHERE locks.run_id = EXCLUDED.run_id
			   OR (EXCLUDED.pr_number > 0 AND locks.pr_number = EXCLUDED.pr_number)
			RETURNING stack_id`, ids, runID, prNumber, reason)
		if err != nil {
			return err
		}
		granted, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		if len(granted) == len(ids) {
			return nil
		}
		conflicts, err = queryAll[Lock](ctx, tx, lockSelect+`
			WHERE l.stack_id = ANY($1::uuid[]) AND NOT (l.stack_id = ANY($2::uuid[]))
			ORDER BY s.key`, ids, nonNil(granted))
		if err != nil {
			return err
		}
		if len(conflicts) == 0 {
			return errors.New("lock refused without a conflicting holder")
		}
		return errLocked
	})
	if errors.Is(err, errLocked) {
		return conflicts, nil
	}
	if err != nil {
		return nil, wrap("try lock stacks", err)
	}
	return nil, nil
}

// ReleaseLocksForRun releases every lock held by a run and returns them.
func (s *Store) ReleaseLocksForRun(ctx context.Context, runID uuid.UUID) ([]Lock, error) {
	out, err := queryAll[Lock](ctx, s.db, `
		WITH l AS (DELETE FROM locks WHERE run_id = $1 RETURNING *)`+lockFromCTE, runID)
	return out, wrap("release locks for run", err)
}

// ReleaseLocksForPR releases every lock a pull request holds in a
// repository and returns them.
func (s *Store) ReleaseLocksForPR(ctx context.Context, repoID int64, prNumber int) ([]Lock, error) {
	if prNumber <= 0 {
		return nil, nil
	}
	out, err := queryAll[Lock](ctx, s.db, `
		WITH l AS (
			DELETE FROM locks WHERE pr_number = $2
			  AND stack_id IN (SELECT id FROM stacks WHERE repo_id = $1)
			RETURNING *
		)`+lockFromCTE, repoID, prNumber)
	return out, wrap("release locks for pr", err)
}

// ReleaseLock releases the lock on one stack and returns it, or
// ErrNotFound when the stack was not locked.
func (s *Store) ReleaseLock(ctx context.Context, stackID uuid.UUID) (Lock, error) {
	out, err := queryOne[Lock](ctx, s.db, `
		WITH l AS (DELETE FROM locks WHERE stack_id = $1 RETURNING *)`+lockFromCTE, stackID)
	return out, wrap("release lock", err)
}

// ListLocks returns the locks held in a repository, or in every repository
// when repoID is 0, ordered by stack key.
func (s *Store) ListLocks(ctx context.Context, repoID int64) ([]Lock, error) {
	out, err := queryAll[Lock](ctx, s.db, lockSelect+`
		WHERE $1::bigint = 0 OR s.repo_id = $1::bigint ORDER BY s.key, s.repo_id`, repoID)
	return out, wrap("list locks", err)
}

// GetLock returns the lock on a stack, or ErrNotFound.
func (s *Store) GetLock(ctx context.Context, stackID uuid.UUID) (Lock, error) {
	out, err := queryOne[Lock](ctx, s.db, lockSelect+` WHERE l.stack_id = $1`, stackID)
	return out, wrap("get lock", err)
}
