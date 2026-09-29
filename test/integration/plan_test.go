//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/scan"
)

var planChanges = map[string]v1.PlanSummary{
	prodVPC:    {Adds: 1, Added: []string{`module.vpc.terraform_data.subnet["d"]`}},
	stagingVPC: {Adds: 1, Added: []string{`module.vpc.terraform_data.subnet["d"]`}},
}

func expectedResolution(t *testing.T, f *fixture, head string) *v1.ResolveResponse {
	t.Helper()
	ctx := context.Background()
	f.co.at(head)
	cfg, _, err := config.Load(f.co.dir)
	require.NoError(t, err)
	g, err := scan.Scan(ctx, f.co.dir, scan.Options{Repo: f.name, SHA: head, Config: cfg})
	require.NoError(t, err)
	changed, err := scan.ChangedPaths(ctx, f.co.dir, f.co.base, head)
	require.NoError(t, err)
	resp, err := graph.Resolve(g, graph.Input{ChangedPaths: changed, Config: cfg})
	require.NoError(t, err)
	return resp
}

func wavesByKey(resp *v1.ResolveResponse) map[string]int {
	out := map[string]int{}
	for _, a := range resp.Affected {
		out[a.Key] = a.Wave
	}
	return out
}

func requireExampleResolution(t *testing.T, want *v1.ResolveResponse) {
	t.Helper()
	require.Equal(t, map[string]int{prodVPC: 0, stagingVPC: 0, prodEKS: 1, stagingApps: 1, prodApps: 1}, wavesByKey(want),
		"a modules/vpc change affects both VPC stacks and, one wave later, their three dependents; "+
			"stacks/prod/apps reads only the state of stacks/prod/vpc")
	require.Len(t, want.Waves, 2)
}

