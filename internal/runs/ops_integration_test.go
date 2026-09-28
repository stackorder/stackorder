//go:build integration

package runs_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/command"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
)

func TestDriftOpensAndClosesIssue(t *testing.T) {
	cfg := baseConfig()
	cfg.Drift = v1.DriftConfig{Schedule: "0 6 * * *", OpenIssue: true}
	e := newEnv(t, cfg)
	g := testGraph(mainSHA)
	gid, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, gid))

	q := &jobQueue{keys: map[string]bool{}}
	require.NoError(t, e.svc.ScheduleDrift(e.ctx, repoID, q.enqueue))
	require.NoError(t, e.svc.ScheduleDrift(e.ctx, repoID, q.enqueue), "scheduling twice in an hour adds nothing")
	jobs := q.take()
	require.Len(t, jobs, 5, "one job per stack of the graph")
	now := e.clock.Now()
	for i, j := range jobs {
		assert.Equal(t, runs.JobDrift, j.Kind)
		assert.Equal(t, now.Add(time.Duration(i)*12*time.Minute), j.RunAfter, "spread across the hour")
		assert.True(t, strings.HasPrefix(j.Dedupe, "drift:"))
		assert.True(t, strings.HasSuffix(j.Dedupe, ":"+now.Format("2006010215")))
	}

	st, err := e.st.GetStackByKey(e.ctx, repoID, vpc)
	require.NoError(t, err)
	var job runs.DriftJob
	for _, j := range jobs {
		require.NoError(t, json.Unmarshal(j.Payload, &job))
		if job.StackID == st.ID.String() {
			break
		}
	}
	payload, err := json.Marshal(job)
	require.NoError(t, err)
	require.NoError(t, e.svc.HandleJob(e.ctx, runs.JobDrift, payload))
	require.NoError(t, e.svc.RunDriftStack(e.ctx, repoID, st.ID.String()), "the hour's drift run is reused")
	ds := e.gh.Dispatches()
	require.Len(t, ds, 1)
	assert.Equal(t, "drift", ds[0].Inputs["mode"])
	assert.Equal(t, mainSHA, ds[0].Inputs["sha"], "drift checks the default branch head")
	es := entries(t, ds[0])
	require.Len(t, es, 1)
	assert.Equal(t, vpc, es[0].Key)
	assert.Equal(t, v1.DefaultEnvironment, es[0].Environment)

	drifted := v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, ExitCode: 2, HasChanges: true, Summary: &v1.PlanSummary{Changes: 1, Changed: []string{"aws_vpc.main"}}}
	p := e.dispatchJob(ds[0].RunID, v1.DefaultEnvironment)
	_, err = e.svc.RecordResult(e.ctx, p, ds[0].Inputs["run_id"], vpc, drifted)
	require.NoError(t, err)
	assert.Equal(t, v1.RunPlanned, e.run(ds[0].Inputs["run_id"]).Status)
	latest, err := e.st.LatestDrift(e.ctx, st.ID)
	require.NoError(t, err)
	assert.True(t, latest.Drifted)
	issues := e.gh.Issues(repoName)
	require.Len(t, issues, 1)
	assert.Equal(t, report.DriftIssueTitle(vpc), issues[0].Title)
	assert.Equal(t, gh.IssueOpen, issues[0].State)
	assert.Contains(t, []string(issues[0].Labels), "stackorder-drift")
	assert.Equal(t, issues[0].Number, latest.IssueNumber)
	_, drift, _ := e.m.snapshot()
	assert.Equal(t, 1, drift)

	e.clock.Advance(time.Hour)
	require.NoError(t, e.svc.RunDriftStack(e.ctx, repoID, st.ID.String()))
	ds = e.gh.Dispatches()
	require.Len(t, ds, 2, "a new hour starts a new drift run")
	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(ds[1].RunID, v1.DefaultEnvironment), ds[1].Inputs["run_id"], vpc, drifted)
	require.NoError(t, err)
	require.Len(t, e.gh.Issues(repoName), 1, "the existing issue is updated")

	e.clock.Advance(time.Hour)
	require.NoError(t, e.svc.RunDriftStack(e.ctx, repoID, st.ID.String()))
	ds = e.gh.Dispatches()
	require.Len(t, ds, 3)
	clean := v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}
	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(ds[2].RunID, v1.DefaultEnvironment), ds[2].Inputs["run_id"], vpc, clean)
	require.NoError(t, err)
	issues = e.gh.Issues(repoName)
	require.Len(t, issues, 1)
	assert.Equal(t, gh.IssueClosed, issues[0].State)
	cs := e.gh.Comments(repoName, issues[0].Number)
	require.Len(t, cs, 1)
	assert.Contains(t, cs[0].Body, "found no drift")
	_, drift, _ = e.m.snapshot()
	assert.Zero(t, drift)

	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(ds[2].RunID, "production"), ds[2].Inputs["run_id"], vpc, clean)
	require.ErrorIs(t, err, principal.ErrForbidden, "drift jobs must run under the default environment")
}

