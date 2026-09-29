package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const instanceRootConfig = `version: 1
stacks:
  instances:
    from_var_files: "workspaces/*.tfvars.json"
var_files: ["../common.tfvars"]
env:
  TF_VAR_environment: "{{ .Instance }}"
  TF_VAR_role: { plan: reader, apply: deployer }
  TF_VAR_only_apply: { apply: from-config }
  TF_VAR_scan: { plan: planning, drift: drifting }
`

const instanceStackConfig = `var_files: ["stack.tfvars"]
instances:
  prod:
    var_files: ["prod-extra.tfvars"]
  dev: {}
`

var prodVarFiles = []string{
	"-var-file=../common.tfvars",
	"-var-file=stack.tfvars",
	"-var-file=workspaces/prod.tfvars.json",
	"-var-file=prod-extra.tfvars",
}

func writeInstanceStack(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "stackorder.yaml"), instanceRootConfig)
	writeFile(t, filepath.Join(root, "stacks", "app", ".stackorder.yaml"), instanceStackConfig)
	writeFile(t, filepath.Join(root, "stacks", "common.tfvars"), "")
	for _, f := range []string{"stack.tfvars", "prod-extra.tfvars", "workspaces/prod.tfvars.json", "workspaces/dev.tfvars.json"} {
		writeFile(t, filepath.Join(root, "stacks", "app", filepath.FromSlash(f)), "{}")
	}
}

func instanceApplyHarness(t *testing.T, saved bool) (*harness, *fakeServer) {
	t.Helper()
	h := newHarness(t)
	writeInstanceStack(t, h.root)
	h.sha = initGit(t, h.root)
	fs := newFakeServer(t)
	h.ci(fs, "workflow_dispatch", dispatchPayload("run-1", h.sha))
	row := plannedRow()
	row.Key, row.Instance, row.Lock.StackKey = "stacks/app:prod", "prod", "stacks/app:prod"
	fs.setRun(applyRun(h.sha, v1.RunApplying, row))
	if saved {
		writeFile(t, filepath.Join(h.root, ".stackorder", "plans", v1.PlanArtifactName("stacks/app:prod", h.sha)+".tfplan"), "saved plan")
	}
	return h, fs
}

func varFileArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-var-file=") {
			out = append(out, a)
		}
	}
	return out
}

