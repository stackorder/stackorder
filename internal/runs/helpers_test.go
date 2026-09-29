package runs

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/gh/codeowners"
	"github.com/stackorder/stackorder/internal/store"
)

func TestParseDisplayTitle(t *testing.T) {
	raw := "0b7c6f2e-5f39-4c1e-9a3e-2d8f4c6b1a90"
	id := uuid.MustParse(raw)
	tests := []struct {
		title string
		want  dispatchTitle
		ok    bool
	}{
		{"stackorder apply " + raw + " wave 2", dispatchTitle{Mode: v1.ModeApply, RunID: id, Wave: 2}, true},
		{"stackorder plan " + strings.ToUpper(raw) + " wave 0", dispatchTitle{Mode: v1.ModePlan, RunID: id, Wave: 0}, true},
		{"  stackorder drift " + raw + " wave 0 ", dispatchTitle{Mode: v1.ModeDrift, RunID: id, Wave: 0}, true},
		{"stackorder resolve " + raw + " wave 0", dispatchTitle{}, false},
		{"stackorder apply " + raw + " wave", dispatchTitle{}, false},
		{"stackorder apply not-a-uuid wave 1", dispatchTitle{}, false},
		{"stackorder apply ------------------------------------ wave 1", dispatchTitle{}, false},
		{"stackorder-run", dispatchTitle{}, false},
		{"Stackorder apply " + raw + " wave 1", dispatchTitle{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got, ok := parseDisplayTitle(tt.title)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
	title := formatDisplayTitle(v1.ModeApply, raw, 3)
	got, ok := parseDisplayTitle(title)
	require.True(t, ok, "the server's own title parses")
	assert.Equal(t, 3, got.Wave)
}

func TestJobStackValue(t *testing.T) {
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{"run (stacks/prod/vpc, stacks/prod/vpc, production, 0)", "stacks/prod/vpc", true},
		{"plan (stacks/prod/vpc)", "stacks/prod/vpc", true},
		{"run / apply (stacks/prod/apps:blue, x)", "stacks/prod/apps:blue", true},
		{"run / apply wave 2 stacks/prod/vpc", "stacks/prod/vpc", true},
		{"run / drift stacks/prod/apps:blue", "stacks/prod/apps:blue", true},
		{"plan / plan stacks/dev/db", "stacks/dev/db", true},
		{"run", "", false},
		{"run ()", "", false},
		{"run (unterminated", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := jobStackValue(tt.name)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMatchJobStack(t *testing.T) {
	rows := []store.RunStack{
		{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc"},
		{Key: "stacks/prod/apps:blue", Path: "stacks/prod/apps", Workspace: "blue"},
		{Key: "stacks/prod/apps:green", Path: "stacks/prod/apps", Workspace: "green"},
		{Key: "stacks/dev/db", Path: "stacks/dev/db"},
	}
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{"run (stacks/prod/vpc, stacks/prod/vpc)", "stacks/prod/vpc", true},
		{"run (stacks/prod/apps:blue, …)", "stacks/prod/apps:blue", true},
		{"run (stacks/prod/apps, stacks/prod/apps:blue)", "", false},
		{"run (stacks/dev/db, x)", "stacks/dev/db", true},
		{"run (stacks/none, x)", "", false},
		{"run / apply wave 0 stacks/prod/vpc", "stacks/prod/vpc", true},
		{"run / apply wave 1 stacks/prod/apps:green", "stacks/prod/apps:green", true},
		{"plan / plan stacks/dev/db", "stacks/dev/db", true},
		{"run / apply wave 0 stacks/prod/apps", "", false},
		{"plan / resolve", "", false},
		{"resolve", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := matchJobStack(tt.name, rows)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got.Key)
		})
	}
}

func TestPickDispatch(t *testing.T) {
	prod := store.Dispatch{ID: uuid.New(), Mode: v1.ModeApply, Environment: "production"}
	prod2 := store.Dispatch{ID: uuid.New(), Mode: v1.ModeApply, Environment: "production", Chunk: 1}
	stage := store.Dispatch{ID: uuid.New(), Mode: v1.ModeApply, Environment: "staging"}
	rows := []store.RunStack{
		{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Environment: "production", DispatchID: &prod.ID},
		{Key: "stacks/prod/eks", Path: "stacks/prod/eks", Environment: "production", DispatchID: &prod2.ID},
		{Key: "stacks/staging/vpc", Path: "stacks/staging/vpc", Environment: "staging", DispatchID: &stage.ID},
	}
	candidates := []store.Dispatch{prod, prod2, stage}
	job := func(key string) gh.WorkflowJob { return gh.WorkflowJob{Name: "run / apply (" + key + ", " + key + ")"} }
	tests := []struct {
		name string
		jobs []gh.WorkflowJob
		want uuid.UUID
		ok   bool
	}{
		{"one environment", []gh.WorkflowJob{job("stacks/staging/vpc")}, stage.ID, true},
		{"a job named by the reusable run workflow", []gh.WorkflowJob{{Name: "run / apply wave 0 stacks/staging/vpc"}}, stage.ID, true},
		{"a chunk of an environment", []gh.WorkflowJob{job("stacks/prod/eks")}, prod2.ID, true},
		{"unmatched jobs are ignored", []gh.WorkflowJob{{Name: "setup"}, job("stacks/prod/vpc")}, prod.ID, true},
		{"jobs of two dispatches decide nothing", []gh.WorkflowJob{job("stacks/prod/vpc"), job("stacks/prod/eks")}, uuid.Nil, false},
		{"no jobs yet", nil, uuid.Nil, false},
		{"stacks of no candidate", []gh.WorkflowJob{job("stacks/dev/db")}, uuid.Nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pickDispatch(tt.jobs, rows, candidates)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got.ID)
		})
	}
}

func TestApprovers(t *testing.T) {
	review := func(login, state, sha string) gh.Review {
		return gh.Review{User: gh.User{Login: login}, State: state, CommitID: sha}
	}
	tests := []struct {
		name    string
		reviews []gh.Review
		want    []string
	}{
		{"none", nil, []string{}},
		{"approvals on head count once per user", []gh.Review{
			review("alice", gh.ReviewApproved, "head"), review("alice", gh.ReviewApproved, "head"), review("Bob", "approved", "head"),
		}, []string{"alice", "bob"}},
		{"stale approvals do not count", []gh.Review{review("alice", gh.ReviewApproved, "old")}, []string{}},
		{"later changes requested cancels", []gh.Review{
			review("alice", gh.ReviewApproved, "head"), review("alice", gh.ReviewChangesRequested, "head"),
		}, []string{}},
		{"approval after changes requested counts", []gh.Review{
			review("alice", gh.ReviewChangesRequested, "head"), review("alice", gh.ReviewApproved, "head"),
		}, []string{"alice"}},
		{"changes requested on an old commit does not cancel", []gh.Review{
			review("alice", gh.ReviewApproved, "head"), review("alice", gh.ReviewChangesRequested, "old"),
		}, []string{"alice"}},
		{"comments neither approve nor cancel", []gh.Review{
			review("alice", gh.ReviewApproved, "head"), review("alice", gh.ReviewCommented, "head"),
		}, []string{"alice"}},
		{"dismissed approval does not count", []gh.Review{review("alice", gh.ReviewDismissed, "head")}, []string{}},
		{"the author cannot approve", []gh.Review{review("author", gh.ReviewApproved, "head")}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, approvers(tt.reviews, "head", "Author"))
		})
	}
}

