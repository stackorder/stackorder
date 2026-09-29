package runs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/command"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

// HandleIssueComment executes a "stackorder …" command posted on a pull
// request. Commands from users without write access are ignored, each pull
// request may issue CommentRateLimit commands per minute, every command is
// audited, and every refusal is answered with a comment naming the reason.
// A comment is executed at most once, however often it is delivered, so a
// command that fails part way is answered with a comment asking for it to
// be posted again instead of being retried.
func (s *Service) HandleIssueComment(ctx context.Context, ev *gh.IssueCommentEvent) error {
	if ev.Action != "created" || !ev.IsPullRequest() || isBot(ev.Comment.User) || isBot(ev.Sender) {
		return nil
	}
	cmd, ok := command.Parse(ev.Comment.Body)
	if !ok {
		if !unknownCommand(ev.Comment.Body) {
			return nil
		}
		cmd = &command.Command{Verb: command.Help, Raw: "stackorder help"}
	}
	repo, err := s.st.GetRepoByName(ctx, ev.RepoFullName())
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return storeErr(err, "repository %s", ev.RepoFullName())
	}
	if repo.Suspended {
		return nil
	}
	c, err := s.client(ctx, repo)
	if err != nil {
		return err
	}
	login := firstNonEmpty(ev.Comment.User.Login, ev.Sender.Login)
	pr := ev.Issue.Number
	target := repo.FullName
	prDetail := strconv.Itoa(pr)
	details := map[string]any{"repo": repo.FullName, "pr": pr, "verb": string(cmd.Verb), "stacks": cmd.Stacks, "comment_id": ev.Comment.ID}
	perm, err := s.permission(ctx, c, repo, login)
	if err != nil {
		return err
	}
	if !gh.HasPushPermission(perm) {
		s.m.CommandReceived(string(cmd.Verb), false)
		s.audit(ctx, login, "command_ignored", target, withDetail(details, "reason", "no write permission"))
		return nil
	}
	first, err := s.claimComment(ctx, repo, ev.Comment.ID)
	if err != nil || !first {
		return err
	}
	since := s.now().Add(-commentWindow)
	n, err := s.st.CountAuditDetail(ctx, "command", target, "pr", prDetail, since)
	if err != nil {
		return storeErr(err, "count commands on %s#%d", target, pr)
	}
	if n >= s.cfg.CommentRateLimit {
		s.m.CommandReceived(string(cmd.Verb), false)
		s.audit(ctx, login, "command", target, withDetail(withDetail(details, "accepted", false), "reason", "rate limited"))
		warned, err := s.st.CountAuditDetail(ctx, "command_rate_limited", target, "pr", prDetail, since)
		if err != nil {
			return storeErr(err, "count rate limit notices on %s#%d", target, pr)
		}
		if warned == 0 {
			s.audit(ctx, login, "command_rate_limited", target, map[string]any{"repo": repo.FullName, "pr": pr})
			s.comment(ctx, repo, pr, fmt.Sprintf("**`%s` was refused.** This pull request sent more than %d Stackorder commands in the last minute; wait a minute and comment again.\n",
				cmd.String(), s.cfg.CommentRateLimit))
		}
		return nil
	}
	s.react(ctx, c, repo, ev.Comment.ID, gh.ReactionEyes)
	var out commandOutcome
	switch cmd.Verb {
	case command.Help:
		s.comment(ctx, repo, pr, command.HelpText())
		out = commandOutcome{accepted: true}
	case command.Plan:
		out, err = s.commandPlan(ctx, repo, c, pr, cmd, login, v1.TriggerComment)
	case command.Apply:
		out, err = s.commandApply(ctx, repo, c, pr, cmd, login)
	case command.Unlock:
		out, err = s.commandUnlock(ctx, repo, pr, cmd, login)
	}
	if err != nil {
		s.log.ErrorContext(ctx, "comment command failed", "repo", repo.FullName, "pr", pr, "command", cmd.String(), "error", err)
		s.m.CommandReceived(string(cmd.Verb), false)
		s.audit(ctx, login, "command", target, withDetail(withDetail(details, "accepted", false), "reason", "error: "+err.Error()))
		s.comment(ctx, repo, pr, "**`"+cmd.String()+"` failed.** Stackorder hit an error while running it and will not retry it; "+
			"the server log has the details. Comment the command again.\n")
		return nil
	}
	if out.dispatched {
		s.react(ctx, c, repo, ev.Comment.ID, gh.ReactionRocket)
	}
	s.m.CommandReceived(string(cmd.Verb), out.accepted)
	details = withDetail(details, "accepted", out.accepted)
	if out.reason != "" {
		details = withDetail(details, "reason", out.reason)
	}
	if out.runID != uuid.Nil {
		details = withDetail(details, "run_id", out.runID.String())
	}
	s.audit(ctx, login, "command", target, details)
	return nil
}

