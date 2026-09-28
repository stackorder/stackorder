package gh

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Reaction contents accepted by CreateReaction.
const (
	ReactionPlusOne  = "+1"
	ReactionMinusOne = "-1"
	ReactionLaugh    = "laugh"
	ReactionConfused = "confused"
	ReactionHeart    = "heart"
	ReactionHooray   = "hooray"
	ReactionRocket   = "rocket"
	ReactionEyes     = "eyes"
)

// Issue states.
const (
	IssueOpen   = "open"
	IssueClosed = "closed"
	IssueAll    = "all"
)

// Comment is an issue or pull request conversation comment.
type Comment struct {
	ID                int64     `json:"id"`
	Body              string    `json:"body"`
	User              User      `json:"user"`
	AuthorAssociation string    `json:"author_association,omitempty"`
	HTMLURL           string    `json:"html_url,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitzero"`
	UpdatedAt         time.Time `json:"updated_at,omitzero"`
}

// IssuePullRequest marks an issue that is a pull request.
type IssuePullRequest struct {
	URL     string `json:"url,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

// Issue is a GitHub issue.
type Issue struct {
	Number      int               `json:"number"`
	Title       string            `json:"title"`
	Body        string            `json:"body,omitempty"`
	State       string            `json:"state"`
	Labels      Labels            `json:"labels"`
	User        User              `json:"user"`
	HTMLURL     string            `json:"html_url,omitempty"`
	CreatedAt   time.Time         `json:"created_at,omitzero"`
	UpdatedAt   time.Time         `json:"updated_at,omitzero"`
	ClosedAt    *time.Time        `json:"closed_at,omitempty"`
	PullRequest *IssuePullRequest `json:"pull_request,omitempty"`
}

// IssueParams is the body of an issue create or update. Zero fields are
// omitted, so an update changes only what is set.
type IssueParams struct {
	Title  string   `json:"title,omitempty"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
	State  string   `json:"state,omitempty"`
}

type commentBody struct {
	Body string `json:"body"`
}

// ListIssueComments returns every conversation comment on an issue or pull
// request, oldest first.
func (c *Client) ListIssueComments(ctx context.Context, repo string, number int) ([]Comment, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[Comment](ctx, c, "/repos/{owner}/{repo}/issues/{issue_number}/comments", rp+"/issues/"+itoa(number)+"/comments", nil, "", 0)
}

// CreateIssueComment posts a comment on an issue or pull request.
func (c *Client) CreateIssueComment(ctx context.Context, repo string, number int, body string) (*Comment, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var cm Comment
	if err := c.call(ctx, http.MethodPost, "/repos/{owner}/{repo}/issues/{issue_number}/comments", rp+"/issues/"+itoa(number)+"/comments", commentBody{Body: body}, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

// UpdateIssueComment replaces the body of a comment.
func (c *Client) UpdateIssueComment(ctx context.Context, repo string, id int64, body string) (*Comment, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var cm Comment
	if err := c.call(ctx, http.MethodPatch, "/repos/{owner}/{repo}/issues/comments/{comment_id}", rp+"/issues/comments/"+i64(id), commentBody{Body: body}, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

// DeleteIssueComment deletes a comment.
func (c *Client) DeleteIssueComment(ctx context.Context, repo string, id int64) error {
	rp, err := repoPath(repo)
	if err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, "/repos/{owner}/{repo}/issues/comments/{comment_id}", rp+"/issues/comments/"+i64(id), nil, nil)
}

// UpsertStickyComment keeps exactly one comment on the pull request whose
// body starts with marker and whose author is the client's own account: the
// App's bot user for an installation client, the signed-in user for a token
// client. body is prefixed with marker and a newline when it does not
// already start with it. An existing comment is updated only when its body
// differs, later duplicates are deleted, and a comment is created when none
// exists. Comments by any other account, other bots included, are never
// adopted or touched.
func (c *Client) UpsertStickyComment(ctx context.Context, repo string, number int, marker, body string) (*Comment, error) {
	if !strings.HasPrefix(body, marker) {
		body = marker + "\n" + body
	}
	comments, err := c.ListIssueComments(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	self, err := c.login(ctx)
	if err != nil {
		return nil, fmt.Errorf("gh: sticky comment author: %w", err)
	}
	var sticky *Comment
	var duplicates []int64
	for i := range comments {
		cm := &comments[i]
		if !strings.EqualFold(cm.User.Login, self) || !strings.HasPrefix(strings.TrimLeft(cm.Body, " \t\r\n"), marker) {
			continue
		}
		if sticky == nil {
			sticky = cm
			continue
		}
		duplicates = append(duplicates, cm.ID)
	}
	if sticky == nil {
		return c.CreateIssueComment(ctx, repo, number, body)
	}
	for _, id := range duplicates {
		if err := c.DeleteIssueComment(ctx, repo, id); err != nil && !isNotFound(err) {
			return nil, err
		}
	}
	if sticky.Body == body {
		return sticky, nil
	}
	return c.UpdateIssueComment(ctx, repo, sticky.ID, body)
}

// CreateReaction adds a reaction to an issue or pull request comment.
func (c *Client) CreateReaction(ctx context.Context, repo string, commentID int64, content string) error {
	rp, err := repoPath(repo)
	if err != nil {
		return err
	}
	body := struct {
		Content string `json:"content"`
	}{Content: content}
	return c.call(ctx, http.MethodPost, "/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions", rp+"/issues/comments/"+i64(commentID)+"/reactions", body, nil)
}

// CreateIssue opens an issue.
func (c *Client) CreateIssue(ctx context.Context, repo string, p IssueParams) (*Issue, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var is Issue
	if err := c.call(ctx, http.MethodPost, "/repos/{owner}/{repo}/issues", rp+"/issues", p, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// UpdateIssue changes the set fields of an issue, including its state.
func (c *Client) UpdateIssue(ctx context.Context, repo string, number int, p IssueParams) (*Issue, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var is Issue
	if err := c.call(ctx, http.MethodPatch, "/repos/{owner}/{repo}/issues/{issue_number}", rp+"/issues/"+itoa(number), p, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// ListIssues returns the issues carrying every label in labels, in state
// open, closed or all (open when empty). Pull requests, which GitHub lists
// as issues too, are left out.
func (c *Client) ListIssues(ctx context.Context, repo string, labels []string, state string) ([]Issue, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	if state != "" {
		q.Set("state", state)
	}
	all, err := list[Issue](ctx, c, "/repos/{owner}/{repo}/issues", rp+"/issues", q, "", 0)
	if err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(all))
	for _, is := range all {
		if is.PullRequest == nil {
			issues = append(issues, is)
		}
	}
	return issues, nil
}
