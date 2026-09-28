package runs

import "time"

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
