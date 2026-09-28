package runs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

// DispatchWaveJob is the payload of JobDispatchWave.
type DispatchWaveJob struct {
	RunID string `json:"run_id"`
	Wave  int    `json:"wave"`
}

// DriftJob is the payload of JobDrift.
type DriftJob struct {
	RepoID  int64  `json:"repo_id"`
	StackID string `json:"stack_id"`
}

// ScheduleDriftJob is the payload of JobScheduleDrift.
type ScheduleDriftJob struct {
	RepoID int64 `json:"repo_id"`
}

// CrossRepoPlanJob is the payload of JobCrossRepoPlan: plan StackKeys of
// the downstream repository Repo after UpstreamRunID applied.
type CrossRepoPlanJob struct {
	Repo          string   `json:"repo"`
	StackKeys     []string `json:"stack_keys"`
	UpstreamRunID string   `json:"upstream_run_id"`
}

// ReconcileJob is the payload of JobReconcile.
type ReconcileJob struct{}

// PruneJob is the payload of JobPrune. Zero durations take
// store.DefaultRetention.
type PruneJob struct {
	PlanText time.Duration `json:"plan_text,omitempty"`
	Events   time.Duration `json:"events,omitempty"`
	Drift    time.Duration `json:"drift,omitempty"`
}

// StaleLocksJob is the payload of JobStaleLocks.
type StaleLocksJob struct{}

// SyncInstallationsJob is the payload of JobSyncInstallations.
type SyncInstallationsJob struct{}

// HandleJob decodes a queued job by kind and runs it.
func (s *Service) HandleJob(ctx context.Context, kind string, payload json.RawMessage) error {
	decode := func(v any) error {
		if len(payload) == 0 {
			return nil
		}
		if err := json.Unmarshal(payload, v); err != nil {
			return principal.Wrap(principal.ErrInvalid, "%s job payload: %v", kind, err)
		}
		return nil
	}
	switch kind {
	case JobDispatchWave:
		var j DispatchWaveJob
		if err := decode(&j); err != nil {
			return err
		}
		return s.DispatchWave(ctx, j.RunID, j.Wave)
	case JobDrift:
		var j DriftJob
		if err := decode(&j); err != nil {
			return err
		}
		return s.RunDriftStack(ctx, j.RepoID, j.StackID)
	case JobScheduleDrift:
		var j ScheduleDriftJob
		if err := decode(&j); err != nil {
			return err
		}
		if s.enqueue == nil {
			return errors.New("runs: schedule drift: no job queue is configured")
		}
		return s.ScheduleDrift(ctx, j.RepoID, s.enqueue)
	case JobCrossRepoPlan:
		var j CrossRepoPlanJob
		if err := decode(&j); err != nil {
			return err
		}
		return s.RunCrossRepoPlan(ctx, j)
	case JobReconcile:
		return s.Reconcile(ctx)
	case JobPrune:
		var j PruneJob
		if err := decode(&j); err != nil {
			return err
		}
		r := store.DefaultRetention
		if j.PlanText > 0 {
			r.PlanText = j.PlanText
		}
		if j.Events > 0 {
			r.Events = j.Events
		}
		if j.Drift > 0 {
			r.Drift = j.Drift
		}
		return s.Prune(ctx, r.PlanText, r.Events, r.Drift)
	case JobStaleLocks:
		return s.RemindStaleLocks(ctx)
	case JobSyncInstallations:
		return s.SyncInstallations(ctx)
	}
	return principal.Wrap(principal.ErrInvalid, "unknown job kind %q", kind)
}
