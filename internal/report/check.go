package report

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/command"
)

// CheckOutput is everything needed to create or update one GitHub check run.
type CheckOutput struct {
	// Title is the one-line headline shown next to the check name.
	Title string
	// Summary is the Markdown shown at the top of the check page.
	Summary string
	// Text is the Markdown shown below the summary.
	Text string
	// Status is one of StatusQueued, StatusInProgress or StatusCompleted.
	Status string
	// Conclusion is set only when Status is StatusCompleted.
	Conclusion string
}

func (c CheckOutput) capped() CheckOutput {
	c.Title = clip(oneLine(c.Title), maxTitle)
	c.Summary, _ = TruncatePlan(tidy(c.Summary), MaxCheckText)
	c.Text, _ = TruncatePlan(tidy(c.Text), MaxCheckText)
	return c
}

func completed(title, conclusion string) CheckOutput {
	return CheckOutput{Title: title, Status: StatusCompleted, Conclusion: conclusion}
}

// StackCheck renders the per-stack check run, "stackorder/plan: <key>" or
// "stackorder/apply: <key>", for one stack of a run.
func StackCheck(run v1.Run, rs v1.RunStack, o Options) CheckOutput {
	p := phaseOf(run)
	out := stackCheckState(run, rs, o, p)
	out.Summary = stackCheckSummary(run, rs, o, p)
	out.Text = stackCheckText(rs)
	return out.capped()
}

func stackCheckState(run v1.Run, rs v1.RunStack, o Options, p phase) CheckOutput {
	if run.Status == v1.RunSuperseded && unfinished(rs.Status, p) {
		return completed("Superseded by a newer commit", ConclusionCancelled)
	}
	if neverRan(run, rs, p) {
		return completed("Not run: the run ended before this stack's turn", ConclusionCancelled)
	}
	switch rs.Status {
	case v1.StackPlanning:
		return CheckOutput{Title: "Planning", Status: StatusInProgress}
	case v1.StackApplying:
		if a, ok := o.approvalFor(rs.Environment); ok {
			return CheckOutput{Title: "Waiting for approval in " + a.Environment, Status: StatusInProgress}
		}
		return CheckOutput{Title: "Applying", Status: StatusInProgress}
	case v1.StackPlanned:
		if p != phaseApply {
			return completed(plannedTitle(rs.Summary, p), ConclusionSuccess)
		}
	case v1.StackApplied:
		if rs.Summary == nil {
			return completed("Applied", ConclusionSuccess)
		}
		return completed("Applied: "+lowerFirst(appliedLong(*rs.Summary)), ConclusionSuccess)
	case v1.StackNoop:
		return completed("No changes, nothing to apply", ConclusionSuccess)
	case v1.StackFailed:
		title := capitalize(p.noun()) + " failed"
		if rs.ExitCode != nil {
			title += fmt.Sprintf(" (exit code %d)", *rs.ExitCode)
		}
		return completed(title, ConclusionFailure)
	case v1.StackBlocked:
		return completed(blockedTitle(run, rs), ConclusionFailure)
	case v1.StackUnconfirmed:
		return completed("Unconfirmed", ConclusionNeutral)
	case v1.StackUnknown:
		return completed("Result unknown: the job ended without reporting", ConclusionFailure)
	case v1.StackSkipped:
		return completed("Skipped: not in the requested subset", ConclusionNeutral)
	}
	if p == phaseApply && rs.Wave > run.CurrentWave {
		return CheckOutput{Title: fmt.Sprintf("Waiting for wave %d", rs.Wave), Status: StatusQueued}
	}
	return CheckOutput{Title: "Queued", Status: StatusQueued}
}

func plannedTitle(s *v1.PlanSummary, p phase) string {
	if p == phaseDrift {
		if s == nil || s.Empty() {
			return "No drift"
		}
		return "Drifted: " + SummaryLong(*s)
	}
	if s == nil {
		return "Planned"
	}
	return SummaryLong(*s)
}

func blockers(run v1.Run, rs v1.RunStack) []string {
	if len(rs.BlockedBy) > 0 {
		return rs.BlockedBy
	}
	var out []string
	for _, other := range sortedStacks(run.Stacks) {
		if other.Status == v1.StackFailed && other.Wave < rs.Wave {
			out = append(out, other.Key)
		}
	}
	return out
}

