//go:build integration

package integration

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/testutil/faketf"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

func (f *fixture) waitDrift(key string, since int) ghfake.Dispatch {
	f.t.Helper()
	var found ghfake.Dispatch
	f.e.WaitFor(func() bool {
		n := 0
		for _, d := range f.dispatches("") {
			if d.Inputs["mode"] != string(v1.ModeDrift) || dispatchEntries(f.t, d)[0].Key != key {
				continue
			}
			if n++; n > since {
				found = d
				return true
			}
		}
		return false
	}, waitFor, "a drift dispatch of "+key)
	return found
}

func TestDriftChecks(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "drift")
	ev := f.openPR(50, f.co.head, "feature/vpc-subnet")
	f.plan(ev)
	merged := f.co.merge(f.co.head, "Merge pull request #50 from acme/feature/vpc-subnet")
	f.merge(ev, merged, applier)

	e.runJob(runs.JobScheduleDrift, runs.ScheduleDriftJob{RepoID: f.id})
	rows, err := e.Store.Pool().Query(t.Context(),
		`SELECT id, payload->>'stack_id', run_after FROM jobs WHERE kind = $1 AND (payload->>'repo_id')::bigint = $2 ORDER BY run_after`,
		runs.JobDrift, f.id)
	require.NoError(t, err)
	jobs := map[string]uuid.UUID{}
	var due []time.Time
	for rows.Next() {
		var (
			id    uuid.UUID
			stack string
			at    time.Time
		)
		require.NoError(t, rows.Scan(&id, &stack, &at))
		jobs[stack] = id
		due = append(due, at)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	require.Len(t, jobs, 6, "one drift job per stack of the default-branch graph")
	assert.Less(t, due[len(due)-1].Sub(due[0]), time.Hour, "the checks are staggered across the hour")
	assert.Greater(t, due[len(due)-1].Sub(due[0]), 30*time.Minute)

	vpc := f.stackID(prodVPC)
	_, err = e.Store.Pool().Exec(t.Context(), `UPDATE jobs SET run_after = now() WHERE id = $1`, jobs[vpc])
	require.NoError(t, err)
	e.waitJob(jobs[vpc])
	d := f.waitDrift(prodVPC, 0)
	entry := dispatchEntries(t, d)[0]
	assert.Equal(t, v1.DefaultEnvironment, entry.Environment, "drift jobs run under the default environment")
	assert.Equal(t, merged, d.Inputs["sha"], "drift checks the default branch head")

	f.setStack(prodVPC, func(b *faketf.Behavior) { b.PlanExit, b.ShowJSON = 2, fixturePath(t, "drift.json") })
	res := f.dispatchWorkflow(d, v1.DefaultEnvironment).run("drift", "--stack", prodVPC, "--run-id", d.Inputs["run_id"])
	requireExit(t, 2, res)
	assert.Equal(t, "true", res.outputs["drifted"])

	run := e.run(d.Inputs["run_id"])
	assert.Equal(t, v1.ModeDrift, run.Mode)
	assert.Equal(t, v1.TriggerSchedule, run.Trigger)
	assert.Equal(t, v1.RunPlanned, run.Status)
	detail := f.stackDetail(prodVPC)
	require.NotNil(t, detail.Drift)
	assert.True(t, detail.Drift.Drifted)
	issues := e.GH.Issues(f.name)
	require.Len(t, issues, 1)
	issue := issues[0]
	assert.Equal(t, report.DriftIssueTitle(prodVPC), issue.Title)
	assert.Equal(t, "Drift detected in stacks/prod/vpc", issue.Title, "the title is stable so later checks find the issue")
	assert.Equal(t, gh.IssueOpen, issue.State)
	assert.Contains(t, issue.Labels, "stackorder-drift")
	assert.Equal(t, issue.Number, detail.Drift.IssueNumber)

	_, err = e.Store.Pool().Exec(t.Context(), `UPDATE runs SET created_at = created_at - interval '2 hours' WHERE id = $1`, d.Inputs["run_id"])
	require.NoError(t, err)
	e.runJob(runs.JobDrift, runs.DriftJob{RepoID: f.id, StackID: vpc})
	later := f.waitDrift(prodVPC, 1)
	require.NotEqual(t, d.Inputs["run_id"], later.Inputs["run_id"], "a later check runs as a new drift run")
	f.setStack(prodVPC, func(b *faketf.Behavior) { b.PlanExit, b.ShowJSON = 0, fixturePath(t, "noop.json") })
	res = f.dispatchWorkflow(later, v1.DefaultEnvironment).run("drift", "--stack", prodVPC, "--run-id", later.Inputs["run_id"])
	requireExit(t, 0, res)
	assert.Equal(t, "false", res.outputs["drifted"])

	issues = e.GH.Issues(f.name)
	require.Len(t, issues, 1, "the clean check reuses the issue")
	assert.Equal(t, gh.IssueClosed, issues[0].State, "a clean check closes the drift issue")
	comments := e.GH.Comments(f.name, issue.Number)
	require.NotEmpty(t, comments)
	assert.Contains(t, comments[len(comments)-1].Body, "The drift check found no drift")
	detail = f.stackDetail(prodVPC)
	require.NotNil(t, detail.Drift)
	assert.False(t, detail.Drift.Drifted)
}

