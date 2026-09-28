package oidc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchWorkflowRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		pattern string
		want    bool
	}{
		{name: "plan at v1", ref: "stackorder/actions/.github/workflows/plan.yml@refs/tags/v1", want: true},
		{name: "run at v1.4.2", ref: "stackorder/actions/.github/workflows/run.yml@refs/tags/v1.4.2", want: true},
		{name: "slash inside the ref", ref: "stackorder/actions/.github/workflows/run.yml@refs/tags/v1/rc1", want: true},
		{name: "v10 also matches v1*", ref: "stackorder/actions/.github/workflows/run.yml@refs/tags/v10.0.0", want: true},
		{name: "locally edited copy in the user repo", ref: "acme/infra/.github/workflows/plan.yml@refs/heads/main"},
		{name: "locally edited copy on the PR ref", ref: "acme/infra/.github/workflows/stackorder-plan.yml@refs/pull/7/merge"},
		{name: "fork of the actions repo", ref: "evil/actions/.github/workflows/plan.yml@refs/tags/v1"},
		{name: "look-alike owner", ref: "stackorder-evil/actions/.github/workflows/plan.yml@refs/tags/v1"},
		{name: "look-alike repo", ref: "stackorder/actions-fork/.github/workflows/plan.yml@refs/tags/v1"},
		{name: "workflow in a subdirectory", ref: "stackorder/actions/.github/workflows/sub/plan.yml@refs/tags/v1"},
		{name: "path traversal", ref: "stackorder/actions/.github/workflows/../../evil/plan.yml@refs/tags/v1"},
		{name: "yaml extension", ref: "stackorder/actions/.github/workflows/plan.yaml@refs/tags/v1"},
		{name: "branch named v1", ref: "stackorder/actions/.github/workflows/plan.yml@refs/heads/v1"},
		{name: "branch path containing the tag", ref: "stackorder/actions/.github/workflows/plan.yml@refs/heads/refs/tags/v1"},
		{name: "v2 tag", ref: "stackorder/actions/.github/workflows/plan.yml@refs/tags/v2.0.0"},
		{name: "commit sha", ref: "stackorder/actions/.github/workflows/plan.yml@5f1c0f3d9a3b2e7c4d6a8b0e1f2a3b4c5d6e7f80"},
		{name: "wrong case", ref: "Stackorder/Actions/.github/workflows/plan.yml@refs/tags/v1"},
		{name: "no ref", ref: "stackorder/actions/.github/workflows/plan.yml"},
		{name: "empty ref", ref: "stackorder/actions/.github/workflows/plan.yml@"},
		{name: "empty path", ref: "@refs/tags/v1"},
		{name: "empty claim", ref: ""},
		{name: "at sign in the claim path", ref: "stackorder/actions/.github/workflows/p@lan.yml@refs/tags/v1"},
		{name: "exact pattern", ref: "acme/wf/.github/workflows/a.yml@refs/heads/main", pattern: "acme/wf/.github/workflows/a.yml@refs/heads/main", want: true},
		{name: "exact pattern other ref", ref: "acme/wf/.github/workflows/a.yml@refs/heads/dev", pattern: "acme/wf/.github/workflows/a.yml@refs/heads/main"},
		{name: "any ref", ref: "acme/wf/.github/workflows/a.yml@refs/heads/feature/x", pattern: "acme/wf/.github/workflows/a.yml@*", want: true},
		{name: "character class", ref: "acme/wf/.github/workflows/b.yml@refs/tags/v1", pattern: "acme/wf/.github/workflows/[ab].yml@refs/tags/v1", want: true},
		{name: "question mark in ref crosses slash", ref: "acme/wf/w.yml@refs/tags/v1/2", pattern: "acme/wf/w.yml@refs/tags/v1?2", want: true},
		{name: "question mark in path stops at slash", ref: "acme/wf/w.yml@main", pattern: "acme?wf/w.yml@main"},
		{name: "pattern without ref", ref: "stackorder/actions/.github/workflows/plan.yml@refs/tags/v1", pattern: "stackorder/actions/.github/workflows/*.yml"},
		{name: "empty pattern", ref: "stackorder/actions/.github/workflows/plan.yml@refs/tags/v1", pattern: "@"},
		{name: "malformed path pattern", ref: "acme/wf/a.yml@main", pattern: "acme/wf/[a.yml@main"},
		{name: "malformed ref pattern", ref: "acme/wf/a.yml@main", pattern: "acme/wf/a.yml@[main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := tt.pattern
			if pattern == "" {
				pattern = DefaultWorkflowRefPattern
			}
			assert.Equal(t, tt.want, MatchWorkflowRef(tt.ref, pattern))
		})
	}
	assert.False(t, MatchWorkflowRef("stackorder/actions/.github/workflows/plan.yml@refs/tags/v1", ""), "an empty pattern never matches")
}

func TestValidateWorkflowRefPattern(t *testing.T) {
	tests := []struct {
		pattern string
		wantErr string
	}{
		{pattern: DefaultWorkflowRefPattern},
		{pattern: "acme/wf/.github/workflows/a.yml@refs/heads/main"},
		{pattern: "acme/*/.github/workflows/*.yml@*"},
		{pattern: "", wantErr: "not of the form"},
		{pattern: "stackorder/actions/.github/workflows/*.yml", wantErr: "not of the form"},
		{pattern: "@refs/tags/v1*", wantErr: "not of the form"},
		{pattern: "stackorder/actions/.github/workflows/*.yml@", wantErr: "not of the form"},
		{pattern: "acme/[wf/a.yml@main", wantErr: "path: syntax error in pattern"},
		{pattern: "acme/wf/a.yml@refs/tags/[v1", wantErr: "ref: syntax error in pattern"},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			err := ValidateWorkflowRefPattern(tt.pattern)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