type commandOutcome struct {
	accepted   bool
	dispatched bool
	reason     string
	runID      uuid.UUID
}

func withDetail(d map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(d)+1)
	for key, val := range d {
		out[key] = val
	}
	out[k] = v
	return out
}

func isBot(u gh.User) bool {
	return strings.EqualFold(u.Type, "Bot") || strings.HasSuffix(u.Login, "[bot]")
}

func unknownCommand(body string) bool {
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || strings.HasPrefix(t, ">") || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		f := strings.Fields(t)
		if len(f) >= 2 && strings.EqualFold(f[0], command.Prefix) && !command.Verb(strings.ToLower(f[1])).Valid() {
			return true
		}
	}
	return false
}

func (s *Service) claimComment(ctx context.Context, repo store.Repo, commentID int64) (bool, error) {
	target := "comment:" + strconv.FormatInt(repo.ID, 10) + ":" + strconv.FormatInt(commentID, 10)
	first := false
	err := s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, target); err != nil {
			return err
		}
		n, err := tx.CountAudit(ctx, "command_received", target, s.now().AddDate(-1, 0, 0))
		if err != nil || n > 0 {
			return err
		}
		first = true
		_, err = tx.RecordAudit(ctx, store.AuditEntry{Actor: schedulerActor, Action: "command_received", Target: target})
		return err
	})
	if err != nil {
		return false, storeErr(err, "claim comment %d", commentID)
	}
	return first, nil
}

func (s *Service) react(ctx context.Context, c *gh.Client, repo store.Repo, commentID int64, content string) {
	if err := c.CreateReaction(ctx, repo.FullName, commentID, content); err != nil {
		s.log.WarnContext(ctx, "react to comment", "repo", repo.FullName, "comment_id", commentID, "error", err)
	}
}

func refusalText(cmd, reason string) string {
	return "**`" + cmd + "` was refused.** " + reason + "\n"
}

func (s *Service) commandPlan(ctx context.Context, repo store.Repo, c *gh.Client, pr int, cmd *command.Command, login string, trigger v1.Trigger) (commandOutcome, error) {
	run, refusal, err := s.planPullRequest(ctx, repo, c, pr, cmd.Stacks, trigger, login)
	if err != nil {
		return commandOutcome{}, err
	}
	if refusal != "" {
		s.comment(ctx, repo, pr, refusalText(cmd.String(), refusal))
		return commandOutcome{reason: refusal}, nil
	}
	return commandOutcome{accepted: true, dispatched: true, runID: run.ID}, nil
}

