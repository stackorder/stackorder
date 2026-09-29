package runs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/command"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

func (s *Service) eventRepo(ctx context.Context, ev *gh.EventCommon) (store.Repo, bool, error) {
	if ev.Repository == nil {
		return store.Repo{}, false, nil
	}
	repo, err := s.st.GetRepo(ctx, ev.Repository.ID)
	if errors.Is(err, store.ErrNotFound) {
		repo, err = s.st.GetRepoByName(ctx, ev.Repository.FullName)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.Repo{}, false, nil
	case err != nil:
		return store.Repo{}, false, storeErr(err, "repository %s", ev.Repository.FullName)
	}
	return repo, !repo.Suspended, nil
}

// HandlePullRequest reacts to pull request activity: a fork gets a neutral
// plan check and nothing runs, a new head commit supersedes older plans, a
// merge into the default branch releases locks (before_merge) or starts the
// apply (on_merge) and makes the pull request's graph the default-branch
// graph, and a close without such a merge warns about the locks it keeps.
func (s *Service) HandlePullRequest(ctx context.Context, ev *gh.PullRequestEvent) error {
	repo, ok, err := s.eventRepo(ctx, &ev.EventCommon)
	if err != nil || !ok {
		return err
	}
	pr := &ev.PullRequest
	switch ev.Action {
	case "opened", "synchronize", "reopened":
		if pr.IsFork() {
			return s.forkNotice(ctx, repo, pr.HeadSHA)
		}
		if ev.Action != "synchronize" {
			return nil
		}
		c, err := s.client(ctx, repo)
		if err != nil {
			return err
		}
		head, err := s.pullHead(ctx, c, repo, pr.Number)
		if err != nil {
			return err
		}
		s.supersede(ctx, repo, pr.Number, head)
		return nil
	case "closed":
		if pr.Merged && pr.BaseRef == repo.DefaultBranch {
			return s.onMerged(ctx, repo, ev)
		}
		return s.onClosedUnmerged(ctx, repo, pr.Number)
	}
	return nil
}

func (s *Service) forkNotice(ctx context.Context, repo store.Repo, sha string) error {
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	existing, err := c.ListCheckRunsForRef(ctx, repo.FullName, sha, report.CheckPlan)
	if err != nil {
		return fmt.Errorf("runs: check runs of %s: %w", shortSHA(sha), err)
	}
	if len(existing) > 0 {
		return nil
	}
	title, summary := report.ForkNotice()
	_, err = c.CreateCheckRun(ctx, repo.FullName, gh.CheckRunParams{
		Name: report.CheckPlan, HeadSHA: sha, Status: gh.CheckRunCompleted, Conclusion: gh.ConclusionNeutral,
		CompletedAt: s.now(), Output: gh.CheckRunOutput{Title: title, Summary: summary},
	})
	if err != nil {
		return fmt.Errorf("runs: fork notice on %s: %w", shortSHA(sha), err)
	}
	return nil
}

func (s *Service) onMerged(ctx context.Context, repo store.Repo, ev *gh.PullRequestEvent) error {
	pr := &ev.PullRequest
	if err := s.recordDefaultGraph(ctx, repo, pr); err != nil {
		return err
	}
	if repoConfig(repo).Apply.Mode == v1.ApplyOnMerge {
		return s.applyOnMerge(ctx, repo, ev)
	}
	released, err := s.st.ReleaseLocksForPR(ctx, repo.ID, pr.Number)
	if err != nil {
		return storeErr(err, "release locks of #%d", pr.Number)
	}
	if len(released) == 0 {
		return nil
	}
	for _, l := range released {
		s.audit(ctx, ev.Sender.Login, "unlock", v1.QualifiedStackKey(repo.FullName, l.StackKey), map[string]any{
			"repo": repo.FullName, "stack": l.StackKey, "stack_id": l.StackID.String(), "pr": pr.Number,
			"run_id": l.RunID.String(), "via": "merge",
		})
	}
	s.refreshLocksGauge(ctx)
	s.comment(ctx, repo, pr.Number, report.UnlockedComment(lockInfos(released), ev.Sender.Login))
	return nil
}

