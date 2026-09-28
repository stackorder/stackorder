package graph_test

import (
	"fmt"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/graph"
)

func ExampleResolve() {
	stack := func(key string) v1.Stack { return v1.Stack{Key: key, Path: key} }
	dep := func(t v1.EdgeType, from, to string) v1.Edge {
		return v1.Edge{From: v1.StackRef(from), To: v1.StackRef(to), Type: t, Inferred: t == v1.EdgeReadsState}
	}
	uses := func(from, to string) v1.Edge {
		return v1.Edge{From: v1.StackRef(from), To: v1.ModuleRef(to), Type: v1.EdgeUsesModule}
	}
	g := &v1.Graph{
		Repo: "acme/infra",
		SHA:  "4f2a9c1",
		Stacks: []v1.Stack{
			stack("stacks/prod/vpc"), stack("stacks/staging/vpc"), stack("stacks/prod/eks"),
			stack("stacks/prod/apps"), stack("stacks/staging/apps"),
		},
		Modules: []v1.Module{
			{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc"},
			{Key: "acme/infra//modules/eks", Kind: v1.ModuleLocal, Path: "modules/eks"},
		},
		Edges: []v1.Edge{
			uses("stacks/prod/vpc", "acme/infra//modules/vpc"),
			uses("stacks/staging/vpc", "acme/infra//modules/vpc"),
			uses("stacks/prod/eks", "acme/infra//modules/eks"),
			dep(v1.EdgeDependsOn, "stacks/prod/eks", "stacks/prod/vpc"),
			dep(v1.EdgeDependsOn, "stacks/staging/apps", "stacks/staging/vpc"),
			dep(v1.EdgeReadsState, "stacks/prod/apps", "stacks/prod/eks"),
		},
	}
	cfg := config.Default()
	cfg.Environments = map[string]string{"stacks/prod/": "production", "stacks/staging/": "staging"}

	resp, err := graph.Resolve(g, graph.Input{ChangedPaths: []string{"modules/vpc/main.tf"}, Config: cfg})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, a := range resp.Affected {
		fmt.Printf("wave %d  %-20s %-11s %v via %v\n", a.Wave, a.Key, a.Environment, a.Reasons, a.Via)
	}
	// Output:
	// wave 0  stacks/prod/vpc      production  [module] via [acme/infra//modules/vpc]
	// wave 0  stacks/staging/vpc   staging     [module] via [acme/infra//modules/vpc]
	// wave 1  stacks/prod/eks      production  [dependent] via [stacks/prod/vpc]
	// wave 1  stacks/staging/apps  staging     [dependent] via [stacks/staging/vpc]
	// wave 2  stacks/prod/apps     production  [reads_state] via [stacks/prod/eks]
}

func ExampleWaves() {
	g := &v1.Graph{
		Stacks: []v1.Stack{{Key: "a"}, {Key: "b"}, {Key: "c"}},
		Edges: []v1.Edge{
			{From: v1.StackRef("a"), To: v1.StackRef("b"), Type: v1.EdgeDependsOn},
			{From: v1.StackRef("b"), To: v1.StackRef("c"), Type: v1.EdgeDependsOn},
			{From: v1.StackRef("c"), To: v1.StackRef("a"), Type: v1.EdgeReadsState},
		},
	}
	fmt.Println(graph.Waves(g, []string{"a", "b"}))
	fmt.Println(graph.Waves(g, []string{"a", "b", "c"}))
	// Output:
	// [[b] [a]] []
	// [] [[a b c a]]
}
