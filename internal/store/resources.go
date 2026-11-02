package store

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// StackResources is the resource list an apply or drift job last reported
// for a stack, read from the stack's state.
type StackResources struct {
	StackID uuid.UUID `db:"stack_id"`
	// RunID is the run of that job, nil once the run is deleted.
	RunID   *uuid.UUID `db:"run_id"`
	RunMode v1.RunMode `db:"run_mode"`
	SHA     string     `db:"sha"`
	// ObservedAt is when the job read the state, or a lower bound of it:
	// the time a confirmed apply result was recorded, otherwise the time
	// its run was created.
	ObservedAt time.Time `db:"observed_at"`
	// ReportedAt is when the list was last recorded.
	ReportedAt time.Time `db:"reported_at"`
	// ChangedAt is when the recorded list last differed from the one
	// before it.
	ChangedAt time.Time          `db:"changed_at"`
	Total     int                `db:"total"`
	Truncated bool               `db:"truncated"`
	Resources []v1.StackResource `db:"resources"`
}

// ToV1 converts the row to the body of the stack's resource endpoint,
// without any last change.
func (r StackResources) ToV1() v1.StackResources {
	out := v1.StackResources{
		Mode:       r.RunMode,
		SHA:        r.SHA,
		ReportedAt: utc(&r.ReportedAt),
		ChangedAt:  utc(&r.ChangedAt),
		Total:      r.Total,
		Truncated:  r.Truncated,
		Items:      slices.Clone(nonNil(r.Resources)),
	}
	if r.RunID != nil {
		out.RunID = r.RunID.String()
	}
	return out
}

type resourceDigest struct {
	Total     int             `json:"total"`
	Truncated bool            `json:"truncated"`
	Items     json.RawMessage `json:"items"`
}

// SetStackResources records the resource list an apply or drift job of run
// reported for the stack, sorted by address and without any last change.
// The run must be an apply or drift run of the stack's repository. The
// list replaces the recorded one only when the state it was read from is
// not older: a confirmed apply result counts as read now, a drift result
// or an unconfirmed apply result as read when its run was created, so a
// drift run created before an apply's list was recorded never replaces
// it, and the same run may report again. A list equal to the recorded one
// keeps its changed_at. It returns ErrInvalid when the run may not report
// for the stack, and ErrNotFound when the stack or the run does not exist.
func (s *Store) SetStackResources(ctx context.Context, stackID, runID uuid.UUID, list v1.ResourceList, unconfirmed bool) error {
	const op = "set stack resources"
	items := make([]v1.StackResource, len(list.Items))
	for i, it := range list.Items {
		it.LastChange = nil
		items[i] = it
	}
	slices.SortStableFunc(items, func(a, b v1.StackResource) int { return cmp.Compare(a.Address, b.Address) })
	body, err := json.Marshal(items)
	if err != nil {
		return wrap(op, err)
	}
	canonical, err := json.Marshal(resourceDigest{Total: list.Total, Truncated: list.Truncated, Items: body})
	if err != nil {
		return wrap(op, err)
	}
	digest := sha256.Sum256(canonical)
	var allowed bool
	err = s.db.QueryRow(ctx, `
		WITH report AS (
			SELECT s.id AS stack_id, r.id AS run_id, r.mode, r.sha,
			       CASE WHEN r.mode = 'apply' AND NOT $6::boolean THEN now() ELSE r.created_at END AS observed_at,
			       s.repo_id = r.repo_id AND r.mode IN ('apply', 'drift') AS allowed
			FROM stacks s CROSS JOIN runs r
			WHERE s.id = $1 AND r.id = $2
		), recorded AS (
			INSERT INTO stack_resources AS sr (stack_id, run_id, run_mode, sha, observed_at, total, truncated, digest, resources)
			SELECT p.stack_id, p.run_id, p.mode, p.sha, p.observed_at, $3::integer, $4::boolean, $5::bytea, $7::jsonb
			FROM report p
			WHERE p.allowed
			ON CONFLICT (stack_id) DO UPDATE SET
				run_id = EXCLUDED.run_id,
				run_mode = EXCLUDED.run_mode,
				sha = EXCLUDED.sha,
				observed_at = EXCLUDED.observed_at,
				reported_at = now(),
				changed_at = CASE WHEN sr.digest = EXCLUDED.digest THEN sr.changed_at ELSE now() END,
				total = EXCLUDED.total,
				truncated = EXCLUDED.truncated,
				resources = CASE WHEN sr.digest = EXCLUDED.digest THEN sr.resources ELSE EXCLUDED.resources END,
				digest = EXCLUDED.digest
			WHERE EXCLUDED.observed_at >= sr.observed_at
		)
		SELECT allowed FROM report`,
		stackID, runID, list.Total, list.Truncated, digest[:], unconfirmed, body).Scan(&allowed)
	if err != nil {
		return wrap(op, err)
	}
	if !allowed {
		return invalid(op, "run is not an apply or drift run of the stack's repository")
	}
	return nil
}

