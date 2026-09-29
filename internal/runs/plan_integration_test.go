//go:build integration

package runs_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
)

func TestPlanFlow(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	job := e.planJob(7)

	created, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, BaseSHA: baseSHA, PRNumber: 7, Mode: v1.ModePlan})
	require.NoError(t, err)
	assert.False(t, created.Existing)
	assert.Equal(t, v1.RunPending, created.Status)
	assert.Equal(t, job.p.Claims.Actor, created.Run.RequestedBy)
	assert.Equal(t, v1.TriggerPullRequest, created.Run.Trigger)
	assert.Equal(t, baseSHA, created.Run.BaseSHA)

	again, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, PRNumber: 7, Mode: v1.ModePlan})
	require.NoError(t, err)
	assert.True(t, again.Existing, "the same workflow run registers once")
	assert.Equal(t, created.RunID, again.RunID)

	resp, err := e.svc.UploadGraph(e.ctx, job.p, created.RunID, v1.GraphUploadRequest{
		Graph: testGraph(headSHA), ChangedPaths: []string{"modules/vpc/main.tf", "README.md"},
	})
	require.NoError(t, err)
	assert.False(t, resp.Cached)
	assert.Equal(t, created.RunID, resp.RunID)
	assert.Equal(t, [][]string{{vpc, staging}, {eks}, {apps}}, resp.Waves)
	require.Len(t, resp.Matrix.Include, 4)
	for _, m := range resp.Matrix.Include {
		assert.Equal(t, headSHA, m.SHA, "the matrix checks out the run's commit")
	}
	envs := map[string]string{}
	for _, a := range resp.Affected {
		envs[a.Key] = a.Environment
	}
	assert.Equal(t, map[string]string{vpc: "production", eks: "production", apps: "production", staging: "staging"}, envs)

	run := e.run(created.RunID)
	assert.Equal(t, v1.RunPlanning, run.Status)
	assert.Equal(t, 3, run.Waves)
	resolve := e.check(report.CheckResolve)
	assert.Equal(t, gh.ConclusionSuccess, resolve.Conclusion)
	assert.Equal(t, "4 stacks affected in 3 waves", resolve.Output.Title)
	assert.Equal(t, headSHA, resolve.HeadSHA)
	rollup := e.check(report.CheckPlan)
	assert.Equal(t, gh.CheckRunInProgress, rollup.Status)
	for _, key := range []string{vpc, staging, eks, apps} {
		c := e.check(report.StackCheckName(report.CheckPlan, key))
		assert.Equal(t, gh.CheckRunQueued, c.Status, key)
		assert.Equal(t, "https://stackorder.test/runs/"+created.RunID, c.DetailsURL)
	}
	assert.Contains(t, e.sticky(7), "Planning: 0 of 4 stacks done")

	for i, key := range []string{vpc, staging, eks} {
		row, err := e.svc.RecordResult(e.ctx, job.p, created.RunID, key, planResult(key, headSHA, i+1))
		require.NoError(t, err)
		assert.Equal(t, v1.StackPlanned, row.Status)
		assert.Equal(t, v1.PlanArtifactName(key, headSHA), row.PlanArtifact)
	}
	assert.Equal(t, v1.RunPlanning, e.run(created.RunID).Status)
	last, err := e.svc.RecordResult(e.ctx, job.p, created.RunID, apps, planResult(apps, headSHA, 0))
	require.NoError(t, err)
	require.NotNil(t, last.ExitCode)
	require.NotNil(t, last.StartedAt, "the reported duration backdates the start")

	run = e.run(created.RunID)
	assert.Equal(t, v1.RunPlanned, run.Status)
	for _, rs := range run.Stacks {
		assert.Equal(t, v1.StackPlanned, rs.Status)
	}
	rollup = e.check(report.CheckPlan)
	assert.Equal(t, gh.ConclusionSuccess, rollup.Conclusion)
	assert.Equal(t, "4 stacks: 6 to add, 0 to change, 0 to destroy", rollup.Output.Title)
	vpcCheck := e.check(report.StackCheckName(report.CheckPlan, vpc))
	assert.Equal(t, gh.ConclusionSuccess, vpcCheck.Conclusion)
	assert.Equal(t, "1 to add, 0 to change, 0 to destroy", vpcCheck.Output.Title)
	assert.Contains(t, vpcCheck.Output.Text, "# plan of "+vpc)
	assert.Len(t, e.checksNamed(report.CheckPlan), 1, "the roll-up is updated, never duplicated")
	assert.Empty(t, e.checksNamed(report.CheckApply), "only an apply turns stackorder/apply green when stacks are affected")

	sticky := e.sticky(7)
	assert.Contains(t, sticky, "### Stackorder: planned")
	for _, key := range []string{vpc, staging, eks, apps} {
		assert.Contains(t, sticky, "`"+key+"`")
	}
	assert.Contains(t, sticky, "# plan of "+eks)
	assert.Len(t, e.comments(7), 1, "one sticky comment")

	dup, err := e.svc.RecordResult(e.ctx, job.p, created.RunID, vpc, planResult(vpc, headSHA, 1))
	require.NoError(t, err, "a duplicate result is accepted")
	assert.Equal(t, v1.StackPlanned, dup.Status)
	_, err = e.svc.RecordResult(e.ctx, job.p, created.RunID, vpc, planResult(vpc, headSHA, 9))
	require.ErrorIs(t, err, principal.ErrConflict, "a different result for a finished stack conflicts")

	other := e.planJob(8)
	_, err = e.svc.RecordResult(e.ctx, other.p, created.RunID, vpc, planResult(vpc, headSHA, 1))
	require.ErrorIs(t, err, principal.ErrForbidden, "a job of another pull request is refused")
	_, err = e.svc.RecordResult(e.ctx, job.p, created.RunID, vpc, v1.StackResult{Mode: v1.ModeApply, Status: v1.ResultSuccess})
	require.ErrorIs(t, err, principal.ErrInvalid)
	_, err = e.svc.RecordResult(e.ctx, job.p, created.RunID, "stacks/none", planResult("stacks/none", headSHA, 1))
	require.ErrorIs(t, err, principal.ErrNotFound)
}