func blockedTitle(run v1.Run, rs v1.RunStack) string {
	b := blockers(run, rs)
	switch len(b) {
	case 0:
		return "Blocked by a failed dependency"
	case 1:
		return "Blocked by " + b[0]
	case 2:
		return "Blocked by " + b[0] + " and " + b[1]
	}
	return fmt.Sprintf("Blocked by %s and %d more", b[0], len(b)-1)
}

func stackCheckSummary(run v1.Run, rs v1.RunStack, o Options, p phase) string {
	var b strings.Builder
	meta := []string{
		"**" + capitalize(p.noun()) + "** of " + code(rs.Key),
		fmt.Sprintf("wave %d of %d", rs.Wave, max(waveCount(run), rs.Wave+1)),
	}
	if rs.Environment != "" {
		meta = append(meta, "environment "+code(rs.Environment))
	}
	if rs.JobURL != "" {
		meta = append(meta, link("job log", rs.JobURL))
	}
	if u := o.runURL(run); u != "" {
		meta = append(meta, link("run details", u))
	}
	b.WriteString(strings.Join(meta, " · ") + "\n\n")

	if run.Status == v1.RunSuperseded {
		b.WriteString(supersededNote(run) + "\n\n")
	} else if neverRan(run, rs, p) {
		b.WriteString("The run ended before this stack's wave was dispatched, so nothing was " + p.pastTense() + " for it.\n\n")
	}
	switch rs.Status {
	case v1.StackBlocked:
		b.WriteString(blockedNote(run, rs) + "\n\n")
	case v1.StackUnconfirmed:
		b.WriteString(UnconfirmedNote("") + "\n\n")
	case v1.StackUnknown:
		b.WriteString("The job ended without reporting a result, so the stack may be partially applied and its S3 state lock may still be held. " +
			"Check the state, then release the locks with " + code("stackorder unlock --force-state "+rs.Key) + ".\n\n")
	case v1.StackFailed:
		b.WriteString(failedNote(p) + "\n\n")
	case v1.StackApplying:
		if a, ok := o.approvalFor(rs.Environment); ok {
			b.WriteString("Waiting for a reviewer to approve the " + code(a.Environment) + " environment: " + link("review the pending deployment", a.URL) + ".\n\n")
		}
	}
	if l, ok := lockFor(rs, otherLocks(run, o)); ok {
		b.WriteString("**Locked:** " + lockLine(l, run.Repo, o) + ". Plans still run, but apply is refused until the lock is released.\n\n")
	}
	if rs.Summary != nil {
		b.WriteString(countsTable(*rs.Summary) + "\n")
	}
	if len(rs.Checks) > 0 {
		b.WriteString(checksTable(rs.Checks) + "\n")
	}
	if rs.PlanArtifact != "" {
		b.WriteString("Plan artifact: " + code(rs.PlanArtifact) + "\n")
	}
	return b.String()
}

func supersededNote(run v1.Run) string {
	return "Superseded: " + code(shortSHA(run.SHA)) + " is no longer the head of this pull request, so these results are informational only."
}

func blockedNote(run v1.Run, rs v1.RunStack) string {
	b := blockers(run, rs)
	if len(b) == 0 {
		return "This stack did not run because a stack it depends on failed."
	}
	return "This stack did not run because " + codeList(b, maxListItems) + ", which it depends on, failed."
}

func failedNote(p phase) string {
	switch p {
	case phaseApply:
		return "Stacks that depend on a failed stack are blocked, the run stops after the current wave and its locks stay held."
	case phaseDrift:
		return "The drift check could not complete; the output shows why."
	}
	return "Apply is refused until every stack plans successfully; push a fix or comment `stackorder plan` to retry."
}

