package graph

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

func exampleStack(key string, wave int, env string, via []string, rs ...v1.Reason) v1.AffectedStack {
	return v1.AffectedStack{
		Key:         key,
		Path:        key,
		Wave:        wave,
		Reasons:     rs,
		Environment: env,
		Tool:        v1.ToolTofu,
		ToolVersion: "1.9.0",
		PlanOutput:  "full",
		Via:         via,
	}
}

func TestResolveDesignExample(t *testing.T) {
	gitApps := "acme/modules//app@v2.1.0"
	withGit := exampleGraph().git(gitApps, "v2.1.0").uses(prodApps, gitApps).uses(stagingApps, gitApps)
	withGit.g.Modules[len(withGit.g.Modules)-1].Path = "app"
	registryIAM := "registry:terraform-aws-modules/iam/aws@5.1"
	withRegistry := exampleGraph().
		module(v1.Module{Key: registryIAM, Kind: v1.ModuleRegistry, Source: "terraform-aws-modules/iam/aws", Path: "vendor/iam", Ref: "5.1"}).
		uses(prodApps, registryIAM)

	tests := []struct {
		name         string
		graph        *v1.Graph
		paths        []string
		wantAffected []v1.AffectedStack
		wantWaves    [][]string
		wantWarnings []string
	}{
		{
			name:  "modules/vpc change reaches both vpcs, their dependents and a remote state reader",
			graph: exampleGraph().build(),
			paths: []string{"modules/vpc/main.tf", "modules/vpc/variables.tf"},
			wantAffected: []v1.AffectedStack{
				exampleStack(prodVPC, 0, "production", []string{vpcModule}, v1.ReasonModule),
				exampleStack(stagingVPC, 0, "staging", []string{vpcModule}, v1.ReasonModule),
				exampleStack(prodEKS, 1, "production", []string{prodVPC}, v1.ReasonDependent),
				exampleStack(stagingApps, 1, "staging", []string{stagingVPC}, v1.ReasonDependent),
				exampleStack(prodApps, 2, "production", []string{prodEKS}, v1.ReasonReadsState),
			},
			wantWaves: [][]string{{prodVPC, stagingVPC}, {prodEKS, stagingApps}, {prodApps}},
		},
		{
			name:  "single stack change reaches only its dependents",
			graph: exampleGraph().build(),
			paths: []string{"stacks/prod/eks/main.tf"},
			wantAffected: []v1.AffectedStack{
				exampleStack(prodEKS, 0, "production", nil, v1.ReasonChanged),
				exampleStack(prodApps, 1, "production", []string{prodEKS}, v1.ReasonReadsState),
			},
			wantWaves: [][]string{{prodEKS}, {prodApps}},
		},
		{
			name:  "leaf stack change",
			graph: exampleGraph().build(),
			paths: []string{"stacks/staging/apps/main.tf"},
			wantAffected: []v1.AffectedStack{
				exampleStack(stagingApps, 0, "staging", nil, v1.ReasonChanged),
			},
			wantWaves: [][]string{{stagingApps}},
		},
		{
			name:  "change and module change together",
			graph: exampleGraph().build(),
			paths: []string{"modules/eks/main.tf", "stacks/prod/eks/outputs.tf", "modules/vpc/main.tf"},
			wantAffected: []v1.AffectedStack{
				exampleStack(prodVPC, 0, "production", []string{vpcModule}, v1.ReasonModule),
				exampleStack(stagingVPC, 0, "staging", []string{vpcModule}, v1.ReasonModule),
				exampleStack(prodEKS, 1, "production", []string{eksModule, prodVPC}, v1.ReasonChanged, v1.ReasonModule, v1.ReasonDependent),
				exampleStack(stagingApps, 1, "staging", []string{stagingVPC}, v1.ReasonDependent),
				exampleStack(prodApps, 2, "production", []string{prodEKS}, v1.ReasonReadsState),
			},
			wantWaves: [][]string{{prodVPC, stagingVPC}, {prodEKS, stagingApps}, {prodApps}},
		},
		{
			name:         "docs-only change is ignored",
			graph:        exampleGraph().build(),
			paths:        []string{"README.md", "docs/guide.md", "stacks/prod/vpc/README.md", "modules/vpc/README", "modules/eks/CHANGES.md"},
			wantAffected: []v1.AffectedStack{},
			wantWaves:    [][]string{},
		},
		{
			name:  "git module consumer bump is a change to the consumer",
			graph: withGit.build(),
			paths: []string{"stacks/prod/apps/main.tf"},
			wantAffected: []v1.AffectedStack{
				exampleStack(prodApps, 0, "production", nil, v1.ReasonChanged),
			},
			wantWaves: [][]string{{prodApps}},
		},
		{
			name:         "git module never matches a path in this repository",
			graph:        withGit.build(),
			paths:        []string{"app/main.tf"},
			wantAffected: []v1.AffectedStack{},
			wantWaves:    [][]string{},
			wantWarnings: []string{"changed path app/main.tf is inside no stack or module"},
		},
		{
			name:         "registry module never matches a path in this repository",
			graph:        withRegistry.build(),
			paths:        []string{"vendor/iam/main.tf"},
			wantAffected: []v1.AffectedStack{},
			wantWaves:    [][]string{},
			wantWarnings: []string{"changed path vendor/iam/main.tf is inside no stack or module"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(tt.graph, Input{ChangedPaths: tt.paths, Config: exampleConfig()})
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.wantAffected, resp.Affected), "affected")
			require.Empty(t, cmp.Diff(tt.wantWaves, resp.Waves), "waves")
			require.Empty(t, cmp.Diff(tt.wantWarnings, resp.Warnings), "warnings")
			require.Empty(t, resp.Cycles)
			require.Empty(t, resp.External)
			require.Len(t, resp.Matrix.Include, len(tt.wantAffected))
			for i, e := range resp.Matrix.Include {
				require.Equal(t, tt.wantAffected[i].Key, e.Key)
				require.Equal(t, "abc123", e.SHA)
				require.Equal(t, tt.wantAffected[i].Wave, e.Wave)
			}
		})
	}
}

