package report

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestStackCheckGolden(t *testing.T) {
	stacks := plannedStacks()
	failed := stacks[0]
	failed.Status, failed.Summary, failed.ExitCode = v1.StackFailed, nil, intp(1)
	failed.PlanText = "Error: No valid credential sources found\n"
	blocked := stacks[2]
	blocked.Status, blocked.BlockedBy = v1.StackBlocked, []string{"stacks/prod/vpc"}
	unconfirmed := stacks[0]
	unconfirmed.Status, unconfirmed.JobURL = v1.StackUnconfirmed, ""
	applying := stacks[2]
	applying.Status = v1.StackApplying
	applied := stacks[0]
	applied.Status = v1.StackApplied
	planning := stacks[4]
	planning.Status = v1.StackPlanning
	unknown := stacks[4]
	unknown.Status = v1.StackUnknown
	checked := stacks[2]
	checked.Checks = []v1.Check{
		{Name: "policy", Status: v1.CheckPass, Summary: "12 rules passed", DetailsURL: "https://github.com/acme/infra/actions/runs/1/job/9", UpdatedAt: t0},
		{Name: "cost", Status: v1.CheckWarn, Summary: "+$41.20 per month | over budget", UpdatedAt: t0},
	}
	approvalOpts := testOpts
	approvalOpts.PendingApprovals = []Approval{{Environment: "production", URL: "https://github.com/acme/infra/actions/runs/7654321"}}
	lockOpts := testOpts
	lockOpts.Locks = []v1.LockInfo{{StackKey: "stacks/prod/vpc", RunID: "9d8c7b6a-0000-4000-8000-000000000017", PRNumber: 17, TakenAt: t0}}

	tests := map[string]struct {
		run  v1.Run
		rs   v1.RunStack
		opts Options
	}{
		"check_stack_planned":      {baseRun(v1.RunPlanned, stacks...), stacks[0], testOpts},
		"check_stack_no_changes":   {baseRun(v1.RunPlanned, stacks...), stacks[1], Options{}},
		"check_stack_summary_mode": {baseRun(v1.RunPlanned, stacks...), stacks[3], testOpts},
		"check_stack_with_checks":  {baseRun(v1.RunPlanned, stacks...), checked, testOpts},
		"check_stack_failed":       {baseRun(v1.RunFailed, failed, blocked), failed, testOpts},
		"check_stack_blocked":      {applyRun(v1.RunFailed, failed, blocked), blocked, testOpts},
		"check_stack_unconfirmed":  {baseRun(v1.RunUnconfirmed, unconfirmed), unconfirmed, testOpts},
		"check_stack_awaiting":     {applyRun(v1.RunApplying, applying), applying, approvalOpts},
		"check_stack_applied":      {applyRun(v1.RunApplied, applied), applied, testOpts},
		"check_stack_unknown":      {applyRun(v1.RunFailed, unknown), unknown, testOpts},
		"check_stack_locked":       {baseRun(v1.RunPlanned, stacks...), stacks[0], lockOpts},
		"check_stack_superseded":   {baseRun(v1.RunSuperseded, stacks[0], planning), planning, testOpts},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			golden(t, name, checkDump(StackCheck(tc.run, tc.rs, tc.opts)))
		})
	}
}

func TestRollupCheckGolden(t *testing.T) {
	cases := stickyCases()
	for _, name := range []string{"planning", "planned", "plan_failed", "failed", "applying", "applied", "unconfirmed", "superseded", "locks_warned", "no_stacks"} {
		tc := cases["sticky_"+name]
		t.Run(name, func(t *testing.T) {
			golden(t, "check_rollup_"+name, checkDump(RollupCheck(tc.run, tc.opts)))
		})
	}
}

