//go:build integration

package runs_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func listOf(addresses ...string) *v1.ResourceList {
	list := &v1.ResourceList{Total: len(addresses), Items: []v1.StackResource{}}
	for _, a := range addresses {
		list.Items = append(list.Items, v1.StackResource{Address: a, Type: "aws_vpc", Provider: "registry.terraform.io/hashicorp/aws"})
	}
	return list
}

func withResources(res v1.StackResult, list *v1.ResourceList) v1.StackResult {
	res.Resources = list
	return res
}

func (e *env) resources(key string) (store.StackResources, error) {
	e.t.Helper()
	return e.st.GetStackResources(e.ctx, e.stack(key).ID)
}

func (e *env) requireResources(key, runID string, want *v1.ResourceList) store.StackResources {
	e.t.Helper()
	got, err := e.resources(key)
	require.NoError(e.t, err, key)
	require.NotNil(e.t, got.RunID, key)
	assert.Equal(e.t, runID, got.RunID.String(), "%s: the run that reported it", key)
	assert.Equal(e.t, want.Total, got.Total, key)
	assert.Equal(e.t, want.Truncated, got.Truncated, key)
	if diff := cmp.Diff(want.Items, got.Resources); diff != "" {
		e.t.Errorf("%s: recorded resources (-want +got):\n%s", key, diff)
	}
	return got
}

func (e *env) requireNoResources(key string) {
	e.t.Helper()
	_, err := e.resources(key)
	require.ErrorIs(e.t, err, store.ErrNotFound, "%s has no recorded list", key)
}

func (e *env) refuseResourceLists(refuse bool) {
	e.t.Helper()
	sql := `ALTER TABLE stack_resources DROP CONSTRAINT refuse_lists`
	if refuse {
		sql = `ALTER TABLE stack_resources ADD CONSTRAINT refuse_lists CHECK (total < 0) NOT VALID`
	}
	_, err := e.st.Pool().Exec(e.ctx, sql)
	require.NoError(e.t, err)
}

func (e *env) startApply() (v1.Run, principal.Principal) {
	e.t.Helper()
	e.planned(7, headSHA)
	e.comment(7, applier, "stackorder apply")
	apply := e.applyRun(7)
	return apply, e.dispatchJob(e.newestDispatchOf(vpc).RunID, "production")
}

func (e *env) stackFinishedAt(runID, key string) time.Time {
	e.t.Helper()
	rs, err := e.st.GetRunStack(e.ctx, uuid.MustParse(runID), e.stack(key).ID)
	require.NoError(e.t, err)
	require.NotNil(e.t, rs.FinishedAt, "%s finished in run %s", key, runID)
	return *rs.FinishedAt
}

func driftResult() v1.StackResult {
	return v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}
}

func (e *env) startDrift() (string, principal.Principal) {
	e.t.Helper()
	require.NoError(e.t, e.svc.RunDriftStack(e.ctx, repoID, e.stack(vpc).ID.String()))
	d := e.newestDispatchOf(vpc)
	return d.Inputs["run_id"], e.dispatchJob(d.RunID, v1.DefaultEnvironment)
}

func (e *env) setDefaultGraph() {
	e.t.Helper()
	g := testGraph(mainSHA)
	gid, _, err := e.st.SaveGraph(e.ctx, repoID, &g)
	require.NoError(e.t, err)
	require.NoError(e.t, e.st.SetDefaultGraph(e.ctx, repoID, gid))
}