func TestResolveTopologies(t *testing.T) {
	tests := []struct {
		name        string
		graph       *v1.Graph
		paths       []string
		wantWaves   [][]string
		wantReasons map[string][]v1.Reason
		wantVia     map[string][]string
	}{
		{
			name:      "chain from the root",
			graph:     newGraph().stacks("s/a", "s/b", "s/c", "s/d").dep("s/b", "s/a").dep("s/c", "s/b").dep("s/d", "s/c").build(),
			paths:     []string{"s/a/main.tf"},
			wantWaves: [][]string{{"s/a"}, {"s/b"}, {"s/c"}, {"s/d"}},
			wantReasons: map[string][]v1.Reason{
				"s/a": reasons(v1.ReasonChanged),
				"s/b": reasons(v1.ReasonDependent),
				"s/c": reasons(v1.ReasonDependent),
				"s/d": reasons(v1.ReasonDependent),
			},
			wantVia: map[string][]string{"s/a": nil, "s/b": {"s/a"}, "s/c": {"s/b"}, "s/d": {"s/c"}},
		},
		{
			name:      "chain from the middle leaves upstream alone",
			graph:     newGraph().stacks("s/a", "s/b", "s/c", "s/d").dep("s/b", "s/a").dep("s/c", "s/b").dep("s/d", "s/c").build(),
			paths:     []string{"s/b/main.tf"},
			wantWaves: [][]string{{"s/b"}, {"s/c"}, {"s/d"}},
			wantReasons: map[string][]v1.Reason{
				"s/b": reasons(v1.ReasonChanged),
				"s/c": reasons(v1.ReasonDependent),
				"s/d": reasons(v1.ReasonDependent),
			},
			wantVia: map[string][]string{"s/b": nil, "s/c": {"s/b"}, "s/d": {"s/c"}},
		},
		{
			name:      "two changes on one chain",
			graph:     newGraph().stacks("s/a", "s/b", "s/c").dep("s/b", "s/a").dep("s/c", "s/b").build(),
			paths:     []string{"s/a/main.tf", "s/c/main.tf"},
			wantWaves: [][]string{{"s/a"}, {"s/b"}, {"s/c"}},
			wantReasons: map[string][]v1.Reason{
				"s/a": reasons(v1.ReasonChanged),
				"s/b": reasons(v1.ReasonDependent),
				"s/c": reasons(v1.ReasonChanged, v1.ReasonDependent),
			},
			wantVia: map[string][]string{"s/a": nil, "s/b": {"s/a"}, "s/c": {"s/b"}},
		},
		{
			name: "diamond",
			graph: newGraph().stacks("s/a", "s/b", "s/c", "s/d").
				dep("s/b", "s/a").reads("s/c", "s/a").dep("s/d", "s/b").dep("s/d", "s/c").build(),
			paths:     []string{"s/a/main.tf"},
			wantWaves: [][]string{{"s/a"}, {"s/b", "s/c"}, {"s/d"}},
			wantReasons: map[string][]v1.Reason{
				"s/a": reasons(v1.ReasonChanged),
				"s/b": reasons(v1.ReasonDependent),
				"s/c": reasons(v1.ReasonReadsState),
				"s/d": reasons(v1.ReasonDependent),
			},
			wantVia: map[string][]string{"s/a": nil, "s/b": {"s/a"}, "s/c": {"s/a"}, "s/d": {"s/b", "s/c"}},
		},
		{
			name: "both edge types into one stack",
			graph: newGraph().stacks("s/a", "s/b", "s/c").
				dep("s/c", "s/a").reads("s/c", "s/b").build(),
			paths:     []string{"s/a/main.tf", "s/b/main.tf"},
			wantWaves: [][]string{{"s/a", "s/b"}, {"s/c"}},
			wantReasons: map[string][]v1.Reason{
				"s/a": reasons(v1.ReasonChanged),
				"s/b": reasons(v1.ReasonChanged),
				"s/c": reasons(v1.ReasonReadsState, v1.ReasonDependent),
			},
			wantVia: map[string][]string{"s/a": nil, "s/b": nil, "s/c": {"s/a", "s/b"}},
		},
		{
			name: "longest path places a stack after its deepest dependency",
			graph: newGraph().stacks("s/a", "s/b", "s/c", "s/d").
				dep("s/d", "s/a").dep("s/b", "s/a").dep("s/c", "s/b").dep("s/d", "s/c").build(),
			paths:     []string{"s/a/main.tf"},
			wantWaves: [][]string{{"s/a"}, {"s/b"}, {"s/c"}, {"s/d"}},
			wantReasons: map[string][]v1.Reason{
				"s/a": reasons(v1.ReasonChanged),
				"s/b": reasons(v1.ReasonDependent),
				"s/c": reasons(v1.ReasonDependent),
				"s/d": reasons(v1.ReasonDependent),
			},
			wantVia: map[string][]string{"s/a": nil, "s/b": {"s/a"}, "s/c": {"s/b"}, "s/d": {"s/a", "s/c"}},
		},
		{
			name: "disconnected components",
			graph: newGraph().stacks("s/a", "s/b", "t/x", "t/y", "t/z", "u/solo").
				dep("s/b", "s/a").dep("t/y", "t/x").dep("t/z", "t/y").build(),
			paths:     []string{"s/a/main.tf", "t/x/main.tf", "u/solo/main.tf"},
			wantWaves: [][]string{{"s/a", "t/x", "u/solo"}, {"s/b", "t/y"}, {"t/z"}},
			wantReasons: map[string][]v1.Reason{
				"s/a":    reasons(v1.ReasonChanged),
				"s/b":    reasons(v1.ReasonDependent),
				"t/x":    reasons(v1.ReasonChanged),
				"t/y":    reasons(v1.ReasonDependent),
				"t/z":    reasons(v1.ReasonDependent),
				"u/solo": reasons(v1.ReasonChanged),
			},
			wantVia: map[string][]string{"s/a": nil, "s/b": {"s/a"}, "t/x": nil, "t/y": {"t/x"}, "t/z": {"t/y"}, "u/solo": nil},
		},
		{
			name: "dependency edges to unaffected stacks are dropped",
			graph: newGraph().stacks("s/base", "s/a", "s/b").
				dep("s/a", "s/base").dep("s/b", "s/a").build(),
			paths:     []string{"s/a/main.tf"},
			wantWaves: [][]string{{"s/a"}, {"s/b"}},
			wantReasons: map[string][]v1.Reason{
				"s/a": reasons(v1.ReasonChanged),
				"s/b": reasons(v1.ReasonDependent),
			},
			wantVia: map[string][]string{"s/a": nil, "s/b": {"s/a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(tt.graph, Input{ChangedPaths: tt.paths})
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.wantWaves, resp.Waves), "waves")
			require.Empty(t, cmp.Diff(tt.wantReasons, reasonsByKey(resp)), "reasons")
			require.Empty(t, cmp.Diff(tt.wantVia, viaByKey(resp)), "via")
			assertWaveOrder(t, tt.graph, resp)
		})
	}
}

func assertWaveOrder(t *testing.T, g *v1.Graph, resp *v1.ResolveResponse) {
	t.Helper()
	wave := map[string]int{}
	for i, keys := range resp.Waves {
		for _, k := range keys {
			wave[k] = i
		}
	}
	for _, a := range resp.Affected {
		require.Equal(t, wave[a.Key], a.Wave, "wave of %s", a.Key)
	}
	for _, e := range g.Edges {
		if !isOrdering(&e) {
			continue
		}
		from, okFrom := wave[e.From.Key]
		to, okTo := wave[e.To.Key]
		if okFrom && okTo {
			require.Less(t, to, from, "edge %s -> %s", e.From.Key, e.To.Key)
		}
	}
}

