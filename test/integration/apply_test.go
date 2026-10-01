//go:build integration

package integration

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/testutil/faketf"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

func (f *fixture) eksChanges() {
	f.setStack(prodEKS, func(b *faketf.Behavior) {
		b.PlanExit, b.ShowJSON = 2, fixturePath(f.t, "eks.json")
	})
}

type applying struct {
	ev    *gh.PullRequestEvent
	plan  *planned
	cmd   gh.Comment
	runID string
	wave0 []ghfake.Dispatch
}

func (f *fixture) startApply(pr int, command string) *applying {
	f.t.Helper()
	ev := f.openPR(pr, f.co.head, "feature/vpc-subnet")
	p := f.plan(ev)
	f.approve(pr, reviewer, f.co.head)
	cmd := f.comment(pr, applier, command)
	require.Equal(f.t, []string{gh.ReactionEyes, gh.ReactionRocket}, f.e.GH.Reactions(cmd.ID), "the command is seen and dispatched")
	all := f.dispatches("")
	require.NotEmpty(f.t, all, "the apply is dispatched")
	a := &applying{ev: ev, plan: p, cmd: cmd, runID: all[0].Inputs["run_id"]}
	a.wave0 = f.waitDispatches(a.runID, 0, len(all))
	return a
}

func planCalls(t *testing.T, f *fixture, key string) int {
	t.Helper()
	n := 0
	for _, c := range faketf.Calls(t, f.tf.Log) {
		if strings.HasSuffix(c.Dir, "/"+key) && c.Args[0] == "plan" {
			n++
		}
	}
	return n
}

func TestApplyAcrossWaves(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "apply-waves")
	f.eksChanges()
	a := f.startApply(10, applyCommand)
	head := f.co.head

	require.Len(t, a.wave0, 2, "wave 0 is dispatched once per environment")
	for i, env := range []string{"production", "staging"} {
		d := a.wave0[i]
		assert.Equal(t, "stackorder-run.yml", d.Workflow)
		assert.Equal(t, "main", d.Ref)
		assert.Equal(t, string(v1.ModeApply), d.Inputs["mode"])
		assert.Equal(t, "0", d.Inputs["wave"])
		assert.Equal(t, head, d.Inputs["sha"])
		entries := dispatchEntries(t, d)
		require.Len(t, entries, 1, env)
		entry := entries[0]
		assert.Equal(t, map[string]string{"production": prodVPC, "staging": stagingVPC}[env], entry.Key)
		assert.Equal(t, env, entry.Environment)
		assert.Equal(t, head, entry.SHA)
		assert.Equal(t, a.plan.w.runID(), entry.PlanRunID, "the entry names the workflow run that uploaded the plan")
		assert.Equal(t, v1.PlanArtifactName(entry.Key, head), entry.Artifact)
	}
	locks := f.locks()
	assert.ElementsMatch(t, []string{prodVPC, stagingVPC, prodEKS, prodApps, stagingApps}, keysOf(locks))
	for key, l := range locks {
		assert.Equal(t, a.runID, l.RunID.String(), key)
		assert.Equal(t, 10, l.PRNumber, key)
	}
	run := e.run(a.runID)
	assert.Equal(t, v1.RunApplying, run.Status)
	assert.Equal(t, v1.TriggerComment, run.Trigger)
	assert.Equal(t, applier, run.RequestedBy)
	stacks := runStacks(run)
	assert.Equal(t, v1.StackNoop, stacks[stagingApps].Status, "a propagated stack without changes is a no-op")
	assert.Equal(t, v1.StackNoop, stacks[prodApps].Status)
	assert.Equal(t, v1.StackPlanned, stacks[prodEKS].Status, "a propagated stack with changes is applied")

	require.NoError(t, os.Remove(f.planFile(v1.PlanArtifactName(stagingVPC, head))), "the artifact of staging/vpc expired")
	for _, d := range a.wave0 {
		for key, res := range f.apply(d, nil) {
			requireExit(t, 0, res)
			assert.Contains(t, res.outputs["summary"], `"adds":1`, key)
		}
		f.complete(d, "success")
	}
	assert.Equal(t, 1, planCalls(t, f, prodVPC), "stacks/prod/vpc applied its saved plan")
	assert.Equal(t, 2, planCalls(t, f, stagingVPC), "stacks/staging/vpc planned again without its plan file")

	wave1 := f.waitDispatches(a.runID, 1, 1)
	entries := dispatchEntries(t, wave1[0])
	require.Len(t, entries, 1, "no-op stacks are not dispatched")
	assert.Equal(t, prodEKS, entries[0].Key)
	assert.Equal(t, "production", entries[0].Environment)
	assert.Equal(t, a.plan.w.runID(), entries[0].PlanRunID)
	requireExit(t, 0, f.apply(wave1[0], nil)[prodEKS])
	f.complete(wave1[0], "success")

	run = e.waitRun(a.runID, v1.RunApplied)
	for key, rs := range runStacks(run) {
		want := v1.StackApplied
		if key == prodApps || key == stagingApps {
			want = v1.StackNoop
		}
		assert.Equal(t, want, rs.Status, key)
	}
	assert.Len(t, f.dispatches(a.runID), 3)
	rollup := f.check(head, report.CheckApply)
	assert.Equal(t, report.ConclusionSuccess, rollup.Conclusion)
	assert.Equal(t, "Applied: 1 added, 0 changed, 0 destroyed", f.check(head, report.StackCheckName(report.CheckApply, prodVPC)).Output.Title)
	assert.Equal(t, "Applied: 0 added, 1 changed, 0 destroyed", f.check(head, report.StackCheckName(report.CheckApply, prodEKS)).Output.Title)
	assert.Equal(t, "No changes, nothing to apply", f.check(head, report.StackCheckName(report.CheckApply, prodApps)).Output.Title)
	applyComment := f.runComment(10, a.runID)
	assert.Contains(t, applyComment.Body, "### Stackorder apply: applied")
	assert.Contains(t, applyComment.Body, "| 0 | `stacks/prod/vpc` | production | 1 | 0 | 0 | 0 | applied |")
	assert.Contains(t, applyComment.Body, "| 1 | `stacks/prod/eks` | production | 0 | 1 | 0 | 0 | applied |")
	assert.Contains(t, applyComment.Body, "| 1 | `stacks/staging/apps` | staging | 0 | 0 | 0 | 0 | no-op |")
	assert.Len(t, f.locks(), 5, "before_merge keeps the locks until the pull request merges")

	merged := f.co.merge(head, "Merge pull request #10 from acme/feature/vpc-subnet")
	f.merge(a.ev, merged, applier)
	assert.Empty(t, f.locks(), "the merge releases the locks")
	replies := f.botComments(10, a.cmd.ID)
	require.NotEmpty(t, replies)
	assert.Contains(t, replies[len(replies)-1].Body, "Released 5 orchestration locks at the request of @"+applier)
	var view v1.GraphView
	e.getJSON("/v1/repos/"+f.name+"/graph", &view)
	assert.Equal(t, head, view.SHA, "the merged pull request's graph is the default-branch graph")
	assert.ElementsMatch(t, exampleStackKeys, keysOf(view.StackIDs))
	unlocks := f.audit("unlock")
	assert.Len(t, unlocks, 5)
	for _, u := range unlocks {
		assert.Equal(t, "merge", u.Details["via"])
	}
}