func TestPullRequestPlanFlow(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "plan-flow")
	ev := f.openPR(1, f.co.head, "feature/vpc-subnet")
	want := expectedResolution(t, f, f.co.head)
	requireExampleResolution(t, want)

	p := f.plan(ev)

	out := p.resolve.outputs
	assert.Equal(t, "false", out["unconfirmed"])
	assert.Equal(t, strconv.Itoa(len(want.Affected)), out["count"])
	var waves [][]string
	require.NoError(t, json.Unmarshal([]byte(out["waves"]), &waves))
	assert.Equal(t, want.Waves, waves, "the server orders the waves as internal/graph does")
	var affected []string
	require.NoError(t, json.Unmarshal([]byte(out["affected"]), &affected))
	byWave := wavesByKey(want)
	assert.True(t, slices.IsSortedFunc(affected, func(a, b string) int {
		if byWave[a] != byWave[b] {
			return byWave[a] - byWave[b]
		}
		return strings.Compare(a, b)
	}), "affected is ordered by wave, then key: %v", affected)
	assert.ElementsMatch(t, keysOf(byWave), affected)

	envs := map[string]string{prodVPC: "production", prodEKS: "production", prodApps: "production", stagingVPC: "staging", stagingApps: "staging"}
	require.Len(t, p.matrix.Include, len(want.Affected))
	for _, entry := range p.matrix.Include {
		assert.Equal(t, byWave[entry.Key], entry.Wave, entry.Key)
		assert.Equal(t, entry.Key, entry.Stack, "no workspaces in the example")
		assert.Equal(t, envs[entry.Key], entry.Environment, entry.Key)
		assert.Equal(t, v1.ToolTerraform, entry.Tool, entry.Key)
		assert.Equal(t, f.co.head, entry.SHA, entry.Key)
	}
	assert.Contains(t, p.resolve.summary, prodVPC, "the step summary lists the affected stacks")

	for key, r := range p.plans {
		_, changes := planChanges[key]
		assert.Equal(t, strconv.FormatBool(changes), r.outputs["has-changes"], key)
		assert.Equal(t, v1.PlanArtifactName(key, f.co.head), r.outputs["artifact"], key)
		assert.Equal(t, "false", r.outputs["unconfirmed"], key)
		assert.FileExists(t, r.outputs["plan-file"], key)
	}

	run := e.run(p.runID)
	assert.Equal(t, v1.RunPlanned, run.Status)
	assert.Equal(t, v1.ModePlan, run.Mode)
	assert.Equal(t, v1.TriggerPullRequest, run.Trigger)
	assert.Equal(t, f.name, run.Repo)
	assert.Equal(t, f.co.head, run.SHA)
	assert.Equal(t, f.co.base, run.BaseSHA)
	assert.Equal(t, 1, run.PRNumber)
	assert.Equal(t, author, run.RequestedBy)
	assert.Equal(t, len(want.Waves), run.Waves)
	stacks := runStacks(run)
	require.ElementsMatch(t, keysOf(byWave), keysOf(stacks))
	for key, rs := range stacks {
		assert.Equal(t, v1.StackPlanned, rs.Status, key)
		assert.Equal(t, byWave[key], rs.Wave, key)
		assert.Equal(t, envs[key], rs.Environment, key)
		assert.Equal(t, v1.PlanArtifactName(key, f.co.head), rs.PlanArtifact, key)
		require.NotNil(t, rs.Summary, key)
		assert.Equal(t, planChanges[key], *rs.Summary, key)
	}
	assert.Equal(t, []v1.Reason{v1.ReasonModule}, stacks[prodVPC].Reasons)
	assert.Equal(t, []v1.Reason{v1.ReasonDependent}, stacks[prodEKS].Reasons)
	assert.Equal(t, string(v1.PlanOutputSummary), stacks[stagingApps].PlanOutput, "stacks/staging/apps sets plan_output: summary")

	checks := f.checks(f.co.head)
	resolve := f.check(f.co.head, report.CheckResolve)
	assert.Equal(t, report.ConclusionSuccess, resolve.Conclusion)
	assert.Equal(t, fmt.Sprintf("%d stacks affected in %d waves", len(want.Affected), len(want.Waves)), resolve.Output.Title)
	rollup := f.check(f.co.head, report.CheckPlan)
	assert.Equal(t, report.ConclusionSuccess, rollup.Conclusion)
	assert.Contains(t, rollup.Output.Text, "| Wave | Stack |", "the roll-up lists the stacks")
	for key := range byWave {
		c := f.check(f.co.head, report.StackCheckName(report.CheckPlan, key))
		assert.Equal(t, report.ConclusionSuccess, c.Conclusion, key)
		wantTitle := "No changes"
		if _, ok := planChanges[key]; ok {
			wantTitle = "1 to add, 0 to change, 0 to destroy"
		}
		assert.Equal(t, wantTitle, c.Output.Title, key)
		assert.Equal(t, e.BaseURL+"/runs/"+p.runID, c.DetailsURL, key)
	}
	assert.NotContains(t, checks, report.CheckApply, "the apply check waits for an apply")
	assert.Len(t, checks, 2+len(byWave), "resolve, the plan roll-up and one check per stack: %v", checkNames(checks))

	sticky := f.sticky(1)
	assert.Contains(t, sticky.Body, "### Stackorder: planned")
	assert.Contains(t, sticky.Body, "| Wave | Stack | Environment | Add | Change | Destroy | Replace | Status | Job |")
	for key := range byWave {
		adds := "0"
		if _, ok := planChanges[key]; ok {
			adds = "1"
		}
		row := fmt.Sprintf("| %d | `%s` | %s | %s | 0 | 0 | 0 | planned |", byWave[key], key, envs[key], adds)
		assert.Contains(t, sticky.Body, row)
	}
	assert.Contains(t, sticky.Body, "<details><summary><code>stacks/prod/vpc</code>")
	assert.Contains(t, sticky.Body, `# module.vpc.terraform_data.subnet["d"] will be created`, "the plan text of a stack with changes is shown")
	assert.NotContains(t, sticky.Body, "<code>stacks/prod/eks</code>", "stacks without changes get no details section")

	_, err := os.Stat(p.plans[prodVPC].outputs["plan-file"])
	require.NoError(t, err, "the plan file stays in STACKORDER_PLAN_DIR for the apply")
}

