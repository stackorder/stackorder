package report

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/command"
)

// Apply gate layers, in the order the server checks them.
const (
	// LayerAuthorization checks allowed_teams membership or push permission.
	LayerAuthorization = 1
	// LayerApprovals checks review count, mergeability, four eyes and code
	// owner review.
	LayerApprovals = 2
	// LayerPlans checks for a planned row on the current head SHA.
	LayerPlans = 3
	// LayerChecks checks that every named policy check passed.
	LayerChecks = 4
	// LayerLocks checks that no other pull request holds a stack lock.
	LayerLocks = 5
)

// LayerName returns the display name of an apply gate layer, or "" for an
// unknown layer.
func LayerName(layer int) string {
	switch layer {
	case LayerAuthorization:
		return "authorization"
	case LayerApprovals:
		return "approvals"
	case LayerPlans:
		return "plans for the head commit"
	case LayerChecks:
		return "policy checks"
	case LayerLocks:
		return "locks"
	}
	return ""
}

// GateFailure is one reason the apply gate refused a command.
type GateFailure struct {
	// Layer is the gate layer that refused, one of the Layer constants.
	Layer int
	// Name overrides LayerName(Layer) in the comment when set.
	Name string
	// Reason is the exact reason, rendered as Markdown on one line.
	Reason string
	// Stacks lists the stack keys the failure applies to, if specific.
	Stacks []string
}

// RefusalComment renders the pull request comment explaining why a comment
// command was refused, one bullet per failure naming its layer and reason.
func RefusalComment(cmd string, failures []GateFailure, o Options) string {
	sorted := append([]GateFailure(nil), failures...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Layer < sorted[j].Layer })
	var b strings.Builder
	b.WriteString("**" + code(cmd) + " was refused.** Nothing was dispatched.\n\n")
	if len(sorted) == 0 {
		b.WriteString("- No reason was recorded; see the " + code(CheckApply) + " check.\n")
	}
	for i, f := range sorted {
		if i == maxListItems {
			fmt.Fprintf(&b, "- … and %d more\n", len(sorted)-i)
			break
		}
		b.WriteString("- " + gateBullet(f) + "\n")
	}
	b.WriteString("\nThe server checks these before anything runs; GitHub environment reviewers and the AWS trust policy still gate the apply job itself. ")
	b.WriteString("Fix the above and comment " + code(cmd) + " again, or " + code(command.Help.Usage()) + " for the list of commands.\n")
	return b.String()
}

func gateBullet(f GateFailure) string {
	name := f.Name
	if name == "" {
		name = LayerName(f.Layer)
	}
	label := fmt.Sprintf("**Layer %d", f.Layer)
	if name != "" {
		label += ", " + Escape(name)
	}
	label += "**"
	if len(f.Stacks) > 0 {
		label += " (" + codeList(f.Stacks, maxListItems) + ")"
	}
	return label + ": " + strings.TrimSpace(oneLine(clip(f.Reason, 2*maxItemBytes)))
}

// LockWarningComment renders the comment posted when a pull request that
// applied changes is closed without merging and keeps its locks.
func LockWarningComment(locks []v1.LockInfo, o Options) string {
	if len(locks) == 0 {
		return "This pull request was closed without merging. It holds no orchestration locks, so nothing needs releasing.\n"
	}
	sorted := append([]v1.LockInfo(nil), locks...)
	sortLocks(sorted)
	items := make([]string, 0, len(sorted))
	for _, l := range sorted {
		items = append(items, lockLine(l, "", o))
	}
	var b strings.Builder
	b.WriteString("**Orchestration locks kept.** This pull request was closed without merging after it applied changes, ")
	b.WriteString("so the default branch no longer matches what is deployed. ")
	verb := "stay"
	if len(sorted) == 1 {
		verb = "stays"
	}
	fmt.Fprintf(&b, "%s %s locked so no other pull request applies over those changes:\n\n", plural(len(sorted), "stack"), verb)
	b.WriteString(bulletList(items, maxListItems))
	b.WriteString("\nBring the default branch in line with what was applied, by merging an equivalent change or reverting the resources, ")
	b.WriteString("then release the locks by commenting " + code(command.Command{Verb: command.Unlock}.String()) + " here. Unlocking needs write permission.\n")
	return b.String()
}

// UnlockedComment renders the reply to "stackorder unlock" listing the
// locks that were released and who asked for it.
func UnlockedComment(released []v1.LockInfo, actor string) string {
	if len(released) == 0 {
		return "No orchestration locks matched, so nothing was released.\n"
	}
	sorted := append([]v1.LockInfo(nil), released...)
	sortLocks(sorted)
	var b strings.Builder
	b.WriteString("Released " + plural(len(sorted), "orchestration lock"))
	if actor != "" {
		b.WriteString(" at the request of " + mention(actor))
	}
	b.WriteString(":\n\n")
	items := make([]string, 0, len(sorted))
	for _, l := range sorted {
		item := code(lockKey(l))
		if l.PRNumber != 0 {
			item += fmt.Sprintf(", held by #%d", l.PRNumber)
		}
		if ts := formatTime(l.TakenAt); ts != "" {
			item += " since " + ts
		}
		items = append(items, item)
	}
	b.WriteString(bulletList(items, maxListItems))
	b.WriteString("\nThe S3 state locks are untouched; release them separately if a runner died mid-apply.\n")
	return b.String()
}

// AppliedComment renders the comment posted when an apply run succeeds, so
// the outcome lands after the command in the pull request timeline. released
// tells whether the run's orchestration locks were released on completion.
// It points at the run's apply comment and run details, or at the apply
// check when the run has neither.
func AppliedComment(a ApplyRef, released bool, o Options) string {
	run := a.Run
	var b strings.Builder
	b.WriteString("**Apply of " + code(shortSHA(run.SHA)) + " succeeded.** ")
	b.WriteString(aggregateLine(run, run.Stacks, phaseApply) + "\n\n")
	if released {
		b.WriteString("The run's orchestration locks were released. ")
	} else {
		b.WriteString("The orchestration locks stay held until this pull request merges. ")
	}
	details := o.runURL(run)
	var where string
	switch {
	case a.CommentURL != "":
		where = "the " + link("apply comment", a.CommentURL)
		if details != "" {
			where += " and the " + link("run details", details)
		}
	case details != "":
		where = "the " + link("run details", details)
	default:
		where = "the " + code(CheckApply) + " check"
	}
	b.WriteString("Per-stack results are in " + where + ".\n")
	return b.String()
}

func mention(login string) string {
	name := strings.TrimSuffix(login, "[bot]")
	if name == "" || len(login) > 64 {
		return Escape(login)
	}
	if strings.IndexFunc(name, notLoginRune) >= 0 {
		return Escape(login)
	}
	return "@" + login
}

func notLoginRune(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-'
}
