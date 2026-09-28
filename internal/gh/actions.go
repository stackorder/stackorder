package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Workflow run and job statuses.
const (
	RunStatusQueued     = "queued"
	RunStatusInProgress = "in_progress"
	RunStatusCompleted  = "completed"
	RunStatusWaiting    = "waiting"
	RunStatusRequested  = "requested"
	RunStatusPending    = "pending"
)

// Deployment protection rule decisions accepted by
// ReviewDeploymentProtectionRule.
const (
	DeploymentApproved = "approved"
	DeploymentRejected = "rejected"
)

// WorkflowRun is one GitHub Actions workflow run.
type WorkflowRun struct {
	ID           int64            `json:"id"`
	Name         string           `json:"name"`
	DisplayTitle string           `json:"display_title,omitempty"`
	Path         string           `json:"path"`
	WorkflowID   int64            `json:"workflow_id,omitempty"`
	RunNumber    int              `json:"run_number,omitempty"`
	RunAttempt   int              `json:"run_attempt"`
	Event        string           `json:"event"`
	Status       string           `json:"status"`
	Conclusion   string           `json:"conclusion,omitempty"`
	HeadSHA      string           `json:"head_sha"`
	HeadBranch   string           `json:"head_branch,omitempty"`
	HTMLURL      string           `json:"html_url"`
	CreatedAt    time.Time        `json:"created_at,omitzero"`
	UpdatedAt    time.Time        `json:"updated_at,omitzero"`
	RunStartedAt time.Time        `json:"run_started_at,omitzero"`
	PullRequests []PullRequestRef `json:"pull_requests,omitempty"`
}

// WorkflowStep is one step of a workflow job.
type WorkflowStep struct {
	Number      int       `json:"number"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion,omitempty"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
}

// WorkflowJob is one job of a workflow run.
type WorkflowJob struct {
	ID           int64          `json:"id"`
	RunID        int64          `json:"run_id"`
	RunAttempt   int            `json:"run_attempt,omitempty"`
	Name         string         `json:"name"`
	WorkflowName string         `json:"workflow_name,omitempty"`
	HeadSHA      string         `json:"head_sha,omitempty"`
	Status       string         `json:"status"`
	Conclusion   string         `json:"conclusion,omitempty"`
	HTMLURL      string         `json:"html_url"`
	CreatedAt    time.Time      `json:"created_at,omitzero"`
	StartedAt    time.Time      `json:"started_at,omitzero"`
	CompletedAt  time.Time      `json:"completed_at,omitzero"`
	Steps        []WorkflowStep `json:"steps,omitempty"`
	Labels       []string       `json:"labels,omitempty"`
	RunnerName   string         `json:"runner_name,omitempty"`
}

// Artifact is a workflow run artifact's metadata.
type Artifact struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	SizeInBytes int64     `json:"size_in_bytes"`
	Expired     bool      `json:"expired"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
	ExpiresAt   time.Time `json:"expires_at,omitzero"`
}

// Environment identifies a deployment environment.
type Environment struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url,omitempty"`
}

// Reviewer is a user or team allowed to approve a deployment.
type Reviewer struct {
	ID    int64  `json:"id,omitempty"`
	Login string `json:"login,omitempty"`
	Slug  string `json:"slug,omitempty"`
	Name  string `json:"name,omitempty"`
}

// DeploymentReviewer is one entry of a pending deployment's reviewers, of
// Type User or Team.
type DeploymentReviewer struct {
	Type     string   `json:"type"`
	Reviewer Reviewer `json:"reviewer"`
}

// PendingDeployment is an environment a workflow run waits on for approval.
type PendingDeployment struct {
	Environment           Environment          `json:"environment"`
	WaitTimer             int                  `json:"wait_timer"`
	WaitTimerStartedAt    *time.Time           `json:"wait_timer_started_at,omitempty"`
	CurrentUserCanApprove bool                 `json:"current_user_can_approve"`
	Reviewers             []DeploymentReviewer `json:"reviewers"`
}

// ListWorkflowRunsParams filters ListWorkflowRuns. Zero fields do not
// filter.
type ListWorkflowRunsParams struct {
	// Workflow is a workflow file name such as "stackorder-run.yml" or a
	// numeric workflow id.
	Workflow string
	Event    string
	Branch   string
	HeadSHA  string
	// CreatedAfter keeps runs created at or after this time.
	CreatedAfter time.Time
	// Status is a status or a conclusion, as GitHub's filter accepts.
	Status string
	// Limit caps the number of runs returned, newest first; 0 is no cap.
	Limit int
}

