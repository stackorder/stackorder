package v1

// ApplyMode says when applies happen relative to the merge.
type ApplyMode string

const (
	ApplyBeforeMerge ApplyMode = "before_merge"
	ApplyOnMerge     ApplyMode = "on_merge"
)

// CrossRepoMode says what happens to external dependents after an apply.
type CrossRepoMode string

const (
	CrossRepoOff  CrossRepoMode = "off"
	CrossRepoPlan CrossRepoMode = "plan"
)

// PlanOutput says how much of a plan reaches the server and the PR comment.
type PlanOutput string

const (
	PlanOutputFull    PlanOutput = "full"
	PlanOutputSummary PlanOutput = "summary"
)

// RepoConfig is the root stackorder.yaml. Field defaults are applied by
// internal/config; the zero value is not valid on its own.
type RepoConfig struct {
	Version      int               `json:"version" yaml:"version"`
	Stacks       StacksConfig      `json:"stacks" yaml:"stacks"`
	Modules      ModulesConfig     `json:"modules" yaml:"modules"`
	Tool         Tool              `json:"tool" yaml:"tool"`
	ToolVersion  string            `json:"tool_version,omitempty" yaml:"tool_version"`
	Environments map[string]string `json:"environments,omitempty" yaml:"environments"`
	Apply        ApplyConfig       `json:"apply" yaml:"apply"`
	Propagate    PropagateConfig   `json:"propagate" yaml:"propagate"`
	Drift        DriftConfig       `json:"drift" yaml:"drift"`
	PlanOutput   PlanOutput        `json:"plan_output" yaml:"plan_output"`
}

// StacksConfig controls stack discovery.
type StacksConfig struct {
	// Discover lists directory globs; a match is a stack when it contains a
	// terraform block with an s3 backend.
	Discover []string `json:"discover" yaml:"discover"`
	// Include lists directories that are stacks regardless of discovery.
	Include []string `json:"include,omitempty" yaml:"include"`
	// Ignore lists file globs whose changes never affect a stack.
	Ignore []string `json:"ignore" yaml:"ignore"`
	// IgnoreLockfile adds .terraform.lock.hcl to Ignore.
	IgnoreLockfile bool `json:"ignore_lockfile,omitempty" yaml:"ignore_lockfile"`
}

// ModulesConfig controls local module discovery.
type ModulesConfig struct {
	Paths []string `json:"paths" yaml:"paths"`
}

// ApplyConfig is the apply gate policy.
type ApplyConfig struct {
	Mode                   ApplyMode `json:"mode" yaml:"mode"`
	RequireApprovals       int       `json:"require_approvals" yaml:"require_approvals"`
	RequireCodeownerReview bool      `json:"require_codeowner_review,omitempty" yaml:"require_codeowner_review"`
	AllowedTeams           []string  `json:"allowed_teams,omitempty" yaml:"allowed_teams"`
	FourEyes               bool      `json:"four_eyes,omitempty" yaml:"four_eyes"`
	FromPlan               *bool     `json:"from_plan,omitempty" yaml:"from_plan"`
	MaxParallel            int       `json:"max_parallel" yaml:"max_parallel"`
}

// FromPlanEnabled returns FromPlan with its default of true.
func (a ApplyConfig) FromPlanEnabled() bool { return a.FromPlan == nil || *a.FromPlan }

// PropagateConfig controls how changes ripple through the graph.
type PropagateConfig struct {
	Dependents *bool         `json:"dependents,omitempty" yaml:"dependents"`
	CrossRepo  CrossRepoMode `json:"cross_repo" yaml:"cross_repo"`
}

// DependentsEnabled returns Dependents with its default of true.
func (p PropagateConfig) DependentsEnabled() bool { return p.Dependents == nil || *p.Dependents }

// DriftConfig controls scheduled drift detection.
type DriftConfig struct {
	// Schedule is a five field cron expression; empty disables drift runs.
	Schedule  string `json:"schedule,omitempty" yaml:"schedule"`
	OpenIssue bool   `json:"open_issue,omitempty" yaml:"open_issue"`
}

// StackConfig is a per-stack .stackorder.yaml.
type StackConfig struct {
	// DependsOn lists stack keys, optionally qualified with "owner/repo//".
	DependsOn      []string          `json:"depends_on,omitempty" yaml:"depends_on"`
	Workspace      string            `json:"workspace,omitempty" yaml:"workspace"`
	Tool           Tool              `json:"tool,omitempty" yaml:"tool"`
	ToolVersion    string            `json:"tool_version,omitempty" yaml:"tool_version"`
	Environment    string            `json:"environment,omitempty" yaml:"environment"`
	Apply          *StackApplyConfig `json:"apply,omitempty" yaml:"apply"`
	PlanOutput     PlanOutput        `json:"plan_output,omitempty" yaml:"plan_output"`
	IgnoreInferred []string          `json:"ignore_inferred,omitempty" yaml:"ignore_inferred"`
}

// StackApplyConfig narrows the apply policy for one stack.
type StackApplyConfig struct {
	AllowedTeams []string `json:"allowed_teams,omitempty" yaml:"allowed_teams"`
}