func TestResolveCycles(t *testing.T) {
	tests := []struct {
		name          string
		graph         *v1.Graph
		paths         []string
		noPropagation bool
		wantCycles    [][]string
		wantAffected  []string
		wantErr       string
	}{
		{
			name:         "self-loop",
			graph:        newGraph().stacks("s/a", "s/b").dep("s/a", "s/a").dep("s/b", "s/a").build(),
			paths:        []string{"s/a/main.tf"},
			wantCycles:   [][]string{{"s/a", "s/a"}},
			wantAffected: []string{"s/a", "s/b"},
			wantErr:      "resolve: dependency cycle: s/a -> s/a",
		},
		{
			name:         "two-cycle",
			graph:        newGraph().stacks("s/a", "s/b").dep("s/a", "s/b").dep("s/b", "s/a").build(),
			paths:        []string{"s/b/main.tf"},
			wantCycles:   [][]string{{"s/a", "s/b", "s/a"}},
			wantAffected: []string{"s/a", "s/b"},
			wantErr:      "resolve: dependency cycle: s/a -> s/b -> s/a",
		},
		{
			name:         "three-cycle",
			graph:        newGraph().stacks("s/a", "s/b", "s/c").dep("s/a", "s/b").dep("s/b", "s/c").reads("s/c", "s/a").build(),
			paths:        []string{"s/c/main.tf"},
			wantCycles:   [][]string{{"s/a", "s/b", "s/c", "s/a"}},
			wantAffected: []string{"s/a", "s/b", "s/c"},
			wantErr:      "resolve: dependency cycle: s/a -> s/b -> s/c -> s/a",
		},
		{
			name: "two cycles",
			graph: newGraph().stacks("s/a", "s/b", "t/x", "t/y", "t/z").
				dep("s/a", "s/b").dep("s/b", "s/a").dep("t/x", "t/z").dep("t/z", "t/y").dep("t/y", "t/x").build(),
			paths:        []string{"s/a/main.tf", "t/y/main.tf"},
			wantCycles:   [][]string{{"s/a", "s/b", "s/a"}, {"t/x", "t/z", "t/y", "t/x"}},
			wantAffected: []string{"s/a", "s/b", "t/x", "t/y", "t/z"},
			wantErr:      "resolve: dependency cycle: s/a -> s/b -> s/a; t/x -> t/z -> t/y -> t/x",
		},
		{
			name:          "cycle between two changed stacks without propagation",
			graph:         newGraph().stacks("s/a", "s/b").dep("s/a", "s/b").dep("s/b", "s/a").build(),
			paths:         []string{"s/a/main.tf", "s/b/main.tf"},
			noPropagation: true,
			wantCycles:    [][]string{{"s/a", "s/b", "s/a"}},
			wantAffected:  []string{"s/a", "s/b"},
			wantErr:       "resolve: dependency cycle: s/a -> s/b -> s/a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Propagate.Dependents = boolPtr(!tt.noPropagation)
			resp, err := Resolve(tt.graph, Input{ChangedPaths: tt.paths, Config: cfg})
			require.ErrorIs(t, err, ErrCycle)
			require.EqualError(t, err, tt.wantErr)
			require.NotNil(t, resp)
			require.Empty(t, cmp.Diff(tt.wantCycles, resp.Cycles))
			require.Equal(t, tt.wantAffected, affectedKeys(resp))
			for _, a := range resp.Affected {
				require.Zero(t, a.Wave)
			}
			require.NotNil(t, resp.Waves)
			require.Empty(t, resp.Waves)
			require.NotNil(t, resp.Matrix.Include)
			require.Empty(t, resp.Matrix.Include)
		})
	}
}

func TestResolveCycleOutsideAffectedSet(t *testing.T) {
	g := newGraph().stacks("s/a", "s/b", "s/x", "s/y").
		dep("s/b", "s/a").dep("s/x", "s/y").dep("s/y", "s/x").build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"s/a/main.tf"}})
	require.NoError(t, err)
	require.Equal(t, [][]string{{"s/a"}, {"s/b"}}, resp.Waves)
	require.Empty(t, resp.Cycles)
}

func TestResolvePropagateDependentsFalse(t *testing.T) {
	cfg := exampleConfig()
	cfg.Propagate.Dependents = boolPtr(false)
	tests := []struct {
		name        string
		paths       []string
		wantWaves   [][]string
		wantReasons map[string][]v1.Reason
	}{
		{
			name:        "module change stops at consumers",
			paths:       []string{"modules/vpc/main.tf"},
			wantWaves:   [][]string{{prodVPC, stagingVPC}},
			wantReasons: map[string][]v1.Reason{prodVPC: reasons(v1.ReasonModule), stagingVPC: reasons(v1.ReasonModule)},
		},
		{
			name:        "stack change stops at the stack",
			paths:       []string{"stacks/prod/eks/main.tf"},
			wantWaves:   [][]string{{prodEKS}},
			wantReasons: map[string][]v1.Reason{prodEKS: reasons(v1.ReasonChanged)},
		},
		{
			name:      "changed stacks still order among themselves without dependent reasons",
			paths:     []string{"stacks/prod/apps/main.tf", "modules/vpc/main.tf", "stacks/prod/eks/main.tf"},
			wantWaves: [][]string{{prodVPC, stagingVPC}, {prodEKS}, {prodApps}},
			wantReasons: map[string][]v1.Reason{
				prodVPC:    reasons(v1.ReasonModule),
				stagingVPC: reasons(v1.ReasonModule),
				prodEKS:    reasons(v1.ReasonChanged),
				prodApps:   reasons(v1.ReasonChanged),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(exampleGraph().build(), Input{ChangedPaths: tt.paths, Config: cfg})
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.wantWaves, resp.Waves), "waves")
			require.Empty(t, cmp.Diff(tt.wantReasons, reasonsByKey(resp)), "reasons")
			for _, a := range resp.Affected {
				if a.Reasons[0] == v1.ReasonChanged {
					require.Nil(t, a.Via, a.Key)
				}
			}
		})
	}
}

