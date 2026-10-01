package report

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/command"
)

func TestCommentsGolden(t *testing.T) {
	failures := []GateFailure{
		{Layer: LayerLocks, Reason: "`stacks/prod/eks` is locked by #17.", Stacks: []string{"stacks/prod/eks"}},
		{Layer: LayerAuthorization, Reason: "@bob is not an active member of `acme/platform-prod`, required by `apply.allowed_teams`.", Stacks: []string{"stacks/prod/vpc", "stacks/prod/eks"}},
		{Layer: LayerApprovals, Name: "four eyes", Reason: "The apply was requested by the pull request author; `apply.four_eyes` requires someone else."},
		{Layer: LayerPlans, Reason: "No plan for the head commit `3f9a2c1`; the last plan is for `0a1b2c3`.\nPush again or comment `stackorder plan`.", Stacks: []string{"stacks/prod/apps:blue"}},
	}
	locks := []v1.LockInfo{
		{StackKey: "stacks/prod/vpc", RunID: runID, PRNumber: 42, TakenAt: t0, Reason: "apply"},
		{StackKey: "stacks/prod/eks", RunID: runID, PRNumber: 42, TakenAt: t0},
	}
	applied := plannedStacks()
	for i := range applied {
		applied[i].Status = v1.StackApplied
	}
	applied[1].Status = v1.StackNoop
	appliedRun := applyRun(v1.RunApplied, applied...)
	quietRun := applyRun(v1.RunApplied,
		stack("stacks/prod/vpc", "production", 0, 201, withStatus(v1.StackNoop), withSummary(0, 0, 0, 0)),
		stack("stacks/prod/dns", "production", 1, 202, withStatus(v1.StackNoop), withSummary(0, 0, 0, 0)))
	quietRun.Waves = 2
	subsetRun := applyRun(v1.RunApplied,
		stack("stacks/prod/vpc", "production", 0, 201, withStatus(v1.StackApplied), withSummary(1, 0, 0, 0)),
		stack("stacks/prod/eks", "production", 1, 0, withStatus(v1.StackSkipped), withSummary(0, 2, 0, 0)))
	subsetRun.Waves = 2
	tests := map[string]string{
		"applied":            AppliedComment(appliedRun, false, testOpts),
		"applied_no_changes": AppliedComment(quietRun, true, testOpts),
		"applied_subset":     AppliedComment(subsetRun, false, Options{}),
		"refusal":            RefusalComment("stackorder apply", failures, testOpts),
		"refusal_no_reasons": RefusalComment("stackorder apply stacks/prod/vpc", nil, testOpts),
		"lock_warning":       LockWarningComment(locks, testOpts),
		"lock_warning_none":  LockWarningComment(nil, testOpts),
		"unlocked":           UnlockedComment(locks, "alice"),
		"unlocked_none":      UnlockedComment(nil, "alice"),
	}
	for name, got := range tests {
		t.Run(name, func(t *testing.T) {
			golden(t, name, got)
		})
	}
}

func TestRenderedCommentsNeverParseAsCommands(t *testing.T) {
	cases := stickyCases()
	bodies := make([]string, 0, len(cases)+6)
	for _, tc := range cases {
		bodies = append(bodies, StickyComment(tc.run, tc.opts))
	}
	locks := []v1.LockInfo{{StackKey: "stacks/a", RunID: runID, PRNumber: 1}}
	bodies = append(bodies,
		RefusalComment("stackorder apply", []GateFailure{{Layer: 9, Reason: "custom"}}, testOpts),
		LockWarningComment(locks, testOpts),
		UnlockedComment(locks, "someone[bot]"),
		UnconfirmedNote(""),
		AppliedComment(applyRun(v1.RunApplied, plannedStacks()...), false, testOpts),
	)
	for i, body := range bodies {
		_, ok := command.Parse(body)
		assert.False(t, ok, "body %d parses as a command:\n%s", i, body)
	}
}

func TestMention(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"alice", "@alice"},
		{"dependabot[bot]", "@dependabot[bot]"},
		{"api-key:ci", "api-key:ci"},
		{"[bot]", `\[bot\]`},
		{"a b", "a b"},
		{strings.Repeat("x", 65), strings.Repeat("x", 65)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, mention(tt.in))
		})
	}
	assert.Equal(t, "Released 1 orchestration lock:\n\n- `stack-id`\n\nThe S3 state locks are untouched; release them separately if a runner died mid-apply.\n",
		UnlockedComment([]v1.LockInfo{{StackID: "stack-id"}}, ""))
}

func TestLayerName(t *testing.T) {
	for layer := LayerAuthorization; layer <= LayerLocks; layer++ {
		assert.NotEmpty(t, LayerName(layer))
	}
	assert.Empty(t, LayerName(0))
}

func TestGateFailureLimits(t *testing.T) {
	failures := make([]GateFailure, 0, 25)
	keys := make([]string, 30)
	for i := range keys {
		keys[i] = fmt.Sprintf("stacks/s%02d", i)
	}
	for i := range 25 {
		failures = append(failures, GateFailure{Layer: i%5 + 1, Reason: "reason\nwith a newline", Stacks: keys})
	}
	out := RefusalComment("stackorder apply", failures, Options{})
	assert.Contains(t, out, "- … and 5 more\n")
	assert.Contains(t, out, "and 10 more)")
	assert.Contains(t, out, ": reason with a newline")
	assert.Contains(t, out, "- **Layer 1, authorization** (")
}
