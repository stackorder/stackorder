package report

import (
	"strconv"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

// DriftIssueTitle returns the stable title of the drift issue for a stack,
// which the server searches for to update an existing issue.
func DriftIssueTitle(key string) string { return "Drift detected in " + key }

// DriftIssue renders the GitHub issue opened or updated when a scheduled
// drift check finds that a stack no longer matches its configuration. The
// title depends only on the stack key; when drift.Drifted is false the body
// says the stack is back in line and the issue can be closed.
func DriftIssue(stack v1.StackDetail, drift v1.DriftStatus, o Options) (title, body string) {
	key := stack.Key
	if key == "" {
		key = v1.StackKey(stack.Path, config.InstanceOf(stack.Instance, stack.Workspace))
	}
	title = DriftIssueTitle(oneLine(key))

	var b strings.Builder
	where := code(key)
	if stack.Repo != "" {
		where += " in " + code(stack.Repo)
	}
	if !drift.Drifted {
		b.WriteString("The drift check found no drift: " + where + " matches its configuration again. This issue can be closed.\n\n")
	} else {
		b.WriteString("The scheduled drift check on the default branch found that " + where + " no longer matches its configuration.\n\n")
	}
	if meta := driftMeta(stack, drift, o); meta != "" {
		b.WriteString(meta + "\n\n")
	}
	head := b.String()

	var tail strings.Builder
	if drift.Drifted {
		if drift.Summary != nil {
			tail.WriteString(SummaryLong(*drift.Summary) + ".\n\n")
			tail.WriteString(countsTable(*drift.Summary) + "\n")
		}
		if lists := addressLists(drift.Summary, false); lists != "" {
			tail.WriteString(lists + "\n")
		}
	}
	if la := stack.LastApply; la != nil {
		tail.WriteString(lastApplyLine(la, stack.Repo, o) + "\n\n")
	}
	if drift.Drifted {
		tail.WriteString("Reconcile by applying the configuration through a pull request, or by changing the configuration to match what is deployed. ")
		tail.WriteString(Escape(o.name()) + " updates this issue after every drift check.\n")
	}
	rest, _ := TruncatePlan(tail.String(), MaxComment-len(head))
	return title, tidy(head + rest)
}

func driftMeta(stack v1.StackDetail, drift v1.DriftStatus, o Options) string {
	var parts []string
	if ts := formatTime(drift.CheckedAt); ts != "" {
		parts = append(parts, "**Checked:** "+ts)
	}
	if stack.Environment != "" {
		parts = append(parts, "**Environment:** "+code(stack.Environment))
	}
	if be := stack.Backend; be != nil && be.Bucket != "" {
		parts = append(parts, "**State:** "+code("s3://"+be.Bucket+"/"+be.Key))
	}
	if u := o.stackURL(stack.ID); u != "" {
		parts = append(parts, link("Stack details", u))
	}
	return strings.Join(parts, " · ")
}

func lastApplyLine(la *v1.RunStackRef, repo string, o Options) string {
	out := "Last " + link("apply", o.runIDURL(la.RunID))
	if la.SHA != "" {
		out += " at " + link(code(shortSHA(la.SHA)), o.commitURL(repo, la.SHA))
	}
	if la.PRNumber != 0 {
		out += " from " + link("#"+strconv.Itoa(la.PRNumber), o.pullURL(repo, la.PRNumber))
	}
	if la.FinishedAt != nil {
		if ts := formatTime(*la.FinishedAt); ts != "" {
			out += " on " + ts
		}
	}
	return out + "."
}