func TestFinishedPlanRunsRefusePullRequestJobs(t *testing.T) {
	e := newEnv(t, baseConfig())
	runID := e.planned(7, headSHA)
	require.Equal(t, v1.RunPlanned, e.run(runID).Status)
	job := e.planJob(7)

	row, err := e.svc.RecordResult(e.ctx, job.p, runID, vpc, planResult(vpc, headSHA, 1))
	require.NoError(t, err, "a retried post of the recorded result is still answered")
	assert.Equal(t, v1.StackPlanned, row.Status)
	late := planResult(vpc, headSHA, 1)
	late.Status, late.ExitCode, late.ErrorText = v1.ResultFailure, 1, "Error: boom"
	_, err = e.svc.RecordResult(e.ctx, job.p, runID, vpc, late)
	require.ErrorIs(t, err, principal.ErrConflict, "a late result cannot change a planned run")
	_, err = e.svc.RecordResult(e.ctx, job.p, runID, vpc, planResult(vpc, headSHA, 5))
	require.ErrorIs(t, err, principal.ErrConflict)
	_, err = e.svc.RecordCheck(e.ctx, job.p, runID, vpc, "policy", v1.CheckVerdict{Status: v1.CheckFail, Summary: "late"})
	require.ErrorIs(t, err, principal.ErrConflict, "nor can a late check verdict")
	_, err = e.svc.GetRunForPrincipal(e.ctx, job.p, runID)
	require.ErrorIs(t, err, principal.ErrConflict, "a pull_request token of a finished run is refused")
	run := e.run(runID)
	assert.Equal(t, v1.RunPlanned, run.Status)
	assert.Equal(t, v1.StackPlanned, stackStatuses(run)[vpc])
	for _, rs := range run.Stacks {
		assert.Empty(t, rs.Checks, rs.Key)
	}
	_, err = e.svc.GetRunForPrincipal(e.ctx, apiKey(), runID)
	require.NoError(t, err, "automation still reads the run")

	e.openPull(8, newHeadSHA)
	job8, failedRun, resp := e.startPlan(8, newHeadSHA)
	broken := resp.Affected[0].Key
	for _, a := range resp.Affected {
		res := planResult(a.Key, newHeadSHA, 1)
		if a.Key == broken {
			res.Status, res.ExitCode, res.ErrorText = v1.ResultFailure, 1, "Error: boom"
		}
		_, err := e.svc.RecordResult(e.ctx, job8.p, failedRun, a.Key, res)
		require.NoError(t, err)
	}
	require.Equal(t, v1.RunFailed, e.run(failedRun).Status)
	_, err = e.svc.RecordResult(e.ctx, job8.p, failedRun, broken, planResult(broken, newHeadSHA, 1))
	require.ErrorIs(t, err, principal.ErrConflict, "a re-run job cannot turn a failed plan run green")
	assert.Equal(t, v1.RunFailed, e.run(failedRun).Status)
	assert.Equal(t, v1.StackFailed, stackStatuses(e.run(failedRun))[broken])

	e.openPull(7, mergeSHA)
	e.startPlan(7, mergeSHA)
	require.Equal(t, v1.RunSuperseded, e.run(runID).Status)
	_, err = e.svc.RecordResult(e.ctx, job.p, runID, vpc, planResult(vpc, headSHA, 1))
	require.ErrorIs(t, err, principal.ErrSuperseded)
	_, err = e.svc.GetRunForPrincipal(e.ctx, job.p, runID)
	require.ErrorIs(t, err, principal.ErrSuperseded)
}