func (s *Service) recordDefaultGraph(ctx context.Context, repo store.Repo, pr *gh.PullRequest) error {
	runs, _, err := s.st.ListRuns(ctx, store.RunFilter{RepoID: repo.ID, PRNumber: pr.Number, Mode: v1.ModePlan, Limit: 50})
	if err != nil {
		return storeErr(err, "plan runs of #%d", pr.Number)
	}
	var graphID *uuid.UUID
	for _, r := range runs {
		if r.GraphID == nil {
			continue
		}
		if r.SHA == pr.HeadSHA {
			graphID = r.GraphID
			break
		}
		if graphID == nil {
			graphID = r.GraphID
		}
	}
	if graphID == nil {
		return nil
	}
	if err := s.st.SetDefaultGraph(ctx, repo.ID, *graphID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return storeErr(err, "record default graph of %s", repo.FullName)
	}
	return nil
}

func (s *Service) applyOnMerge(ctx context.Context, repo store.Repo, ev *gh.PullRequestEvent) error {
	pr := &ev.PullRequest
	sha := firstNonEmpty(pr.MergeCommitSHA, pr.HeadSHA)
	merger := ev.Sender.Login
	var existing bool
	err := s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, fmt.Sprintf("merge:%d:%d", repo.ID, pr.Number)); err != nil {
			return err
		}
		runs, _, err := tx.ListRuns(ctx, store.RunFilter{RepoID: repo.ID, PRNumber: pr.Number, SHA: sha, Mode: v1.ModeApply, Limit: 1})
		existing = len(runs) > 0
		return err
	})
	if err != nil {
		return storeErr(err, "apply runs of #%d", pr.Number)
	}
	if existing {
		return nil
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	res, err := s.gate(ctx, gateInput{repo: repo, client: c, pr: pr.Number, requester: merger, pull: pr,
		layers: layerAuthorization | layerPlans | layerChecks | layerLocks})
	if err != nil {
		return err
	}
	opts, err := s.reportOptions(ctx, s.st, repo, pr.Number)
	if err != nil {
		return err
	}
	if nothingToApply(res) {
		return nil
	}
	cmd := "stackorder apply (on merge)"
	if len(res.failures) > 0 {
		s.comment(ctx, repo, pr.Number, report.RefusalComment(cmd, res.failures, opts))
		return nil
	}
	if len(res.keys) == 0 {
		return nil
	}
	_, failures, err := s.startApply(ctx, applyRequest{
		repo: repo, pr: pr.Number, sha: sha, baseSHA: pr.BaseSHA, view: res.view,
		keys: res.keys, trigger: v1.TriggerPullRequest, requester: merger,
	})
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		s.comment(ctx, repo, pr.Number, report.RefusalComment(cmd, failures, opts))
	}
	return nil
}

func (s *Service) onClosedUnmerged(ctx context.Context, repo store.Repo, pr int) error {
	locks, err := s.st.ListLocks(ctx, repo.ID)
	if err != nil {
		return storeErr(err, "locks of %s", repo.FullName)
	}
	var held []v1.LockInfo
	for _, l := range locks {
		if l.PRNumber == pr {
			held = append(held, l.ToV1())
		}
	}
	if len(held) == 0 {
		return nil
	}
	target := fmt.Sprintf("pr:%s#%d", repo.FullName, pr)
	n, err := s.st.CountAudit(ctx, "lock_warning", target, s.now().Add(-staleLockAge))
	if err != nil || n > 0 {
		return err
	}
	opts, err := s.reportOptions(ctx, s.st, repo, pr)
	if err != nil {
		return err
	}
	s.audit(ctx, "", "lock_warning", target, map[string]any{"locks": len(held)})
	s.comment(ctx, repo, pr, report.LockWarningComment(held, opts))
	return nil
}

// HandlePullRequestReview is subscribed so that approvals reach the server
// promptly, but it changes no state: the apply gate reads reviews from
// GitHub when a command asks for an apply, so there is nothing to refresh
// and it returns nil.
func (s *Service) HandlePullRequestReview(context.Context, *gh.PullRequestReviewEvent) error {
	return nil
}

// HandleCheckSuite is subscribed for completeness; check suites carry
// nothing the server acts on, so it returns nil.
func (s *Service) HandleCheckSuite(context.Context, *gh.CheckSuiteEvent) error {
	return nil
}