func TestApplyResultRecordsTheResourceList(t *testing.T) {
	tests := []struct {
		name   string
		ok     bool
		status v1.StackStatus
	}{
		{name: "applied", ok: true, status: v1.StackApplied},
		{name: "failed apply", ok: false, status: v1.StackFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logRecorder{}
			e := newEnv(t, baseConfig(), withLogger(logs.logger()))
			apply, prod := e.startApply()

			_, err := e.svc.RecordResult(e.ctx, prod, apply.ID, staging, withResources(applyResult(true), listOf("aws_vpc.staging")))
			require.ErrorIs(t, err, principal.ErrForbidden, "a job of another environment is refused")
			e.requireNoResources(staging)

			list := listOf("aws_subnet.a", "aws_vpc.main")
			row, err := e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withResources(applyResult(tt.ok), list))
			require.NoError(t, err)
			assert.Equal(t, tt.status, row.Status)
			recorded := e.requireResources(vpc, apply.ID, list)
			assert.Equal(t, v1.ModeApply, recorded.RunMode)
			assert.Equal(t, e.storedRun(apply.ID).SHA, recorded.SHA)

			_, err = e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withResources(applyResult(!tt.ok), listOf("aws_vpc.other")))
			require.ErrorIs(t, err, principal.ErrConflict, "a stack's result is final")
			assert.Equal(t, recorded, e.requireResources(vpc, apply.ID, list), "a refused result records nothing")

			e.requireNoResources(eks)
			assert.Empty(t, logs.records(t, "record resources"))
			assert.Empty(t, logs.records(t, "drop invalid resources"))
		})
	}
}

func TestDuplicateResultRecordsTheResourceList(t *testing.T) {
	logs := &logRecorder{}
	e := newEnv(t, baseConfig(), withLogger(logs.logger()))
	apply, prod := e.startApply()
	e.setDefaultGraph()
	driftID, driftJob := e.startDrift()
	drifted := listOf("aws_subnet.a")
	_, err := e.svc.RecordResult(e.ctx, driftJob, driftID, vpc, withResources(driftResult(), drifted))
	require.NoError(t, err)
	e.requireResources(vpc, driftID, drifted)
	list := listOf("aws_vpc.main")

	e.refuseResourceLists(true)
	row, err := e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withResources(applyResult(true), list))
	require.NoError(t, err, "a list the store refuses never fails the result")
	assert.Equal(t, v1.StackApplied, row.Status)
	e.requireResources(vpc, driftID, drifted)
	warned := logs.records(t, "record resources")
	require.Len(t, warned, 1)
	assert.Equal(t, apply.ID, warned[0]["run_id"])
	assert.Equal(t, vpc, warned[0]["stack"])
	assert.NotContains(t, logs.String(), "aws_vpc.main", "no item content is logged")

	e.refuseResourceLists(false)
	row, err = e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withResources(applyResult(true), list))
	require.NoError(t, err)
	assert.Equal(t, v1.StackApplied, row.Status)
	recorded := e.requireResources(vpc, apply.ID, list)
	assert.True(t, recorded.ObservedAt.Equal(e.stackFinishedAt(apply.ID, vpc)),
		"the retry is dated when the stack finished, after the drift run of its approval wait was created")
	assert.Len(t, logs.records(t, "record resources"), 1, "the retry records the list")

	_, err = e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, applyResult(true))
	require.NoError(t, err, "a repeated result without a list is accepted")
	e.requireResources(vpc, apply.ID, list)
}

func TestDriftResultRecordsTheResourceList(t *testing.T) {
	tests := []struct {
		name string
		res  v1.StackResult
	}{
		{
			name: "drifted",
			res: v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, ExitCode: 2, HasChanges: true,
				Summary: &v1.PlanSummary{Changes: 1, Changed: []string{"aws_vpc.main"}}},
		},
		{name: "no drift", res: v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}},
		{name: "failed after init", res: v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultFailure, ExitCode: 1, ErrorText: "Error: boom"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, baseConfig())
			e.setDefaultGraph()
			runID, job := e.startDrift()
			list := listOf("aws_vpc.main")
			_, err := e.svc.RecordResult(e.ctx, job, runID, vpc, withResources(tt.res, list))
			require.NoError(t, err)
			recorded := e.requireResources(vpc, runID, list)
			assert.Equal(t, v1.ModeDrift, recorded.RunMode)
			assert.Equal(t, mainSHA, recorded.SHA)

			e.clock.Advance(time.Hour)
			later, job := e.startDrift()
			require.NotEqual(t, runID, later)
			empty := &v1.ResourceList{Items: []v1.StackResource{}}
			_, err = e.svc.RecordResult(e.ctx, job, later, vpc, withResources(tt.res, empty))
			require.NoError(t, err)
			e.requireResources(vpc, later, empty)
		})
	}
}

