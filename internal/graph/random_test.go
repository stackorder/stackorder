package graph

import (
	"fmt"
	"math/rand/v2"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

type randomCase struct {
	graph *v1.Graph
	in    Input
}

func randomDAG(rng *rand.Rand) randomCase {
	dirs := 1 + rng.IntN(30)
	perm := rng.Perm(dirs)
	b := newGraph()
	var keys []string
	for i := range dirs {
		dir := fmt.Sprintf("stacks/s%02d", perm[i])
		if len(keys) > 0 && rng.IntN(6) == 0 {
			parent, _ := v1.SplitStackKey(keys[rng.IntN(len(keys))])
			dir = fmt.Sprintf("%s/n%02d", parent, perm[i])
		}
		switch rng.IntN(8) {
		case 0:
			keys = append(keys, dir+":blue")
		case 1:
			keys = append(keys, dir, dir+":blue", dir+":green")
		default:
			keys = append(keys, dir)
		}
	}
	n := len(keys)
	order := rng.Perm(n)
	shuffled := make([]string, n)
	for i, j := range order {
		shuffled[i] = keys[j]
	}
	keys = shuffled
	b.stacks(keys...)
	edgeProb := 1 + rng.IntN(5)
	for i := range n {
		for j := range i {
			if rng.IntN(10) >= edgeProb {
				continue
			}
			if rng.IntN(3) == 0 {
				b.reads(keys[i], keys[j])
			} else {
				b.dep(keys[i], keys[j])
			}
		}
	}
	m := rng.IntN(8)
	modules := make([]string, m)
	for i := range m {
		b.local(fmt.Sprintf("modules/m%02d", i))
		modules[i] = localKey(fmt.Sprintf("modules/m%02d", i))
		for j := range i {
			if rng.IntN(4) == 0 {
				b.uses(modules[i], modules[j])
			}
		}
	}
	gitKey := "acme/modules//shared@v1.0.0"
	b.git(gitKey, "v1.0.0")
	for _, k := range keys {
		for _, mod := range modules {
			if rng.IntN(6) == 0 {
				b.uses(k, mod)
			}
		}
		if rng.IntN(5) == 0 {
			b.uses(k, gitKey)
		}
	}
	for e := range rng.IntN(3) {
		dependent := fmt.Sprintf("acme/other//stacks/dependent%d", e)
		upstream := fmt.Sprintf("acme/other//stacks/upstream%d", e)
		b.external(dependent, "acme/other").external(upstream, "acme/other")
		b.dep(dependent, keys[rng.IntN(n)])
		b.reads(dependent, keys[rng.IntN(n)])
		b.dep(keys[rng.IntN(n)], upstream)
	}

	var paths []string
	for _, k := range keys {
		if rng.IntN(7) == 0 {
			dir, _ := v1.SplitStackKey(k)
			paths = append(paths, dir+"/main.tf")
		}
	}
	for i := range m {
		if rng.IntN(5) == 0 {
			paths = append(paths, fmt.Sprintf("modules/m%02d/variables.tf", i))
		}
	}
	paths = append(paths, "docs/guide.md", "shared/main.tf")
	cfg := config.Default()
	cfg.Propagate.Dependents = boolPtr(rng.IntN(4) != 0)
	cfg.Environments = map[string]string{"stacks/": "production"}
	in := Input{ChangedPaths: paths, Config: cfg}
	if rng.IntN(3) == 0 {
		for _, k := range keys {
			if rng.IntN(3) == 0 {
				in.Requested = append(in.Requested, k)
			}
		}
	}
	return randomCase{graph: b.build(), in: in}
}

type oracleResult struct {
	reasons   map[string][]v1.Reason
	via       map[string][]string
	scheduled []string
	domain    map[string]bool
	external  []string
}

func oracle(g *v1.Graph, in Input) oracleResult {
	local := map[string]bool{}
	external := map[string]bool{}
	for _, s := range g.Stacks {
		if s.External {
			external[s.Key] = true
		} else {
			local[s.Key] = true
		}
	}
	bits := map[string]reasonSet{}
	via := map[string]map[string]bool{}
	addVia := func(k, v string) {
		if via[k] == nil {
			via[k] = map[string]bool{}
		}
		via[k][v] = true
	}
	modules := map[string]bool{}
	for _, p := range in.ChangedPaths {
		if strings.HasSuffix(p, ".md") {
			continue
		}
		dir := path.Dir(p)
		for _, s := range g.Stacks {
			if !s.External && s.Path == dir {
				bits[s.Key] |= bitChanged
			}
		}
		for _, m := range g.Modules {
			if m.Kind == v1.ModuleLocal && m.Path == dir {
				modules[m.Key] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, e := range g.Edges {
			if e.Type == v1.EdgeUsesModule && e.From.Kind == v1.NodeModule && modules[e.To.Key] && !modules[e.From.Key] {
				modules[e.From.Key] = true
				changed = true
			}
		}
	}
	for _, e := range g.Edges {
		if e.Type == v1.EdgeUsesModule && e.From.Kind == v1.NodeStack && modules[e.To.Key] && local[e.From.Key] {
			bits[e.From.Key] |= bitModule
		}
	}
	for k, r := range bits {
		if r&bitModule == 0 {
			continue
		}
		reached := map[string]bool{}
		for changed := true; changed; {
			changed = false
			for _, e := range g.Edges {
				if e.Type != v1.EdgeUsesModule || !modules[e.To.Key] || reached[e.To.Key] {
					continue
				}
				if (e.From.Kind == v1.NodeStack && e.From.Key == k) || (e.From.Kind == v1.NodeModule && reached[e.From.Key]) {
					reached[e.To.Key] = true
					changed = true
				}
			}
		}
		for m := range reached {
			addVia(k, m)
		}
	}
	if in.Config.Propagate.DependentsEnabled() {
		for changed := true; changed; {
			changed = false
			for _, e := range g.Edges {
				if e.Type != v1.EdgeDependsOn && e.Type != v1.EdgeReadsState {
					continue
				}
				if _, ok := bits[e.To.Key]; !ok || !local[e.From.Key] || e.From.Key == e.To.Key {
					continue
				}
				bit := bitDependent
				if e.Type == v1.EdgeReadsState {
					bit = bitReadsState
				}
				if bits[e.From.Key]&bit == 0 || !via[e.From.Key][e.To.Key] {
					bits[e.From.Key] |= bit
					addVia(e.From.Key, e.To.Key)
					changed = true
				}
			}
		}
	}
	res := oracleResult{reasons: map[string][]v1.Reason{}, via: map[string][]string{}, domain: map[string]bool{}}
	var scheduled []string
	if len(in.Requested) == 0 {
		for k := range bits {
			scheduled = append(scheduled, k)
		}
	} else {
		for _, k := range in.Requested {
			if _, ok := bits[k]; !ok {
				bits[k] = bitRequested
			}
			scheduled = append(scheduled, k)
		}
	}
	for k := range bits {
		res.domain[k] = true
	}
	res.scheduled = uniqueSorted(scheduled)
	for _, k := range res.scheduled {
		res.reasons[k] = bits[k].list()
		res.via[k] = uniqueSorted(keysOf(via[k]))
	}
	for _, e := range g.Edges {
		if (e.Type == v1.EdgeDependsOn || e.Type == v1.EdgeReadsState) && external[e.From.Key] && slices.Contains(res.scheduled, e.To.Key) {
			res.external = append(res.external, e.From.Key)
		}
	}
	res.external = uniqueSorted(res.external)
	return res
}

func oracleWaves(g *v1.Graph, domain map[string]bool, scheduled []string) map[string]int {
	isScheduled := map[string]bool{}
	for _, k := range scheduled {
		isScheduled[k] = true
	}
	out := map[string][]string{}
	for _, e := range g.Edges {
		if (e.Type == v1.EdgeDependsOn || e.Type == v1.EdgeReadsState) && domain[e.From.Key] && domain[e.To.Key] {
			out[e.From.Key] = append(out[e.From.Key], e.To.Key)
		}
	}
	best := map[string]int{}
	var longest func(n string) int
	longest = func(n string) int {
		if v, ok := best[n]; ok {
			return v
		}
		v := 0
		for _, d := range out[n] {
			step := longest(d)
			if isScheduled[d] {
				step++
			}
			v = max(v, step)
		}
		best[n] = v
		return v
	}
	waves := map[string]int{}
	for _, k := range scheduled {
		waves[k] = longest(k)
	}
	return waves
}

func TestResolveRandomDAGs(t *testing.T) {
	rng := rand.New(rand.NewPCG(20260928, 1))
	for iter := range 400 {
		rc := randomDAG(rng)
		t.Run(fmt.Sprintf("dag%03d", iter), func(t *testing.T) {
			resp, err := Resolve(rc.graph, rc.in)
			require.NoError(t, err)
			want := oracle(rc.graph, rc.in)

			require.Equal(t, len(want.scheduled), len(resp.Affected))
			require.Empty(t, cmp.Diff(want.reasons, reasonsByKey(resp)), "reasons")
			gotVia := viaByKey(resp)
			for k, v := range want.via {
				require.Empty(t, cmp.Diff(v, gotVia[k]), "via of %s", k)
			}
			require.Empty(t, cmp.Diff(want.external, resp.External), "external")

			wantWave := oracleWaves(rc.graph, want.domain, want.scheduled)
			seen := map[string]bool{}
			for w, keys := range resp.Waves {
				require.NotEmpty(t, keys, "wave %d is empty", w)
				require.True(t, slices.IsSorted(keys), "wave %d is not sorted", w)
				for _, k := range keys {
					require.False(t, seen[k], "%s appears twice", k)
					seen[k] = true
					require.Equal(t, wantWave[k], w, "wave of %s", k)
				}
			}
			require.Len(t, seen, len(want.scheduled))
			assertWaveOrder(t, rc.graph, resp)
			require.True(t, slices.IsSortedFunc(resp.Affected, func(a, b v1.AffectedStack) int {
				if a.Wave != b.Wave {
					return a.Wave - b.Wave
				}
				return strings.Compare(a.Key, b.Key)
			}))
			require.Len(t, resp.Matrix.Include, len(resp.Affected))
			for i, e := range resp.Matrix.Include {
				require.Equal(t, resp.Affected[i].Key, e.Key)
				require.Equal(t, resp.Affected[i].Wave, e.Wave)
				require.Equal(t, rc.graph.SHA, e.SHA)
				require.Equal(t, "production", e.Environment)
			}
			if len(rc.in.Requested) == 0 {
				waves, cycles := Waves(rc.graph, affectedKeys(resp))
				require.Empty(t, cycles)
				if len(resp.Waves) == 0 {
					require.Empty(t, waves)
				} else {
					require.Equal(t, resp.Waves, waves)
				}
			}
			_, err = Validate(rc.graph)
			require.NoError(t, err)
		})
	}
}

func randomDigraph(rng *rand.Rand) (*v1.Graph, []string) {
	n := 1 + rng.IntN(12)
	b := newGraph()
	keys := make([]string, n)
	for i := range n {
		keys[i] = fmt.Sprintf("k%02d", i)
		b.stacks(keys[i])
	}
	edges := rng.IntN(2 * n)
	for range edges {
		from, to := keys[rng.IntN(n)], keys[rng.IntN(n)]
		if rng.IntN(2) == 0 {
			b.dep(from, to)
		} else {
			b.reads(from, to)
		}
	}
	return b.build(), keys
}

func reachability(g *v1.Graph, keys []string) map[string]map[string]bool {
	reach := map[string]map[string]bool{}
	for _, k := range keys {
		reach[k] = map[string]bool{}
	}
	for _, e := range g.Edges {
		reach[e.From.Key][e.To.Key] = true
	}
	for _, via := range keys {
		for _, from := range keys {
			if !reach[from][via] {
				continue
			}
			for _, to := range keys {
				if reach[via][to] {
					reach[from][to] = true
				}
			}
		}
	}
	return reach
}

func TestWavesRandomDigraphs(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 4242))
	for iter := range 500 {
		g, keys := randomDigraph(rng)
		t.Run(fmt.Sprintf("digraph%03d", iter), func(t *testing.T) {
			reach := reachability(g, keys)
			edge := map[[2]string]bool{}
			for _, e := range g.Edges {
				edge[[2]string{e.From.Key, e.To.Key}] = true
			}
			components := map[string]bool{}
			for _, k := range keys {
				if !reach[k][k] {
					continue
				}
				minKey := k
				for _, o := range keys {
					if reach[k][o] && reach[o][k] && o < minKey {
						minKey = o
					}
				}
				components[minKey] = true
			}

			waves, cycles := Waves(g, keys)
			require.Len(t, cycles, len(components))
			require.True(t, slices.IsSortedFunc(cycles, slices.Compare[[]string]))
			for _, c := range cycles {
				require.GreaterOrEqual(t, len(c), 2)
				require.Equal(t, c[0], c[len(c)-1])
				require.True(t, components[c[0]], "cycle %v does not start at the smallest key of its component", c)
				interior := c[:len(c)-1]
				require.Len(t, uniqueSorted(interior), len(interior), "cycle %v repeats a node", c)
				for i := 0; i+1 < len(c); i++ {
					require.True(t, edge[[2]string{c[i], c[i+1]}], "cycle %v uses a missing edge %s -> %s", c, c[i], c[i+1])
					require.True(t, reach[c[i]][c[0]] && reach[c[0]][c[i]], "cycle %v leaves its component", c)
				}
			}
			if len(components) > 0 {
				require.Nil(t, waves)
				return
			}
			wave := map[string]int{}
			count := 0
			for w, ks := range waves {
				for _, k := range ks {
					wave[k] = w
					count++
				}
			}
			require.Equal(t, len(keys), count)
			for _, e := range g.Edges {
				require.Less(t, wave[e.To.Key], wave[e.From.Key], "edge %s -> %s", e.From.Key, e.To.Key)
			}
			for _, k := range keys {
				deepest := -1
				for _, e := range g.Edges {
					if e.From.Key == k {
						deepest = max(deepest, wave[e.To.Key])
					}
				}
				require.Equal(t, deepest+1, wave[k], "wave of %s is not its longest path", k)
			}
		})
	}
}
