package graph

import (
	"errors"
	"slices"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// ErrCycle is returned, wrapped, when the stacks to be ordered contain a
// dependency cycle over depends_on and reads_state edges.
var ErrCycle = errors.New("dependency cycle")

type deps map[string][]string

// Waves layers keys by longest path over the depends_on and reads_state
// edges of g whose endpoints are both in keys: a stack with no dependency
// among keys is in wave 0, any other stack is one wave after its latest
// dependency. Keys that are not in g form their own roots. When the edges
// contain a cycle, waves is nil and cycles spells every cycle found.
func Waves(g *v1.Graph, keys []string) (waves [][]string, cycles [][]string) {
	nodes := uniqueSorted(slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == "" }))
	in := make(map[string]bool, len(nodes))
	for _, k := range nodes {
		in[k] = true
	}
	d := deps{}
	if g != nil {
		for i := range g.Edges {
			e := &g.Edges[i]
			if isOrdering(e) && in[e.From.Key] && in[e.To.Key] {
				d[e.From.Key] = append(d[e.From.Key], e.To.Key)
			}
		}
	}
	d.normalize()
	if cycles := findCycles(nodes, d); len(cycles) > 0 {
		return nil, cycles
	}
	_, waves = layer(nodes, d)
	return waves, nil
}

func (d deps) normalize() {
	for k, v := range d {
		d[k] = uniqueSorted(v)
	}
}

func layer(nodes []string, d deps) (map[string]int, [][]string) {
	remaining := make(map[string]int, len(nodes))
	dependents := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		remaining[n] = len(d[n])
		for _, dep := range d[n] {
			dependents[dep] = append(dependents[dep], n)
		}
	}
	wave := make(map[string]int, len(nodes))
	queue := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if remaining[n] == 0 {
			queue = append(queue, n)
		}
	}
	maxWave := -1
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		maxWave = max(maxWave, wave[n])
		for _, m := range dependents[n] {
			wave[m] = max(wave[m], wave[n]+1)
			remaining[m]--
			if remaining[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	if maxWave < 0 {
		return wave, nil
	}
	waves := make([][]string, maxWave+1)
	for _, n := range nodes {
		waves[wave[n]] = append(waves[wave[n]], n)
	}
	return wave, waves
}

func findCycles(nodes []string, d deps) [][]string {
	t := tarjan{d: d, index: map[string]int{}, low: map[string]int{}, onStack: map[string]bool{}}
	for _, n := range nodes {
		if _, seen := t.index[n]; !seen {
			t.visit(n)
		}
	}
	var cycles [][]string
	for _, scc := range t.sccs {
		if len(scc) == 1 && !slices.Contains(d[scc[0]], scc[0]) {
			continue
		}
		cycles = append(cycles, spellCycle(scc, d))
	}
	slices.SortFunc(cycles, slices.Compare)
	return cycles
}

type tarjan struct {
	d       deps
	next    int
	index   map[string]int
	low     map[string]int
	onStack map[string]bool
	stack   []string
	sccs    [][]string
}

func (t *tarjan) visit(n string) {
	t.index[n] = t.next
	t.low[n] = t.next
	t.next++
	t.stack = append(t.stack, n)
	t.onStack[n] = true
	for _, m := range t.d[n] {
		if _, seen := t.index[m]; !seen {
			t.visit(m)
			t.low[n] = min(t.low[n], t.low[m])
		} else if t.onStack[m] {
			t.low[n] = min(t.low[n], t.index[m])
		}
	}
	if t.low[n] != t.index[n] {
		return
	}
	var scc []string
	for {
		m := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.onStack[m] = false
		scc = append(scc, m)
		if m == n {
			break
		}
	}
	slices.Sort(scc)
	t.sccs = append(t.sccs, scc)
}

func spellCycle(scc []string, d deps) []string {
	start := scc[0]
	member := make(map[string]bool, len(scc))
	for _, n := range scc {
		member[n] = true
	}
	parent := map[string]string{}
	last := start
	for queue := []string{start}; len(queue) > 0; {
		n := queue[0]
		queue = queue[1:]
		if slices.Contains(d[n], start) {
			last = n
			break
		}
		for _, m := range d[n] {
			if _, seen := parent[m]; member[m] && m != start && !seen {
				parent[m] = n
				queue = append(queue, m)
			}
		}
	}
	cycle := []string{start}
	for c := last; c != start; c = parent[c] {
		cycle = append(cycle, c)
	}
	slices.Reverse(cycle[1:])
	return append(cycle, start)
}

func closure(scheduled []string, member map[string]bool, d deps) deps {
	out := make(deps, len(scheduled))
	for _, x := range scheduled {
		seen := map[string]bool{x: true}
		stack := slices.Clone(d[x])
		var found []string
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[n] {
				continue
			}
			seen[n] = true
			if member[n] {
				found = append(found, n)
				continue
			}
			stack = append(stack, d[n]...)
		}
		if len(found) > 0 {
			out[x] = uniqueSorted(found)
		}
	}
	return out
}

func formatCycles(cycles [][]string) string {
	parts := make([]string, len(cycles))
	for i, c := range cycles {
		parts[i] = strings.Join(c, " -> ")
	}
	return strings.Join(parts, "; ")
}
