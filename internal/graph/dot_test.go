package graph

import (
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata")

func golden(t *testing.T, name, got string) {
	t.Helper()
	file := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.WriteFile(file, []byte(got), 0o600))
	}
	want, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, string(want), got)
}

func dotExampleGraph() *v1.Graph {
	gitApps := "acme/modules//app@v2.1.0"
	return exampleGraph().
		git(gitApps, "v2.1.0").
		uses(prodApps, gitApps).
		external("acme/edge//stacks/prod/cdn", "acme/edge").
		dep("acme/edge//stacks/prod/cdn", prodApps).
		dep(stagingApps, "stacks/staging/missing").
		build()
}

func TestToDOTGolden(t *testing.T) {
	g := dotExampleGraph()
	resp, err := Resolve(g, Input{ChangedPaths: []string{"modules/vpc/main.tf"}, Config: exampleConfig()})
	require.NoError(t, err)
	highlight := map[string]int{}
	for _, a := range resp.Affected {
		highlight[a.Key] = a.Wave
	}
	golden(t, "example.dot", ToDOT(g, highlight))
	golden(t, "example-plain.dot", ToDOT(g, nil))
}

func TestToDOT(t *testing.T) {
	header := "digraph stackorder {\n  rankdir=BT;\n  fontname=\"Helvetica\";\n  node [fontname=\"Helvetica\", fontsize=11];\n  edge [fontname=\"Helvetica\", fontsize=9];\n"
	tests := []struct {
		name      string
		graph     *v1.Graph
		highlight map[string]int
		want      string
	}{
		{
			name: "nil graph",
			want: header + "}\n",
		},
		{
			name:  "empty graph has no label",
			graph: &v1.Graph{},
			want:  header + "}\n",
		},
		{
			name: "quotes, backslashes and newlines are escaped",
			graph: &v1.Graph{
				SHA:    "s",
				Stacks: []v1.Stack{{Key: "we\"ird\\key\nx"}},
			},
			want: header +
				"  label=\"s\";\n  labelloc=t;\n" +
				"  \"stack:we\\\"ird\\\\key\\nx\" [label=\"we\\\"ird\\\\key\\nx\", shape=box];\n" +
				"}\n",
		},
		{
			name: "wave colours wrap around and negative waves stay in range",
			graph: newGraph().stacks("a", "b", "c").
				external("x/y//z", "x/y").build(),
			highlight: map[string]int{"a": 6, "b": -1, "x/y//z": 0, "ghost": 3},
			want: header +
				"  label=\"acme/infra abc123\";\n  labelloc=t;\n" +
				"  \"stack:a\" [label=\"a\\nwave 6\", shape=box, fillcolor=\"#cfe8ff\", style=\"filled\"];\n" +
				"  \"stack:b\" [label=\"b\\nwave -1\", shape=box, fillcolor=\"#d2f3f0\", style=\"filled\"];\n" +
				"  \"stack:c\" [label=\"c\", shape=box];\n" +
				"  \"stack:x/y//z\" [label=\"x/y//z\\nwave 0\", shape=box, fillcolor=\"#cfe8ff\", style=\"dashed,filled\"];\n" +
				"}\n",
		},
		{
			name: "duplicate nodes and edges are drawn once and stacks and modules never collide",
			graph: newGraph().stacks("a", "a", "b").
				module(v1.Module{Key: "a", Kind: v1.ModuleLocal, Path: "a"}).
				module(v1.Module{Key: "a", Kind: v1.ModuleLocal, Path: "a"}).
				dep("b", "a").dep("b", "a").
				edge(v1.Edge{From: v1.StackRef("b"), To: v1.ModuleRef("a"), Type: v1.EdgeUsesModule}).
				build(),
			want: header +
				"  label=\"acme/infra abc123\";\n  labelloc=t;\n" +
				"  \"stack:a\" [label=\"a\", shape=box];\n" +
				"  \"stack:b\" [label=\"b\", shape=box];\n" +
				"  \"module:a\" [label=\"a\", shape=component];\n" +
				"  \"stack:b\" -> \"module:a\" [label=\"uses_module\", color=gray50, fontcolor=gray50];\n" +
				"  \"stack:b\" -> \"stack:a\" [label=\"depends_on\"];\n" +
				"}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ToDOT(tt.graph, tt.highlight))
		})
	}
}

func TestToDOTDeterministic(t *testing.T) {
	base := dotExampleGraph()
	highlight := map[string]int{prodVPC: 0, prodEKS: 1}
	want := ToDOT(base, highlight)
	rng := rand.New(rand.NewPCG(3, 5))
	for range 30 {
		g := *base
		g.Stacks = append([]v1.Stack(nil), base.Stacks...)
		g.Modules = append([]v1.Module(nil), base.Modules...)
		g.Edges = append([]v1.Edge(nil), base.Edges...)
		rng.Shuffle(len(g.Stacks), func(a, b int) { g.Stacks[a], g.Stacks[b] = g.Stacks[b], g.Stacks[a] })
		rng.Shuffle(len(g.Modules), func(a, b int) { g.Modules[a], g.Modules[b] = g.Modules[b], g.Modules[a] })
		rng.Shuffle(len(g.Edges), func(a, b int) { g.Edges[a], g.Edges[b] = g.Edges[b], g.Edges[a] })
		require.Equal(t, want, ToDOT(&g, highlight))
	}
}