// DispatchWorkflow triggers a workflow_dispatch run of workflowFile on ref,
// a branch or tag name.
func (c *Client) DispatchWorkflow(ctx context.Context, repo, workflowFile, ref string, inputs map[string]string) error {
	rp, err := repoPath(repo)
	if err != nil {
		return err
	}
	if workflowFile == "" || ref == "" {
		return errors.New("gh: dispatch: workflow and ref are required")
	}
	wf, err := segment("workflow", workflowFile)
	if err != nil {
		return err
	}
	body := struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs,omitempty"`
	}{Ref: ref, Inputs: inputs}
	return c.call(ctx, http.MethodPost, "/repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches",
		rp+"/actions/workflows/"+wf+"/dispatches", body, nil)
}

// ListWorkflowRuns returns workflow runs matching p, newest first.
func (c *Client) ListWorkflowRuns(ctx context.Context, repo string, p ListWorkflowRunsParams) ([]WorkflowRun, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("event", p.Event)
	set("branch", p.Branch)
	set("head_sha", p.HeadSHA)
	set("status", p.Status)
	if !p.CreatedAfter.IsZero() {
		q.Set("created", ">="+p.CreatedAfter.UTC().Format(time.RFC3339))
	}
	route, path := "/repos/{owner}/{repo}/actions/runs", rp+"/actions/runs"
	if p.Workflow != "" {
		wf, err := segment("workflow", p.Workflow)
		if err != nil {
			return nil, err
		}
		route = "/repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs"
		path = rp + "/actions/workflows/" + wf + "/runs"
	}
	return list[WorkflowRun](ctx, c, route, path, q, "workflow_runs", p.Limit)
}

// GetWorkflowRun returns one workflow run.
func (c *Client) GetWorkflowRun(ctx context.Context, repo string, id int64) (*WorkflowRun, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var run WorkflowRun
	if err := c.get(ctx, "/repos/{owner}/{repo}/actions/runs/{run_id}", rp+"/actions/runs/"+i64(id), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// ListWorkflowJobs returns the jobs of a run's latest attempt.
func (c *Client) ListWorkflowJobs(ctx context.Context, repo string, runID int64) ([]WorkflowJob, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[WorkflowJob](ctx, c, "/repos/{owner}/{repo}/actions/runs/{run_id}/jobs", rp+"/actions/runs/"+i64(runID)+"/jobs", nil, "jobs", 0)
}

// ListArtifacts returns the artifacts a run uploaded.
func (c *Client) ListArtifacts(ctx context.Context, repo string, runID int64) ([]Artifact, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[Artifact](ctx, c, "/repos/{owner}/{repo}/actions/runs/{run_id}/artifacts", rp+"/actions/runs/"+i64(runID)+"/artifacts", nil, "artifacts", 0)
}

// ReviewDeploymentProtectionRule answers a deployment_protection_rule
// event for the App's own custom protection rule. state is approved or
// rejected.
func (c *Client) ReviewDeploymentProtectionRule(ctx context.Context, repo string, runID int64, environmentName, state, comment string) error {
	rp, err := repoPath(repo)
	if err != nil {
		return err
	}
	if state != DeploymentApproved && state != DeploymentRejected {
		return fmt.Errorf("gh: deployment protection rule: state %q is not approved or rejected", state)
	}
	if environmentName == "" {
		return errors.New("gh: deployment protection rule: environment name is required")
	}
	body := struct {
		EnvironmentName string `json:"environment_name"`
		State           string `json:"state"`
		Comment         string `json:"comment,omitempty"`
	}{EnvironmentName: environmentName, State: state, Comment: comment}
	return c.call(ctx, http.MethodPost, "/repos/{owner}/{repo}/actions/runs/{run_id}/deployment_protection_rule",
		rp+"/actions/runs/"+i64(runID)+"/deployment_protection_rule", body, nil)
}

// ListPendingDeployments returns the environments a run is waiting on, so
// the sticky comment can link reviewers to the approval page.
func (c *Client) ListPendingDeployments(ctx context.Context, repo string, runID int64) ([]PendingDeployment, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []PendingDeployment
	if err := c.get(ctx, "/repos/{owner}/{repo}/actions/runs/{run_id}/pending_deployments", rp+"/actions/runs/"+i64(runID)+"/pending_deployments", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RunIDFromCallbackURL extracts the workflow run id from a
// deployment_callback_url such as
// https://api.github.com/repos/o/r/actions/runs/42/deployment_protection_rule.
func RunIDFromCallbackURL(callback string) (int64, error) {
	u, err := url.Parse(callback)
	if err != nil {
		return 0, fmt.Errorf("gh: deployment callback url: %w", err)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+2 < len(segs); i++ {
		if segs[i] == "actions" && segs[i+1] == "runs" {
			if id, err := strconv.ParseInt(segs[i+2], 10, 64); err == nil && id > 0 {
				return id, nil
			}
		}
	}
	return 0, fmt.Errorf("gh: deployment callback url %q has no run id", callback)
}
