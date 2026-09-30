package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func ptr(s string) *string { return &s }

func TestStateObjectKey(t *testing.T) {
	tests := []struct {
		prefix, workspace, key string
		want                   string
	}{
		{key: "vpc.tfstate", want: "vpc.tfstate"},
		{workspace: "default", key: "vpc.tfstate", want: "vpc.tfstate"},
		{workspace: "blue", key: "vpc.tfstate", want: "env:/blue/vpc.tfstate"},
		{prefix: "ws", workspace: "blue", key: "vpc.tfstate", want: "ws/blue/vpc.tfstate"},
		{prefix: "ws", key: "vpc.tfstate", want: "vpc.tfstate"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, StateObjectKey(tt.prefix, tt.workspace, tt.key))
		})
	}
}

func TestEnvironmentFor(t *testing.T) {
	envs := map[string]string{
		"stacks/prod/":            "production",
		"stacks/prod/core/":       "production-core",
		"stacks/staging":          "staging",
		"stacks/prod/:blue":       "production-blue",
		":green":                  "green-anywhere",
		"stacks/prod/core/:green": "core-green",
	}
	tests := []struct {
		path, instance, want string
	}{
		{"stacks/prod/vpc", "", "production"},
		{"stacks/prod/core/iam", "", "production-core"},
		{"./stacks/staging/vpc/", "", "staging"},
		{"stacks/dev/vpc", "", ""},
		{"stacks/production/vpc", "", ""},
		{"stacks/prod/vpc", "red", "production"},
		{"stacks/prod/core/iam", "blue", "production-blue"},
		{"stacks/prod/vpc", "green", "green-anywhere"},
		{"stacks/prod/core/iam", "green", "core-green"},
		{"stacks/dev/vpc", "green", "green-anywhere"},
		{"stacks/dev/vpc", "blue", ""},
		{"stacks/prod/vpc:blue", "blue", "production-blue"},
	}
	for _, tt := range tests {
		t.Run(tt.path+"@"+tt.instance, func(t *testing.T) {
			assert.Equal(t, tt.want, EnvironmentFor(envs, tt.path, tt.instance))
		})
	}
}

func TestEnvironmentForEmptyPrefix(t *testing.T) {
	envs := map[string]string{"/": "slash", "./": "dot-slash", "./:blue": "blue-anywhere"}
	assert.Empty(t, EnvironmentFor(envs, "stacks/a", ""), "a bare empty prefix matches nothing")
	assert.Equal(t, "blue-anywhere", EnvironmentFor(envs, "stacks/a", "blue"))
}