func TestVersionsBehind(t *testing.T) {
	released := []string{"v1.0.0", "v1.1.0", "v1.2.0", "v2.0.0-rc.1", "v2.0.0", "latest", "v1.10.0"}
	tests := []struct {
		ref  string
		want int
		ok   bool
	}{
		{"v1.0.0", 4, true},
		{"1.1.0", 3, true},
		{"v1.2.0", 2, true},
		{"v1.10.0", 1, true},
		{"v2.0.0", 0, true},
		{"v2.0.0-rc.1", 1, true},
		{"main", 0, false},
		{"v1.2", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got, ok := versionsBehind(released, tt.ref)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSemverOrder(t *testing.T) {
	ordered := []string{"v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta", "v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0", "v1.0.1", "v1.1.0", "v2.0.0"}
	for i := 1; i < len(ordered); i++ {
		a, ok := parseSemver(ordered[i-1])
		require.True(t, ok, ordered[i-1])
		b, ok := parseSemver(ordered[i])
		require.True(t, ok, ordered[i])
		assert.Negative(t, a.compare(b), "%s < %s", ordered[i-1], ordered[i])
		assert.Positive(t, b.compare(a), "%s > %s", ordered[i], ordered[i-1])
	}
	for _, bad := range []string{"v01.0.0", "1.0", "1.0.0-", "x.y.z", "1.0.0.0"} {
		_, ok := parseSemver(bad)
		assert.False(t, ok, bad)
	}
	build, ok := parseSemver("v1.2.3+build.5")
	require.True(t, ok)
	plain, _ := parseSemver("1.2.3")
	assert.Zero(t, build.compare(plain), "build metadata is ignored")
}

func TestStackCodeowners(t *testing.T) {
	f, err := codeowners.Parse(strings.NewReader(strings.Join([]string{
		"* @acme/everyone",
		"/stacks/staging/ @acme/staging",
		"/stacks/prod/ @acme/platform-prod",
		"stacks/prod/legacy/** @alice",
		"*.tf @acme/terraform",
		"/stacks/dev/unowned/",
	}, "\n")))
	require.NoError(t, err)
	tests := []struct {
		dir  string
		want []string
	}{
		{"stacks/prod/vpc", []string{"@acme/terraform"}},
		{"stacks/prod/legacy/dns", []string{"@acme/terraform"}},
		{"stacks/other", []string{"@acme/terraform"}},
		{"stacks/dev/unowned", nil},
		{"/stacks/prod/vpc/", []string{"@acme/terraform"}},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			assert.Equal(t, tt.want, stackCodeowners(f, tt.dir))
		})
	}
	assert.Nil(t, stackCodeowners(nil, "stacks/prod/vpc"))
}

func TestStackCodeownersFollowsTheRuleGitHubAppliesToTheStackFiles(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		dir   string
		want  []string
	}{
		{"catch-all", []string{"* @acme/infra"}, "stacks/prod/vpc", []string{"@acme/infra"}},
		{"double star", []string{"** @acme/infra"}, "stacks/prod/vpc", []string{"@acme/infra"}},
		{"extension", []string{"*.tf @acme/terraform"}, "stacks/prod/vpc", []string{"@acme/terraform"}},
		{"anchored file pattern", []string{"/stacks/prod/**/*.tf @acme/prod"}, "stacks/prod/vpc", []string{"@acme/prod"}},
		{"directory", []string{"/stacks/prod/ @acme/platform-prod", "/stacks/staging/ @acme/staging"}, "stacks/staging/vpc", []string{"@acme/staging"}},
		{"later file rule wins", []string{"/stacks/ @acme/a", "*.tf @acme/b"}, "stacks/prod/vpc", []string{"@acme/b"}},
		{"later directory rule wins", []string{"*.tf @acme/b", "/stacks/prod/ @acme/a"}, "stacks/prod/vpc", []string{"@acme/a"}},
		{"other directory", []string{"/stacks/prod/ @acme/platform-prod"}, "stacks/staging/vpc", nil},
		{"explicitly unowned", []string{"* @acme/infra", "/stacks/dev/"}, "stacks/dev/vpc", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := codeowners.Parse(strings.NewReader(strings.Join(tt.lines, "\n")))
			require.NoError(t, err)
			assert.Equal(t, tt.want, stackCodeowners(f, tt.dir))
		})
	}
}

