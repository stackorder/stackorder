package runs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

var errLockConflict = errors.New("runs: lock conflict")

// CreateRun finds or creates the run a runner job belongs to. A plan job
// of a pull request registers the plan run of its head commit, a job the
// server dispatched binds itself to its dispatch, and an API key starts a
// manual apply run after taking the locks of its stacks.
func (s *Service) CreateRun(ctx context.Context, p principal.Principal, req v1.CreateRunRequest) (*v1.CreateRunResponse, error) {
	switch p.Kind {
	case principal.OIDC:
		c, err := claimsOf(p)
		if err != nil {
			return nil, err
		}
		switch c.EventName {
		case eventPullRequest:
			return s.createPlanRun(ctx, c, req)
		case eventWorkflowDispatch:
			return s.registerDispatchedJob(ctx, c, req)
		}
		return nil, principal.Wrap(principal.ErrForbidden, "claim event_name %q cannot start runs", c.EventName)
	case principal.APIKey:
		return s.createManualRun(ctx, p, req)
	}
	return nil, principal.Wrap(principal.ErrForbidden, "%s principals cannot start runs", p.Kind)
}

func (s *Service) createPlanRun(ctx context.Context, c *oidc.Claims, req v1.CreateRunRequest) (*v1.CreateRunResponse, error) {
	if req.Mode != "" && req.Mode != v1.ModePlan {
		return nil, principal.Wrap(principal.ErrForbidden, "a pull_request job may only start plan runs")
	}
	if !strings.EqualFold(req.Repo, c.Repository) {
		return nil, principal.Wrap(principal.ErrForbidden, "claim repository is %q, the request names %q", c.Repository, req.Repo)
	}
	repo, err := s.st.GetRepoByName(ctx, req.Repo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, principal.Wrap(principal.ErrForbidden, "repository %s is not installed", req.Repo)
		}
		return nil, storeErr(err, "repository %s", req.Repo)
	}
	if err := checkRepoClaims(c, repo); err != nil {
		return nil, err
	}
	pr, ok := c.PullRequestNumber()
	if !ok || pr != req.PRNumber {
		return nil, principal.Wrap(principal.ErrForbidden, "claim ref is %q, the request names pull request %d", c.Ref, req.PRNumber)
	}
	if req.SHA == "" {
		return nil, &principal.InvalidError{Field: "sha", Reason: "is required"}
	}
	cl, err := s.client(ctx, repo)
	if err != nil {
		return nil, err
	}
	head, err := s.pullHead(ctx, cl, repo, pr)
	if err != nil {
		return nil, err
	}
	if head != req.SHA {
		return nil, principal.Wrap(principal.ErrSuperseded, "pull request #%d is at %s, not %s", pr, shortSHA(head), shortSHA(req.SHA))
	}
	wr, _ := c.RunIDInt()
	attempt, _ := strconv.Atoi(c.RunAttempt)
	params := store.CreateRunParams{
		RepoID: repo.ID, SHA: req.SHA, BaseSHA: req.BaseSHA, PRNumber: pr,
		Trigger: v1.TriggerPullRequest, Mode: v1.ModePlan, Status: v1.RunPending,
		RequestedBy: c.Actor, WorkflowRunID: wr, WorkflowRunAttempt: attempt,
	}
	run, created, err := s.findOrCreatePlanRun(ctx, params)
	if err != nil {
		return nil, err
	}
	if created {
		s.m.RunStatusChanged(run.Status, run.Trigger, run.Mode)
		s.supersede(ctx, repo, pr, head)
	} else if wr != 0 && (run.WorkflowRunID != wr || run.WorkflowRunAttempt != attempt) {
		if err := s.st.SetRunWorkflowRun(ctx, run.ID, wr, attempt); err != nil {
			return nil, storeErr(err, "record workflow run of run %s", run.ID)
		}
	}
	return s.createResponse(ctx, run.ID, !created)
}

func (s *Service) findOrCreatePlanRun(ctx context.Context, p store.CreateRunParams) (store.Run, bool, error) {
	var (
		out     store.Run
		created bool
	)
	key := fmt.Sprintf("run:%d:%s:%d:%s", p.RepoID, p.SHA, p.PRNumber, p.Mode)
	err := s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, key); err != nil {
			return err
		}
		run, err := tx.FindOpenRun(ctx, p.RepoID, p.SHA, p.PRNumber, p.Mode)
		switch {
		case err == nil && reusablePlanRun(run, p):
			out = run
			return nil
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return err
		}
		out, err = tx.CreateRun(ctx, p)
		created = err == nil
		return err
	})
	if err != nil {
		return store.Run{}, false, storeErr(err, "plan run for %s", shortSHA(p.SHA))
	}
	return out, created, nil
}

