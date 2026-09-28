package report

import (
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// SummaryLine renders the compact form of a plan summary, "+3 ~1 -0", with
// " ±2" appended when resources are replaced.
func SummaryLine(s v1.PlanSummary) string {
	out := fmt.Sprintf("+%d ~%d -%d", s.Adds, s.Changes, s.Destroys)
	if s.Replaces > 0 {
		out += fmt.Sprintf(" ±%d", s.Replaces)
	}
	return out
}

// SummaryLong renders the sentence form of a plan summary, such as
// "3 to add, 1 to change, 0 to destroy", or "No changes" for an empty plan.
// Replacements, imports and moves are appended only when present.
func SummaryLong(s v1.PlanSummary) string { return longForm(s, false) }

func appliedLong(s v1.PlanSummary) string { return longForm(s, true) }

func longForm(s v1.PlanSummary, past bool) string {
	if s.Total() == 0 {
		if s.OutputChanges == 0 {
			return "No changes"
		}
		if past {
			return "No resource changes, " + plural(s.OutputChanges, "output") + " changed"
		}
		return "No resource changes, " + plural(s.OutputChanges, "output") + " to change"
	}
	verbs := [][2]string{{"to add", "added"}, {"to change", "changed"}, {"to destroy", "destroyed"}, {"to replace", "replaced"}, {"to import", "imported"}, {"to move", "moved"}}
	counts := []int{s.Adds, s.Changes, s.Destroys, s.Replaces, s.Imports, s.Moves}
	parts := make([]string, 0, len(counts))
	for i, n := range counts {
		if i >= 3 && n == 0 {
			continue
		}
		verb := verbs[i][0]
		if past {
			verb = verbs[i][1]
		}
		parts = append(parts, strconv.Itoa(n)+" "+verb)
	}
	return strings.Join(parts, ", ")
}

func lowerFirst(s string) string {
	if strings.HasPrefix(s, "No ") {
		return "no " + s[3:]
	}
	return s
}

func sumSummaries(stacks []v1.RunStack) (v1.PlanSummary, int) {
	var sum v1.PlanSummary
	n := 0
	for _, rs := range stacks {
		if rs.Summary == nil {
			continue
		}
		n++
		sum.Adds += rs.Summary.Adds
		sum.Changes += rs.Summary.Changes
		sum.Destroys += rs.Summary.Destroys
		sum.Replaces += rs.Summary.Replaces
		sum.Imports += rs.Summary.Imports
		sum.Moves += rs.Summary.Moves
		sum.OutputChanges += rs.Summary.OutputChanges
	}
	return sum, n
}

func countsTable(s v1.PlanSummary) string {
	cols := []string{"Add", "Change", "Destroy", "Replace"}
	vals := []int{s.Adds, s.Changes, s.Destroys, s.Replaces}
	for _, extra := range []struct {
		name string
		n    int
	}{{"Import", s.Imports}, {"Move", s.Moves}, {"Outputs", s.OutputChanges}} {
		if extra.n > 0 {
			cols = append(cols, extra.name)
			vals = append(vals, extra.n)
		}
	}
	var b strings.Builder
	b.WriteString("| " + strings.Join(cols, " | ") + " |\n|")
	for range cols {
		b.WriteString(" ---: |")
	}
	b.WriteString("\n|")
	for _, v := range vals {
		b.WriteString(" " + strconv.Itoa(v) + " |")
	}
	b.WriteString("\n")
	return b.String()
}

func hasAddresses(s *v1.PlanSummary) bool {
	return s != nil && len(s.Added)+len(s.Changed)+len(s.Destroyed)+len(s.Replaced) > 0
}

func addressLists(s *v1.PlanSummary, past bool) string {
	if !hasAddresses(s) {
		return ""
	}
	groups := []struct {
		future, past string
		addrs        []string
	}{
		{"To add", "Added", s.Added},
		{"To change", "Changed", s.Changed},
		{"To destroy", "Destroyed", s.Destroyed},
		{"To replace", "Replaced", s.Replaced},
	}
	var b strings.Builder
	for _, g := range groups {
		if len(g.addrs) == 0 {
			continue
		}
		label := g.future
		if past {
			label = g.past
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "**%s (%d)**\n\n", label, len(g.addrs))
		for _, a := range g.addrs {
			b.WriteString("- " + code(a) + "\n")
		}
	}
	return b.String()
}
