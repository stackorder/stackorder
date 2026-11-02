//go:build integration

package integration

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/testutil/faketf"
)

const (
	builtinProvider = "terraform.io/builtin/terraform"
	awsProvider     = "registry.terraform.io/hashicorp/aws"

	valuesSentinel    = "values-sentinel-5d3e9b"
	sensitiveSentinel = "sensitive-sentinel-5d3e9b"
	identitySentinel  = "identity-sentinel-5d3e9b"
	outputsSentinel   = "outputs-sentinel-5d3e9b"
)

var stateSentinels = []string{valuesSentinel, sensitiveSentinel, identitySentinel, outputsSentinel}

type stateResource struct {
	module, address, typ, provider string
	data, tainted, deposed         bool
}

func managed(module, address, typ, provider string) stateResource {
	return stateResource{module: module, address: address, typ: typ, provider: provider}
}

func (r stateResource) taint() stateResource { r.tainted = true; return r }

func (r stateResource) depose() stateResource { r.deposed = true; return r }

func dataSource(module, address, typ string) stateResource {
	return stateResource{module: module, address: address, typ: typ, provider: builtinProvider, data: true}
}

func stateJSON(t *testing.T, resources ...stateResource) string {
	t.Helper()
	modules := map[string]map[string]any{"": {"resources": []any{}}}
	var order []string
	var ensure func(addr string) map[string]any
	ensure = func(addr string) map[string]any {
		if m, ok := modules[addr]; ok {
			return m
		}
		m := map[string]any{"address": addr, "resources": []any{}}
		modules[addr] = m
		order = append(order, addr)
		parent := ""
		if i := strings.LastIndex(addr, ".module."); i >= 0 {
			parent = addr[:i]
		}
		p := ensure(parent)
		children, _ := p["child_modules"].([]any)
		p["child_modules"] = append(children, m)
		return m
	}
	for _, r := range resources {
		m := ensure(r.module)
		mode := "managed"
		if r.data {
			mode = "data"
		}
		obj := map[string]any{
			"address":          r.address,
			"mode":             mode,
			"type":             r.typ,
			"name":             "x",
			"provider_name":    r.provider,
			"schema_version":   0,
			"values":           map[string]any{"input": map[string]any{"secret": valuesSentinel}},
			"sensitive_values": map[string]any{sensitiveSentinel: true},
		}
		if !r.data {
			obj["identity_schema_version"] = 0
			obj["identity"] = map[string]any{"id": identitySentinel}
		}
		if r.tainted {
			obj["tainted"] = true
		}
		m["resources"] = append(m["resources"].([]any), obj)
		if r.deposed {
			old := map[string]any{}
			for k, v := range obj {
				old[k] = v
			}
			delete(old, "tainted")
			old["deposed_key"] = "00a1b2c3"
			m["resources"] = append(m["resources"].([]any), old)
		}
	}
	state := map[string]any{
		"format_version":    "1.0",
		"terraform_version": faketf.DefaultVersion,
		"values": map[string]any{
			"outputs": map[string]any{
				"db_password": map[string]any{"sensitive": true, "type": "string", "value": outputsSentinel},
				"vpc_name":    map[string]any{"sensitive": false, "type": "string", "value": outputsSentinel + "-name"},
			},
			"root_module": modules[""],
		},
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	for _, s := range stateSentinels {
		require.Contains(t, string(data), s, "the state carries every sentinel")
	}
	return string(data)
}

func listed(resources ...stateResource) []v1.StackResource {
	var out []v1.StackResource
	for _, r := range resources {
		if r.data {
			continue
		}
		out = append(out, v1.StackResource{Address: r.address, Type: r.typ, Module: r.module, Provider: r.provider, Tainted: r.tainted, Deposed: r.deposed})
	}
	slices.SortFunc(out, func(a, b v1.StackResource) int { return cmp.Compare(a.Address, b.Address) })
	return out
}

func withoutChanges(items []v1.StackResource) ([]v1.StackResource, map[string]v1.ResourceChange) {
	out := make([]v1.StackResource, len(items))
	changes := map[string]v1.ResourceChange{}
	for i, it := range items {
		if it.LastChange != nil {
			changes[it.Address] = *it.LastChange
		}
		it.LastChange = nil
		out[i] = it
	}
	return out, changes
}

type resourceReads struct {
	f      *fixture
	bodies []string
}

func (r *resourceReads) get(key string) v1.StackResources {
	r.f.t.Helper()
	path := "/v1/stacks/" + r.f.stackID(key) + "/resources"
	status, body := r.f.e.call(http.MethodGet, path, nil, withAPIKey)
	require.Equal(r.f.t, http.StatusOK, status, "GET %s: %s", path, body)
	r.bodies = append(r.bodies, body)
	var out v1.StackResources
	requireJSON(r.f.t, body, &out)
	require.NotNil(r.f.t, out.Items, "items is never null")
	return out
}

func (f *fixture) recordedResources() map[string]string {
	f.t.Helper()
	rows, err := f.e.Store.Pool().Query(f.t.Context(), `
		SELECT s.key, coalesce(sr.run_id::text, '') FROM stack_resources sr JOIN stacks s ON s.id = sr.stack_id
		WHERE s.repo_id = $1`, f.id)
	require.NoError(f.t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, run string
		require.NoError(f.t, rows.Scan(&key, &run))
		out[key] = run
	}
	require.NoError(f.t, rows.Err())
	return out
}

func requireNoSentinel(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range stateSentinels {
		require.NotContains(t, text, s, "%s carries a state value", what)
	}
}

func requireNoSentinelInJob(t *testing.T, what string, res jobResult) {
	t.Helper()
	requireNoSentinel(t, what+" stdout", res.stdout)
	requireNoSentinel(t, what+" stderr", res.stderr)
	requireNoSentinel(t, what+" step summary", res.summary)
	for k, v := range res.outputs {
		requireNoSentinel(t, what+" output "+k, v)
	}
}

func TestStackResources(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "resources")
	f.eksChanges()

	subnetA := managed("module.vpc", `module.vpc.terraform_data.subnet["a"]`, "terraform_data", builtinProvider)
	subnetD := managed("module.vpc", `module.vpc.terraform_data.subnet["d"]`, "terraform_data", builtinProvider)
	vpc := managed("module.vpc", "module.vpc.terraform_data.vpc", "terraform_data", builtinProvider)
	flowLogs := managed("module.vpc.module.flow_logs", "module.vpc.module.flow_logs.aws_s3_bucket.logs", "aws_s3_bucket", awsProvider)
	vpcID := managed("", "aws_ssm_parameter.vpc_id", "aws_ssm_parameter", awsProvider)
	zones := dataSource("module.vpc", "module.vpc.data.aws_availability_zones.all", "aws_availability_zones")
	remote := dataSource("", "data.terraform_remote_state.network", "terraform_remote_state")
	applied := []stateResource{vpcID, remote, subnetA, subnetD, vpc, zones, flowLogs}
	f.setStack(prodVPC, func(b *faketf.Behavior) { b.StateJSON = stateJSON(t, applied...) })

	cluster := managed("module.eks", "module.eks.terraform_data.cluster", "terraform_data", builtinProvider)
	nodes := managed("module.eks", "module.eks.terraform_data.node_group", "terraform_data", builtinProvider)
	addon := managed("module.eks.module.addons", `module.eks.module.addons.terraform_data.addon["dns.k8s/core"]`, "terraform_data", builtinProvider)
	eksState := []stateResource{cluster, nodes, addon, dataSource("module.eks", "module.eks.data.aws_iam_policy_document.assume", "aws_iam_policy_document")}
	f.setStack(prodEKS, func(b *faketf.Behavior) { b.StateJSON = stateJSON(t, eksState...) })

	reads := &resourceReads{f: f}
	var jobs []jobResult

	a := f.startApply(120, applyCommand)
	head := f.co.head
	assert.Empty(t, f.recordedResources(), "a plan run never records a resource list")
	for _, res := range a.plan.plans {
		jobs = append(jobs, res)
	}
	for _, key := range []string{prodVPC, prodApps} {
		status, body := e.call(http.MethodGet, "/v1/stacks/"+f.stackID(key)+"/resources", nil, withAPIKey)
		require.Equal(t, http.StatusOK, status, body)
		assert.JSONEq(t, `{"total":0,"items":[]}`, body, "%s has no list before any apply", key)
	}

	for _, d := range a.wave0 {
		for _, res := range f.apply(d, nil) {
			requireExit(t, 0, res)
			jobs = append(jobs, res)
		}
		f.complete(d, "success")
	}
	wave1 := f.waitDispatches(a.runID, 1, 1)
	eksApply := f.apply(wave1[0], nil)[prodEKS]
	requireExit(t, 0, eksApply)
	jobs = append(jobs, eksApply)
	f.complete(wave1[0], "success")
	e.waitRun(a.runID, v1.RunApplied)

	afterApply := reads.get(prodVPC)
	assert.Equal(t, a.runID, afterApply.RunID)
	assert.Equal(t, v1.ModeApply, afterApply.Mode)
	assert.Equal(t, head, afterApply.SHA)
	require.NotNil(t, afterApply.ReportedAt)
	require.NotNil(t, afterApply.ChangedAt)
	assert.Equal(t, 5, afterApply.Total, "data sources are left out")
	assert.False(t, afterApply.Truncated)
	items, changes := withoutChanges(afterApply.Items)
	assert.Equal(t, listed(applied...), items)
	for _, it := range items {
		assert.NotContains(t, []string{zones.address, remote.address}, it.Address, "a data source is never listed")
	}
	require.Equal(t, []string{subnetD.address}, keysOf(changes), "only the address the applied plan created is attributed")
	created := changes[subnetD.address]
	assert.Equal(t, v1.ResourceAction("create"), created.Action)
	assert.Equal(t, a.runID, created.RunID)
	assert.Equal(t, head, created.SHA)
	assert.Equal(t, 120, created.PRNumber)
	assert.NotNil(t, created.FinishedAt)

	eks := reads.get(prodEKS)
	assert.Equal(t, a.runID, eks.RunID)
	assert.Equal(t, 3, eks.Total)
	eksItems, eksChanges := withoutChanges(eks.Items)
	assert.Equal(t, listed(eksState...), eksItems)
	require.Equal(t, []string{cluster.address}, keysOf(eksChanges))
	assert.Equal(t, v1.ResourceAction("update"), eksChanges[cluster.address].Action)
	assert.Equal(t, 120, eksChanges[cluster.address].PRNumber)

	staging := reads.get(stagingVPC)
	assert.Equal(t, a.runID, staging.RunID, "an empty state is recorded as an empty list")
	assert.Equal(t, v1.ModeApply, staging.Mode)
	assert.Zero(t, staging.Total)
	assert.Empty(t, staging.Items)
	noop := reads.get(prodApps)
	assert.Empty(t, noop.RunID, "a stack whose apply was a no-op ran no job and has no list")

	merged := f.co.merge(head, "Merge pull request #120 from acme/feature/vpc-subnet")
	f.merge(a.ev, merged, applier)

	stackID := f.stackID(prodVPC)
	e.runJob(runs.JobDrift, runs.DriftJob{RepoID: f.id, StackID: stackID})
	first := f.waitDrift(prodVPC, 0)
	drifted := []stateResource{vpcID, remote, subnetD, vpc.taint(), zones, flowLogs}
	f.setStack(prodVPC, func(b *faketf.Behavior) {
		b.PlanExit, b.ShowJSON, b.StateJSON = 2, fixturePath(t, "drift.json"), stateJSON(t, drifted...)
	})
	res := f.dispatchWorkflow(first, v1.DefaultEnvironment).run("drift", "--stack", prodVPC, "--run-id", first.Inputs["run_id"])
	requireExit(t, 2, res)
	jobs = append(jobs, res)

	afterDrift := reads.get(prodVPC)
	assert.Equal(t, first.Inputs["run_id"], afterDrift.RunID, "a drift run created after the apply's list replaces it")
	assert.Equal(t, v1.ModeDrift, afterDrift.Mode)
	assert.Equal(t, merged, afterDrift.SHA)
	assert.Equal(t, 4, afterDrift.Total)
	items, changes = withoutChanges(afterDrift.Items)
	assert.Equal(t, listed(drifted...), items, "the list is the persisted state, not the plan's refreshed one")
	assert.True(t, items[slices.IndexFunc(items, func(it v1.StackResource) bool { return it.Address == vpc.address })].Tainted)
	assert.Equal(t, map[string]v1.ResourceChange{subnetD.address: created}, changes, "a drift run is never attributed")
	require.NotNil(t, afterDrift.ChangedAt)
	assert.True(t, afterDrift.ChangedAt.After(*afterApply.ChangedAt), "a different list moves changed_at")

	_, err := e.Store.Pool().Exec(t.Context(), `UPDATE runs SET created_at = created_at - interval '2 hours' WHERE id = $1`, first.Inputs["run_id"])
	require.NoError(t, err)
	e.runJob(runs.JobDrift, runs.DriftJob{RepoID: f.id, StackID: stackID})
	stale := f.waitDrift(prodVPC, 1)
	require.NotEqual(t, first.Inputs["run_id"], stale.Inputs["run_id"])

	manualState := []stateResource{vpcID, remote, subnetA, subnetD, vpc.depose(), zones, flowLogs}
	f.setStack(prodVPC, func(b *faketf.Behavior) {
		b.PlanExit, b.ShowJSON, b.StateJSON = 2, fixturePath(t, "drift.json"), stateJSON(t, manualState...)
	})
	f.co.at(f.main)
	outputs, summary := f.prepare(map[string]string{"STACKORDER_SERVER_URL": e.BaseURL, "STACKORDER_API_KEY": suite.apiKey})
	manual := f.cli(outputs, summary, "apply", "--local", "--stack", prodVPC)
	requireExit(t, 0, manual)
	jobs = append(jobs, manual)
	var page v1.Page[v1.Run]
	e.getJSON("/v1/repos/"+f.name+"/runs?mode=apply", &page)
	var manualRun v1.Run
	for _, r := range page.Items {
		if r.Trigger == v1.TriggerManual {
			manualRun = r
		}
	}
	require.NotEmpty(t, manualRun.ID, "the manual apply started a run")
	e.waitRun(manualRun.ID, v1.RunApplied)

	afterManual := reads.get(prodVPC)
	assert.Equal(t, manualRun.ID, afterManual.RunID, "a manual apply --local records its list")
	assert.Equal(t, v1.ModeApply, afterManual.Mode)
	assert.Equal(t, merged, afterManual.SHA)
	assert.Equal(t, 5, afterManual.Total)
	items, changes = withoutChanges(afterManual.Items)
	assert.Equal(t, listed(manualState...), items)
	assert.True(t, items[slices.IndexFunc(items, func(it v1.StackResource) bool { return it.Address == vpc.address })].Deposed)
	assert.Equal(t, []string{subnetD.address, vpc.address}, keysOf(changes))
	assert.Equal(t, created, changes[subnetD.address], "an address the manual apply did not change keeps its pull request's run")
	updated := changes[vpc.address]
	assert.Equal(t, v1.ResourceAction("update"), updated.Action, "the latest applied run that changed an address wins")
	assert.Equal(t, manualRun.ID, updated.RunID)
	assert.Equal(t, merged, updated.SHA)
	assert.Zero(t, updated.PRNumber, "a manual run has no pull request")

	late := []stateResource{vpcID, subnetD}
	f.setStack(prodVPC, func(b *faketf.Behavior) {
		b.PlanExit, b.ShowJSON, b.StateJSON = 0, fixturePath(t, "noop.json"), stateJSON(t, late...)
	})
	res = f.dispatchWorkflow(stale, v1.DefaultEnvironment).run("drift", "--stack", prodVPC, "--run-id", stale.Inputs["run_id"])
	requireExit(t, 0, res)
	jobs = append(jobs, res)
	assert.Equal(t, v1.RunPlanned, e.run(stale.Inputs["run_id"]).Status, "the drift result itself is accepted")
	afterStale := reads.get(prodVPC)
	assert.Equal(t, manualRun.ID, afterStale.RunID, "a drift run created before the apply's list was recorded never replaces it")
	assert.Equal(t, afterManual.Items, afterStale.Items)
	assert.Equal(t, manualRun.ID, f.recordedResources()[prodVPC])

	owner := newBrowser(t, e)
	e.GH.SetOrgMembership(acmeOrg, "dana", gh.MembershipActive, "member")
	resp, body := owner.signIn("dana")
	require.Equal(t, http.StatusFound, resp.status, body)
	resp, body = owner.do(http.MethodGet, "/v1/stacks/"+stackID+"/resources", nil, nil)
	require.Equal(t, http.StatusOK, resp.status, body)
	reads.bodies = append(reads.bodies, body)
	var bySession v1.StackResources
	requireJSON(t, body, &bySession)
	assert.Equal(t, afterStale, bySession, "a session of the repository's organisation reads the same list as an API key")

	const outsiderInst, outsiderOrg, outsiderLogin = 61, "umbrella", "ada"
	addRepo(t, e, outsiderOrg+"/infra", outsiderInst, outsiderOrg)
	e.GH.SetOrgMembership(outsiderOrg, outsiderLogin, gh.MembershipActive, "admin")
	outsider := newBrowser(t, e)
	resp, body = outsider.signIn(outsiderLogin)
	require.Equal(t, http.StatusFound, resp.status, body)
	resp, body = outsider.do(http.MethodGet, "/v1/stacks/"+stackID+"/resources", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.status, "another organisation's stack does not exist for this person: %s", body)
	reads.bodies = append(reads.bodies, body)

	w := f.dispatchWorkflow(stale, v1.DefaultEnvironment)
	claims := w.claims
	claims.Audience = jwt.ClaimStrings{e.BaseURL}
	syncIssuer(e)
	token := e.OIDC.Token(claims)
	status, body := e.call(http.MethodGet, "/v1/stacks/"+stackID+"/resources", nil, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	assert.Equal(t, http.StatusUnauthorized, status, "a runner token does not read resource lists: %s", body)
	assert.NotContains(t, body, `"items"`)
	reads.bodies = append(reads.bodies, body)

	for i, b := range reads.bodies {
		requireNoSentinel(t, "resources response "+strconv.Itoa(i), b)
	}
	for i, res := range jobs {
		requireNoSentinelInJob(t, "job "+strconv.Itoa(i), res)
	}
	var stored []string
	rows, err := e.Store.Pool().Query(t.Context(), `
		SELECT to_jsonb(sr)::text FROM stack_resources sr JOIN stacks s ON s.id = sr.stack_id WHERE s.repo_id = $1
		UNION ALL
		SELECT to_jsonb(rs)::text FROM run_stacks rs JOIN runs r ON r.id = rs.run_id WHERE r.repo_id = $1`, f.id)
	require.NoError(t, err)
	for rows.Next() {
		var row string
		require.NoError(t, rows.Scan(&row))
		stored = append(stored, row)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	require.GreaterOrEqual(t, len(stored), 4, "the stack_resources and run_stacks rows of the repository")
	for _, row := range stored {
		requireNoSentinel(t, "a stack_resources or run_stacks row", row)
	}
	tables, err := e.Store.Pool().Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	require.NoError(t, err)
	var names []string
	for tables.Next() {
		var name string
		require.NoError(t, tables.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, tables.Err())
	tables.Close()
	require.Contains(t, names, "stack_resources")
	for _, name := range names {
		var n int
		require.NoError(t, e.Store.Pool().QueryRow(t.Context(),
			`SELECT count(*) FROM "`+name+`" x WHERE x::text LIKE ANY($1)`,
			[]string{"%" + valuesSentinel + "%", "%" + sensitiveSentinel + "%", "%" + identitySentinel + "%", "%" + outputsSentinel + "%"}).Scan(&n))
		assert.Zero(t, n, "no row of %s carries a state value", name)
	}
	github, err := json.Marshal([]any{e.GH.CheckRuns(f.name), e.GH.Comments(f.name, 120), e.GH.Issues(f.name)})
	require.NoError(t, err)
	requireNoSentinel(t, "what GitHub received", string(github))
}