func reusablePlanRun(run store.Run, p store.CreateRunParams) bool {
	if run.Trigger != v1.TriggerPullRequest {
		return false
	}
	if run.Status == v1.RunPending || run.Status == v1.RunPlanning {
		return true
	}
	return run.WorkflowRunID == p.WorkflowRunID && run.WorkflowRunAttempt == p.WorkflowRunAttempt
}

func (s *Service) createResponse(ctx context.Context, id uuid.UUID, existing bool) (*v1.CreateRunResponse, error) {
	detail, err := s.st.RunDetail(ctx, id)
	if err != nil {
		return nil, storeErr(err, "run %s", id)
	}
	return &v1.CreateRunResponse{RunID: detail.ID, Status: detail.Status, Existing: existing, Run: &detail}, nil
}

func (s *Service) supersede(ctx context.Context, repo store.Repo, pr int, head string) {
	runs, _, err := s.st.ListRuns(ctx, store.RunFilter{RepoID: repo.ID, PRNumber: pr, Mode: v1.ModePlan, Limit: 500})
	if err != nil {
		s.log.WarnContext(ctx, "list runs to supersede", "repo", repo.FullName, "pr", pr, "error", err)
		return
	}
	for _, r := range runs {
		if r.SHA == head || r.Status.Terminal() {
			continue
		}
		moved, err := s.st.UpdateRunStatus(ctx, r.ID, v1.RunSuperseded, nonTerminalRunStatuses...)
		if err != nil {
			if !errors.Is(err, store.ErrConflict) {
				s.log.WarnContext(ctx, "supersede run", "run_id", r.ID, "error", err)
			}
			continue
		}
		s.m.RunStatusChanged(moved.Status, moved.Trigger, moved.Mode)
		s.renderQuiet(ctx, r.ID, renderOpts{supersededBy: head})
	}
}

var nonTerminalRunStatuses = []v1.RunStatus{v1.RunPending, v1.RunPlanning, v1.RunPlanned, v1.RunApplying}

func (s *Service) registerDispatchedJob(ctx context.Context, c *oidc.Claims, req v1.CreateRunRequest) (*v1.CreateRunResponse, error) {
	if req.RunID == "" {
		return nil, &principal.InvalidError{Field: "run_id", Reason: "is required for a workflow_dispatch job"}
	}
	run, repo, err := s.loadRun(ctx, req.RunID)
	if err != nil {
		return nil, err
	}
	if req.Repo != "" && !strings.EqualFold(req.Repo, repo.FullName) {
		return nil, principal.Wrap(principal.ErrForbidden, "run %s belongs to %s, not %s", run.ID, repo.FullName, req.Repo)
	}
	if err := s.bindJob(ctx, run, repo, c, nil); err != nil {
		return nil, err
	}
	return s.createResponse(ctx, run.ID, true)
}

// GetRunForPrincipal returns a run to the runner job asking for it. A job
// the server dispatched is bound to its dispatch on first contact, and a
// job of another workflow run is refused.
func (s *Service) GetRunForPrincipal(ctx context.Context, p principal.Principal, runID string) (*v1.Run, error) {
	run, repo, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	switch p.Kind {
	case principal.OIDC:
		c, err := claimsOf(p)
		if err != nil {
			return nil, err
		}
		switch c.EventName {
		case eventPullRequest:
			if err := checkRepoClaims(c, repo); err != nil {
				return nil, err
			}
			if err := checkPullClaims(c, run); err != nil {
				return nil, err
			}
		case eventWorkflowDispatch:
			if err := s.bindJob(ctx, run, repo, c, nil); err != nil {
				return nil, err
			}
		default:
			return nil, principal.Wrap(principal.ErrForbidden, "claim event_name %q cannot read runs", c.EventName)
		}
	case principal.APIKey, principal.Session:
	default:
		return nil, principal.Wrap(principal.ErrForbidden, "%s principals cannot read runs", p.Kind)
	}
	detail, err := s.st.RunDetail(ctx, run.ID)
	if err != nil {
		return nil, storeErr(err, "run %s", run.ID)
	}
	return &detail, nil
}

