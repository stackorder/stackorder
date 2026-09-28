package graph

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	largeStacks  = 300
	largeModules = 40
	largeEdges   = 2000
)

func largeGraph() (*v1.Graph, Input) {
	rng := rand.New(rand.NewPCG(300, 2000))
	b := newGraph()
	stacks := make([]string, largeStacks)
	for i := range stacks {
		env := "prod"
		if i%2 == 1 {
			env = "staging"
		}
		stacks[i] = fmt.Sprintf("stacks/%s/s%03d", env, i)
		b.stacks(stacks[i])
	}
	modules := make([]string, largeModules)
	for i := range modules {
		dir := fmt.Sprintf("modules/m%02d", i)
		b.local(dir)
		modules[i] = localKey(dir)
	}
	seen := map[[2]string]bool{}
	for len(b.g.Edges) < largeEdges {
		switch rng.IntN(10) {
		case 0, 1:
			s, m := stacks[rng.IntN(largeStacks)], modules[rng.IntN(largeModules)]
			if !seen[[2]string{s, m}] {
				seen[[2]string{s, m}] = true
				b.uses(s, m)
			}
		case 2:
			i, j := rng.IntN(largeModules), rng.IntN(largeModules)
			if i > j && !seen[[2]string{modules[i], modules[j]}] {
				seen[[2]string{modules[i], modules[j]}] = true
				b.uses(modules[i], modules[j])
			}
		default:
			i, j := rng.IntN(largeStacks), rng.IntN(largeStacks)
			if i <= j || seen[[2]string{stacks[i], stacks[j]}] {
				continue
			}
			seen[[2]string{stacks[i], stacks[j]}] = true
			if rng.IntN(4) == 0 {
				b.reads(stacks[i], stacks[j])
			} else {
				b.dep(stacks[i], stacks[j])
			}
		}
	}
	cfg := exampleConfig()
	in := Input{
		ChangedPaths: []string{
			"modules/m00/main.tf",
			"modules/m17/variables.tf",
			stacks[3] + "/main.tf",
			stacks[150] + "/outputs.tf",
			"README.md",
		},
		Config: cfg,
	}
	return b.build(), in
}

func TestResolveLargeGraph(t *testing.T) {
	g, in := largeGraph()
	require.Len(t, g.Stacks, largeStacks)
	require.Len(t, g.Edges, largeEdges)
	_, err := Validate(g)
	require.NoError(t, err)
	resp, err := Resolve(g, in)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Affected)
	require.Len(t, resp.Matrix.Include, len(resp.Affected))
	assertWaveOrder(t, g, resp)

	in.Requested = []string{"stacks/prod/s298", "stacks/staging/s001", "stacks/prod/s150"}
	resp, err = Resolve(g, in)
	require.NoError(t, err)
	require.Len(t, resp.Affected, 3)
	assertWaveOrder(t, g, resp)
}

func BenchmarkResolve(b *testing.B) {
	g, in := largeGraph()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Resolve(g, in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResolveRequested(b *testing.B) {
	g, in := largeGraph()
	in.Requested = []string{"stacks/prod/s298", "stacks/staging/s001", "stacks/prod/s150", "stacks/prod/s200"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Resolve(g, in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidate(b *testing.B) {
	g, _ := largeGraph()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Validate(g); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkToDOT(b *testing.B) {
	g, _ := largeGraph()
	b.ReportAllocs()
	for b.Loop() {
		_ = ToDOT(g, nil)
	}
}