func TestCommentRateLimitAndPermissions(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	e.gh.SetCollaboratorPermission(repoName, "reader", "read")

	ignored := e.comment(7, "reader", "stackorder help")
	assert.Empty(t, e.comments(7)[1:], "a read-only user gets no answer")
	assert.Empty(t, e.gh.Reactions(ignored.ID))
	assert.Len(t, e.audit("command_ignored"), 1)
	assert.Empty(t, e.audit("command"))

	bot := e.gh.IssueCommentEvent(repoName, 7, "stackorder help", "renovate[bot]")
	require.NoError(t, e.svc.HandleIssueComment(e.ctx, bot))
	assert.Empty(t, e.gh.Reactions(bot.Comment.ID), "bots are ignored")

	for range 10 {
		e.comment(7, applier, "stackorder help")
	}
	helps := 0
	for _, c := range e.comments(7) {
		if strings.HasPrefix(c, "**Stackorder commands**") {
			helps++
		}
	}
	assert.Equal(t, 10, helps)
	limited := e.comment(7, applier, "stackorder help")
	assert.Contains(t, e.lastComment(7), "sent more than 10 Stackorder commands in the last minute")
	e.comment(7, applier, "stackorder help")
	refusals := 0
	for _, c := range e.comments(7) {
		if strings.Contains(c, "in the last minute") {
			refusals++
		}
	}
	assert.Equal(t, 1, refusals, "one refusal per window, however many commands are dropped")
	assert.Empty(t, e.gh.Reactions(limited.ID))
	assert.Len(t, e.audit("command"), 12, "every accepted or refused command is audited")

	e.clock.Advance(2 * time.Minute)
	ok := e.comment(7, applier, "stackorder help")
	assert.Equal(t, []string{gh.ReactionEyes}, e.gh.Reactions(ok.ID), "the window moves on")
	_, _, cmds := e.m.snapshot()
	assert.Equal(t, 11, cmds["help:true"])
	assert.Equal(t, 3, cmds["help:false"], "the read-only user and the two dropped commands")

	redelivered := e.gh.IssueCommentEvent(repoName, 7, "stackorder help", applier)
	require.NoError(t, e.svc.HandleIssueComment(e.ctx, redelivered))
	n := len(e.comments(7))
	require.NoError(t, e.svc.HandleIssueComment(e.ctx, redelivered))
	assert.Len(t, e.comments(7), n, "a comment is executed once")
}

