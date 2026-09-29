package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// NodeKind distinguishes the two node types of the dependency graph.
type NodeKind string

const (
	NodeStack  NodeKind = "stack"
	NodeModule NodeKind = "module"
)

// ModuleKind says where a module's source lives.
type ModuleKind string

const (
	ModuleLocal    ModuleKind = "local"
	ModuleGit      ModuleKind = "git"
	ModuleRegistry ModuleKind = "registry"
)

// EdgeType is one of the three edge kinds of the dependency graph.
type EdgeType string

const (
	// EdgeDependsOn is an explicit stack to stack edge from .stackorder.yaml.
	// It orders applies and propagates change.
	EdgeDependsOn EdgeType = "depends_on"
	// EdgeUsesModule is a stack or module to module edge parsed from module
	// blocks. It propagates change but does not order applies.
	EdgeUsesModule EdgeType = "uses_module"
	// EdgeReadsState is a stack to stack edge inferred from
	// terraform_remote_state data sources. It orders applies softly and
	// propagates change.
	EdgeReadsState EdgeType = "reads_state"
)

// Tool is the Terraform-compatible binary a stack is run with.
type Tool string

const (
	ToolTerraform Tool = "terraform"
	ToolTofu      Tool = "tofu"
)

// RunMode is what a run does to its stacks.
type RunMode string

const (
	ModePlan  RunMode = "plan"
	ModeApply RunMode = "apply"
	ModeDrift RunMode = "drift"
)

// Trigger records what started a run.
type Trigger string

const (
	TriggerPullRequest Trigger = "pull_request"
	TriggerComment     Trigger = "comment"
	TriggerPush        Trigger = "push"
	TriggerSchedule    Trigger = "schedule"
	TriggerRerequest   Trigger = "rerequest"
	TriggerManual      Trigger = "manual"
)

// RunStatus is the state of a run as a whole.
type RunStatus string

const (
	RunPending     RunStatus = "pending"
	RunPlanning    RunStatus = "planning"
	RunPlanned     RunStatus = "planned"
	RunApplying    RunStatus = "applying"
	RunApplied     RunStatus = "applied"
	RunFailed      RunStatus = "failed"
	RunUnconfirmed RunStatus = "unconfirmed"
	RunSuperseded  RunStatus = "superseded"
)

// Terminal reports whether no further transitions are expected.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunApplied, RunFailed, RunUnconfirmed, RunSuperseded:
		return true
	}
	return false
}

// StackStatus is the state of one stack inside a run.
type StackStatus string

const (
	StackPending     StackStatus = "pending"
	StackPlanning    StackStatus = "planning"
	StackPlanned     StackStatus = "planned"
	StackApplying    StackStatus = "applying"
	StackApplied     StackStatus = "applied"
	StackFailed      StackStatus = "failed"
	StackBlocked     StackStatus = "blocked"
	StackNoop        StackStatus = "noop"
	StackUnconfirmed StackStatus = "unconfirmed"
	StackUnknown     StackStatus = "unknown"
	StackSkipped     StackStatus = "skipped"
)

// Terminal reports whether the stack has finished for this run.
func (s StackStatus) Terminal() bool {
	switch s {
	case StackApplied, StackFailed, StackBlocked, StackNoop, StackUnconfirmed, StackUnknown, StackSkipped:
		return true
	}
	return false
}

// ResultStatus is what the CLI reports for one plan, apply or drift execution.
type ResultStatus string

const (
	ResultSuccess ResultStatus = "success"
	ResultFailure ResultStatus = "failure"
	ResultError   ResultStatus = "error"
)

// CheckStatus is the verdict of a named policy or cost check.
type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckFail CheckStatus = "fail"
	CheckWarn CheckStatus = "warn"
)

// StackKey builds the in-repository identity of a stack: the path, then
// ":" and the instance name when there is one. An instance of "default"
// counts as none.
func StackKey(path, instance string) string {
	path = strings.Trim(strings.TrimPrefix(path, "./"), "/")
	if instance == "" || instance == "default" {
		return path
	}
	return path + ":" + instance
}

