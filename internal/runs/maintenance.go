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
// whose workflow run completed without every stack reporting. An apply run
// still pending without any dispatch two minutes after it was created,
// which a failure between its creation and its first dispatch leaves
// behind, has its wave 0 dispatched while its plans are still current, and
// is otherwise failed with a comment on its pull request and its locks
// released.
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
	errs = append(errs, s.recoverStalledApplies(ctx, now))
	return errors.Join(errs...)
}

func (s *Service) recoverStalledApplies(ctx context.Context, now time.Time) error {
	pending, _, err := s.st.ListRuns(ctx, store.RunFilter{Status: v1.RunPending, Mode: v1.ModeApply, Limit: 500})
	if err != nil {
		return storeErr(err, "pending apply runs")
	}
	var errs []error
	for _, run := range pending {
		if run.Trigger == v1.TriggerManual || now.Sub(run.CreatedAt) < stalledApplyAge {
			continue
		}
		dispatches, err := s.st.ListDispatches(ctx, run.ID)
		if err != nil {
			errs = append(errs, storeErr(err, "dispatches of run %s", run.ID))
			continue
		}
		if len(dispatches) > 0 {
			continue
		}
		errs = append(errs, s.recoverApply(ctx, run))
	}
	return errors.Join(errs...)
}

func (s *Service) recoverApply(ctx context.Context, run store.Run) error {
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return storeErr(err, "repository of run %s", run.ID)
	}
	if repo.Suspended {
		return nil
	}
	reason, err := s.staleApply(ctx, repo, run)
	if err != nil {
		return err
	}
	if reason == "" {
		s.log.InfoContext(ctx, "dispatching an apply left without a dispatch", "run_id", run.ID)
		return s.dispatchWave(ctx, run.ID, 0)
	}
	return s.abandonApply(ctx, repo, run, reason)
}

func (s *Service) staleApply(ctx context.Context, repo store.Repo, run store.Run) (string, error) {
	if run.PRNumber <= 0 {
		return "", nil
	}
	applies, _, err := s.st.ListRuns(ctx, store.RunFilter{RepoID: repo.ID, PRNumber: run.PRNumber, Mode: v1.ModeApply, Limit: 20})
	if err != nil {
		return "", storeErr(err, "apply runs of #%d", run.PRNumber)
	}
	for _, r := range applies {
		if r.ID != run.ID && r.CreatedAt.After(run.CreatedAt) {
			return fmt.Sprintf("apply run %s of this pull request started after it", r.ID), nil
		}
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return "", err
	}
	pull, err := c.GetPull(ctx, repo.FullName, run.PRNumber)
	if err != nil {
		return "", fmt.Errorf("runs: pull request %s#%d: %w", repo.FullName, run.PRNumber, err)
	}
	if run.Trigger == v1.TriggerComment {
		switch {
		case pull.Merged || pull.MergedAt != nil || pull.State != gh.IssueOpen:
			return "the pull request is no longer open", nil
		case pull.HeadSHA != run.SHA:
			return fmt.Sprintf("the pull request moved on to %s, so the plans of %s are no longer current", shortSHA(pull.HeadSHA), shortSHA(run.SHA)), nil
		}
	}
	view, err := planViewAt(ctx, s.st, repo.ID, run.PRNumber, pull.HeadSHA)
	if err != nil {
		return "", err
	}
	rows, err := s.st.GetRunStacks(ctx, run.ID)
	if err != nil {
		return "", storeErr(err, "stacks of run %s", run.ID)
	}
	var changed []string
	for _, rs := range rows {
		if rs.Status != v1.StackPlanned {
			continue
		}
		plan, ok := view.stored[rs.Key]
		if !ok || plan.Status != v1.StackPlanned || plan.PlanArtifact != rs.PlanArtifact || plan.PlanRunID != rs.PlanRunID {
			changed = append(changed, "`"+rs.Key+"`")
		}
	}
	if len(changed) > 0 {
		return "the plans of " + strings.Join(changed, ", ") + " changed since the apply was requested", nil
	}
	return "", nil
}

func (s *Service) abandonApply(ctx context.Context, repo store.Repo, run store.Run, reason string) error {
	moved, err := s.transition(ctx, run, v1.RunFailed, v1.RunPending)
	if err != nil || !moved {
		return err
	}
	s.log.WarnContext(ctx, "abandoning an apply left without a dispatch", "run_id", run.ID, "reason", reason)
	if err := s.st.AddRunWarning(ctx, run.ID, "the apply was never dispatched and was abandoned: "+reason); err != nil {
		return storeErr(err, "record warning on run %s", run.ID)
	}
	released, err := s.st.ReleaseLocksForRun(ctx, run.ID)
	if err != nil {
		return storeErr(err, "release locks of run %s", run.ID)
	}
	for _, l := range released {
		s.audit(ctx, "", "unlock", v1.QualifiedStackKey(repo.FullName, l.StackKey), map[string]any{
			"repo": repo.FullName, "stack": l.StackKey, "stack_id": l.StackID.String(), "pr": l.PRNumber,
			"run_id": run.ID.String(), "reason": "apply abandoned: " + reason,
		})
	}
	if len(released) > 0 {
		s.refreshLocksGauge(ctx)
	}
	if run.PRNumber > 0 {
		s.comment(ctx, repo, run.PRNumber, abandonedApplyComment(run, reason, len(released)))
	}
	s.renderQuiet(ctx, run.ID, renderOpts{allStacks: true})
	return nil
}

