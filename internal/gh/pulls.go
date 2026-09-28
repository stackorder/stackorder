package gh

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Review states as the REST API returns them. Webhook payloads use the
// lower case form; Review.Approved accepts both.
const (
	ReviewApproved         = "APPROVED"
	ReviewChangesRequested = "CHANGES_REQUESTED"
	ReviewCommented        = "COMMENTED"
	ReviewDismissed        = "DISMISSED"
	ReviewPending          = "PENDING"
)

// Labels is a list of label names, encoded on the wire as GitHub's label
// objects.
type Labels []string

// UnmarshalJSON decodes an array of label objects or of plain names.
func (l *Labels) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make(Labels, 0, len(raw))
	for _, r := range raw {
		var name string
		if json.Unmarshal(r, &name) == nil {
			out = append(out, name)
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(r, &obj); err != nil {
			return err
		}
		out = append(out, obj.Name)
	}
	*l = out
	return nil
}

// MarshalJSON encodes the names as label objects.
func (l Labels) MarshalJSON() ([]byte, error) {
	objs := make([]struct {
		Name string `json:"name"`
	}, len(l))
	for i, n := range l {
		objs[i].Name = n
	}
	return json.Marshal(objs)
}

// PullBranch is the repository side of a pull request head or base.
type PullBranch struct {
	// Repo is nil when the head repository of a fork has been deleted.
	Repo *Repository
}

// PullRequest is a pull request. HeadSHA, HeadRef, BaseSHA and BaseRef are
// the head.sha, head.ref, base.sha and base.ref of GitHub's payload; Head and
// Base carry the repositories, so Head.Repo.Fork tells a fork PR apart.
type PullRequest struct {
	Number         int        `json:"number"`
	Title          string     `json:"title,omitempty"`
	State          string     `json:"state"`
	Merged         bool       `json:"merged"`
	MergeCommitSHA string     `json:"merge_commit_sha,omitempty"`
	Mergeable      *bool      `json:"mergeable"`
	MergeableState string     `json:"mergeable_state,omitempty"`
	Draft          bool       `json:"draft"`
	HTMLURL        string     `json:"html_url,omitempty"`
	User           User       `json:"user"`
	Labels         Labels     `json:"labels"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
	HeadSHA        string     `json:"-"`
	HeadRef        string     `json:"-"`
	BaseSHA        string     `json:"-"`
	BaseRef        string     `json:"-"`
	Head           PullBranch `json:"-"`
	Base           PullBranch `json:"-"`
}

type pullAlias PullRequest

type wireBranch struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *Repository `json:"repo"`
}

type pullWire struct {
	*pullAlias
	Head wireBranch `json:"head"`
	Base wireBranch `json:"base"`
}

// UnmarshalJSON decodes GitHub's nested head and base objects.
func (p *PullRequest) UnmarshalJSON(b []byte) error {
	w := pullWire{pullAlias: (*pullAlias)(p)}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	p.HeadRef, p.HeadSHA, p.Head.Repo = w.Head.Ref, w.Head.SHA, w.Head.Repo
	p.BaseRef, p.BaseSHA, p.Base.Repo = w.Base.Ref, w.Base.SHA, w.Base.Repo
	return nil
}

// MarshalJSON encodes the pull request in GitHub's wire format.
func (p PullRequest) MarshalJSON() ([]byte, error) {
	a := pullAlias(p)
	return json.Marshal(pullWire{
		pullAlias: &a,
		Head:      wireBranch{Ref: p.HeadRef, SHA: p.HeadSHA, Repo: p.Head.Repo},
		Base:      wireBranch{Ref: p.BaseRef, SHA: p.BaseSHA, Repo: p.Base.Repo},
	})
}

// IsFork reports whether the head branch lives in another repository.
func (p *PullRequest) IsFork() bool {
	if p.Head.Repo == nil {
		return true
	}
	if p.Base.Repo != nil && !strings.EqualFold(p.Head.Repo.FullName, p.Base.Repo.FullName) {
		return true
	}
	return p.Head.Repo.Fork && p.Base.Repo == nil
}

// Review is a pull request review.
type Review struct {
	ID          int64     `json:"id"`
	User        User      `json:"user"`
	Body        string    `json:"body,omitempty"`
	State       string    `json:"state"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at,omitzero"`
	HTMLURL     string    `json:"html_url,omitempty"`
}

// Approved reports whether the review approves the pull request.
func (r Review) Approved() bool { return strings.EqualFold(r.State, ReviewApproved) }

// GetPull returns one pull request, including its mergeability.
func (c *Client) GetPull(ctx context.Context, repo string, number int) (*PullRequest, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var pr PullRequest
	if err := c.get(ctx, "/repos/{owner}/{repo}/pulls/{pull_number}", rp+"/pulls/"+itoa(number), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// ListReviews returns every review of a pull request, oldest first.
func (c *Client) ListReviews(ctx context.Context, repo string, number int) ([]Review, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[Review](ctx, c, "/repos/{owner}/{repo}/pulls/{pull_number}/reviews", rp+"/pulls/"+itoa(number)+"/reviews", nil, "", 0)
}

// ListPullFiles returns the paths a pull request changes. A renamed file
// contributes both its new and its previous path.
func (c *Client) ListPullFiles(ctx context.Context, repo string, number int) ([]string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	type file struct {
		Filename         string `json:"filename"`
		PreviousFilename string `json:"previous_filename"`
	}
	files, err := list[file](ctx, c, "/repos/{owner}/{repo}/pulls/{pull_number}/files", rp+"/pulls/"+itoa(number)+"/files", nil, "", 0)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		for _, p := range []string{f.Filename, f.PreviousFilename} {
			if p != "" && !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	return paths, nil
}