func TestPolicyCheckNamesLeaveServerChecksAlone(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	job, runID, _ := e.startPlan(7, headSHA)
	before := e.check(report.StackCheckName(report.CheckPlan, vpc)).Output
	for _, name := range []string{"plan", "Apply", "resolve"} {
		_, err := e.svc.RecordCheck(e.ctx, job.p, runID, vpc, name, v1.CheckVerdict{Status: v1.CheckFail, Summary: "overridden"})
		require.ErrorIs(t, err, principal.ErrInvalid, name)
	}
	assert.Equal(t, before, e.check(report.StackCheckName(report.CheckPlan, vpc)).Output)
	for _, rs := range e.run(runID).Stacks {
		assert.Empty(t, rs.Checks, rs.Key)
	}

	_, err := e.svc.RecordCheck(e.ctx, job.p, runID, vpc, "plan-cost", v1.CheckVerdict{Status: v1.CheckPass})
	require.NoError(t, err, "a name that only starts like a server check is a policy check")
	assert.Equal(t, gh.ConclusionSuccess, e.check(report.PolicyCheckName("plan-cost", vpc)).Conclusion)
}

func TestPlanOutputSummaryComesFromTheDefaultBranch(t *testing.T) {
	e := newEnv(t, baseConfig())
	onMain := testGraph(mainSHA)
	for i := range onMain.Stacks {
		if onMain.Stacks[i].Key == vpc {
			onMain.Stacks[i].Config = &v1.StackConfig{PlanOutput: v1.PlanOutputSummary}
		}
	}
	id, _, err := e.st.SaveGraph(e.ctx, repoID, &onMain)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, id))

	e.openPull(7, headSHA)
	job, runID, resp := e.startPlan(7, headSHA)
	outputs := map[string]string{}
	for _, m := range resp.Matrix.Include {
		outputs[m.Key] = m.PlanOutput
	}
	assert.Equal(t, map[string]string{vpc: "summary", staging: "full", eks: "full", apps: "full"}, outputs,
		"the pull request's copy of the stack no longer says summary, the default branch's does")
	for _, key := range []string{vpc, staging} {
		_, err := e.svc.RecordResult(e.ctx, job.p, runID, key, planResult(key, headSHA, 1))
		require.NoError(t, err)
	}
	rows, err := e.st.GetRunStacksWithText(e.ctx, uuid.MustParse(runID))
	require.NoError(t, err)
	text := map[string]string{}
	for _, rs := range rows {
		text[rs.Key] = rs.PlanText
	}
	assert.Empty(t, text[vpc], "no plan text of a summary stack is kept")
	assert.Equal(t, "# plan of "+staging, text[staging])
	assert.NotContains(t, e.sticky(7), "# plan of "+vpc)
	assert.NotContains(t, e.check(report.StackCheckName(report.CheckPlan, vpc)).Output.Text, "# plan of "+vpc)

	cfg := baseConfig()
	cfg.PlanOutput = v1.PlanOutputSummary
	e.setConfig(cfg)
	full := &v1.RepoConfig{Version: 1, PlanOutput: v1.PlanOutputFull}
	e.openPull(8, newHeadSHA)
	planJob := e.planJob(8)
	created, err := e.svc.CreateRun(e.ctx, planJob.p, v1.CreateRunRequest{Repo: repoName, SHA: newHeadSHA, PRNumber: 8, Mode: v1.ModePlan})
	require.NoError(t, err)
	resp, err = e.svc.UploadGraph(e.ctx, planJob.p, created.RunID, v1.GraphUploadRequest{
		Graph: testGraph(newHeadSHA), ChangedPaths: []string{"modules/vpc/main.tf"}, Config: full,
	})
	require.NoError(t, err)
	for _, m := range resp.Matrix.Include {
		assert.Equal(t, "summary", m.PlanOutput, "a pull request's stackorder.yaml cannot lift the default branch's summary for %s", m.Key)
	}
}

func TestPlanOutputSummaryIsReadFromTheDefaultBranchBeforeTheFirstMerge(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetContents(repoName, "main", "stacks/prod/vpc/.stackorder.yaml", []byte("plan_output: summary\n"))
	e.openPull(7, headSHA)
	job, runID, resp := e.startPlan(7, headSHA)
	outputs := map[string]string{}
	for _, m := range resp.Matrix.Include {
		outputs[m.Key] = m.PlanOutput
	}
	assert.Equal(t, map[string]string{vpc: "summary", staging: "full", eks: "full", apps: "full"}, outputs,
		"without a default-branch graph the stack's .stackorder.yaml is read at the default branch")
	_, err := e.svc.RecordResult(e.ctx, job.p, runID, vpc, planResult(vpc, headSHA, 1))
	require.NoError(t, err)
	rows, err := e.st.GetRunStacksWithText(e.ctx, uuid.MustParse(runID))
	require.NoError(t, err)
	for _, rs := range rows {
		if rs.Key == vpc {
			assert.Empty(t, rs.PlanText, "no plan text of a summary stack is kept")
		}
	}
}