func TestApplyFailureBlocksDependents(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "apply-failure")
	f.eksChanges()
	f.setStack(prodVPC, func(b *faketf.Behavior) { b.ApplyExit = 1 })
	a := f.startApply(11, applyCommand)
	head := f.co.head
	require.Len(t, a.wave0, 2)

	prod := f.apply(a.wave0[0], nil)[prodVPC]
	requireExit(t, 1, prod)
	assert.Contains(t, prod.stderr, "apply of stacks/prod/vpc failed")
	requireExit(t, 0, f.apply(a.wave0[1], nil)[stagingVPC])

	run := e.waitRun(a.runID, v1.RunFailed)
	stacks := runStacks(run)
	assert.Equal(t, v1.StackFailed, stacks[prodVPC].Status)
	require.NotNil(t, stacks[prodVPC].ExitCode)
	assert.Equal(t, 1, *stacks[prodVPC].ExitCode)
	assert.Equal(t, v1.StackApplied, stacks[stagingVPC].Status, "unrelated stacks of the wave finish")
	assert.Equal(t, v1.StackBlocked, stacks[prodEKS].Status)
	assert.Equal(t, []string{prodVPC}, stacks[prodEKS].BlockedBy)
	assert.Equal(t, v1.StackNoop, stacks[prodApps].Status)
	assert.Len(t, f.dispatches(a.runID), 2, "later waves are not dispatched")
	assert.Len(t, f.locks(), 5, "a failed before_merge apply keeps its locks")

	assert.Equal(t, report.ConclusionFailure, f.check(head, report.CheckApply).Conclusion)
	assert.Equal(t, "1 failed, 1 blocked", f.check(head, report.CheckApply).Output.Title)
	assert.Equal(t, "Blocked by stacks/prod/vpc", f.check(head, report.StackCheckName(report.CheckApply, prodEKS)).Output.Title)
	assert.Equal(t, "Apply failed (exit code 1)", f.check(head, report.StackCheckName(report.CheckApply, prodVPC)).Output.Title)
	replies := f.botComments(11, a.cmd.ID)
	require.Len(t, replies, 1)
	failure := replies[0].Body
	assert.Contains(t, failure, "**Apply failed** in wave 0 of `"+head[:7]+"`: `stacks/prod/vpc` (failed). Blocked dependents: `stacks/prod/eks`. Later waves were not dispatched.")
	assert.Contains(t, failure, "The orchestration locks stay held")
	assert.Contains(t, f.runComment(11, a.runID).Body, "### Stackorder apply: failed")

	cmd := f.comment(11, applier, "stackorder unlock stacks/prod/vpc stacks/prod/eks")
	replies = f.botComments(11, cmd.ID)
	require.Len(t, replies, 1)
	assert.Contains(t, replies[0].Body, "Released 2 orchestration locks at the request of @"+applier)
	assert.ElementsMatch(t, []string{stagingVPC, prodApps, stagingApps}, keysOf(f.locks()))

	status, body := e.call(http.MethodPost, "/v1/unlock", v1.UnlockRequest{Repo: f.name, StackKey: stagingVPC, Reason: "applied by hand"}, withAPIKey)
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, `"stack_key":"stacks/staging/vpc"`)
	assert.ElementsMatch(t, []string{prodApps, stagingApps}, keysOf(f.locks()))
	var viaAPI []v1.AuditEntry
	for _, u := range f.audit("unlock") {
		if u.Details["stack"] == stagingVPC {
			viaAPI = append(viaAPI, u)
		}
		if u.Details["via"] == "comment" {
			assert.Equal(t, applier, u.Actor)
		}
	}
	require.Len(t, viaAPI, 1, "the API unlock is audited")
	assert.Equal(t, "apikey:integration", viaAPI[0].Actor)
	assert.Equal(t, "applied by hand", viaAPI[0].Details["reason"])
	assert.Len(t, f.audit("unlock"), 3)
}

