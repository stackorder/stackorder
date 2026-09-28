package gh

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Webhook event names, as sent in X-GitHub-Event.
const (
	EventPing                     = "ping"
	EventInstallation             = "installation"
	EventInstallationRepositories = "installation_repositories"
	EventPullRequest              = "pull_request"
	EventPullRequestReview        = "pull_request_review"
	EventIssueComment             = "issue_comment"
	EventPush                     = "push"
	EventCheckRun                 = "check_run"
	EventCheckSuite               = "check_suite"
	EventWorkflowRun              = "workflow_run"
	EventWorkflowJob              = "workflow_job"
	EventDeploymentProtectionRule = "deployment_protection_rule"
)

// Event is implemented by every payload ParseEvent returns.
type Event interface {
	// Common returns the fields every payload shares.
	Common() *EventCommon
}

// EventCommon holds the fields shared by webhook payloads.
type EventCommon struct {
	Action       string        `json:"action,omitempty"`
	Installation *Installation `json:"installation,omitempty"`
	Repository   *Repository   `json:"repository,omitempty"`
	Sender       User          `json:"sender"`
}

// Common implements Event.
func (c *EventCommon) Common() *EventCommon { return c }

// InstallationID returns the installation the delivery belongs to, or 0.
func (c *EventCommon) InstallationID() int64 {
	if c.Installation == nil {
		return 0
	}
	return c.Installation.ID
}

// RepoFullName returns the repository's "owner/name", or "".
func (c *EventCommon) RepoFullName() string {
	if c.Repository == nil {
		return ""
	}
	return c.Repository.FullName
}

// PingEvent is sent when a webhook is created.
type PingEvent struct {
	EventCommon
	Zen    string `json:"zen"`
	HookID int64  `json:"hook_id"`
}

// InstallationEvent reports an installation being created, deleted,
// suspended, unsuspended or accepting new permissions.
type InstallationEvent struct {
	EventCommon
	Repositories []Repository `json:"repositories,omitempty"`
}

// InstallationRepositoriesEvent reports repositories added to or removed
// from an installation.
type InstallationRepositoriesEvent struct {
	EventCommon
	RepositorySelection string       `json:"repository_selection"`
	RepositoriesAdded   []Repository `json:"repositories_added"`
	RepositoriesRemoved []Repository `json:"repositories_removed"`
}

// Label is a label attached by a labeled or unlabeled action.
type Label struct {
	Name string `json:"name"`
}

// PullRequestEvent reports activity on a pull request.
type PullRequestEvent struct {
	EventCommon
	Number      int         `json:"number"`
	PullRequest PullRequest `json:"pull_request"`
	Before      string      `json:"before,omitempty"`
	After       string      `json:"after,omitempty"`
	Label       *Label      `json:"label,omitempty"`
}

// PullRequestReviewEvent reports a review submitted, edited or dismissed.
type PullRequestReviewEvent struct {
	EventCommon
	Review      Review      `json:"review"`
	PullRequest PullRequest `json:"pull_request"`
}

// IssueCommentEvent reports a comment on an issue or pull request.
type IssueCommentEvent struct {
	EventCommon
	Issue   Issue   `json:"issue"`
	Comment Comment `json:"comment"`
}

// IsPullRequest reports whether the comment is on a pull request.
func (e *IssueCommentEvent) IsPullRequest() bool { return e.Issue.PullRequest != nil }

// PushCommit is one commit of a push.
type PushCommit struct {
	ID        string    `json:"id"`
	Message   string    `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp,omitzero"`
	Added     []string  `json:"added"`
	Removed   []string  `json:"removed"`
	Modified  []string  `json:"modified"`
}

// Pusher is the git identity of a push.
type Pusher struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// PushEvent reports commits or tags pushed to a ref.
type PushEvent struct {
	EventCommon
	Ref        string       `json:"ref"`
	Before     string       `json:"before"`
	After      string       `json:"after"`
	Created    bool         `json:"created"`
	Deleted    bool         `json:"deleted"`
	Forced     bool         `json:"forced"`
	BaseRef    string       `json:"base_ref,omitempty"`
	Commits    []PushCommit `json:"commits"`
	HeadCommit *PushCommit  `json:"head_commit,omitempty"`
	Pusher     Pusher       `json:"pusher"`
}

// IsTag reports whether the push is to a tag.
func (e *PushEvent) IsTag() bool { return strings.HasPrefix(e.Ref, "refs/tags/") }

// TagName returns the tag name, or "" for a branch push.
func (e *PushEvent) TagName() string {
	name, _ := strings.CutPrefix(e.Ref, "refs/tags/")
	if name == e.Ref {
		return ""
	}
	return name
}

