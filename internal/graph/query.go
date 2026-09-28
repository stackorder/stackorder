package graph

import (
	"slices"

	v1 "github.com/stackorder/stackorder/api/v1"
)

var defaultTraversal = []v1.EdgeType{v1.EdgeDependsOn, v1.EdgeReadsState}

// Dependents returns the keys of every node that transitively depends on one
// of keys over edges of the given types, following edges backwards. With no
// types it follows depends_on and reads_state. The result is sorted and
// excludes the input keys; keys may name stacks or modules.
func Dependents(g *v1.Graph, keys []string, types ...v1.EdgeType) []string {
	return reach(g, keys, types, true)
}

// Dependencies returns the keys of every node that one of keys transitively
// depends on over edges of the given types, following edges forwards. With no
// types it follows depends_on and reads_state. The result is sorted and
// excludes the input keys; keys may name stacks or modules.
func Dependencies(g *v1.Graph, keys []string, types ...v1.EdgeType) []string {
	return reach(g, keys, types, false)
}

func reach(g *v1.Graph, keys []string, types []v1.EdgeType, backwards bool) []string {
	if g == nil || len(keys) == 0 {
		return nil
	}
	if len(types) == 0 {
		types = defaultTraversal
	}
	adj := map[v1.NodeRef][]v1.NodeRef{}
	for i := range g.Edges {
		e := &g.Edges[i]
		if !slices.Contains(types, e.Type) {
			continue
		}
		if backwards {
			adj[e.To] = append(adj[e.To], e.From)
		} else {
			adj[e.From] = append(adj[e.From], e.To)
		}
	}
	input := make(map[string]bool, len(keys))
	seen := map[v1.NodeRef]bool{}
	var queue []v1.NodeRef
	for _, k := range keys {
		input[k] = true
		for _, ref := range []v1.NodeRef{v1.StackRef(k), v1.ModuleRef(k)} {
			if !seen[ref] {
				seen[ref] = true
				queue = append(queue, ref)
			}
		}
	}
	var out []string
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, m := range adj[n] {
			if seen[m] {
				continue
			}
			seen[m] = true
			queue = append(queue, m)
			if !input[m.Key] {
				out = append(out, m.Key)
			}
		}
	}
	return uniqueSorted(out)
}

// ModuleConsumers returns the sorted keys of every stack that uses the module
// moduleKey, directly or through any number of nested modules.
func ModuleConsumers(g *v1.Graph, moduleKey string) []string {
	if g == nil {
		return nil
	}
	ix := newIndex(g)
	seen := map[string]bool{moduleKey: true}
	queue := []string{moduleKey}
	var out []string
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		for _, e := range ix.in[v1.ModuleRef(m)] {
			if !isModuleUse(e) {
				continue
			}
			if e.From.Kind == v1.NodeStack {
				out = append(out, e.From.Key)
			} else if !seen[e.From.Key] {
				seen[e.From.Key] = true
				queue = append(queue, e.From.Key)
			}
		}
	}
	return uniqueSorted(out)
}

// StackModules returns every module the stack stackKey uses, directly or
// through nested modules, sorted by module key. Ref is the module's pinned
// ref, or the "ref" meta of the edge that reaches it when the module has
// none; Latest and Behind are left for the caller to fill from recorded
// module versions.
func StackModules(g *v1.Graph, stackKey string) []v1.ModuleConsume {
	if g == nil {
		return nil
	}
	ix := newIndex(g)
	refs := map[string]string{}
	queue := []v1.NodeRef{v1.StackRef(stackKey)}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		edges := slices.Clone(ix.out[n])
		slices.SortFunc(edges, func(a, b *v1.Edge) int { return compareEdges(*a, *b) })
		for _, e := range edges {
			if !isModuleUse(e) {
				continue
			}
			if _, seen := refs[e.To.Key]; seen {
				continue
			}
			ref := e.Meta["ref"]
			if m := ix.modules[e.To.Key]; m != nil && m.Ref != "" {
				ref = m.Ref
			}
			refs[e.To.Key] = ref
			queue = append(queue, e.To)
		}
	}
	out := make([]v1.ModuleConsume, 0, len(refs))
	for _, key := range keysOf(refs) {
		out = append(out, v1.ModuleConsume{ModuleKey: key, Ref: refs[key]})
	}
	return out
}
