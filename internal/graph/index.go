package graph

import (
	"cmp"
	"path"
	"slices"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

type index struct {
	stacks     map[string]*v1.Stack
	modules    map[string]*v1.Module
	out        map[v1.NodeRef][]*v1.Edge
	in         map[v1.NodeRef][]*v1.Edge
	dupStacks  []string
	dupModules []string
}

func newIndex(g *v1.Graph) *index {
	ix := &index{
		stacks:  make(map[string]*v1.Stack, len(g.Stacks)),
		modules: make(map[string]*v1.Module, len(g.Modules)),
		out:     make(map[v1.NodeRef][]*v1.Edge),
		in:      make(map[v1.NodeRef][]*v1.Edge),
	}
	for i := range g.Stacks {
		s := &g.Stacks[i]
		if s.Key == "" {
			continue
		}
		if _, dup := ix.stacks[s.Key]; dup {
			ix.dupStacks = append(ix.dupStacks, s.Key)
			continue
		}
		ix.stacks[s.Key] = s
	}
	for i := range g.Modules {
		m := &g.Modules[i]
		if m.Key == "" {
			continue
		}
		if _, dup := ix.modules[m.Key]; dup {
			ix.dupModules = append(ix.dupModules, m.Key)
			continue
		}
		ix.modules[m.Key] = m
	}
	for i := range g.Edges {
		e := &g.Edges[i]
		ix.out[e.From] = append(ix.out[e.From], e)
		ix.in[e.To] = append(ix.in[e.To], e)
	}
	ix.dupStacks = uniqueSorted(ix.dupStacks)
	ix.dupModules = uniqueSorted(ix.dupModules)
	return ix
}

func (ix *index) localStack(key string) *v1.Stack {
	s := ix.stacks[key]
	if s == nil || !schedulable(s) {
		return nil
	}
	return s
}

func (ix *index) stackDirs() map[string][]string {
	dirs := make(map[string][]string)
	for key, s := range ix.stacks {
		if schedulable(s) {
			d := stackDir(s)
			dirs[d] = append(dirs[d], key)
		}
	}
	return dirs
}

func schedulable(s *v1.Stack) bool {
	p, _ := v1.SplitStackKey(s.Key)
	return !s.External && p != "" && p == repoPath(p) && stackDir(s) != ""
}

func (ix *index) moduleDirs() map[string][]string {
	dirs := make(map[string][]string)
	for key, m := range ix.modules {
		if m.Kind != v1.ModuleLocal {
			continue
		}
		if d := repoPath(m.Path); d != "" {
			dirs[d] = append(dirs[d], key)
		}
	}
	return dirs
}

func isOrdering(e *v1.Edge) bool {
	return (e.Type == v1.EdgeDependsOn || e.Type == v1.EdgeReadsState) &&
		e.From.Kind == v1.NodeStack && e.To.Kind == v1.NodeStack
}

func isModuleUse(e *v1.Edge) bool {
	return e.Type == v1.EdgeUsesModule && e.To.Kind == v1.NodeModule &&
		(e.From.Kind == v1.NodeStack || e.From.Kind == v1.NodeModule)
}

func cleanPath(p string) string {
	p = strings.TrimLeft(strings.ReplaceAll(p, "\\", "/"), "/")
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return p
}

func outsideRepo(p string) bool {
	return p == ".." || strings.HasPrefix(p, "../")
}

func repoPath(p string) string {
	p = cleanPath(p)
	if outsideRepo(p) {
		return ""
	}
	return p
}

func stackDir(s *v1.Stack) string {
	p := s.Path
	if p == "" {
		p, _ = v1.SplitStackKey(s.Key)
	}
	return repoPath(p)
}

func hasSegment(p, segment string) bool {
	for part := range strings.SplitSeq(p, "/") {
		if part == segment {
			return true
		}
	}
	return false
}

func deepest(dirs map[string][]string, p string) []string {
	for d := p; ; d = path.Dir(d) {
		if keys, ok := dirs[d]; ok {
			return keys
		}
		if !strings.Contains(d, "/") {
			return nil
		}
	}
}

func uniqueSorted(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := slices.Clone(keys)
	slices.Sort(out)
	return slices.Compact(out)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func compareRefs(a, b v1.NodeRef) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Key, b.Key))
}

func compareEdges(a, b v1.Edge) int {
	return cmp.Or(
		compareRefs(a.From, b.From),
		compareRefs(a.To, b.To),
		cmp.Compare(a.Type, b.Type),
		compareBool(a.Inferred, b.Inferred),
	)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}