func TestInitBackendConfig(t *testing.T) {
	tests := []struct {
		name        string
		rootConfig  string
		stackConfig func(abs string) string
		key         string
		envConfig   string
		want        func(root, abs string) []string
	}{
		{
			name:      "environment only",
			key:       "stacks/app",
			envConfig: "bucket=state, key=app.tfstate",
			want: func(string, string) []string {
				return []string{"init", "-input=false", "-no-color", "-backend-config=bucket=state", "-backend-config=key=app.tfstate"}
			},
		},
		{
			name:       "configured entries, files joined to the root, then the environment",
			rootConfig: "version: 1\nbackend_config:\n  - infra/state.s3.tfbackend\n  - 'key={{ .Path }}/{{ .Instance }}.tfstate'\n",
			stackConfig: func(string) string {
				return "backend_config: [region=eu-west-1]\ninstances:\n  prod:\n    backend_config: [shared/prod.tfbackend]\n"
			},
			key:       "stacks/app:prod",
			envConfig: "dynamodb_table=locks",
			want: func(root, _ string) []string {
				return []string{
					"init", "-input=false", "-no-color", "-reconfigure",
					"-backend-config=" + filepath.Join(root, "infra", "state.s3.tfbackend"),
					"-backend-config=key=stacks/app/prod.tfstate",
					"-backend-config=region=eu-west-1",
					"-backend-config=" + filepath.Join(root, "shared", "prod.tfbackend"),
					"-backend-config=dynamodb_table=locks",
				}
			},
		},
		{
			name: "a template that renders an absolute file is kept as rendered",
			stackConfig: func(abs string) string {
				return "backend_config: ['{{ `" + abs + "` }}']\n"
			},
			key: "stacks/app",
			want: func(_, abs string) []string {
				return []string{"init", "-input=false", "-no-color", "-reconfigure", "-backend-config=" + abs}
			},
		},
		{
			name:        "configured entries without the environment",
			stackConfig: func(string) string { return "backend_config: ['key={{ .Name }}.tfstate']\n" },
			key:         "stacks/app",
			want: func(string, string) []string {
				return []string{"init", "-input=false", "-no-color", "-reconfigure", "-backend-config=key=app.tfstate"}
			},
		},
		{
			name: "nothing configured",
			key:  "stacks/app",
			want: func(string, string) []string { return []string{"init", "-input=false", "-no-color"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if tt.rootConfig != "" {
				writeFile(t, filepath.Join(h.root, "stackorder.yaml"), tt.rootConfig)
			}
			abs := filepath.Join(t.TempDir(), "outside.s3.tfbackend")
			if tt.stackConfig != nil {
				writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), tt.stackConfig(abs))
			}
			if tt.envConfig != "" {
				t.Setenv(EnvBackendConfig, tt.envConfig)
			}
			r := h.run("plan", "--stack", tt.key)
			require.Equal(t, 0, r.code, r.stderr)
			root, err := filepath.Abs(h.root)
			require.NoError(t, err)
			if diff := cmp.Diff(tt.want(root, abs), h.tfCall("init").args); diff != "" {
				t.Fatalf("init argv mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBackendIsReadFromTheToolDataDir(t *testing.T) {
	const (
		stale   = `{"version":3,"backend":{"type":"s3","config":{"bucket":"stale","key":"dev.tfstate"}}}`
		current = `{"version":3,"backend":{"type":"s3","config":{"bucket":"state","key":"prod.tfstate"}}}`
	)
	tests := []struct {
		name       string
		env        string
		processDir string
		wantDir    string
	}{
		{name: "configured per instance", env: "env:\n  TF_DATA_DIR: \".terraform-{{ .Instance }}\"\n", wantDir: ".terraform-prod"},
		{name: "configured over the process value", env: "env:\n  TF_DATA_DIR: \".terraform-{{ .Instance }}\"\n", processDir: "process-data", wantDir: ".terraform-prod"},
		{name: "process value", processDir: "process-data", wantDir: "process-data"},
		{name: "configured for another mode only", env: "env:\n  TF_DATA_DIR: { apply: .terraform-apply }\n", processDir: "process-data", wantDir: "process-data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "version: 1\n"+tt.env)
			writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), "instances: [prod, dev]\n")
			writeFile(t, filepath.Join(h.root, "stacks", "app", ".terraform", "terraform.tfstate"), stale)
			if tt.processDir != "" {
				t.Setenv("TF_DATA_DIR", tt.processDir)
			}
			h.tf.Backend = json.RawMessage(current)
			r := h.run("plan", "--stack", "stacks/app:prod", "--run-id", "run-1")
			require.Equal(t, 0, r.code, r.stderr)
			assert.FileExists(t, filepath.Join(h.root, "stacks", "app", tt.wantDir, "terraform.tfstate"))
			assert.Equal(t, &v1.Backend{Type: "s3", Bucket: "state", Key: "prod.tfstate"}, fs.lastResult().Result.Backend)
		})
	}
}

func TestSharedCheckoutResetsTheWorkspace(t *testing.T) {
	const stackConfig = "instances:\n  live: { workspace: live }\n  other: { workspace: other }\n  plain: {}\n"
	tests := []struct {
		name          string
		rootConfig    string
		second        string
		wantWorkspace string
	}{
		{name: "an instance without a workspace runs in default", rootConfig: "backend_config: ['key={{ .Instance }}.tfstate']\n", second: "plain"},
		{name: "an instance with a workspace selects its own", rootConfig: "backend_config: ['key={{ .Instance }}.tfstate']\n", second: "other", wantWorkspace: "other"},
		{name: "without backend config the selection is left alone", second: "plain", wantWorkspace: "live"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "version: 1\n"+tt.rootConfig)
			writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), stackConfig)
			envFile := filepath.Join(h.root, "stacks", "app", ".terraform", "environment")
			r := h.run("plan", "--stack", "stacks/app:live")
			require.Equal(t, 0, r.code, r.stderr)
			data, err := os.ReadFile(envFile)
			require.NoError(t, err)
			require.Equal(t, "live", string(data))
			r = h.run("plan", "--stack", "stacks/app:"+tt.second)
			require.Equal(t, 0, r.code, r.stderr)
			if tt.wantWorkspace == "" {
				assert.NoFileExists(t, envFile)
				return
			}
			data, err = os.ReadFile(envFile)
			require.NoError(t, err)
			assert.Equal(t, tt.wantWorkspace, string(data))
		})
	}
}