func (s *Service) planPullRequest(ctx context.Context, repo store.Repo, c *gh.Client, pr int, keys []string, trigger v1.Trigger, login string) (store.Run, string, error) {
	pull, err := c.GetPull(ctx, repo.FullName, pr)
	if err != nil {
		return store.Run{}, "", fmt.Errorf("runs: pull request %s#%d: %w", repo.FullName, pr, err)
	}
	if pull.State != gh.IssueOpen {
		return store.Run{}, "The pull request is closed.", nil
	}
	if pull.IsFork() {
		title, _ := report.ForkNotice()
		return store.Run{}, title + ".", nil
	}
	view, err := planViewAt(ctx, s.st, repo.ID, pr, pull.HeadSHA)
	if err != nil {
		return store.Run{}, "", err
	}
	g, graphID, err := s.headGraph(ctx, repo, view, pull.HeadSHA)
	if errors.Is(err, store.ErrNotFound) {
		return store.Run{}, fmt.Sprintf("No graph was uploaded for the head commit %s yet; the stackorder plan workflow uploads it when it runs on a push.", shortSHA(pull.HeadSHA)), nil
	}
	if err != nil {
		return store.Run{}, "", err
	}
	req := serverPlan{
		repo: repo, pr: pr, sha: pull.HeadSHA, baseSHA: pull.BaseSHA, graph: g, graphID: graphID,
		keys: keys, prior: view.stored, trigger: trigger, requester: login,
	}
	if len(req.keys) == 0 {
		req.keys = view.keys()
	}
	if len(req.keys) == 0 {
		if req.changed, err = c.ListPullFiles(ctx, repo.FullName, pr); err != nil {
			return store.Run{}, "", fmt.Errorf("runs: files of %s#%d: %w", repo.FullName, pr, err)
		}
	}
	return s.startServerPlan(ctx, req)
}

func (s *Service) headGraph(ctx context.Context, repo store.Repo, view planView, sha string) (*v1.Graph, uuid.UUID, error) {
	if id, ok := view.graphID(); ok {
		g, err := s.st.GetGraphByID(ctx, id)
		if err == nil {
			return g, id, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, uuid.Nil, storeErr(err, "graph %s", id)
		}
	}
	g, id, err := s.st.GetGraph(ctx, repo.ID, sha)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, uuid.Nil, storeErr(err, "graph of %s", shortSHA(sha))
	}
	return g, id, err
}

type serverPlan struct {
	repo      store.Repo
	pr        int
	sha       string
	baseSHA   string
	graph     *v1.Graph
	graphID   uuid.UUID
	keys      []string
	changed   []string
	prior     map[string]store.RunStack
	trigger   v1.Trigger
	requester string
	warnings  []string
	claim     func(ctx context.Context, tx *store.Store) (bool, error)
}

var errPlanClaimed = errors.New("runs: plan already started")

func (s *Service) startServerPlan(ctx context.Context, req serverPlan) (store.Run, string, error) {
	locks, err := s.lockMap(ctx, req.repo.ID, req.pr)
	if err != nil {
		return store.Run{}, "", err
	}
	g := s.withExternalDependents(ctx, req.repo, req.graph)
	resp, rerr := graph.Resolve(g, graph.Input{ChangedPaths: req.changed, Config: repoConfig(req.repo), Requested: req.keys, Locks: locks})
	if refusal := planRefusal(resp, rerr, req); refusal != "" {
		return store.Run{}, refusal, nil
	}
	if err := s.enforcePlanOutput(ctx, req.repo, resp.Affected); err != nil {
		return store.Run{}, "", err
	}
	var rows []store.RunStack
	for _, a := range resp.Affected {
		st, err := s.st.GetStackByKey(ctx, req.repo.ID, a.Key)
		if err != nil {
			return store.Run{}, "", storeErr(err, "stack %s", a.Key)
		}
		row := store.RunStack{StackID: st.ID, Wave: a.Wave, Status: v1.StackPending, Mode: v1.ModePlan,
			Reasons: a.Reasons, Via: a.Via, Environment: a.Environment, PlanOutput: a.PlanOutput}
		if prior, ok := req.prior[a.Key]; ok {
			row.Reasons, row.Via, row.Environment = prior.Reasons, prior.Via, firstNonEmpty(prior.Environment, a.Environment)
		}
		rows = append(rows, row)
	}
	warnings := slices.Concat(req.warnings, resp.Warnings)
	var run store.Run
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		if req.claim != nil {
			first, err := req.claim(ctx, tx)
			if err != nil {
				return err
			}
			if !first {
				return errPlanClaimed
			}
		}
		var err error
		run, err = tx.CreateRun(ctx, store.CreateRunParams{
			RepoID: req.repo.ID, SHA: req.sha, BaseSHA: req.baseSHA, PRNumber: req.pr,
			Trigger: req.trigger, Mode: v1.ModePlan, Status: v1.RunPending, RequestedBy: req.requester,
		})
		if err != nil {
			return err
		}
		if err := tx.UpsertRunStacks(ctx, run.ID, rows); err != nil {
			return err
		}
		return tx.SetRunGraph(ctx, run.ID, req.graphID, len(resp.Waves), warnings)
	})
	if errors.Is(err, errPlanClaimed) {
		return store.Run{}, "", nil
	}
	if err != nil {
		return store.Run{}, "", storeErr(err, "plan run on %s", req.repo.FullName)
	}
	s.m.RunStatusChanged(run.Status, run.Trigger, run.Mode)
	if err := s.dispatchWave(ctx, run.ID, 0); err != nil {
		return store.Run{}, "", err
	}
	return run, "", nil
}

