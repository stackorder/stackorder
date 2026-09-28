package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestSummaryLine(t *testing.T) {
	tests := []struct {
		name string
		in   *v1.PlanSummary
		want string
	}{
		{name: "missing", in: nil, want: "no plan summary"},
		{name: "empty", in: &v1.PlanSummary{}, want: "no changes"},
		{name: "counts", in: &v1.PlanSummary{Adds: 1, Changes: 2, Destroys: 3, Replaces: 4}, want: "1 to add, 2 to change, 3 to destroy, 4 to replace"},
		{name: "imports, moves and outputs", in: &v1.PlanSummary{Imports: 2, Moves: 1, OutputChanges: 3}, want: "0 to add, 0 to change, 0 to destroy, 0 to replace, 2 to import, 1 moved, 3 output changes"},
		{name: "outputs only", in: &v1.PlanSummary{OutputChanges: 1}, want: "0 to add, 0 to change, 0 to destroy, 0 to replace, 1 output changes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, summaryLine(tt.in))
		})
	}
}

func TestClip(t *testing.T) {
	assert.Equal(t, "abc", clip("abc", 5))
	assert.Equal(t, "ab", clip("abc", 2))
	assert.Equal(t, "a", clip("aé", 2))
	assert.Equal(t, "aé", clip("aé", 3))
}

func TestCycleLines(t *testing.T) {
	assert.Equal(t, []string{"cycle: a -> b -> a", "cycle: c -> c", "cycle: "}, cycleLines([][]string{{"a", "b", "a"}, {"c"}, {}}))
}

func TestStackMarkdown(t *testing.T) {
	many := make([]string, 60)
	for i := range many {
		many[i] = fmt.Sprintf("null_resource.r[%d]", i)
	}
	t.Run("success lists addresses", func(t *testing.T) {
		md := stackMarkdown(v1.ModePlan, "stacks/app", &v1.StackResult{Status: v1.ResultSuccess, Summary: &v1.PlanSummary{Adds: 60, Added: many, Destroys: 1, Destroyed: []string{"aws_s3_bucket.old"}}}, "a note")
		assert.True(t, strings.HasPrefix(md, "### stackorder plan: `stacks/app`\n\n> a note\n\n**60 to add, 0 to change, 1 to destroy, 0 to replace**\n"))
		assert.Contains(t, md, "<details><summary>Added (60)</summary>")
		assert.Contains(t, md, "- `null_resource.r[49]`\n- and 10 more\n")
		assert.NotContains(t, md, "null_resource.r[50]")
		assert.Contains(t, md, "<details><summary>Destroyed (1)</summary>\n\n- `aws_s3_bucket.old`\n")
		assert.NotContains(t, md, "Changed")
	})
	t.Run("failure quotes the error", func(t *testing.T) {
		md := stackMarkdown(v1.ModeApply, "stacks/app", &v1.StackResult{Status: v1.ResultFailure, ExitCode: 1, ErrorText: "Error: ```boom```"}, "")
		assert.Equal(t, "### stackorder apply: `stacks/app`\n\n**failure** (exit code 1)\n\n```\nError: '''boom'''\n```\n", md)
	})
	t.Run("error without text", func(t *testing.T) {
		md := stackMarkdown(v1.ModeDrift, "stacks/app", &v1.StackResult{Status: v1.ResultError, ExitCode: 1}, "")
		assert.Equal(t, "### stackorder drift: `stacks/app`\n\n**error** (exit code 1)\n", md)
	})
}

func TestResolveMarkdown(t *testing.T) {
	resp := sampleResolve()
	resp.RunID = "run-1"
	md := resolveMarkdown(resp, "")
	assert.Equal(t, "### stackorder resolve\n\nRun `run-1`.\n\n2 stacks affected in 2 waves.\n\n"+
		"| Wave | Stack | Reasons | Environment |\n| --- | --- | --- | --- |\n"+
		"| 0 | `stacks/a` | changed,module | production |\n"+
		"| 1 | `stacks/b` | dependent | default |\n"+
		"\n- warning: stacks/b is locked by PR #3\n", md)
	assert.Equal(t, "### stackorder resolve\n\n> careful\n\nNo stacks affected.\n", resolveMarkdown(&v1.ResolveResponse{}, "careful"))
}

func TestWriteAffectedTableErrors(t *testing.T) {
	err := writeAffectedTable(&failingWriter{}, &v1.ResolveResponse{})
	require.ErrorContains(t, err, "disk full")
	err = writeAffectedTable(&failingWriter{}, sampleResolve())
	require.ErrorContains(t, err, "disk full")
	require.ErrorContains(t, writeJSON(&failingWriter{}, 1), "writing JSON")
}