func TestCommentCommands(t *testing.T) {
	e := newEnv(t, baseConfig())
	planRun := e.planned(7, headSHA)

	e.comment(7, applier, "stackorder deploy everything")
	assert.Equal(t, command.HelpText(), e.lastComment(7), "an unknown verb gets the help")

	cm := e.comment(7, applier, "please\nstackorder plan "+eks+" stacks/none")
	assert.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, e.gh.Reactions(cm.ID))
	ds := e.gh.Dispatches()
	require.Len(t, ds, 1)
	assert.Equal(t, []string{eks}, entryKeys(entries(t, ds[0])))
	replan := e.run(ds[0].Inputs["run_id"])
	assert.Equal(t, v1.TriggerComment, replan.Trigger)
	assert.Equal(t, applier, replan.RequestedBy)
	assert.Equal(t, headSHA, replan.SHA)
	assert.Equal(t, v1.RunPlanning, replan.Status)
	assert.Equal(t, []v1.Reason{v1.ReasonDependent}, replan.Stacks[0].Reasons, "the reasons of the earlier plan are kept")
	assert.Contains(t, strings.Join(replan.Warnings, "\n"), "stacks/none")

	rollup := e.check(report.CheckPlan)
	assert.Equal(t, gh.CheckRunInProgress, rollup.Status, "the merged plan of the head is running again")
	assert.Contains(t, rollup.Output.Title, "3 of 4 stacks done")

	res := planResult(eks, headSHA, 5)
	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, e.gh.WorkflowRunEvent("requested", repoName, gh.WorkflowRun{ID: ds[0].RunID})))
	_, err := e.svc.RecordResult(e.ctx, e.dispatchJob(ds[0].RunID+1, v1.DefaultEnvironment), replan.ID, eks, res)
	require.ErrorIs(t, err, principal.ErrForbidden, "a dispatched plan only takes results from its own workflow run")
	_, err = e.svc.RecordResult(e.ctx, e.planJob(7).p, replan.ID, eks, res)
	require.ErrorIs(t, err, principal.ErrForbidden, "a pull request's plan job cannot report into a plan the server dispatched")
	_, err = e.svc.UploadGraph(e.ctx, e.planJob(7).p, replan.ID, v1.GraphUploadRequest{Graph: testGraph(headSHA), ChangedPaths: []string{"stacks/prod/apps/main.tf"}})
	require.ErrorIs(t, err, principal.ErrForbidden, "nor replace its stacks")
	_, err = e.svc.GetRunForPrincipal(e.ctx, e.planJob(7).p, replan.ID)
	require.ErrorIs(t, err, principal.ErrForbidden)
	p := e.dispatchJob(ds[0].RunID, v1.DefaultEnvironment)
	_, err = e.svc.RecordResult(e.ctx, p, replan.ID, eks, res)
	require.NoError(t, err)
	assert.Equal(t, v1.RunPlanned, e.run(replan.ID).Status)
	rollup = e.check(report.CheckPlan)
	assert.Equal(t, gh.ConclusionSuccess, rollup.Conclusion)
	assert.Equal(t, "4 stacks: 8 to add, 0 to change, 0 to destroy", rollup.Output.Title, "the newest plan of each stack counts")
	assert.Equal(t, v1.RunPlanned, e.run(planRun).Status)

	e.comment(7, applier, "stackorder apply "+eks)
	apply := e.applyRun(7)
	assert.Equal(t, map[string]v1.StackStatus{vpc: v1.StackSkipped, staging: v1.StackSkipped, eks: v1.StackPlanned, apps: v1.StackSkipped}, stackStatuses(apply))
	assert.Equal(t, map[string]int{eks: 7}, e.locks(), "only the named stacks are locked")
	ds = e.gh.Dispatches()
	require.Len(t, ds, 2)
	es := entries(t, ds[1])
	require.Len(t, es, 1)
	assert.Equal(t, eks, es[0].Key)
	assert.Equal(t, 0, es[0].Wave, "a subset is ordered among itself")
	assert.Equal(t, ds[0].RunID, es[0].PlanRunID, "the re-plan's artifact is applied")
	e.reportAll(ds[1], nil)
	assert.Equal(t, v1.RunApplied, e.run(apply.ID).Status)
	assert.Contains(t, e.check(report.CheckApply).Output.Title, "(3 skipped)")

	e.comment(7, applier, "stackorder unlock "+vpc)
	assert.Equal(t, "No orchestration locks matched, so nothing was released.\n", e.lastComment(7))
	e.comment(7, applier, "stackorder unlock "+eks)
	assert.Contains(t, e.lastComment(7), "Released 1 orchestration lock")
	assert.Empty(t, e.locks())

	e.gh.FailNext("GET /repos/{owner}/{repo}/pulls/{pull_number}", http.StatusNotFound, 1)
	failing := e.gh.IssueCommentEvent(repoName, 7, "stackorder plan", applier)
	require.NoError(t, e.svc.HandleIssueComment(e.ctx, failing))
	assert.Contains(t, e.lastComment(7), "**`stackorder plan` failed.**", "a command that fails part way is answered")
	assert.Contains(t, e.lastComment(7), "Comment the command again")
	n := len(e.comments(7))
	require.NoError(t, e.svc.HandleIssueComment(e.ctx, failing))
	assert.Len(t, e.comments(7), n, "a failed comment is not executed again on redelivery")

	e.gh.SetPull(repoName, gh.PullRequest{Number: 7, State: gh.IssueClosed, HeadSHA: headSHA, User: gh.User{Login: author}})
	e.comment(7, applier, "stackorder plan")
	assert.Equal(t, "**`stackorder plan` was refused.** The pull request is closed.\n", e.lastComment(7))
}

