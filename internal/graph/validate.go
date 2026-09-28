package graph

import (
	"errors"
	"fmt"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Validate checks the structure of a whole graph. It returns an error
// wrapping ErrInvalidGraph for empty or duplicate node keys, unknown module
// kinds, unknown edge types, edges whose endpoints have the wrong kind for
// their type, self edges and dangling edge endpoints; the error also wraps
// ErrCycle when depends_on and reads_state edges form a cycle anywhere in the
// graph. A depends_on edge to an unknown stack, a stack key that does not
// match its path and workspace, a local module without a path and a cycle of
// modules are only warnings. Warnings are sorted.
func Validate(g *v1.Graph) (warnings []string, err error) {
	if g == nil {
		return nil, fmt.Errorf("%w: nil graph", ErrInvalidGraph)
	}
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	ix := newIndex(g)
	for i := range g.Stacks {
		s := &g.Stacks[i]
		if s.Key == "" {
			fail("stacks[%d]: empty key", i)
			continue
		}
		if !s.External && s.Path != "" && s.Key != v1.StackKey(s.Path, s.Workspace) {
			warn("stack %s: key does not match path %q and workspace %q", s.Key, s.Path, s.Workspace)
		}
	}
	for _, k := range ix.dupStacks {
		fail("stack %s: duplicate key", k)
	}
	for i := range g.Modules {
		m := &g.Modules[i]
		if m.Key == "" {
			fail("modules[%d]: empty key", i)
			continue
		}
		switch m.Kind {
		case v1.ModuleLocal:
			if repoPath(m.Path) == "" {
				warn("module %s: local module has no path, so changes to it are never detected", m.Key)
			}
		case v1.ModuleGit, v1.ModuleRegistry:
		default:
			fail("module %s: unknown kind %q", m.Key, m.Kind)
		}
	}
	for _, k := range ix.dupModules {
		fail("module %s: duplicate key", k)
	}

	stackDeps, moduleDeps := deps{}, deps{}
	for i := range g.Edges {
		e := &g.Edges[i]
		name := fmt.Sprintf("edge %s -> %s (%s)", describe(e.From), describe(e.To), e.Type)
		switch e.Type {
		case v1.EdgeDependsOn, v1.EdgeReadsState:
			if e.From.Kind != v1.NodeStack || e.To.Kind != v1.NodeStack {
				fail("%s: must connect two stacks", name)
				continue
			}
		case v1.EdgeUsesModule:
			if !isModuleUse(e) {
				fail("%s: must go from a stack or module to a module", name)
				continue
			}
		default:
			fail("%s: unknown edge type", name)
			continue
		}
		if e.From == e.To {
			fail("%s: self edge", name)
			continue
		}
		fromOK, toOK := ix.exists(e.From), ix.exists(e.To)
		if !fromOK {
			fail("%s: unknown %s", name, describe(e.From))
		}
		switch {
		case toOK:
		case e.Type == v1.EdgeDependsOn:
			warn("stack %s depends_on unknown stack %s", e.From.Key, e.To.Key)
		default:
			fail("%s: unknown %s", name, describe(e.To))
		}
		if !fromOK || !toOK {
			continue
		}
		if e.Type == v1.EdgeUsesModule {
			if e.From.Kind == v1.NodeModule {
				moduleDeps[e.From.Key] = append(moduleDeps[e.From.Key], e.To.Key)
			}
			continue
		}
		stackDeps[e.From.Key] = append(stackDeps[e.From.Key], e.To.Key)
	}
	stackDeps.normalize()
	moduleDeps.normalize()
	if cycles := findCycles(keysOf(ix.stacks), stackDeps); len(cycles) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s", ErrCycle, formatCycles(cycles)))
	}
	for _, c := range findCycles(keysOf(ix.modules), moduleDeps) {
		warn("module cycle: %s", formatCycles([][]string{c}))
	}
	warnings = uniqueSorted(warnings)
	if len(errs) > 0 {
		return warnings, fmt.Errorf("%w: %w", ErrInvalidGraph, errors.Join(errs...))
	}
	return warnings, nil
}

func (ix *index) exists(ref v1.NodeRef) bool {
	switch ref.Kind {
	case v1.NodeStack:
		_, ok := ix.stacks[ref.Key]
		return ok
	case v1.NodeModule:
		_, ok := ix.modules[ref.Key]
		return ok
	}
	return false
}

func describe(ref v1.NodeRef) string {
	if ref.Kind == "" {
		return "node " + ref.Key
	}
	return string(ref.Kind) + " " + ref.Key
}