func TestSupersedingPush(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "supersede")
	ev := f.openPR(2, f.co.head, "feature/vpc-subnet")
	first := f.plan(ev)
	old := f.co.head
	require.Equal(t, v1.RunPlanned, e.run(first.runID).Status)

	next := f.co.edit("modules/vpc/variables.tf", "fix(vpc): describe the zones", func(s string) string {
		return s + "\n# Zones a to d each get one subnet.\n"
	})
	ev2 := f.push(ev, next)

	run := e.run(first.runID)
	assert.Equal(t, v1.RunSuperseded, run.Status, "a new head commit supersedes the plan run of the old one")
	checks := f.checks(old)
	for _, name := range append([]string{report.CheckPlan}, planCheckNames(run)...) {
		c, ok := checks[name]
		require.True(t, ok, name)
		assert.Equal(t, report.ConclusionNeutral, c.Conclusion, name)
		assert.Equal(t, "Superseded by "+next[:7], c.Output.Title, name)
	}
	assert.Equal(t, report.ConclusionSuccess, checks[report.CheckResolve].Conclusion, "the resolve check keeps its verdict")

	late := first.w.run("plan", "--stack", prodVPC, "--run-id", first.runID)
	requireExit(t, 3, late)
	assert.Contains(t, late.stderr, "superseded")

	second := f.plan(ev2)
	assert.NotEqual(t, first.runID, second.runID)
	assert.Equal(t, v1.RunPlanned, e.run(second.runID).Status)
	assert.Equal(t, report.ConclusionSuccess, f.check(next, report.CheckPlan).Conclusion)
	sticky := f.sticky(2)
	assert.Contains(t, sticky.Body, "### Stackorder: planned")
	assert.Contains(t, sticky.Body, next[:7], "the sticky comment follows the new head")
	assert.NotContains(t, sticky.Body, old[:7])
}

func planCheckNames(run v1.Run) []string {
	out := make([]string, 0, len(run.Stacks))
	for _, rs := range run.Stacks {
		out = append(out, report.StackCheckName(report.CheckPlan, rs.Key))
	}
	return out
}

func (f *fixture) graphCounts() (graphs, edges int) {
	f.t.Helper()
	ctx := f.t.Context()
	require.NoError(f.t, f.e.Store.Pool().QueryRow(ctx, `SELECT count(*) FROM graphs WHERE repo_id = $1`, f.id).Scan(&graphs))
	require.NoError(f.t, f.e.Store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM edges e JOIN graphs g ON g.id = e.graph_id WHERE g.repo_id = $1`, f.id).Scan(&edges))
	return graphs, edges
}

func (f *fixture) runGraph(runID string) string {
	f.t.Helper()
	var id string
	require.NoError(f.t, f.e.Store.Pool().QueryRow(f.t.Context(), `SELECT graph_id::text FROM runs WHERE id = $1`, runID).Scan(&id))
	return id
}

func TestCacheHitOnSameTree(t *testing.T) {
	e := shared(t)
	f := newFixture(t, e, "cache-hit")
	ev := f.openPR(3, f.co.head, "feature/vpc-subnet")
	first := f.plan(ev)
	graphs, edges := f.graphCounts()
	require.Equal(t, 1, graphs)
	require.NotZero(t, edges)

	retry := f.co.emptyCommit("chore: retrigger the plan")
	ev2 := f.push(ev, retry)
	w := f.planWorkflow(ev2)
	res := w.run("resolve", "--format", "json")
	requireExit(t, 0, res)
	var resp v1.ResolveResponse
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &resp))
	assert.True(t, resp.Cached, "the same tree for a new commit is a cache hit")
	assert.NotEqual(t, first.runID, resp.RunID, "the new commit gets its own plan run")
	var firstWaves [][]string
	require.NoError(t, json.Unmarshal([]byte(first.resolve.outputs["waves"]), &firstWaves))
	assert.Equal(t, firstWaves, resp.Waves)
	assert.Equal(t, first.resolve.outputs["matrix"], strings.ReplaceAll(res.outputs["matrix"], retry, f.co.head),
		"the matrix is the same but for the commit")

	g2, e2 := f.graphCounts()
	assert.Equal(t, graphs, g2, "no graph is saved for a cache hit")
	assert.Equal(t, edges, e2, "no edge is saved for a cache hit")
	assert.Equal(t, f.runGraph(first.runID), f.runGraph(resp.RunID), "the new run uses the stored graph")
	resolve := f.check(retry, report.CheckResolve)
	assert.Equal(t, report.ConclusionSuccess, resolve.Conclusion)
	assert.Contains(t, resolve.Output.Summary, "The scanned tree matches a stored graph")
}