func TestPropagatedNoopAndRerun(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	job, runID, resp := e.startPlan(7, headSHA)
	for _, a := range resp.Affected {
		adds := 1
		if a.Key == apps || a.Key == eks {
			adds = 0
		}
		_, err := e.svc.RecordResult(e.ctx, job.p, runID, a.Key, planResult(a.Key, headSHA, adds))
		require.NoError(t, err)
	}
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	assert.Equal(t, v1.StackNoop, stackStatuses(apply)[eks], "a propagated stack with an empty plan is a no-op")
	assert.Equal(t, v1.StackNoop, stackStatuses(apply)[apps])
	for _, d := range e.gh.Dispatches() {
		e.reportAll(d, nil)
	}
	assert.Len(t, e.gh.Dispatches(), 2, "waves of no-ops advance without a dispatch")
	assert.Equal(t, v1.RunApplied, e.run(apply.ID).Status)

	_, err := e.svc.Rerun(e.ctx, "stranger", runID)
	require.ErrorIs(t, err, principal.ErrForbidden)
	_, err = e.svc.Rerun(e.ctx, applier, apply.ID)
	require.ErrorIs(t, err, principal.ErrInvalid, "only plans are re-run")
	rerun, err := e.svc.Rerun(e.ctx, applier, runID)
	require.NoError(t, err)
	assert.Equal(t, v1.TriggerRerequest, rerun.Trigger)
	assert.Len(t, rerun.Stacks, 4)
	ds := e.gh.Dispatches()
	require.Len(t, ds, 3)
	assert.Equal(t, "plan", ds[2].Inputs["mode"])
	assert.Len(t, entries(t, ds[2]), 4)
	assert.Len(t, e.audit("rerun"), 1)

	rerequest := e.gh.CheckRunEvent("rerequested", repoName, gh.CheckRun{
		Name: report.StackCheckName(report.CheckPlan, vpc), HeadSHA: headSHA,
		PullRequests: []gh.PullRequestRef{{Number: 7}},
	}, "")
	rerequest.Sender = gh.User{Login: applier}
	require.NoError(t, e.svc.HandleCheckRun(e.ctx, rerequest))
	ds = e.gh.Dispatches()
	require.Len(t, ds, 4)
	assert.Equal(t, []string{vpc}, entryKeys(entries(t, ds[3])))
	assert.Equal(t, v1.TriggerRerequest, e.run(ds[3].Inputs["run_id"]).Trigger)
	require.NoError(t, e.svc.HandleCheckSuite(e.ctx, &gh.CheckSuiteEvent{}))
	require.NoError(t, e.svc.HandlePullRequestReview(e.ctx, &gh.PullRequestReviewEvent{}))
}

