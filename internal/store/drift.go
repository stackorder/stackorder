package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Drift is one drift observation of a stack.
type Drift struct {
	ID          uuid.UUID       `db:"id"`
	StackID     uuid.UUID       `db:"stack_id"`
	StackKey    string          `db:"stack_key"`
	RunID       *uuid.UUID      `db:"run_id"`
	CheckedAt   time.Time       `db:"checked_at"`
	Drifted     bool            `db:"drifted"`
	Summary     *v1.PlanSummary `db:"summary"`
	IssueNumber int             `db:"issue_number"`
}

// ToV1 converts the row; the caller fills in the issue URL.
func (d Drift) ToV1() v1.DriftStatus {
	return v1.DriftStatus{
		CheckedAt:   d.CheckedAt.UTC(),
		Drifted:     d.Drifted,
		Summary:     d.Summary,
		IssueNumber: d.IssueNumber,
	}
}

const driftCols = `d.id, d.stack_id, s.key AS stack_key, d.run_id, d.checked_at, d.drifted, d.summary, d.issue_number`

const driftSelect = `SELECT ` + driftCols + ` FROM drift d JOIN stacks s ON s.id = d.stack_id`

// RecordDrift stores a drift observation. ID and StackKey are ignored; a
// zero CheckedAt means now.
func (s *Store) RecordDrift(ctx context.Context, d Drift) (Drift, error) {
	var at *time.Time
	if !d.CheckedAt.IsZero() {
		at = &d.CheckedAt
	}
	out, err := queryOne[Drift](ctx, s.db, `
		WITH d AS (
			INSERT INTO drift (stack_id, run_id, checked_at, drifted, summary, issue_number)
			VALUES ($1, $2, COALESCE($3::timestamptz, now()), $4, $5, $6)
			RETURNING *
		)
		SELECT `+driftCols+` FROM d JOIN stacks s ON s.id = d.stack_id`,
		d.StackID, d.RunID, at, d.Drifted, d.Summary, d.IssueNumber)
	return out, wrap("record drift", err)
}

// SetDriftIssue records the GitHub issue opened or updated for a drift
// observation.
func (s *Store) SetDriftIssue(ctx context.Context, id uuid.UUID, issueNumber int) error {
	return s.execOne(ctx, "set drift issue", `UPDATE drift SET issue_number = $2 WHERE id = $1`, id, issueNumber)
}

// LatestDrift returns the newest drift observation of a stack.
func (s *Store) LatestDrift(ctx context.Context, stackID uuid.UUID) (Drift, error) {
	out, err := queryOne[Drift](ctx, s.db, driftSelect+`
		WHERE d.stack_id = $1 ORDER BY d.checked_at DESC, d.id DESC LIMIT 1`, stackID)
	return out, wrap("latest drift", err)
}

// LatestDriftForRepo returns the newest drift observation of every stack
// of a repository that is not marked removed, ordered by stack key.
func (s *Store) LatestDriftForRepo(ctx context.Context, repoID int64) ([]Drift, error) {
	out, err := queryAll[Drift](ctx, s.db, `
		SELECT * FROM (
			SELECT DISTINCT ON (d.stack_id) `+driftCols+`
			FROM drift d JOIN stacks s ON s.id = d.stack_id
			WHERE s.repo_id = $1 AND s.removed_at IS NULL
			ORDER BY d.stack_id, d.checked_at DESC, d.id DESC
		) latest ORDER BY stack_key`, repoID)
	return out, wrap("latest drift for repo", err)
}

// PruneDrift deletes drift observations checked more than olderThan ago,
// always keeping the newest observation of each stack, and returns how many
// were deleted.
func (s *Store) PruneDrift(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM drift d
		WHERE d.checked_at < now() - $1::bigint * interval '1 microsecond'
		  AND EXISTS (
		      SELECT 1 FROM drift n
		      WHERE n.stack_id = d.stack_id AND (n.checked_at, n.id) > (d.checked_at, d.id))`,
		micros(olderThan))
	if err != nil {
		return 0, wrap("prune drift", err)
	}
	return tag.RowsAffected(), nil
}
