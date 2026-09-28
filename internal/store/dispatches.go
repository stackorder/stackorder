package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Dispatch is one workflow_dispatch of stackorder-run.yml for a (wave,
// environment, mode) of a run.
type Dispatch struct {
	ID            uuid.UUID  `db:"id"`
	RunID         uuid.UUID  `db:"run_id"`
	RepoID        int64      `db:"repo_id"`
	Wave          int        `db:"wave"`
	Environment   string     `db:"environment"`
	Mode          v1.RunMode `db:"mode"`
	WorkflowRunID *int64     `db:"workflow_run_id"`
	DispatchedAt  time.Time  `db:"dispatched_at"`
	CompletedAt   *time.Time `db:"completed_at"`
	Conclusion    string     `db:"conclusion"`
}

const dispatchCols = `d.id, d.run_id, r.repo_id, d.wave, d.environment, d.mode, d.workflow_run_id,
	d.dispatched_at, d.completed_at, d.conclusion`

const dispatchSelect = `SELECT ` + dispatchCols + ` FROM dispatches d JOIN runs r ON r.id = d.run_id`

const dispatchFromCTE = ` SELECT ` + dispatchCols + ` FROM d JOIN runs r ON r.id = d.run_id`

// CreateDispatch records a dispatch for a (run, wave, environment, mode).
// If one already exists it is returned with created == false, which makes
// the dispatching handler idempotent. An empty environment is
// v1.DefaultEnvironment.
func (s *Store) CreateDispatch(ctx context.Context, runID uuid.UUID, wave int, environment string, mode v1.RunMode) (Dispatch, bool, error) {
	const op = "create dispatch"
	environment = environmentOrDefault(environment)
	out, err := queryOne[Dispatch](ctx, s.db, `
		WITH d AS (
			INSERT INTO dispatches (run_id, wave, environment, mode)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (run_id, wave, environment, mode) DO NOTHING
			RETURNING *
		)`+dispatchFromCTE, runID, wave, environment, mode)
	if err == nil {
		return out, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Dispatch{}, false, wrap(op, err)
	}
	out, err = queryOne[Dispatch](ctx, s.db, dispatchSelect+`
		WHERE d.run_id = $1 AND d.wave = $2 AND d.environment = $3 AND d.mode = $4`, runID, wave, environment, mode)
	return out, false, wrap(op, err)
}

// SetDispatchWorkflowRun links a dispatch to the Actions workflow run it
// started. A dispatch starts one workflow run, so linking it again to the
// same run is a no-op and linking it to a different one is ErrConflict.
func (s *Store) SetDispatchWorkflowRun(ctx context.Context, id uuid.UUID, workflowRunID int64) error {
	const op = "set dispatch workflow run"
	tag, err := s.db.Exec(ctx, `
		UPDATE dispatches SET workflow_run_id = $2
		WHERE id = $1 AND (workflow_run_id IS NULL OR workflow_run_id = $2)`, id, workflowRunID)
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return s.guardError(ctx, op, `SELECT 1 FROM dispatches WHERE id = $1`, id)
	}
	return nil
}

// CompleteDispatch records the conclusion of a dispatch. The first
// completion time is kept when it is reported twice.
func (s *Store) CompleteDispatch(ctx context.Context, id uuid.UUID, conclusion string) (Dispatch, error) {
	out, err := queryOne[Dispatch](ctx, s.db, `
		WITH d AS (
			UPDATE dispatches SET completed_at = COALESCE(completed_at, now()), conclusion = $2
			WHERE id = $1
			RETURNING *
		)`+dispatchFromCTE, id, conclusion)
	return out, wrap("complete dispatch", err)
}

// ListDispatches returns the dispatches of a run ordered by wave and
// environment.
func (s *Store) ListDispatches(ctx context.Context, runID uuid.UUID) ([]Dispatch, error) {
	out, err := queryAll[Dispatch](ctx, s.db, dispatchSelect+`
		WHERE d.run_id = $1 ORDER BY d.wave, d.environment`, runID)
	return out, wrap("list dispatches", err)
}

// FindDispatchByWorkflowRun returns the dispatch linked to an Actions
// workflow run.
func (s *Store) FindDispatchByWorkflowRun(ctx context.Context, workflowRunID int64) (Dispatch, error) {
	out, err := queryOne[Dispatch](ctx, s.db, dispatchSelect+`
		WHERE d.workflow_run_id = $1 ORDER BY d.dispatched_at DESC LIMIT 1`, workflowRunID)
	return out, wrap("find dispatch by workflow run", err)
}

// OpenDispatches returns every dispatch without a conclusion, oldest
// first, for workflow_run reconciliation.
func (s *Store) OpenDispatches(ctx context.Context) ([]Dispatch, error) {
	out, err := queryAll[Dispatch](ctx, s.db, dispatchSelect+`
		WHERE d.completed_at IS NULL ORDER BY d.dispatched_at, d.id`)
	return out, wrap("open dispatches", err)
}