func TestInstanceNames(t *testing.T) {
	fromVarFiles := Default()
	fromVarFiles.Stacks.Instances.FromVarFiles = "workspaces/*.tfvars*"
	matched := []string{"workspaces/prod.tfvars.json", "workspaces/dev.tfvars", "workspaces/staging.auto.tfvars"}
	tests := []struct {
		name    string
		root    *v1.RepoConfig
		stack   *v1.StackConfig
		matched []string
		want    []string
		wantErr []string
	}{
		{
			name:    "explicit instances win over var files and workspace",
			root:    fromVarFiles,
			stack:   &v1.StackConfig{Workspace: "blue", Instances: v1.Instances{"west": {}, "east": {}}},
			matched: matched,
			want:    []string{"east", "west"},
		},
		{
			name:    "from_var_files names up to the first dot, sorted",
			root:    fromVarFiles,
			stack:   &v1.StackConfig{Workspace: "blue"},
			matched: matched,
			want:    []string{"dev", "prod", "staging"},
		},
		{
			name:    "matches are ignored without from_var_files",
			root:    Default(),
			stack:   &v1.StackConfig{},
			matched: matched,
			want:    []string{""},
		},
		{
			name:  "no matches fall back to the legacy workspace",
			root:  fromVarFiles,
			stack: &v1.StackConfig{Workspace: "blue"},
			want:  []string{"blue"},
		},
		{name: "legacy workspace", root: Default(), stack: &v1.StackConfig{Workspace: "blue"}, want: []string{"blue"}},
		{name: "workspace default is none", root: Default(), stack: &v1.StackConfig{Workspace: "default"}, want: []string{""}},
		{name: "templated workspace renders with the path", root: Default(), stack: &v1.StackConfig{Workspace: "{{ .Name }}"}, want: []string{"vpc"}},
		{name: "templated workspace that renders empty", root: Default(), stack: &v1.StackConfig{Workspace: "{{ .Instance }}"}, want: []string{""}},
		{
			name:    "templated workspace that renders an invalid name",
			root:    Default(),
			stack:   &v1.StackConfig{Workspace: "{{ .Instance }}-x"},
			wantErr: []string{`stacks/vpc/.stackorder.yaml: workspace: "-x" names the stack's only instance`},
		},
		{
			name:    "templated workspace that fails to render",
			root:    Default(),
			stack:   &v1.StackConfig{Workspace: "{{ .Nope }}"},
			wantErr: []string{"stacks/vpc/.stackorder.yaml: workspace:", "Nope"},
		},
		{name: "nothing", root: Default(), stack: &v1.StackConfig{}, want: []string{""}},
		{name: "nil stack", root: Default(), want: []string{""}},
		{
			name:    "invalid and duplicate derived names",
			root:    fromVarFiles,
			matched: []string{"workspaces/default.tfvars", "workspaces/_x.tfvars", "workspaces/a.tfvars", "workspaces/a.tfvars.json"},
			wantErr: []string{
				`stacks/vpc/workspaces/default.tfvars: instance name "default" is reserved`,
				`stacks/vpc/workspaces/_x.tfvars: instance name "_x" must match`,
				`stacks/vpc/workspaces/a.tfvars and stacks/vpc/workspaces/a.tfvars.json both name instance "a"`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InstanceNames(tt.root, "stacks/vpc", tt.stack, tt.matched)
			if tt.wantErr != nil {
				require.Error(t, err)
				for _, want := range tt.wantErr {
					assert.ErrorContains(t, err, want)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVarFileWarnings(t *testing.T) {
	root := Default()
	root.Stacks.Instances.FromVarFiles = "*.tfvars"
	explicit := &v1.StackConfig{Instances: v1.Instances{"east": {}, "west": {}}}
	matched := []string{"north.tfvars", "east.tfvars"}
	tests := []struct {
		name    string
		root    *v1.RepoConfig
		stack   *v1.StackConfig
		matched []string
		want    []string
	}{
		{
			name:  "a match the explicit list does not declare",
			root:  root,
			stack: explicit,
			want:  []string{`infra/app/north.tfvars: names instance "north", which infra/app/.stackorder.yaml does not declare; the file is not used`},
		},
		{name: "derived instances", root: root, stack: &v1.StackConfig{}},
		{name: "from_var_files unset", root: Default(), stack: explicit},
		{
			name:    "files Terraform auto-loads in the stack directory",
			root:    root,
			stack:   &v1.StackConfig{},
			matched: []string{"terraform.tfvars", "terraform.tfvars.json", "prod.auto.tfvars", "dev.auto.tfvars.json", "vars/qa.auto.tfvars", "staging.tfvars"},
			want: []string{
				"infra/app/dev.auto.tfvars.json: Terraform auto-loads it for every instance of the stack; stacks.instances.from_var_files should not match it",
				"infra/app/prod.auto.tfvars: Terraform auto-loads it for every instance of the stack; stacks.instances.from_var_files should not match it",
				"infra/app/terraform.tfvars: Terraform auto-loads it for every instance of the stack; stacks.instances.from_var_files should not match it",
				"infra/app/terraform.tfvars.json: Terraform auto-loads it for every instance of the stack; stacks.instances.from_var_files should not match it",
			},
		},
		{
			name:    "an auto-loaded match the explicit list does not declare",
			root:    root,
			stack:   explicit,
			matched: []string{"east.auto.tfvars", "north.auto.tfvars"},
			want: []string{
				"infra/app/east.auto.tfvars: Terraform auto-loads it for every instance of the stack; stacks.instances.from_var_files should not match it",
				"infra/app/north.auto.tfvars: Terraform auto-loads it for every instance of the stack; stacks.instances.from_var_files should not match it",
				`infra/app/north.auto.tfvars: names instance "north", which infra/app/.stackorder.yaml does not declare; the file is not used`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := matched
			if tt.matched != nil {
				m = tt.matched
			}
			assert.Equal(t, tt.want, VarFileWarnings(tt.root, "infra/app", tt.stack, m))
		})
	}
}

func TestMatchVarFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"workspaces/prod.tfvars.json", "workspaces/dev.tfvars.json", "workspaces/notes.md", "main.tf"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "workspaces", "dir.tfvars.json"), 0o755))

	root := Default()
	got, err := MatchVarFiles(root, dir)
	require.NoError(t, err)
	assert.Nil(t, got, "unset glob")

	root.Stacks.Instances.FromVarFiles = "./workspaces/*.tfvars.json"
	got, err = MatchVarFiles(root, dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"workspaces/dev.tfvars.json", "workspaces/prod.tfvars.json"}, got)
}

func TestInstanceOf(t *testing.T) {
	tests := []struct{ instance, workspace, want string }{
		{"prod", "blue", "prod"},
		{"prod", "", "prod"},
		{"", "blue", "blue"},
		{"", "default", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, InstanceOf(tt.instance, tt.workspace), "InstanceOf(%q, %q)", tt.instance, tt.workspace)
	}
}

func TestVarFileInstance(t *testing.T) {
	tests := map[string]string{
		"workspaces/prod.tfvars.json": "prod",
		"eu-west-1.auto.tfvars":       "eu-west-1",
		"workspaces\\dev.tfvars":      "dev",
		"plain":                       "plain",
	}
	for in, want := range tests {
		assert.Equal(t, want, VarFileInstance(in), in)
	}
}

func instancesRoot() *v1.RepoConfig {
	root := Default()
	root.Stacks.Instances.FromVarFiles = "workspaces/*.tfvars.json"
	root.Tool = v1.ToolTofu
	root.ToolVersion = "1.9.0"
	root.Apply.AllowedTeams = []string{"platform-eng"}
	root.Environments = map[string]string{
		"stacks/prod/": "production",
		"infra/":       "infra-{{ .Instance }}",
		":canary":      "canary",
	}
	root.BackendConfig = []string{"infra/state.s3.tfbackend", `key={{ trimPrefix "infra/" .Path }}/{{ .Instance }}.tfstate`}
	root.VarFiles = []string{"common.tfvars"}
	root.Env = v1.EnvConfig{
		"TF_VAR_environment": v1.EnvString("{{ .Instance }}"),
		"TF_VAR_role":        {Modes: &v1.EnvModes{Plan: ptr("reader"), Apply: ptr("deployer")}},
		"A":                  v1.EnvString("root"),
	}
	return root
}

func instancesStack() *v1.StackConfig {
	return &v1.StackConfig{
		Workspace:      "{{ .Instance }}",
		PlanOutput:     v1.PlanOutputSummary,
		Apply:          &v1.StackApplyConfig{AllowedTeams: []string{"stack-team"}},
		DependsOn:      []string{"infra/vpc:{{ .Instance }}"},
		IgnoreInferred: []string{"infra/legacy"},
		BackendConfig:  []string{"region=eu-west-1"},
		VarFiles:       []string{"stack.tfvars"},
		Env: v1.EnvConfig{
			"A": v1.EnvString("stack"),
			"B": {Modes: &v1.EnvModes{Plan: ptr("p-{{ .Name }}")}},
		},
		Instances: v1.Instances{
			"prod": {
				Environment:    "prod-{{ .Name }}",
				BackendConfig:  []string{"bucket=prod"},
				VarFiles:       []string{"{{ .Instance }}-extra.tfvars"},
				Env:            v1.EnvConfig{"A": {Modes: &v1.EnvModes{Apply: ptr("inst")}}},
				PlanOutput:     v1.PlanOutputFull,
				Apply:          &v1.StackApplyConfig{AllowedTeams: []string{"prod-team"}},
				DependsOn:      []string{"infra/dns"},
				IgnoreInferred: []string{"infra/old"},
			},
			"dev":    {Apply: &v1.StackApplyConfig{}},
			"canary": {Workspace: "default"},
		},
	}
}

func TestResolve(t *testing.T) {
	root := Default()
	root.Tool = v1.ToolTofu
	root.Environments = map[string]string{"stacks/prod/": "production"}
	root.Apply.AllowedTeams = []string{"platform-eng"}

	tests := []struct {
		name     string
		root     *v1.RepoConfig
		path     string
		stack    *v1.StackConfig
		instance string
		matched  []string
		want     Effective
	}{
		{
			name: "root only",
			root: root, path: "./stacks/prod/vpc/",
			want: Effective{
				Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Tool: v1.ToolTofu, Environment: "production", EnvironmentConfigured: true,
				PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "unmapped stack runs under default",
			root: root, path: "stacks/dev/vpc", stack: &v1.StackConfig{},
			want: Effective{
				Key: "stacks/dev/vpc", Path: "stacks/dev/vpc", Tool: v1.ToolTofu, Environment: v1.DefaultEnvironment,
				PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "stack overrides the root",
			root: root, path: "stacks/prod/secrets", instance: "blue",
			stack: &v1.StackConfig{
				Workspace: "blue", Tool: v1.ToolTerraform, ToolVersion: "1.14.0", Environment: "vault",
				PlanOutput: v1.PlanOutputSummary, Apply: &v1.StackApplyConfig{AllowedTeams: []string{"platform-prod"}},
				DependsOn: []string{"stacks/prod/vpc"}, IgnoreInferred: []string{"stacks/legacy"},
			},
			want: Effective{
				Key: "stacks/prod/secrets:blue", Path: "stacks/prod/secrets", Instance: "blue", Workspace: "blue",
				Tool: v1.ToolTerraform, ToolVersion: "1.14.0", Environment: "vault", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputSummary,
				AllowedTeams: []string{"platform-prod"}, DependsOn: []string{"stacks/prod/vpc"}, IgnoreInferred: []string{"stacks/legacy"},
			},
		},
		{
			name: "legacy workspace is protected by its own environment",
			root: root, path: "stacks/dev/app", instance: "blue", stack: &v1.StackConfig{Workspace: "blue"},
			want: Effective{
				Key: "stacks/dev/app:blue", Path: "stacks/dev/app", Instance: "blue", Workspace: "blue", Tool: v1.ToolTofu,
				Environment: "blue", PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "legacy workspace keeps default when asked",
			root: root, path: "stacks/dev/app", instance: "blue", stack: &v1.StackConfig{Workspace: "blue", Environment: "default"},
			want: Effective{
				Key: "stacks/dev/app:blue", Path: "stacks/dev/app", Instance: "blue", Workspace: "blue", Tool: v1.ToolTofu,
				Environment: v1.DefaultEnvironment, EnvironmentConfigured: true, PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "prefix mapping beats the instance name",
			root: root, path: "stacks/prod/app", instance: "blue", stack: &v1.StackConfig{Workspace: "blue"},
			want: Effective{
				Key: "stacks/prod/app:blue", Path: "stacks/prod/app", Instance: "blue", Workspace: "blue", Tool: v1.ToolTofu,
				Environment: "production", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "workspace default is none",
			root: root, path: "stacks/x", stack: &v1.StackConfig{Workspace: "default"},
			want: Effective{
				Key: "stacks/x", Path: "stacks/x", Tool: v1.ToolTofu, Environment: v1.DefaultEnvironment,
				PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "instance overrides and merged lists",
			root: instancesRoot(), path: "infra/app", stack: instancesStack(), instance: "prod",
			matched: []string{"workspaces/dev.tfvars.json", "workspaces/prod.tfvars.json"},
			want: Effective{
				Key: "infra/app:prod", Path: "infra/app", Instance: "prod", Workspace: "prod",
				Tool: v1.ToolTofu, ToolVersion: "1.9.0", Environment: "prod-app", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputFull,
				AllowedTeams:   []string{"prod-team"},
				DependsOn:      []string{"infra/vpc:prod", "infra/dns"},
				IgnoreInferred: []string{"infra/legacy", "infra/old"},
				BackendConfig:  []string{"infra/state.s3.tfbackend", "key=app/prod.tfstate", "region=eu-west-1", "bucket=prod"},
				VarFiles:       []string{"common.tfvars", "stack.tfvars", "workspaces/prod.tfvars.json", "prod-extra.tfvars"},
				Env: map[v1.RunMode]map[string]string{
					v1.ModePlan:  {"TF_VAR_environment": "prod", "TF_VAR_role": "reader", "B": "p-app"},
					v1.ModeApply: {"TF_VAR_environment": "prod", "TF_VAR_role": "deployer", "A": "inst"},
					v1.ModeDrift: {"TF_VAR_environment": "prod", "TF_VAR_role": "reader", "B": "p-app"},
				},
			},
		},
		{
			name: "instance without overrides takes the stack and the rendered environments match",
			root: instancesRoot(), path: "infra/app", stack: instancesStack(), instance: "dev",
			want: Effective{
				Key: "infra/app:dev", Path: "infra/app", Instance: "dev", Workspace: "dev",
				Tool: v1.ToolTofu, ToolVersion: "1.9.0", Environment: "infra-dev", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputSummary,
				AllowedTeams:   []string{"stack-team"},
				DependsOn:      []string{"infra/vpc:dev"},
				IgnoreInferred: []string{"infra/legacy"},
				BackendConfig:  []string{"infra/state.s3.tfbackend", "key=app/dev.tfstate", "region=eu-west-1"},
				VarFiles:       []string{"common.tfvars", "stack.tfvars"},
				Env: map[v1.RunMode]map[string]string{
					v1.ModePlan:  {"TF_VAR_environment": "dev", "TF_VAR_role": "reader", "A": "stack", "B": "p-app"},
					v1.ModeApply: {"TF_VAR_environment": "dev", "TF_VAR_role": "deployer", "A": "stack"},
					v1.ModeDrift: {"TF_VAR_environment": "dev", "TF_VAR_role": "reader", "A": "stack", "B": "p-app"},
				},
			},
		},
		{
			name: "an instance key beats a prefix and an instance workspace of default is none",
			root: instancesRoot(), path: "infra/app", stack: instancesStack(), instance: "canary",
			want: Effective{
				Key: "infra/app:canary", Path: "infra/app", Instance: "canary",
				Tool: v1.ToolTofu, ToolVersion: "1.9.0", Environment: "canary", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputSummary,
				AllowedTeams:   []string{"stack-team"},
				DependsOn:      []string{"infra/vpc:canary"},
				IgnoreInferred: []string{"infra/legacy"},
				BackendConfig:  []string{"infra/state.s3.tfbackend", "key=app/canary.tfstate", "region=eu-west-1"},
				VarFiles:       []string{"common.tfvars", "stack.tfvars"},
				Env: map[v1.RunMode]map[string]string{
					v1.ModePlan:  {"TF_VAR_environment": "canary", "TF_VAR_role": "reader", "A": "stack", "B": "p-app"},
					v1.ModeApply: {"TF_VAR_environment": "canary", "TF_VAR_role": "deployer", "A": "stack"},
					v1.ModeDrift: {"TF_VAR_environment": "canary", "TF_VAR_role": "reader", "A": "stack", "B": "p-app"},
				},
			},
		},
		{
			name: "an undeclared instance falls back to its own name",
			root: root, path: "stacks/dev/app", instance: "eu-west-1",
			want: Effective{
				Key: "stacks/dev/app:eu-west-1", Path: "stacks/dev/app", Instance: "eu-west-1", Tool: v1.ToolTofu,
				Environment: "eu-west-1", PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "a drift value beats the plan value",
			root: root, path: "stacks/dev/app",
			stack: &v1.StackConfig{Env: v1.EnvConfig{"X": {Modes: &v1.EnvModes{Plan: ptr("p"), Drift: ptr("d")}}}},
			want: Effective{
				Key: "stacks/dev/app", Path: "stacks/dev/app", Tool: v1.ToolTofu, Environment: v1.DefaultEnvironment,
				PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
				Env: map[v1.RunMode]map[string]string{
					v1.ModePlan: {"X": "p"}, v1.ModeApply: {}, v1.ModeDrift: {"X": "d"},
				},
			},
		},
		{
			name: "an instance override beats the stack environment, which beats the environments match",
			root: root, path: "stacks/prod/app", instance: "blue",
			stack: &v1.StackConfig{Environment: "stack-env", Instances: v1.Instances{"blue": {Environment: "blue-env"}, "green": {}}},
			want: Effective{
				Key: "stacks/prod/app:blue", Path: "stacks/prod/app", Instance: "blue", Tool: v1.ToolTofu,
				Environment: "blue-env", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "an instance without an override takes the stack environment",
			root: root, path: "stacks/prod/app", instance: "green",
			stack: &v1.StackConfig{Environment: "stack-env", Instances: v1.Instances{"blue": {Environment: "blue-env"}, "green": {}}},
			want: Effective{
				Key: "stacks/prod/app:green", Path: "stacks/prod/app", Instance: "green", Tool: v1.ToolTofu,
				Environment: "stack-env", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "an environment that renders empty falls through in order",
			root: root, path: "stacks/prod/app", instance: "dev",
			stack: &v1.StackConfig{
				Environment: `{{ if eq .Instance "prod" }}stack-prod{{ end }}`,
				Instances:   v1.Instances{"dev": {Environment: `{{ if eq .Instance "qa" }}qa{{ end }}`}},
			},
			want: Effective{
				Key: "stacks/prod/app:dev", Path: "stacks/prod/app", Instance: "dev", Tool: v1.ToolTofu,
				Environment: "production", EnvironmentConfigured: true, PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "templated ignore_inferred is rendered",
			root: root, path: "stacks/dev/app", instance: "x",
			stack: &v1.StackConfig{IgnoreInferred: []string{"infra/{{ .Instance }}"}, Instances: v1.Instances{"x": {IgnoreInferred: []string{"{{ .Key }}-old"}}}},
			want: Effective{
				Key: "stacks/dev/app:x", Path: "stacks/dev/app", Instance: "x", Tool: v1.ToolTofu, Environment: "x",
				PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"}, IgnoreInferred: []string{"infra/x", "stacks/dev/app:x-old"},
			},
		},
		{
			name: "instance default is none",
			root: root, path: "stacks/dev/app", instance: "default",
			want: Effective{
				Key: "stacks/dev/app", Path: "stacks/dev/app", Tool: v1.ToolTofu, Environment: v1.DefaultEnvironment,
				PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
		{
			name: "matches are ignored without from_var_files",
			root: root, path: "stacks/dev/app", instance: "prod", matched: []string{"workspaces/prod.tfvars"},
			want: Effective{
				Key: "stacks/dev/app:prod", Path: "stacks/dev/app", Instance: "prod", Tool: v1.ToolTofu, Environment: "prod",
				PlanOutput: v1.PlanOutputFull, AllowedTeams: []string{"platform-eng"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveMatched(tt.root, tt.path, tt.stack, tt.instance, tt.matched)
			require.NoError(t, err)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ResolveMatched mismatch (-want +got):\n%s", diff)
			}
			if tt.matched == nil {
				plain, err := Resolve(tt.root, tt.path, tt.stack, tt.instance)
				require.NoError(t, err)
				assert.Equal(t, got, plain)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name     string
		root     func(*v1.RepoConfig)
		stack    *v1.StackConfig
		instance string
		want     []string
	}{
		{
			name: "environments value",
			root: func(c *v1.RepoConfig) { c.Environments = map[string]string{"stacks/": "{{ .Env }}"} },
			want: []string{`stackorder.yaml: environments["stacks/"]: template "{{ .Env }}"`},
		},
		{
			name: "root backend_config",
			root: func(c *v1.RepoConfig) { c.BackendConfig = []string{"ok=1", "key={{ .Nope }}"} },
			want: []string{"stackorder.yaml: backend_config[1]:"},
		},
		{
			name:  "stack env value per mode",
			stack: &v1.StackConfig{Env: v1.EnvConfig{"X": {Modes: &v1.EnvModes{Apply: ptr("{{ .Nope }}")}}}},
			want:  []string{"stacks/a/.stackorder.yaml: env.X.apply:"},
		},
		{
			name:     "instance var_files",
			stack:    &v1.StackConfig{Instances: v1.Instances{"prod": {VarFiles: []string{"{{ nope }}"}}}},
			instance: "prod",
			want:     []string{"stacks/a/.stackorder.yaml: instances.prod.var_files[0]:"},
		},
		{
			name:     "rendered workspace",
			stack:    &v1.StackConfig{Workspace: "{{ .Instance }} x"},
			instance: "prod",
			want:     []string{`stacks/a/.stackorder.yaml: workspace: "prod x" contains invalid characters`},
		},
		{
			name:     "rendered depends_on",
			stack:    &v1.StackConfig{Instances: v1.Instances{"prod": {DependsOn: []string{"/{{ .Instance }}"}}}},
			instance: "prod",
			want:     []string{"stacks/a/.stackorder.yaml: instances.prod.depends_on[0]:", "must be repository relative"},
		},
		{
			name: "every error together",
			root: func(c *v1.RepoConfig) { c.VarFiles = []string{"{{ .A }}"} },
			stack: &v1.StackConfig{
				Environment: "{{ .B }}",
				Env:         v1.EnvConfig{"Y": v1.EnvString("{{ .C }}")},
			},
			want: []string{"stackorder.yaml: var_files[0]:", "stacks/a/.stackorder.yaml: environment:", "stacks/a/.stackorder.yaml: env.Y:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := Default()
			if tt.root != nil {
				tt.root(root)
			}
			_, err := Resolve(root, "stacks/a", tt.stack, tt.instance)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}

func TestResolveRefusesAmbiguousInput(t *testing.T) {
	root := instancesRoot()
	_, err := ResolveMatched(root, "infra/app", instancesStack(), "prod", []string{"workspaces/prod.tfvars.json", "workspaces/prod.x.tfvars.json"})
	assert.ErrorContains(t, err, `stacks.instances.from_var_files: workspaces/prod.tfvars.json, workspaces/prod.x.tfvars.json all name instance "prod"`)

	_, err = Resolve(root, "infra/app:zz", nil, "prod")
	assert.ErrorContains(t, err, `stack path "infra/app:zz" carries an instance suffix`)
}

func TestEnvFor(t *testing.T) {
	e := Effective{Env: map[v1.RunMode]map[string]string{v1.ModePlan: {"A": "1"}, v1.ModeApply: {}}}
	assert.Equal(t, map[string]string{"A": "1"}, e.EnvFor(v1.ModePlan))
	assert.Nil(t, e.EnvFor(v1.ModeApply))
	assert.Nil(t, e.EnvFor(v1.ModeDrift))
	assert.Nil(t, Effective{}.EnvFor(v1.ModePlan))

	e.EnvFor(v1.ModePlan)["A"] = "changed"
	assert.Equal(t, "1", e.Env[v1.ModePlan]["A"], "EnvFor returns a copy")
}

func TestNormalizePath(t *testing.T) {
	tests := map[string]string{
		"./stacks/prod/vpc/":       "stacks/prod/vpc",
		"stacks//prod/vpc":         "stacks/prod/vpc",
		"stacks\\prod\\vpc":        "stacks/prod/vpc",
		"stacks/prod/vpc:blue":     "stacks/prod/vpc:blue",
		"./stacks/prod/vpc/:blue":  "stacks/prod/vpc:blue",
		"stacks\\prod\\vpc:eu-1.a": "stacks/prod/vpc:eu-1.a",
		"stacks/prod/vpc:default":  "stacks/prod/vpc",
		"stacks/prod/../dev/vpc:x": "stacks/dev/vpc:x",
		".":                        "",
		"":                         "",
	}
	for in, want := range tests {
		assert.Equal(t, want, NormalizePath(in), in)
	}
}