// HandleCheckRun re-plans on a "Re-run" click: one stack for a
// "stackorder/plan: <key>" check, every affected stack for the
// "stackorder/plan" roll-up.
func (s *Service) HandleCheckRun(ctx context.Context, ev *gh.CheckRunEvent) error {
	if ev.Action != "rerequested" || isBot(ev.Sender) {
		return nil
	}
	name := ev.CheckRun.Name
	var keys []string
	switch {
	case name == report.CheckPlan:
	case strings.HasPrefix(name, report.CheckPlan+": "):
		keys = []string{strings.TrimPrefix(name, report.CheckPlan+": ")}
	default:
		return nil
	}
	repo, ok, err := s.eventRepo(ctx, &ev.EventCommon)
	if err != nil || !ok {
		return err
	}
	pr, err := s.checkRunPR(ctx, repo, ev.CheckRun)
	if err != nil || pr == 0 {
		return err
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	perm, err := s.permission(ctx, c, repo, ev.Sender.Login)
	if err != nil || !gh.HasPushPermission(perm) {
		return err
	}
	cmd := &command.Command{Verb: command.Plan, Stacks: keys}
	out, err := s.commandPlan(ctx, repo, c, pr, cmd, ev.Sender.Login, v1.TriggerRerequest)
	if err != nil {
		return err
	}
	s.audit(ctx, ev.Sender.Login, "rerun", repo.FullName, map[string]any{
		"repo": repo.FullName, "pr": pr, "check": name, "accepted": out.accepted, "reason": out.reason,
	})
	return nil
}

func (s *Service) checkRunPR(ctx context.Context, repo store.Repo, cr gh.CheckRun) (int, error) {
	if len(cr.PullRequests) > 0 {
		return cr.PullRequests[0].Number, nil
	}
	runs, _, err := s.st.ListRuns(ctx, store.RunFilter{RepoID: repo.ID, SHA: cr.HeadSHA, Mode: v1.ModePlan, Limit: 20})
	if err != nil {
		return 0, storeErr(err, "plan runs of %s", shortSHA(cr.HeadSHA))
	}
	for _, r := range runs {
		if r.PRNumber > 0 {
			return r.PRNumber, nil
		}
	}
	return 0, nil
}

func (s *Service) workflowPath() string { return ".github/workflows/" + s.cfg.WorkflowFile }

// HandleWorkflowRun binds a workflow run of stackorder-run.yml to its
// dispatch, by an earlier binding or by its display title; when several
// dispatches of a wave share the title, the stacks named by the run's jobs
// decide, and a run whose dispatch is already bound elsewhere binds
// nothing. On completion it closes the dispatch: stacks it carried that
// never reported are marked unknown, which fails the run.
func (s *Service) HandleWorkflowRun(ctx context.Context, ev *gh.WorkflowRunEvent) error {
	wr := ev.WorkflowRun
	path := wr.Path
	if path == "" && ev.Workflow != nil {
		path = ev.Workflow.Path
	}
	if path != s.workflowPath() {
		return nil
	}
	repo, ok, err := s.eventRepo(ctx, &ev.EventCommon)
	if err != nil || !ok {
		return err
	}
	d, found, err := s.dispatchForWorkflowRun(ctx, repo, wr, "")
	if err != nil || !found {
		return err
	}
	if wr.Status != gh.RunStatusCompleted && ev.Action != "completed" {
		return nil
	}
	return s.completeDispatch(ctx, d, firstNonEmpty(wr.Conclusion, "unknown"))
}

func (s *Service) dispatchForWorkflowRun(ctx context.Context, repo store.Repo, wr gh.WorkflowRun, environment string) (store.Dispatch, bool, error) {
	d, err := s.st.FindDispatchByWorkflowRun(ctx, wr.ID)
	if err == nil {
		return d, d.RepoID == repo.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Dispatch{}, false, storeErr(err, "dispatch of workflow run %d", wr.ID)
	}
	t, ok := parseDisplayTitle(wr.DisplayTitle)
	if !ok {
		return store.Dispatch{}, false, nil
	}
	run, err := s.st.GetRun(ctx, t.RunID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Dispatch{}, false, nil
	}
	if err != nil {
		return store.Dispatch{}, false, storeErr(err, "run %s", t.RunID)
	}
	if run.RepoID != repo.ID || run.Mode != t.Mode {
		return store.Dispatch{}, false, nil
	}
	dispatches, err := s.st.ListDispatches(ctx, run.ID)
	if err != nil {
		return store.Dispatch{}, false, storeErr(err, "dispatches of run %s", run.ID)
	}
	var titled []store.Dispatch
	for _, cand := range dispatches {
		if cand.Wave == t.Wave && cand.Mode == t.Mode && (environment == "" || strings.EqualFold(cand.Environment, environment)) {
			titled = append(titled, cand)
		}
	}
	switch len(titled) {
	case 0:
		return store.Dispatch{}, false, nil
	case 1:
		d = titled[0]
	default:
		picked, ok, err := s.dispatchByJobs(ctx, repo, run.ID, wr.ID, titled)
		if err != nil || !ok {
			return store.Dispatch{}, false, err
		}
		d = picked
	}
	if d.WorkflowRunID != nil || d.CompletedAt != nil {
		return store.Dispatch{}, false, nil
	}
	if err := s.st.SetDispatchWorkflowRun(ctx, d.ID, wr.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.Dispatch{}, false, nil
		}
		return store.Dispatch{}, false, storeErr(err, "bind dispatch %s", d.ID)
	}
	d.WorkflowRunID = &wr.ID
	return d, true, nil
}

