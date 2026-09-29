package runs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

// Reconcile catches up with workflow runs whose webhooks were lost. It
// resends dispatches whose workflow_dispatch call never succeeded, binds
// dispatches older than a minute to their workflow run by display title
// and job names as HandleWorkflowRun does, marks the stacks of dispatches
// unbound for longer than UnboundDispatchTimeout unknown unless a workflow
// run with their title is still running undecided, and closes dispatches
// whose workflow run completed without every stack reporting.
func (s *Service) Reconcile(ctx context.Context) error {
	open, err := s.st.OpenDispatches(ctx)
	if err != nil {
		return storeErr(err, "open dispatches")
	}
	now := s.now()
	byRepo := map[int64][]store.Dispatch{}
	var repoIDs []int64
	for _, d := range open {
		if _, ok := byRepo[d.RepoID]; !ok {
			repoIDs = append(repoIDs, d.RepoID)
		}
		byRepo[d.RepoID] = append(byRepo[d.RepoID], d)
	}
	var errs []error
	for _, id := range repoIDs {
		if err := s.reconcileRepo(ctx, id, byRepo[id], now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) reconcileRepo(ctx context.Context, repoID int64, dispatches []store.Dispatch, now time.Time) error {
	repo, err := s.st.GetRepo(ctx, repoID)
	if err != nil {
		return storeErr(err, "repository %d", repoID)
	}
	if repo.Suspended {
		return nil
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	var errs []error
	var unbound []store.Dispatch
	for _, d := range dispatches {
		age := now.Sub(d.DispatchedAt)
		switch {
		case d.SentAt == nil:
			if age < unboundGrace {
				continue
			}
			if age >= s.cfg.UnboundDispatchTimeout {
				errs = append(errs, s.timeOut(ctx, d))
				continue
			}
			errs = append(errs, s.resend(ctx, d))
		case d.WorkflowRunID == nil:
			if age >= unboundGrace {
				unbound = append(unbound, d)
			}
		default:
			errs = append(errs, s.reconcileBound(ctx, c, repo, d))
		}
	}
	if len(unbound) > 0 {
		errs = append(errs, s.bindByTitle(ctx, c, repo, unbound, now))
	}
	return errors.Join(errs...)
}

func (s *Service) reconcileBound(ctx context.Context, c *gh.Client, repo store.Repo, d store.Dispatch) error {
	wr, err := c.GetWorkflowRun(ctx, repo.FullName, *d.WorkflowRunID)
	if errors.Is(err, gh.ErrNotFound) {
		return s.completeDispatch(ctx, d, "not_found")
	}
	if err != nil {
		return fmt.Errorf("runs: workflow run %d: %w", *d.WorkflowRunID, err)
	}
	if wr.Status != gh.RunStatusCompleted {
		return nil
	}
	return s.completeDispatch(ctx, d, firstNonEmpty(wr.Conclusion, "unknown"))
}

func (s *Service) bindByTitle(ctx context.Context, c *gh.Client, repo store.Repo, unbound []store.Dispatch, now time.Time) error {
	oldest := unbound[0].DispatchedAt
	titles := map[string]bool{}
	for _, d := range unbound {
		if d.DispatchedAt.Before(oldest) {
			oldest = d.DispatchedAt
		}
		titles[formatDisplayTitle(d.Mode, d.RunID.String(), d.Wave)] = true
	}
	runs, err := c.ListWorkflowRuns(ctx, repo.FullName, gh.ListWorkflowRunsParams{
		Workflow: s.cfg.WorkflowFile, Event: "workflow_dispatch", CreatedAfter: oldest.Add(-unboundGrace),
	})
	if err != nil {
		return fmt.Errorf("runs: workflow runs of %s: %w", repo.FullName, err)
	}
	slices.SortStableFunc(runs, func(a, b gh.WorkflowRun) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	var errs []error
	waiting := map[uuid.UUID]bool{}
	for _, d := range unbound {
		waiting[d.ID] = true
	}
	bound := map[uuid.UUID]bool{}
	undecided := map[string]int{}
	for _, wr := range runs {
		if !titles[wr.DisplayTitle] {
			continue
		}
		prior, err := s.st.FindDispatchByWorkflowRun(ctx, wr.ID)
		switch {
		case err == nil:
			if waiting[prior.ID] {
				bound[prior.ID] = true
			}
			continue
		case !errors.Is(err, store.ErrNotFound):
			errs = append(errs, storeErr(err, "dispatch of workflow run %d", wr.ID))
			continue
		}
		d, found, err := s.dispatchForWorkflowRun(ctx, repo, wr, "")
		if err != nil {
			errs = append(errs, err)
		}
		if !found {
			if wr.Status != gh.RunStatusCompleted {
				undecided[wr.DisplayTitle]++
			}
			continue
		}
		bound[d.ID] = true
		if wr.Status == gh.RunStatusCompleted {
			errs = append(errs, s.completeDispatch(ctx, d, firstNonEmpty(wr.Conclusion, "unknown")))
		}
	}
	for _, d := range unbound {
		title := formatDisplayTitle(d.Mode, d.RunID.String(), d.Wave)
		if bound[d.ID] || undecided[title] > 0 || now.Sub(d.DispatchedAt) < s.cfg.UnboundDispatchTimeout {
			continue
		}
		errs = append(errs, s.timeOut(ctx, d))
	}
	return errors.Join(errs...)
}

func (s *Service) timeOut(ctx context.Context, d store.Dispatch) error {
	if _, err := s.st.CompleteDispatch(ctx, d.ID, "timed_out"); err != nil {
		return storeErr(err, "complete dispatch %s", d.ID)
	}
	return s.markVanished(ctx, d, "workflow run not found; check that .github/workflows/"+s.cfg.WorkflowFile+" exists on the default branch")
}

// SyncInstallations reconciles the stored installations and repositories
// with the App's installations on GitHub. Every installation is recorded
// with its suspension state, and the repositories of the active ones are
// recorded as HandleInstallation records them. stackorder.yaml is loaded
// only for repositories it has not seen, or whose default branch had no
// commit to read it at, so a sync makes no configuration reads for known
// repositories, with the file or without it; push events keep those
// current. It learns installations made while the server ran in setup mode
// and repairs installation webhooks lost during downtime. It never forgets
// an installation or a repository; only their deletion webhooks do. An
// installation that fails does not stop the others, and every failure is
// returned.
func (s *Service) SyncInstallations(ctx context.Context) error {
	insts, err := s.gh.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("runs: list installations: %w", err)
	}
	var errs []error
	for _, inst := range insts {
		if err := s.syncInstallation(ctx, inst); err != nil {
			errs = append(errs, err)
		}
	}
	s.log.InfoContext(ctx, "installations synced", "installations", len(insts), "failed", len(errs))
	return errors.Join(errs...)
}

func (s *Service) syncInstallation(ctx context.Context, inst gh.Installation) error {
	if inst.ID == 0 {
		return nil
	}
	if _, err := s.st.UpsertInstallation(ctx, store.Installation{ID: inst.ID, Account: inst.Account.Login, AccountType: inst.Account.Type}); err != nil {
		return storeErr(err, "record installation %d", inst.ID)
	}
	suspended := inst.SuspendedAt != nil
	if err := s.st.SuspendInstallation(ctx, inst.ID, suspended); err != nil {
		return storeErr(err, "record suspension of installation %d", inst.ID)
	}
	if suspended {
		return nil
	}
	repos, err := s.gh.InstallationRepos(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("runs: repositories of installation %d: %w", inst.ID, err)
	}
	return s.recordRepos(ctx, inst.ID, repos, func(r store.Repo) bool { return r.Config == nil && r.ConfigSHA == "" })
}

// RemindStaleLocks posts a reminder on closed pull requests that still
// hold orchestration locks taken more than a day ago, at most once a day
// per lock.
func (s *Service) RemindStaleLocks(ctx context.Context) error {
	locks, err := s.st.ListLocks(ctx, 0)
	if err != nil {
		return storeErr(err, "locks")
	}
	now := s.now()
	type prKey struct {
		repo int64
		pr   int
	}
	due := map[prKey][]store.Lock{}
	var order []prKey
	for _, l := range locks {
		if l.PRNumber == 0 || now.Sub(l.TakenAt) < staleLockAge {
			continue
		}
		n, err := s.st.CountAudit(ctx, "lock_reminder", reminderTarget(l, now), time.Time{})
		if err != nil {
			return storeErr(err, "reminders of lock %s", l.StackID)
		}
		if n > 0 {
			continue
		}
		k := prKey{l.RepoID, l.PRNumber}
		if _, ok := due[k]; !ok {
			order = append(order, k)
		}
		due[k] = append(due[k], l)
	}
	var errs []error
	for _, k := range order {
		errs = append(errs, s.remind(ctx, k.repo, k.pr, due[k]))
	}
	return errors.Join(errs...)
}

func (s *Service) remind(ctx context.Context, repoID int64, pr int, locks []store.Lock) error {
	repo, err := s.st.GetRepo(ctx, repoID)
	if err != nil {
		return storeErr(err, "repository %d", repoID)
	}
	if repo.Suspended {
		return nil
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	pull, err := c.GetPull(ctx, repo.FullName, pr)
	if err != nil {
		return fmt.Errorf("runs: pull request %s#%d: %w", repo.FullName, pr, err)
	}
	if pull.State != gh.IssueClosed {
		return nil
	}
	opts, err := s.reportOptions(ctx, s.st, repo, pr)
	if err != nil {
		return err
	}
	if _, err := c.CreateIssueComment(ctx, repo.FullName, pr, report.LockWarningComment(lockInfos(locks), opts)); err != nil {
		return fmt.Errorf("runs: remind %s#%d of its locks: %w", repo.FullName, pr, err)
	}
	for _, l := range locks {
		s.audit(ctx, "", "lock_reminder", reminderTarget(l, s.now()), map[string]any{"repo": repo.FullName, "pr": pr, "stack": l.StackKey})
	}
	return nil
}

func reminderTarget(l store.Lock, now time.Time) string {
	return "lock:" + l.StackID.String() + ":" + now.Format("2006-01-02")
}

// Prune applies the retention policy: plan text older than planText,
// webhook events and finished jobs older than events, drift history older
// than drift, expired OIDC token ids and sessions. A zero duration keeps
// that kind forever.
func (s *Service) Prune(ctx context.Context, planText, events, drift time.Duration) error {
	res, err := s.st.Prune(ctx, store.Retention{PlanText: planText, Events: events, Drift: drift})
	if err != nil {
		return fmt.Errorf("runs: prune: %w", err)
	}
	s.log.InfoContext(ctx, "pruned", "plan_text", res.PlanText, "events", res.Events, "jobs", res.Jobs,
		"drift", res.Drift, "jtis", res.JTIs, "sessions", res.Sessions)
	return nil
}

// CanActOnRepo reports whether login has push permission on the
// repository, cached for TeamCacheTTL.
func (s *Service) CanActOnRepo(ctx context.Context, login string, repoID int64) (bool, error) {
	repo, err := s.st.GetRepo(ctx, repoID)
	if err != nil {
		return false, storeErr(err, "repository %d", repoID)
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return false, err
	}
	perm, err := s.permission(ctx, c, repo, login)
	if err != nil {
		return false, err
	}
	return gh.HasPushPermission(perm), nil
}

func (s *Service) authorizeActor(ctx context.Context, actor string, repo store.Repo) error {
	if actor == "" {
		return principal.Wrap(principal.ErrForbidden, "an actor is required")
	}
	if strings.HasPrefix(actor, "apikey:") {
		return nil
	}
	ok, err := s.CanActOnRepo(ctx, actor, repo.ID)
	if err != nil {
		return err
	}
	if !ok {
		return principal.Wrap(principal.ErrForbidden, "%s needs write access to %s", actor, repo.FullName)
	}
	return nil
}

// Unlock releases the orchestration lock on a stack for a human with push
// permission, or for an API key, audits it and tells the pull request that
// held it. ForceState only annotates the record: the server never touches
// Terraform state. Unlocking a stack that holds no lock releases nothing.
func (s *Service) Unlock(ctx context.Context, actor string, stackID string, req v1.UnlockRequest) (*v1.UnlockResponse, error) {
	id, err := uuid.Parse(strings.TrimSpace(stackID))
	if err != nil {
		return nil, &principal.InvalidError{Field: "stack_id", Reason: fmt.Sprintf("%q is not a stack id", stackID)}
	}
	stack, err := s.st.GetStack(ctx, id)
	if err != nil {
		return nil, storeErr(err, "stack %s", stackID)
	}
	repo, err := s.st.GetRepo(ctx, stack.RepoID)
	if err != nil {
		return nil, storeErr(err, "repository of stack %s", stackID)
	}
	if err := s.authorizeActor(ctx, actor, repo); err != nil {
		return nil, err
	}
	resp := &v1.UnlockResponse{Released: []v1.LockInfo{}}
	lock, err := s.st.ReleaseLock(ctx, stack.ID)
	if errors.Is(err, store.ErrNotFound) {
		return resp, nil
	}
	if err != nil {
		return nil, storeErr(err, "release lock of %s", stack.Key)
	}
	resp.Released = append(resp.Released, lock.ToV1())
	s.audit(ctx, actor, "unlock", "stack:"+stack.ID.String(), map[string]any{
		"repo": repo.FullName, "stack": stack.Key, "pr": lock.PRNumber, "run_id": lock.RunID.String(),
		"reason": req.Reason, "force_state": req.ForceState,
	})
	s.refreshLocksGauge(ctx)
	if lock.PRNumber > 0 {
		body := report.UnlockedComment(resp.Released, actor)
		if req.Reason != "" {
			body += "\nReason given: " + report.Escape(req.Reason) + "\n"
		}
		if req.ForceState {
			body += "\nThe unlock was marked `--force-state`: a runner may have died mid-apply, so check the S3 state lock of `" +
				stack.Key + "` before the next apply. Stackorder never touches Terraform state itself.\n"
		}
		s.comment(ctx, repo, lock.PRNumber, body)
	}
	return resp, nil
}

// UnlockByKey is Unlock for a stack addressed by repository and key.
func (s *Service) UnlockByKey(ctx context.Context, actor string, req v1.UnlockRequest) (*v1.UnlockResponse, error) {
	if req.Repo == "" || req.StackKey == "" {
		return nil, &principal.InvalidError{Field: "stack_key", Reason: "repo and stack_key are required"}
	}
	repo, err := s.st.GetRepoByName(ctx, req.Repo)
	if err != nil {
		return nil, storeErr(err, "repository %s", req.Repo)
	}
	stack, err := s.st.GetStackByKey(ctx, repo.ID, config.NormalizePath(req.StackKey))
	if err != nil {
		return nil, storeErr(err, "stack %s of %s", req.StackKey, repo.FullName)
	}
	return s.Unlock(ctx, actor, stack.ID.String(), req)
}

// Rerun plans every stack of a plan run again, in a new run dispatched
// with mode plan, and returns that run.
func (s *Service) Rerun(ctx context.Context, actor string, runID string) (*v1.Run, error) {
	run, repo, err := s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Mode != v1.ModePlan {
		return nil, &principal.InvalidError{Field: "run_id", Reason: fmt.Sprintf("run %s is a %s run; only plans are re-run", run.ID, run.Mode)}
	}
	if run.Status == v1.RunSuperseded {
		return nil, principal.Wrap(principal.ErrSuperseded, "run %s was superseded by a newer commit", run.ID)
	}
	if err := s.authorizeActor(ctx, actor, repo); err != nil {
		return nil, err
	}
	rows, err := s.st.GetRunStacks(ctx, run.ID)
	if err != nil {
		return nil, storeErr(err, "stacks of run %s", run.ID)
	}
	if run.GraphID == nil || len(rows) == 0 {
		return nil, principal.Wrap(principal.ErrConflict, "run %s has no resolved stacks to plan again", run.ID)
	}
	g, err := s.st.GetGraphByID(ctx, *run.GraphID)
	if err != nil {
		return nil, storeErr(err, "graph of run %s", run.ID)
	}
	prior := map[string]store.RunStack{}
	keys := make([]string, len(rows))
	for i, rs := range rows {
		prior[rs.Key] = rs
		keys[i] = rs.Key
	}
	next, refusal, err := s.startServerPlan(ctx, serverPlan{
		repo: repo, pr: run.PRNumber, sha: run.SHA, baseSHA: run.BaseSHA, graph: g, graphID: *run.GraphID,
		keys: keys, prior: prior, trigger: v1.TriggerRerequest, requester: actor,
	})
	if err != nil {
		return nil, err
	}
	if refusal != "" {
		return nil, principal.Wrap(principal.ErrConflict, "%s", refusal)
	}
	s.audit(ctx, actor, "rerun", "run:"+run.ID.String(), map[string]any{"repo": repo.FullName, "new_run_id": next.ID.String()})
	detail, err := s.st.RunDetail(ctx, next.ID)
	if err != nil {
		return nil, storeErr(err, "run %s", next.ID)
	}
	return &detail, nil
}