func TestNoopCandidate(t *testing.T) {
	empty := &v1.PlanSummary{}
	changes := &v1.PlanSummary{Adds: 1}
	outputs := &v1.PlanSummary{OutputChanges: 1}
	tests := []struct {
		name       string
		reasons    []v1.Reason
		summary    *v1.PlanSummary
		hasChanges bool
		want       bool
	}{
		{"propagated with an empty plan", []v1.Reason{v1.ReasonDependent}, empty, false, true},
		{"read state with an empty plan", []v1.Reason{v1.ReasonReadsState, v1.ReasonDependent}, empty, false, true},
		{"propagated with changes", []v1.Reason{v1.ReasonDependent}, changes, true, false},
		{"propagated with output changes", []v1.Reason{v1.ReasonDependent}, outputs, false, false},
		{"changed directly", []v1.Reason{v1.ReasonChanged, v1.ReasonDependent}, empty, false, false},
		{"module change", []v1.Reason{v1.ReasonModule}, empty, false, false},
		{"requested", []v1.Reason{v1.ReasonRequested}, empty, false, false},
		{"no reasons", nil, empty, false, false},
		{"exit code says changes", []v1.Reason{v1.ReasonDependent}, nil, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, noopCandidate(tt.reasons, tt.summary, tt.hasChanges))
		})
	}
}

