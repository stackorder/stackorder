package report

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestDriftIssueGolden(t *testing.T) {
	finished := t0.Add(-72 * time.Hour)
	stack := v1.StackDetail{
		ID: "stk-prod-vpc", Repo: "acme/infra", Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Environment: "production",
		Backend:   &v1.Backend{Type: "s3", Bucket: "acme-tfstate", Key: "prod/vpc.tfstate", Region: "eu-west-1"},
		LastApply: &v1.RunStackRef{RunID: "8e7d6c5b-0000-4000-8000-000000000031", SHA: "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567", PRNumber: 31, Status: v1.StackApplied, FinishedAt: &finished},
	}
	drifted := v1.DriftStatus{CheckedAt: t0, Drifted: true, Summary: &v1.PlanSummary{
		Changes: 2, Destroys: 1, Changed: []string{"aws_security_group.web", "aws_route_table.private"}, Destroyed: []string{"aws_route.legacy"},
	}}
	tests := map[string]struct {
		stack v1.StackDetail
		drift v1.DriftStatus
	}{
		"drift_issue":            {stack, drifted},
		"drift_issue_reconciled": {stack, v1.DriftStatus{CheckedAt: t0.Add(24 * time.Hour)}},
		"drift_issue_minimal":    {v1.StackDetail{Path: "stacks/dev/app", Workspace: "green"}, v1.DriftStatus{Drifted: true}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			title, body := DriftIssue(tc.stack, tc.drift, testOpts)
			golden(t, name, "title: "+title+"\n\n"+body)
		})
	}
}

func TestDriftIssueTitle(t *testing.T) {
	assert.Equal(t, "Drift detected in stacks/prod/vpc", DriftIssueTitle("stacks/prod/vpc"))
	assert.Equal(t, "Drift detected in stacks/dev/app:green", DriftIssueTitle("stacks/dev/app:green"))
}