func TestPullRequestCannotNarrowItsAffectedSet(t *testing.T) {
	e := newEnv(t, baseConfig())
	onMain := testGraph(mainSHA)
	id, _, err := e.st.SaveGraph(e.ctx, repoID, &onMain)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, id))

	e.gh.SetContents(repoName, headSHA, "stacks/prod/vpc/main.tf", []byte("terraform {}\n"))
	narrowed := testGraph(headSHA)
	narrowed.Stacks = slices.DeleteFunc(narrowed.Stacks, func(st v1.Stack) bool { return st.Key != staging })
	narrowed.Edges = slices.DeleteFunc(narrowed.Edges, func(ed v1.Edge) bool { return ed.From.Key != staging })
	prConfig := baseConfig()
	prConfig.Stacks.Discover = []string{"stacks/staging/**"}
	e.openPull(7, headSHA)
	job := e.planJob(7)
	created, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, BaseSHA: baseSHA, PRNumber: 7, Mode: v1.ModePlan})
	require.NoError(t, err)
	resp, err := e.svc.UploadGraph(e.ctx, job.p, created.RunID, v1.GraphUploadRequest{
		Graph: narrowed, ChangedPaths: []string{"modules/vpc/main.tf", "stacks/prod/vpc/main.tf"}, Config: prConfig,
	})
	require.NoError(t, err)

	waves := map[string]int{}
	for _, a := range resp.Affected {
		waves[a.Key] = a.Wave
	}
	assert.Equal(t, map[string]int{vpc: 0, staging: 0, eks: 1, apps: 2}, waves,
		"the stacks the default branch's stacks.discover finds are planned in dependency order")
	assert.Len(t, resp.Matrix.Include, 4)
	assert.Contains(t, resp.Warnings, "the stackorder.yaml of this pull request differs from the default branch's in stacks.discover; "+
		"under the default branch's configuration stacks/prod/apps, stacks/prod/eks, stacks/prod/vpc are affected as well and planned too")
	run := e.run(created.RunID)
	assert.Contains(t, run.Warnings, resp.Warnings[0])
	assert.Len(t, run.Stacks, 4)
	stored, _, err := e.st.GetGraph(e.ctx, repoID, headSHA)
	require.NoError(t, err)
	keys := make([]string, 0, len(stored.Stacks))
	for _, st := range stored.Stacks {
		keys = append(keys, st.Key)
	}
	assert.ElementsMatch(t, []string{staging, vpc, eks, apps}, keys, "the run's graph carries the added stacks and their edges")
	assert.Contains(t, stored.Edges, v1.Edge{From: v1.StackRef(eks), To: v1.StackRef(vpc), Type: v1.EdgeDependsOn})

	_, err = e.svc.RecordResult(e.ctx, job.p, created.RunID, staging, planResult(staging, headSHA, 1))
	require.NoError(t, err)
	assert.Equal(t, v1.RunPlanning, e.run(created.RunID).Status, "the plan waits for the stacks the pull request tried to leave out")
	e.comment(7, applier, "stackorder apply")
	applies, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: 7, Mode: v1.ModeApply})
	require.NoError(t, err)
	assert.Empty(t, applies, "no apply before every stack affected under the default branch planned")
	assert.Contains(t, e.lastComment(7), vpc)

	for _, key := range []string{vpc, eks, apps} {
		_, err := e.svc.RecordResult(e.ctx, job.p, created.RunID, key, planResult(key, headSHA, 1))
		require.NoError(t, err)
	}
	assert.Equal(t, v1.RunPlanned, e.run(created.RunID).Status)
	e.comment(7, applier, "stackorder apply")
	assert.Len(t, e.applyRun(7).Stacks, 4)
	assert.Len(t, e.locks(), 4, "the apply locks every stack affected under the default branch")

	e.openPull(8, newHeadSHA)
	job8 := e.planJob(8)
	created8, err := e.svc.CreateRun(e.ctx, job8.p, v1.CreateRunRequest{Repo: repoName, SHA: newHeadSHA, BaseSHA: baseSHA, PRNumber: 8, Mode: v1.ModePlan})
	require.NoError(t, err)
	same := baseConfig()
	same.Stacks.Discover = []string{"stacks/**"}
	resp8, err := e.svc.UploadGraph(e.ctx, job8.p, created8.RunID, v1.GraphUploadRequest{
		Graph: testGraph(newHeadSHA), ChangedPaths: []string{"stacks/staging/vpc/main.tf"}, Config: same,
	})
	require.NoError(t, err)
	require.Len(t, resp8.Affected, 1)
	for _, w := range resp8.Warnings {
		assert.NotContains(t, w, "differs from the default branch", "a copy that spells out the defaults changes nothing")
	}
}