func TestForkPullRequest(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "fork")
	mergeable := true
	fork := &gh.Repository{ID: 990001, Name: "infra", FullName: "evil/infra", Owner: gh.User{Login: "evil"}, Fork: true}
	ev := e.GH.PullRequestEvent("opened", f.name, gh.PullRequest{
		Number: 60, Title: "Widen the VPC", User: gh.User{Login: "evil"},
		HeadSHA: f.co.head, HeadRef: "patch-1", BaseSHA: f.co.base, BaseRef: "main",
		Head: gh.PullBranch{Repo: fork}, Mergeable: &mergeable,
	})
	f.deliver(gh.EventPullRequest, ev)
	f.deliver(gh.EventPullRequest, f.e.GH.PullRequestEvent("synchronize", f.name, ev.PullRequest))

	checks := f.e.GH.CheckRuns(f.name)
	require.Len(t, checks, 1, "a fork gets a single check, however often it is delivered")
	c := checks[0]
	assert.Equal(t, report.CheckPlan, c.Name)
	assert.Equal(t, f.co.head, c.HeadSHA)
	assert.Equal(t, gh.CheckRunCompleted, c.Status)
	assert.Equal(t, report.ConclusionNeutral, c.Conclusion)
	title, _ := report.ForkNotice()
	assert.Equal(t, title, c.Output.Title)
	assert.Empty(t, e.GH.Comments(f.name, 60), "nothing is commented")
	assert.Empty(t, f.dispatches(""), "nothing is dispatched")
	var n int
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE repo_id = $1`, f.id).Scan(&n))
	assert.Zero(t, n, "no run is created")
}

func deadURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}

func TestServerUnreachable(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "unreachable")
	ev := f.openPR(70, f.co.head, "feature/vpc-subnet")
	want := expectedResolution(t, f, f.co.head)
	w := f.planWorkflow(ev)
	w.server = deadURL(t)

	res := w.run("resolve")
	requireExit(t, 0, res)
	assert.Equal(t, "true", res.outputs["unconfirmed"])
	assert.Empty(t, res.outputs["run-id"])
	assert.Equal(t, "5", res.outputs["count"])
	var waves [][]string
	requireJSON(t, res.outputs["waves"], &waves)
	assert.Equal(t, want.Waves, waves, "the affected set is computed on the runner")
	assert.Contains(t, res.stderr, "::warning::the resolution is unconfirmed: the server is unreachable")
	resolve := f.check(f.co.head, report.CheckResolve)
	assert.Equal(t, report.ConclusionNeutral, resolve.Conclusion)
	assert.Equal(t, "Unconfirmed: 5 stacks affected", resolve.Output.Title)

	var matrix v1.Matrix
	requireJSON(t, res.outputs["matrix"], &matrix)
	require.Len(t, matrix.Include, 5)
	for _, key := range []string{prodVPC, prodEKS} {
		plan := w.run("plan", "--stack", key, "--run-id", res.outputs["run-id"])
		requireExit(t, 0, plan)
		assert.Equal(t, "true", plan.outputs["unconfirmed"], key)
		c := f.check(f.co.head, report.StackCheckName(report.CheckPlan, key))
		assert.Equal(t, report.ConclusionNeutral, c.Conclusion, key)
		assert.Contains(t, c.Output.Title, "Unconfirmed: ", key)
	}
	for _, c := range e.GH.CheckRuns(f.name) {
		assert.Equal(t, report.ConclusionNeutral, c.Conclusion, "%s: nobody mistakes a fallback for a green light", c.Name)
	}

	apply := f.dispatchWorkflow(ghfake.Dispatch{Workflow: "stackorder-run.yml", RunID: 4242, Inputs: map[string]string{
		"run_id": uuid.NewString(), "mode": "apply", "wave": "0", "sha": f.co.head, "stacks": "[]",
	}}, "production")
	apply.server = w.server
	applied := apply.run("apply", "--stack", prodVPC)
	requireExit(t, 3, applied)
	assert.Contains(t, applied.stderr, "fail closed")
	var n int
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE repo_id = $1`, f.id).Scan(&n))
	assert.Zero(t, n, "the server heard nothing")
}

