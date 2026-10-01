package report

import (
	"fmt"
	"html"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/command"
)

const minDetailBody = 1024

// StickyComment renders the single pull request comment Stackorder keeps up
// to date for a run. Its first line is always Marker. Plan text is shared
// fairly across stacks and cut at line boundaries so the whole comment stays
// under MaxComment bytes.
func StickyComment(run v1.Run, o Options) string {
	stacks := sortedStacks(run.Stacks)
	head := stickyHead(run, stacks, o)
	applies := appliesSection(o)
	approvals := approvalsSection(o)
	locks := locksSection(run, o)
	warnings := warningsSection(run.Warnings)
	footer := stickyFooter()
	room := MaxComment - len(head) - len(applies) - len(approvals) - len(locks) - len(warnings) - len(footer)

	var blocks []block
	if run.Status != v1.RunSuperseded {
		blocks = stickyDetails(stacks, o)
	}
	table := ""
	if len(stacks) > 0 {
		tableRoom := room
		if len(blocks) > 0 {
			tableRoom = room / 2
		}
		table = stackTable(run, stacks, o, tableRoom-1) + "\n"
	}
	details := renderDetails(blocks, room-len(table))

	out := head + applies + approvals + locks + table + details + warnings + footer
	if len(out) > MaxComment {
		out, _ = TruncatePlan(out, MaxComment)
	}
	return out
}

func stickyHead(run v1.Run, stacks []v1.RunStack, o Options) string {
	return Marker + "\n" + runHead(Escape(o.name()), run, stacks, o)
}

func runHead(title string, run v1.Run, stacks []v1.RunStack, o Options) string {
	var b strings.Builder
	b.WriteString("### " + title + ": " + runStatusWord(run.Status) + "\n\n")
	b.WriteString(runMetaLine(run, o) + "\n\n")
	p := phaseOf(run)
	t := tallyOf(stacks, p)
	switch run.Status {
	case v1.RunSuperseded:
		b.WriteString(supersededNote(run) + "\n\n")
	case v1.RunPending, v1.RunPlanning, v1.RunApplying, "":
		if len(stacks) == 0 {
			b.WriteString("Waiting for the resolve job to report the affected stacks.\n\n")
			break
		}
		b.WriteString(inProgress(run, t, p).Title + ".\n\n")
	case v1.RunFailed:
		switch {
		case len(stacks) == 0:
			b.WriteString("The run failed before any stack was planned; see the " + code(CheckResolve) + " check.\n\n")
		case t.broken() == 0:
			b.WriteString(aggregateLine(run, stacks, p) + "\n\nThe run failed although no stack reported a failure; see the run details.\n\n")
		default:
			b.WriteString(aggregateLine(run, stacks, p) + "\n\n" + brokenTitle(run, t) + ". " + failedNote(p) + "\n\n")
		}
	case v1.RunUnconfirmed:
		b.WriteString(UnconfirmedNote("") + "\n\n")
		b.WriteString(aggregateLine(run, stacks, p) + "\n\n")
	default:
		b.WriteString(aggregateLine(run, stacks, p) + "\n\n")
	}
	return b.String()
}

func appliesSection(o Options) string {
	if len(o.Applies) == 0 {
		return ""
	}
	items := make([]string, 0, len(o.Applies))
	for _, a := range o.Applies {
		r := a.Run
		item := link(code(shortSHA(r.SHA)), o.commitURL(r.Repo, r.SHA)) + ": " + runStatusWord(r.Status)
		if by := origin(r); by != "" {
			item += ", " + by
		}
		item += "."
		if a.CommentURL != "" {
			item += " " + link("Apply comment", a.CommentURL)
		} else if u := o.runIDURL(r.ID); u != "" {
			item += " " + link("Run details", u)
		}
		items = append(items, item)
	}
	return "**Applies**\n\nThe plans below stay as they were planned. Each apply reports its progress and result in its own comment.\n\n" + bulletList(items, maxListItems) + "\n"
}