func TestDefaultBranchConfigDoesNotBringBackADeletedStack(t *testing.T) {
	const dns = "legacy/dns"
	cfg := baseConfig()
	cfg.Stacks.Include = []string{dns}
	e := newEnv(t, cfg)
	onMain := testGraph(mainSHA)
	onMain.Stacks = append(onMain.Stacks, v1.Stack{Key: dns, Path: dns, Backend: &v1.Backend{Type: "s3", Bucket: "state", Key: dns + ".tfstate"}})
	id, _, err := e.st.SaveGraph(e.ctx, repoID, &onMain)
	require.NoError(t, err)
	require.NoError(t, e.st.SetDefaultGraph(e.ctx, repoID, id))

	e.openPull(7, headSHA)
	job := e.planJob(7)
	created, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, BaseSHA: baseSHA, PRNumber: 7, Mode: v1.ModePlan})
	require.NoError(t, err)
	resp, err := e.svc.UploadGraph(e.ctx, job.p, created.RunID, v1.GraphUploadRequest{
		Graph: testGraph(headSHA), ChangedPaths: []string{"stackorder.yaml", dns + "/main.tf", "stacks/staging/vpc/main.tf"}, Config: baseConfig(),
	})
	require.NoError(t, err)

	keys := make([]string, 0, len(resp.Affected))
	for _, a := range resp.Affected {
		keys = append(keys, a.Key)
	}
	assert.Equal(t, []string{staging}, keys, "a stack whose directory the pull request deletes is not planned")
	removed := "the stackorder.yaml of this pull request differs from the default branch's in stacks.include; " +
		"legacy/dns, which only the default branch's configuration discovers, no longer exists at 3333333 and is recorded as removed"
	assert.Contains(t, resp.Warnings, removed)
	assert.Contains(t, e.run(created.RunID).Warnings, removed)
	stored, _, err := e.st.GetGraph(e.ctx, repoID, headSHA)
	require.NoError(t, err)
	for _, st := range stored.Stacks {
		assert.NotEqual(t, dns, st.Key, "the run's graph, which becomes the default-branch graph on merge, leaves the deleted stack out")
	}
}

func TestCreateRunBinding(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	job := e.planJob(7)
	tests := []struct {
		name string
		p    principal.Principal
		req  v1.CreateRunRequest
		want error
	}{
		{"other repository", job.p, v1.CreateRunRequest{Repo: "acme/other", SHA: headSHA, PRNumber: 7}, principal.ErrForbidden},
		{"pull request number differs from the ref", job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, PRNumber: 8}, principal.ErrForbidden},
		{"stale head", job.p, v1.CreateRunRequest{Repo: repoName, SHA: baseSHA, PRNumber: 7}, principal.ErrSuperseded},
		{"apply from a pull request", job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, PRNumber: 7, Mode: v1.ModeApply}, principal.ErrForbidden},
		{"missing sha", job.p, v1.CreateRunRequest{Repo: repoName, PRNumber: 7}, principal.ErrInvalid},
		{"dispatch without run id", e.dispatchJob(9, v1.DefaultEnvironment), v1.CreateRunRequest{Repo: repoName}, principal.ErrInvalid},
		{"dispatch for an unknown run", e.dispatchJob(9, v1.DefaultEnvironment), v1.CreateRunRequest{RunID: uuid.NewString()}, principal.ErrNotFound},
		{"session", principal.Principal{Kind: principal.Session, Login: "x"}, v1.CreateRunRequest{Repo: repoName}, principal.ErrForbidden},
		{"api key without manual trigger", apiKey(), v1.CreateRunRequest{Repo: repoName, SHA: headSHA, Mode: v1.ModeApply, Stacks: []string{vpc}}, principal.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.CreateRun(e.ctx, tt.p, tt.req)
			require.ErrorIs(t, err, tt.want)
		})
	}

	wrongID := e.planJob(7)
	wrongID.p.Claims.RepositoryID = "999"
	_, err := e.svc.CreateRun(e.ctx, wrongID.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, PRNumber: 7})
	require.ErrorIs(t, err, principal.ErrForbidden, "repository_id must match")
}

func TestCacheHitAndRerunAttempt(t *testing.T) {
	e := newEnv(t, baseConfig())
	first := e.planned(7, headSHA)

	e.openPull(8, newHeadSHA)
	job := e.planJob(8)
	created, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: newHeadSHA, PRNumber: 8})
	require.NoError(t, err)
	g := testGraph(newHeadSHA)
	g.TreeHash = "tree-" + headSHA
	resp, err := e.svc.UploadGraph(e.ctx, job.p, created.RunID, v1.GraphUploadRequest{Graph: g, ChangedPaths: []string{"modules/vpc/main.tf"}})
	require.NoError(t, err)
	assert.True(t, resp.Cached, "the same tree reuses the stored graph")
	for _, m := range resp.Matrix.Include {
		assert.Equal(t, newHeadSHA, m.SHA, "even a cached graph plans the run's own commit")
	}
	u := uuid.MustParse(created.RunID)
	stored, err := e.st.GetRun(e.ctx, u)
	require.NoError(t, err)
	require.NotNil(t, stored.GraphID)
	firstStored, err := e.st.GetRun(e.ctx, uuid.MustParse(first))
	require.NoError(t, err)
	assert.Equal(t, *firstStored.GraphID, *stored.GraphID, "the run is linked to the existing graph")
	assert.Contains(t, e.check(report.CheckResolve).Output.Summary, "stored graph")

	rerun := e.planJob(7)
	rerun.p.Claims.RunID = job.p.Claims.RunID
	rerun.p.Claims.RunAttempt = "2"
	again, err := e.svc.CreateRun(e.ctx, rerun.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, PRNumber: 7})
	require.NoError(t, err)
	assert.False(t, again.Existing, "a new workflow run on a planned commit starts a new plan run")
	assert.NotEqual(t, first, again.RunID)
}

