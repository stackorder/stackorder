package ghfake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/stackorder/stackorder/internal/gh"
)

const zeroSHA = "0000000000000000000000000000000000000000"

func (s *Server) common(action, repo, sender string) gh.EventCommon {
	c := gh.EventCommon{Action: action, Sender: s.user(sender)}
	if repo == "" {
		return c
	}
	rs := s.ensureRepo(repo)
	r := rs.repo
	c.Repository = &r
	if rs.installation != 0 {
		c.Installation = &gh.Installation{ID: rs.installation}
	}
	return c
}

// PullRequestEvent builds a pull_request payload for a pull request, with
// the repository and installation filled in from the fake's state. The pull
// request is stored as if SetPull had been called.
func (s *Server) PullRequestEvent(action, repo string, pr gh.PullRequest) *gh.PullRequestEvent {
	s.SetPull(repo, pr)
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := *s.repos[strings.ToLower(repo)].pulls[pr.Number]
	sender := stored.User.Login
	if sender == "" {
		sender = "octocat"
	}
	ev := &gh.PullRequestEvent{EventCommon: s.common(action, repo, sender), Number: pr.Number, PullRequest: stored}
	if action == "synchronize" {
		ev.After = stored.HeadSHA
	}
	return ev
}

// PullRequestReviewEvent builds a pull_request_review payload. The review is
// appended to the pull request's reviews; its state uses the lower case
// webhook form.
func (s *Server) PullRequestReviewEvent(action, repo string, number int, review gh.Review) *gh.PullRequestReviewEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	if review.ID == 0 {
		review.ID = s.id()
	}
	if review.SubmittedAt.IsZero() {
		review.SubmittedAt = s.now()
	}
	rs.reviews[number] = append(rs.reviews[number], review)
	var pr gh.PullRequest
	if p, ok := rs.pulls[number]; ok {
		pr = *p
	} else {
		pr = gh.PullRequest{Number: number, State: gh.IssueOpen}
	}
	hook := review
	hook.State = strings.ToLower(review.State)
	return &gh.PullRequestReviewEvent{EventCommon: s.common(action, repo, review.User.Login), Review: hook, PullRequest: pr}
}

// IssueCommentEvent builds an issue_comment "created" payload for a comment
// by login on pull request number, and stores the comment so reactions and
// replies to it work.
func (s *Server) IssueCommentEvent(repo string, number int, body, login string) *gh.IssueCommentEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	cm := s.addComment(rs, number, s.user(login), s.association(rs, login), body)
	issue := gh.Issue{
		Number:      number,
		State:       gh.IssueOpen,
		HTMLURL:     fmt.Sprintf("https://github.com/%s/pull/%d", rs.repo.FullName, number),
		PullRequest: &gh.IssuePullRequest{HTMLURL: fmt.Sprintf("https://github.com/%s/pull/%d", rs.repo.FullName, number)},
	}
	if pr, ok := rs.pulls[number]; ok {
		issue.Title, issue.State, issue.User, issue.Labels = pr.Title, pr.State, pr.User, pr.Labels
	}
	return &gh.IssueCommentEvent{EventCommon: s.common("created", repo, login), Issue: issue, Comment: cm}
}

// PushEvent builds a push payload moving ref (refs/heads/... or
// refs/tags/...) to after with one commit touching paths, and updates the
// fake's refs and tags accordingly.
func (s *Server) PushEvent(repo, ref, after string, paths ...string) *gh.PushEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	short := strings.TrimPrefix(ref, "refs/")
	before, existed := rs.refs[short]
	if !existed {
		before = zeroSHA
	}
	rs.refs[short] = after
	if name, ok := strings.CutPrefix(short, "tags/"); ok {
		found := false
		for i := range rs.tags {
			if rs.tags[i].Name == name {
				rs.tags[i].SHA, found = after, true
			}
		}
		if !found {
			rs.tags = append(rs.tags, gh.Tag{Name: name, SHA: after})
		}
	}
	commit := gh.PushCommit{ID: after, Message: "update", Timestamp: s.now(), Added: []string{}, Removed: []string{}, Modified: append([]string{}, paths...)}
	ev := &gh.PushEvent{
		EventCommon: s.common("", repo, "octocat"),
		Ref:         ref,
		Before:      before,
		After:       after,
		Created:     !existed,
		Commits:     []gh.PushCommit{commit},
		HeadCommit:  &commit,
		Pusher:      gh.Pusher{Name: "octocat", Email: "octocat@example.com"},
	}
	return ev
}