func chainGraph() *v1.Graph {
	stack := func(k string) v1.Stack { return v1.Stack{Key: k, Path: k} }
	return &v1.Graph{
		Repo: "acme/infra",
		Stacks: []v1.Stack{
			stack("vpc"), stack("eks"), stack("apps"), stack("dns"), stack("cdn"), stack("other"),
		},
		Edges: []v1.Edge{
			{From: v1.StackRef("eks"), To: v1.StackRef("vpc"), Type: v1.EdgeDependsOn},
			{From: v1.StackRef("apps"), To: v1.StackRef("eks"), Type: v1.EdgeReadsState, Inferred: true},
			{From: v1.StackRef("cdn"), To: v1.StackRef("dns"), Type: v1.EdgeDependsOn},
			{From: v1.StackRef("other"), To: v1.ModuleRef("acme/infra//modules/x"), Type: v1.EdgeUsesModule},
		},
	}
}

func TestBlockedByFailure(t *testing.T) {
	row := func(key string, wave int, status v1.StackStatus, blockedBy ...string) store.RunStack {
		return store.RunStack{StackID: uuid.NewSHA1(uuid.Nil, []byte(key)), Key: key, Wave: wave, Status: status, BlockedBy: blockedBy}
	}
	rows := make([]store.RunStack, 0, 7)
	rows = append(rows,
		row("vpc", 0, v1.StackFailed),
		row("dns", 0, v1.StackApplied),
		row("eks", 1, v1.StackPlanned),
		row("cdn", 1, v1.StackPlanned),
		row("apps", 2, v1.StackPlanned),
		row("other", 2, v1.StackPlanned),
	)
	got := blockedByFailure(chainGraph(), rows, rows[0])
	keys := make([]string, len(got))
	for i, rs := range got {
		keys[i] = rs.Key
		assert.Equal(t, []string{"vpc"}, rs.BlockedBy)
	}
	assert.Equal(t, []string{"eks", "apps"}, keys, "every transitive dependent over depends_on and reads_state, nothing else")

	rows[2].Status = v1.StackBlocked
	rows[2].BlockedBy = []string{"vpc"}
	rows[4].Status = v1.StackApplied
	assert.Empty(t, blockedByFailure(chainGraph(), rows, rows[0]), "already blocked by the same stack, or finished")

	second := row("eks-sibling", 0, v1.StackFailed)
	g := chainGraph()
	g.Stacks = append(g.Stacks, v1.Stack{Key: "eks-sibling", Path: "eks-sibling"})
	g.Edges = append(g.Edges, v1.Edge{From: v1.StackRef("eks"), To: v1.StackRef("eks-sibling"), Type: v1.EdgeDependsOn})
	more := blockedByFailure(g, append(rows, second), second)
	require.Len(t, more, 1)
	assert.Equal(t, "eks", more[0].Key)
	assert.Equal(t, []string{"eks-sibling", "vpc"}, more[0].BlockedBy, "a blocked stack collects every failed predecessor")

	assert.Nil(t, blockedByFailure(nil, rows, rows[0]))
	lower := row("vpc", 1, v1.StackFailed)
	assert.Empty(t, blockedByFailure(chainGraph(), []store.RunStack{row("eks", 1, v1.StackPlanned)}, lower), "only later waves are blocked")
}

