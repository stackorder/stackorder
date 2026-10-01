package report

import (
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// RunMarker returns the hidden first line that identifies the pull request
// comment of one apply run.
func RunMarker(runID string) string { return "<!-- stackorder:run:" + runID + " -->" }

// RunComment renders the pull request comment that tracks one apply run,
// posted when its first wave is dispatched and edited as it progresses.
// Its first line is RunMarker(run.ID). It shows the run's status, pending
// environment approvals, a row per stack and the warnings; plan output
// stays in the sticky comment. The comment stays under MaxComment bytes.
func RunComment(run v1.Run, o Options) string {
	stacks := sortedStacks(run.Stacks)
	head := RunMarker(run.ID) + "\n" + runHead(Escape(o.name())+" apply", run, stacks, o) + originLine(run)
	approvals := approvalsSection(o)
	warnings := warningsSection(run.Warnings)
	table := ""
	if len(stacks) > 0 {
		table = stackTable(run, stacks, o, MaxComment-len(head)-len(approvals)-len(warnings)-1) + "\n"
	}
	out := head + approvals + table + warnings
	if len(out) > MaxComment {
		out, _ = TruncatePlan(out, MaxComment)
	}
	return out
}

func originLine(run v1.Run) string {
	var parts []string
	if by := origin(run); by != "" {
		parts = append(parts, capitalize(by)+".")
	}
	if !run.Status.Terminal() {
		parts = append(parts, "This comment is updated as the apply runs.")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + "\n\n"
}

func origin(run v1.Run) string {
	switch {
	case run.RequestedBy == "":
		return ""
	case run.Trigger == v1.TriggerPullRequest:
		return "started on merge by " + Escape(run.RequestedBy)
	}
	return "requested by " + Escape(run.RequestedBy)
}