func abandonedApplyComment(run store.Run, reason string, released int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**The apply of `%s` did not start.** Stackorder recorded apply run %s but never dispatched it, and %s, so it was abandoned.",
		shortSHA(run.SHA), run.ID, reason)
	if released > 0 {
		fmt.Fprintf(&b, " Its %d orchestration lock(s) were released.", released)
	}
	if run.Trigger == v1.TriggerComment {
		b.WriteString(" Comment `stackorder apply` again to apply the current plans.")
	}
	b.WriteString("\n")
	return b.String()
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
// and repairs installation webhooks lost during downtime, deletion ones
// included: when GitHub listed every installation and the repositories of
// every active one, stored installations it no longer lists are forgotten,
// and so are stored repositories of a listed installation that no listing
// names. When any listing fails nothing is forgotten. An installation that
// fails does not stop the others, and every failure is returned.
func (s *Service) SyncInstallations(ctx context.Context) error {
	insts, err := s.gh.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("runs: list installations: %w", err)
	}
	var errs []error
	listing := installationListing{installations: map[int64]bool{}, listed: map[int64]bool{}, repos: map[int64]bool{}}
	complete := true
	for _, inst := range insts {
		if inst.ID == 0 {
			continue
		}
		listing.installations[inst.ID] = true
		repos, listed, err := s.syncInstallation(ctx, inst)
		if err != nil {
			errs = append(errs, err)
		}
		if !listed && inst.SuspendedAt == nil {
			complete = false
			continue
		}
		if listed {
			listing.listed[inst.ID] = true
		}
		for _, r := range repos {
			listing.repos[r.ID] = true
		}
	}
	forgotten := 0
	if complete {
		n, err := s.forgetUnlisted(ctx, listing)
		if err != nil {
			errs = append(errs, err)
		}
		forgotten = n
	}
	s.log.InfoContext(ctx, "installations synced", "installations", len(insts), "failed", len(errs),
		"forgotten", forgotten, "complete_listing", complete)
	return errors.Join(errs...)
}

type installationListing struct {
	installations map[int64]bool
	listed        map[int64]bool
	repos         map[int64]bool
}

func (s *Service) syncInstallation(ctx context.Context, inst gh.Installation) ([]gh.Repository, bool, error) {
	if _, err := s.st.UpsertInstallation(ctx, store.Installation{ID: inst.ID, Account: inst.Account.Login, AccountType: inst.Account.Type}); err != nil {
		return nil, false, storeErr(err, "record installation %d", inst.ID)
	}
	suspended := inst.SuspendedAt != nil
	if err := s.st.SuspendInstallation(ctx, inst.ID, suspended); err != nil {
		return nil, false, storeErr(err, "record suspension of installation %d", inst.ID)
	}
	if suspended {
		return nil, false, nil
	}
	repos, err := s.gh.InstallationRepos(ctx, inst.ID)
	if err != nil {
		return nil, false, fmt.Errorf("runs: repositories of installation %d: %w", inst.ID, err)
	}
	return repos, true, s.recordRepos(ctx, inst.ID, repos, func(r store.Repo) bool { return r.Config == nil && r.ConfigSHA == "" })
}

func (s *Service) forgetUnlisted(ctx context.Context, l installationListing) (int, error) {
	stored, err := s.st.ListInstallations(ctx)
	if err != nil {
		return 0, storeErr(err, "installations")
	}
	forgotten := 0
	for _, inst := range stored {
		if l.installations[inst.ID] {
			continue
		}
		if err := s.st.DeleteInstallation(ctx, inst.ID); err != nil {
			return forgotten, storeErr(err, "forget installation %d", inst.ID)
		}
		forgotten++
		s.log.InfoContext(ctx, "forgot an installation GitHub no longer lists", "installation", inst.ID, "account", inst.Account)
	}
	repos, err := s.st.ListRepos(ctx)
	if err != nil {
		return forgotten, storeErr(err, "repositories")
	}
	for _, r := range repos {
		if !l.listed[r.InstallationID] || l.repos[r.ID] {
			continue
		}
		if err := s.st.DeleteRepo(ctx, r.ID); err != nil {
			return forgotten, storeErr(err, "forget repository %s", r.FullName)
		}
		forgotten++
		s.log.InfoContext(ctx, "forgot a repository its installation no longer lists", "repo", r.FullName, "installation", r.InstallationID)
	}
	return forgotten, nil
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
	s.audit(ctx, actor, "unlock", v1.QualifiedStackKey(repo.FullName, stack.Key), map[string]any{
		"repo": repo.FullName, "stack": stack.Key, "stack_id": stack.ID.String(), "pr": lock.PRNumber,
		"run_id": lock.RunID.String(), "reason": req.Reason, "force_state": req.ForceState,
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
	s.audit(ctx, actor, "rerun", repo.FullName, map[string]any{
		"repo": repo.FullName, "pr": run.PRNumber, "run_id": run.ID.String(), "new_run_id": next.ID.String(),
	})
	detail, err := s.st.RunDetail(ctx, next.ID)
	if err != nil {
		return nil, storeErr(err, "run %s", next.ID)
	}
	return &detail, nil
}