func TestApplyListBeatsADriftCreatedDuringItsWait(t *testing.T) {
	e := newEnv(t, baseConfig())
	apply, prod := e.startApply()
	e.setDefaultGraph()
	driftID, driftJob := e.startDrift()
	require.False(t, e.storedRun(driftID).CreatedAt.Before(e.storedRun(apply.ID).CreatedAt), "the drift run is the newer run")

	before := listOf("aws_vpc.main")
	_, err := e.svc.RecordResult(e.ctx, driftJob, driftID, vpc, withResources(driftResult(), before))
	require.NoError(t, err)
	e.requireResources(vpc, driftID, before)

	after := listOf("aws_subnet.a", "aws_vpc.main")
	_, err = e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, withResources(applyResult(true), after))
	require.NoError(t, err)
	e.requireResources(vpc, apply.ID, after)
}

func TestRepeatedApplyResultKeepsANewerList(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	created, err := e.svc.CreateRun(e.ctx, apiKey(), v1.CreateRunRequest{
		Repo: repoName, SHA: mainSHA, Mode: v1.ModeApply, Trigger: v1.TriggerManual, Stacks: []string{vpc},
	})
	require.NoError(t, err)
	applied := listOf("aws_vpc.main")
	_, err = e.svc.RecordResult(e.ctx, apiKey(), created.RunID, vpc, withResources(applyResult(true), applied))
	require.NoError(t, err)
	first := e.requireResources(vpc, created.RunID, applied)
	assert.False(t, first.ObservedAt.Before(e.stackFinishedAt(created.RunID, vpc)), "a first apply result is dated when it is recorded")

	e.setDefaultGraph()
	driftID, driftJob := e.startDrift()
	drifted := listOf("aws_subnet.a", "aws_vpc.main")
	_, err = e.svc.RecordResult(e.ctx, driftJob, driftID, vpc, withResources(driftResult(), drifted))
	require.NoError(t, err)
	newer := e.requireResources(vpc, driftID, drifted)

	row, err := e.svc.RecordResult(e.ctx, apiKey(), created.RunID, vpc, withResources(applyResult(true), applied))
	require.NoError(t, err, "the replay is accepted")
	assert.Equal(t, v1.StackApplied, row.Status)
	assert.Equal(t, newer, e.requireResources(vpc, driftID, drifted),
		"a repeated apply result posted after a newer drift list was recorded does not replace it")
}

func TestManualApplyRecordsTheResourceList(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.planned(7, headSHA)
	created, err := e.svc.CreateRun(e.ctx, apiKey(), v1.CreateRunRequest{
		Repo: repoName, SHA: mainSHA, Mode: v1.ModeApply, Trigger: v1.TriggerManual, Stacks: []string{vpc},
	})
	require.NoError(t, err)
	list := listOf("aws_vpc.main")
	row, err := e.svc.RecordResult(e.ctx, apiKey(), created.RunID, vpc, withResources(applyResult(true), list))
	require.NoError(t, err)
	assert.Equal(t, v1.StackApplied, row.Status)
	e.requireResources(vpc, created.RunID, list)
	e.requireNoResources(staging)
}

func TestPlanResultNeverRecordsTheResourceList(t *testing.T) {
	logs := &logRecorder{}
	e := newEnv(t, baseConfig(), withLogger(logs.logger()))
	e.openPull(7, headSHA)
	job, runID, resp := e.startPlan(7, headSHA)
	require.NotEmpty(t, resp.Affected)
	for _, a := range resp.Affected {
		_, err := e.svc.RecordResult(e.ctx, job.p, runID, a.Key, withResources(planResult(a.Key, headSHA, 1), listOf("aws_vpc.main")))
		require.NoError(t, err)
	}
	assert.Equal(t, v1.RunPlanned, e.run(runID).Status)
	stacks, err := e.st.ListStacks(e.ctx, repoID, true)
	require.NoError(t, err)
	for _, st := range stacks {
		e.requireNoResources(st.Key)
	}
	assert.Empty(t, logs.records(t, "record resources"), "a plan result's list never reaches the store")
}