func TestCycleFailsResolve(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	job := e.planJob(7)
	created, err := e.svc.CreateRun(e.ctx, job.p, v1.CreateRunRequest{Repo: repoName, SHA: headSHA, PRNumber: 7})
	require.NoError(t, err)
	g := testGraph(headSHA)
	g.Edges = append(g.Edges, v1.Edge{From: v1.StackRef(vpc), To: v1.StackRef(apps), Type: v1.EdgeDependsOn})
	resp, err := e.svc.UploadGraph(e.ctx, job.p, created.RunID, v1.GraphUploadRequest{Graph: g, ChangedPaths: []string{"modules/vpc/main.tf"}})
	require.NoError(t, err, "a cycle is a result, not an error")
	require.Len(t, resp.Cycles, 1)
	assert.Equal(t, []string{apps, eks, vpc, apps}, resp.Cycles[0])
	assert.Empty(t, resp.Matrix.Include)

	assert.Equal(t, v1.RunFailed, e.run(created.RunID).Status)
	resolve := e.check(report.CheckResolve)
	assert.Equal(t, gh.ConclusionFailure, resolve.Conclusion)
	assert.Contains(t, resolve.Output.Title, "Dependency cycle: "+apps+" → "+eks+" → "+vpc+" → "+apps)
	assert.Equal(t, gh.ConclusionFailure, e.check(report.CheckPlan).Conclusion)

	_, err = e.svc.UploadGraph(e.ctx, job.p, created.RunID, v1.GraphUploadRequest{Graph: testGraph(headSHA)})
	require.ErrorIs(t, err, principal.ErrConflict, "a failed run takes no graph")
}

func TestSupersedeOnNewHead(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	oldJob, oldRun, _ := e.startPlan(7, headSHA)
	_, err := e.svc.RecordResult(e.ctx, oldJob.p, oldRun, vpc, planResult(vpc, headSHA, 1))
	require.NoError(t, err)

	e.openPull(7, newHeadSHA)
	ev := e.gh.PullRequestEvent("synchronize", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueOpen, HeadSHA: newHeadSHA, BaseSHA: baseSHA, User: gh.User{Login: author}, Mergeable: ptr(true),
	})
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, ev))
	assert.Equal(t, v1.RunSuperseded, e.run(oldRun).Status)
	for _, c := range e.gh.CheckRuns(repoName) {
		if c.HeadSHA != headSHA || !strings.HasPrefix(c.Name, report.CheckPlan) {
			continue
		}
		assert.Equal(t, gh.ConclusionNeutral, c.Conclusion, c.Name)
		assert.Equal(t, "Superseded by 4444444", c.Output.Title, c.Name)
	}
	assert.Equal(t, gh.ConclusionSuccess, e.check(report.CheckResolve).Conclusion, "the resolve check stays as it was")

	_, err = e.svc.RecordResult(e.ctx, oldJob.p, oldRun, eks, planResult(eks, headSHA, 1))
	require.ErrorIs(t, err, principal.ErrSuperseded)
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, ev), "a redelivered event changes nothing")

	_, newRun, _ := e.startPlan(7, newHeadSHA)
	assert.NotEqual(t, oldRun, newRun)
	assert.Equal(t, v1.RunPlanning, e.run(newRun).Status)
	assert.Contains(t, e.sticky(7), "4444444", "the sticky comment follows the new head")
}

func TestSupersedeFollowsTheCurrentHead(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	other := runs.New(e.st, e.app, runs.Config{BaseURL: "https://stackorder.test", Clock: e.clock.Now}, nil, nil)
	req := v1.CreateRunRequest{Repo: repoName, SHA: headSHA, BaseSHA: baseSHA, PRNumber: 7, Mode: v1.ModePlan}
	first, err := other.CreateRun(e.ctx, e.planJob(7).p, req)
	require.NoError(t, err)
	stale := e.gh.PullRequestEvent("synchronize", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueOpen, HeadSHA: headSHA, BaseSHA: baseSHA, User: gh.User{Login: author}, Mergeable: ptr(true),
	})

	e.openPull(7, newHeadSHA)
	_, current, _ := e.startPlan(7, newHeadSHA)
	assert.Equal(t, v1.RunSuperseded, e.run(first.RunID).Status)

	require.NoError(t, e.svc.HandlePullRequest(e.ctx, stale))
	assert.Equal(t, v1.RunPlanning, e.run(current).Status, "a synchronize event handled after a newer one leaves the current head alone")

	_, err = other.CreateRun(e.ctx, e.planJob(7).p, req)
	require.ErrorIs(t, err, principal.ErrSuperseded, "another server that saw the old head checks the pull request again")
	assert.Equal(t, v1.RunPlanning, e.run(current).Status)
}