func TestResolveRequested(t *testing.T) {
	tests := []struct {
		name         string
		paths        []string
		requested    []string
		wantWaves    [][]string
		wantReasons  map[string][]v1.Reason
		wantWarnings []string
	}{
		{
			name:        "subset of the affected set",
			paths:       []string{"modules/vpc/main.tf"},
			requested:   []string{prodVPC, prodEKS},
			wantWaves:   [][]string{{prodVPC}, {prodEKS}},
			wantReasons: map[string][]v1.Reason{prodVPC: reasons(v1.ReasonModule), prodEKS: reasons(v1.ReasonDependent)},
		},
		{
			name:        "order is preserved through an affected stack that was not requested",
			paths:       []string{"modules/vpc/main.tf"},
			requested:   []string{prodApps, prodVPC},
			wantWaves:   [][]string{{prodVPC}, {prodApps}},
			wantReasons: map[string][]v1.Reason{prodVPC: reasons(v1.ReasonModule), prodApps: reasons(v1.ReasonReadsState)},
		},
		{
			name:      "requested unaffected stack is added with reason requested",
			paths:     []string{"stacks/prod/eks/main.tf"},
			requested: []string{prodEKS, stagingVPC, prodApps},
			wantWaves: [][]string{{prodEKS, stagingVPC}, {prodApps}},
			wantReasons: map[string][]v1.Reason{
				prodEKS:    reasons(v1.ReasonChanged),
				stagingVPC: reasons(v1.ReasonRequested),
				prodApps:   reasons(v1.ReasonReadsState),
			},
		},
		{
			name:        "requested stacks order among themselves with no change at all",
			requested:   []string{prodApps, prodVPC, prodEKS},
			wantWaves:   [][]string{{prodVPC}, {prodEKS}, {prodApps}},
			wantReasons: map[string][]v1.Reason{prodVPC: reasons(v1.ReasonRequested), prodEKS: reasons(v1.ReasonRequested), prodApps: reasons(v1.ReasonRequested)},
		},
		{
			name:        "requested keys are normalised and may be qualified with this repository",
			paths:       []string{"modules/vpc/main.tf"},
			requested:   []string{"./stacks/prod/vpc/", " Acme/Infra//stacks/prod/eks ", "", prodVPC},
			wantWaves:   [][]string{{prodVPC}, {prodEKS}},
			wantReasons: map[string][]v1.Reason{prodVPC: reasons(v1.ReasonModule), prodEKS: reasons(v1.ReasonDependent)},
		},
		{
			name:        "unknown and external requests are warnings",
			paths:       []string{"stacks/prod/vpc/main.tf"},
			requested:   []string{prodVPC, "stacks/nope", "acme/network//stacks/tgw", "other/repo//stacks/x"},
			wantWaves:   [][]string{{prodVPC}},
			wantReasons: map[string][]v1.Reason{prodVPC: reasons(v1.ReasonChanged)},
			wantWarnings: []string{
				"requested stack acme/network//stacks/tgw is external to this repository and cannot be scheduled",
				"requested stack other/repo//stacks/x is not in the graph",
				"requested stack stacks/nope is not in the graph",
			},
		},
		{
			name:        "blank requests are no restriction",
			paths:       []string{"stacks/prod/eks/main.tf"},
			requested:   []string{"", "  "},
			wantWaves:   [][]string{{prodEKS}, {prodApps}},
			wantReasons: map[string][]v1.Reason{prodEKS: reasons(v1.ReasonChanged), prodApps: reasons(v1.ReasonReadsState)},
		},
		{
			name:         "nothing valid requested schedules nothing",
			paths:        []string{"modules/vpc/main.tf"},
			requested:    []string{"stacks/nope"},
			wantWaves:    [][]string{},
			wantReasons:  map[string][]v1.Reason{},
			wantWarnings: []string{"requested stack stacks/nope is not in the graph"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := exampleGraph().external("acme/network//stacks/tgw", "acme/network").build()
			resp, err := Resolve(g, Input{ChangedPaths: tt.paths, Config: exampleConfig(), Requested: tt.requested})
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.wantWaves, resp.Waves), "waves")
			require.Empty(t, cmp.Diff(tt.wantReasons, reasonsByKey(resp)), "reasons")
			require.Empty(t, cmp.Diff(tt.wantWarnings, resp.Warnings), "warnings")
			require.Len(t, resp.Matrix.Include, len(resp.Affected))
		})
	}
}

func TestResolveRequestedCycleInAffectedSet(t *testing.T) {
	g := newGraph().stacks("s/a", "s/b", "s/c").dep("s/b", "s/c").dep("s/c", "s/b").build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"s/b/main.tf"}, Requested: []string{"s/a"}})
	require.ErrorIs(t, err, ErrCycle)
	require.Equal(t, [][]string{{"s/b", "s/c", "s/b"}}, resp.Cycles)
	require.Equal(t, []string{"s/a"}, affectedKeys(resp))
}

func TestResolveDeepestStack(t *testing.T) {
	g := newGraph().stacks("stacks/app", "stacks/app/child", "stacks/app/child/grand", "stacks/other").
		dep("stacks/app/child", "stacks/app").build()
	tests := []struct {
		name  string
		paths []string
		want  map[string][]v1.Reason
	}{
		{
			name:  "file in the outer stack",
			paths: []string{"stacks/app/main.tf"},
			want: map[string][]v1.Reason{
				"stacks/app":       reasons(v1.ReasonChanged),
				"stacks/app/child": reasons(v1.ReasonDependent),
			},
		},
		{
			name:  "file in a nested stack never affects the enclosing one",
			paths: []string{"stacks/app/child/main.tf"},
			want:  map[string][]v1.Reason{"stacks/app/child": reasons(v1.ReasonChanged)},
		},
		{
			name:  "deepest of three levels",
			paths: []string{"stacks/app/child/grand/sub/dir/file.json"},
			want:  map[string][]v1.Reason{"stacks/app/child/grand": reasons(v1.ReasonChanged)},
		},
		{
			name:  "plain subdirectory of a stack belongs to it",
			paths: []string{"stacks/app/templates/user_data.sh"},
			want:  map[string][]v1.Reason{"stacks/app": reasons(v1.ReasonChanged), "stacks/app/child": reasons(v1.ReasonDependent)},
		},
		{
			name:  "the stack directory itself",
			paths: []string{"stacks/other"},
			want:  map[string][]v1.Reason{"stacks/other": reasons(v1.ReasonChanged)},
		},
		{
			name:  "prefix of a sibling name is not enclosing",
			paths: []string{"stacks/other-thing/main.tf", "stacks/app2/main.tf"},
			want:  map[string][]v1.Reason{},
		},
		{
			name:  "paths are cleaned before matching",
			paths: []string{"./stacks/other/main.tf", "stacks\\app\\child\\grand\\x.tf", "/stacks/app/child/../child/y.tf"},
			want: map[string][]v1.Reason{
				"stacks/other":           reasons(v1.ReasonChanged),
				"stacks/app/child/grand": reasons(v1.ReasonChanged),
				"stacks/app/child":       reasons(v1.ReasonChanged),
			},
		},
		{
			name:  "terraform working directory is ignored at any depth",
			paths: []string{"stacks/app/.terraform/modules/modules.json", "stacks/app/child/.terraform/terraform.tfstate"},
			want:  map[string][]v1.Reason{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(g, Input{ChangedPaths: tt.paths})
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.want, reasonsByKey(resp)))
		})
	}
}