func (s *Service) dispatchByJobs(ctx context.Context, repo store.Repo, runID uuid.UUID, workflowRunID int64, candidates []store.Dispatch) (store.Dispatch, bool, error) {
	c, err := s.client(ctx, repo)
	if err != nil {
		return store.Dispatch{}, false, err
	}
	jobs, err := c.ListWorkflowJobs(ctx, repo.FullName, workflowRunID)
	if err != nil {
		return store.Dispatch{}, false, fmt.Errorf("runs: jobs of workflow run %d: %w", workflowRunID, err)
	}
	rows, err := s.st.GetRunStacks(ctx, runID)
	if err != nil {
		return store.Dispatch{}, false, storeErr(err, "stacks of run %s", runID)
	}
	d, ok := pickDispatch(jobs, rows, candidates)
	return d, ok, nil
}

func pickDispatch(jobs []gh.WorkflowJob, rows []store.RunStack, candidates []store.Dispatch) (store.Dispatch, bool) {
	carried := make([][]store.RunStack, len(candidates))
	for i, d := range candidates {
		for _, rs := range rows {
			if carriedBy(rs, d) {
				carried[i] = append(carried[i], rs)
			}
		}
	}
	picked := -1
	for _, j := range jobs {
		for i := range candidates {
			if _, ok := matchJobStack(j.Name, carried[i]); !ok {
				continue
			}
			if picked >= 0 && picked != i {
				return store.Dispatch{}, false
			}
			picked = i
		}
	}
	if picked < 0 {
		return store.Dispatch{}, false
	}
	return candidates[picked], true
}

func (s *Service) completeDispatch(ctx context.Context, d store.Dispatch, conclusion string) error {
	if _, err := s.st.CompleteDispatch(ctx, d.ID, conclusion); err != nil {
		return storeErr(err, "complete dispatch %s", d.ID)
	}
	return s.markVanished(ctx, d, "")
}

func (s *Service) markVanished(ctx context.Context, d store.Dispatch, warning string) error {
	run, err := s.st.GetRun(ctx, d.RunID)
	if err != nil {
		return storeErr(err, "run %s", d.RunID)
	}
	rows, err := s.st.GetRunStacks(ctx, run.ID)
	if err != nil {
		return storeErr(err, "stacks of run %s", run.ID)
	}
	var changed []uuid.UUID
	unknown := v1.StackUnknown
	for _, rs := range rows {
		if !carriedBy(rs, d) || finishedFor(run.Mode, rs.Status) {
			continue
		}
		_, err := s.st.UpdateRunStack(ctx, run.ID, rs.StackID, store.RunStackPatch{
			Status: &unknown, IfStatus: []v1.StackStatus{rs.Status},
		})
		switch {
		case errors.Is(err, store.ErrConflict):
			continue
		case err != nil:
			return storeErr(err, "mark %s unknown", rs.Key)
		}
		s.m.StackFinished(run.Mode, v1.StackUnknown, 0)
		changed = append(changed, rs.StackID)
	}
	if len(changed) == 0 && warning == "" {
		return s.advance(ctx, run.ID)
	}
	if warning != "" {
		if err := s.st.AddRunWarning(ctx, run.ID, warning); err != nil {
			return storeErr(err, "record warning on run %s", run.ID)
		}
	}
	if run.Mode == v1.ModeApply {
		for _, id := range changed {
			row, err := s.st.GetRunStack(ctx, run.ID, id)
			if err != nil {
				return storeErr(err, "stack row %s", id)
			}
			blocked, err := s.blockDependents(ctx, run, row)
			if err != nil {
				return err
			}
			changed = append(changed, blocked...)
		}
	}
	if err := s.advance(ctx, run.ID); err != nil {
		return err
	}
	return s.render(ctx, run.ID, renderOpts{stacks: changed})
}

func carriedBy(rs store.RunStack, d store.Dispatch) bool {
	if rs.DispatchID != nil {
		return *rs.DispatchID == d.ID
	}
	if d.Mode != v1.ModeApply {
		return true
	}
	return rs.Status != v1.StackSkipped && rs.Wave == d.Wave && strings.EqualFold(rs.Environment, d.Environment)
}