func (s *Service) createManualRun(ctx context.Context, p principal.Principal, req v1.CreateRunRequest) (*v1.CreateRunResponse, error) {
	switch {
	case req.Trigger != v1.TriggerManual:
		return nil, &principal.InvalidError{Field: "trigger", Reason: "an API key starts manual runs only"}
	case req.Mode != v1.ModeApply:
		return nil, &principal.InvalidError{Field: "mode", Reason: "a manual run applies"}
	case len(req.Stacks) == 0:
		return nil, &principal.InvalidError{Field: "stacks", Reason: "name at least one stack"}
	case req.SHA == "":
		return nil, &principal.InvalidError{Field: "sha", Reason: "is required"}
	}
	repo, err := s.st.GetRepoByName(ctx, req.Repo)
	if err != nil {
		return nil, storeErr(err, "repository %s", req.Repo)
	}
	var stacks []store.Stack
	seen := map[string]bool{}
	for _, raw := range req.Stacks {
		key := config.NormalizePath(raw)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		st, err := s.st.GetStackByKey(ctx, repo.ID, key)
		if errors.Is(err, store.ErrNotFound) {
			return nil, &principal.InvalidError{Field: "stacks", Reason: fmt.Sprintf("%s is not a stack of %s", key, repo.FullName)}
		}
		if err != nil {
			return nil, storeErr(err, "stack %s", key)
		}
		stacks = append(stacks, st)
	}
	if len(stacks) == 0 {
		return nil, &principal.InvalidError{Field: "stacks", Reason: "name at least one stack"}
	}
	graphID, _ := s.currentGraphID(ctx, repo.ID)
	var (
		run       store.Run
		conflicts []store.Lock
	)
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		var err error
		run, err = tx.CreateRun(ctx, store.CreateRunParams{
			RepoID: repo.ID, SHA: req.SHA, BaseSHA: req.BaseSHA, Trigger: v1.TriggerManual,
			Mode: v1.ModeApply, Status: v1.RunPending, RequestedBy: p.Actor(),
		})
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(stacks))
		rows := make([]store.RunStack, len(stacks))
		for i, st := range stacks {
			ids[i] = st.ID
			rows[i] = store.RunStack{StackID: st.ID, Status: v1.StackPlanned, Mode: v1.ModeApply, Environment: st.Environment}
		}
		conflicts, err = tx.TryLockStacks(ctx, ids, run.ID, 0, "manual apply by "+p.Actor())
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return errLockConflict
		}
		if err := tx.UpsertRunStacks(ctx, run.ID, rows); err != nil {
			return err
		}
		if graphID != uuid.Nil {
			if err := tx.SetRunGraph(ctx, run.ID, graphID, 1, nil); err != nil {
				return err
			}
		}
		run, err = tx.UpdateRunStatus(ctx, run.ID, v1.RunApplying)
		return err
	})
	if errors.Is(err, errLockConflict) {
		return nil, &principal.LockedError{Conflicts: lockInfos(conflicts)}
	}
	if err != nil {
		return nil, storeErr(err, "manual run on %s", repo.FullName)
	}
	s.m.RunStatusChanged(v1.RunPending, run.Trigger, run.Mode)
	s.m.RunStatusChanged(run.Status, run.Trigger, run.Mode)
	s.refreshLocksGauge(ctx)
	s.audit(ctx, p.Actor(), "manual_apply", "run:"+run.ID.String(), map[string]any{"repo": repo.FullName, "stacks": keysOf(stacks)})
	return s.createResponse(ctx, run.ID, false)
}

func keysOf(stacks []store.Stack) []string {
	out := make([]string, len(stacks))
	for i, st := range stacks {
		out[i] = st.Key
	}
	return out
}

func lockInfos(locks []store.Lock) []v1.LockInfo {
	out := make([]v1.LockInfo, len(locks))
	for i, l := range locks {
		out[i] = l.ToV1()
	}
	return out
}

func (s *Service) currentGraph(ctx context.Context, repoID int64) (*v1.Graph, uuid.UUID, error) {
	g, id, err := s.st.GetDefaultGraph(ctx, repoID)
	if errors.Is(err, store.ErrNotFound) {
		g, id, err = s.st.LatestGraph(ctx, repoID)
	}
	return g, id, err
}

func (s *Service) currentGraphID(ctx context.Context, repoID int64) (uuid.UUID, error) {
	_, id, err := s.currentGraph(ctx, repoID)
	return id, err
}
