package graph

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

var wavePalette = []string{"#cfe8ff", "#d4f5d4", "#fff1c2", "#ffd9d2", "#e6d9ff", "#d2f3f0"}

// ToDOT renders g in Graphviz DOT. Stacks are boxes, modules are components,
// external stacks are dashed boxes and edge endpoints missing from the graph
// are dotted. Edges keep the api/v1 direction, from dependent to dependency,
// with rankdir=BT so dependencies are drawn above their dependents; inferred
// edges are dashed. Stacks named in highlight are filled with a colour per
// wave and labelled with their wave. The output is deterministic.
func ToDOT(g *v1.Graph, highlight map[string]int) string {
	var b strings.Builder
	b.WriteString("digraph stackorder {\n")
	b.WriteString("  rankdir=BT;\n")
	b.WriteString("  fontname=\"Helvetica\";\n")
	b.WriteString("  node [fontname=\"Helvetica\", fontsize=11];\n")
	b.WriteString("  edge [fontname=\"Helvetica\", fontsize=9];\n")
	if g == nil {
		b.WriteString("}\n")
		return b.String()
	}
	if g.Repo != "" || g.SHA != "" {
		fmt.Fprintf(&b, "  label=%s;\n  labelloc=t;\n", quote(strings.TrimSpace(g.Repo+" "+g.SHA)))
	}
	declared := map[v1.NodeRef]bool{}
	stacks := slices.Clone(g.Stacks)
	slices.SortStableFunc(stacks, func(a, b v1.Stack) int { return cmp.Compare(a.Key, b.Key) })
	for _, s := range stacks {
		ref := v1.StackRef(s.Key)
		if declared[ref] {
			continue
		}
		declared[ref] = true
		label := s.Key
		attrs := []string{"shape=box"}
		var styles []string
		if s.External {
			styles = append(styles, "dashed")
		}
		if w, ok := highlight[s.Key]; ok {
			label = fmt.Sprintf("%s\nwave %d", s.Key, w)
			styles = append(styles, "filled")
			attrs = append(attrs, "fillcolor="+quote(wavePalette[((w%len(wavePalette))+len(wavePalette))%len(wavePalette)]))
		}
		if len(styles) > 0 {
			attrs = append(attrs, "style="+quote(strings.Join(styles, ",")))
		}
		writeNode(&b, ref, label, attrs)
	}
	modules := slices.Clone(g.Modules)
	slices.SortStableFunc(modules, func(a, b v1.Module) int { return cmp.Compare(a.Key, b.Key) })
	for _, m := range modules {
		ref := v1.ModuleRef(m.Key)
		if declared[ref] {
			continue
		}
		declared[ref] = true
		writeNode(&b, ref, m.Key, []string{"shape=component"})
	}
	edges := slices.Clone(g.Edges)
	slices.SortStableFunc(edges, compareEdges)
	edges = slices.CompactFunc(edges, func(a, b v1.Edge) bool { return compareEdges(a, b) == 0 })
	var dangling []v1.NodeRef
	for _, e := range edges {
		for _, ref := range []v1.NodeRef{e.From, e.To} {
			if !declared[ref] {
				declared[ref] = true
				dangling = append(dangling, ref)
			}
		}
	}
	slices.SortFunc(dangling, compareRefs)
	for _, ref := range dangling {
		writeNode(&b, ref, ref.Key, []string{"shape=box", "style=dotted"})
	}
	for _, e := range edges {
		attrs := []string{"label=" + quote(string(e.Type))}
		if e.Type == v1.EdgeUsesModule {
			attrs = append(attrs, "color=gray50", "fontcolor=gray50")
		}
		if e.Inferred {
			attrs = append(attrs, "style=dashed")
		}
		fmt.Fprintf(&b, "  %s -> %s [%s];\n", nodeID(e.From), nodeID(e.To), strings.Join(attrs, ", "))
	}
	b.WriteString("}\n")
	return b.String()
}

func writeNode(b *strings.Builder, ref v1.NodeRef, label string, attrs []string) {
	all := append([]string{"label=" + quote(label)}, attrs...)
	fmt.Fprintf(b, "  %s [%s];\n", nodeID(ref), strings.Join(all, ", "))
}

func nodeID(ref v1.NodeRef) string {
	return quote(string(ref.Kind) + ":" + ref.Key)
}

var dotEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", "")

func quote(s string) string {
	return `"` + dotEscaper.Replace(s) + `"`
}