func TestPlanTarget(t *testing.T) {
	rows := func(statuses ...v1.StackStatus) []store.RunStack {
		out := make([]store.RunStack, len(statuses))
		for i, s := range statuses {
			out[i] = store.RunStack{Status: s}
		}
		return out
	}
	assert.Equal(t, v1.RunPlanned, planTarget(nil))
	assert.Equal(t, v1.RunPlanning, planTarget(rows(v1.StackPlanned, v1.StackPending)))
	assert.Equal(t, v1.RunPlanning, planTarget(rows(v1.StackFailed, v1.StackPlanning)), "not failed while a stack still runs")
	assert.Equal(t, v1.RunPlanned, planTarget(rows(v1.StackPlanned, v1.StackNoop, v1.StackSkipped)))
	assert.Equal(t, v1.RunFailed, planTarget(rows(v1.StackPlanned, v1.StackFailed)))
	assert.Equal(t, v1.RunFailed, planTarget(rows(v1.StackUnknown, v1.StackUnconfirmed)))
	assert.Equal(t, v1.RunUnconfirmed, planTarget(rows(v1.StackPlanned, v1.StackUnconfirmed)))
}

func TestDispatchGroups(t *testing.T) {
	keys := []string{"p5", "p1", "s1", "p3", "p2", "p4", "d1"}
	rows := make([]store.RunStack, 0, len(keys))
	for _, k := range keys {
		env := map[byte]string{'p': "production", 's': "staging", 'd': ""}[k[0]]
		rows = append(rows, store.RunStack{Key: k, Environment: env})
	}
	groups := dispatchGroups(v1.ModeApply, rows, 2)
	type g struct {
		env   string
		chunk int
		keys  string
	}
	got := make([]g, 0, len(groups))
	for _, gr := range groups {
		var keys []string
		for _, rs := range gr.rows {
			keys = append(keys, rs.Key)
		}
		got = append(got, g{gr.env, gr.chunk, strings.Join(keys, ",")})
	}
	assert.Equal(t, []g{
		{"default", 0, "d1"},
		{"production", 0, "p1,p2"},
		{"production", 1, "p3,p4"},
		{"production", 2, "p5"},
		{"staging", 0, "s1"},
	}, got)

	plan := dispatchGroups(v1.ModePlan, rows, 2)
	require.Len(t, plan, 1, "plans go out in one dispatch")
	assert.Equal(t, v1.DefaultEnvironment, plan[0].env)
	assert.Len(t, plan[0].rows, len(rows))
}

func TestChunk(t *testing.T) {
	assert.Equal(t, [][]int{{1, 2}, {3, 4}, {5}}, chunk([]int{1, 2, 3, 4, 5}, 2))
	assert.Equal(t, [][]int{{1, 2, 3}}, chunk([]int{1, 2, 3}, 0))
	assert.Nil(t, chunk([]int{}, 3))
}

