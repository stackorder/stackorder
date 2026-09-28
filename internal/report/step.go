package report

import (
	"fmt"
	"strings"
	"time"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	// MaxStepSummary is the size a step summary is kept under, well below
	// the 1 MiB GitHub accepts per step.
	MaxStepSummary = 512 << 10
	maxErrorText   = 64 << 10
)

// StepSummary renders the Markdown the CLI appends to GITHUB_STEP_SUMMARY
// after a plan, apply or drift execution of one stack.
func StepSummary(res v1.StackResult, key string, o Options) string {
	mode := res.Mode
	if mode == "" {
		mode = v1.ModePlan
	}
	var b strings.Builder
	b.WriteString("### " + Escape(o.name()) + " " + string(mode) + ": " + code(key) + "\n\n")
	b.WriteString(stepMeta(res) + "\n\n")
	if res.Unconfirmed {
		b.WriteString(UnconfirmedNote("the CLI could not reach the server during this run") + "\n\n")
	}
	b.WriteString(stepHeadline(res, mode) + "\n\n")
	if res.Summary != nil && !res.Summary.Empty() {
		b.WriteString(countsTable(*res.Summary) + "\n")
	}
	if res.ErrorText != "" {
		b.WriteString("**Error**\n\n" + fencedBlock("", res.ErrorText, "text", "\n").fit(maxErrorText))
	}
	room := MaxStepSummary - b.Len()
	switch {
	case res.PlanText != "":
		label := "Plan output"
		if res.Truncated {
			label += " (truncated at 256 KB)"
		}
		b.WriteString(fencedBlock("<details><summary>"+label+"</summary>\n\n", res.PlanText, "hcl", "\n</details>\n").fit(room))
	case hasAddresses(res.Summary):
		body, _ := TruncatePlan(addressLists(res.Summary, mode == v1.ModeApply), room)
		b.WriteString(body)
	}
	return b.String()
}

func stepMeta(res v1.StackResult) string {
	status := res.Status
	if status == "" {
		status = v1.ResultSuccess
	}
	parts := []string{"**Result:** " + string(status)}
	if res.Tool != "" {
		parts = append(parts, "**Tool:** "+strings.TrimSpace(string(res.Tool)+" "+Escape(res.ToolVersion)))
	}
	if res.DurationMS > 0 {
		parts = append(parts, "**Duration:** "+formatDuration(res.DurationMS))
	}
	parts = append(parts, fmt.Sprintf("**Exit code:** %d", res.ExitCode))
	if res.Artifact != "" {
		parts = append(parts, "**Artifact:** "+code(res.Artifact))
	}
	if res.JobURL != "" {
		parts = append(parts, link("Job", res.JobURL))
	}
	return strings.Join(parts, " · ")
}

func stepHeadline(res v1.StackResult, mode v1.RunMode) string {
	if res.Status == v1.ResultFailure || res.Status == v1.ResultError {
		return capitalize(modePhase(mode).noun()) + " failed."
	}
	if res.Summary == nil {
		return unsummarisedHeadline(res, mode)
	}
	s := *res.Summary
	switch mode {
	case v1.ModeApply:
		return "Applied: " + lowerFirst(appliedLong(s)) + "."
	case v1.ModeDrift:
		if !res.HasChanges && s.Empty() {
			return "No drift: the stack matches its configuration."
		}
		return "Drift detected: " + lowerFirst(SummaryLong(s)) + "."
	}
	return SummaryLong(s) + "."
}

func unsummarisedHeadline(res v1.StackResult, mode v1.RunMode) string {
	switch {
	case mode == v1.ModeApply:
		return "Applied."
	case mode == v1.ModeDrift && res.HasChanges:
		return "Drift detected; no summary was reported."
	case mode == v1.ModeDrift:
		return "No drift: the stack matches its configuration."
	case res.HasChanges:
		return "The plan has changes; no summary was reported."
	}
	return "No changes."
}

func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d >= time.Second {
		d = d.Round(time.Second)
	}
	return d.String()
}
