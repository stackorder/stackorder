package v1_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func strictYAML(src string, out any) error {
	dec := yaml.NewDecoder(strings.NewReader(src))
	dec.KnownFields(true)
	return dec.Decode(out)
}

func ptr(s string) *string { return &s }

func TestInstancesForms(t *testing.T) {
	prod := v1.InstanceConfig{
		Environment: "production",
		Workspace:   "{{ .Instance }}",
		VarFiles:    []string{"prod.tfvars"},
		Env:         v1.EnvConfig{"ROLE": {Modes: &v1.EnvModes{Apply: ptr("deployer")}}},
		Apply:       &v1.StackApplyConfig{AllowedTeams: []string{"prod-team"}},
	}
	tests := []struct {
		name     string
		yaml     string
		json     string
		want     v1.Instances
		wantJSON string
	}{
		{
			name:     "list",
			yaml:     "instances: [prod, dev]\n",
			json:     `{"instances":["prod","dev"]}`,
			want:     v1.Instances{"prod": {}, "dev": {}},
			wantJSON: `{"instances":{"dev":{},"prod":{}}}`,
		},
		{
			name: "map",
			yaml: `instances:
  dev:
  prod:
    environment: production
    workspace: "{{ .Instance }}"
    var_files: [prod.tfvars]
    env: { ROLE: { apply: deployer } }
    apply: { allowed_teams: [prod-team] }
`,
			json:     `{"instances":{"prod":{"environment":"production","workspace":"{{ .Instance }}","var_files":["prod.tfvars"],"env":{"ROLE":{"apply":"deployer"}},"apply":{"allowed_teams":["prod-team"]}},"dev":{}}}`,
			want:     v1.Instances{"prod": prod, "dev": {}},
			wantJSON: `{"instances":{"dev":{},"prod":{"environment":"production","workspace":"{{ .Instance }}","var_files":["prod.tfvars"],"env":{"ROLE":{"apply":"deployer"}},"apply":{"allowed_teams":["prod-team"]}}}}`,
		},
		{
			name:     "empty list",
			yaml:     "instances: []\n",
			json:     `{"instances":[]}`,
			want:     v1.Instances{},
			wantJSON: `{}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fromYAML v1.StackConfig
			require.NoError(t, strictYAML(tt.yaml, &fromYAML))
			if diff := cmp.Diff(tt.want, fromYAML.Instances); diff != "" {
				t.Errorf("YAML mismatch (-want +got):\n%s", diff)
			}

			var fromJSON v1.StackConfig
			require.NoError(t, json.Unmarshal([]byte(tt.json), &fromJSON))
			if diff := cmp.Diff(tt.want, fromJSON.Instances); diff != "" {
				t.Errorf("JSON mismatch (-want +got):\n%s", diff)
			}

			encoded, err := json.Marshal(fromYAML)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(encoded))
			again, err := json.Marshal(fromJSON)
			require.NoError(t, err)
			assert.Equal(t, string(encoded), string(again), "deterministic encoding")

			var roundTrip v1.StackConfig
			require.NoError(t, json.Unmarshal(encoded, &roundTrip))
			assert.Equal(t, fromYAML.Instances.Names(), roundTrip.Instances.Names())

			out, err := yaml.Marshal(fromYAML)
			require.NoError(t, err)
			var yamlTrip v1.StackConfig
			require.NoError(t, yaml.Unmarshal(out, &yamlTrip))
			if len(tt.want) > 0 {
				if diff := cmp.Diff(tt.want, yamlTrip.Instances, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("YAML round trip mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestInstancesErrors(t *testing.T) {
	tests := []struct {
		name, yaml, json, want string
	}{
		{name: "duplicate name", yaml: "instances: [a, a]\n", json: `{"instances":["a","a"]}`, want: `"a" is listed twice`},
		{name: "scalar", yaml: "instances: a\n", json: `{"instances":"a"}`, want: "instances"},
		{name: "list of objects", yaml: "instances: [{a: 1}]\n", json: `{"instances":[{"a":1}]}`, want: "cannot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s v1.StackConfig
			assert.ErrorContains(t, strictYAML(tt.yaml, &s), tt.want)
			assert.Error(t, json.Unmarshal([]byte(tt.json), &s))
		})
	}
}

func TestYAMLNullsAreRefused(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{name: "null instance name", yaml: "instances: [null]\n", want: "instances[0]: empty instance name"},
		{name: "empty list item", yaml: "instances:\n  - a\n  -\n", want: "instances[1]: empty instance name"},
		{name: "null env value", yaml: "env:\n  X: ~\n", want: "env.X: no value"},
		{name: "missing env value", yaml: "env:\n  X:\n", want: "env.X: no value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s v1.StackConfig
			assert.ErrorContains(t, strictYAML(tt.yaml, &s), tt.want)
		})
	}
}

func TestInstancesStrictYAML(t *testing.T) {
	var s v1.StackConfig
	err := strictYAML("instances:\n  prod: { stage: x }\n", &s)
	assert.ErrorContains(t, err, "field stage not found")
	err = strictYAML("env:\n  X: { plan: a, deploy: b }\n", &s)
	assert.ErrorContains(t, err, "field deploy not found")
}

func TestInstancesNames(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, v1.Instances{"c": {}, "a": {}, "b": {}}.Names())
	assert.Empty(t, v1.Instances(nil).Names())
}

func TestEnvValueShapes(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		json string
		want v1.EnvValue
	}{
		{name: "string", yaml: "x", json: `"x"`, want: v1.EnvString("x")},
		{name: "empty string", yaml: `""`, json: `""`, want: v1.EnvString("")},
		{name: "number spelled in YAML", yaml: "8080", json: `"8080"`, want: v1.EnvString("8080")},
		{name: "template", yaml: `"{{ .Instance }}"`, json: `"{{ .Instance }}"`, want: v1.EnvString("{{ .Instance }}")},
		{
			name: "all modes",
			yaml: "{plan: reader, apply: deployer, drift: auditor}",
			json: `{"plan":"reader","apply":"deployer","drift":"auditor"}`,
			want: v1.EnvValue{Modes: &v1.EnvModes{Plan: ptr("reader"), Apply: ptr("deployer"), Drift: ptr("auditor")}},
		},
		{
			name: "some modes, one empty",
			yaml: `{apply: ""}`,
			json: `{"apply":""}`,
			want: v1.EnvValue{Modes: &v1.EnvModes{Apply: ptr("")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fromYAML v1.EnvConfig
			require.NoError(t, strictYAML("X: "+tt.yaml+"\n", &fromYAML))
			if diff := cmp.Diff(tt.want, fromYAML["X"]); diff != "" {
				t.Errorf("YAML mismatch (-want +got):\n%s", diff)
			}

			var fromJSON v1.EnvValue
			require.NoError(t, json.Unmarshal([]byte(tt.json), &fromJSON))
			if diff := cmp.Diff(tt.want, fromJSON); diff != "" {
				t.Errorf("JSON mismatch (-want +got):\n%s", diff)
			}

			encoded, err := json.Marshal(tt.want)
			require.NoError(t, err)
			assert.JSONEq(t, tt.json, string(encoded))

			out, err := yaml.Marshal(v1.EnvConfig{"X": tt.want})
			require.NoError(t, err)
			var yamlTrip v1.EnvConfig
			require.NoError(t, strictYAML(string(out), &yamlTrip))
			if diff := cmp.Diff(tt.want, yamlTrip["X"]); diff != "" {
				t.Errorf("YAML round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEnvValueErrors(t *testing.T) {
	var v v1.EnvValue
	assert.Error(t, json.Unmarshal([]byte(`1`), &v))
	assert.Error(t, json.Unmarshal([]byte(`["a"]`), &v))
	var env v1.EnvConfig
	assert.ErrorContains(t, strictYAML("X: [a]\n", &env), "env value must be a string or a map")
}

func TestRepoConfigJSONOmitsUnsetInstanceKeys(t *testing.T) {
	encoded, err := json.Marshal(v1.RepoConfig{Version: 1})
	require.NoError(t, err)
	for _, key := range []string{"exclude", "instances", "backend_config", "var_files", `"env"`} {
		assert.NotContains(t, string(encoded), key)
	}
	encoded, err = json.Marshal(v1.RepoConfig{Stacks: v1.StacksConfig{Instances: v1.InstancesConfig{FromVarFiles: "w/*.tfvars"}}})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"instances":{"from_var_files":"w/*.tfvars"}`)
}

func TestStackKeyWithInstance(t *testing.T) {
	tests := []struct {
		path, instance, key string
	}{
		{"./stacks/a/", "", "stacks/a"},
		{"stacks/a", "default", "stacks/a"},
		{"stacks/a", "eu-west-1", "stacks/a:eu-west-1"},
		{"stacks/a", "v1.2_x", "stacks/a:v1.2_x"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			assert.Equal(t, tt.key, v1.StackKey(tt.path, tt.instance))
			p, instance := v1.SplitStackKey(tt.key)
			assert.Equal(t, strings.Trim(strings.TrimPrefix(tt.path, "./"), "/"), p)
			if tt.instance != "default" {
				assert.Equal(t, tt.instance, instance)
			}
		})
	}
}