func finishedFor(mode v1.RunMode, st v1.StackStatus) bool {
	if mode == v1.ModeApply {
		return st.Terminal()
	}
	return planDone(st)
}

// HandleWorkflowJob records the job URL of a stack and moves it to
// planning or applying when its matrix job starts, for jobs of a bound
// dispatch of stackorder-run.yml and for the plan jobs of the pull request
// workflow run that registered a plan run. Jobs it cannot match to a stack
// are ignored.
func (s *Service) HandleWorkflowJob(ctx context.Context, ev *gh.WorkflowJobEvent) error {
	job := ev.WorkflowJob
	repo, ok, err := s.eventRepo(ctx, &ev.EventCommon)
	if err != nil || !ok {
		return err
	}
	run, carried, found, err := s.jobStacks(ctx, repo, job.RunID)
	if err != nil || !found {
		return err
	}
	rs, ok := matchJobStack(job.Name, carried)
	if !ok {
		return nil
	}
	patch := store.RunStackPatch{}
	if job.HTMLURL != "" && job.HTMLURL != rs.JobURL {
		patch.JobURL = &job.HTMLURL
	}
	if job.Status == gh.RunStatusInProgress || job.Status == gh.RunStatusWaiting {
		started := v1.StackApplying
		from := []v1.StackStatus{v1.StackPlanned, v1.StackPending}
		if run.Mode != v1.ModeApply {
			started, from = v1.StackPlanning, []v1.StackStatus{v1.StackPending}
		}
		if slices.Contains(from, rs.Status) {
			patch.Status, patch.IfStatus = &started, from
		}
	}
	if patch.Status == nil && patch.JobURL == nil {
		return nil
	}
	if _, err := s.st.UpdateRunStack(ctx, run.ID, rs.StackID, patch); err != nil && !errors.Is(err, store.ErrConflict) {
		return storeErr(err, "record job of %s", rs.Key)
	}
	return s.render(ctx, run.ID, renderOpts{stacks: []uuid.UUID{rs.StackID}})
}

func (s *Service) jobStacks(ctx context.Context, repo store.Repo, workflowRunID int64) (store.Run, []store.RunStack, bool, error) {
	var (
		run      store.Run
		dispatch *store.Dispatch
	)
	d, err := s.st.FindDispatchByWorkflowRun(ctx, workflowRunID)
	switch {
	case err == nil:
		if d.RepoID != repo.ID {
			return store.Run{}, nil, false, nil
		}
		dispatch = &d
		if run, err = s.st.GetRun(ctx, d.RunID); err != nil {
			return store.Run{}, nil, false, storeErr(err, "run %s", d.RunID)
		}
	case errors.Is(err, store.ErrNotFound):
		run, err = s.st.FindRunByWorkflowRun(ctx, repo.ID, workflowRunID)
		if errors.Is(err, store.ErrNotFound) {
			return store.Run{}, nil, false, nil
		}
		if err != nil {
			return store.Run{}, nil, false, storeErr(err, "run of workflow run %d", workflowRunID)
		}
		if run.Trigger != v1.TriggerPullRequest || run.Mode != v1.ModePlan {
			return store.Run{}, nil, false, nil
		}
	default:
		return store.Run{}, nil, false, storeErr(err, "dispatch of workflow run %d", workflowRunID)
	}
	if run.Status.Terminal() {
		return store.Run{}, nil, false, nil
	}
	rows, err := s.st.GetRunStacks(ctx, run.ID)
	if err != nil {
		return store.Run{}, nil, false, storeErr(err, "stacks of run %s", run.ID)
	}
	if dispatch == nil {
		return run, rows, true, nil
	}
	var carried []store.RunStack
	for _, rs := range rows {
		if carriedBy(rs, *dispatch) {
			carried = append(carried, rs)
		}
	}
	return run, carried, true, nil
}