func TestVarFiles(t *testing.T) {
	tests := []struct {
		name    string
		command string
		saved   bool
		want    []string
	}{
		{name: "plan", command: "plan", want: prodVarFiles},
		{name: "drift", command: "drift", want: prodVarFiles},
		{name: "apply re-plan", command: "apply", want: prodVarFiles},
		{name: "apply of a saved plan", command: "apply", saved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h *harness
			if tt.command == "apply" {
				h, _ = instanceApplyHarness(t, tt.saved)
			} else {
				h = newHarness(t)
				writeInstanceStack(t, h.root)
			}
			r := h.run(tt.command, "--stack", "stacks/app:prod")
			wantCode := 0
			if tt.command == "drift" {
				wantCode = ExitChanges
			}
			require.Equal(t, wantCode, r.code, r.stderr)
			var plans [][]string
			for _, c := range h.tfCalls() {
				switch c[0] {
				case "plan":
					plans = append(plans, c)
				case "apply", "init", "show":
					assert.Empty(t, varFileArgs(c), c)
				}
			}
			if tt.saved {
				assert.Empty(t, plans)
				assert.Contains(t, h.tfCommands(), "apply")
				return
			}
			require.Len(t, plans, 1)
			assert.Equal(t, tt.want, varFileArgs(plans[0]))
		})
	}
}

func TestMatchedVarFileOfEachInstance(t *testing.T) {
	tests := []struct {
		name        string
		stackConfig string
		key         string
		want        []string
	}{
		{name: "declared instance", stackConfig: instanceStackConfig, key: "stacks/app:dev", want: []string{"-var-file=../common.tfvars", "-var-file=stack.tfvars", "-var-file=workspaces/dev.tfvars.json"}},
		{name: "derived instance", key: "stacks/app:dev", want: []string{"-var-file=../common.tfvars", "-var-file=workspaces/dev.tfvars.json"}},
		{name: "declared instance with its own files last", stackConfig: instanceStackConfig, key: "stacks/app:prod", want: prodVarFiles},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			writeInstanceStack(t, h.root)
			stackFile := filepath.Join(h.root, "stacks", "app", ".stackorder.yaml")
			if tt.stackConfig == "" {
				require.NoError(t, os.Remove(stackFile))
			} else {
				writeFile(t, stackFile, tt.stackConfig)
			}
			r := h.run("plan", "--stack", tt.key)
			require.Equal(t, 0, r.code, r.stderr)
			assert.Equal(t, tt.want, varFileArgs(h.tfCall("plan").args))
		})
	}
}

func TestMissingVarFile(t *testing.T) {
	tests := []struct {
		name    string
		command string
		remove  string
		want    string
	}{
		{name: "plan, root file", command: "plan", remove: "stacks/common.tfvars", want: "stack stacks/app:prod: var file ../common.tfvars does not exist"},
		{name: "drift, instance file", command: "drift", remove: "stacks/app/prod-extra.tfvars", want: "stack stacks/app:prod: var file prod-extra.tfvars does not exist"},
		{name: "apply re-plan, stack file", command: "apply", remove: "stacks/app/stack.tfvars", want: "stack stacks/app:prod: var file stack.tfvars does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h *harness
			if tt.command == "apply" {
				h, _ = instanceApplyHarness(t, false)
			} else {
				h = newHarness(t)
				writeInstanceStack(t, h.root)
			}
			require.NoError(t, os.Remove(filepath.Join(h.root, filepath.FromSlash(tt.remove))))
			r := h.run(tt.command, "--stack", "stacks/app:prod")
			assert.Equal(t, ExitFailure, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.want)
			assert.NotContains(t, h.tfCommands(), "plan")
			assert.NotContains(t, h.tfCommands(), "apply")
		})
	}
}

func TestToolEnvironmentPerMode(t *testing.T) {
	stackVars := map[string]string{
		"STACKORDER_STACK":      "stacks/app:prod",
		"STACKORDER_STACK_PATH": "stacks/app",
		"STACKORDER_INSTANCE":   "prod",
		"TF_VAR_environment":    "prod",
	}
	tests := []struct {
		name    string
		command string
		saved   bool
		calls   []string
		want    map[string]string
		absent  []string
	}{
		{
			name: "plan", command: "plan", calls: []string{"init", "plan", "show"},
			want: map[string]string{"TF_VAR_role": "reader", "TF_VAR_scan": "planning", "TF_VAR_only_apply": "outer"},
		},
		{
			name: "drift falls back to plan", command: "drift", calls: []string{"init", "plan"},
			want: map[string]string{"TF_VAR_role": "reader", "TF_VAR_scan": "drifting", "TF_VAR_only_apply": "outer"},
		},
		{
			name: "apply re-plan uses apply values", command: "apply", calls: []string{"init", "plan", "apply"},
			want:   map[string]string{"TF_VAR_role": "deployer", "TF_VAR_only_apply": "from-config"},
			absent: []string{"TF_VAR_scan"},
		},
		{
			name: "apply of a saved plan", command: "apply", saved: true, calls: []string{"init", "apply"},
			want:   map[string]string{"TF_VAR_role": "deployer", "TF_VAR_only_apply": "from-config"},
			absent: []string{"TF_VAR_scan"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h *harness
			if tt.command == "apply" {
				h, _ = instanceApplyHarness(t, tt.saved)
			} else {
				h = newHarness(t)
				writeInstanceStack(t, h.root)
			}
			t.Setenv("TF_VAR_only_apply", "outer")
			r := h.run(tt.command, "--stack", "stacks/app:prod")
			require.Contains(t, []int{0, ExitChanges}, r.code, r.stderr)
			for _, command := range tt.calls {
				env := h.tfCall(command).env
				for k, v := range stackVars {
					assert.Equal(t, v, env[k], "%s: %s", command, k)
				}
				for k, v := range tt.want {
					assert.Equal(t, v, env[k], "%s: %s", command, k)
				}
				for _, k := range tt.absent {
					assert.NotContains(t, env, k, command)
				}
			}
		})
	}
}

