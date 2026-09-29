package runs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/graph"
)

func defaulted(c v1.RepoConfig) *v1.RepoConfig {
	config.ApplyDefaults(&c)
	return &c
}

func TestConfigDiff(t *testing.T) {
	base := defaulted(v1.RepoConfig{Environments: map[string]string{"stacks/prod/": "production"}})
	assert.Empty(t, configDiff(base, defaulted(v1.RepoConfig{Version: 1, Environments: map[string]string{"stacks/prod/": "production"}})),
		"spelling out the defaults changes nothing")

	pr := defaulted(v1.RepoConfig{
		Stacks:       v1.StacksConfig{Discover: []string{"stacks/staging/**"}, Ignore: []string{"**"}},
		Environments: map[string]string{"stacks/prod/": "staging"},
		Tool:         v1.ToolTofu,
	})
	f := false
	pr.Propagate.Dependents = &f
	assert.Equal(t, []string{"environments", "propagate.dependents", "stacks.discover", "stacks.ignore", "tool"}, configDiff(base, pr))
}

func TestDiscovers(t *testing.T) {
	cfg := defaulted(v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"stacks/**"}, Include: []string{"./legacy/dns/"}}})
	assert.True(t, discovers(cfg, "stacks/prod/vpc"))
	assert.True(t, discovers(cfg, "legacy/dns"), "stacks.include")
	assert.False(t, discovers(cfg, "other/vpc"))
	cfg.Modules.Paths = []string{"stacks/shared"}
	assert.False(t, discovers(cfg, "stacks/shared/net"), "directories under modules.paths are modules")
}

func narrowingGraphs() (uploaded, baseline *v1.Graph) {
	vpcMod := v1.Module{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc"}
	dnsMod := v1.Module{Key: "acme/infra//modules/dns", Kind: v1.ModuleLocal, Path: "modules/dns"}
	uses := func(from v1.NodeRef, m v1.Module) v1.Edge {
		return v1.Edge{From: from, To: v1.ModuleRef(m.Key), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": ""}}
	}
	uploaded = &v1.Graph{
		Repo: "acme/infra", SHA: "head",
		Stacks:  []v1.Stack{{Key: "stacks/staging/vpc", Path: "stacks/staging/vpc"}},
		Modules: []v1.Module{vpcMod},
		Edges:   []v1.Edge{uses(v1.StackRef("stacks/staging/vpc"), vpcMod)},
	}
	baseline = &v1.Graph{
		Repo: "acme/infra", SHA: "main",
		Stacks: []v1.Stack{
			{Key: "stacks/staging/vpc", Path: "stacks/staging/vpc"},
			{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc"},
			{Key: "stacks/prod/eks", Path: "stacks/prod/eks"},
			{Key: "stacks/prod/dns", Path: "stacks/prod/dns"},
			{Key: "acme/other//stacks/x", Repo: "acme/other", External: true},
		},
		Modules: []v1.Module{vpcMod, dnsMod},
		Edges: []v1.Edge{
			uses(v1.StackRef("stacks/staging/vpc"), vpcMod),
			uses(v1.StackRef("stacks/prod/vpc"), vpcMod),
			uses(v1.StackRef("stacks/prod/dns"), dnsMod),
			{From: v1.StackRef("stacks/prod/eks"), To: v1.StackRef("stacks/prod/vpc"), Type: v1.EdgeDependsOn},
			{From: v1.StackRef("stacks/prod/eks"), To: v1.StackRef("stacks/prod/gone"), Type: v1.EdgeDependsOn},
		},
	}
	return uploaded, baseline
}

func TestWithDefaultBranchStacks(t *testing.T) {
	uploaded, baseline := narrowingGraphs()
	base := defaulted(v1.RepoConfig{})
	pr := defaulted(v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"stacks/staging/**"}}})
	in := graph.Input{ChangedPaths: []string{"modules/vpc/main.tf"}, Config: pr}

	assert.Nil(t, withDefaultBranchStacks(uploaded, nil, in, pr, base), "no baseline graph, nothing to add")
	assert.Nil(t, withDefaultBranchStacks(uploaded, baseline, in, base, base), "nothing the pull request's configuration does not discover")

	got := withDefaultBranchStacks(uploaded, baseline, in, pr, base)
	require.NotNil(t, got)
	var keys []string
	for _, st := range got.Stacks {
		keys = append(keys, st.Key)
	}
	assert.Equal(t, []string{"stacks/staging/vpc", "stacks/prod/vpc", "stacks/prod/eks"}, keys,
		"only the stacks affected under the default-branch configuration join; stacks/prod/dns is not")
	assert.Len(t, got.Modules, 1, "no module that only unaffected stacks use")
	assert.Contains(t, got.Edges, v1.Edge{From: v1.StackRef("stacks/prod/eks"), To: v1.StackRef("stacks/prod/vpc"), Type: v1.EdgeDependsOn})
	for _, e := range got.Edges {
		assert.NotEqual(t, "stacks/prod/gone", e.To.Key, "edges to nodes outside the graph are dropped")
	}
	assert.Len(t, uploaded.Stacks, 1, "the uploaded graph is left alone")

	deleted := defaulted(v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"stacks/**"}, Ignore: []string{"**"}}})
	assert.Nil(t, withDefaultBranchStacks(uploaded, baseline, in, deleted, base),
		"a stack the pull request's configuration still discovers was removed by the pull request, not hidden")
}