func aggregateLine(run v1.Run, stacks []v1.RunStack, p phase) string {
	if len(stacks) == 0 {
		return "No stacks are affected by this change."
	}
	sum, _ := sumSummaries(stacks)
	scope := plural(len(stacks), "stack") + " in " + plural(waveCount(run), "wave")
	if p == phaseApply && run.Status == v1.RunApplied {
		done, skipped := appliedStacks(stacks)
		sum, _ = sumSummaries(done)
		scope = plural(len(done), "stack") + " in " + plural(waveCount(run), "wave")
		if skipped > 0 {
			scope += fmt.Sprintf(", skipped %d not in the requested subset", skipped)
		}
		return "Applied " + scope + ": " + lowerFirst(appliedLong(sum)) + "."
	}
	return capitalize(scope) + ": " + lowerFirst(SummaryLong(sum)) + "."
}

func stickyFooter() string {
	usages := make([]string, 0, len(command.Verbs()))
	for _, v := range command.Verbs() {
		usages = append(usages, code(v.Usage()))
	}
	return "---\n<sub>Commands: " + strings.Join(usages, " · ") + "</sub>\n"
}

func stickyDetails(stacks []v1.RunStack, o Options) []block {
	var out []block
	for _, rs := range stacks {
		if b, ok := stackDetail(rs, o); ok {
			out = append(out, b)
		}
	}
	return out
}

func stackDetail(rs v1.RunStack, o Options) (block, bool) {
	if rs.Summary != nil && rs.Summary.Empty() && rs.Status != v1.StackFailed {
		return block{}, false
	}
	applied := rs.Status == v1.StackApplied
	open := "<details><summary><code>" + html.EscapeString(oneLine(rs.Key)) + "</code>: " + html.EscapeString(detailHeadline(rs, o)) + "</summary>\n\n"
	const closing = "\n</details>\n\n"
	switch {
	case summaryMode(rs):
		if !hasAddresses(rs.Summary) {
			return block{}, false
		}
		return block{open: open + summaryModeNote, body: addressLists(rs.Summary, applied), close: closing}, true
	case rs.PlanText != "":
		lang := "hcl"
		if rs.Status == v1.StackFailed {
			lang = "text"
		}
		return fencedBlock(open, rs.PlanText, lang, closing), true
	case hasAddresses(rs.Summary):
		return block{open: open, body: addressLists(rs.Summary, applied), close: closing}, true
	}
	return block{}, false
}

func detailHeadline(rs v1.RunStack, o Options) string {
	word := stackStatusWord(rs, o)
	if rs.Summary == nil {
		return word
	}
	if rs.Status == v1.StackApplied {
		return word + ", " + lowerFirst(appliedLong(*rs.Summary))
	}
	return word + ", " + lowerFirst(SummaryLong(*rs.Summary))
}

func renderDetails(blocks []block, room int) string {
	over := make([]int, len(blocks)+1)
	total := make([]int, len(blocks)+1)
	for i, b := range blocks {
		over[i+1] = over[i] + b.overhead()
		total[i+1] = total[i] + len(b.body)
	}
	for n := len(blocks); n > 0; n-- {
		note := omittedNote(len(blocks) - n)
		avail := room - over[n] - len(note)
		if avail < 0 || (total[n] > avail && avail/n < minDetailBody) {
			continue
		}
		sizes := make([]int, n)
		for i, b := range blocks[:n] {
			sizes[i] = len(b.body)
		}
		alloc := shareBudget(sizes, avail)
		var sb strings.Builder
		for i, b := range blocks[:n] {
			sb.WriteString(b.render(alloc[i]))
		}
		sb.WriteString(note)
		return sb.String()
	}
	return omittedNote(len(blocks))
}

func omittedNote(n int) string {
	if n == 0 {
		return ""
	}
	return "Plan output for " + plural(n, "more stack") + " is not shown, to stay within GitHub's comment size limit; see the job logs.\n\n"
}