func TestResolveNestedModules(t *testing.T) {
	a, b, c := localKey("modules/a"), localKey("modules/b"), localKey("modules/c")
	net, sub := localKey("modules/net"), localKey("modules/net/sub")
	g := newGraph().
		stacks("stacks/one", "stacks/two", "stacks/three", "stacks/four", "stacks/five").
		local("modules/a").local("modules/b").local("modules/c").local("modules/net").local("modules/net/sub").
		uses("stacks/one", a).uses(a, b).uses(b, c).
		uses("stacks/two", b).
		uses("stacks/three", a).uses("stacks/three", c).
		uses("stacks/four", net).uses(net, sub).
		uses("stacks/five", sub).
		build()
	tests := []struct {
		name    string
		paths   []string
		want    map[string][]v1.Reason
		wantVia map[string][]string
	}{
		{
			name:  "change two levels below the stack",
			paths: []string{"modules/c/main.tf"},
			want: map[string][]v1.Reason{
				"stacks/one":   reasons(v1.ReasonModule),
				"stacks/two":   reasons(v1.ReasonModule),
				"stacks/three": reasons(v1.ReasonModule),
			},
			wantVia: map[string][]string{
				"stacks/one":   {a, b, c},
				"stacks/two":   {b, c},
				"stacks/three": {a, b, c},
			},
		},
		{
			name:    "change in the intermediate module",
			paths:   []string{"modules/b/variables.tf"},
			want:    map[string][]v1.Reason{"stacks/one": reasons(v1.ReasonModule), "stacks/two": reasons(v1.ReasonModule), "stacks/three": reasons(v1.ReasonModule)},
			wantVia: map[string][]string{"stacks/one": {a, b}, "stacks/two": {b}, "stacks/three": {a, b}},
		},
		{
			name:    "change in the top module only reaches its direct users",
			paths:   []string{"modules/a/main.tf"},
			want:    map[string][]v1.Reason{"stacks/one": reasons(v1.ReasonModule), "stacks/three": reasons(v1.ReasonModule)},
			wantVia: map[string][]string{"stacks/one": {a}, "stacks/three": {a}},
		},
		{
			name:    "nested module directory belongs to the deepest module",
			paths:   []string{"modules/net/sub/main.tf"},
			want:    map[string][]v1.Reason{"stacks/four": reasons(v1.ReasonModule), "stacks/five": reasons(v1.ReasonModule)},
			wantVia: map[string][]string{"stacks/four": {net, sub}, "stacks/five": {sub}},
		},
		{
			name:    "enclosing module directory does not reach users of the nested one",
			paths:   []string{"modules/net/main.tf"},
			want:    map[string][]v1.Reason{"stacks/four": reasons(v1.ReasonModule)},
			wantVia: map[string][]string{"stacks/four": {net}},
		},
		{
			name:    "module and stack change together",
			paths:   []string{"modules/c/main.tf", "stacks/two/main.tf"},
			want:    map[string][]v1.Reason{"stacks/one": reasons(v1.ReasonModule), "stacks/two": reasons(v1.ReasonChanged, v1.ReasonModule), "stacks/three": reasons(v1.ReasonModule)},
			wantVia: map[string][]string{"stacks/one": {a, b, c}, "stacks/two": {b, c}, "stacks/three": {a, b, c}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(g, Input{ChangedPaths: tt.paths})
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.want, reasonsByKey(resp)), "reasons")
			require.Empty(t, cmp.Diff(tt.wantVia, viaByKey(resp)), "via")
		})
	}
}

func TestResolveModuleCycleTerminates(t *testing.T) {
	a, b := localKey("modules/a"), localKey("modules/b")
	g := newGraph().stacks("stacks/one").local("modules/a").local("modules/b").
		uses("stacks/one", a).uses(a, b).uses(b, a).build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"modules/b/main.tf"}})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"stacks/one": {a, b}}, viaByKey(resp))
}

func TestResolveModuleInsideStack(t *testing.T) {
	inner := localKey("stacks/app/modules/inner")
	g := newGraph().stacks("stacks/app", "stacks/other").local("stacks/app/modules/inner").
		uses("stacks/other", inner).build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"stacks/app/modules/inner/main.tf"}})
	require.NoError(t, err)
	require.Equal(t, map[string][]v1.Reason{
		"stacks/app":   reasons(v1.ReasonChanged),
		"stacks/other": reasons(v1.ReasonModule),
	}, reasonsByKey(resp))
}

func TestResolveExternal(t *testing.T) {
	tgw := "acme/network//stacks/prod/tgw"
	app := "acme/apps//stacks/prod/web"
	g := newGraph().stacks("stacks/vpc", "stacks/dns", "stacks/edge").
		external(tgw, "acme/network").
		external(app, "acme/apps").
		stack(v1.Stack{Key: "stacks/legacy", Path: "stacks/legacy", Repo: "acme/legacy", External: true}).
		dep("stacks/dns", "stacks/vpc").
		dep("stacks/vpc", tgw).
		dep(app, "stacks/dns").
		reads(app, "stacks/vpc").
		dep("stacks/legacy", "stacks/vpc").
		dep("stacks/edge", app).
		build()
	tests := []struct {
		name         string
		paths        []string
		requested    []string
		wantWaves    [][]string
		wantExternal []string
	}{
		{
			name:         "external dependents are listed, never scheduled",
			paths:        []string{"stacks/vpc/main.tf"},
			wantWaves:    [][]string{{"stacks/vpc"}, {"stacks/dns"}},
			wantExternal: []string{"acme/apps//stacks/prod/web", "acme/legacy//stacks/legacy"},
		},
		{
			name:      "an external dependency neither orders nor lists",
			paths:     []string{"stacks/edge/main.tf"},
			wantWaves: [][]string{{"stacks/edge"}},
		},
		{
			name:         "paths of external stacks never match",
			paths:        []string{"stacks/prod/tgw/main.tf", "stacks/prod/web/main.tf", "stacks/legacy/main.tf"},
			wantWaves:    [][]string{},
			wantExternal: nil,
		},
		{
			name:         "only external dependents of scheduled stacks are listed",
			paths:        []string{"stacks/vpc/main.tf"},
			requested:    []string{"stacks/dns"},
			wantWaves:    [][]string{{"stacks/dns"}},
			wantExternal: []string{"acme/apps//stacks/prod/web"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(g, Input{ChangedPaths: tt.paths, Requested: tt.requested})
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(tt.wantWaves, resp.Waves), "waves")
			require.Empty(t, cmp.Diff(tt.wantExternal, resp.External), "external")
			for _, a := range resp.Affected {
				require.NotContains(t, []string{tgw, app, "stacks/legacy"}, a.Key)
			}
		})
	}
}

func TestResolveExternalWithoutDependentPropagation(t *testing.T) {
	cfg := config.Default()
	cfg.Propagate.Dependents = boolPtr(false)
	g := newGraph().stacks("stacks/vpc").external("acme/apps//stacks/web", "acme/apps").
		dep("acme/apps//stacks/web", "stacks/vpc").build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"stacks/vpc/main.tf"}, Config: cfg})
	require.NoError(t, err)
	require.Equal(t, []string{"acme/apps//stacks/web"}, resp.External)
}

