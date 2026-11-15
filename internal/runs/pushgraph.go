package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

// RecordDefaultGraph stores the graph a push-triggered resolve job scanned on
// the default branch and makes it the repository's default-branch graph, so a
// direct push to the default branch updates the stack lists, the overview,
// removed stacks and drift scheduling at once instead of waiting for the next
// planned merge.
//
// The token must come from a push whose ref is the repository's default branch
// and whose commit the graph is for, and that commit must still be the head of
// the default branch: a graph for a commit that is no longer the head, which an
// out-of-order, superseded or force push leaves behind, is ignored and Recorded
// is false. Recordings of one repository are serialised and the head is read
// inside that serialisation, so the last push to reach the head wins and an
// older push cannot replace a newer graph.
func (s *Service) RecordDefaultGraph(ctx context.Context, p principal.Principal, repoName string, req v1.RecordGraphRequest) (*v1.RecordGraphResponse, error) {
	c, err := claimsOf(p)
	if err != nil {
		return nil, err
	}
	if c.EventName != eventPush {
		return nil, principal.Wrap(principal.ErrForbidden, "only a push to the default branch records its graph; the token's event_name is %q", c.EventName)
	}
	repo, err := s.st.GetRepoByName(ctx, repoName)
	if errors.Is(err, store.ErrNotFound) {
		return nil, principal.Wrap(principal.ErrForbidden, "repository %s is not installed", repoName)
	}
	if err != nil {
		return nil, storeErr(err, "repository %s", repoName)
	}
	if err := checkRepoClaims(c, repo); err != nil {
		return nil, err
	}
	if want := "refs/heads/" + repo.DefaultBranch; c.Ref != want {
		return nil, principal.Wrap(principal.ErrForbidden, "claim ref is %q, want the default branch %q", c.Ref, want)
	}
	g := req.Graph
	switch {
	case g.SHA == "":
		g.SHA = c.SHA
	case g.SHA != c.SHA:
		return nil, &principal.InvalidError{Field: "graph.sha", Reason: fmt.Sprintf("is %s, the token is for %s", g.SHA, c.SHA)}
	}
	if g.SHA == "" {
		return nil, &principal.InvalidError{Field: "graph.sha", Reason: "is required"}
	}
	switch {
	case g.Repo == "":
		g.Repo = repo.FullName
	case !strings.EqualFold(g.Repo, repo.FullName):
		return nil, &principal.InvalidError{Field: "graph.repo", Reason: fmt.Sprintf("is %s, the token belongs to %s", g.Repo, repo.FullName)}
	}
	resp := &v1.RecordGraphResponse{SHA: g.SHA}
	if repo.Suspended {
		resp.Reason = "the installation is suspended"
		return resp, nil
	}
	client, err := s.client(ctx, repo)
	if err != nil {
		return nil, err
	}
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, fmt.Sprintf("default-graph:%d", repo.ID)); err != nil {
			return err
		}
		head, err := client.GetRef(ctx, repo.FullName, "heads/"+repo.DefaultBranch)
		switch {
		case errors.Is(err, gh.ErrNotFound):
			resp.Reason = "the default branch has no head"
			return nil
		case err != nil:
			return fmt.Errorf("runs: head of %s: %w", repo.FullName, err)
		}
		resp.Head = head
		if head != g.SHA {
			resp.Reason = "superseded by a newer commit on the default branch"
			return nil
		}
		id, _, err := tx.SaveGraph(ctx, repo.ID, &g)
		if err != nil {
			return storeErr(err, "save graph of %s at %s", repo.FullName, shortSHA(g.SHA))
		}
		if err := tx.SetDefaultGraph(ctx, repo.ID, id); err != nil {
			return storeErr(err, "record default graph of %s", repo.FullName)
		}
		resp.Recorded = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}