func TestResolveCheckGolden(t *testing.T) {
	affected := []v1.AffectedStack{
		{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Wave: 0, Reasons: []v1.Reason{v1.ReasonModule}, Environment: "production", Via: []string{"acme/infra//modules/vpc"}},
		{Key: "stacks/staging/vpc", Path: "stacks/staging/vpc", Wave: 0, Reasons: []v1.Reason{v1.ReasonChanged}, Environment: "staging"},
		{Key: "stacks/prod/eks", Path: "stacks/prod/eks", Wave: 1, Reasons: []v1.Reason{v1.ReasonDependent}, Environment: "production", Via: []string{"stacks/prod/vpc"},
			LockedBy: &v1.LockInfo{StackKey: "stacks/prod/eks", RunID: "r17", PRNumber: 17, TakenAt: t0}},
		{Key: "stacks/prod/apps:blue", Path: "stacks/prod/apps", Workspace: "blue", Wave: 2, Reasons: []v1.Reason{v1.ReasonReadsState, v1.ReasonDependent}, Environment: "production", Via: []string{"stacks/prod/eks"}},
	}
	ok := &v1.ResolveResponse{
		RunID:    runID,
		Affected: affected,
		Waves:    [][]string{{"stacks/prod/vpc", "stacks/staging/vpc"}, {"stacks/prod/eks"}, {"stacks/prod/apps:blue"}},
		Warnings: []string{"stacks/legacy: no backend \"s3\" block, skipped"},
		External: []string{"acme/network-infra//stacks/prod/tgw"},
		Cached:   true,
	}
	local := *ok
	local.RunID, local.Cached, local.Unconfirmed, local.External = "", false, true, nil
	tests := map[string]struct {
		resp *v1.ResolveResponse
		err  error
	}{
		"check_resolve_ok":          {ok, nil},
		"check_resolve_unconfirmed": {&local, nil},
		"check_resolve_empty":       {&v1.ResolveResponse{RunID: runID}, nil},
		"check_resolve_cycle":       {&v1.ResolveResponse{Cycles: [][]string{{"stacks/a", "stacks/b", "stacks/c", "stacks/a"}}}, nil},
		"check_resolve_cycles": {&v1.ResolveResponse{Cycles: [][]string{{"stacks/a", "stacks/b"}, {"stacks/x", "stacks/y", "stacks/z"}},
			Warnings: []string{"inferred edge stacks/y -> stacks/z"}}, nil},
		"check_resolve_error": {nil, errors.Join(errors.New("post graph"), &v1.Error{Code: "invalid", Message: "graph: stack key \"../x\" escapes the repository"})},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			golden(t, name, checkDump(ResolveCheck(tc.resp, tc.err, testOpts)))
		})
	}
}

func TestNoticesGolden(t *testing.T) {
	title, summary := ForkNotice()
	tests := map[string]string{
		"fork_notice":      title + "\n\n" + summary,
		"unconfirmed_note": UnconfirmedNote("dial tcp 10.0.0.1:443: i/o timeout"),
	}
	for name, got := range tests {
		t.Run(name, func(t *testing.T) {
			golden(t, name, got)
		})
	}
}

