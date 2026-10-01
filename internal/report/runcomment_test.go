package report

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestRunCommentGolden(t *testing.T) {
	for name, tc := range runCommentCases() {
		t.Run(name, func(t *testing.T) {
			golden(t, name, RunComment(tc.run, tc.opts))
		})
	}
}

func TestRunMarkerIsTheFirstLine(t *testing.T) {
	for name, tc := range runCommentCases() {
		t.Run(name, func(t *testing.T) {
			first, _, _ := strings.Cut(RunComment(tc.run, tc.opts), "\n")
			assert.Equal(t, RunMarker(runID), first)
		})
	}
}

func TestRunCommentBudget(t *testing.T) {
	run := hugeRun(2000, 1<<10)
	run.Mode, run.Status = v1.ModeApply, v1.RunApplying
	out := RunComment(run, testOpts)
	require.LessOrEqual(t, len(out), MaxComment)
	require.LessOrEqual(t, utf8.RuneCountInString(out), MaxComment)
	assert.True(t, strings.HasPrefix(out, RunMarker(runID)+"\n"))
	assert.Contains(t, out, "more stacks not listed")
	assert.NotContains(t, out, "<details>")
}

func TestRunCommentHasNoPlanOutput(t *testing.T) {
	for name, tc := range runCommentCases() {
		t.Run(name, func(t *testing.T) {
			out := RunComment(tc.run, tc.opts)
			assert.NotContains(t, out, "<details>")
			assert.NotContains(t, out, "```")
			assert.NotContains(t, out, "Terraform will perform the following actions")
			assert.NotContains(t, out, "VpcLimitExceeded")
			assert.NotContains(t, out, "aws_secretsmanager_secret")
		})
	}
}

func TestRunCommentLeavesOutOtherLocks(t *testing.T) {
	tc := runCommentCases()["run_warned"]
	out := RunComment(tc.run, tc.opts)
	assert.Contains(t, out, "**Warnings**")
	assert.NotContains(t, out, "Locked by other pull requests")
	assert.NotContains(t, out, "#17")
}

func TestAppliedSubsetCountsOnlyAppliedStacks(t *testing.T) {
	run := applyRun(v1.RunApplied,
		stack("stacks/a", "", 0, 0, withStatus(v1.StackApplied), withSummary(1, 0, 0, 0)),
		stack("stacks/b", "", 1, 0, withStatus(v1.StackSkipped), withSummary(4, 0, 0, 0)))
	assert.Contains(t, RunComment(run, Options{}), "\nApplied 1 stack in 3 waves, skipped 1 not in the requested subset: 1 added, 0 changed, 0 destroyed.\n")

	run.Stacks[1].Status = v1.StackPlanned
	assert.Contains(t, RunComment(run, Options{}), "\nApplied 1 stack in 3 waves: 1 added, 0 changed, 0 destroyed.\n")
}
