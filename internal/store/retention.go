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
// cleared.
func (s *Store) PrunePlanText(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE run_stacks SET plan_text = NULL
		WHERE plan_text IS NOT NULL AND updated_at < now() - $1::bigint * interval '1 microsecond'`,
		micros(olderThan))
	if err != nil {
		return 0, wrap("prune plan text", err)
	}
	return tag.RowsAffected(), nil
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
