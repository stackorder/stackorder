package graph

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestWaves(t *testing.T) {
	tests := []struct {
		name       string
		graph      *v1.Graph
		keys       []string
		wantWaves  [][]string
		wantCycles [][]string
	}{
		{
			name:  "no keys",
			graph: newGraph().stacks("a").build(),
		},
		{
			name:      "nil graph layers keys as roots",
			keys:      []string{"b", "a"},
			wantWaves: [][]string{{"a", "b"}},
		},
		{
			name:      "unknown and empty keys are roots or dropped",
			graph:     newGraph().stacks("a").build(),
			keys:      []string{"a", "", "ghost", "a"},
			wantWaves: [][]string{{"a", "ghost"}},
		},
		{
			name:      "chain",
			graph:     newGraph().stacks("a", "b", "c", "d").dep("b", "a").dep("c", "b").dep("d", "c").build(),
			keys:      []string{"d", "c", "b", "a"},
			wantWaves: [][]string{{"a"}, {"b"}, {"c"}, {"d"}},
		},
		{
			name:      "reads_state orders like depends_on",
			graph:     newGraph().stacks("a", "b", "c").reads("b", "a").dep("c", "b").build(),
			keys:      []string{"a", "b", "c"},
			wantWaves: [][]string{{"a"}, {"b"}, {"c"}},
		},
		{
			name:      "diamond",
			graph:     newGraph().stacks("a", "b", "c", "d").dep("b", "a").dep("c", "a").dep("d", "b").dep("d", "c").build(),
			keys:      []string{"a", "b", "c", "d"},
			wantWaves: [][]string{{"a"}, {"b", "c"}, {"d"}},
		},
		{
			name:      "longest path wins over a shortcut",
			graph:     newGraph().stacks("a", "b", "c", "d").dep("d", "a").dep("b", "a").dep("c", "b").dep("d", "c").build(),
			keys:      []string{"a", "b", "c", "d"},
			wantWaves: [][]string{{"a"}, {"b"}, {"c"}, {"d"}},
		},
		{
			name:      "disconnected components",
			graph:     newGraph().stacks("a", "b", "x", "y", "z", "lonely").dep("b", "a").dep("y", "x").dep("z", "y").build(),
			keys:      []string{"a", "b", "x", "y", "z", "lonely"},
			wantWaves: [][]string{{"a", "lonely", "x"}, {"b", "y"}, {"z"}},
		},
		{
			name:      "edges leaving the key set are dropped",
			graph:     newGraph().stacks("a", "b", "c").dep("b", "a").dep("c", "b").build(),
			keys:      []string{"a", "c"},
			wantWaves: [][]string{{"a", "c"}},
		},
		{
			name: "uses_module and malformed edges do not order",
			graph: newGraph().stacks("a", "b").local("modules/m").uses("a", localKey("modules/m")).
				edge(v1.Edge{From: v1.StackRef("b"), To: v1.StackRef("a"), Type: "bogus"}).
				edge(v1.Edge{From: v1.ModuleRef("b"), To: v1.StackRef("a"), Type: v1.EdgeDependsOn}).
				build(),
			keys:      []string{"a", "b"},
			wantWaves: [][]string{{"a", "b"}},
		},
		{
			name:       "self-loop",
			graph:      newGraph().stacks("a", "b").dep("a", "a").dep("b", "a").build(),
			keys:       []string{"a", "b"},
			wantCycles: [][]string{{"a", "a"}},
		},
		{
			name:       "two-cycle",
			graph:      newGraph().stacks("b", "a").dep("b", "a").dep("a", "b").build(),
			keys:       []string{"a", "b"},
			wantCycles: [][]string{{"a", "b", "a"}},
		},
		{
			name:       "three-cycle spelled in edge direction",
			graph:      newGraph().stacks("a", "b", "c").dep("a", "c").dep("c", "b").dep("b", "a").build(),
			keys:       []string{"c", "b", "a"},
			wantCycles: [][]string{{"a", "c", "b", "a"}},
		},
		{
			name:       "three-cycle with a mixed edge type",
			graph:      newGraph().stacks("a", "b", "c").dep("a", "b").reads("b", "c").dep("c", "a").build(),
			keys:       []string{"a", "b", "c"},
			wantCycles: [][]string{{"a", "b", "c", "a"}},
		},
		{
			name: "shortest cycle through the smallest key in a component",
			graph: newGraph().stacks("a", "b", "c", "d").
				dep("a", "b").dep("b", "c").dep("c", "d").dep("d", "a").dep("b", "a").build(),
			keys:       []string{"a", "b", "c", "d"},
			wantCycles: [][]string{{"a", "b", "a"}},
		},
		{
			name: "several cycles are sorted",
			graph: newGraph().stacks("a", "b", "x", "y", "z", "free").
				dep("y", "x").dep("x", "y").dep("b", "a").dep("a", "b").dep("z", "z").dep("free", "a").build(),
			keys:       []string{"a", "b", "x", "y", "z", "free"},
			wantCycles: [][]string{{"a", "b", "a"}, {"x", "y", "x"}, {"z", "z"}},
		},
		{
			name:      "cycle outside the key set is ignored",
			graph:     newGraph().stacks("a", "b", "c").dep("b", "a").dep("a", "b").dep("c", "a").build(),
			keys:      []string{"a", "c"},
			wantWaves: [][]string{{"a"}, {"c"}},
		},
		{
			name:      "duplicate edges count once",
			graph:     newGraph().stacks("a", "b").dep("b", "a").dep("b", "a").reads("b", "a").build(),
			keys:      []string{"a", "b"},
			wantWaves: [][]string{{"a"}, {"b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waves, cycles := Waves(tt.graph, tt.keys)
			require.Empty(t, cmp.Diff(tt.wantWaves, waves), "waves")
			require.Empty(t, cmp.Diff(tt.wantCycles, cycles), "cycles")
		})
	}
}

func TestFormatCycles(t *testing.T) {
	tests := []struct {
		name   string
		cycles [][]string
		want   string
	}{
		{name: "none", want: ""},
		{name: "one", cycles: [][]string{{"a", "b", "a"}}, want: "a -> b -> a"},
		{name: "several", cycles: [][]string{{"a", "a"}, {"x", "y", "z", "x"}}, want: "a -> a; x -> y -> z -> x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, formatCycles(tt.cycles))
		})
	}
}

func TestClosure(t *testing.T) {
	d := deps{
		"apps": {"eks"},
		"eks":  {"vpc"},
		"dns":  {"vpc"},
		"web":  {"apps", "dns"},
	}
	member := map[string]bool{"vpc": true, "apps": true, "web": true}
	got := closure([]string{"apps", "vpc", "web"}, member, d)
	want := deps{"apps": {"vpc"}, "web": {"apps", "vpc"}}
	require.Empty(t, cmp.Diff(want, got))
}
