package graph

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

var allEdgeTypes = []v1.EdgeType{v1.EdgeDependsOn, v1.EdgeUsesModule, v1.EdgeReadsState}

func TestDependents(t *testing.T) {
	example := exampleGraph().build()
	cyclic := newGraph().stacks("a", "b", "c").dep("a", "b").dep("b", "a").dep("c", "a").build()
	tests := []struct {
		name  string
		graph *v1.Graph
		keys  []string
		types []v1.EdgeType
		want  []string
	}{
		{name: "nil graph", keys: []string{prodVPC}},
		{name: "no keys", graph: example},
		{name: "unknown key", graph: example, keys: []string{"nope"}},
		{name: "default types are depends_on and reads_state", graph: example, keys: []string{prodVPC}, want: []string{prodApps, prodEKS}},
		{name: "depends_on only", graph: example, keys: []string{prodVPC}, types: []v1.EdgeType{v1.EdgeDependsOn}, want: []string{prodEKS}},
		{name: "reads_state only", graph: example, keys: []string{prodEKS}, types: []v1.EdgeType{v1.EdgeReadsState}, want: []string{prodApps}},
		{name: "module users", graph: example, keys: []string{vpcModule}, types: []v1.EdgeType{v1.EdgeUsesModule}, want: []string{prodVPC, stagingVPC}},
		{
			name: "everything downstream of a module", graph: example, keys: []string{vpcModule}, types: allEdgeTypes,
			want: []string{prodApps, prodEKS, prodVPC, stagingApps, stagingVPC},
		},
		{name: "inputs are excluded", graph: example, keys: []string{prodVPC, prodEKS}, want: []string{prodApps}},
		{name: "inputs reached through a cycle are excluded", graph: cyclic, keys: []string{"a"}, want: []string{"b", "c"}},
		{name: "leaf has no dependents", graph: example, keys: []string{stagingApps}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Empty(t, cmp.Diff(tt.want, Dependents(tt.graph, tt.keys, tt.types...)))
		})
	}
}

func TestDependencies(t *testing.T) {
	example := exampleGraph().build()
	tests := []struct {
		name  string
		graph *v1.Graph
		keys  []string
		types []v1.EdgeType
		want  []string
	}{
		{name: "nil graph", keys: []string{prodApps}},
		{name: "root has no dependencies", graph: example, keys: []string{prodVPC}},
		{name: "default types", graph: example, keys: []string{prodApps}, want: []string{prodEKS, prodVPC}},
		{name: "reads_state only", graph: example, keys: []string{prodApps}, types: []v1.EdgeType{v1.EdgeReadsState}, want: []string{prodEKS}},
		{name: "modules used", graph: example, keys: []string{prodEKS}, types: []v1.EdgeType{v1.EdgeUsesModule}, want: []string{eksModule}},
		{
			name: "all types", graph: example, keys: []string{prodApps}, types: allEdgeTypes,
			want: []string{eksModule, vpcModule, prodEKS, prodVPC},
		},
		{name: "several inputs", graph: example, keys: []string{prodApps, stagingApps}, want: []string{prodEKS, prodVPC, stagingVPC}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Empty(t, cmp.Diff(tt.want, Dependencies(tt.graph, tt.keys, tt.types...)))
		})
	}
}

func TestModuleConsumers(t *testing.T) {
	a, b, c := localKey("modules/a"), localKey("modules/b"), localKey("modules/c")
	gitVPC := "acme/modules//vpc@v1.2.0"
	nested := newGraph().stacks("s/one", "s/two", "s/three").
		local("modules/a").local("modules/b").local("modules/c").git(gitVPC, "v1.2.0").
		uses("s/one", a).uses(a, b).uses(b, c).uses(c, a).
		uses("s/two", b).
		uses("s/three", gitVPC).uses(b, gitVPC).
		edge(v1.Edge{From: v1.StackRef("s/three"), To: v1.StackRef("s/one"), Type: v1.EdgeDependsOn}).
		edge(v1.Edge{From: v1.StackRef("s/three"), To: v1.ModuleRef(c), Type: v1.EdgeDependsOn}).
		build()
	tests := []struct {
		name   string
		graph  *v1.Graph
		module string
		want   []string
	}{
		{name: "nil graph", module: vpcModule},
		{name: "direct consumers", graph: exampleGraph().build(), module: vpcModule, want: []string{prodVPC, stagingVPC}},
		{name: "single consumer", graph: exampleGraph().build(), module: eksModule, want: []string{prodEKS}},
		{name: "unknown module", graph: exampleGraph().build(), module: "nope"},
		{name: "transitive through nested modules and a module cycle", graph: nested, module: c, want: []string{"s/one", "s/two"}},
		{name: "git module consumers, direct and nested", graph: nested, module: gitVPC, want: []string{"s/one", "s/three", "s/two"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Empty(t, cmp.Diff(tt.want, ModuleConsumers(tt.graph, tt.module)))
		})
	}
}

func TestStackModules(t *testing.T) {
	a := localKey("modules/a")
	gitVPC := "acme/modules//vpc@v1.2.0"
	reg := "registry:terraform-aws-modules/iam/aws@5.1"
	g := newGraph().stacks("s/one", "s/two").
		local("modules/a").git(gitVPC, "v1.2.0").
		module(v1.Module{Key: reg, Kind: v1.ModuleRegistry, Source: "terraform-aws-modules/iam/aws"}).
		uses("s/one", a).uses(a, gitVPC).
		edge(v1.Edge{From: v1.ModuleRef(a), To: v1.ModuleRef(reg), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "5.1"}}).
		edge(v1.Edge{From: v1.StackRef("s/one"), To: v1.ModuleRef(gitVPC), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "ignored"}}).
		dep("s/one", "s/two").
		build()
	tests := []struct {
		name  string
		graph *v1.Graph
		stack string
		want  []v1.ModuleConsume
	}{
		{name: "nil graph", stack: "s/one"},
		{
			name: "direct and nested modules with refs", graph: g, stack: "s/one",
			want: []v1.ModuleConsume{
				{ModuleKey: a},
				{ModuleKey: gitVPC, Ref: "v1.2.0"},
				{ModuleKey: reg, Ref: "5.1"},
			},
		},
		{name: "stack without modules", graph: g, stack: "s/two", want: []v1.ModuleConsume{}},
		{name: "unknown stack", graph: g, stack: "nope", want: []v1.ModuleConsume{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Empty(t, cmp.Diff(tt.want, StackModules(tt.graph, tt.stack)))
		})
	}
}
