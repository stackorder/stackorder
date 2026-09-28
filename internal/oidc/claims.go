package oidc

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the payload of a GitHub Actions OIDC token. GitHub encodes every
// custom claim, numeric identifiers included, as a JSON string, and so does
// this type.
type Claims struct {
	jwt.RegisteredClaims

	// Repository is the "owner/repo" the workflow runs in.
	Repository string `json:"repository,omitempty"`
	// RepositoryID is the numeric id of Repository.
	RepositoryID string `json:"repository_id,omitempty"`
	// RepositoryOwner is the owner part of Repository.
	RepositoryOwner string `json:"repository_owner,omitempty"`
	// RepositoryOwnerID is the numeric id of RepositoryOwner.
	RepositoryOwnerID string `json:"repository_owner_id,omitempty"`
	// RunID is the numeric id of the workflow run.
	RunID string `json:"run_id,omitempty"`
	// RunNumber is the per-workflow sequence number of the run.
	RunNumber string `json:"run_number,omitempty"`
	// RunAttempt is the attempt number of the run, starting at 1.
	RunAttempt string `json:"run_attempt,omitempty"`
	// SHA is the commit the workflow run is for.
	SHA string `json:"sha,omitempty"`
	// Ref is the git ref of the run, e.g. refs/pull/7/merge.
	Ref string `json:"ref,omitempty"`
	// RefType is "branch" or "tag".
	RefType string `json:"ref_type,omitempty"`
	// BaseRef is the target branch of a pull request.
	BaseRef string `json:"base_ref,omitempty"`
	// HeadRef is the source branch of a pull request.
	HeadRef string `json:"head_ref,omitempty"`
	// EventName is the event that triggered the run.
	EventName string `json:"event_name,omitempty"`
	// Environment is the GitHub environment the job runs under, if any.
	Environment string `json:"environment,omitempty"`
	// JobWorkflowRef is "owner/repo/path@ref" of the workflow file that
	// defines the job, which is the reusable workflow when one is called.
	JobWorkflowRef string `json:"job_workflow_ref,omitempty"`
	// JobWorkflowSHA is the commit of JobWorkflowRef.
	JobWorkflowSHA string `json:"job_workflow_sha,omitempty"`
	// WorkflowRef is "owner/repo/path@ref" of the calling workflow file.
	WorkflowRef string `json:"workflow_ref,omitempty"`
	// WorkflowSHA is the commit of WorkflowRef.
	WorkflowSHA string `json:"workflow_sha,omitempty"`
	// Workflow is the name of the workflow.
	Workflow string `json:"workflow,omitempty"`
	// Actor is the login of the user that triggered the run.
	Actor string `json:"actor,omitempty"`
	// ActorID is the numeric id of Actor.
	ActorID string `json:"actor_id,omitempty"`
	// RunnerEnvironment is "github-hosted" or "self-hosted".
	RunnerEnvironment string `json:"runner_environment,omitempty"`
	// Enterprise is the enterprise slug, when the owner belongs to one.
	Enterprise string `json:"enterprise,omitempty"`
	// RepositoryVisibility is "public", "private" or "internal".
	RepositoryVisibility string `json:"repository_visibility,omitempty"`
}

// PullRequestNumber returns N when Ref is exactly refs/pull/N/merge with N a
// positive decimal number without leading zeros.
func (c Claims) PullRequestNumber() (int, bool) {
	rest, ok := strings.CutPrefix(c.Ref, "refs/pull/")
	if !ok {
		return 0, false
	}
	digits, ok := strings.CutSuffix(rest, "/merge")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 || strconv.Itoa(n) != digits {
		return 0, false
	}
	return n, true
}

// RunIDInt parses RunID. The error wraps ErrMalformed when RunID is not a
// positive integer.
func (c Claims) RunIDInt() (int64, error) {
	id, err := strconv.ParseInt(c.RunID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: run_id %q: %w", ErrMalformed, c.RunID, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("%w: run_id %q is not positive", ErrMalformed, c.RunID)
	}
	return id, nil
}