// WorkflowRunEvent builds a workflow_run payload. Missing fields of run are
// taken from the stored run with the same id.
func (s *Server) WorkflowRunEvent(action, repo string, run gh.WorkflowRun) *gh.WorkflowRunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored := s.findRun(repo, run.ID); stored != nil && run.Path == "" {
		run = *stored
	}
	ev := &gh.WorkflowRunEvent{EventCommon: s.common(action, repo, "octocat"), WorkflowRun: run}
	if run.Path != "" {
		ev.Workflow = &gh.Workflow{ID: run.WorkflowID, Name: run.Name, Path: run.Path}
	}
	return ev
}

// WorkflowJobEvent builds a workflow_job payload.
func (s *Server) WorkflowJobEvent(action, repo string, job gh.WorkflowJob) *gh.WorkflowJobEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &gh.WorkflowJobEvent{EventCommon: s.common(action, repo, "octocat"), WorkflowJob: job}
}

// CheckRunEvent builds a check_run payload; requestedAction is the button
// identifier for a requested_action event.
func (s *Server) CheckRunEvent(action, repo string, run gh.CheckRun, requestedAction string) *gh.CheckRunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := &gh.CheckRunEvent{EventCommon: s.common(action, repo, "octocat"), CheckRun: run}
	if requestedAction != "" {
		ev.RequestedAction = &gh.RequestedAction{Identifier: requestedAction}
	}
	return ev
}

// InstallationEvent builds an installation payload for a registered
// installation, listing its repositories.
func (s *Server) InstallationEvent(action string, id int64) *gh.InstallationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := &gh.InstallationEvent{EventCommon: gh.EventCommon{Action: action, Sender: s.user("octocat")}}
	in, ok := s.installations[id]
	if !ok {
		ev.Installation = &gh.Installation{ID: id}
		return ev
	}
	inst := in.inst
	ev.Installation = &inst
	for _, full := range in.repos {
		ev.Repositories = append(ev.Repositories, s.repos[strings.ToLower(full)].repo)
	}
	return ev
}

// DeploymentProtectionRuleEvent builds a deployment_protection_rule
// "requested" payload for a run waiting on environment, with a callback URL
// pointing at the fake.
func (s *Server) DeploymentProtectionRuleEvent(repo string, runID int64, environment, sha string) *gh.DeploymentProtectionRuleEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := &gh.DeploymentProtectionRuleEvent{
		EventCommon:           s.common("requested", repo, "octocat"),
		Environment:           environment,
		Event:                 "workflow_dispatch",
		DeploymentCallbackURL: fmt.Sprintf("%s/repos/%s/actions/runs/%d/deployment_protection_rule", s.srv.URL, repo, runID),
		Deployment: &gh.Deployment{
			ID:          s.id(),
			SHA:         sha,
			Ref:         s.ensureRepo(repo).repo.DefaultBranch,
			Task:        "deploy",
			Environment: environment,
			Creator:     s.botUser(),
			CreatedAt:   s.now(),
		},
	}
	if run := s.findRun(repo, runID); run != nil {
		ev.Deployment.Ref = run.HeadBranch
		ev.Event = run.Event
	}
	return ev
}

// SignedWebhook encodes payload (any JSON value, or raw []byte) and returns
// the body with the headers GitHub sends: X-GitHub-Event,
// X-GitHub-Delivery, X-Hub-Signature-256 and Content-Type.
func SignedWebhook(secret []byte, event string, payload any) ([]byte, http.Header) {
	body, ok := payload.([]byte)
	if !ok {
		b, err := json.Marshal(payload)
		if err != nil {
			panic(fmt.Sprintf("ghfake: encode %s payload: %v", event, err))
		}
		body = b
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "GitHub-Hookshot/fake")
	h.Set(gh.HeaderEvent, event)
	h.Set(gh.HeaderDelivery, uuid.NewString())
	h.Set(gh.HeaderSignature, gh.SignPayload(secret, body))
	h.Set("X-GitHub-Hook-ID", "1")
	h.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
	h.Set("X-GitHub-Hook-Installation-Target-ID", "1")
	return body, h
}

// Deliver posts a signed webhook to url and returns the response status.
func Deliver(ctx context.Context, client *http.Client, url string, secret []byte, event string, payload any) (int, error) {
	body, h := SignedWebhook(secret, event, payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return 0, fmt.Errorf("ghfake: deliver %s: %w", event, err)
	}
	req.Header = h
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("ghfake: deliver %s: %w", event, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
