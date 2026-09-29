package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadStackInstances(t *testing.T) {
	tests := []struct {
		name          string
		rootConfig    string
		stackConfig   string
		files         []string
		key           string
		wantKey       string
		wantInstance  string
		wantWorkspace string
		wantErr       string
	}{
		{name: "no instances", key: "infra/app", wantKey: "infra/app"},
		{name: "ad hoc workspace from the suffix", key: "infra/app:green", wantKey: "infra/app:green", wantInstance: "green", wantWorkspace: "green"},
		{name: "ad hoc suffix must be an instance name", key: "infra/app:a+b", wantErr: `instance name "a+b" must match`},
		{
			name: "legacy workspace", stackConfig: "workspace: blue\n", key: "infra/app",
			wantKey: "infra/app:blue", wantInstance: "blue", wantWorkspace: "blue",
		},
		{
			name: "legacy workspace named by its suffix", stackConfig: "workspace: blue\n", key: "infra/app:blue",
			wantKey: "infra/app:blue", wantInstance: "blue", wantWorkspace: "blue",
		},
		{
			name: "suffix other than the legacy workspace is ad hoc", stackConfig: "workspace: blue\n", key: "infra/app:green",
			wantKey: "infra/app:green", wantInstance: "green", wantWorkspace: "green",
		},
		{
			name: "declared instance without a workspace selects none", stackConfig: "instances: [prod, dev]\n", key: "infra/app:prod",
			wantKey: "infra/app:prod", wantInstance: "prod",
		},
		{
			name: "declared instance with a workspace override", stackConfig: "instances:\n  prod: { workspace: live }\n", key: "infra/app:prod",
			wantKey: "infra/app:prod", wantInstance: "prod", wantWorkspace: "live",
		},
		{
			name: "declared instances with a workspace template", stackConfig: "workspace: \"ws-{{ .Instance }}\"\ninstances: [prod]\n", key: "infra/app:prod",
			wantKey: "infra/app:prod", wantInstance: "prod", wantWorkspace: "ws-prod",
		},
		{
			name: "bare path of a directory with instances", stackConfig: "instances: [prod, dev]\n", key: "infra/app",
			wantErr: "stack infra/app has instances; name one as infra/app:<instance> (dev, prod)",
		},
		{
			name: "unknown instance", stackConfig: "instances: [prod, dev]\n", key: "infra/app:qa",
			wantErr: `stack infra/app has no instance "qa"; its instances are dev, prod`,
		},
		{
			name:       "instances derived from var files",
			rootConfig: "version: 1\nstacks:\n  instances:\n    from_var_files: \"*.tfvars\"\n",
			files:      []string{"east.tfvars", "west.tfvars"},
			key:        "infra/app:west",
			wantKey:    "infra/app:west", wantInstance: "west",
		},
		{
			name:       "bare path of a directory with derived instances",
			rootConfig: "version: 1\nstacks:\n  instances:\n    from_var_files: \"*.tfvars\"\n",
			files:      []string{"east.tfvars", "west.tfvars"},
			key:        "infra/app",
			wantErr:    "(east, west)",
		},
		{
			name:       "invalid derived instance",
			rootConfig: "version: 1\nstacks:\n  instances:\n    from_var_files: \"*.tfvars\"\n",
			files:      []string{"default.tfvars"},
			key:        "infra/app",
			wantErr:    `instance name "default" is reserved`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "infra", "app")
			writeFile(t, filepath.Join(dir, "main.tf"), "")
			if tt.rootConfig != "" {
				writeFile(t, filepath.Join(root, "stackorder.yaml"), tt.rootConfig)
			}
			if tt.stackConfig != "" {
				writeFile(t, filepath.Join(dir, ".stackorder.yaml"), tt.stackConfig)
			}
			for _, f := range tt.files {
				writeFile(t, filepath.Join(dir, f), "")
			}
			st, err := loadStack(root, tt.key)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, st.key)
			assert.Equal(t, tt.wantInstance, st.eff.Instance)
			assert.Equal(t, tt.wantWorkspace, st.workspace)
			assert.Equal(t, tt.wantWorkspace, st.eff.Workspace)
		})
	}
}