func TestToolEnvironmentOfAStackWithoutInstances(t *testing.T) {
	h := newHarness(t)
	r := h.run("plan", "--stack", "stacks/app")
	require.Equal(t, 0, r.code, r.stderr)
	env := h.tfCall("plan").env
	assert.Equal(t, "stacks/app", env["STACKORDER_STACK"])
	assert.Equal(t, "stacks/app", env["STACKORDER_STACK_PATH"])
	assert.Contains(t, env, "STACKORDER_INSTANCE")
	assert.Empty(t, env["STACKORDER_INSTANCE"])
}

func TestConfiguredSecretsAreRedacted(t *testing.T) {
	const secret = "s3cr3t-prod-value"
	tests := []struct {
		name   string
		ci     bool
		fail   bool
		envCfg string
		masked bool
	}{
		{name: "secret name in CI", ci: true, envCfg: "TF_VAR_db_password: \"s3cr3t-{{ .Instance }}-value\"", masked: true},
		{name: "secret name per mode in CI", ci: true, envCfg: "TF_VAR_api_token: { apply: \"s3cr3t-{{ .Instance }}-value\" }", masked: true},
		{name: "secret name in a failure outside CI", fail: true, envCfg: "TF_VAR_db_password: \"s3cr3t-{{ .Instance }}-value\"", masked: true},
		{name: "plain name is left alone", ci: true, envCfg: "TF_VAR_label: \"s3cr3t-{{ .Instance }}-value\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			var fs *fakeServer
			if tt.ci {
				fs = newFakeServer(t)
				h.ci(fs, "pull_request", prPayload())
			}
			writeFile(t, filepath.Join(h.root, "stackorder.yaml"), "version: 1\nenv:\n  "+tt.envCfg+"\n")
			writeFile(t, filepath.Join(h.root, "stacks", "app", ".stackorder.yaml"), "instances: [prod]\n")
			h.tf.PlanOutput = "  + note = \"" + secret + "\"\n"
			h.tf.ShowText = writeFile(t, filepath.Join(t.TempDir(), "show.txt"), "value = \""+secret+"\"\n")
			if tt.fail {
				h.tf.PlanExit = 1
				h.tf.FailOutput = "cannot use " + secret
			}
			r := h.run("plan", "--stack", "stacks/app:prod", "--run-id", "run-1")
			if tt.fail {
				require.Equal(t, ExitFailure, r.code, r.stderr)
			} else {
				require.Equal(t, 0, r.code, r.stderr)
			}
			if tt.fail {
				var line string
				for l := range strings.Lines(r.stderr) {
					if strings.HasPrefix(l, "stackorder: ") {
						line = l
					}
				}
				assert.Contains(t, line, "cannot use ***")
				assert.NotContains(t, line, secret)
				return
			}
			out := strings.ReplaceAll(r.stdout+r.stderr, "::add-mask::"+secret, "")
			if !tt.masked {
				assert.Contains(t, out, secret)
				assert.NotContains(t, r.stdout, "::add-mask::"+secret)
				return
			}
			assert.NotContains(t, out, secret)
			maskAt := strings.Index(r.stdout, "::add-mask::"+secret)
			require.GreaterOrEqual(t, maskAt, 0, r.stdout)
			assert.Less(t, maskAt, strings.Index(r.stdout, "  + note = \"***\""))
			assert.Equal(t, "value = \"***\"\n", fs.lastResult().Result.PlanText)
		})
	}
}

func TestStackFlagHelp(t *testing.T) {
	a := newTestApp(io.Discard)
	for _, cmd := range []*cobra.Command{a.planCommand(), a.applyCommand(), a.driftCommand(), a.checkCommand()} {
		t.Run(cmd.Name(), func(t *testing.T) {
			f := cmd.Flags().Lookup("stack")
			require.NotNil(t, f)
			assert.Equal(t, "stack key: path or path:instance", f.Usage)
		})
	}
}