// HandlePush keeps the stored default-branch configuration and default
// branch name current, and records module versions when a semver tag is
// pushed to a repository whose git modules stacks consume.
func (s *Service) HandlePush(ctx context.Context, ev *gh.PushEvent) error {
	repo, ok, err := s.eventRepo(ctx, &ev.EventCommon)
	if err != nil || !ok {
		return err
	}
	if b := ev.Repository.DefaultBranch; b != "" && b != repo.DefaultBranch {
		if repo, err = s.st.UpsertRepo(ctx, store.RepoParams{
			ID: repo.ID, InstallationID: repo.InstallationID, FullName: repo.FullName, DefaultBranch: b, Private: repo.Private,
		}); err != nil {
			return storeErr(err, "update default branch of %s", repo.FullName)
		}
	}
	if ev.Deleted {
		return nil
	}
	switch {
	case ev.Branch() != "" && ev.Branch() == repo.DefaultBranch:
		c, err := s.client(ctx, repo)
		if err != nil {
			return err
		}
		head, err := c.GetRef(ctx, repo.FullName, "heads/"+repo.DefaultBranch)
		switch {
		case errors.Is(err, gh.ErrNotFound):
			head = ev.After
		case err != nil:
			return fmt.Errorf("runs: head of %s: %w", repo.FullName, err)
		}
		return s.loadConfig(ctx, c, repo, head, ev.Sender.Login)
	case ev.IsTag():
		return s.recordTag(ctx, repo, ev)
	}
	return nil
}