func TestResolveLocks(t *testing.T) {
	taken := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	locks := map[string]v1.LockInfo{
		prodVPC:  {StackKey: prodVPC, RunID: "run-1", PRNumber: 42, TakenAt: taken, Reason: "apply"},
		"ghost":  {StackKey: "ghost", RunID: "run-2"},
		prodApps: {StackKey: prodApps, RunID: "run-3", PRNumber: 7, TakenAt: taken},
	}
	resp, err := Resolve(exampleGraph().build(), Input{ChangedPaths: []string{"stacks/prod/vpc/main.tf"}, Config: exampleConfig(), Locks: locks})
	require.NoError(t, err)
	got := map[string]*v1.LockInfo{}
	for _, a := range resp.Affected {
		got[a.Key] = a.LockedBy
	}
	want := map[string]*v1.LockInfo{
		prodVPC:  {StackKey: prodVPC, RunID: "run-1", PRNumber: 42, TakenAt: taken, Reason: "apply"},
		prodEKS:  nil,
		prodApps: {StackKey: prodApps, RunID: "run-3", PRNumber: 7, TakenAt: taken},
	}
	require.Empty(t, cmp.Diff(want, got))
	locks[prodVPC] = v1.LockInfo{RunID: "mutated"}
	require.Equal(t, "run-1", resp.Affected[0].LockedBy.RunID)
}

func TestResolveEnvironment(t *testing.T) {
	cfg := exampleConfig()
	cfg.Environments["stacks/prod/eu/"] = "production-eu"
	g := newGraph().
		stacks("stacks/prod/vpc", "stacks/prod/eu/vpc", "stacks/tools").
		stack(v1.Stack{Key: "stacks/prod/scanned", Path: "stacks/prod/scanned", Environment: "sandbox"}).
		stack(v1.Stack{Key: "stacks/ops/scanned", Path: "stacks/ops/scanned", Environment: "ops"}).
		stack(v1.Stack{Key: "stacks/prod/override", Path: "stacks/prod/override", Environment: "from-file", Config: &v1.StackConfig{Environment: "from-file"}}).
		stack(v1.Stack{Key: "stacks/prod/file-only", Path: "stacks/prod/file-only", Config: &v1.StackConfig{Environment: "from-file"}}).
		build()
	paths := []string{
		"stacks/prod/vpc/main.tf", "stacks/prod/eu/vpc/main.tf", "stacks/tools/main.tf",
		"stacks/prod/scanned/main.tf", "stacks/ops/scanned/main.tf",
		"stacks/prod/override/main.tf", "stacks/prod/file-only/main.tf",
	}
	resp, err := Resolve(g, Input{ChangedPaths: paths, Config: cfg})
	require.NoError(t, err)
	got := map[string]string{}
	for _, a := range resp.Affected {
		got[a.Key] = a.Environment
	}
	require.Equal(t, map[string]string{
		"stacks/prod/vpc":       "production",
		"stacks/prod/eu/vpc":    "production-eu",
		"stacks/tools":          v1.DefaultEnvironment,
		"stacks/prod/scanned":   "production",
		"stacks/ops/scanned":    "ops",
		"stacks/prod/override":  "from-file",
		"stacks/prod/file-only": "from-file",
	}, got)
	require.Equal(t, []string{`stack stacks/tools has no environment mapping; it runs under environment "default"`}, resp.Warnings)
	for _, e := range resp.Matrix.Include {
		require.Equal(t, got[e.Key], e.Environment)
	}
}

func TestResolveToolAndPlanOutput(t *testing.T) {
	cfg := exampleConfig()
	cfg.PlanOutput = v1.PlanOutputSummary
	g := newGraph().
		stacks("stacks/plain").
		stack(v1.Stack{Key: "stacks/own", Path: "stacks/own", Tool: v1.ToolTerraform, ToolVersion: "1.14.0", PlanOutput: "full"}).
		stack(v1.Stack{Key: "stacks/file", Path: "stacks/file", Config: &v1.StackConfig{Tool: v1.ToolTerraform, ToolVersion: "1.13.1", PlanOutput: v1.PlanOutputFull}}).
		build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"stacks/plain/a.tf", "stacks/own/a.tf", "stacks/file/a.tf"}, Config: cfg})
	require.NoError(t, err)
	type tool struct {
		Tool        v1.Tool
		ToolVersion string
		PlanOutput  string
	}
	got := map[string]tool{}
	for _, a := range resp.Affected {
		got[a.Key] = tool{a.Tool, a.ToolVersion, a.PlanOutput}
	}
	require.Equal(t, map[string]tool{
		"stacks/plain": {v1.ToolTofu, "1.9.0", "summary"},
		"stacks/own":   {v1.ToolTerraform, "1.14.0", "full"},
		"stacks/file":  {v1.ToolTerraform, "1.13.1", "full"},
	}, got)
}

func TestResolveWorkspaces(t *testing.T) {
	g := newGraph().
		stacks("stacks/app", "stacks/app:blue", "stacks/app:green", "stacks/db:blue", "stacks/web:blue").
		dep("stacks/web:blue", "stacks/app:blue").
		dep("stacks/app:blue", "stacks/db:blue").
		build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"stacks/app/main.tf"}})
	require.NoError(t, err)
	require.Equal(t, [][]string{{"stacks/app", "stacks/app:blue", "stacks/app:green"}, {"stacks/web:blue"}}, resp.Waves)
	require.Equal(t, map[string][]v1.Reason{
		"stacks/app":       reasons(v1.ReasonChanged),
		"stacks/app:blue":  reasons(v1.ReasonChanged),
		"stacks/app:green": reasons(v1.ReasonChanged),
		"stacks/web:blue":  reasons(v1.ReasonDependent),
	}, reasonsByKey(resp))
	type entry struct{ Stack, Key, Workspace string }
	got := make([]entry, 0, len(resp.Matrix.Include))
	for _, e := range resp.Matrix.Include {
		got = append(got, entry{e.Stack, e.Key, e.Workspace})
	}
	require.Equal(t, []entry{
		{"stacks/app", "stacks/app", ""},
		{"stacks/app", "stacks/app:blue", "blue"},
		{"stacks/app", "stacks/app:green", "green"},
		{"stacks/web", "stacks/web:blue", "blue"},
	}, got)
}

func TestResolveInstanceFromKeyNotWorkspace(t *testing.T) {
	g := newGraph().
		stack(v1.Stack{Key: "stacks/app:blue"}).
		stack(v1.Stack{Key: "stacks/db", Path: "stacks/db", Workspace: "default"}).
		build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"stacks/app/main.tf", "stacks/db/main.tf"}})
	require.NoError(t, err)
	require.Len(t, resp.Affected, 2)
	require.Equal(t, "stacks/app", resp.Affected[0].Path)
	require.Equal(t, "blue", resp.Affected[0].Instance)
	require.Empty(t, resp.Affected[0].Workspace)
	require.Equal(t, "blue", resp.Affected[0].Environment)
	require.Equal(t, "blue", resp.Matrix.Include[0].Instance)
	require.Equal(t, "stacks/db", resp.Affected[1].Path)
	require.Empty(t, resp.Affected[1].Instance)
	require.Empty(t, resp.Affected[1].Workspace)
}