func planRefusal(resp *v1.ResolveResponse, rerr error, req serverPlan) string {
	switch {
	case errors.Is(rerr, graph.ErrCycle):
		spelled := make([]string, len(resp.Cycles))
		for i, cyc := range resp.Cycles {
			spelled[i] = "`" + report.SpellCycle(cyc) + "`"
		}
		return "The graph has a dependency cycle: " + strings.Join(spelled, ", ") + "."
	case rerr != nil:
		return "The graph could not be resolved: " + rerr.Error() + "."
	case len(resp.Affected) == 0 && len(req.keys) > 0:
		named := make([]string, len(req.keys))
		for i, k := range req.keys {
			named[i] = "`" + k + "`"
		}
		return "None of " + strings.Join(named, ", ") + " is a stack of the graph for " + shortSHA(req.sha) + "."
	case len(resp.Affected) == 0:
		return "No stack is affected by this pull request, so there is nothing to plan."
	}
	return ""
}

func (s *Service) commandApply(ctx context.Context, repo store.Repo, c *gh.Client, pr int, cmd *command.Command, login string) (commandOutcome, error) {
	if repoConfig(repo).Apply.Mode == v1.ApplyOnMerge {
		reason := "This repository applies on merge (`apply.mode: on_merge`), so the apply starts when the pull request is merged."
		s.comment(ctx, repo, pr, refusalText(cmd.String(), reason))
		return commandOutcome{reason: reason}, nil
	}
	opts, err := s.reportOptions(ctx, s.st, repo, pr)
	if err != nil {
		return commandOutcome{}, err
	}
	res, err := s.gate(ctx, gateInput{repo: repo, client: c, pr: pr, requester: login, keys: cmd.Stacks, layers: layersAll})
	if err != nil {
		return commandOutcome{}, err
	}
	if nothingToApply(res) {
		reason := "No stack is affected by this pull request, so there is nothing to apply."
		s.comment(ctx, repo, pr, refusalText(cmd.String(), reason))
		return commandOutcome{reason: reason}, nil
	}
	if len(res.failures) > 0 {
		s.comment(ctx, repo, pr, report.RefusalComment(cmd.String(), res.failures, opts))
		return commandOutcome{reason: gateSummary(res.failures)}, nil
	}
	run, failures, err := s.startApply(ctx, applyRequest{
		repo: repo, pr: pr, sha: res.pull.HeadSHA, baseSHA: res.pull.BaseSHA, view: res.view,
		keys: res.keys, trigger: v1.TriggerComment, requester: login,
	})
	if err != nil {
		return commandOutcome{}, err
	}
	if len(failures) > 0 {
		s.comment(ctx, repo, pr, report.RefusalComment(cmd.String(), failures, opts))
		return commandOutcome{reason: gateSummary(failures)}, nil
	}
	return commandOutcome{accepted: true, dispatched: true, runID: run.ID}, nil
}