func TestStackCheckStates(t *testing.T) {
	approvals := Options{PendingApprovals: []Approval{{Environment: "production", URL: "https://example.com/approve"}}}
	tests := []struct {
		name       string
		run        v1.Run
		status     v1.StackStatus
		opts       Options
		wave       int
		mutate     func(*v1.RunStack)
		wantStatus string
		wantConcl  string
		wantTitle  string
	}{
		{name: "pending plan", run: baseRun(v1.RunPlanning), status: v1.StackPending, wantStatus: StatusQueued, wantTitle: "Queued"},
		{name: "empty status", run: baseRun(v1.RunPlanning), status: "", wantStatus: StatusQueued, wantTitle: "Queued"},
		{name: "pending later wave", run: applyRun(v1.RunApplying), status: v1.StackPending, wave: 2, wantStatus: StatusQueued, wantTitle: "Waiting for wave 2"},
		{name: "planning", run: baseRun(v1.RunPlanning), status: v1.StackPlanning, wantStatus: StatusInProgress, wantTitle: "Planning"},
		{name: "applying", run: applyRun(v1.RunApplying), status: v1.StackApplying, wantStatus: StatusInProgress, wantTitle: "Applying"},
		{name: "awaiting approval", run: applyRun(v1.RunApplying), status: v1.StackApplying, opts: approvals, wantStatus: StatusInProgress, wantTitle: "Waiting for approval in production"},
		{name: "planned without summary", run: baseRun(v1.RunPlanned), status: v1.StackPlanned, mutate: func(rs *v1.RunStack) { rs.Summary = nil }, wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "Planned"},
		{name: "planned", run: baseRun(v1.RunPlanned), status: v1.StackPlanned, wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "1 to add, 0 to change, 0 to destroy"},
		{name: "applied without summary", run: applyRun(v1.RunApplied), status: v1.StackApplied, mutate: func(rs *v1.RunStack) { rs.Summary = nil }, wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "Applied"},
		{name: "noop", run: applyRun(v1.RunApplied), status: v1.StackNoop, wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "No changes, nothing to apply"},
		{name: "failed apply", run: applyRun(v1.RunFailed), status: v1.StackFailed, wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "Apply failed"},
		{name: "failed drift", run: func() v1.Run { r := baseRun(v1.RunFailed); r.Mode = v1.ModeDrift; return r }(), status: v1.StackFailed, wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "Drift check failed"},
		{name: "blocked without cause", run: applyRun(v1.RunFailed), status: v1.StackBlocked, wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "Blocked by a failed dependency"},
		{name: "blocked by two", run: applyRun(v1.RunFailed), status: v1.StackBlocked, mutate: func(rs *v1.RunStack) { rs.BlockedBy = []string{"a", "b"} }, wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "Blocked by a and b"},
		{name: "blocked by many", run: applyRun(v1.RunFailed), status: v1.StackBlocked, mutate: func(rs *v1.RunStack) { rs.BlockedBy = []string{"a", "b", "c", "d"} }, wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "Blocked by a and 3 more"},
		{name: "blocked inferred from earlier wave", run: applyRun(v1.RunFailed, stack("stacks/net", "", 0, 0, withStatus(v1.StackFailed)), stack("stacks/later", "", 3, 0, withStatus(v1.StackFailed))), status: v1.StackBlocked, wave: 1, wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "Blocked by stacks/net"},
		{name: "skipped", run: applyRun(v1.RunApplied), status: v1.StackSkipped, wantStatus: StatusCompleted, wantConcl: ConclusionNeutral, wantTitle: "Skipped: not in the requested subset"},
		{name: "unknown status word", run: baseRun(v1.RunPlanning), status: "mystery", wantStatus: StatusQueued, wantTitle: "Queued"},
		{name: "drift found", run: func() v1.Run { r := baseRun(v1.RunPlanned); r.Mode = v1.ModeDrift; return r }(), status: v1.StackPlanned, wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "Drifted: 1 to add, 0 to change, 0 to destroy"},
		{name: "no drift", run: func() v1.Run { r := baseRun(v1.RunPlanned); r.Mode = v1.ModeDrift; return r }(), status: v1.StackPlanned, mutate: func(rs *v1.RunStack) { rs.Summary = &v1.PlanSummary{} }, wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "No drift"},
		{name: "planned stack waits for its apply wave", run: applyRun(v1.RunApplying), status: v1.StackPlanned, wave: 1, wantStatus: StatusQueued, wantTitle: "Waiting for wave 1"},
		{name: "planned stack in the current apply wave is queued", run: applyRun(v1.RunApplying), status: v1.StackPlanned, wantStatus: StatusQueued, wantTitle: "Queued"},
		{name: "planned stack of a plan run applying elsewhere", run: baseRun(v1.RunApplying), status: v1.StackPlanned, wave: 2, wantStatus: StatusQueued, wantTitle: "Waiting for wave 2"},
		{name: "planned stack never applied in a failed run", run: applyRun(v1.RunFailed), status: v1.StackPlanned, wave: 2, wantStatus: StatusCompleted, wantConcl: ConclusionCancelled, wantTitle: "Not run: the run ended before this stack's turn"},
		{name: "pending stack never applied in a failed run", run: applyRun(v1.RunFailed), status: v1.StackPending, wave: 2, wantStatus: StatusCompleted, wantConcl: ConclusionCancelled, wantTitle: "Not run: the run ended before this stack's turn"},
		{name: "superseded stack with empty status", run: baseRun(v1.RunSuperseded), status: "", wantStatus: StatusCompleted, wantConcl: ConclusionCancelled, wantTitle: "Superseded by a newer commit"},
		{name: "superseded apply of a planned stack", run: applyRun(v1.RunSuperseded), status: v1.StackPlanned, wave: 1, wantStatus: StatusCompleted, wantConcl: ConclusionCancelled, wantTitle: "Superseded by a newer commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := stack("stacks/prod/app", "production", tt.wave, 0, withSummary(1, 0, 0, 0), withStatus(tt.status))
			if tt.mutate != nil {
				tt.mutate(&rs)
			}
			got := StackCheck(tt.run, rs, tt.opts)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantConcl, got.Conclusion)
			assert.Equal(t, tt.wantTitle, got.Title)
		})
	}
}

