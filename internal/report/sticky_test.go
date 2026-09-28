package report

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestStickyCommentGolden(t *testing.T) {
	for name, tc := range stickyCases() {
		t.Run(name, func(t *testing.T) {
			golden(t, name, StickyComment(tc.run, tc.opts))
		})
	}
}

func TestStickyCommentBudget(t *testing.T) {
	tests := []struct {
		name        string
		stacks      int
		textBytes   int
		wantDetails int
		wantOmitted bool
	}{
		{name: "20 stacks with 300 KB plans each", stacks: 20, textBytes: 300 << 10, wantDetails: 20},
		{name: "3 stacks share the budget", stacks: 3, textBytes: 256 << 10, wantDetails: 3},
		{name: "200 stacks drop late plans", stacks: 200, textBytes: 50 << 10, wantOmitted: true},
		{name: "2000 stacks trim the table", stacks: 2000, textBytes: 1 << 10, wantOmitted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := hugeRun(tt.stacks, tt.textBytes)
			out := StickyComment(run, testOpts)
			require.LessOrEqual(t, len(out), MaxComment)
			require.LessOrEqual(t, utf8.RuneCountInString(out), MaxComment)
			assert.True(t, strings.HasPrefix(out, Marker+"\n"))
			assert.True(t, strings.HasSuffix(out, "</sub>\n"), "footer must survive the budget")
			details := strings.Count(out, "<details>")
			assert.Equal(t, details, strings.Count(out, "</details>"))
			if tt.wantDetails > 0 {
				assert.Equal(t, tt.wantDetails, details)
				assert.Equal(t, tt.wantDetails, strings.Count(out, TruncationNote))
			}
			if tt.wantOmitted {
				assert.Contains(t, out, "is not shown, to stay within GitHub's comment size limit")
			}
			assert.Greater(t, len(out), MaxComment*9/10, "the budget should be used, not wasted")
		})
	}
}

func TestStickyCommentPathologicalInputs(t *testing.T) {
	run := hugeRun(50, 10<<10)
	run.Stacks[0].Key = strings.Repeat("stacks/very-long-directory-name/", 2000)
	for range 500 {
		run.Warnings = append(run.Warnings, strings.Repeat("warning text ", 200))
	}
	o := testOpts
	for i := range 500 {
		o.Locks = append(o.Locks, v1.LockInfo{StackKey: fmt.Sprintf("stacks/prod/service-%02d", i), RunID: "r", PRNumber: 7})
		o.PendingApprovals = append(o.PendingApprovals, Approval{Environment: fmt.Sprintf("env-%03d", i), URL: "https://github.com/acme/infra/actions/runs/1"})
	}
	out := StickyComment(run, o)
	assert.LessOrEqual(t, len(out), MaxComment)
	assert.True(t, strings.HasPrefix(out, Marker+"\n"))
}

func TestMarkerIsAlwaysTheFirstLine(t *testing.T) {
	for name, tc := range stickyCases() {
		t.Run(name, func(t *testing.T) {
			first, _, _ := strings.Cut(StickyComment(tc.run, tc.opts), "\n")
			assert.Equal(t, Marker, first)
		})
	}
	first, _, _ := strings.Cut(StickyComment(v1.Run{}, Options{}), "\n")
	assert.Equal(t, Marker, first)
}

func TestStickyCommentIsOrderIndependent(t *testing.T) {
	run := baseRun(v1.RunPlanned, plannedStacks()...)
	reversed := run
	reversed.Stacks = nil
	for i := len(run.Stacks) - 1; i >= 0; i-- {
		reversed.Stacks = append(reversed.Stacks, run.Stacks[i])
	}
	assert.Equal(t, StickyComment(run, testOpts), StickyComment(reversed, testOpts))
}

func TestSummaryModeNeverLeaksPlanText(t *testing.T) {
	rs := stack("stacks/prod/secrets", "production", 0, 1, withSummary(1, 0, 0, 0),
		withAddrs([]string{"aws_secretsmanager_secret.db"}, nil, nil, nil),
		withText("password = \"hunter2\"\n"),
		func(rs *v1.RunStack) { rs.PlanOutput = string(v1.PlanOutputSummary) })
	run := baseRun(v1.RunPlanned, rs)
	for name, out := range map[string]string{
		"sticky":  StickyComment(run, testOpts),
		"check":   checkDump(StackCheck(run, rs, testOpts)),
		"rollup":  checkDump(RollupCheck(run, testOpts)),
		"applied": checkDump(StackCheck(run, func() v1.RunStack { r := rs; r.Status = v1.StackApplied; return r }(), testOpts)),
	} {
		assert.NotContains(t, out, "hunter2", name)
	}
	assert.Contains(t, StickyComment(run, testOpts), "`aws_secretsmanager_secret.db`")
}

func TestStickyShowsAddressesWithoutPlanText(t *testing.T) {
	rs := stack("stacks/a", "default", 0, 1, withSummary(1, 0, 0, 0), withAddrs([]string{"aws_s3_bucket.logs"}, nil, nil, nil))
	out := StickyComment(baseRun(v1.RunPlanned, rs), Options{})
	assert.Contains(t, out, "<details><summary><code>stacks/a</code>: planned, 1 to add, 0 to change, 0 to destroy</summary>\n\n**To add (1)**\n\n- `aws_s3_bucket.logs`\n\n</details>\n")
	assert.NotContains(t, out, "Plan output is set to `summary`")
}

func TestStickyDetailsSkipEmptySummaryMode(t *testing.T) {
	rs := stack("stacks/a", "default", 0, 1, withSummary(1, 0, 0, 0), func(rs *v1.RunStack) { rs.PlanOutput = string(v1.PlanOutputSummary) })
	out := StickyComment(baseRun(v1.RunPlanned, rs), Options{})
	assert.NotContains(t, out, "<details>")
}

func TestStickySafetyNetKeepsTheLimit(t *testing.T) {
	o := Options{}
	for i := range maxListItems {
		o.PendingApprovals = append(o.PendingApprovals, Approval{Environment: string(rune('a' + i)), URL: "https://example.com/" + strings.Repeat("x", 5000)})
	}
	out := StickyComment(hugeRun(5, 1<<10), o)
	assert.LessOrEqual(t, len(out), MaxComment)
	assert.True(t, strings.HasPrefix(out, Marker+"\n"))
	assert.True(t, strings.HasSuffix(out, TruncationNote+"\n"))
}

func TestStickyFailedRunWithoutFailedStack(t *testing.T) {
	out := StickyComment(baseRun(v1.RunFailed, stack("stacks/a", "", 0, 0, withSummary(0, 0, 0, 0))), Options{})
	assert.Contains(t, out, "1 stack in 3 waves: no changes.\n\nThe run failed although no stack reported a failure; see the run details.\n")
}

func BenchmarkStickyComment(b *testing.B) {
	for _, n := range []int{300, 3000, 10000} {
		run := hugeRun(n, 2<<10)
		b.Run(fmt.Sprintf("%d stacks", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = StickyComment(run, testOpts)
			}
		})
	}
}

func TestStickyAppliedSubsetCountsOnlyAppliedStacks(t *testing.T) {
	run := applyRun(v1.RunApplied,
		stack("stacks/a", "", 0, 0, withStatus(v1.StackApplied), withSummary(1, 0, 0, 0)),
		stack("stacks/b", "", 1, 0, withStatus(v1.StackSkipped), withSummary(4, 0, 0, 0)))
	assert.Contains(t, StickyComment(run, Options{}), "\nApplied 1 stack in 3 waves, skipped 1 not in the requested subset: 1 added, 0 changed, 0 destroyed.\n")
}