func checksTable(checks []v1.Check) string {
	sorted := append([]v1.Check(nil), checks...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var b strings.Builder
	b.WriteString("| Check | Status | Summary |\n| --- | --- | --- |\n")
	for _, c := range sorted {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", link(Escape(c.Name), c.DetailsURL), Escape(string(c.Status)), Escape(clip(c.Summary, maxItemBytes)))
	}
	return b.String()
}

func stackCheckText(rs v1.RunStack) string {
	switch {
	case summaryMode(rs):
		return summaryModeNote + addressLists(rs.Summary, rs.Status == v1.StackApplied)
	case rs.PlanText != "":
		lang := "hcl"
		if rs.Status == v1.StackFailed {
			lang = "text"
		}
		return fencedBlock("", rs.PlanText, lang, "").fit(MaxCheckText)
	}
	return addressLists(rs.Summary, rs.Status == v1.StackApplied)
}

const (
	maxWaveKeys = 50
	maxTitle    = 255
)

const summaryModeNote = "Plan output is set to `summary` for this stack, so only resource addresses are shown. The full plan is in the job log.\n\n"

type tally struct {
	total    int
	phase    phase
	byStatus map[v1.StackStatus]int
}

func tallyOf(stacks []v1.RunStack, p phase) tally {
	t := tally{total: len(stacks), phase: p, byStatus: map[v1.StackStatus]int{}}
	for _, rs := range stacks {
		t.byStatus[rs.Status]++
	}
	return t
}

func (t tally) n(statuses ...v1.StackStatus) int {
	n := 0
	for _, s := range statuses {
		n += t.byStatus[s]
	}
	return n
}

func (t tally) running() int { return t.idle() + t.n(v1.StackPlanning, v1.StackApplying) }

func (t tally) idle() int {
	n := t.n(v1.StackPending, "")
	if t.phase == phaseApply {
		n += t.n(v1.StackPlanned)
	}
	return n
}

func (t tally) broken() int { return t.n(v1.StackFailed, v1.StackBlocked, v1.StackUnknown) }

// RollupCheck renders the roll-up check run, "stackorder/plan" or
// "stackorder/apply", for a whole run. It fails when any stack failed, was
// blocked or vanished, is neutral when any result is unconfirmed, succeeds
// when every stack is planned (plan phase) or applied, noop or skipped
// (apply phase), and is in progress otherwise; during an apply a planned
// stack is still waiting for its wave.
func RollupCheck(run v1.Run, o Options) CheckOutput {
	p := phaseOf(run)
	t := tallyOf(run.Stacks, p)
	out := rollupState(run, t, p)
	out.Summary = rollupSummary(run, t, p, o)
	if len(run.Stacks) > 0 {
		out.Text = stackTable(run, sortedStacks(run.Stacks), o, MaxCheckText-len(warningsSection(run.Warnings))-1) + "\n"
	}
	out.Text += warningsSection(run.Warnings)
	return out.capped()
}

func rollupState(run v1.Run, t tally, p phase) CheckOutput {
	switch {
	case run.Status == v1.RunSuperseded:
		return completed("Superseded by a newer commit", ConclusionCancelled)
	case t.broken() > 0:
		return completed(brokenTitle(run, t), ConclusionFailure)
	case run.Status == v1.RunFailed:
		return completed("Run failed", ConclusionFailure)
	case t.running() > 0:
		return inProgress(run, t, p)
	case run.Status == v1.RunUnconfirmed || t.n(v1.StackUnconfirmed) > 0:
		return completed("Unconfirmed: "+plural(t.total, "stack")+" checked without the server", ConclusionNeutral)
	case t.total == 0 && !run.Status.Terminal() && run.Status != v1.RunPlanned:
		return CheckOutput{Title: "Waiting for the affected stacks", Status: StatusQueued}
	}
	return completed(successTitle(run, p), ConclusionSuccess)
}

func brokenTitle(run v1.Run, t tally) string {
	var parts []string
	for _, s := range []struct {
		status v1.StackStatus
		word   string
	}{{v1.StackFailed, "failed"}, {v1.StackBlocked, "blocked"}, {v1.StackUnknown, "unknown"}} {
		if n := t.n(s.status); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s.word))
		}
	}
	if r := t.running(); r > 0 && !run.Status.Terminal() {
		parts = append(parts, fmt.Sprintf("%d still running", r))
	}
	return capitalize(strings.Join(parts, ", "))
}

func inProgress(run v1.Run, t tally, p phase) CheckOutput {
	done := t.total - t.running()
	status := StatusInProgress
	if t.idle() == t.total && (run.Status == v1.RunPending || run.Status == "") {
		status = StatusQueued
	}
	if p == phaseApply {
		return CheckOutput{Title: fmt.Sprintf("Applying wave %d of %d: %d of %d stacks done", run.CurrentWave, waveCount(run), done, t.total), Status: status}
	}
	return CheckOutput{Title: fmt.Sprintf("%s: %d of %d stacks done", p.gerund(), done, t.total), Status: status}
}

func successTitle(run v1.Run, p phase) string {
	if len(run.Stacks) == 0 {
		return "No stacks affected"
	}
	sum, _ := sumSummaries(run.Stacks)
	n := plural(len(run.Stacks), "stack")
	switch p {
	case phaseApply:
		if sum.Empty() {
			return "Applied " + n + ", no changes"
		}
		return "Applied " + n + ": " + appliedLong(sum)
	case phaseDrift:
		if sum.Empty() {
			return "No drift in " + n
		}
		return "Drift in " + n + ": " + SummaryLong(sum)
	}
	if sum.Empty() {
		return "No changes in " + n
	}
	return n + ": " + SummaryLong(sum)
}

