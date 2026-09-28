package report

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func sortedStacks(stacks []v1.RunStack) []v1.RunStack {
	out := append([]v1.RunStack(nil), stacks...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Wave != out[j].Wave {
			return out[i].Wave < out[j].Wave
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func waveCount(run v1.Run) int {
	if run.Waves > 0 {
		return run.Waves
	}
	n := 0
	for _, rs := range run.Stacks {
		n = max(n, rs.Wave+1)
	}
	return n
}

type phase int

const (
	phasePlan phase = iota
	phaseApply
	phaseDrift
)

func phaseOf(run v1.Run) phase {
	if run.Mode == v1.ModeDrift {
		return phaseDrift
	}
	if run.Mode == v1.ModeApply || run.Status == v1.RunApplying || run.Status == v1.RunApplied {
		return phaseApply
	}
	for _, rs := range run.Stacks {
		switch rs.Status {
		case v1.StackApplying, v1.StackApplied, v1.StackNoop:
			return phaseApply
		}
	}
	return phasePlan
}

func modePhase(m v1.RunMode) phase {
	switch m {
	case v1.ModeApply:
		return phaseApply
	case v1.ModeDrift:
		return phaseDrift
	}
	return phasePlan
}

func (p phase) noun() string {
	switch p {
	case phaseApply:
		return "apply"
	case phaseDrift:
		return "drift check"
	}
	return "plan"
}

func (p phase) pastTense() string {
	switch p {
	case phaseApply:
		return "applied"
	case phaseDrift:
		return "checked"
	}
	return "planned"
}

func (p phase) gerund() string {
	if p == phaseDrift {
		return "Checking drift"
	}
	return "Planning"
}

func unfinished(s v1.StackStatus, p phase) bool {
	switch s {
	case v1.StackPending, v1.StackPlanning, v1.StackApplying, "":
		return true
	case v1.StackPlanned:
		return p == phaseApply
	}
	return false
}

func neverRan(run v1.Run, rs v1.RunStack, p phase) bool {
	return unfinished(rs.Status, p) && (run.Status == v1.RunFailed || run.Status == v1.RunApplied)
}

func stackStatusWord(rs v1.RunStack, o Options) string {
	switch rs.Status {
	case v1.StackNoop:
		return "no-op"
	case v1.StackApplying:
		if _, ok := o.approvalFor(rs.Environment); ok {
			return "awaiting approval"
		}
	case "":
		return "pending"
	}
	return string(rs.Status)
}

func runStatusWord(s v1.RunStatus) string {
	if s == "" {
		return string(v1.RunPending)
	}
	return string(s)
}

func summaryMode(rs v1.RunStack) bool { return rs.PlanOutput == string(v1.PlanOutputSummary) }

func otherLocks(run v1.Run, o Options) []v1.LockInfo {
	var out []v1.LockInfo
	for _, l := range o.Locks {
		if run.PRNumber != 0 && l.PRNumber == run.PRNumber {
			continue
		}
		out = append(out, l)
	}
	sortLocks(out)
	return out
}

func sortLocks(locks []v1.LockInfo) {
	sort.SliceStable(locks, func(i, j int) bool {
		if locks[i].StackKey != locks[j].StackKey {
			return locks[i].StackKey < locks[j].StackKey
		}
		return locks[i].RunID < locks[j].RunID
	})
}

func lockKey(l v1.LockInfo) string {
	if l.StackKey != "" {
		return l.StackKey
	}
	return l.StackID
}

func lockLine(l v1.LockInfo, repo string, o Options) string {
	var b strings.Builder
	b.WriteString(code(lockKey(l)))
	if l.PRNumber != 0 {
		b.WriteString(" is locked by " + link(fmt.Sprintf("#%d", l.PRNumber), o.pullURL(repo, l.PRNumber)))
	} else {
		b.WriteString(" is locked")
	}
	if ts := formatTime(l.TakenAt); ts != "" {
		b.WriteString(" since " + ts)
	}
	if u := o.runIDURL(l.RunID); u != "" {
		b.WriteString(" (" + link("run", u) + ")")
	}
	if l.Reason != "" {
		b.WriteString(": " + Escape(clip(l.Reason, maxItemBytes)))
	}
	return b.String()
}

func lockFor(key string, locks []v1.LockInfo) (v1.LockInfo, bool) {
	for _, l := range locks {
		if l.StackKey == key {
			return l, true
		}
	}
	return v1.LockInfo{}, false
}

const tableHeader = "| Wave | Stack | Environment | Add | Change | Destroy | Replace | Status | Job |\n" +
	"| ---: | --- | --- | ---: | ---: | ---: | ---: | --- | --- |\n"

func stackRow(rs v1.RunStack, o Options) string {
	counts := []string{"", "", "", ""}
	if s := rs.Summary; s != nil {
		counts = []string{strconv.Itoa(s.Adds), strconv.Itoa(s.Changes), strconv.Itoa(s.Destroys), strconv.Itoa(s.Replaces)}
	}
	return fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %s | %s | %s |\n",
		rs.Wave, codeCell(rs.Key), Escape(rs.Environment), counts[0], counts[1], counts[2], counts[3],
		stackStatusWord(rs, o), jobCell(rs.JobURL))
}

func jobCell(url string) string {
	if url == "" {
		return ""
	}
	return link("log", url)
}

func stackTable(run v1.Run, stacks []v1.RunStack, o Options, budget int) string {
	var b strings.Builder
	b.WriteString(tableHeader)
	reserve := len(moreStacksNote(len(stacks), o.runURL(run)))
	for i, rs := range stacks {
		row := stackRow(rs, o)
		if b.Len()+len(row)+reserve > budget {
			b.WriteString(moreStacksNote(len(stacks)-i, o.runURL(run)))
			break
		}
		b.WriteString(row)
	}
	return b.String()
}

func moreStacksNote(n int, runURL string) string {
	return "\n… and " + plural(n, "more stack") + " not listed to stay within GitHub's size limit; " + link("see the run details", runURL) + ".\n"
}
