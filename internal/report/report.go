// Package report renders every piece of text Stackorder posts to GitHub or
// writes to the runner log: check run output, the sticky pull request
// comment, apply gate refusals, lock notices, drift issues and step
// summaries.
//
// Every function is pure and deterministic. Output depends only on the
// arguments; nothing reads the clock, the environment or the network, so
// the same run always renders to the same bytes and an unchanged comment
// can be skipped instead of re-posted.
package report

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	// Marker is the hidden first line that identifies the sticky comment.
	Marker = "<!-- stackorder:sticky -->"
	// MaxCheckText is GitHub's limit on a check run's summary and on its
	// text, in characters; the renderer counts bytes, which is stricter.
	MaxCheckText = 65535
	// MaxComment is the size the sticky comment and other long bodies are
	// kept under, just below GitHub's 65536 character limit.
	MaxComment = 65000
	// MaxPlanText is the cap on plan text sent by the CLI and stored by the
	// server.
	MaxPlanText = 256 << 10
	// TruncationNote is the line TruncatePlan appends after a cut.
	TruncationNote = "… truncated, see the job log"
	// DefaultServerName is used when Options.ServerName is empty.
	DefaultServerName = "Stackorder"
)

const (
	// CheckResolve is the check run name of the resolve job.
	CheckResolve = "stackorder/resolve"
	// CheckPlan is the name of the plan roll-up check run.
	CheckPlan = "stackorder/plan"
	// CheckApply is the name of the apply roll-up check run.
	CheckApply = "stackorder/apply"
)

// StackCheckName returns the per-stack check run name under a roll-up, such
// as "stackorder/plan: stacks/prod/vpc".
func StackCheckName(rollup, key string) string { return rollup + ": " + key }

// PolicyCheckName returns the check run name of a named policy check on a
// stack, such as "stackorder/policy: stacks/prod/vpc".
func PolicyCheckName(name, key string) string { return "stackorder/" + name + ": " + key }

const (
	// StatusQueued is the GitHub check run status for work not yet started.
	StatusQueued = "queued"
	// StatusInProgress is the GitHub check run status for running work.
	StatusInProgress = "in_progress"
	// StatusCompleted is the GitHub check run status once a conclusion is set.
	StatusCompleted = "completed"
	// ConclusionSuccess marks a completed check as passed.
	ConclusionSuccess = "success"
	// ConclusionFailure marks a completed check as failed.
	ConclusionFailure = "failure"
	// ConclusionNeutral marks a completed check that neither passes nor fails.
	ConclusionNeutral = "neutral"
	// ConclusionCancelled marks a check whose run was superseded.
	ConclusionCancelled = "cancelled"
	// ConclusionActionRequired marks a check that needs a human to act.
	ConclusionActionRequired = "action_required"
)

// Options carries the context every renderer may need beyond the v1 value
// it renders. The zero value is valid and renders without links.
type Options struct {
	// BaseURL is the Stackorder server's public URL, used for run and stack
	// links.
	BaseURL string
	// RepoURL is the repository's web URL, such as
	// "https://github.com/acme/infra"; when empty, github.com and the run's
	// repository are assumed.
	RepoURL string
	// ServerName is the product name shown in headings.
	ServerName string
	// PendingApprovals lists environment deployments waiting for a reviewer.
	PendingApprovals []Approval
	// Locks lists orchestration locks other pull requests hold on stacks of
	// the rendered run.
	Locks []v1.LockInfo
}

// Approval points at a deployment waiting for review in one GitHub
// environment.
type Approval struct {
	// Environment is the GitHub environment name.
	Environment string
	// URL is the page where a reviewer approves the deployment.
	URL string
}

const (
	maxListItems = 20
	maxItemBytes = 500
)

func (o Options) name() string {
	if o.ServerName == "" {
		return DefaultServerName
	}
	return o.ServerName
}

func (o Options) base() string { return strings.TrimRight(o.BaseURL, "/") }

func (o Options) runURL(run v1.Run) string {
	if run.HTMLURL != "" {
		return run.HTMLURL
	}
	return o.runIDURL(run.ID)
}

func (o Options) runIDURL(id string) string {
	if o.BaseURL == "" || id == "" {
		return ""
	}
	return o.base() + "/runs/" + pathEscape(id)
}

func (o Options) stackURL(id string) string {
	if o.BaseURL == "" || id == "" {
		return ""
	}
	return o.base() + "/stacks/" + pathEscape(id)
}

func (o Options) repoURL(repo string) string {
	if o.RepoURL != "" {
		return strings.TrimRight(o.RepoURL, "/")
	}
	if repo == "" {
		return ""
	}
	return "https://github.com/" + repo
}

func (o Options) commitURL(repo, sha string) string {
	r := o.repoURL(repo)
	if r == "" || sha == "" {
		return ""
	}
	return r + "/commit/" + sha
}

func (o Options) pullURL(repo string, n int) string {
	r := o.repoURL(repo)
	if r == "" || n == 0 {
		return ""
	}
	return fmt.Sprintf("%s/pull/%d", r, n)
}

func (o Options) approvalFor(env string) (Approval, bool) {
	for _, a := range o.PendingApprovals {
		if a.Environment == env && env != "" {
			return a, true
		}
	}
	return Approval{}, false
}

func pathEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

var urlEscaper = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E", "\n", "", "\r", "")

func link(text, url string) string {
	if url == "" {
		return text
	}
	return "[" + text + "](" + urlEscaper.Replace(url) + ")"
}

var escaper = strings.NewReplacer(
	"\r\n", " ", "\n", " ", "\r", " ", "\t", " ",
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
	"<", `\<`, ">", `\>`, "|", `\|`, "~", `\~`, "&", `\&`, "#", `\#`,
	"@", "@\u200b",
)

// Escape makes arbitrary text safe inside a Markdown table cell or list
// item: line breaks and tabs become spaces, Markdown and HTML punctuation is
// backslash escaped, and "@" is followed by a zero-width space so repository
// content can never mention a user or team.
func Escape(s string) string { return escaper.Replace(s) }

var lineFolder = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

func oneLine(s string) string { return lineFolder.Replace(s) }

func code(s string) string {
	s = oneLine(s)
	if s == "" {
		return ""
	}
	f := strings.Repeat("`", longestRun(s, '`')+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || (strings.HasPrefix(s, " ") && strings.HasSuffix(s, " ")) {
		s = " " + s + " "
	}
	return f + s + f
}

func codeCell(s string) string { return strings.ReplaceAll(code(s), "|", `\|`) }

func longestRun(s string, c byte) int {
	best, n := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
			best = max(best, n)
			continue
		}
		n = 0
	}
	return best
}

func clip(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:max(cut, 0)] + "…"
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func bulletList(items []string, limit int) string {
	var b strings.Builder
	for i, item := range items {
		if i == limit {
			fmt.Fprintf(&b, "- … and %d more\n", len(items)-limit)
			break
		}
		b.WriteString("- " + item + "\n")
	}
	return b.String()
}

func codeList(keys []string, limit int) string {
	out := make([]string, 0, min(len(keys), limit+1))
	for i, k := range keys {
		if i == limit {
			out = append(out, fmt.Sprintf("and %d more", len(keys)-i))
			break
		}
		out = append(out, code(k))
	}
	return strings.Join(out, ", ")
}

func tidy(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

func escapedItems(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, Escape(clip(s, maxItemBytes)))
	}
	return out
}
