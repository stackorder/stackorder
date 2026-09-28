package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Context is the repository, commit, event and workflow run a command works
// on. In GitHub Actions it is read from the GITHUB_* variables and the event
// payload; outside Actions the commit, repository and default branch come
// from git.
type Context struct {
	// CI is true inside GitHub Actions, where GITHUB_ACTIONS is "true".
	CI bool
	// Repository is "owner/repo".
	Repository string
	// SHA is the commit plans and applies are for: the pull request head for
	// pull request events (GITHUB_SHA is the merge commit there), the sha
	// input of a dispatched workflow, GITHUB_SHA otherwise, and HEAD outside
	// Actions.
	SHA string
	// Ref is GITHUB_REF, such as refs/pull/12/merge.
	Ref string
	// EventName is GITHUB_EVENT_NAME, such as pull_request.
	EventName string
	// PRNumber is the pull request number, or 0.
	PRNumber int
	// BaseSHA is the pull request base commit, or the before commit of a
	// push.
	BaseSHA string
	// HeadSHA is the pull request head commit, or the after commit of a
	// push.
	HeadSHA string
	// RunID is GITHUB_RUN_ID, the Actions workflow run.
	RunID int64
	// RunAttempt is GITHUB_RUN_ATTEMPT.
	RunAttempt int
	// ServerURL is GITHUB_SERVER_URL, https://github.com by default.
	ServerURL string
	// APIURL is GITHUB_API_URL, https://api.github.com by default.
	APIURL string
	// Workspace is GITHUB_WORKSPACE, the checkout directory.
	Workspace string
	// Token is GITHUB_TOKEN, used only for the neutral fallback checks.
	Token string
	// Actor is GITHUB_ACTOR.
	Actor string
	// DefaultBranch is the repository's default branch.
	DefaultBranch string
	// IsFork is true for a pull request whose head lives in another
	// repository.
	IsFork bool
	// DispatchRunID is the stackorder run id from the run_id input of a
	// workflow the server dispatched.
	DispatchRunID string
}

type eventRepo struct {
	FullName      string `json:"full_name"`
	Fork          bool   `json:"fork"`
	DefaultBranch string `json:"default_branch"`
}

type eventPayload struct {
	Before      string `json:"before"`
	After       string `json:"after"`
	PullRequest *struct {
		Number int `json:"number"`
		Head   struct {
			SHA  string     `json:"sha"`
			Repo *eventRepo `json:"repo"`
		} `json:"head"`
		Base struct {
			SHA  string     `json:"sha"`
			Repo *eventRepo `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository *eventRepo     `json:"repository"`
	Inputs     map[string]any `json:"inputs"`
}

// LoadContext reads the GitHub Actions context from the environment and the
// event payload at GITHUB_EVENT_PATH, filling what is missing from the git
// checkout at root. An unreadable or malformed event payload is an error.
func LoadContext(ctx context.Context, root string) (*Context, error) {
	c := &Context{
		CI:         os.Getenv("GITHUB_ACTIONS") == "true",
		Repository: os.Getenv("GITHUB_REPOSITORY"),
		SHA:        os.Getenv("GITHUB_SHA"),
		Ref:        os.Getenv("GITHUB_REF"),
		EventName:  os.Getenv("GITHUB_EVENT_NAME"),
		ServerURL:  cmp.Or(strings.TrimRight(os.Getenv("GITHUB_SERVER_URL"), "/"), "https://github.com"),
		APIURL:     cmp.Or(strings.TrimRight(os.Getenv("GITHUB_API_URL"), "/"), "https://api.github.com"),
		Workspace:  os.Getenv("GITHUB_WORKSPACE"),
		Token:      os.Getenv("GITHUB_TOKEN"),
		Actor:      os.Getenv("GITHUB_ACTOR"),
	}
	c.RunID, _ = strconv.ParseInt(os.Getenv("GITHUB_RUN_ID"), 10, 64)
	c.RunAttempt, _ = strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if p := os.Getenv("GITHUB_EVENT_PATH"); p != "" {
		if err := c.readEvent(p); err != nil {
			return nil, err
		}
	}
	if c.PRNumber == 0 {
		c.PRNumber = prFromRef(c.Ref)
	}
	c.fillFromGit(ctx, root)
	return c, nil
}

func (c *Context) readEvent(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return fmt.Errorf("reading the event payload: %w", err)
	}
	var ev eventPayload
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("decoding the event payload %s: %w", path, err)
	}
	if ev.Repository != nil {
		c.DefaultBranch = ev.Repository.DefaultBranch
		c.Repository = cmp.Or(c.Repository, ev.Repository.FullName)
	}
	if pr := ev.PullRequest; pr != nil {
		c.PRNumber = pr.Number
		c.HeadSHA = pr.Head.SHA
		c.BaseSHA = pr.Base.SHA
		c.SHA = cmp.Or(pr.Head.SHA, c.SHA)
		switch head, base := pr.Head.Repo, pr.Base.Repo; {
		case head == nil:
			c.IsFork = true
		case base != nil && head.FullName != "" && base.FullName != "":
			c.IsFork = !strings.EqualFold(head.FullName, base.FullName)
		default:
			c.IsFork = head.Fork
		}
	}
	if c.EventName == "push" {
		if !zeroSHA(ev.Before) {
			c.BaseSHA = ev.Before
		}
		c.HeadSHA = ev.After
	}
	if id, ok := ev.Inputs["run_id"].(string); ok {
		c.DispatchRunID = strings.TrimSpace(id)
	}
	if sha, ok := ev.Inputs["sha"].(string); ok && strings.TrimSpace(sha) != "" {
		c.SHA = strings.TrimSpace(sha)
	}
	return nil
}

func (c *Context) fillFromGit(ctx context.Context, root string) {
	if !c.CI || c.SHA == "" {
		if head, err := gitOutput(ctx, root, "rev-parse", "HEAD"); err == nil {
			c.SHA = head
		}
	}
	if c.Repository == "" {
		if remote, err := gitOutput(ctx, root, "remote", "get-url", "origin"); err == nil {
			c.Repository = parseRemote(remote)
		}
	}
	if c.DefaultBranch == "" {
		if ref, err := gitOutput(ctx, root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
			c.DefaultBranch = strings.TrimPrefix(ref, "origin/")
		}
	}
}

// JobURL links the Actions workflow run, or is empty outside Actions.
func (c *Context) JobURL() string {
	if !c.CI || c.Repository == "" || c.RunID == 0 {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%d", c.ServerURL, c.Repository, c.RunID)
}

// Trigger maps the event to the run trigger reported to the server.
func (c *Context) Trigger() v1.Trigger {
	switch c.EventName {
	case "pull_request", "pull_request_target":
		return v1.TriggerPullRequest
	case "push":
		return v1.TriggerPush
	case "schedule":
		return v1.TriggerSchedule
	case "issue_comment":
		return v1.TriggerComment
	case "check_run", "check_suite":
		return v1.TriggerRerequest
	}
	return v1.TriggerManual
}

func prFromRef(ref string) int {
	rest, ok := strings.CutPrefix(ref, "refs/pull/")
	if !ok {
		return 0
	}
	n, _, _ := strings.Cut(rest, "/")
	pr, err := strconv.Atoi(n)
	if err != nil {
		return 0
	}
	return pr
}

func zeroSHA(sha string) bool {
	return strings.Trim(sha, "0") == ""
}
