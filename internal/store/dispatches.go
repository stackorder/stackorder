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
// environment, mode) of a run. A wave and environment with more stacks than
// one dispatch may carry is split into chunks numbered from 0.
type Dispatch struct {
	ID            uuid.UUID  `db:"id"`
	RunID         uuid.UUID  `db:"run_id"`
	RepoID        int64      `db:"repo_id"`
	Wave          int        `db:"wave"`
	Environment   string     `db:"environment"`
	Mode          v1.RunMode `db:"mode"`
	Chunk         int        `db:"chunk"`
	WorkflowRunID *int64     `db:"workflow_run_id"`
	DispatchedAt  time.Time  `db:"dispatched_at"`
	// SentAt is when GitHub accepted the workflow_dispatch call; nil while
	// the call has not been made or has not succeeded.
	SentAt      *time.Time `db:"sent_at"`
	CompletedAt *time.Time `db:"completed_at"`
	Conclusion  string     `db:"conclusion"`
}

const dispatchCols = `d.id, d.run_id, r.repo_id, d.wave, d.environment, d.mode, d.chunk, d.workflow_run_id,
	d.dispatched_at, d.sent_at, d.completed_at, d.conclusion`

const dispatchSelect = `SELECT ` + dispatchCols + ` FROM dispatches d JOIN runs r ON r.id = d.run_id`

const dispatchFromCTE = ` SELECT ` + dispatchCols + ` FROM d JOIN runs r ON r.id = d.run_id`

// CreateDispatch records a dispatch for a (run, wave, environment, mode).
// If one already exists it is returned with created == false, which makes
// the dispatching handler idempotent. An empty environment is
// v1.DefaultEnvironment. It is CreateDispatchChunk for chunk 0.
func (s *Store) CreateDispatch(ctx context.Context, runID uuid.UUID, wave int, environment string, mode v1.RunMode) (Dispatch, bool, error) {
	return s.CreateDispatchChunk(ctx, runID, wave, environment, mode, 0)
}

// CreateDispatchChunk records one chunk of the dispatches for a (run, wave,
// environment, mode), or returns the existing one with created == false.
func (s *Store) CreateDispatchChunk(ctx context.Context, runID uuid.UUID, wave int, environment string, mode v1.RunMode, chunk int) (Dispatch, bool, error) {
	const op = "create dispatch"
	if chunk < 0 {
		return Dispatch{}, false, invalid(op, "chunk must not be negative")
	}
	environment = environmentOrDefault(environment)
	out, err := queryOne[Dispatch](ctx, s.db, `
		WITH d AS (
			INSERT INTO dispatches (run_id, wave, environment, mode, chunk)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (run_id, wave, environment, mode, chunk) DO NOTHING
			RETURNING *
		)`+dispatchFromCTE, runID, wave, environment, mode, chunk)
	if err == nil {
		return out, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Dispatch{}, false, wrap(op, err)
	}
	out, err = queryOne[Dispatch](ctx, s.db, dispatchSelect+`
		WHERE d.run_id = $1 AND d.wave = $2 AND d.environment = $3 AND d.mode = $4 AND d.chunk = $5`,
		runID, wave, environment, mode, chunk)
	return out, false, wrap(op, err)
}

// MarkDispatchSent records that GitHub accepted the dispatch. The first
// time is kept when it is marked twice.
func (s *Store) MarkDispatchSent(ctx context.Context, id uuid.UUID) (Dispatch, error) {
	out, err := queryOne[Dispatch](ctx, s.db, `
		WITH d AS (
			UPDATE dispatches SET sent_at = COALESCE(sent_at, now()) WHERE id = $1 RETURNING *
		)`+dispatchFromCTE, id)
	return out, wrap("mark dispatch sent", err)
}

// GetDispatch returns one dispatch.
func (s *Store) GetDispatch(ctx context.Context, id uuid.UUID) (Dispatch, error) {
	out, err := queryOne[Dispatch](ctx, s.db, dispatchSelect+` WHERE d.id = $1`, id)
	return out, wrap("get dispatch", err)
}

// SetDispatchWorkflowRun links a dispatch to the Actions workflow run it
// started. A dispatch starts one workflow run and a workflow run belongs to
// one dispatch, so linking it again to the same run is a no-op, while
// linking it to a different run, or linking a run already linked to another
// dispatch, is ErrConflict.
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

// ListDispatches returns the dispatches of a run ordered by wave,
// environment, chunk and dispatch time.
func (s *Store) ListDispatches(ctx context.Context, runID uuid.UUID) ([]Dispatch, error) {
	out, err := queryAll[Dispatch](ctx, s.db, dispatchSelect+`
		WHERE d.run_id = $1 ORDER BY d.wave, d.environment, d.chunk, d.dispatched_at, d.id`, runID)
	return out, wrap("list dispatches", err)
}

// FindDispatchByWorkflowRun returns the dispatch linked to an Actions
// workflow run.
func (s *Store) FindDispatchByWorkflowRun(ctx context.Context, workflowRunID int64) (Dispatch, error) {
	out, err := queryOne[Dispatch](ctx, s.db, dispatchSelect+` WHERE d.workflow_run_id = $1`, workflowRunID)
	return out, wrap("find dispatch by workflow run", err)
}

// OpenDispatches returns every dispatch without a conclusion, oldest
// first, for workflow_run reconciliation.
func (s *Store) OpenDispatches(ctx context.Context) ([]Dispatch, error) {
	out, err := queryAll[Dispatch](ctx, s.db, dispatchSelect+`
		WHERE d.completed_at IS NULL ORDER BY d.dispatched_at, d.id`)
	return out, wrap("open dispatches", err)
}