func TestWorkflowRunCompletedWithoutResult(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	var prod, stage int64
	for _, d := range e.gh.Dispatches() {
		if entries(t, d)[0].Environment == "production" {
			prod = d.RunID
		} else {
			stage = d.RunID
		}
	}
	_, err := e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(prod, "production"), apply.ID)
	require.NoError(t, err)
	e.gh.CompleteWorkflowRun(repoName, prod, gh.ConclusionFailure)
	ev := e.gh.WorkflowRunEvent("completed", repoName, gh.WorkflowRun{ID: prod})
	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, ev))
	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, ev), "a redelivered completion changes nothing")
	r := e.run(apply.ID)
	assert.Equal(t, v1.StackUnknown, stackStatuses(r)[vpc])
	assert.Equal(t, v1.StackBlocked, stackStatuses(r)[eks])
	assert.Equal(t, v1.StackPlanned, stackStatuses(r)[staging], "the other environment's dispatch is still running")
	assert.Equal(t, v1.RunApplying, r.Status)

	_, err = e.svc.GetRunForPrincipal(e.ctx, e.dispatchJob(stage, "staging"), apply.ID)
	require.NoError(t, err)
	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(stage, "staging"), apply.ID, staging, applyResult(true))
	require.NoError(t, err)
	r = e.run(apply.ID)
	assert.Equal(t, v1.RunFailed, r.Status)
	unknown := e.check(report.StackCheckName(report.CheckApply, vpc))
	assert.Equal(t, gh.ConclusionFailure, unknown.Conclusion)
	assert.Equal(t, "Result unknown: the job ended without reporting", unknown.Output.Title)
	d, err := e.st.FindDispatchByWorkflowRun(e.ctx, prod)
	require.NoError(t, err)
	assert.Equal(t, "failure", d.Conclusion)

	other := e.gh.AddWorkflowRun(repoName, gh.WorkflowRun{Path: ".github/workflows/unrelated.yml", Status: gh.RunStatusCompleted})
	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, e.gh.WorkflowRunEvent("completed", repoName, other)))
}