func TestRollupConclusion(t *testing.T) {
	st := func(s v1.StackStatus) v1.RunStack {
		return stack("stacks/"+string(s), "", 0, 0, withStatus(s), withSummary(0, 0, 0, 0))
	}
	drift := func(status v1.RunStatus, stacks ...v1.RunStack) v1.Run {
		r := baseRun(status, stacks...)
		r.Mode = v1.ModeDrift
		return r
	}
	tests := []struct {
		name       string
		run        v1.Run
		wantStatus string
		wantConcl  string
		wantTitle  string
	}{
		{name: "failure wins over running", run: baseRun(v1.RunPlanning, st(v1.StackFailed), st(v1.StackPlanning)), wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "1 failed, 1 still running"},
		{name: "unknown fails", run: applyRun(v1.RunFailed, st(v1.StackUnknown), st(v1.StackApplied)), wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "1 unknown"},
		{name: "failed run without failed stacks", run: baseRun(v1.RunFailed), wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "Run failed"},
		{name: "running before unconfirmed", run: baseRun(v1.RunPlanning, st(v1.StackUnconfirmed), st(v1.StackPlanning)), wantStatus: StatusInProgress, wantTitle: "Planning: 1 of 2 stacks done"},
		{name: "all pending is queued", run: baseRun(v1.RunPending, st(v1.StackPending)), wantStatus: StatusQueued, wantTitle: "Planning: 0 of 1 stacks done"},
		{name: "unconfirmed stack is neutral", run: baseRun(v1.RunPlanned, st(v1.StackUnconfirmed), st(v1.StackPlanned)), wantStatus: StatusCompleted, wantConcl: ConclusionNeutral, wantTitle: "Unconfirmed: 2 stacks checked without the server"},
		{name: "no stacks yet", run: baseRun(v1.RunPlanning), wantStatus: StatusQueued, wantTitle: "Waiting for the affected stacks"},
		{name: "planned no changes", run: baseRun(v1.RunPlanned, st(v1.StackPlanned), st(v1.StackSkipped)), wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "No changes in 2 stacks"},
		{name: "applied no changes", run: applyRun(v1.RunApplied, st(v1.StackNoop)), wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "Applied 1 stack, no changes"},
		{name: "applied subset counts only applied stacks", run: applyRun(v1.RunApplied,
			stack("stacks/a", "", 0, 0, withStatus(v1.StackApplied), withSummary(1, 0, 0, 0)),
			stack("stacks/b", "", 0, 0, withStatus(v1.StackSkipped), withSummary(4, 0, 0, 0)),
			stack("stacks/c", "", 1, 0, withStatus(v1.StackSkipped), withSummary(0, 2, 0, 0))),
			wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "Applied 1 stack (2 skipped): 1 added, 0 changed, 0 destroyed"},
		{name: "drift clean", run: drift(v1.RunPlanned, st(v1.StackPlanned)), wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "No drift in 1 stack"},
		{name: "drift found", run: drift(v1.RunPlanned, stack("stacks/a", "", 0, 0, withSummary(0, 2, 0, 0))), wantStatus: StatusCompleted, wantConcl: ConclusionSuccess, wantTitle: "Drift in 1 stack: 0 to add, 2 to change, 0 to destroy"},
		{name: "drift running", run: drift(v1.RunPlanning, st(v1.StackPlanning)), wantStatus: StatusInProgress, wantTitle: "Checking drift: 0 of 1 stacks done"},
		{name: "apply between waves is not green", run: func() v1.Run {
			r := applyRun(v1.RunApplying, st(v1.StackApplied), stack("stacks/later", "", 1, 0, withSummary(1, 0, 0, 0)))
			return r
		}(), wantStatus: StatusInProgress, wantTitle: "Applying wave 0 of 3: 1 of 2 stacks done"},
		{name: "apply requested before any dispatch is queued", run: func() v1.Run {
			r := baseRun(v1.RunPending, st(v1.StackPlanned), st(v1.StackPlanned))
			r.Mode = v1.ModeApply
			return r
		}(), wantStatus: StatusQueued, wantTitle: "Applying wave 0 of 3: 0 of 2 stacks done"},
		{name: "plan run applying with later waves planned", run: baseRun(v1.RunApplying, st(v1.StackApplied), stack("stacks/later", "", 1, 0)), wantStatus: StatusInProgress, wantTitle: "Applying wave 0 of 3: 1 of 2 stacks done"},
		{name: "failed apply run does not claim stacks still run", run: applyRun(v1.RunFailed, st(v1.StackFailed), stack("stacks/later", "", 1, 0)), wantStatus: StatusCompleted, wantConcl: ConclusionFailure, wantTitle: "1 failed"},
		{name: "applied run with a stack left planned is not green", run: applyRun(v1.RunApplied, st(v1.StackApplied), stack("stacks/later", "", 1, 0)), wantStatus: StatusInProgress, wantTitle: "Applying wave 0 of 3: 1 of 2 stacks done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RollupCheck(tt.run, Options{})
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantConcl, got.Conclusion)
			assert.Equal(t, tt.wantTitle, got.Title)
		})
	}
}