func (s *Service) loadConfig(ctx context.Context, c *gh.Client, repo store.Repo, sha, actor string) error {
	ref := firstNonEmpty(sha, repo.DefaultBranch)
	data, err := c.GetContents(ctx, repo.FullName, config.RootFile, ref)
	if errors.Is(err, gh.ErrNotFound) {
		if err := s.st.UpdateRepoConfig(ctx, repo.ID, nil, sha); err != nil {
			return storeErr(err, "clear configuration of %s", repo.FullName)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("runs: %s of %s at %s: %w", config.RootFile, repo.FullName, shortSHA(ref), err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		s.audit(ctx, actor, "config_invalid", "repo:"+repo.FullName, map[string]any{"sha": sha, "error": err.Error()})
		s.log.WarnContext(ctx, "invalid stackorder.yaml on the default branch; keeping the previous configuration", "repo", repo.FullName, "sha", sha, "error", err)
		return nil
	}
	if err := s.st.UpdateRepoConfig(ctx, repo.ID, cfg, sha); err != nil {
		return storeErr(err, "store configuration of %s", repo.FullName)
	}
	return nil
}

func (s *Service) recordTag(ctx context.Context, repo store.Repo, ev *gh.PushEvent) error {
	tag := ev.TagName()
	if _, ok := parseSemver(tag); !ok {
		return nil
	}
	modules, err := s.st.ListModules(ctx, store.ModuleFilter{Kind: v1.ModuleGit})
	if err != nil {
		return storeErr(err, "git modules")
	}
	name := strings.ToLower(repo.FullName)
	families := map[string]store.Module{}
	for _, m := range modules {
		k := strings.ToLower(m.Key)
		if k == name || strings.HasPrefix(k, name+"//") || strings.HasPrefix(k, name+"@") {
			if _, ok := families[m.BaseKey]; !ok {
				families[m.BaseKey] = m
			}
		}
	}
	var at = s.now()
	if ev.HeadCommit != nil && !ev.HeadCommit.Timestamp.IsZero() {
		at = ev.HeadCommit.Timestamp.UTC()
	}
	bases := make([]string, 0, len(families))
	for b := range families {
		bases = append(bases, b)
	}
	slices.Sort(bases)
	for _, b := range bases {
		m := families[b]
		if err := s.st.RecordModuleVersion(ctx, m.ID, tag, ev.After, at); err != nil {
			return storeErr(err, "record %s of %s", tag, b)
		}
		s.logBehind(ctx, m, b)
	}
	return nil
}

func (s *Service) logBehind(ctx context.Context, m store.Module, family string) {
	versions, err := s.st.ListModuleVersions(ctx, m.ID)
	if err != nil {
		return
	}
	released := make([]string, len(versions))
	for i, v := range versions {
		released[i] = v.Version
	}
	consumers, err := s.st.ModuleConsumers(ctx, m.ID)
	if err != nil {
		return
	}
	for _, c := range consumers {
		if n, ok := versionsBehind(released, c.Ref); ok && n > 0 {
			s.log.InfoContext(ctx, "module consumer behind", "module", family, "repo", c.Repo, "stack", c.StackKey, "ref", c.Ref, "behind", n)
		}
	}
}

// HandleInstallation keeps installations and their repositories in step
// with GitHub and loads stackorder.yaml for repositories it has not seen.
func (s *Service) HandleInstallation(ctx context.Context, ev *gh.InstallationEvent) error {
	inst := ev.Installation
	if inst == nil || inst.ID == 0 {
		return nil
	}
	switch ev.Action {
	case "deleted":
		if err := s.st.DeleteInstallation(ctx, inst.ID); err != nil {
			return storeErr(err, "delete installation %d", inst.ID)
		}
		return nil
	case "suspend":
		if err := s.st.SuspendInstallation(ctx, inst.ID, true); err != nil && !errors.Is(err, store.ErrNotFound) {
			return storeErr(err, "suspend installation %d", inst.ID)
		}
		return nil
	case "created", "unsuspend", "new_permissions_accepted":
	default:
		return nil
	}
	if _, err := s.st.UpsertInstallation(ctx, store.Installation{ID: inst.ID, Account: inst.Account.Login, AccountType: inst.Account.Type}); err != nil {
		return storeErr(err, "record installation %d", inst.ID)
	}
	if err := s.st.SuspendInstallation(ctx, inst.ID, false); err != nil {
		return storeErr(err, "unsuspend installation %d", inst.ID)
	}
	return s.addRepos(ctx, inst.ID, ev.Repositories)
}

// HandleInstallationRepositories records repositories added to an
// installation, loading their configuration, and forgets removed ones.
func (s *Service) HandleInstallationRepositories(ctx context.Context, ev *gh.InstallationRepositoriesEvent) error {
	inst := ev.Installation
	if inst == nil || inst.ID == 0 {
		return nil
	}
	for _, r := range ev.RepositoriesRemoved {
		if err := s.st.DeleteRepo(ctx, r.ID); err != nil {
			return storeErr(err, "delete repository %s", r.FullName)
		}
	}
	if len(ev.RepositoriesAdded) == 0 {
		return nil
	}
	if _, err := s.st.GetInstallation(ctx, inst.ID); errors.Is(err, store.ErrNotFound) {
		if _, err := s.st.UpsertInstallation(ctx, store.Installation{ID: inst.ID, Account: inst.Account.Login, AccountType: inst.Account.Type}); err != nil {
			return storeErr(err, "record installation %d", inst.ID)
		}
	} else if err != nil {
		return storeErr(err, "installation %d", inst.ID)
	}
	return s.addRepos(ctx, inst.ID, ev.RepositoriesAdded)
}

func (s *Service) addRepos(ctx context.Context, installationID int64, repos []gh.Repository) error {
	return s.recordRepos(ctx, installationID, repos, func(r store.Repo) bool { return r.Config == nil })
}

func (s *Service) recordRepos(ctx context.Context, installationID int64, repos []gh.Repository, reloadKnown func(store.Repo) bool) error {
	if len(repos) == 0 {
		return nil
	}
	c, err := s.gh.Client(ctx, installationID)
	if err != nil {
		return fmt.Errorf("runs: github client for installation %d: %w", installationID, err)
	}
	for _, r := range repos {
		_, getErr := s.st.GetRepo(ctx, r.ID)
		known := getErr == nil
		if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
			return storeErr(getErr, "repository %s", r.FullName)
		}
		meta := r
		if meta.DefaultBranch == "" {
			full, err := c.GetRepository(ctx, r.FullName)
			if err != nil {
				return fmt.Errorf("runs: repository %s: %w", r.FullName, err)
			}
			meta = *full
		}
		repo, err := s.st.UpsertRepo(ctx, store.RepoParams{
			ID: meta.ID, InstallationID: installationID, FullName: meta.FullName, DefaultBranch: meta.DefaultBranch, Private: meta.Private,
		})
		if err != nil {
			return storeErr(err, "record repository %s", meta.FullName)
		}
		if known && !reloadKnown(repo) {
			continue
		}
		sha, err := c.GetRef(ctx, repo.FullName, "heads/"+repo.DefaultBranch)
		if err != nil && !errors.Is(err, gh.ErrNotFound) {
			return fmt.Errorf("runs: head of %s: %w", repo.FullName, err)
		}
		if err := s.loadConfig(ctx, c, repo, sha, schedulerActor); err != nil {
			return err
		}
	}
	return nil
}

// HandleDeploymentProtectionRule answers GitHub for the App's custom
// deployment protection rule: a job of an apply the server dispatched may
// start in its environment when its requester still passes apply gate
// layers 1 and 2 for the stacks it deploys there.
func (s *Service) HandleDeploymentProtectionRule(ctx context.Context, ev *gh.DeploymentProtectionRuleEvent) error {
	repo, ok, err := s.eventRepo(ctx, &ev.EventCommon)
	if err != nil || !ok {
		return err
	}
	runID, err := ev.RunID()
	if err != nil {
		s.log.WarnContext(ctx, "deployment protection rule without a run id", "error", err)
		return nil
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	decide := func(state, comment string) error {
		err := c.ReviewDeploymentProtectionRule(ctx, repo.FullName, runID, ev.Environment, state, comment)
		var apiErr *gh.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
			s.log.InfoContext(ctx, "deployment protection rule already answered", "run", runID, "error", err)
			return nil
		}
		if err != nil {
			return fmt.Errorf("runs: answer deployment protection rule of run %d: %w", runID, err)
		}
		s.audit(ctx, "", "deployment_"+state, fmt.Sprintf("workflow_run:%s:%d", repo.FullName, runID), map[string]any{
			"environment": ev.Environment, "comment": comment,
		})
		return nil
	}
	d, found, err := s.dispatchForProtection(ctx, c, repo, runID, ev.Environment)
	if err != nil {
		return err
	}
	if !found {
		return decide(gh.DeploymentRejected, "Stackorder did not dispatch workflow run "+fmt.Sprint(runID)+", so it cannot vouch for this deployment.")
	}
	run, err := s.st.GetRun(ctx, d.RunID)
	if err != nil {
		return storeErr(err, "run %s", d.RunID)
	}
	if !strings.EqualFold(d.Environment, ev.Environment) {
		return decide(gh.DeploymentRejected, fmt.Sprintf("Stackorder dispatched this workflow run for environment %s, not %s.", d.Environment, ev.Environment))
	}
	switch {
	case d.CompletedAt != nil:
		return decide(gh.DeploymentRejected, fmt.Sprintf("Workflow run %d already completed its Stackorder dispatch, so a re-run of it is not vouched for; start a new apply instead.", runID))
	case run.Status.Terminal():
		return decide(gh.DeploymentRejected, fmt.Sprintf("Stackorder run %s is already %s, so it deploys nothing more; start a new apply instead.", run.ID, run.Status))
	}
	if run.Mode != v1.ModeApply {
		return decide(gh.DeploymentApproved, "Stackorder "+string(run.Mode)+" dispatch of run "+run.ID.String()+": read-only, nothing is applied.")
	}
	rows, err := s.st.GetRunStacks(ctx, run.ID)
	if err != nil {
		return storeErr(err, "stacks of run %s", run.ID)
	}
	var stacks []store.RunStack
	for _, rs := range rows {
		if carriedBy(rs, d) {
			stacks = append(stacks, rs)
		}
	}
	layers := layerAuthorization
	if run.Trigger == v1.TriggerComment {
		layers |= layerApprovals
	}
	res, err := s.gate(ctx, gateInput{repo: repo, client: c, pr: run.PRNumber, requester: run.RequestedBy,
		layers: layers, reviewSHA: run.SHA, stacks: stacks})
	if err != nil {
		return err
	}
	if len(res.failures) > 0 {
		opts, err := s.reportOptions(ctx, s.st, repo, run.PRNumber)
		if err != nil {
			return err
		}
		return decide(gh.DeploymentRejected, report.RefusalComment("stackorder apply", res.failures, opts))
	}
	return decide(gh.DeploymentApproved, protectionApproval(run, ev.Environment, stacks, layers))
}

func (s *Service) dispatchForProtection(ctx context.Context, c *gh.Client, repo store.Repo, runID int64, environment string) (store.Dispatch, bool, error) {
	d, err := s.st.FindDispatchByWorkflowRun(ctx, runID)
	if err == nil {
		return d, d.RepoID == repo.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Dispatch{}, false, storeErr(err, "dispatch of workflow run %d", runID)
	}
	wr, err := c.GetWorkflowRun(ctx, repo.FullName, runID)
	if errors.Is(err, gh.ErrNotFound) {
		return store.Dispatch{}, false, nil
	}
	if err != nil {
		return store.Dispatch{}, false, fmt.Errorf("runs: workflow run %d: %w", runID, err)
	}
	if wr.Path != s.workflowPath() {
		return store.Dispatch{}, false, nil
	}
	return s.dispatchForWorkflowRun(ctx, repo, *wr, environment)
}

func protectionApproval(run store.Run, env string, stacks []store.RunStack, layers gateLayers) string {
	keys := make([]string, len(stacks))
	for i, rs := range stacks {
		keys[i] = rs.Key
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Stackorder approved the %s deployment of %s for run %s, requested by %s. Checked:", env, strings.Join(keys, ", "), run.ID, run.RequestedBy)
	b.WriteString(" layer 1, the requester may apply every stack (apply.allowed_teams or write access)")
	if layers&layerApprovals != 0 {
		fmt.Fprintf(&b, "; layer 2, approvals, mergeability, four eyes and code owner review on %s", shortSHA(run.SHA))
	}
	b.WriteString(".")
	return b.String()
}