func TestResultResourceListIsWhitelisted(t *testing.T) {
	logs := &logRecorder{}
	e := newEnv(t, baseConfig(), withLogger(logs.logger()))
	apply, prod := e.startApply()

	const secret = "hunter2hunter2"
	long := "aws_vpc." + strings.Repeat("x", 2048)
	body := `{
		"mode": "apply", "status": "success", "summary": {"adds": 1}, "duration_ms": 2000,
		"resources": {"total": 6, "items": [
			{"address": "aws_vpc.main", "type": "aws_vpc", "provider": "registry.terraform.io/hashicorp/aws",
			 "values": {"cidr": "` + secret + `"}, "sensitive_values": {"password": "` + secret + `"},
			 "identity": {"id": "` + secret + `"}, "depends_on": ["` + secret + `"],
			 "last_change": {"action": "create", "run_id": "forged", "sha": "forged"}},
			{"address": "aws_vpc.\u202e` + secret + `", "type": "aws_vpc"},
			{"address": "` + long + `", "type": "aws_vpc"},
			{"address": "aws_subnet.` + secret + `", "type": "aws_\n` + secret + `"},
			{"address": "module.net.aws_subnet.a[\"eu-west-1a/x.y\"]", "type": "aws_subnet", "module": "module.net", "tainted": true},
			{"address": "aws_vpc.main", "type": "aws_vpc", "deposed": true}
		], "outputs": {"db_password": "` + secret + `"}}
	}`
	var res v1.StackResult
	require.NoError(t, json.Unmarshal([]byte(body), &res))

	_, err := e.svc.RecordResult(e.ctx, prod, apply.ID, vpc, res)
	require.NoError(t, err)
	want := &v1.ResourceList{Total: 6, Truncated: true, Items: []v1.StackResource{
		{Address: "aws_vpc.main", Type: "aws_vpc", Provider: "registry.terraform.io/hashicorp/aws", Deposed: true},
		{Address: `module.net.aws_subnet.a["eu-west-1a/x.y"]`, Type: "aws_subnet", Module: "module.net", Tainted: true},
	}}
	e.requireResources(vpc, apply.ID, want)

	var stored string
	require.NoError(t, e.st.Pool().QueryRow(e.ctx, `SELECT resources::text FROM stack_resources`).Scan(&stored))
	for _, banned := range []string{secret, "values", "identity", "depends_on", "last_change", "forged", "outputs"} {
		assert.NotContains(t, stored, banned, "only whitelisted fields are stored")
	}

	warned := logs.records(t, "drop invalid resources")
	require.Len(t, warned, 1, "one warning for every dropped item")
	assert.Equal(t, apply.ID, warned[0]["run_id"])
	assert.Equal(t, vpc, warned[0]["stack"])
	assert.InDelta(t, 3, warned[0]["dropped"], 0)
	assert.InDelta(t, 2, warned[0]["kept"], 0)
	assert.InDelta(t, 6, warned[0]["total"], 0)
	assert.InDelta(t, 1, warned[0]["index"], 0)
	assert.Equal(t, "address", warned[0]["field"])
	assert.Equal(t, "holds a control or format character", warned[0]["reason"])
	all := logs.String()
	for _, banned := range []string{secret, "aws_vpc.main", "module.net", "aws_subnet", "registry.terraform.io"} {
		assert.NotContains(t, all, banned, "no item content is logged")
	}
}

func TestRefusedResultRecordsNoResourceList(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.setDefaultGraph()
	runID, _ := e.startDrift()
	wrongEnv := e.dispatchJob(e.newestDispatchOf(vpc).RunID, "production")
	drift := v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}

	_, err := e.svc.RecordResult(e.ctx, wrongEnv, runID, vpc, withResources(drift, listOf("aws_vpc.main")))
	require.ErrorIs(t, err, principal.ErrForbidden)
	_, err = e.svc.RecordResult(e.ctx, apiKey(), runID, vpc, withResources(drift, listOf("aws_vpc.main")))
	require.ErrorIs(t, err, principal.ErrForbidden, "an API key reports only for manual runs")
	_, err = e.svc.RecordResult(e.ctx, e.dispatchJob(e.newestDispatchOf(vpc).RunID, v1.DefaultEnvironment), runID, vpc,
		withResources(applyResult(true), listOf("aws_vpc.main")))
	var invalid *principal.InvalidError
	require.ErrorAs(t, err, &invalid, "a result of the wrong mode is refused")
	e.requireNoResources(vpc)
}