func rollupSummary(run v1.Run, t tally, p phase, o Options) string {
	var b strings.Builder
	b.WriteString(runMetaLine(run, o) + "\n\n")
	if run.Status == v1.RunSuperseded {
		b.WriteString(supersededNote(run) + "\n\n")
	}
	if t.total > 0 {
		sum, n := sumSummaries(run.Stacks)
		fmt.Fprintf(&b, "%s in %s.\n\n", plural(t.total, "stack"), plural(waveCount(run), "wave"))
		if n > 0 {
			b.WriteString(countsTable(sum) + "\n")
		}
	}
	if t.broken() > 0 {
		b.WriteString(failedNote(p) + "\n\n")
	}
	if run.Status == v1.RunUnconfirmed || t.n(v1.StackUnconfirmed) > 0 {
		b.WriteString(UnconfirmedNote("") + "\n\n")
	}
	b.WriteString(approvalsSection(o))
	b.WriteString(locksSection(run, o))
	return b.String()
}

func runMetaLine(run v1.Run, o Options) string {
	parts := []string{"**Mode:** " + string(run.Mode)}
	if run.SHA != "" {
		parts = append(parts, "**Commit:** "+link(code(shortSHA(run.SHA)), o.commitURL(run.Repo, run.SHA)))
	}
	if u := o.runURL(run); u != "" {
		parts = append(parts, link("Run details", u))
	}
	return strings.Join(parts, " · ")
}

func approvalsSection(o Options) string {
	if len(o.PendingApprovals) == 0 {
		return ""
	}
	sorted := append([]Approval(nil), o.PendingApprovals...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Environment < sorted[j].Environment })
	items := make([]string, 0, len(sorted))
	for _, a := range sorted {
		items = append(items, code(a.Environment)+": "+link("review the pending deployment", a.URL))
	}
	return "**Waiting for approval**\n\n" + bulletList(items, maxListItems) + "\n"
}

func locksSection(run v1.Run, o Options) string {
	locks := otherLocks(run, o)
	if len(locks) == 0 {
		return ""
	}
	items := make([]string, 0, len(locks))
	for _, l := range locks {
		items = append(items, lockLine(l, run.Repo, o))
	}
	return "**Locked by other pull requests**, so apply is refused until they are released:\n\n" + bulletList(items, maxListItems) + "\n"
}

func warningsSection(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return "**Warnings**\n\n" + bulletList(escapedItems(warnings), maxListItems) + "\n"
}

// ResolveCheck renders the "stackorder/resolve" check run from the outcome
// of a graph upload or of a local resolution. A transport or server error,
// or any dependency cycle, fails the check; a locally computed result is
// neutral.
func ResolveCheck(resp *v1.ResolveResponse, err error, o Options) CheckOutput {
	if err != nil || resp == nil {
		return resolveError(err)
	}
	if len(resp.Cycles) > 0 {
		return resolveCycles(resp)
	}
	var out CheckOutput
	switch {
	case len(resp.Affected) == 0:
		out = completed("No stacks affected", ConclusionSuccess)
	default:
		out = completed(fmt.Sprintf("%s affected in %s", plural(len(resp.Affected), "stack"), plural(len(resp.Waves), "wave")), ConclusionSuccess)
	}
	if resp.Unconfirmed {
		out.Conclusion = ConclusionNeutral
		out.Title = "Unconfirmed: " + lowerFirst(out.Title)
	}
	var b strings.Builder
	if resp.Unconfirmed {
		b.WriteString(UnconfirmedNote("the affected set was computed on the runner") + "\n\n")
	}
	if resp.Cached {
		b.WriteString("The scanned tree matches a stored graph, so the stored resolution was reused.\n\n")
	}
	if u := o.runIDURL(resp.RunID); u != "" {
		b.WriteString(link("Run details", u) + "\n\n")
	}
	for i, wave := range resp.Waves {
		fmt.Fprintf(&b, "- Wave %d: %s\n", i, codeList(wave, maxWaveKeys))
	}
	if len(resp.Waves) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(warningsSection(resp.Warnings))
	if len(resp.External) > 0 {
		items := make([]string, 0, len(resp.External))
		for _, e := range resp.External {
			items = append(items, code(e))
		}
		b.WriteString("**External dependents** in other repositories, planned after an apply when `propagate.cross_repo` is `plan`:\n\n" + bulletList(items, maxListItems) + "\n")
	}
	out.Summary = b.String()
	out.Text = affectedTable(resp.Affected, o)
	return out.capped()
}