func nothingToApply(res gateResult) bool {
	return len(res.view.runs) > 0 && len(res.keys) == 0
}

func gateSummary(failures []report.GateFailure) string {
	parts := make([]string, len(failures))
	for i, f := range failures {
		parts[i] = fmt.Sprintf("layer %d: %s", f.Layer, f.Reason)
	}
	return strings.Join(parts, "; ")
}

type applyRequest struct {
	repo      store.Repo
	pr        int
	sha       string
	baseSHA   string
	view      planView
	keys      []string
	trigger   v1.Trigger
	requester string
}

func (s *Service) startApply(ctx context.Context, req applyRequest) (store.Run, []report.GateFailure, error) {
	graphID, ok := req.view.graphID()
	if !ok {
		return store.Run{}, []report.GateFailure{{Layer: report.LayerPlans, Reason: "the plans of the head commit have no graph"}}, nil
	}
	g, err := s.st.GetGraphByID(ctx, graphID)
	if err != nil {
		return store.Run{}, nil, storeErr(err, "graph %s", graphID)
	}
	cfg := repoConfig(req.repo)
	defaults, err := s.defaultStackConfigs(ctx, req.repo, req.view.keys())
	if err != nil {
		return store.Run{}, nil, err
	}
	waves := subsetWaves(g, req.view.keys(), req.keys)
	var (
		rows   []store.RunStack
		locked []uuid.UUID
		last   int
	)
	for _, key := range req.view.keys() {
		src := req.view.stored[key]
		env, err := applyEnvironment(cfg, defaults, src)
		if err != nil {
			return store.Run{}, nil, err
		}
		row := store.RunStack{
			StackID: src.StackID, Mode: v1.ModeApply, Status: v1.StackSkipped, Reasons: src.Reasons, Via: src.Via,
			Environment: env, PlanOutput: src.PlanOutput,
			Summary: src.Summary, Adds: src.Adds, Changes: src.Changes, Destroys: src.Destroys, Replaces: src.Replaces,
			HasChanges: src.HasChanges, PlanArtifact: src.PlanArtifact, PlanRunID: src.PlanRunID,
		}
		if slices.Contains(req.keys, key) {
			row.Wave = waves[key]
			last = max(last, row.Wave)
			row.Status = v1.StackPlanned
			if noopCandidate(src.Reasons, src.Summary, src.HasChanges) {
				row.Status = v1.StackNoop
			}
			locked = append(locked, src.StackID)
		}
		rows = append(rows, row)
	}
	var (
		run       store.Run
		conflicts []store.Lock
		running   store.Run
	)
	err = s.st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockKey(ctx, fmt.Sprintf("apply:%d:%d", req.repo.ID, req.pr)); err != nil {
			return err
		}
		active, found, err := activeApply(ctx, tx, req.repo.ID, req.pr, s.now())
		if err != nil {
			return err
		}
		if found {
			running = active
			return errApplyRunning
		}
		run, err = tx.CreateRun(ctx, store.CreateRunParams{
			RepoID: req.repo.ID, SHA: req.sha, BaseSHA: req.baseSHA, PRNumber: req.pr,
			Trigger: req.trigger, Mode: v1.ModeApply, Status: v1.RunPending, RequestedBy: req.requester,
		})
		if err != nil {
			return err
		}
		reason := fmt.Sprintf("apply of #%d by %s", req.pr, req.requester)
		if conflicts, err = tx.TryLockStacks(ctx, locked, run.ID, req.pr, reason); err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return errLockConflict
		}
		if err := tx.UpsertRunStacks(ctx, run.ID, rows); err != nil {
			return err
		}
		return tx.SetRunGraph(ctx, run.ID, graphID, last+1, nil)
	})
	if errors.Is(err, errApplyRunning) {
		return store.Run{}, []report.GateFailure{{Layer: report.LayerLocks,
			Reason: fmt.Sprintf("apply run %s of this pull request is still %s; wait for it to finish before applying again", running.ID, running.Status)}}, nil
	}
	if errors.Is(err, errLockConflict) {
		failures := make([]report.GateFailure, 0, len(conflicts))
		for _, l := range conflicts {
			holder := "a manual run"
			if l.PRNumber > 0 {
				holder = fmt.Sprintf("#%d", l.PRNumber)
			}
			failures = append(failures, report.GateFailure{Layer: report.LayerLocks, Stacks: []string{l.StackKey},
				Reason: "locked by " + holder + " while this apply was starting; release it there with `stackorder unlock`"})
		}
		return store.Run{}, failures, nil
	}
	if err != nil {
		return store.Run{}, nil, storeErr(err, "apply run on %s", req.repo.FullName)
	}
	s.m.RunStatusChanged(run.Status, run.Trigger, run.Mode)
	s.refreshLocksGauge(ctx)
	if err := s.dispatchWave(ctx, run.ID, 0); err != nil {
		return store.Run{}, nil, err
	}
	return run, nil, nil
}