func TestSpellCycle(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{[]string{"a", "b", "c", "a"}, "a → b → c → a"},
		{[]string{"a", "b", "c"}, "a → b → c → a"},
		{[]string{"a"}, "a → a"},
		{[]string{"a", "a"}, "a → a"},
		{nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, SpellCycle(tt.in))
		})
	}
}

func TestCheckOutputLimits(t *testing.T) {
	huge := hugeRun(3000, 2<<20/3000)
	huge.Warnings = []string{strings.Repeat("w", 1<<20)}
	big := huge.Stacks[0]
	big.PlanText = strings.Repeat("resource line\n", 200_000)
	checks := map[string]CheckOutput{
		"stack":  StackCheck(huge, big, testOpts),
		"rollup": RollupCheck(huge, testOpts),
	}
	resp := &v1.ResolveResponse{Waves: [][]string{{}}}
	for i := range 5000 {
		key := fmt.Sprintf("stacks/prod/service-%04d", i)
		resp.Affected = append(resp.Affected, v1.AffectedStack{Key: key, Path: key, Via: []string{strings.Repeat("m", 100)}})
		resp.Waves[0] = append(resp.Waves[0], key)
	}
	checks["resolve"] = ResolveCheck(resp, nil, testOpts)
	checks["resolve error"] = ResolveCheck(nil, fmt.Errorf("upload: %w", fmt.Errorf("%s", strings.Repeat("e", 200_000))), testOpts)
	for name, c := range checks {
		t.Run(name, func(t *testing.T) {
			assert.LessOrEqual(t, len(c.Summary), MaxCheckText)
			assert.LessOrEqual(t, len(c.Text), MaxCheckText)
			assert.True(t, utf8.ValidString(c.Summary) && utf8.ValidString(c.Text))
		})
	}
	assert.Contains(t, checks["stack"].Text, TruncationNote)
	assert.Contains(t, checks["resolve"].Summary, "and 4950 more")
}

func TestFenceOutlastsBackticksInPlan(t *testing.T) {
	rs := stack("stacks/a", "default", 0, 1, withSummary(1, 0, 0, 0), withText("value = <<EOT\n```\nnot the end\n````\nEOT\n"))
	text := StackCheck(baseRun(v1.RunPlanned, rs), rs, Options{}).Text
	assert.True(t, strings.HasPrefix(text, "`````hcl\n"), text)
	assert.True(t, strings.HasSuffix(text, "\n`````\n"), text)
}