func TestReconcile(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	require.Len(t, e.gh.Dispatches(), 2)

	require.NoError(t, e.svc.Reconcile(e.ctx))
	dispatches, err := e.st.ListDispatches(e.ctx, uuid.MustParse(apply.ID))
	require.NoError(t, err)
	for _, d := range dispatches {
		assert.Nil(t, d.WorkflowRunID, "a fresh dispatch is left to its webhook")
	}

	e.clock.Advance(2 * time.Minute)
	require.NoError(t, e.svc.Reconcile(e.ctx))
	dispatches, err = e.st.ListDispatches(e.ctx, uuid.MustParse(apply.ID))
	require.NoError(t, err)
	for _, d := range dispatches {
		require.NotNil(t, d.WorkflowRunID, "bound by display title and, where two share it, by their jobs")
	}
	byRun := map[int64]string{}
	for _, d := range e.gh.Dispatches() {
		byRun[d.RunID] = entries(t, d)[0].Environment
	}
	for _, d := range dispatches {
		assert.Equal(t, d.Environment, byRun[*d.WorkflowRunID])
	}

	prodRun := *dispatches[0].WorkflowRunID
	e.gh.CompleteWorkflowRun(repoName, prodRun, gh.ConclusionCancelled)
	require.NoError(t, e.svc.Reconcile(e.ctx))
	r := e.run(apply.ID)
	assert.Equal(t, v1.StackUnknown, stackStatuses(r)[vpc], "a completed run whose stacks never reported is caught up")

	e2 := newEnv(t, baseConfig())
	e2.setNoTitles(true)
	e2.planned(7, headSHA)
	e2.comment(7, applier, "stackorder apply")
	apply2 := e2.applyRun(7)
	e2.clock.Advance(2 * time.Minute)
	require.NoError(t, e2.svc.Reconcile(e2.ctx))
	assert.Equal(t, v1.RunApplying, e2.run(apply2.ID).Status, "an unbound dispatch waits for the timeout")
	e2.clock.Advance(30 * time.Minute)
	require.NoError(t, e2.svc.Reconcile(e2.ctx))
	r2 := e2.run(apply2.ID)
	assert.Equal(t, v1.RunFailed, r2.Status)
	assert.Equal(t, v1.StackUnknown, stackStatuses(r2)[vpc])
	assert.Equal(t, v1.StackUnknown, stackStatuses(r2)[staging])
	assert.Contains(t, r2.Warnings, "workflow run not found; check that .github/workflows/stackorder-run.yml exists on the default branch")
	require.NoError(t, e2.svc.Reconcile(e2.ctx), "nothing is left to reconcile")

	e3 := newEnv(t, baseConfig())
	e3.setNoJobs(true)
	e3.planned(7, headSHA)
	e3.comment(7, applier, "stackorder apply")
	apply3 := e3.applyRun(7)
	ds := e3.gh.Dispatches()
	require.Len(t, ds, 2)
	require.Equal(t, ds[0].Inputs["wave"], ds[1].Inputs["wave"], "both environments' dispatches share one display title")
	e3.clock.Advance(31 * time.Minute)
	require.NoError(t, e3.svc.Reconcile(e3.ctx))
	r3 := e3.run(apply3.ID)
	assert.Equal(t, v1.RunApplying, r3.Status, "a workflow run that cannot be told apart yet keeps its dispatch from timing out")
	unbound, err := e3.st.ListDispatches(e3.ctx, uuid.MustParse(apply3.ID))
	require.NoError(t, err)
	for _, d := range unbound {
		assert.Nil(t, d.WorkflowRunID, "an ambiguous title binds nothing on its own")
	}

	for _, d := range ds {
		e3.gh.SetJobs(repoName, d.RunID, matrixJobs(d))
	}
	first := ds[0]
	require.NoError(t, e3.svc.HandleWorkflowRun(e3.ctx, e3.gh.WorkflowRunEvent("in_progress", repoName, gh.WorkflowRun{ID: first.RunID})))
	d, err := e3.st.FindDispatchByWorkflowRun(e3.ctx, first.RunID)
	require.NoError(t, err, "the workflow_run event binds by the stacks its jobs name")
	assert.Equal(t, entries(t, first)[0].Environment, d.Environment)
	dup := e3.gh.AddWorkflowRun(repoName, gh.WorkflowRun{Path: ".github/workflows/stackorder-run.yml", Event: "workflow_dispatch",
		DisplayTitle: "stackorder apply " + apply3.ID + " wave 0"})
	e3.gh.SetJobs(repoName, dup.ID, matrixJobs(first))
	require.NoError(t, e3.svc.HandleWorkflowRun(e3.ctx, e3.gh.WorkflowRunEvent("requested", repoName, dup)))
	_, err = e3.st.FindDispatchByWorkflowRun(e3.ctx, dup.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "a duplicated dispatch's workflow run takes no other dispatch")
	require.NoError(t, e3.svc.Reconcile(e3.ctx))
	_, err = e3.st.FindDispatchByWorkflowRun(e3.ctx, dup.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	d, err = e3.st.FindDispatchByWorkflowRun(e3.ctx, ds[1].RunID)
	require.NoError(t, err, "Reconcile binds the other one the same way")
	assert.Equal(t, entries(t, ds[1])[0].Environment, d.Environment)
	assert.Equal(t, v1.RunApplying, e3.run(apply3.ID).Status)
}

func TestDispatchFailure(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	e.gh.FailNext("POST /repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches", 404, 1)
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	assert.Equal(t, v1.RunFailed, apply.Status)
	assert.Contains(t, strings.Join(apply.Warnings, "\n"), "workflow dispatch failed")
	var found bool
	for _, c := range e.comments(7) {
		if strings.Contains(c, "GitHub refused to dispatch `.github/workflows/stackorder-run.yml` on `main`") {
			found = true
		}
	}
	assert.True(t, found, "the failure is explained on the pull request")
}

func TestPushUpdatesConfigAndModuleVersions(t *testing.T) {
	e := newEnv(t, baseConfig())
	good := []byte("version: 1\napply:\n  mode: on_merge\n  require_approvals: 2\n")
	e.gh.SetContents(repoName, newHeadSHA, "stackorder.yaml", good)
	require.NoError(t, e.svc.HandlePush(e.ctx, e.gh.PushEvent(repoName, "refs/heads/main", newHeadSHA, "stackorder.yaml")))
	repo, err := e.st.GetRepo(e.ctx, repoID)
	require.NoError(t, err)
	require.NotNil(t, repo.Config)
	assert.Equal(t, v1.ApplyOnMerge, repo.Config.Apply.Mode)
	assert.Equal(t, 2, repo.Config.Apply.RequireApprovals)
	assert.Equal(t, newHeadSHA, repo.ConfigSHA)

	e.gh.SetContents(repoName, mergeSHA, "stackorder.yaml", []byte("version: 7\n"))
	require.NoError(t, e.svc.HandlePush(e.ctx, e.gh.PushEvent(repoName, "refs/heads/main", mergeSHA, "stackorder.yaml")))
	repo, err = e.st.GetRepo(e.ctx, repoID)
	require.NoError(t, err)
	assert.Equal(t, v1.ApplyOnMerge, repo.Config.Apply.Mode, "an invalid file keeps the previous configuration")
	rows := e.audit("config_invalid")
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0].Details["error"], "version")

	require.NoError(t, e.svc.HandlePush(e.ctx, e.gh.PushEvent(repoName, "refs/heads/feature", baseSHA)), "other branches are ignored")

	renamed := e.gh.PushEvent(repoName, "refs/heads/trunk", baseSHA)
	renamed.Repository.DefaultBranch = "trunk"
	require.NoError(t, e.svc.HandlePush(e.ctx, renamed))
	repo, err = e.st.GetRepo(e.ctx, repoID)
	require.NoError(t, err)
	assert.Equal(t, "trunk", repo.DefaultBranch)
	assert.Nil(t, repo.Config, "a default branch without stackorder.yaml has the default configuration")

	const modules = "acme/modules"
	e.gh.SetRepo(modules, gh.Repository{ID: 400, DefaultBranch: "main"})
	e.gh.AddInstallation(instID, "acme", modules)
	_, err = e.st.UpsertRepo(e.ctx, store.RepoParams{ID: 400, InstallationID: instID, FullName: modules, DefaultBranch: "main"})
	require.NoError(t, err)
	g := testGraph(headSHA)
	_, _, err = e.st.SaveGraph(e.ctx, repoID, &g)
	require.NoError(t, err)

	tag := e.gh.PushEvent(modules, "refs/tags/v1.1.0", mainSHA)
	tagged := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tag.HeadCommit.Timestamp = tagged
	require.NoError(t, e.svc.HandlePush(e.ctx, tag))
	require.NoError(t, e.svc.HandlePush(e.ctx, e.gh.PushEvent(modules, "refs/tags/not-a-version", baseSHA)))
	mod, err := e.st.GetModuleByKey(e.ctx, "acme/modules//dns@v1.0.0")
	require.NoError(t, err)
	versions, err := e.st.ListModuleVersions(e.ctx, mod.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Equal(t, "v1.1.0", versions[0].Version)
	assert.Equal(t, mainSHA, versions[0].SHA)
	assert.True(t, tagged.Equal(versions[0].TaggedAt))
}