// GetStackResources returns the resource list recorded for the stack, or
// ErrNotFound when no job has reported one.
func (s *Store) GetStackResources(ctx context.Context, stackID uuid.UUID) (StackResources, error) {
	out, err := queryOne[StackResources](ctx, s.db, `
		SELECT stack_id, run_id, run_mode, sha, observed_at, reported_at, changed_at, total, truncated, resources
		FROM stack_resources WHERE stack_id = $1`, stackID)
	return out, wrap("get stack resources", err)
}

// ResourceChange is the applied run whose plan last changed one address
// of a stack.
type ResourceChange struct {
	Address  string            `db:"address"`
	Action   v1.ResourceAction `db:"action"`
	RunID    uuid.UUID         `db:"run_id"`
	SHA      string            `db:"sha"`
	PRNumber int               `db:"pr_number"`
	// FinishedAt is when the stack's apply in that run finished.
	FinishedAt *time.Time `db:"finished_at"`
}

// ToV1 converts the row to the last change of a listed resource.
func (c ResourceChange) ToV1() v1.ResourceChange {
	return v1.ResourceChange{
		Action:     c.Action,
		RunID:      c.RunID.String(),
		SHA:        c.SHA,
		PRNumber:   c.PRNumber,
		FinishedAt: utc(c.FinishedAt),
	}
}

// StackResourceChanges returns, for those of addresses an applied run of
// the stack changed, the newest such run and what its plan summary says it
// did, derived from the summaries of the stack's applied rows, which are
// never pruned. Rows of an apply that was skipped, a no-op, failed or
// unconfirmed never count, nor do imports and moves, which summaries do
// not list. When a run both destroyed an address and created, updated or
// replaced it, the latter wins; when the newest change destroyed it, the
// address has no entry, since what changed it since is unknown.
func (s *Store) StackResourceChanges(ctx context.Context, stackID uuid.UUID, addresses []string) ([]ResourceChange, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	out, err := queryAll[ResourceChange](ctx, s.db, `
		SELECT address, action, run_id, sha, pr_number, finished_at FROM (
			SELECT DISTINCT ON (a.address) a.address, a.action, rs.run_id, r.sha, r.pr_number, rs.finished_at
			FROM run_stacks rs
			JOIN runs r ON r.id = rs.run_id
			CROSS JOIN LATERAL (
				SELECT x, 'create', 0 FROM jsonb_array_elements_text(rs.summary->'added') x
				UNION ALL SELECT x, 'update', 1 FROM jsonb_array_elements_text(rs.summary->'changed') x
				UNION ALL SELECT x, 'replace', 2 FROM jsonb_array_elements_text(rs.summary->'replaced') x
				UNION ALL SELECT x, 'destroy', 3 FROM jsonb_array_elements_text(rs.summary->'destroyed') x
			) a (address, action, ord)
			WHERE rs.stack_id = $1 AND rs.mode = 'apply' AND rs.status = 'applied'
			  AND a.address = ANY($2::text[])
			ORDER BY a.address, r.created_at DESC, r.id DESC, a.ord
		) latest
		WHERE action <> 'destroy'
		ORDER BY address`, stackID, addresses)
	return out, wrap("stack resource changes", err)
}