var errApplyRunning = errors.New("runs: an apply of the pull request is running")

func activeApply(ctx context.Context, db *store.Store, repoID int64, pr int, now time.Time) (store.Run, bool, error) {
	if pr <= 0 {
		return store.Run{}, false, nil
	}
	runs, _, err := db.ListRuns(ctx, store.RunFilter{RepoID: repoID, PRNumber: pr, Mode: v1.ModeApply, Limit: 20})
	if err != nil {
		return store.Run{}, false, err
	}
	for _, r := range runs {
		switch {
		case r.Status == v1.RunApplying, r.Status == v1.RunPlanned:
			return r, true, nil
		case r.Status == v1.RunPending && now.Sub(r.CreatedAt) < unboundGrace:
			return r, true, nil
		}
	}
	return store.Run{}, false, nil
}

func applyEnvironment(cfg *v1.RepoConfig, defaults map[string]*v1.StackConfig, src store.RunStack) (string, error) {
	dir, instance := v1.SplitStackKey(src.Key)
	sc := defaults[src.Key]
	eff, err := config.Resolve(cfg, cmp.Or(src.Path, dir), sc, instance)
	if err != nil {
		return "", fmt.Errorf("runs: environment of %s: %w", src.Key, err)
	}
	if sc == nil && !eff.EnvironmentConfigured && src.Environment != "" {
		return src.Environment, nil
	}
	return eff.Environment, nil
}

func (s *Service) commandUnlock(ctx context.Context, repo store.Repo, pr int, cmd *command.Command, login string) (commandOutcome, error) {
	var released []store.Lock
	if len(cmd.Stacks) == 0 {
		locks, err := s.st.ReleaseLocksForPR(ctx, repo.ID, pr)
		if err != nil {
			return commandOutcome{}, storeErr(err, "release locks of #%d", pr)
		}
		released = locks
	} else {
		for _, key := range cmd.Stacks {
			st, err := s.st.GetStackByKey(ctx, repo.ID, key)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return commandOutcome{}, storeErr(err, "stack %s", key)
			}
			l, err := s.st.ReleaseLockOfPR(ctx, st.ID, pr)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return commandOutcome{}, storeErr(err, "release lock of %s", key)
			}
			released = append(released, l)
		}
	}
	for _, l := range released {
		s.audit(ctx, login, "unlock", v1.QualifiedStackKey(repo.FullName, l.StackKey), map[string]any{
			"repo": repo.FullName, "stack": l.StackKey, "stack_id": l.StackID.String(), "pr": l.PRNumber,
			"run_id": l.RunID.String(), "via": "comment",
		})
	}
	s.refreshLocksGauge(ctx)
	s.comment(ctx, repo, pr, report.UnlockedComment(lockInfos(released), login))
	return commandOutcome{accepted: true}, nil
}
