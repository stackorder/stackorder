package gh

import (
	"context"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"
)

// Check run statuses.
const (
	CheckRunQueued     = "queued"
	CheckRunInProgress = "in_progress"
	CheckRunCompleted  = "completed"
)

// Check run conclusions.
const (
	ConclusionSuccess        = "success"
	ConclusionFailure        = "failure"
	ConclusionNeutral        = "neutral"
	ConclusionCancelled      = "cancelled"
	ConclusionSkipped        = "skipped"
	ConclusionTimedOut       = "timed_out"
	ConclusionActionRequired = "action_required"
)

// MaxCheckOutput is GitHub's limit, in bytes here, for each of the check run
// output title, summary and text.
const MaxCheckOutput = 65535

// CheckRunOutput is the rendered body of a check run.
type CheckRunOutput struct {
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Text    string `json:"text,omitempty"`
}

// CheckRunAction is a button GitHub shows on a check run; a click arrives as
// a check_run requested_action event carrying Identifier.
type CheckRunAction struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Identifier  string `json:"identifier"`
}

// CheckRunParams is the body of a check run create or update. Zero fields
// are omitted; HeadSHA is ignored on update.
type CheckRunParams struct {
	Name        string           `json:"name,omitempty"`
	HeadSHA     string           `json:"head_sha,omitempty"`
	Status      string           `json:"status,omitempty"`
	Conclusion  string           `json:"conclusion,omitempty"`
	DetailsURL  string           `json:"details_url,omitempty"`
	ExternalID  string           `json:"external_id,omitempty"`
	Output      CheckRunOutput   `json:"output,omitzero"`
	StartedAt   time.Time        `json:"started_at,omitzero"`
	CompletedAt time.Time        `json:"completed_at,omitzero"`
	Actions     []CheckRunAction `json:"actions,omitempty"`
}

// CheckRun is a check run as GitHub returns it.
type CheckRun struct {
	ID           int64            `json:"id"`
	Name         string           `json:"name"`
	HeadSHA      string           `json:"head_sha"`
	Status       string           `json:"status"`
	Conclusion   string           `json:"conclusion,omitempty"`
	DetailsURL   string           `json:"details_url,omitempty"`
	ExternalID   string           `json:"external_id,omitempty"`
	HTMLURL      string           `json:"html_url,omitempty"`
	Output       CheckRunOutput   `json:"output,omitzero"`
	StartedAt    time.Time        `json:"started_at,omitzero"`
	CompletedAt  time.Time        `json:"completed_at,omitzero"`
	App          *AppRef          `json:"app,omitempty"`
	CheckSuite   *CheckSuiteRef   `json:"check_suite,omitempty"`
	PullRequests []PullRequestRef `json:"pull_requests,omitempty"`
}

// AppRef identifies the App that owns a check run or suite.
type AppRef struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug,omitempty"`
}

// CheckSuiteRef identifies the suite a check run belongs to.
type CheckSuiteRef struct {
	ID         int64  `json:"id"`
	HeadBranch string `json:"head_branch,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
}

// PullRequestRef is the short pull request reference embedded in check
// runs, check suites, workflow runs and deployments.
type PullRequestRef struct {
	Number int       `json:"number"`
	Head   BranchRef `json:"head"`
	Base   BranchRef `json:"base"`
}

// BranchRef is a branch name and commit.
type BranchRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

func (p CheckRunParams) wire(update bool) CheckRunParams {
	if update {
		p.HeadSHA = ""
	}
	if !p.StartedAt.IsZero() {
		p.StartedAt = p.StartedAt.UTC().Truncate(time.Second)
	}
	if !p.CompletedAt.IsZero() {
		p.CompletedAt = p.CompletedAt.UTC().Truncate(time.Second)
	}
	p.Output.Title = truncateUTF8(p.Output.Title, MaxCheckOutput)
	p.Output.Summary = truncateUTF8(p.Output.Summary, MaxCheckOutput)
	p.Output.Text = truncateUTF8(p.Output.Text, MaxCheckOutput)
	return p
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for i := 0; i < utf8.UTFMax-1 && n > 0 && !utf8.RuneStart(s[n]); i++ {
		n--
	}
	return s[:n]
}

// CreateCheckRun creates a check run on a commit.
func (c *Client) CreateCheckRun(ctx context.Context, repo string, p CheckRunParams) (*CheckRun, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var run CheckRun
	if err := c.call(ctx, http.MethodPost, "/repos/{owner}/{repo}/check-runs", rp+"/check-runs", p.wire(false), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// UpdateCheckRun updates an existing check run.
func (c *Client) UpdateCheckRun(ctx context.Context, repo string, id int64, p CheckRunParams) (*CheckRun, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var run CheckRun
	if err := c.call(ctx, http.MethodPatch, "/repos/{owner}/{repo}/check-runs/{check_run_id}", rp+"/check-runs/"+i64(id), p.wire(true), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// ListCheckRunsForRef lists the latest check runs on a commit, branch or
// tag, optionally only those named checkName.
func (c *Client) ListCheckRunsForRef(ctx context.Context, repo, ref, checkName string) ([]CheckRun, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	if _, err := checkPath("ref", ref); err != nil {
		return nil, err
	}
	q := url.Values{}
	if checkName != "" {
		q.Set("check_name", checkName)
	}
	return list[CheckRun](ctx, c, "/repos/{owner}/{repo}/commits/{ref}/check-runs", rp+"/commits/"+url.PathEscape(ref)+"/check-runs", q, "check_runs", 0)
}
