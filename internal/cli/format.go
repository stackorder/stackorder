package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const maxSummaryAddresses = 50

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("writing JSON: %w", err)
	}
	return nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func summaryLine(s *v1.PlanSummary) string {
	if s == nil {
		return "no plan summary"
	}
	if s.Empty() {
		return "no changes"
	}
	parts := []string{
		fmt.Sprintf("%d to add", s.Adds),
		fmt.Sprintf("%d to change", s.Changes),
		fmt.Sprintf("%d to destroy", s.Destroys),
		fmt.Sprintf("%d to replace", s.Replaces),
	}
	if s.Imports > 0 {
		parts = append(parts, fmt.Sprintf("%d to import", s.Imports))
	}
	if s.Moves > 0 {
		parts = append(parts, fmt.Sprintf("%d moved", s.Moves))
	}
	if s.OutputChanges > 0 {
		parts = append(parts, fmt.Sprintf("%d output changes", s.OutputChanges))
	}
	return strings.Join(parts, ", ")
}

func reasons(rs []v1.Reason) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return strings.Join(out, ",")
}

func sortedAffected(resp *v1.ResolveResponse) []v1.AffectedStack {
	out := slices.Clone(resp.Affected)
	slices.SortStableFunc(out, func(a, b v1.AffectedStack) int {
		if a.Wave != b.Wave {
			return a.Wave - b.Wave
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

func writeAffectedTable(w io.Writer, resp *v1.ResolveResponse) error {
	if len(resp.Affected) == 0 {
		_, err := fmt.Fprintln(w, "no stacks affected")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "WAVE\tSTACK\tREASONS\tENVIRONMENT")
	for _, s := range sortedAffected(resp) {
		env := s.Environment
		if env == "" {
			env = v1.DefaultEnvironment
		}
		line := fmt.Sprintf("%d\t%s\t%s\t%s", s.Wave, s.Key, reasons(s.Reasons), env)
		if s.LockedBy != nil {
			line += fmt.Sprintf("\tlocked by PR #%d", s.LockedBy.PRNumber)
		}
		_, _ = fmt.Fprintln(tw, line)
	}
	return tw.Flush()
}

func cycleLines(cycles [][]string) []string {
	out := make([]string, 0, len(cycles))
	for _, c := range cycles {
		path := slices.Clone(c)
		if len(path) == 1 || (len(path) > 1 && path[len(path)-1] != path[0]) {
			path = append(path, path[0])
		}
		out = append(out, "cycle: "+strings.Join(path, " -> "))
	}
	return out
}

func resolveMarkdown(resp *v1.ResolveResponse, note string) string {
	var b strings.Builder
	b.WriteString("### stackorder resolve\n\n")
	if note != "" {
		b.WriteString("> " + note + "\n\n")
	}
	if resp.RunID != "" {
		fmt.Fprintf(&b, "Run `%s`.\n\n", resp.RunID)
	}
	if len(resp.Affected) == 0 {
		b.WriteString("No stacks affected.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%d stacks affected in %d waves.\n\n", len(resp.Affected), len(resp.Waves))
	b.WriteString("| Wave | Stack | Reasons | Environment |\n| --- | --- | --- | --- |\n")
	for _, s := range sortedAffected(resp) {
		env := s.Environment
		if env == "" {
			env = v1.DefaultEnvironment
		}
		fmt.Fprintf(&b, "| %d | `%s` | %s | %s |\n", s.Wave, s.Key, reasons(s.Reasons), env)
	}
	for _, w := range resp.Warnings {
		fmt.Fprintf(&b, "\n- warning: %s", w)
	}
	if len(resp.Warnings) > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

func writeAddressList(b *strings.Builder, title string, addrs []string) {
	if len(addrs) == 0 {
		return
	}
	fmt.Fprintf(b, "\n<details><summary>%s (%d)</summary>\n\n", title, len(addrs))
	for i, a := range addrs {
		if i == maxSummaryAddresses {
			fmt.Fprintf(b, "- and %d more\n", len(addrs)-i)
			break
		}
		fmt.Fprintf(b, "- `%s`\n", a)
	}
	b.WriteString("\n</details>\n")
}

func stackMarkdown(mode v1.RunMode, key string, res *v1.StackResult, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### stackorder %s: `%s`\n\n", mode, key)
	if note != "" {
		b.WriteString("> " + note + "\n\n")
	}
	if res.Status != v1.ResultSuccess {
		fmt.Fprintf(&b, "**%s** (exit code %d)\n", res.Status, res.ExitCode)
		if res.ErrorText != "" {
			b.WriteString("\n```\n" + strings.ReplaceAll(res.ErrorText, "```", "'''") + "\n```\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "**%s**\n", summaryLine(res.Summary))
	if s := res.Summary; s != nil {
		writeAddressList(&b, "Added", s.Added)
		writeAddressList(&b, "Changed", s.Changed)
		writeAddressList(&b, "Destroyed", s.Destroyed)
		writeAddressList(&b, "Replaced", s.Replaced)
	}
	return b.String()
}

func writeGraphText(w io.Writer, g *v1.Graph) error {
	out := map[string][]v1.Edge{}
	for _, e := range g.Edges {
		out[e.From.Key] = append(out[e.From.Key], e)
	}
	stacks := slices.Clone(g.Stacks)
	slices.SortFunc(stacks, func(a, b v1.Stack) int { return strings.Compare(a.Key, b.Key) })
	modules := slices.Clone(g.Modules)
	slices.SortFunc(modules, func(a, b v1.Module) int { return strings.Compare(a.Key, b.Key) })
	var b strings.Builder
	fmt.Fprintf(&b, "%d stacks, %d modules, %d edges\n", len(g.Stacks), len(g.Modules), len(g.Edges))
	writeNode := func(key, label string) {
		fmt.Fprintf(&b, "\n%s%s\n", key, label)
		edges := out[key]
		slices.SortFunc(edges, func(x, y v1.Edge) int {
			return strings.Compare(string(x.Type)+x.To.Key, string(y.Type)+y.To.Key)
		})
		for _, e := range edges {
			suffix := ""
			if e.Inferred {
				suffix = " (inferred)"
			}
			if ref := e.Meta["ref"]; ref != "" && !strings.HasSuffix(e.To.Key, "@"+ref) {
				suffix += " @" + ref
			}
			fmt.Fprintf(&b, "  %-12s %s%s\n", e.Type, e.To.Key, suffix)
		}
	}
	for _, s := range stacks {
		label := ""
		if s.External {
			label = " (external)"
		}
		writeNode(s.Key, label)
	}
	for _, m := range modules {
		writeNode(m.Key, " ("+string(m.Kind)+" module)")
	}
	for _, w := range g.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s", w)
	}
	if len(g.Warnings) > 0 {
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
