package runs

import (
	"context"
	"errors"
	"strconv"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	eventPullRequest      = "pull_request"
	eventWorkflowDispatch = "workflow_dispatch"
)

func claimsOf(p principal.Principal) (*oidc.Claims, error) {
	if p.Kind != principal.OIDC || p.Claims == nil {
		return nil, principal.Wrap(principal.ErrForbidden, "a GitHub Actions OIDC token is required")
	}
	return p.Claims, nil
}

func checkRepoClaims(c *oidc.Claims, repo store.Repo) error {
	if !strings.EqualFold(c.Repository, repo.FullName) {
		return principal.Wrap(principal.ErrForbidden, "claim repository is %q, want %q", c.Repository, repo.FullName)
	}
	if want := strconv.FormatInt(repo.ID, 10); c.RepositoryID != want {
		return principal.Wrap(principal.ErrForbidden, "claim repository_id is %q, want %q", c.RepositoryID, want)
	}
	return nil
}

func checkDispatchRef(c *oidc.Claims, repo store.Repo) error {
	if want := "refs/heads/" + repo.DefaultBranch; c.Ref != want {
		return principal.Wrap(principal.ErrForbidden, "claim ref is %q, want %q", c.Ref, want)
	}
	return nil
}

func checkPullClaims(c *oidc.Claims, run store.Run) error {
	if run.Trigger != v1.TriggerPullRequest || run.Mode != v1.ModePlan {
		return principal.Wrap(principal.ErrForbidden, "run %s is a %s run started by %s; only the jobs the server dispatched for it may use it", run.ID, run.Mode, run.Trigger)
	}
	pr, ok := c.PullRequestNumber()
	if !ok || run.PRNumber == 0 || pr != run.PRNumber {
		return principal.Wrap(principal.ErrForbidden, "claim ref is %q, want %q", c.Ref, "refs/pull/"+strconv.Itoa(run.PRNumber)+"/merge")
	}
	return nil
}

func jobEnvironment(run store.Run, row *store.RunStack) string {
	if run.Mode == v1.ModeApply && row != nil {
		return row.Environment
	}
	return v1.DefaultEnvironment
}

func (s *Service) bindJob(ctx context.Context, run store.Run, repo store.Repo, c *oidc.Claims, row *store.RunStack) error {
	if err := checkRepoClaims(c, repo); err != nil {
		return err
	}
	if err := checkDispatchRef(c, repo); err != nil {
		return err
	}
	wr, err := c.RunIDInt()
	if err != nil {
		return principal.Wrap(principal.ErrForbidden, "claim run_id: %v", err)
	}
	env := c.Environment
	if row != nil || run.Mode != v1.ModeApply {
		want := jobEnvironment(run, row)
		if !strings.EqualFold(c.Environment, want) {
			return principal.Wrap(principal.ErrForbidden, "claim environment is %q, want %q", c.Environment, want)
		}
		env = want
	}
	if err := s.bindDispatch(ctx, run, row, env, wr); err != nil {
		return err
	}
	attempt, _ := strconv.Atoi(c.RunAttempt)
	if run.WorkflowRunID != wr || run.WorkflowRunAttempt != attempt {
		if err := s.st.SetRunWorkflowRun(ctx, run.ID, wr, attempt); err != nil {
			return storeErr(err, "record workflow run of run %s", run.ID)
		}
	}
	return nil
}

func (s *Service) bindDispatch(ctx context.Context, run store.Run, row *store.RunStack, env string, wr int64) error {
	for range 2 {
		dispatches, err := s.st.ListDispatches(ctx, run.ID)
		if err != nil {
			return storeErr(err, "dispatches of run %s", run.ID)
		}
		var candidates []store.Dispatch
		for _, d := range dispatches {
			switch {
			case d.WorkflowRunID != nil && *d.WorkflowRunID == wr:
				if !strings.EqualFold(d.Environment, env) || d.Mode != run.Mode {
					return principal.Wrap(principal.ErrForbidden,
						"claim run_id %d is the workflow run of the %s dispatch in environment %s", wr, d.Mode, d.Environment)
				}
				if d.CompletedAt != nil {
					return principal.Wrap(principal.ErrForbidden, "workflow run %d already completed", wr)
				}
				if row != nil && row.DispatchID != nil && *row.DispatchID != d.ID {
					return principal.Wrap(principal.ErrForbidden,
						"claim run_id %d is the workflow run of another dispatch than the one that carries %s", wr, row.Key)
				}
				return nil
			case d.Mode != run.Mode || !strings.EqualFold(d.Environment, env):
				continue
			case row != nil && row.DispatchID != nil:
				if *row.DispatchID == d.ID {
					candidates = append(candidates, d)
				}
			case row != nil:
				if d.Wave == row.Wave || run.Mode != v1.ModeApply {
					candidates = append(candidates, d)
				}
			case d.Wave == run.CurrentWave || run.Mode != v1.ModeApply:
				candidates = append(candidates, d)
			}
		}
		var unbound []store.Dispatch
		for _, d := range candidates {
			if d.WorkflowRunID == nil && d.CompletedAt == nil {
				unbound = append(unbound, d)
			}
		}
		if len(unbound) == 0 {
			return principal.Wrap(principal.ErrForbidden,
				"claim run_id %d matches no workflow run dispatched for run %s in environment %s", wr, run.ID, env)
		}
		if len(unbound) > 1 && row == nil {
			return nil
		}
		err = s.st.SetDispatchWorkflowRun(ctx, unbound[0].ID, wr)
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, store.ErrConflict):
			return storeErr(err, "bind dispatch %s", unbound[0].ID)
		}
	}
	return principal.Wrap(principal.ErrForbidden, "claim run_id %d could not be bound to run %s", wr, run.ID)
}
