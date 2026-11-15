package store

import (
	"context"
	"time"
)

// Retention says how long each kind of history is kept. A zero duration
// disables pruning of that kind.
type Retention struct {
	// PlanText is how long stored plan text is kept after its row was last
	// updated; summaries and run history are kept indefinitely.
	PlanText time.Duration
	// Events is how long webhook events and completed jobs are kept.
	Events time.Duration
	// Drift is how long drift history is kept; the newest observation of
	// every stack is always kept.
	Drift time.Duration
}

// DefaultRetention matches the defaults of STACKORDER_PLAN_TEXT_RETENTION,
// STACKORDER_EVENT_RETENTION and STACKORDER_DRIFT_RETENTION.
var DefaultRetention = Retention{
	PlanText: 720 * time.Hour,
	Events:   168 * time.Hour,
	Drift:    2160 * time.Hour,
}

// PruneResult counts the rows each retention step removed or cleared.
type PruneResult struct {
	PlanText int64
	Events   int64
	Jobs     int64
	Drift    int64
	JTIs     int64
	Sessions int64
}

// PrunePlanText clears the plan text of stack rows last updated more than
// olderThan ago, keeping their summaries, and returns how many were
// cleared. A row that kept its full plan text in the artifact store loses
// its plan_url in the same statement and its objects are queued for
// removal, so the link is never served for an object about to be deleted.
// The rows are locked with FOR UPDATE SKIP LOCKED so concurrent instances
// clear disjoint batches.
func (s *Store) PrunePlanText(ctx context.Context, olderThan time.Duration) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `
		WITH expired AS (
			SELECT rs.run_id, rs.stack_id, s.key AS stack_key, rs.plan_url
			FROM run_stacks rs JOIN stacks s ON s.id = rs.stack_id
			WHERE rs.plan_text IS NOT NULL
			  AND rs.updated_at < now() - $1::bigint * interval '1 microsecond'
			FOR UPDATE OF rs SKIP LOCKED
		),
		cleared AS (
			UPDATE run_stacks rs SET plan_text = NULL, plan_url = ''
			FROM expired e WHERE rs.run_id = e.run_id AND rs.stack_id = e.stack_id
		),
		queued AS (
			INSERT INTO object_deletions (run_id, stack_key)
			SELECT run_id, stack_key FROM expired WHERE plan_url <> ''
			ON CONFLICT (run_id, stack_key) DO NOTHING
		)
		SELECT count(*) FROM expired`, micros(olderThan)).Scan(&n)
	if err != nil {
		return 0, wrap("prune plan text", err)
	}
	return n, nil
}

// Prune applies a retention policy and removes expired OIDC token ids and
// sessions. Each step commits on its own so a large backlog does not hold
// one long transaction.
func (s *Store) Prune(ctx context.Context, r Retention) (PruneResult, error) {
	var (
		out PruneResult
		err error
	)
	if r.PlanText > 0 {
		if out.PlanText, err = s.PrunePlanText(ctx, r.PlanText); err != nil {
			return out, err
		}
	}
	if r.Events > 0 {
		if out.Events, out.Jobs, err = s.PruneQueue(ctx, r.Events); err != nil {
			return out, err
		}
	}
	if r.Drift > 0 {
		if out.Drift, err = s.PruneDrift(ctx, r.Drift); err != nil {
			return out, err
		}
	}
	if out.JTIs, err = s.PruneJTIs(ctx); err != nil {
		return out, err
	}
	out.Sessions, err = s.PruneSessions(ctx)
	return out, err
}
