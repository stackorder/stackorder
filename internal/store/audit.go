package store

import (
	"context"
	"time"
)

// AuditEntry is one audited action, such as an unlock or a re-run.
type AuditEntry struct {
	ID      int64          `db:"id"`
	At      time.Time      `db:"at"`
	Actor   string         `db:"actor"`
	Action  string         `db:"action"`
	Target  string         `db:"target"`
	Details map[string]any `db:"details"`
}

// AuditFilter narrows ListAudit. Zero fields match everything.
type AuditFilter struct {
	Actor  string
	Action string
	Target string
	// Limit defaults to 50 and is capped at 500.
	Limit int
	// Cursor is the value returned by the previous page.
	Cursor string
}

const auditCols = `id, at, actor, action, target, details`

// RecordAudit appends an entry; ID and At are assigned by the database.
func (s *Store) RecordAudit(ctx context.Context, e AuditEntry) (AuditEntry, error) {
	const op = "record audit"
	if e.Actor == "" || e.Action == "" {
		return AuditEntry{}, invalid(op, "actor and action are required")
	}
	var details any
	if len(e.Details) > 0 {
		details = e.Details
	}
	out, err := queryOne[AuditEntry](ctx, s.db, `
		INSERT INTO audit (actor, action, target, details) VALUES ($1, $2, $3, $4::jsonb)
		RETURNING `+auditCols, e.Actor, e.Action, e.Target, details)
	return out, wrap(op, err)
}

// ListAudit returns entries newest first and the cursor of the next page.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, string, error) {
	const op = "list audit"
	before, err := decodeIDCursor(op, f.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := pageSize(f.Limit)
	out, err := queryAll[AuditEntry](ctx, s.db, `
		SELECT `+auditCols+` FROM audit
		WHERE ($1::text = '' OR actor = $1::text)
		  AND ($2::text = '' OR action = $2::text)
		  AND ($3::text = '' OR target = $3::text)
		  AND ($4::bigint = 0 OR id < $4::bigint)
		ORDER BY id DESC LIMIT $5`, f.Actor, f.Action, f.Target, before, limit+1)
	if err != nil {
		return nil, "", wrap(op, err)
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		next = encodeIDCursor(out[limit-1].ID)
	}
	return out, next, nil
}

// CountAuditDetail counts the entries with an action and target whose
// details hold value under key, as text, recorded at or after since. It
// enforces rate limits on part of a target, such as one pull request of a
// repository.
func (s *Store) CountAuditDetail(ctx context.Context, action, target, key, value string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM audit
		WHERE action = $1 AND target = $2 AND details->>$3 = $4 AND at >= $5`,
		action, target, key, value, since).Scan(&n)
	return n, wrap("count audit by detail", err)
}

// CountAudit counts the entries with an action and target recorded at or
// after since, which is how per-target rate limits are enforced.
func (s *Store) CountAudit(ctx context.Context, action, target string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM audit WHERE action = $1 AND target = $2 AND at >= $3`,
		action, target, since).Scan(&n)
	return n, wrap("count audit", err)
}
