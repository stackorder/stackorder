package report

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestStepSummaryGolden(t *testing.T) {
	sum := &v1.PlanSummary{Adds: 2, Changes: 1, Replaces: 1, Imports: 1, Added: []string{"aws_vpc.main", "aws_subnet.a"}, Changed: []string{"aws_route_table.main"}, Replaced: []string{"aws_instance.nat"}}
	tests := map[string]struct {
		res v1.StackResult
		key string
	}{
		"step_plan": {v1.StackResult{Mode: v1.ModePlan, Status: v1.ResultSuccess, ExitCode: 2, HasChanges: true, Summary: sum,
			PlanText: tfPlan("+", "aws_vpc.main", "aws_subnet.a"), Artifact: v1.PlanArtifactName("stacks/prod/vpc", headSHA),
			JobURL: jobBase + "101", Tool: v1.ToolTofu, ToolVersion: "1.12.0", DurationMS: 42_400}, "stacks/prod/vpc"},
		"step_plan_failed": {v1.StackResult{Status: v1.ResultFailure, ExitCode: 1, ErrorText: "Error: Failed to get existing workspaces: S3 bucket does not exist.\n",
			Tool: v1.ToolTerraform, ToolVersion: "1.14.0", DurationMS: 850, Unconfirmed: true}, "stacks/prod/vpc"},
		"step_apply_summary_mode": {v1.StackResult{Mode: v1.ModeApply, Status: v1.ResultSuccess, Summary: sum, Truncated: true}, "stacks/prod/secrets"},
		"step_drift":              {v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, ExitCode: 2, HasChanges: true, Summary: &v1.PlanSummary{Changes: 1}, PlanText: tfPlan("~", "aws_security_group.web"), Truncated: true}, "stacks/prod/vpc"},
		"step_drift_clean":        {v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultSuccess, Summary: &v1.PlanSummary{}}, "stacks/prod/vpc"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			golden(t, name, StepSummary(tc.res, tc.key, testOpts))
		})
	}
}

func TestStepSummaryHeadlines(t *testing.T) {
	tests := []struct {
		name string
		res  v1.StackResult
		want string
	}{
		{"default mode and status", v1.StackResult{Summary: &v1.PlanSummary{Adds: 1}}, "**Result:** success · **Exit code:** 0\n\n1 to add, 0 to change, 0 to destroy."},
		{"apply error", v1.StackResult{Mode: v1.ModeApply, Status: v1.ResultError}, "\n\nApply failed."},
		{"drift failure", v1.StackResult{Mode: v1.ModeDrift, Status: v1.ResultFailure}, "\n\nDrift check failed."},
		{"applied without summary", v1.StackResult{Mode: v1.ModeApply}, "Applied: no changes."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, StepSummary(tt.res, "stacks/a", Options{}), tt.want)
		})
	}
}

func TestStepSummaryCapsPlanText(t *testing.T) {
	res := v1.StackResult{PlanText: strings.Repeat("plan line\n", MaxStepSummary/5), ErrorText: strings.Repeat("e\n", maxErrorText)}
	out := StepSummary(res, "stacks/a", Options{})
	assert.LessOrEqual(t, len(out), MaxStepSummary)
	assert.Equal(t, 2, strings.Count(out, TruncationNote))
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{850, "850ms"},
		{1_000, "1s"},
		{65_400, "1m5s"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, formatDuration(tt.ms))
		})
	}
}