func resolveError(err error) CheckOutput {
	out := completed("Resolve failed", ConclusionFailure)
	msg := "no resolution was returned"
	if err != nil {
		msg = err.Error()
	}
	out.Summary = "The dependency graph could not be resolved, so no stack was planned.\n\n" + fencedBlock("", clip(msg, MaxCheckText/2), "text", "").fit(MaxCheckText)
	return out.capped()
}

func resolveCycles(resp *v1.ResolveResponse) CheckOutput {
	spelled := make([]string, 0, len(resp.Cycles))
	for _, c := range resp.Cycles {
		spelled = append(spelled, SpellCycle(c))
	}
	title := plural(len(spelled), "dependency cycle")
	if len(spelled) == 1 && len(spelled[0]) <= 120 {
		title = "Dependency cycle: " + spelled[0]
	}
	out := completed(capitalize(title), ConclusionFailure)
	var b strings.Builder
	b.WriteString("Waves cannot be ordered because `depends_on` and `reads_state` edges form a cycle. Remove one edge of each cycle, or suppress an inferred edge with `ignore_inferred`.\n\n")
	items := make([]string, 0, len(spelled))
	for _, s := range spelled {
		items = append(items, code(s))
	}
	b.WriteString(bulletList(items, maxListItems) + "\n")
	b.WriteString(warningsSection(resp.Warnings))
	out.Summary = b.String()
	return out.capped()
}

// SpellCycle renders a cycle as "a → b → c → a". The input may or may not
// repeat its first key at the end.
func SpellCycle(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}
	keys := append([]string(nil), cycle...)
	if len(keys) == 1 || keys[0] != keys[len(keys)-1] {
		keys = append(keys, keys[0])
	}
	return strings.Join(keys, " → ")
}

func affectedTable(affected []v1.AffectedStack, o Options) string {
	if len(affected) == 0 {
		return ""
	}
	sorted := append([]v1.AffectedStack(nil), affected...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Wave != sorted[j].Wave {
			return sorted[i].Wave < sorted[j].Wave
		}
		return sorted[i].Key < sorted[j].Key
	})
	var b strings.Builder
	b.WriteString("| Wave | Stack | Environment | Why | Via | Lock |\n| ---: | --- | --- | --- | --- | --- |\n")
	for _, a := range sorted {
		reasons := make([]string, 0, len(a.Reasons))
		for _, r := range a.Reasons {
			reasons = append(reasons, string(r))
		}
		via := make([]string, 0, len(a.Via))
		for _, v := range a.Via {
			via = append(via, codeCell(v))
		}
		lock := ""
		if a.LockedBy != nil {
			lock = "held"
			if a.LockedBy.PRNumber != 0 {
				lock = link(fmt.Sprintf("#%d", a.LockedBy.PRNumber), o.pullURL("", a.LockedBy.PRNumber))
			}
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s |\n", a.Wave, codeCell(a.Key), Escape(a.Environment), Escape(strings.Join(reasons, ", ")), strings.Join(via, ", "), lock)
	}
	return b.String()
}

// UnconfirmedNote renders the paragraph that marks a result computed on the
// runner without the server. reason, when set, says what happened.
func UnconfirmedNote(reason string) string {
	var b strings.Builder
	b.WriteString("**Unconfirmed:** the Stackorder server could not confirm this result")
	if reason = strings.TrimSpace(reason); reason != "" {
		b.WriteString(" (" + Escape(clip(reason, maxItemBytes)) + ")")
	}
	b.WriteString(". It is not a green light: " + code(command.Command{Verb: command.Apply}.String()) + " is refused until the server confirms a plan for this commit.")
	return b.String()
}

// ForkNotice returns the title and summary of the single neutral check
// posted on a pull request from a fork, where nothing runs.
func ForkNotice() (title, summary string) {
	title = "Not run: pull request from a fork"
	summary = "Pull requests from forks get a read-only `GITHUB_TOKEN` and no `id-token` permission, " +
		"so the plan job can reach neither the AWS role nor the Stackorder server. Nothing was planned.\n\n" +
		"A maintainer can push the branch to this repository to plan it. Planning forks through " +
		"`pull_request_target` with a label gate is possible and documented, but it runs untrusted code with " +
		"this repository's credentials and is not recommended.\n"
	return title, summary
}