func TestSubsetWaves(t *testing.T) {
	g := chainGraph()
	all := []string{"vpc", "eks", "apps", "dns", "cdn"}
	assert.Equal(t, map[string]int{"vpc": 0, "eks": 1, "apps": 2, "dns": 0, "cdn": 1}, subsetWaves(g, all, all))
	assert.Equal(t, map[string]int{"vpc": 0, "apps": 1}, subsetWaves(g, all, []string{"vpc", "apps"}),
		"order through an affected stack left out of the subset is kept")
	assert.Equal(t, map[string]int{"vpc": 0, "apps": 0}, subsetWaves(g, []string{"vpc", "apps"}, []string{"vpc", "apps"}),
		"an unaffected stack between them does not order them")
}

func TestAugmentExternal(t *testing.T) {
	g := &v1.Graph{Repo: "acme/network", SHA: "s", Stacks: []v1.Stack{{Key: "stacks/tgw", Path: "stacks/tgw"}}}
	deps := []store.ExternalDependent{
		{Repo: "acme/infra", FromKey: "stacks/vpc:blue", ToKey: "stacks/tgw", Type: v1.EdgeDependsOn},
		{Repo: "acme/infra", FromKey: "stacks/vpc:blue", ToKey: "stacks/gone", Type: v1.EdgeDependsOn},
		{Repo: "acme/apps", FromKey: "stacks/api", ToKey: "stacks/tgw", Type: v1.EdgeReadsState},
	}
	out := augmentExternal(g, deps)
	require.NotSame(t, g, out)
	assert.Len(t, g.Stacks, 1, "the input graph is not modified")
	assert.Equal(t, []v1.Stack{
		{Key: "stacks/tgw", Path: "stacks/tgw"},
		{Key: "acme/infra//stacks/vpc:blue", Path: "stacks/vpc", Workspace: "blue", Repo: "acme/infra", External: true},
		{Key: "acme/apps//stacks/api", Path: "stacks/api", Repo: "acme/apps", External: true},
	}, out.Stacks)
	assert.Equal(t, []v1.Edge{
		{From: v1.StackRef("acme/infra//stacks/vpc:blue"), To: v1.StackRef("stacks/tgw"), Type: v1.EdgeDependsOn},
		{From: v1.StackRef("acme/apps//stacks/api"), To: v1.StackRef("stacks/tgw"), Type: v1.EdgeReadsState},
	}, out.Edges)
	assert.Same(t, g, augmentExternal(g, nil))
}

func TestUnknownCommand(t *testing.T) {
	assert.True(t, unknownCommand("stackorder deploy"))
	assert.True(t, unknownCommand("thanks!\nStackOrder frobnicate stacks/a"))
	assert.False(t, unknownCommand("stackorder"))
	assert.False(t, unknownCommand("stackorder apply"))
	assert.False(t, unknownCommand("> stackorder deploy"))
	assert.False(t, unknownCommand("```\nstackorder deploy\n```"))
	assert.False(t, unknownCommand("    stackorder deploy"))
	assert.False(t, unknownCommand("I like stackorder deploy"))
}