func TestForkPullRequestNotice(t *testing.T) {
	e := newEnv(t, baseConfig())
	fork := &gh.Repository{ID: 900, FullName: "someone/infra", Fork: true}
	ev := e.gh.PullRequestEvent("opened", repoName, gh.PullRequest{
		Number: 9, State: gh.IssueOpen, HeadSHA: headSHA, User: gh.User{Login: "someone"},
		Head: gh.PullBranch{Repo: fork},
	})
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, ev))
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, ev))
	checks := e.checksNamed(report.CheckPlan)
	require.Len(t, checks, 1, "one neutral check, even on redelivery")
	title, _ := report.ForkNotice()
	assert.Equal(t, gh.ConclusionNeutral, checks[0].Conclusion)
	assert.Equal(t, title, checks[0].Output.Title)
	runs, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: 9})
	require.NoError(t, err)
	assert.Empty(t, runs, "nothing runs for a fork")
}

func TestUndispatchedPlanLeavesTheHeadPlanAlone(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	before := e.check(report.CheckPlan)
	require.Equal(t, gh.ConclusionSuccess, before.Conclusion)
	e.gh.FailNext("POST /repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches", http.StatusUnprocessableEntity, 1)
	e.comment(7, applier, "stackorder plan "+vpc)
	assert.Contains(t, e.lastComment(7), "could not start the plan")

	after := e.check(report.CheckPlan)
	assert.Equal(t, gh.CheckRunCompleted, after.Status, "no plan is running")
	assert.Equal(t, gh.ConclusionSuccess, after.Conclusion)
	assert.Equal(t, before.Output.Title, after.Output.Title)
	e.comment(7, applier, "stackorder apply")
	assert.NotContains(t, e.lastComment(7), "still running")
	assert.Equal(t, v1.RunApplying, e.applyRun(7).Status, "the head's plan still stands")
}

func TestWorkflowJobProgress(t *testing.T) {
	e := newEnv(t, baseConfig())
	runID := e.planned(7, headSHA)
	e.comment(7, applier, "stackorder plan "+vpc)
	d := e.gh.Dispatches()
	require.Len(t, d, 1)
	planRun := d[0].Inputs["run_id"]
	assert.NotEqual(t, runID, planRun)
	assert.Equal(t, "plan", d[0].Inputs["mode"])
	assert.Equal(t, "main", d[0].Ref)
	es := entries(t, d[0])
	require.Len(t, es, 1)
	assert.Equal(t, v1.DefaultEnvironment, es[0].Environment, "plan dispatches run under the default environment")

	require.NoError(t, e.svc.HandleWorkflowRun(e.ctx, e.gh.WorkflowRunEvent("requested", repoName, gh.WorkflowRun{ID: d[0].RunID})))
	job := gh.WorkflowJob{RunID: d[0].RunID, Name: "run / plan (" + vpc + ", " + vpc + ", default, 0)", Status: gh.RunStatusInProgress,
		HTMLURL: "https://github.com/acme/infra/actions/runs/1/job/77"}
	require.NoError(t, e.svc.HandleWorkflowJob(e.ctx, e.gh.WorkflowJobEvent("in_progress", repoName, job)))
	r := e.run(planRun)
	assert.Equal(t, v1.StackPlanning, r.Stacks[0].Status)
	assert.Equal(t, job.HTMLURL, r.Stacks[0].JobURL)
	assert.Equal(t, gh.CheckRunInProgress, e.check(report.StackCheckName(report.CheckPlan, vpc)).Status)

	unmatched := job
	unmatched.Name = "run / plan (stacks/nope, x)"
	require.NoError(t, e.svc.HandleWorkflowJob(e.ctx, e.gh.WorkflowJobEvent("in_progress", repoName, unmatched)))
	stranger := job
	stranger.RunID = 424242
	require.NoError(t, e.svc.HandleWorkflowJob(e.ctx, e.gh.WorkflowJobEvent("in_progress", repoName, stranger)))
}