func TestCheckTitleIsOneBoundedLine(t *testing.T) {
	rs := stack("stacks/a", "", 1, 0, withStatus(v1.StackBlocked), func(rs *v1.RunStack) { rs.BlockedBy = []string{strings.Repeat("x\n", 400)} })
	title := StackCheck(applyRun(v1.RunFailed, rs), rs, Options{}).Title
	assert.LessOrEqual(t, len(title), maxTitle)
	assert.NotContains(t, title, "\n")
}

func checkDump(c CheckOutput) string {
	return "title: " + c.Title + "\nstatus: " + c.Status + "\nconclusion: " + c.Conclusion +
		"\n\n--- summary ---\n" + c.Summary + "\n--- text ---\n" + c.Text
}

func TestApplyBetweenWaves(t *testing.T) {
	done := stack("stacks/prod/vpc", "production", 0, 1, withSummary(1, 0, 0, 0), withStatus(v1.StackApplied))
	next := stack("stacks/prod/eks", "production", 1, 2, withSummary(0, 1, 0, 0))
	run := applyRun(v1.RunApplying, done, next)

	assert.Contains(t, StickyComment(run, Options{}), "\nApplying wave 0 of 3: 1 of 2 stacks done.\n")
	assert.NotEqual(t, ConclusionSuccess, RollupCheck(run, Options{}).Conclusion)
	assert.Equal(t, StatusQueued, StackCheck(run, next, Options{}).Status)

	failed := done
	failed.Status = v1.StackFailed
	run = applyRun(v1.RunFailed, failed, next)
	check := StackCheck(run, next, Options{})
	assert.Equal(t, ConclusionCancelled, check.Conclusion)
	assert.Contains(t, check.Summary, "The run ended before this stack's wave was dispatched, so nothing was applied for it.")
	assert.NotContains(t, StickyComment(run, Options{}), "still running")
}

func TestStackCheckFindsLockByKeyOrID(t *testing.T) {
	rs := stack("stacks/prod/vpc", "production", 0, 1, withSummary(1, 0, 0, 0))
	run := baseRun(v1.RunPlanned, rs)
	tests := map[string]struct {
		lock v1.LockInfo
		want bool
	}{
		"by key":             {v1.LockInfo{StackKey: rs.Key, RunID: "r", PRNumber: 17}, true},
		"by id without key":  {v1.LockInfo{StackID: rs.StackID, RunID: "r", PRNumber: 17}, true},
		"other key, same id": {v1.LockInfo{StackKey: "stacks/other", StackID: rs.StackID, RunID: "r", PRNumber: 17}, false},
		"other stack":        {v1.LockInfo{StackID: "stk-other", RunID: "r", PRNumber: 17}, false},
		"own pull request":   {v1.LockInfo{StackKey: rs.Key, RunID: "r", PRNumber: run.PRNumber}, false},
		"empty lock":         {v1.LockInfo{RunID: "r", PRNumber: 17}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			summary := StackCheck(run, rs, Options{Locks: []v1.LockInfo{tt.lock}}).Summary
			assert.Equal(t, tt.want, strings.Contains(summary, "**Locked:**"), summary)
		})
	}
}

func TestStacksLeftBehindByAnEndedRun(t *testing.T) {
	tests := []struct {
		name string
		mode v1.RunMode
		stat v1.StackStatus
		want string
	}{
		{"plan", v1.ModePlan, v1.StackPending, "nothing was planned for it"},
		{"apply", v1.ModeApply, v1.StackPlanned, "nothing was applied for it"},
		{"drift", v1.ModeDrift, v1.StackPlanning, "nothing was checked for it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := stack("stacks/b", "", 1, 0, withStatus(tt.stat))
			run := baseRun(v1.RunFailed, stack("stacks/a", "", 0, 0, withStatus(v1.StackFailed)), left)
			run.Mode = tt.mode
			got := StackCheck(run, left, Options{})
			assert.Equal(t, StatusCompleted, got.Status)
			assert.Equal(t, ConclusionCancelled, got.Conclusion)
			assert.Contains(t, got.Summary, tt.want)
		})
	}
}