func TestMatrixEntries(t *testing.T) {
	run := store.Run{SHA: "abc", Mode: v1.ModeApply}
	rows := []store.RunStack{
		{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Wave: 1, Environment: "production", PlanRunID: 42, PlanArtifact: "art", PlanOutput: "summary"},
		{Key: "stacks/prod/apps:blue", Path: "stacks/prod/apps", Workspace: "blue", Environment: ""},
	}
	nodes := map[string]v1.Stack{
		"stacks/prod/vpc":       {Tool: v1.ToolTofu, ToolVersion: "1.9.0"},
		"stacks/prod/apps:blue": {Config: &v1.StackConfig{Tool: v1.ToolTerraform}},
	}
	cfg := &v1.RepoConfig{Tool: v1.ToolTofu, ToolVersion: "1.8.0", PlanOutput: v1.PlanOutputFull}
	got := matrixEntries(run, rows, nodes, cfg)
	assert.Equal(t, []v1.MatrixEntry{
		{Stack: "stacks/prod/vpc", Key: "stacks/prod/vpc", Environment: "production", Wave: 1, Tool: v1.ToolTofu,
			ToolVersion: "1.9.0", PlanOutput: "summary", SHA: "abc", PlanRunID: 42, Artifact: "art"},
		{Stack: "stacks/prod/apps", Key: "stacks/prod/apps:blue", Workspace: "blue", Environment: v1.DefaultEnvironment,
			Tool: v1.ToolTerraform, ToolVersion: "1.8.0", PlanOutput: "full", SHA: "abc"},
	}, got)

	run.Mode = v1.ModeDrift
	drift := matrixEntries(run, rows[:1], nodes, cfg)
	assert.Equal(t, v1.DefaultEnvironment, drift[0].Environment, "drift runs under the default environment")
	assert.Zero(t, drift[0].PlanRunID)
	assert.Empty(t, drift[0].Artifact)
}

func TestResultStatusAndDuplicates(t *testing.T) {
	assert.Equal(t, v1.StackPlanned, resultStatus(v1.ModePlan, v1.StackResult{Status: v1.ResultSuccess}))
	assert.Equal(t, v1.StackPlanned, resultStatus(v1.ModeDrift, v1.StackResult{Status: v1.ResultSuccess, HasChanges: true}))
	assert.Equal(t, v1.StackApplied, resultStatus(v1.ModeApply, v1.StackResult{Status: v1.ResultSuccess}))
	assert.Equal(t, v1.StackFailed, resultStatus(v1.ModeApply, v1.StackResult{Status: v1.ResultError}))
	assert.Equal(t, v1.StackFailed, resultStatus(v1.ModePlan, v1.StackResult{Status: v1.ResultFailure}))
	assert.Equal(t, v1.StackUnconfirmed, resultStatus(v1.ModePlan, v1.StackResult{Status: v1.ResultSuccess, Unconfirmed: true}))

	zero := 0
	row := store.RunStack{Status: v1.StackPlanned, ExitCode: &zero, PlanArtifact: "a", Summary: &v1.PlanSummary{Adds: 1}, HasChanges: true, Mode: v1.ModePlan}
	res := v1.StackResult{Status: v1.ResultSuccess, Artifact: "a", Summary: &v1.PlanSummary{Adds: 1}, HasChanges: true}
	assert.True(t, duplicateResult(row, v1.StackPlanned, res))
	res.Artifact = "b"
	assert.False(t, duplicateResult(row, v1.StackPlanned, res))
	res.Artifact, res.Summary = "a", &v1.PlanSummary{Adds: 2}
	assert.False(t, duplicateResult(row, v1.StackPlanned, res))
	assert.False(t, duplicateResult(store.RunStack{Status: v1.StackPending}, v1.StackPlanned, res))
}

func TestCutUTF8(t *testing.T) {
	assert.Equal(t, "abc", cutUTF8("abc", 10))
	assert.Equal(t, "ab", cutUTF8("abc", 2))
	assert.Equal(t, "a", cutUTF8("aé", 2), "never splits a rune")
}

func TestTTLCache(t *testing.T) {
	c := newTTLCache()
	base := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	c.put("k", "v", base)
	v, ok := c.get("k", time.Minute, base.Add(59*time.Second))
	assert.True(t, ok)
	assert.Equal(t, "v", v)
	_, ok = c.get("k", time.Minute, base.Add(time.Minute))
	assert.False(t, ok, "expired")
	_, ok = c.get("k", time.Minute, base.Add(-time.Second))
	assert.False(t, ok, "a clock that went back invalidates")
	_, ok = c.get("missing", time.Minute, base)
	assert.False(t, ok)
}