func TestManualApply(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "manual")
	f.plan(f.openPR(80, f.co.head, "feature/vpc-subnet"))
	gate := filepath.Join(t.TempDir(), "release")
	f.setStack(prodVPC, func(b *faketf.Behavior) { b.ApplyWaitFile = gate })
	f.co.at(f.co.head)

	outputs, summary := f.prepare(map[string]string{"STACKORDER_SERVER_URL": e.BaseURL, "STACKORDER_API_KEY": suite.apiKey})
	done := make(chan jobResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- f.cli(outputs, summary, "apply", "--local", "--stack", prodVPC)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(gate, nil, 0o600)
		<-finished
	})
	e.WaitFor(func() bool {
		_, ok := f.locks()[prodVPC]
		return ok
	}, waitFor, "the manual apply takes the lock")
	lock := f.locks()[prodVPC]
	assert.Zero(t, lock.PRNumber)
	assert.Equal(t, "manual apply by apikey:integration", lock.Reason)

	second := f.cli(outputs, summary, "apply", "--local", "--stack", prodVPC)
	requireExit(t, 3, second)
	assert.Contains(t, second.stderr, "locked")

	require.NoError(t, os.WriteFile(gate, nil, 0o600))
	var first jobResult
	select {
	case first = <-done:
	case <-time.After(jobTimeout):
		t.Fatal("the first manual apply did not finish")
	}
	requireExit(t, 0, first)
	assert.Empty(t, f.locks(), "the manual run releases its lock when its result arrives")

	var page v1.Page[v1.Run]
	e.getJSON("/v1/repos/"+f.name+"/runs?mode=apply", &page)
	require.Len(t, page.Items, 1, "the refused apply started no run")
	run := e.run(page.Items[0].ID)
	assert.Equal(t, v1.TriggerManual, run.Trigger)
	assert.Equal(t, v1.RunApplied, run.Status)
	assert.Equal(t, "apikey:integration", run.RequestedBy)
	assert.Equal(t, f.co.head, run.SHA)
	assert.Equal(t, v1.StackApplied, runStacks(run)[prodVPC].Status)
	manual := f.audit("manual_apply")
	require.Len(t, manual, 1)
	assert.Equal(t, "run:"+run.ID, manual[0].Target)
	assert.Equal(t, "apikey:integration", manual[0].Actor)
	unlocks := f.audit("unlock")
	require.Len(t, unlocks, 1)
	assert.Equal(t, "run applied", unlocks[0].Details["reason"])
}
