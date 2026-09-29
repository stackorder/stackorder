package runs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestAllowedTeams(t *testing.T) {
	cfg := defaulted(v1.RepoConfig{Apply: v1.ApplyConfig{AllowedTeams: []string{"platform"}}})
	kyc := &v1.StackConfig{
		Apply: &v1.StackApplyConfig{AllowedTeams: []string{"kyc"}},
		Instances: v1.Instances{
			"production": {Apply: &v1.StackApplyConfig{AllowedTeams: []string{"kyc-prod"}}},
			"staging":    {},
		},
	}
	broken := &v1.StackConfig{Instances: v1.Instances{
		"production": {Environment: `{{ if eq .Instance "production" }}{{ .Nope }}{{ end }}`},
		"staging":    {Environment: `{{ if eq .Instance "production" }}{{ .Nope }}{{ end }}`},
	}}
	defaults := map[string]*v1.StackConfig{
		"infra/kyc":        kyc,
		"infra/vpc":        {PlanOutput: v1.PlanOutputSummary, Apply: &v1.StackApplyConfig{AllowedTeams: []string{"network"}}},
		"infra/app":        broken,
		"stacks/prod/apps": {Workspace: "blue", Apply: &v1.StackApplyConfig{AllowedTeams: []string{"apps"}}},
	}
	tests := []struct {
		key     string
		want    []string
		wantErr string
	}{
		{key: "infra/kyc:production", want: []string{"kyc-prod"}},
		{key: "infra/kyc:staging", want: []string{"kyc"}},
		{key: "infra/kyc:new", want: []string{"kyc"}},
		{key: "infra/vpc", want: []string{"network"}},
		{key: "infra/vpc:production", want: []string{"network"}},
		{key: "infra/net", want: []string{"platform"}},
		{key: "stacks/unknown", want: []string{"platform"}},
		{key: "stacks/prod/apps:blue", want: []string{"apps"}},
		{key: "infra/app:staging", want: []string{"platform"}},
		{key: "infra/app:production", wantErr: `map has no entry for key "Nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := allowedTeams(cfg, defaults, tt.key)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSummaryOnDefaultBranch(t *testing.T) {
	full := defaulted(v1.RepoConfig{PlanOutput: v1.PlanOutputFull})
	defaults := map[string]*v1.StackConfig{
		"infra/kyc": {Instances: v1.Instances{"production": {PlanOutput: v1.PlanOutputSummary}, "staging": {}}},
		"infra/vpc": {PlanOutput: v1.PlanOutputSummary},
		"infra/app": {Environment: "{{ .Nope }}"},
		"infra/net": {PlanOutput: v1.PlanOutputFull},
	}
	tests := []struct {
		name    string
		cfg     *v1.RepoConfig
		stack   v1.AffectedStack
		want    bool
		wantErr bool
	}{
		{name: "an instance override", cfg: full, stack: v1.AffectedStack{Key: "infra/kyc:production", Path: "infra/kyc", Instance: "production"}, want: true},
		{name: "a sibling instance keeps full", cfg: full, stack: v1.AffectedStack{Key: "infra/kyc:staging", Path: "infra/kyc", Instance: "staging"}},
		{name: "the stack file", cfg: full, stack: v1.AffectedStack{Key: "infra/vpc", Path: "infra/vpc"}, want: true},
		{name: "the stack file without a path", cfg: full, stack: v1.AffectedStack{Key: "infra/vpc"}, want: true},
		{name: "an instance the default branch does not know keeps its directory's file", cfg: full, stack: v1.AffectedStack{Key: "infra/vpc:new", Path: "infra/vpc", Instance: "new"}, want: true},
		{name: "the root", cfg: defaulted(v1.RepoConfig{PlanOutput: v1.PlanOutputSummary}), stack: v1.AffectedStack{Key: "infra/unknown", Path: "infra/unknown"}, want: true},
		{name: "the default-branch stack file is more specific than the root", cfg: defaulted(v1.RepoConfig{PlanOutput: v1.PlanOutputSummary}), stack: v1.AffectedStack{Key: "infra/net", Path: "infra/net"}},
		{name: "nothing says summary", cfg: full, stack: v1.AffectedStack{Key: "infra/net", Path: "infra/net"}},
		{name: "a rendering error fails closed", cfg: full, stack: v1.AffectedStack{Key: "infra/app", Path: "infra/app"}, want: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := summaryOnDefaultBranch(tt.cfg, defaults, tt.stack)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