// SplitStackKey is the inverse of StackKey: it splits "path:instance" at
// the last ":" and returns an empty instance for a key without a suffix.
func SplitStackKey(key string) (path, instance string) {
	if i := strings.LastIndex(key, ":"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// QualifiedStackKey builds the cross-repository identity "owner/repo//key".
func QualifiedStackKey(repo, key string) string {
	return repo + "//" + key
}

// SplitQualifiedStackKey splits "owner/repo//key" into its parts. A key with
// no repository part returns repo == "".
func SplitQualifiedStackKey(qualified string) (repo, key string) {
	if i := strings.Index(qualified, "//"); i >= 0 {
		return qualified[:i], qualified[i+2:]
	}
	return "", qualified
}

// Backend describes where a stack keeps its state.
type Backend struct {
	Type          string `json:"type"`
	Bucket        string `json:"bucket,omitempty"`
	Key           string `json:"key,omitempty"`
	Region        string `json:"region,omitempty"`
	DynamoDBTable string `json:"dynamodb_table,omitempty"`
	UseLockfile   bool   `json:"use_lockfile,omitempty"`
	// WorkspaceKeyPrefix is the S3 backend's workspace_key_prefix, defaulting
	// to "env:" when workspaces are used.
	WorkspaceKeyPrefix string `json:"workspace_key_prefix,omitempty"`
}

// Stack is a node of the graph that Terraform runs in: one instance of a
// stack directory.
type Stack struct {
	// Key is StackKey(Path, Instance).
	Key string `json:"key"`
	// Path is the repository relative directory, slash separated, no leading
	// "./" and no trailing "/".
	Path string `json:"path"`
	// Instance names the instance of the directory; empty for a directory
	// with a single unnamed instance.
	Instance string `json:"instance,omitempty"`
	// Workspace is the Terraform workspace selected after init; empty means
	// none.
	Workspace string `json:"workspace,omitempty"`
	// Repo is "owner/repo". Empty means the repository the graph belongs to.
	Repo        string   `json:"repo,omitempty"`
	Backend     *Backend `json:"backend,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Tool        Tool     `json:"tool,omitempty"`
	ToolVersion string   `json:"tool_version,omitempty"`
	PlanOutput  string   `json:"plan_output,omitempty"`
	// Config is the raw per-stack .stackorder.yaml, when present.
	Config *StackConfig `json:"config,omitempty"`
	// External marks a stack referenced by a cross-repo depends_on edge that
	// is not part of this graph's repository.
	External bool `json:"external,omitempty"`
	// WatchPaths lists repository relative files outside the stack
	// directory that the stack reads at init or plan, sorted and unique.
	WatchPaths []string `json:"watch_paths,omitempty"`
}

// Module is a node of the graph that stacks and other modules consume.
type Module struct {
	Key  string     `json:"key"`
	Kind ModuleKind `json:"kind"`
	// Path is the repository relative directory for local modules.
	Path string `json:"path,omitempty"`
	// Source is the raw source string as written in the module block.
	Source string `json:"source"`
	// Ref is the git ref or registry version pinned by the consumer.
	Ref string `json:"ref,omitempty"`
}

// NodeRef points at a node by kind and key.
type NodeRef struct {
	Kind NodeKind `json:"kind"`
	Key  string   `json:"key"`
}

// StackRef builds a NodeRef for a stack key.
func StackRef(key string) NodeRef { return NodeRef{Kind: NodeStack, Key: key} }

// ModuleRef builds a NodeRef for a module key.
func ModuleRef(key string) NodeRef { return NodeRef{Kind: NodeModule, Key: key} }

// Edge connects two nodes.
type Edge struct {
	From     NodeRef  `json:"from"`
	To       NodeRef  `json:"to"`
	Type     EdgeType `json:"type"`
	Inferred bool     `json:"inferred,omitempty"`
	// Meta carries the module ref for uses_module edges and the matched
	// bucket and key for reads_state edges.
	Meta map[string]string `json:"meta,omitempty"`
}

// Graph is the dependency graph of one repository at one commit. The runner
// builds it; the server stores it and resolves against it.
type Graph struct {
	Repo     string   `json:"repo"`
	SHA      string   `json:"sha"`
	TreeHash string   `json:"tree_hash,omitempty"`
	Stacks   []Stack  `json:"stacks"`
	Modules  []Module `json:"modules"`
	Edges    []Edge   `json:"edges"`
	Warnings []string `json:"warnings,omitempty"`
}

// Reason says why a stack is in the affected set. A stack's reasons are
// listed in the order the constants are declared.
type Reason string

const (
	ReasonChanged    Reason = "changed"
	ReasonWatchPath  Reason = "watch_path"
	ReasonModule     Reason = "module"
	ReasonReadsState Reason = "reads_state"
	ReasonDependent  Reason = "dependent"
	ReasonRequested  Reason = "requested"
)

// AffectedStack is one entry of a resolution result.
type AffectedStack struct {
	Key         string   `json:"key"`
	Path        string   `json:"path"`
	Instance    string   `json:"instance,omitempty"`
	Workspace   string   `json:"workspace,omitempty"`
	Wave        int      `json:"wave"`
	Reasons     []Reason `json:"reasons"`
	Environment string   `json:"environment,omitempty"`
	Tool        Tool     `json:"tool,omitempty"`
	ToolVersion string   `json:"tool_version,omitempty"`
	PlanOutput  string   `json:"plan_output,omitempty"`
	// Via lists the node keys through which the change reached this stack.
	Via []string `json:"via,omitempty"`
	// Locked names the PR holding an orchestration lock on this stack, if any.
	LockedBy *LockInfo `json:"locked_by,omitempty"`
}

// DefaultEnvironment is the GitHub environment assigned to stacks that match
// no prefix in the environments map. GitHub creates it on first use with no
// protection rules.
const DefaultEnvironment = "default"

// MatrixEntry is one element of the GitHub Actions matrix include list. The
// same shape is passed as the `stacks` input of stackorder-run.yml.
type MatrixEntry struct {
	Stack       string `json:"stack"`
	Key         string `json:"key"`
	Instance    string `json:"instance,omitempty"`
	Workspace   string `json:"workspace"`
	Environment string `json:"environment"`
	Wave        int    `json:"wave"`
	Tool        Tool   `json:"tool"`
	ToolVersion string `json:"tool_version"`
	PlanOutput  string `json:"plan_output"`
	// SHA is the commit the job must check out.
	SHA string `json:"sha,omitempty"`
	// PlanRunID is the Actions workflow run that uploaded the plan artifact,
	// and Artifact its name; both are set for apply dispatches.
	PlanRunID int64  `json:"plan_run_id,omitempty"`
	Artifact  string `json:"artifact,omitempty"`
}

// PlanArtifactName builds the workflow artifact name for a stack's plan file.
func PlanArtifactName(stackKey, sha string) string {
	return "stackorder-plan-" + StackKeySlug(stackKey) + "-" + sha
}

// StackKeySlug names a stack in artifact names and object keys: the key
// with / and : replaced by -, then - and the first 8 hex characters of the
// key's SHA-256, so keys such as a/b and a-b get different slugs.
func StackKeySlug(stackKey string) string {
	sum := sha256.Sum256([]byte(stackKey))
	return strings.NewReplacer("/", "-", ":", "-").Replace(stackKey) + "-" + hex.EncodeToString(sum[:4])
}

// Matrix is the JSON GitHub Actions expects in a strategy.matrix expression.
type Matrix struct {
	Include []MatrixEntry `json:"include"`
}

// CreateRunRequest is the body of POST /v1/runs.
type CreateRunRequest struct {
	Repo     string  `json:"repo"`
	SHA      string  `json:"sha"`
	BaseSHA  string  `json:"base_sha,omitempty"`
	PRNumber int     `json:"pr_number,omitempty"`
	Mode     RunMode `json:"mode"`
	Trigger  Trigger `json:"trigger,omitempty"`
	// WorkflowRunID and Attempt identify the Actions run posting results.
	WorkflowRunID int64 `json:"workflow_run_id,omitempty"`
	Attempt       int   `json:"workflow_run_attempt,omitempty"`
	// RunID is set when the workflow was dispatched by the server for an
	// existing run and the runner is only registering itself.
	RunID string `json:"run_id,omitempty"`
	// Stacks names the stacks of a manual run started with an API key from
	// outside Actions; the server takes their locks before answering.
	Stacks []string `json:"stacks,omitempty"`
}

// CreateRunResponse is the body returned by POST /v1/runs.
type CreateRunResponse struct {
	RunID    string    `json:"run_id"`
	Status   RunStatus `json:"status"`
	Existing bool      `json:"existing"`
	Run      *Run      `json:"run,omitempty"`
}

// GraphUploadRequest is the body of POST /v1/runs/{id}/graph.
type GraphUploadRequest struct {
	Graph        Graph       `json:"graph"`
	ChangedPaths []string    `json:"changed_paths"`
	BaseSHA      string      `json:"base_sha,omitempty"`
	Config       *RepoConfig `json:"config,omitempty"`
	// Stacks restricts the run to these stack keys, for a
	// "stackorder plan stacks/a stacks/b" comment.
	Stacks []string `json:"stacks,omitempty"`
}

// ResolveResponse is what the server returns from a graph upload and what the
// CLI computes locally when the server is unreachable.
type ResolveResponse struct {
	RunID    string          `json:"run_id,omitempty"`
	Affected []AffectedStack `json:"affected"`
	// Waves lists stack keys by wave index.
	Waves    [][]string `json:"waves"`
	Warnings []string   `json:"warnings,omitempty"`
	// Cycles is non-empty when resolution failed; each entry spells one cycle.
	Cycles [][]string `json:"cycles,omitempty"`
	// External lists cross-repo dependents recorded on the run.
	External []string `json:"external,omitempty"`
	Matrix   Matrix   `json:"matrix"`
	// Cached is true when the tree hash matched a stored graph.
	Cached bool `json:"cached"`
	// Unconfirmed is true when the CLI produced this locally.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}

// PlanSummary is the redacted, size independent digest of a plan.
type PlanSummary struct {
	Adds     int `json:"adds"`
	Changes  int `json:"changes"`
	Destroys int `json:"destroys"`
	Replaces int `json:"replaces"`
	// Imports and Moves are reported when the tool supports them.
	Imports int `json:"imports,omitempty"`
	Moves   int `json:"moves,omitempty"`
	// Addresses lists resource addresses by action.
	Added     []string `json:"added,omitempty"`
	Changed   []string `json:"changed,omitempty"`
	Destroyed []string `json:"destroyed,omitempty"`
	Replaced  []string `json:"replaced,omitempty"`
	// OutputChanges counts changed outputs.
	OutputChanges int `json:"output_changes,omitempty"`
}

// Total returns the number of resource actions in the summary.
func (s PlanSummary) Total() int {
	return s.Adds + s.Changes + s.Destroys + s.Replaces + s.Imports + s.Moves
}

// Empty reports whether the plan is a no-op.
func (s PlanSummary) Empty() bool { return s.Total() == 0 && s.OutputChanges == 0 }

// StackResult is the body of POST /v1/runs/{id}/stacks/{key}/result.
type StackResult struct {
	Mode        RunMode      `json:"mode"`
	Status      ResultStatus `json:"status"`
	ExitCode    int          `json:"exit_code"`
	HasChanges  bool         `json:"has_changes"`
	Summary     *PlanSummary `json:"summary,omitempty"`
	PlanText    string       `json:"plan_text,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
	ErrorText   string       `json:"error_text,omitempty"`
	Artifact    string       `json:"plan_artifact,omitempty"`
	JobURL      string       `json:"job_url,omitempty"`
	Tool        Tool         `json:"tool,omitempty"`
	ToolVersion string       `json:"tool_version,omitempty"`
	DurationMS  int64        `json:"duration_ms,omitempty"`
	// Backend is reported so the UI can link a stack to its state object.
	Backend *Backend `json:"backend,omitempty"`
	// Unconfirmed is set when the CLI could not reach the server during the
	// run and is reporting late.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}

// CheckVerdict is the body of POST /v1/runs/{id}/stacks/{key}/checks/{name}.
type CheckVerdict struct {
	Status     CheckStatus `json:"status"`
	Summary    string      `json:"summary,omitempty"`
	Details    string      `json:"details,omitempty"`
	DetailsURL string      `json:"details_url,omitempty"`
}

// Check is a stored check verdict.
type Check struct {
	Name       string      `json:"name"`
	Status     CheckStatus `json:"status"`
	Summary    string      `json:"summary,omitempty"`
	DetailsURL string      `json:"details_url,omitempty"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

// LockInfo describes an orchestration lock.
type LockInfo struct {
	StackID  string    `json:"stack_id,omitempty"`
	StackKey string    `json:"stack_key,omitempty"`
	RunID    string    `json:"run_id"`
	PRNumber int       `json:"pr_number,omitempty"`
	TakenAt  time.Time `json:"taken_at"`
	Reason   string    `json:"reason,omitempty"`
}

// Run is the human and automation view of a run.
type Run struct {
	ID          string     `json:"id"`
	Repo        string     `json:"repo"`
	SHA         string     `json:"sha"`
	BaseSHA     string     `json:"base_sha,omitempty"`
	PRNumber    int        `json:"pr_number,omitempty"`
	Trigger     Trigger    `json:"trigger"`
	Mode        RunMode    `json:"mode"`
	Status      RunStatus  `json:"status"`
	RequestedBy string     `json:"requested_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Waves       int        `json:"waves"`
	CurrentWave int        `json:"current_wave"`
	Stacks      []RunStack `json:"stacks,omitempty"`
	Warnings    []string   `json:"warnings,omitempty"`
	HTMLURL     string     `json:"html_url,omitempty"`
}

// RunStack is one stack's row inside a run.
type RunStack struct {
	StackID      string       `json:"stack_id"`
	Key          string       `json:"key"`
	Path         string       `json:"path"`
	Instance     string       `json:"instance,omitempty"`
	Workspace    string       `json:"workspace,omitempty"`
	Environment  string       `json:"environment,omitempty"`
	Wave         int          `json:"wave"`
	Status       StackStatus  `json:"status"`
	Reasons      []Reason     `json:"reasons,omitempty"`
	Summary      *PlanSummary `json:"summary,omitempty"`
	ExitCode     *int         `json:"exit_code,omitempty"`
	JobURL       string       `json:"job_url,omitempty"`
	PlanArtifact string       `json:"plan_artifact,omitempty"`
	PlanText     string       `json:"plan_text,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
	Checks       []Check      `json:"checks,omitempty"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	FinishedAt   *time.Time   `json:"finished_at,omitempty"`
	// BlockedBy lists the keys of the failed predecessors that put a
	// blocked stack in that state.
	BlockedBy []string `json:"blocked_by,omitempty"`
	// PlanOutput is the stack's effective plan_output; "summary" means plan
	// text must never be rendered for it.
	PlanOutput string `json:"plan_output,omitempty"`
	// PlanURL links to the full plan text when the server keeps it in its
	// artifact bucket; PlanText then holds only the beginning.
	PlanURL string `json:"plan_url,omitempty"`
	// Lock is the orchestration lock on the stack now, whichever run holds
	// it; an apply job applies only while its own run holds it.
	Lock *LockInfo `json:"lock,omitempty"`
}

// StackDetail is the body of GET /v1/stacks/{id}.
type StackDetail struct {
	ID          string          `json:"id"`
	Repo        string          `json:"repo"`
	Key         string          `json:"key"`
	Path        string          `json:"path"`
	Instance    string          `json:"instance,omitempty"`
	Workspace   string          `json:"workspace,omitempty"`
	Environment string          `json:"environment,omitempty"`
	Backend     *Backend        `json:"backend,omitempty"`
	Tool        Tool            `json:"tool,omitempty"`
	LastApply   *RunStackRef    `json:"last_apply,omitempty"`
	LastPlan    *RunStackRef    `json:"last_plan,omitempty"`
	Drift       *DriftStatus    `json:"drift,omitempty"`
	Lock        *LockInfo       `json:"lock,omitempty"`
	DependsOn   []string        `json:"depends_on,omitempty"`
	Dependents  []string        `json:"dependents,omitempty"`
	Modules     []ModuleConsume `json:"modules,omitempty"`
}

// RunStackRef points at one stack row in one run.
type RunStackRef struct {
	RunID      string       `json:"run_id"`
	SHA        string       `json:"sha"`
	PRNumber   int          `json:"pr_number,omitempty"`
	Status     StackStatus  `json:"status"`
	Summary    *PlanSummary `json:"summary,omitempty"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	JobURL     string       `json:"job_url,omitempty"`
}

// DriftStatus is the latest drift observation for a stack.
type DriftStatus struct {
	CheckedAt   time.Time    `json:"checked_at"`
	Drifted     bool         `json:"drifted"`
	Summary     *PlanSummary `json:"summary,omitempty"`
	IssueNumber int          `json:"issue_number,omitempty"`
	IssueURL    string       `json:"issue_url,omitempty"`
}

// ModuleConsume records that a stack pins a module at a ref.
type ModuleConsume struct {
	ModuleKey string `json:"module_key"`
	Ref       string `json:"ref,omitempty"`
	Latest    string `json:"latest,omitempty"`
	// Behind is the number of released versions newer than Ref, when known.
	Behind int `json:"behind,omitempty"`
}

// ModuleDetail is the body of GET /v1/modules/{id}.
type ModuleDetail struct {
	ID     string     `json:"id"`
	Key    string     `json:"key"`
	Kind   ModuleKind `json:"kind"`
	Source string     `json:"source"`
	// Latest is the newest stable version by semver, else the newest pre-release.
	Latest    string           `json:"latest,omitempty"`
	Versions  []ModuleVersion  `json:"versions,omitempty"`
	Consumers []ModuleConsumer `json:"consumers,omitempty"`
}

// ModuleVersion is one released version of a git module.
type ModuleVersion struct {
	Version  string    `json:"version"`
	SHA      string    `json:"sha,omitempty"`
	TaggedAt time.Time `json:"tagged_at"`
}

// ModuleConsumer is a stack that uses a module.
type ModuleConsumer struct {
	StackID  string `json:"stack_id"`
	Repo     string `json:"repo"`
	StackKey string `json:"stack_key"`
	Ref      string `json:"ref,omitempty"`
	Behind   int    `json:"behind,omitempty"`
}

// GraphView is the body of GET /v1/repos/{owner}/{repo}/graph.
type GraphView struct {
	Repo  string `json:"repo"`
	SHA   string `json:"sha"`
	Graph Graph  `json:"graph"`
	// StackIDs maps stack keys to server stack ids.
	StackIDs map[string]string `json:"stack_ids,omitempty"`
	// Affected and Waves replay a run's resolution when ?run= is given.
	Affected []AffectedStack `json:"affected,omitempty"`
	Waves    [][]string      `json:"waves,omitempty"`
}

// Overview is the body of GET /v1/overview.
type Overview struct {
	Repos          int                 `json:"repos"`
	Stacks         int                 `json:"stacks"`
	Drifted        int                 `json:"drifted"`
	LocksHeld      int                 `json:"locks_held"`
	RunsByStatus   map[RunStatus]int   `json:"runs_by_status"`
	StacksByStatus map[StackStatus]int `json:"stacks_by_status"`
	RecentRuns     []Run               `json:"recent_runs,omitempty"`
}

// RepoSummary is one row of GET /v1/repos.
type RepoSummary struct {
	ID            int64      `json:"id"`
	FullName      string     `json:"full_name"`
	DefaultBranch string     `json:"default_branch"`
	Stacks        int        `json:"stacks"`
	Drifted       int        `json:"drifted"`
	LocksHeld     int        `json:"locks_held"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
}

// UnlockRequest is the body of POST /v1/stacks/{id}/unlock and of
// POST /v1/unlock, which addresses the stack by repository and key instead.
type UnlockRequest struct {
	Repo       string `json:"repo,omitempty"`
	StackKey   string `json:"stack_key,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ForceState bool   `json:"force_state,omitempty"`
}

// UnlockResponse is returned by the unlock endpoints.
type UnlockResponse struct {
	Released []LockInfo `json:"released"`
}

// Error is the body of every non-2xx response.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Page wraps list responses.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Total      int    `json:"total,omitempty"`
}

// Whoami is the body of GET /v1/me.
type Whoami struct {
	Login     string   `json:"login"`
	AvatarURL string   `json:"avatar_url,omitempty"`
	Orgs      []string `json:"orgs"`
	Admin     bool     `json:"admin,omitempty"`
}

// AuditEntry is one audited action, such as an unlock or a re-run, as
// listed by GET /v1/audit.
type AuditEntry struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	// Target names what the action was applied to, such as a stack or a
	// run.
	Target  string         `json:"target,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}