func TestUnionResolution(t *testing.T) {
	uploaded, baseline := narrowingGraphs()
	base := defaulted(v1.RepoConfig{})
	pr := defaulted(v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"stacks/staging/**"}}})
	in := graph.Input{ChangedPaths: []string{"modules/vpc/main.tf"}, Config: pr}
	prResp, err := graph.Resolve(uploaded, in)
	require.NoError(t, err)
	g := withDefaultBranchStacks(uploaded, baseline, in, pr, base)
	in.Config = base
	baseResp, baseErr := graph.Resolve(g, in)

	resp, err := unionResolution(g, prResp, baseResp, baseErr, []string{"stacks.discover"})
	require.NoError(t, err)
	waves := map[string]int{}
	for _, a := range resp.Affected {
		waves[a.Key] = a.Wave
	}
	assert.Equal(t, map[string]int{"stacks/staging/vpc": 0, "stacks/prod/vpc": 0, "stacks/prod/eks": 1}, waves)
	assert.Equal(t, [][]string{{"stacks/prod/vpc", "stacks/staging/vpc"}, {"stacks/prod/eks"}}, resp.Waves)
	assert.Len(t, resp.Matrix.Include, 3)
	assert.Contains(t, resp.Warnings, "the stackorder.yaml of this pull request differs from the default branch's in stacks.discover; "+
		"under the default branch's configuration stacks/prod/eks, stacks/prod/vpc are affected as well and planned too")

	same, err := unionResolution(g, baseResp, baseResp, nil, []string{"tool"})
	require.NoError(t, err)
	assert.Same(t, baseResp, same, "nothing added, nothing changed")

	cyclic := *g
	cyclic.Edges = append(cyclic.Edges, v1.Edge{From: v1.StackRef("stacks/prod/vpc"), To: v1.StackRef("stacks/staging/vpc"), Type: v1.EdgeDependsOn},
		v1.Edge{From: v1.StackRef("stacks/staging/vpc"), To: v1.StackRef("stacks/prod/vpc"), Type: v1.EdgeDependsOn})
	resp, err = unionResolution(&cyclic, prResp, baseResp, nil, []string{"stacks.discover"})
	require.ErrorIs(t, err, graph.ErrCycle, "a cycle through the added stacks fails the resolution")
	assert.NotEmpty(t, resp.Cycles)
	assert.Empty(t, resp.Matrix.Include)
}