// Branch returns the branch name, or "" for a tag push.
func (e *PushEvent) Branch() string {
	name, _ := strings.CutPrefix(e.Ref, "refs/heads/")
	if name == e.Ref {
		return ""
	}
	return name
}

// Paths returns the distinct paths added, removed or modified by the
// pushed commits, in first-seen order.
func (e *PushEvent) Paths() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range e.Commits {
		for _, group := range [][]string{c.Added, c.Removed, c.Modified} {
			for _, p := range group {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// RequestedAction is the button a user clicked on a check run.
type RequestedAction struct {
	Identifier string `json:"identifier"`
}

// CheckRunEvent reports a check run created, completed, rerequested or
// with a requested action.
type CheckRunEvent struct {
	EventCommon
	CheckRun        CheckRun         `json:"check_run"`
	RequestedAction *RequestedAction `json:"requested_action,omitempty"`
}

// CheckSuite is the suite of a check_suite event.
type CheckSuite struct {
	ID           int64            `json:"id"`
	HeadBranch   string           `json:"head_branch,omitempty"`
	HeadSHA      string           `json:"head_sha"`
	Status       string           `json:"status,omitempty"`
	Conclusion   string           `json:"conclusion,omitempty"`
	Before       string           `json:"before,omitempty"`
	After        string           `json:"after,omitempty"`
	App          *AppRef          `json:"app,omitempty"`
	PullRequests []PullRequestRef `json:"pull_requests,omitempty"`
}

// CheckSuiteEvent reports a check suite requested, rerequested or completed.
type CheckSuiteEvent struct {
	EventCommon
	CheckSuite CheckSuite `json:"check_suite"`
}

// Workflow identifies the workflow of a workflow_run event.
type Workflow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// WorkflowRunEvent reports a workflow run requested, in progress or
// completed.
type WorkflowRunEvent struct {
	EventCommon
	WorkflowRun WorkflowRun `json:"workflow_run"`
	Workflow    *Workflow   `json:"workflow,omitempty"`
}

// WorkflowJobEvent reports a job queued, in progress, waiting or completed.
type WorkflowJobEvent struct {
	EventCommon
	WorkflowJob WorkflowJob `json:"workflow_job"`
}

// Deployment is the deployment a protection rule is asked about.
type Deployment struct {
	ID          int64     `json:"id"`
	SHA         string    `json:"sha"`
	Ref         string    `json:"ref"`
	Task        string    `json:"task,omitempty"`
	Environment string    `json:"environment"`
	Creator     User      `json:"creator,omitzero"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
}

// DeploymentProtectionRuleEvent asks the App, as a custom deployment
// protection rule, to approve or reject a job's deployment.
type DeploymentProtectionRuleEvent struct {
	EventCommon
	Environment           string           `json:"environment"`
	Event                 string           `json:"event"`
	DeploymentCallbackURL string           `json:"deployment_callback_url"`
	Deployment            *Deployment      `json:"deployment,omitempty"`
	PullRequests          []PullRequestRef `json:"pull_requests,omitempty"`
}

// RunID returns the workflow run id from the callback URL, for
// ReviewDeploymentProtectionRule.
func (e *DeploymentProtectionRuleEvent) RunID() (int64, error) {
	return RunIDFromCallbackURL(e.DeploymentCallbackURL)
}

// ParseEvent decodes a webhook body by its X-GitHub-Event name into the
// matching *XxxEvent type. Unknown names return ErrUnsupportedEvent.
func ParseEvent(name string, body []byte) (any, error) {
	var ev Event
	switch name {
	case EventPing:
		ev = &PingEvent{}
	case EventInstallation:
		ev = &InstallationEvent{}
	case EventInstallationRepositories:
		ev = &InstallationRepositoriesEvent{}
	case EventPullRequest:
		ev = &PullRequestEvent{}
	case EventPullRequestReview:
		ev = &PullRequestReviewEvent{}
	case EventIssueComment:
		ev = &IssueCommentEvent{}
	case EventPush:
		ev = &PushEvent{}
	case EventCheckRun:
		ev = &CheckRunEvent{}
	case EventCheckSuite:
		ev = &CheckSuiteEvent{}
	case EventWorkflowRun:
		ev = &WorkflowRunEvent{}
	case EventWorkflowJob:
		ev = &WorkflowJobEvent{}
	case EventDeploymentProtectionRule:
		ev = &DeploymentProtectionRuleEvent{}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEvent, name)
	}
	if err := json.Unmarshal(body, ev); err != nil {
		return nil, fmt.Errorf("gh: decode %s event: %w", name, err)
	}
	return ev, nil
}
