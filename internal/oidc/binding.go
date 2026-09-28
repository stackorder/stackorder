package oidc

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	eventPullRequest      = "pull_request"
	eventWorkflowDispatch = "workflow_dispatch"
)

// ErrBinding reports a verified token whose claims do not match the run the
// server expects. Callers should answer with forbidden rather than
// unauthorized: the token is genuine but was issued to another job.
type ErrBinding struct {
	// Reason names the claim that did not match, e.g. "sha" or
	// "environment".
	Reason string
	// Want is the value the server expected; for job_workflow_ref it is the
	// pattern.
	Want string
	// Got is the value carried by the token.
	Got string
}

// Error implements error.
func (e *ErrBinding) Error() string {
	return fmt.Sprintf("oidc: claim %s is %q, want %q", e.Reason, e.Got, e.Want)
}

// Is reports whether target is an *ErrBinding with the same Reason, or with
// an empty Reason, which matches every binding error.
func (e *ErrBinding) Is(target error) bool {
	t, ok := target.(*ErrBinding)
	return ok && (t.Reason == "" || t.Reason == e.Reason)
}

// PlanBinding is what the server expects of a token posted by a plan job of
// stackorder-plan.yml.
type PlanBinding struct {
	// Repository is the "owner/repo" of the run.
	Repository string
	// RepositoryID is the GitHub numeric id of Repository, as a string.
	RepositoryID string
	// SHA is the commit of the run; a pull_request token carries the PR
	// head SHA.
	SHA string
	// PRNumber is the pull request the run belongs to.
	PRNumber int
}

// BindPlan checks that c was issued to a pull_request workflow for the PR,
// repository and commit of b: repository (case insensitive), repository_id,
// event_name "pull_request", ref "refs/pull/<PRNumber>/merge" and sha. The
// first mismatch is returned as an *ErrBinding. Every field of b is
// required; a missing one is an error that is not an *ErrBinding.
func BindPlan(c *Claims, b PlanBinding) error {
	if c == nil {
		return errors.New("oidc: plan binding: nil claims")
	}
	if err := requireFields("plan", []field{
		{"Repository", b.Repository != ""}, {"RepositoryID", b.RepositoryID != ""}, {"SHA", b.SHA != ""}, {"PRNumber", b.PRNumber > 0},
	}); err != nil {
		return err
	}
	return firstMismatch([]claimCheck{
		{"repository", c.Repository, b.Repository, true},
		{"repository_id", c.RepositoryID, b.RepositoryID, false},
		{"event_name", c.EventName, eventPullRequest, false},
		{"ref", c.Ref, "refs/pull/" + strconv.Itoa(b.PRNumber) + "/merge", false},
		{"sha", c.SHA, b.SHA, true},
	})
}

// DispatchBinding is what the server expects of a token posted by a job of
// stackorder-run.yml that the server dispatched.
type DispatchBinding struct {
	// Repository is the "owner/repo" of the run.
	Repository string
	// RepositoryID is the GitHub numeric id of Repository, as a string.
	RepositoryID string
	// RunID is the Actions workflow run the server dispatched.
	RunID int64
	// RunAttempt is the attempt of RunID the server expects; zero skips
	// the check.
	RunAttempt int
	// Environment is the GitHub environment the server assigned to the
	// stack; empty skips the check.
	Environment string
	// DefaultBranch is the branch the dispatch ran on, with or without the
	// refs/heads/ prefix.
	DefaultBranch string
	// SHA is the value the sha claim must equal. For workflow_dispatch
	// GitHub sets that claim to the head of DefaultBranch when the run was
	// created, not to the sha input the job checks out; empty skips the
	// check.
	SHA string
}

// BindDispatch checks that c was issued to the workflow_dispatch run the
// server started: repository (case insensitive), repository_id, event_name
// "workflow_dispatch", run_id, ref "refs/heads/<DefaultBranch>", then
// run_attempt when b.RunAttempt is set, environment (case insensitive, as
// GitHub treats environment names) when b.Environment is set and sha when
// b.SHA is set. The first mismatch is
// returned as an *ErrBinding. Repository, RepositoryID, RunID and
// DefaultBranch are required; a missing one is an error that is not an
// *ErrBinding.
func BindDispatch(c *Claims, b DispatchBinding) error {
	if c == nil {
		return errors.New("oidc: dispatch binding: nil claims")
	}
	branch := strings.TrimPrefix(b.DefaultBranch, "refs/heads/")
	if err := requireFields("dispatch", []field{
		{"Repository", b.Repository != ""}, {"RepositoryID", b.RepositoryID != ""}, {"RunID", b.RunID > 0}, {"DefaultBranch", branch != ""},
	}); err != nil {
		return err
	}
	checks := []claimCheck{
		{"repository", c.Repository, b.Repository, true},
		{"repository_id", c.RepositoryID, b.RepositoryID, false},
		{"event_name", c.EventName, eventWorkflowDispatch, false},
		{"run_id", c.RunID, strconv.FormatInt(b.RunID, 10), false},
		{"ref", c.Ref, "refs/heads/" + branch, false},
	}
	if b.RunAttempt != 0 {
		checks = append(checks, claimCheck{"run_attempt", c.RunAttempt, strconv.Itoa(b.RunAttempt), false})
	}
	if b.Environment != "" {
		checks = append(checks, claimCheck{"environment", c.Environment, b.Environment, true})
	}
	if b.SHA != "" {
		checks = append(checks, claimCheck{"sha", c.SHA, b.SHA, true})
	}
	return firstMismatch(checks)
}

// BindWorkflowRef checks job_workflow_ref against pattern with
// MatchWorkflowRef and returns an *ErrBinding with Reason
// "job_workflow_ref" on a mismatch. An empty pattern disables the check,
// which is how an unset STACKORDER_REQUIRED_WORKFLOW_REF behaves.
func BindWorkflowRef(c *Claims, pattern string) error {
	if pattern == "" {
		return nil
	}
	if c == nil {
		return errors.New("oidc: workflow ref binding: nil claims")
	}
	if !MatchWorkflowRef(c.JobWorkflowRef, pattern) {
		return &ErrBinding{Reason: "job_workflow_ref", Want: pattern, Got: c.JobWorkflowRef}
	}
	return nil
}

type claimCheck struct {
	claim, got, want string
	fold             bool
}

func firstMismatch(checks []claimCheck) error {
	for _, ch := range checks {
		if ch.got == ch.want || (ch.fold && strings.EqualFold(ch.got, ch.want)) {
			continue
		}
		return &ErrBinding{Reason: ch.claim, Want: ch.want, Got: ch.got}
	}
	return nil
}

type field struct {
	name    string
	present bool
}

func requireFields(kind string, fields []field) error {
	var missing []string
	for _, f := range fields {
		if !f.present {
			missing = append(missing, f.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("oidc: %s binding is missing %s", kind, strings.Join(missing, ", "))
}