func (f *fixture) duplicateOf(d ghfake.Dispatch) ghfake.Dispatch {
	f.t.Helper()
	var title string
	for _, r := range f.e.GH.WorkflowRuns(f.name) {
		if r.ID == d.RunID {
			title = r.DisplayTitle
		}
	}
	require.NotEmpty(f.t, title)
	run := f.e.GH.AddWorkflowRun(f.name, gh.WorkflowRun{
		Name: "stackorder run", DisplayTitle: title, Path: ".github/workflows/" + d.Workflow,
		Event: "workflow_dispatch", HeadBranch: "main", HeadSHA: f.main, Status: gh.RunStatusInProgress,
	})
	dup := d
	dup.RunID = run.ID
	return dup
}

func TestDuplicateDispatchIsRefused(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "duplicate-dispatch")
	a := f.startApply(12, applyCommand)
	prod := a.wave0[0]
	requireExit(t, 0, f.apply(prod, nil)[prodVPC])

	dup := f.duplicateOf(prod)
	f.deliver(gh.EventWorkflowRun, e.GH.WorkflowRunEvent("requested", f.name, gh.WorkflowRun{ID: dup.RunID}))
	var bound int64
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(),
		`SELECT workflow_run_id FROM dispatches WHERE run_id = $1 AND environment = 'production'`, a.runID).Scan(&bound))
	assert.Equal(t, prod.RunID, bound, "the duplicate workflow run binds nothing")

	w := f.dispatchWorkflow(dup, "production")
	res := w.run("apply", "--stack", prodVPC, "--run-id", a.runID, "--plan-file", f.planFile(v1.PlanArtifactName(prodVPC, f.co.head)))
	requireExit(t, 3, res)
	assert.Contains(t, res.stderr, "403")

	claims := w.claims
	claims.Audience = jwt.ClaimStrings{e.BaseURL}
	syncIssuer(e)
	token := e.OIDC.Token(claims)
	status, body := e.call(http.MethodPost, "/v1/runs/"+a.runID+"/stacks/"+strings.ReplaceAll(prodVPC, "/", "%2F")+"/result",
		v1.StackResult{Mode: v1.ModeApply, Status: v1.ResultSuccess}, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
	assert.Equal(t, http.StatusForbidden, status, body)
	assert.Contains(t, body, `"code":"forbidden"`)

	requireExit(t, 0, f.apply(a.wave0[1], nil)[stagingVPC])
	run := e.waitRun(a.runID, v1.RunApplied)
	assert.Equal(t, v1.StackApplied, runStacks(run)[prodVPC].Status, "the first result stands")
}