func TestResolveEnvironmentIsRendered(t *testing.T) {
	cfg := config.Default()
	cfg.Environments = map[string]string{"stacks/": "env-{{ .Instance }}"}
	g := newGraph().
		stack(v1.Stack{Key: "stacks/app:prod", Path: "stacks/app", Instance: "prod", Environment: "prod"}).
		stack(v1.Stack{
			Key: "stacks/web:blue", Path: "stacks/web", Instance: "blue", Environment: "blue",
			Config: &v1.StackConfig{Instances: v1.Instances{"blue": {Environment: "web-{{ .Instance }}"}}},
		}).
		build()
	resp, err := Resolve(g, Input{Config: cfg, ChangedPaths: []string{"stacks/app/main.tf", "stacks/web/main.tf"}})
	require.NoError(t, err)
	require.Len(t, resp.Affected, 2)
	require.Equal(t, "env-prod", resp.Affected[0].Environment)
	require.Equal(t, "web-blue", resp.Affected[1].Environment)
	require.Empty(t, resp.Warnings)
}

func TestResolveWarnsAboutAScannedUnmappedStack(t *testing.T) {
	g := newGraph().
		stack(v1.Stack{Key: "stacks/app", Path: "stacks/app", Environment: v1.DefaultEnvironment}).
		stack(v1.Stack{Key: "stacks/web", Path: "stacks/web", Environment: v1.DefaultEnvironment, Config: &v1.StackConfig{Environment: "default"}}).
		build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"stacks/app/main.tf", "stacks/web/main.tf"}})
	require.NoError(t, err)
	require.Equal(t, []string{`stack stacks/app has no environment mapping; it runs under environment "default"`}, resp.Warnings)
}

func TestResolveIgnore(t *testing.T) {
	g := newGraph().stacks("stacks/a").local("modules/m").uses("stacks/a", localKey("modules/m")).build()
	tests := []struct {
		name         string
		cfg          *v1.RepoConfig
		paths        []string
		wantAffected []string
		wantWarnings []string
	}{
		{
			name:  "nil config uses the default ignore list",
			paths: []string{"stacks/a/README.md", "stacks/a/README", "modules/m/docs/usage.md"},
		},
		{
			name:         "lock file is not ignored by default",
			paths:        []string{"stacks/a/.terraform.lock.hcl"},
			wantAffected: []string{"stacks/a"},
		},
		{
			name:  "lock file is ignored with ignore_lockfile",
			cfg:   func() *v1.RepoConfig { c := config.Default(); c.Stacks.IgnoreLockfile = true; return c }(),
			paths: []string{"stacks/a/.terraform.lock.hcl", "modules/m/.terraform.lock.hcl"},
		},
		{
			name:         "custom globs replace the defaults",
			cfg:          func() *v1.RepoConfig { c := config.Default(); c.Stacks.Ignore = []string{"**/*.png"}; return c }(),
			paths:        []string{"modules/m/diagram.png", "stacks/a/README.md"},
			wantAffected: []string{"stacks/a"},
		},
		{
			name:         "empty ignore list ignores nothing",
			cfg:          func() *v1.RepoConfig { c := config.Default(); c.Stacks.Ignore = []string{}; return c }(),
			paths:        []string{"modules/m/README.md"},
			wantAffected: []string{"stacks/a"},
		},
		{
			name:         "invalid glob is skipped with a warning",
			cfg:          func() *v1.RepoConfig { c := config.Default(); c.Stacks.Ignore = []string{"[", "**/*.md"}; return c }(),
			paths:        []string{"stacks/a/main.tf", "stacks/a/NOTES.md"},
			wantAffected: []string{"stacks/a"},
			wantWarnings: []string{`ignore glob "[" is invalid and was skipped`},
		},
		{
			name:  "terraform directories and empty paths are skipped",
			paths: []string{".terraform/x", "modules/m/.terraform/y", "", ".", "/"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(g, Input{ChangedPaths: tt.paths, Config: tt.cfg})
			require.NoError(t, err)
			keys := affectedKeys(resp)
			if len(tt.wantAffected) == 0 {
				require.Empty(t, keys)
			} else {
				require.Equal(t, tt.wantAffected, keys)
			}
			var warnings []string
			for _, w := range resp.Warnings {
				if w != `stack stacks/a has no environment mapping; it runs under environment "default"` {
					warnings = append(warnings, w)
				}
			}
			require.Empty(t, cmp.Diff(tt.wantWarnings, warnings))
		})
	}
}

func TestResolvePartialConfigTakesDefaults(t *testing.T) {
	cfg := &v1.RepoConfig{Environments: map[string]string{"stacks/prod/": "production"}}
	before, err := json.Marshal(cfg)
	require.NoError(t, err)
	resp, err := Resolve(exampleGraph().build(), Input{
		ChangedPaths: []string{"stacks/prod/vpc/README.md", "stacks/staging/vpc/docs/notes.md", "stacks/prod/eks/main.tf"},
		Config:       cfg,
	})
	require.NoError(t, err)
	require.Equal(t, [][]string{{prodEKS}, {prodApps}}, resp.Waves)
	for _, e := range resp.Matrix.Include {
		require.Equal(t, v1.ToolTerraform, e.Tool, e.Key)
		require.Equal(t, string(v1.PlanOutputFull), e.PlanOutput, e.Key)
		require.Equal(t, "production", e.Environment, e.Key)
	}
	after, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestResolveWarnings(t *testing.T) {
	cfg := exampleConfig()
	g := newGraph().stacks("stacks/prod/a", "stacks/prod/b").
		dep("stacks/prod/a", "stacks/prod/missing").
		dep("stacks/prod/b", "stacks/prod/missing").
		dep("stacks/prod/b", "stacks/prod/a").
		build()
	resp, err := Resolve(g, Input{
		ChangedPaths: []string{"stacks/prod/a/main.tf", "scripts/deploy.sh", "../outside.tf", "scripts/deploy.sh", "stacks/prod/c/main.tf"},
		Config:       cfg,
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		"changed path ../outside.tf is inside no stack or module",
		"changed path scripts/deploy.sh is inside no stack or module",
		"changed path stacks/prod/c/main.tf is inside no stack or module",
		"stack stacks/prod/a depends_on unknown stack stacks/prod/missing",
		"stack stacks/prod/b depends_on unknown stack stacks/prod/missing",
	}, resp.Warnings)
	require.Equal(t, [][]string{{"stacks/prod/a"}, {"stacks/prod/b"}}, resp.Waves)
}

func TestResolveInvalidGraph(t *testing.T) {
	tests := []struct {
		name    string
		graph   *v1.Graph
		wantErr string
	}{
		{name: "nil graph", wantErr: "resolve: invalid graph: nil graph"},
		{
			name:    "duplicate stack",
			graph:   newGraph().stacks("stacks/a", "stacks/b", "stacks/a").build(),
			wantErr: "resolve: invalid graph: duplicate node keys stacks/a",
		},
		{
			name:    "duplicate module",
			graph:   newGraph().stacks("stacks/a").local("modules/m").local("modules/m").build(),
			wantErr: "resolve: invalid graph: duplicate node keys acme/infra//modules/m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := Resolve(tt.graph, Input{ChangedPaths: []string{"stacks/a/main.tf"}})
			require.ErrorIs(t, err, ErrInvalidGraph)
			require.False(t, errors.Is(err, ErrCycle))
			require.EqualError(t, err, tt.wantErr)
			require.Nil(t, resp)
		})
	}
}

