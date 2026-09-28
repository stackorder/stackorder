package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Check is a named policy or cost verdict on one stack of one run.
type Check struct {
	RunID      uuid.UUID      `db:"run_id"`
	StackID    uuid.UUID      `db:"stack_id"`
	StackKey   string         `db:"stack_key"`
	Name       string         `db:"name"`
	Status     v1.CheckStatus `db:"status"`
	Summary    string         `db:"summary"`
	Details    string         `db:"details"`
	DetailsURL string         `db:"details_url"`
	UpdatedAt  time.Time      `db:"updated_at"`
}

// ToV1 converts the row.
func (c Check) ToV1() v1.Check {
	return v1.Check{
		Name:       c.Name,
		Status:     c.Status,
		Summary:    c.Summary,
		DetailsURL: c.DetailsURL,
		UpdatedAt:  c.UpdatedAt.UTC(),
	}
}

const checkCols = `c.run_id, c.stack_id, s.key AS stack_key, c.name, c.status, c.summary, c.details,
	c.details_url, c.updated_at`

// UpsertCheck records the latest verdict of a named check. The stack must
// be part of the run, or ErrNotFound is returned.
func (s *Store) UpsertCheck(ctx context.Context, c Check) (Check, error) {
	const op = "upsert check"
	if c.Name == "" {
		return Check{}, invalid(op, "name is required")
	}
	out, err := queryOne[Check](ctx, s.db, `
		WITH c AS (
			INSERT INTO checks (run_id, stack_id, name, status, summary, details, details_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (run_id, stack_id, name) DO UPDATE SET
				status = EXCLUDED.status,
				summary = EXCLUDED.summary,
				details = EXCLUDED.details,
				details_url = EXCLUDED.details_url,
				updated_at = now()
			RETURNING *
		)
		SELECT `+checkCols+` FROM c JOIN stacks s ON s.id = c.stack_id`,
		c.RunID, c.StackID, c.Name, c.Status, c.Summary, c.Details, c.DetailsURL)
	return out, wrap(op, err)
}

// ListChecks returns every check of a run ordered by stack key and name.
func (s *Store) ListChecks(ctx context.Context, runID uuid.UUID) ([]Check, error) {
	out, err := queryAll[Check](ctx, s.db, `
		SELECT `+checkCols+` FROM checks c JOIN stacks s ON s.id = c.stack_id
		WHERE c.run_id = $1 ORDER BY s.key, c.name`, runID)
	return out, wrap("list checks", err)
}