func TestWorkflowRunWithoutResults(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "lost-results")
	a := f.startApply(13, applyCommand+" "+prodVPC)
	require.Len(t, a.wave0, 1, "the subset has one stack in one environment")
	d := a.wave0[0]
	run := e.run(a.runID)
	assert.Equal(t, v1.StackSkipped, runStacks(run)[stagingVPC].Status, "stacks outside the subset are skipped")

	f.complete(d, "failure")
	run = e.waitRun(a.runID, v1.RunFailed)
	assert.Equal(t, v1.StackUnknown, runStacks(run)[prodVPC].Status, "a workflow run that ends without a result leaves its stack unknown")
	var bound int64
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(), `SELECT workflow_run_id FROM dispatches WHERE run_id = $1`, a.runID).Scan(&bound))
	assert.Equal(t, d.RunID, bound, "the workflow_run event bound the dispatch by its display title")
	assert.Equal(t, "Result unknown: the job ended without reporting", f.check(f.co.head, report.StackCheckName(report.CheckApply, prodVPC)).Output.Title)

	cmd := f.comment(13, applier, applyCommand+" "+stagingVPC)
	require.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, e.GH.Reactions(cmd.ID))
	var next ghfake.Dispatch
	for _, x := range f.dispatches("") {
		if x.Inputs["run_id"] != a.runID {
			next = x
		}
	}
	require.NotZero(t, next.RunID)
	e.GH.SetWorkflowRunStatus(f.name, next.RunID, gh.RunStatusInProgress)
	_, err := e.Store.Pool().Exec(t.Context(),
		`UPDATE dispatches SET dispatched_at = dispatched_at - interval '2 minutes' WHERE run_id = $1`, next.Inputs["run_id"])
	require.NoError(t, err)
	e.runJob(runs.JobReconcile, runs.ReconcileJob{})
	var reconciled *int64
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(), `SELECT workflow_run_id FROM dispatches WHERE run_id = $1`, next.Inputs["run_id"]).Scan(&reconciled))
	require.NotNil(t, reconciled, "Reconcile binds a dispatch whose webhook was lost")
	assert.Equal(t, next.RunID, *reconciled)

	requireExit(t, 0, f.apply(next, nil)[stagingVPC])
	f.complete(next, "success")
	run = e.waitRun(next.Inputs["run_id"], v1.RunApplied)
	assert.Equal(t, v1.StackApplied, runStacks(run)[stagingVPC].Status)
}

func TestApplyOnMerge(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "on-merge", withConfig(func(s string) string {
		return strings.Replace(s, "mode: before_merge", "mode: on_merge", 1)
	}))
	ev := f.openPR(14, f.co.head, "feature/vpc-subnet")
	p := f.plan(ev)
	head := f.co.head
	assert.NotContains(t, f.checks(head), report.CheckApply, "on_merge repositories get no apply check before the merge")

	cmd := f.comment(14, applier, applyCommand)
	replies := f.botComments(14, cmd.ID)
	require.Len(t, replies, 1)
	assert.Contains(t, replies[0].Body, "applies on merge (`apply.mode: on_merge`)")
	assert.Empty(t, f.dispatches(""))

	merged := f.co.merge(head, "Merge pull request #14 from acme/feature/vpc-subnet")
	f.merge(ev, merged, applier)
	all := f.dispatches("")
	require.Len(t, all, 2, "the merge dispatches wave 0 once per environment")
	runID := all[0].Inputs["run_id"]
	wave0 := f.waitDispatches(runID, 0, 2)
	for _, d := range wave0 {
		assert.Equal(t, merged, d.Inputs["sha"], "the merge commit is applied")
		for _, entry := range dispatchEntries(t, d) {
			assert.Equal(t, merged, entry.SHA)
			assert.Equal(t, p.w.runID(), entry.PlanRunID)
			assert.Equal(t, v1.PlanArtifactName(entry.Key, head), entry.Artifact, "with the plans of the head commit")
		}
	}
	run := e.run(runID)
	assert.Equal(t, v1.ModeApply, run.Mode)
	assert.Equal(t, v1.TriggerPullRequest, run.Trigger)
	assert.Equal(t, merged, run.SHA)
	assert.Equal(t, applier, run.RequestedBy)
	assert.Len(t, f.locks(), 5)

	for _, d := range wave0 {
		for _, res := range f.apply(d, nil) {
			requireExit(t, 0, res)
		}
		f.complete(d, "success")
	}
	e.waitRun(runID, v1.RunApplied)
	assert.Empty(t, f.locks(), "an on_merge run releases its locks when it completes")
	assert.Len(t, f.dispatches(runID), 2, "wave 1 holds only no-op stacks")
	assert.Equal(t, report.ConclusionSuccess, f.check(merged, report.CheckApply).Conclusion)
}