func TestResolveNeverSchedulesStacksOutsideTheRepository(t *testing.T) {
	m := localKey("modules/m")
	g := newGraph().stacks("stacks/a").
		stack(v1.Stack{Key: "../../etc"}).
		stack(v1.Stack{Key: "stacks/escape", Path: "../../etc"}).
		stack(v1.Stack{Key: "./stacks/dot", Path: "stacks/dot"}).
		stack(v1.Stack{Key: "/abs/key", Path: "abs/key"}).
		local("modules/m").
		uses("stacks/a", m).uses("../../etc", m).uses("./stacks/dot", m).
		dep("stacks/escape", "stacks/a").dep("/abs/key", "stacks/a").
		build()
	resp, err := Resolve(g, Input{
		ChangedPaths: []string{"modules/m/main.tf", "stacks/escape/main.tf", "stacks/dot/main.tf", "abs/key/main.tf"},
	})
	require.NoError(t, err)
	require.Equal(t, [][]string{{"stacks/a"}}, resp.Waves)
	require.Equal(t, []string{"stacks/a"}, affectedKeys(resp))
	require.Equal(t, "stacks/a", resp.Matrix.Include[0].Stack)
	require.Equal(t, []string{
		`changed path abs/key/main.tf is inside no stack or module`,
		`changed path stacks/dot/main.tf is inside no stack or module`,
		`changed path stacks/escape/main.tf is inside no stack or module`,
		`stack ../../etc has no canonical directory inside the repository and is never scheduled`,
		`stack ./stacks/dot has no canonical directory inside the repository and is never scheduled`,
		`stack /abs/key has no canonical directory inside the repository and is never scheduled`,
		`stack stacks/a has no environment mapping; it runs under environment "default"`,
		`stack stacks/escape has no canonical directory inside the repository and is never scheduled`,
	}, resp.Warnings)

	resp, err = Resolve(g, Input{Requested: []string{"../../etc", "stacks/escape", "stacks/a"}})
	require.NoError(t, err)
	require.Equal(t, []string{"stacks/a"}, affectedKeys(resp))
	require.Contains(t, resp.Warnings, "requested stack ../../etc has no canonical directory inside the repository and cannot be scheduled")
	require.Contains(t, resp.Warnings, "requested stack stacks/escape has no canonical directory inside the repository and cannot be scheduled")
}

func TestResolveToleratesMalformedEdges(t *testing.T) {
	g := newGraph().stacks("stacks/a", "stacks/b").stack(v1.Stack{}).stack(v1.Stack{Key: "stacks/nopath:ws", Path: "../x"}).
		module(v1.Module{Kind: v1.ModuleLocal, Path: "modules/unnamed"}).
		module(v1.Module{Key: "nowhere", Kind: v1.ModuleLocal}).
		local("modules/real").
		edge(v1.Edge{From: v1.StackRef("stacks/b"), To: v1.ModuleRef(localKey("modules/real")), Type: v1.EdgeReadsState}).
		edge(v1.Edge{From: v1.StackRef("stacks/b"), To: v1.StackRef("stacks/a"), Type: "bogus"}).
		edge(v1.Edge{From: v1.ModuleRef("stacks/b"), To: v1.StackRef("stacks/a"), Type: v1.EdgeDependsOn}).
		edge(v1.Edge{From: v1.StackRef("stacks/b"), To: v1.ModuleRef("missing"), Type: v1.EdgeUsesModule}).
		edge(v1.Edge{From: v1.StackRef("ghost"), To: v1.StackRef("stacks/a"), Type: v1.EdgeDependsOn}).
		edge(v1.Edge{From: v1.StackRef("stacks/b"), To: v1.StackRef("stacks/b"), Type: v1.EdgeUsesModule}).
		build()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"stacks/a/main.tf", "modules/unnamed/x.tf", "modules/real/x.tf"}})
	require.NoError(t, err)
	require.Equal(t, []string{"stacks/a"}, affectedKeys(resp))
}

func TestResolveDoesNotMutateInputs(t *testing.T) {
	g := exampleGraph().build()
	before, err := json.Marshal(g)
	require.NoError(t, err)
	cfg := exampleConfig()
	cfgBefore, err := json.Marshal(cfg)
	require.NoError(t, err)
	paths := []string{"modules/vpc/main.tf", "stacks/prod/eks/main.tf"}
	requested := []string{prodApps, "./stacks/prod/vpc"}
	_, err = Resolve(g, Input{ChangedPaths: paths, Config: cfg, Requested: requested})
	require.NoError(t, err)
	after, err := json.Marshal(g)
	require.NoError(t, err)
	cfgAfter, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	require.JSONEq(t, string(cfgBefore), string(cfgAfter))
	require.Equal(t, []string{"modules/vpc/main.tf", "stacks/prod/eks/main.tf"}, paths)
	require.Equal(t, []string{prodApps, "./stacks/prod/vpc"}, requested)
}

func TestResolveDeterministic(t *testing.T) {
	base := exampleGraph().
		external("acme/apps//stacks/web", "acme/apps").
		dep("acme/apps//stacks/web", prodApps).
		dep(prodApps, "stacks/prod/missing").
		build()
	in := Input{
		ChangedPaths: []string{"modules/vpc/main.tf", "scripts/x.sh", "stacks/prod/eks/main.tf"},
		Config:       exampleConfig(),
	}
	want, err := Resolve(base, in)
	require.NoError(t, err)
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	rng := rand.New(rand.NewPCG(7, 11))
	for i := range 50 {
		g := *base
		g.Stacks = append([]v1.Stack(nil), base.Stacks...)
		g.Modules = append([]v1.Module(nil), base.Modules...)
		g.Edges = append([]v1.Edge(nil), base.Edges...)
		rng.Shuffle(len(g.Stacks), func(a, b int) { g.Stacks[a], g.Stacks[b] = g.Stacks[b], g.Stacks[a] })
		rng.Shuffle(len(g.Modules), func(a, b int) { g.Modules[a], g.Modules[b] = g.Modules[b], g.Modules[a] })
		rng.Shuffle(len(g.Edges), func(a, b int) { g.Edges[a], g.Edges[b] = g.Edges[b], g.Edges[a] })
		paths := append([]string(nil), in.ChangedPaths...)
		rng.Shuffle(len(paths), func(a, b int) { paths[a], paths[b] = paths[b], paths[a] })
		got, err := Resolve(&g, Input{ChangedPaths: paths, Config: in.Config})
		require.NoError(t, err)
		gotJSON, err := json.Marshal(got)
		require.NoError(t, err)
		require.JSONEq(t, string(wantJSON), string(gotJSON), "shuffle %d", i)
	}
}

func TestResolveResponseJSON(t *testing.T) {
	resp, err := Resolve(newGraph().build(), Input{})
	require.NoError(t, err)
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	require.JSONEq(t, `{"affected":[],"waves":[],"matrix":{"include":[]},"cached":false}`, string(data))
}
