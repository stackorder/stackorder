package cli

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
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

var jobEnvNames = []string{"STACKORDER_SERVER_URL", "STACKORDER_RUN_ID", "STACKORDER_REPOSITORY"}

func jobEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range jobEnvNames {
		if v, ok := env[k]; ok {
			out[k] = v
		}
	}
	return out
}

func (h *harness) toolCallsAfterVersion() []tfCall {
	h.t.Helper()
	var out []tfCall
	for _, c := range h.tfLog() {
		if c.args[0] != "version" {
			out = append(out, c)
		}
	}
	return out
}

func TestToolEnvironmentCarriesTheServer(t *testing.T) {
	tests := []struct {
		name    string
		command string
		setup   func(t *testing.T) (*harness, *fakeServer)
		calls   []string
	}{
		{
			name: "plan", command: "plan", calls: []string{"init", "plan", "show", "show"},
			setup: func(t *testing.T) (*harness, *fakeServer) {
				h, fs := newHarness(t), newFakeServer(t)
				h.ci(fs, "pull_request", prPayload())
				return h, fs
			},
		},
		{
			name: "apply", command: "apply", calls: []string{"init", "show", "apply"},
			setup: func(t *testing.T) (*harness, *fakeServer) {
				h, fs := newApplyHarness(t)
				h.savedPlan(h.sha)
				return h, fs
			},
		},
		{
			name: "drift", command: "drift", calls: []string{"init", "plan", "show", "show"},
			setup: func(t *testing.T) (*harness, *fakeServer) {
				h, fs := newHarness(t), newFakeServer(t)
				h.ci(fs, "workflow_dispatch", dispatchPayload("run-1", headSHA))
				return h, fs
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := tt.setup(t)
			t.Setenv(EnvServerURL, "https://inherited.acme.example")
			t.Setenv(EnvRunID, "run-inherited")
			r := h.run("--server", fs.url(), tt.command, "--stack", "stacks/app", "--run-id", "run-1")
			require.Contains(t, []int{0, ExitChanges}, r.code, r.stderr)
			assert.Equal(t, "run-1", fs.lastResult().RunID)

			want := map[string]string{
				"STACKORDER_SERVER_URL": fs.url(),
				"STACKORDER_RUN_ID":     "run-1",
				"STACKORDER_REPOSITORY": "acme/infra",
			}
			calls := h.toolCallsAfterVersion()
			commands := make([]string, 0, len(calls))
			for _, c := range calls {
				commands = append(commands, c.args[0])
				if diff := cmp.Diff(want, jobEnv(c.env)); diff != "" {
					t.Errorf("%s %s environment (-want +got):\n%s", tt.command, c.args[0], diff)
				}
			}
			assert.Equal(t, tt.calls, commands)
		})
	}
}

func TestToolEnvironmentInLocalMode(t *testing.T) {
	empty := map[string]string{"STACKORDER_SERVER_URL": "", "STACKORDER_RUN_ID": "", "STACKORDER_REPOSITORY": ""}
	tests := []struct {
		name    string
		command string
		flags   []string
		setup   func(t *testing.T, h *harness)
		want    map[string]string
	}{
		{name: "plan without a server", command: "plan", want: empty},
		{name: "drift without a server", command: "drift", want: empty},
		{
			name: "an empty --server hides the server of the environment", command: "plan", flags: []string{"--server", ""},
			setup: func(t *testing.T, _ *harness) { t.Setenv(EnvServerURL, "https://stackorder.acme.example") },
			want:  empty,
		},
		{
			name: "repository from the origin remote, run id from the environment", command: "plan",
			setup: func(t *testing.T, h *harness) {
				initGit(t, h.root)
				gitRun(t, h.root, "remote", "add", "origin", "git@github.com:acme/infra.git")
				t.Setenv(EnvRunID, "run-7")
			},
			want: map[string]string{"STACKORDER_SERVER_URL": "", "STACKORDER_RUN_ID": "run-7", "STACKORDER_REPOSITORY": "acme/infra"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if tt.setup != nil {
				tt.setup(t, h)
			}
			r := h.run(append(tt.flags, tt.command, "--stack", "stacks/app")...)
			require.Contains(t, []int{0, ExitChanges}, r.code, r.stderr)
			calls := h.toolCallsAfterVersion()
			require.NotEmpty(t, calls)
			for _, c := range calls {
				if diff := cmp.Diff(tt.want, jobEnv(c.env)); diff != "" {
					t.Errorf("%s %s environment (-want +got):\n%s", tt.command, c.args[0], diff)
				}
			}
		})
	}
}
