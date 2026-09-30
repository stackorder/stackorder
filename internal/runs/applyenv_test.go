package runs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestApplyEnvironment(t *testing.T) {
	cfg := defaulted(v1.RepoConfig{Environments: map[string]string{"infra/": "infra-{{ .Instance }}", "stacks/prod/": "production"}})
	tests := []struct {
		name     string
		defaults map[string]*v1.StackConfig
		src      store.RunStack
		want     string
		wantErr  string
	}{
		{
			name: "rendered environments match",
			src:  store.RunStack{Key: "infra/app:prod", Path: "infra/app", Environment: "from-the-pr"},
			want: "infra-prod",
		},
		{
			name:     "default-branch instance override",
			defaults: map[string]*v1.StackConfig{"infra/app": {Instances: v1.Instances{"prod": {Environment: "app-{{ .Instance }}"}}}},
			src:      store.RunStack{Key: "infra/app:prod", Path: "infra/app"},
			want:     "app-prod",
		},
		{
			name:     "the instance name protects an unmapped instance against the pull request",
			defaults: map[string]*v1.StackConfig{"stacks/dev/app": {Instances: v1.Instances{"blue": {}}}},
			src:      store.RunStack{Key: "stacks/dev/app:blue", Path: "stacks/dev/app", Environment: "unprotected"},
			want:     "blue",
		},
		{
			name: "without a default-branch stack file the pull request's recorded environment is ignored",
			src:  store.RunStack{Key: "stacks/dev/app", Path: "stacks/dev/app", Environment: "recorded"},
			want: v1.DefaultEnvironment,
		},
		{
			name: "a directory missing from the default branch takes the default-branch environments match",
			src:  store.RunStack{Key: "stacks/prod/new", Path: "stacks/prod/new", Environment: "weaker"},
			want: "production",
		},
		{
			name: "an instance without a default-branch stack file runs under its own name",
			src:  store.RunStack{Key: "stacks/dev/kyc:production", Path: "stacks/dev/kyc", Environment: "dev"},
			want: "production",
		},
		{
			name:     "a new instance takes its directory's default-branch environment",
			defaults: map[string]*v1.StackConfig{"stacks/dev/app": {Environment: "protected"}},
			src:      store.RunStack{Key: "stacks/dev/app:new", Path: "stacks/dev/app", Environment: "dev"},
			want:     "protected",
		},
		{
			name: "nothing configured runs under default",
			src:  store.RunStack{Key: "stacks/dev/app", Path: "stacks/dev/app"},
			want: v1.DefaultEnvironment,
		},
		{
			name:     "a rendering error refuses",
			defaults: map[string]*v1.StackConfig{"stacks/dev/app": {Environment: "{{ .Nope }}"}},
			src:      store.RunStack{Key: "stacks/dev/app", Path: "stacks/dev/app"},
			wantErr:  "runs: environment of stacks/dev/app:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyEnvironment(cfg, tt.defaults, tt.src)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