func TestPullRequestPlanJobProgress(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	job, runID, _ := e.startPlan(7, headSHA)
	wr, err := job.p.Claims.RunIDInt()
	require.NoError(t, err)

	resolveJob := gh.WorkflowJob{RunID: wr, Name: "plan / resolve", Status: gh.RunStatusInProgress, HTMLURL: "https://github.com/acme/infra/actions/runs/5/job/1"}
	require.NoError(t, e.svc.HandleWorkflowJob(e.ctx, e.gh.WorkflowJobEvent("in_progress", repoName, resolveJob)))
	planJob := gh.WorkflowJob{RunID: wr, Name: "plan / plan (" + eks + ", production)", Status: gh.RunStatusInProgress,
		HTMLURL: "https://github.com/acme/infra/actions/runs/5/job/2"}
	require.NoError(t, e.svc.HandleWorkflowJob(e.ctx, e.gh.WorkflowJobEvent("in_progress", repoName, planJob)))
	require.NoError(t, e.svc.HandleWorkflowJob(e.ctx, e.gh.WorkflowJobEvent("in_progress", repoName, planJob)), "a redelivered job event changes nothing")

	statuses := stackStatuses(e.run(runID))
	assert.Equal(t, v1.StackPlanning, statuses[eks], "the plan job of the pull request workflow run starts its stack")
	assert.Equal(t, v1.StackPending, statuses[vpc])
	for _, rs := range e.run(runID).Stacks {
		if rs.Key == eks {
			assert.Equal(t, planJob.HTMLURL, rs.JobURL)
		}
	}
	assert.Equal(t, gh.CheckRunInProgress, e.check(report.StackCheckName(report.CheckPlan, eks)).Status)

	_, err = e.svc.RecordResult(e.ctx, job.p, runID, eks, planResult(eks, headSHA, 1))
	require.NoError(t, err)
	assert.Equal(t, v1.StackPlanned, stackStatuses(e.run(runID))[eks], "the result is the source of truth")
}

func TestArtifactStoreKeepsFullPlanText(t *testing.T) {
	e := newEnv(t, baseConfig(), withArtifacts())
	e.openPull(7, headSHA)
	job, runID, _ := e.startPlan(7, headSHA)
	res := planResult(vpc, headSHA, 1)
	res.PlanText = strings.Repeat("resource change line\n", 1000)
	row, err := e.svc.RecordResult(e.ctx, job.p, runID, vpc, res)
	require.NoError(t, err)
	key := "runs/" + runID + "/stacks-prod-vpc-69df0ef0/plan.txt"
	assert.Equal(t, "https://artifacts.test/"+key, row.PlanURL)
	assert.True(t, row.Truncated)
	full, err := e.st.GetRunStack(e.ctx, uuid.MustParse(runID), uuid.MustParse(row.StackID))
	require.NoError(t, err)
	assert.Len(t, full.PlanText, 8<<10, "Postgres keeps the first 8 KB")
	e.artifacts.mu.Lock()
	defer e.artifacts.mu.Unlock()
	assert.Equal(t, res.PlanText, string(e.artifacts.items[key]))
	assert.Contains(t, string(e.artifacts.items["runs/"+runID+"/stacks-prod-vpc-69df0ef0/plan.json"]), `"adds":1`)
}

func TestNothingAffected(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.openPull(7, headSHA)
	_, runID, resp := e.startPlan(7, headSHA, "README.md")
	assert.Empty(t, resp.Affected)
	assert.Empty(t, resp.Matrix.Include)
	assert.Equal(t, v1.RunPlanned, e.run(runID).Status, "a run with nothing to plan is planned at once")
	rollup := e.check(report.CheckPlan)
	assert.Equal(t, gh.ConclusionSuccess, rollup.Conclusion)
	assert.Equal(t, "No stacks affected", rollup.Output.Title)
	assert.Equal(t, "No stacks affected", e.check(report.CheckResolve).Output.Title)
	apply := e.check(report.CheckApply)
	assert.Equal(t, gh.ConclusionSuccess, apply.Conclusion, "branch protection can require stackorder/apply on a pull request that changes no stack")
	assert.Equal(t, headSHA, apply.HeadSHA)
	assert.Equal(t, "No stacks affected", apply.Output.Title)

	e.comment(7, applier, "stackorder apply")
	assert.Contains(t, e.lastComment(7), "there is nothing to apply", "an apply of nothing is answered, not run")
	cfg := baseConfig()
	cfg.Apply.Mode = v1.ApplyOnMerge
	e.setConfig(cfg)
	e.openPull(8, newHeadSHA)
	e.startPlan(8, newHeadSHA, "README.md")
	for _, c := range e.checksNamed(report.CheckApply) {
		assert.NotEqual(t, newHeadSHA, c.HeadSHA, "on_merge applies after the merge, so its pull requests carry no apply check")
	}
	n := len(e.comments(7))
	merged := e.gh.PullRequestEvent("closed", repoName, gh.PullRequest{
		Number: 7, State: gh.IssueClosed, Merged: true, MergeCommitSHA: mergeSHA, HeadSHA: headSHA, BaseSHA: baseSHA, User: gh.User{Login: author},
	})
	require.NoError(t, e.svc.HandlePullRequest(e.ctx, merged))
	assert.Len(t, e.comments(7), n, "merging a pull request that affects nothing applies nothing and says nothing")
	applies, _, err := e.st.ListRuns(e.ctx, store.RunFilter{RepoID: repoID, PRNumber: 7, Mode: v1.ModeApply})
	require.NoError(t, err)
	assert.Empty(t, applies)
}