func TestInstallationEvents(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRepo("beta/infra", gh.Repository{ID: 700, DefaultBranch: "trunk"})
	e.gh.AddInstallation(2, "beta", "beta/infra")
	e.gh.SetContents("beta/infra", "trunk", "stackorder.yaml", []byte("version: 1\ntool: tofu\n"))
	e.gh.SetRef("beta/infra", "heads/trunk", headSHA)

	ev := e.gh.InstallationEvent("created", 2)
	for i := range ev.Repositories {
		ev.Repositories[i].DefaultBranch = ""
	}
	require.NoError(t, e.svc.HandleInstallation(e.ctx, ev))
	require.NoError(t, e.svc.HandleInstallation(e.ctx, ev))
	repo, err := e.st.GetRepo(e.ctx, 700)
	require.NoError(t, err)
	assert.Equal(t, "beta/infra", repo.FullName)
	assert.Equal(t, "trunk", repo.DefaultBranch, "fetched when the payload lacks it")
	require.NotNil(t, repo.Config)
	assert.Equal(t, v1.ToolTofu, repo.Config.Tool)
	assert.Equal(t, headSHA, repo.ConfigSHA)

	suspend := e.gh.InstallationEvent("suspend", 2)
	require.NoError(t, e.svc.HandleInstallation(e.ctx, suspend))
	repo, err = e.st.GetRepo(e.ctx, 700)
	require.NoError(t, err)
	assert.True(t, repo.Suspended)
	require.NoError(t, e.svc.HandleInstallation(e.ctx, e.gh.InstallationEvent("unsuspend", 2)))
	repo, err = e.st.GetRepo(e.ctx, 700)
	require.NoError(t, err)
	assert.False(t, repo.Suspended)

	e.gh.SetRepo("beta/apps", gh.Repository{ID: 701, DefaultBranch: "main"})
	e.gh.AddInstallation(2, "beta", "beta/apps")
	added := &gh.InstallationRepositoriesEvent{
		EventCommon:       gh.EventCommon{Action: "added", Installation: &gh.Installation{ID: 2, Account: gh.User{Login: "beta"}}},
		RepositoriesAdded: []gh.Repository{{ID: 701, FullName: "beta/apps"}},
	}
	require.NoError(t, e.svc.HandleInstallationRepositories(e.ctx, added))
	apps, err := e.st.GetRepo(e.ctx, 701)
	require.NoError(t, err)
	assert.Equal(t, "main", apps.DefaultBranch)
	assert.Nil(t, apps.Config, "a repository without stackorder.yaml gets defaults")

	removed := &gh.InstallationRepositoriesEvent{
		EventCommon:         gh.EventCommon{Action: "removed", Installation: &gh.Installation{ID: 2}},
		RepositoriesRemoved: []gh.Repository{{ID: 701, FullName: "beta/apps"}},
	}
	require.NoError(t, e.svc.HandleInstallationRepositories(e.ctx, removed))
	_, err = e.st.GetRepo(e.ctx, 701)
	require.ErrorIs(t, err, store.ErrNotFound)

	require.NoError(t, e.svc.HandleInstallation(e.ctx, e.gh.InstallationEvent("deleted", 2)))
	_, err = e.st.GetRepo(e.ctx, 700)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = e.st.GetInstallation(e.ctx, 2)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestPrune(t *testing.T) {
	e := newEnv(t, baseConfig())
	runID := e.planned(7, headSHA)
	_, err := e.st.Pool().Exec(e.ctx, `UPDATE run_stacks SET updated_at = now() - interval '40 days' WHERE run_id = $1`, runID)
	require.NoError(t, err)
	require.NoError(t, e.svc.HandleJob(e.ctx, runs.JobPrune, []byte(`{}`)))
	rows, err := e.st.GetRunStacksWithText(e.ctx, uuid.MustParse(runID))
	require.NoError(t, err)
	for _, rs := range rows {
		assert.Empty(t, rs.PlanText, "plan text older than 30 days is pruned")
		assert.NotNil(t, rs.Summary, "summaries are kept")
	}
	require.ErrorIs(t, e.svc.HandleJob(e.ctx, "nope", nil), principal.ErrInvalid)
}
