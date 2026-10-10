package config

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestParseInstanceKeys(t *testing.T) {
	root, err := Parse([]byte(`
version: 1
stacks:
  exclude: ["infra/state-backend"]
  instances:
    from_var_files: "workspaces/*.tfvars.json"
backend_config:
  - infra/state.s3.tfbackend
  - 'key={{ trimPrefix "infra/" .Path }}/{{ .Instance }}.tfstate'
var_files: [common.tfvars]
env:
  TF_VAR_environment: "{{ .Instance }}"
  TF_VAR_role: { plan: reader, apply: deployer }
environments:
  "infra/": "infra-{{ .Instance }}"
  "infra/:prod": production
  ":canary": canary
`))
	require.NoError(t, err)
	assert.Equal(t, []string{"infra/state-backend"}, root.Stacks.Exclude)
	assert.Equal(t, "workspaces/*.tfvars.json", root.Stacks.Instances.FromVarFiles)
	assert.Len(t, root.BackendConfig, 2)
	assert.Equal(t, []string{"common.tfvars"}, root.VarFiles)
	want := v1.EnvConfig{
		"TF_VAR_environment": v1.EnvString("{{ .Instance }}"),
		"TF_VAR_role":        {Modes: &v1.EnvModes{Plan: ptr("reader"), Apply: ptr("deployer")}},
	}
	if diff := cmp.Diff(want, root.Env); diff != "" {
		t.Errorf("env mismatch (-want +got):\n%s", diff)
	}

	tests := []struct {
		name string
		src  string
		want v1.Instances
	}{
		{name: "list", src: "instances: [prod, dev]\n", want: v1.Instances{"prod": {}, "dev": {}}},
		{name: "map with empty entries", src: "instances:\n  prod:\n  dev: {}\n", want: v1.Instances{"prod": {}, "dev": {}}},
		{
			name: "map with overrides",
			src: `workspace: "{{ .Instance }}"
instances:
  prod:
    environment: production
    workspace: live
    backend_config: [bucket=prod]
    var_files: [prod.tfvars]
    env: { REGION: eu-west-1, ROLE: { apply: deployer } }
    plan_output: summary
    apply: { allowed_teams: [prod-team] }
    depends_on: ["infra/vpc:{{ .Instance }}"]
    ignore_inferred: [infra/legacy]
`,
			want: v1.Instances{"prod": {
				Environment:    "production",
				Workspace:      "live",
				BackendConfig:  []string{"bucket=prod"},
				VarFiles:       []string{"prod.tfvars"},
				Env:            v1.EnvConfig{"REGION": v1.EnvString("eu-west-1"), "ROLE": {Modes: &v1.EnvModes{Apply: ptr("deployer")}}},
				PlanOutput:     v1.PlanOutputSummary,
				Apply:          &v1.StackApplyConfig{AllowedTeams: []string{"prod-team"}},
				DependsOn:      []string{"infra/vpc:{{ .Instance }}"},
				IgnoreInferred: []string{"infra/legacy"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseStack([]byte(tt.src))
			require.NoError(t, err)
			if diff := cmp.Diff(tt.want, s.Instances); diff != "" {
				t.Errorf("instances mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseRootValidation(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{name: "empty exclude glob", src: "stacks:\n  exclude: [\"\"]\n", want: []string{"stacks.exclude[0]: empty glob"}},
		{name: "invalid exclude glob", src: "stacks:\n  exclude: [\"a/[\"]\n", want: []string{"stacks.exclude[0]:"}},
		{name: "absolute from_var_files", src: "stacks:\n  instances:\n    from_var_files: /etc/*.tfvars\n", want: []string{"stacks.instances.from_var_files:"}},
		{name: "invalid from_var_files", src: "stacks:\n  instances:\n    from_var_files: \"a/[\"\n", want: []string{"stacks.instances.from_var_files:"}},
		{name: "unknown key under instances", src: "stacks:\n  instances:\n    from: x\n", want: []string{"field from not found"}},
		{name: "empty backend_config entry", src: "backend_config: [\"\"]\n", want: []string{"backend_config[0]: empty entry"}},
		{name: "backend_config without a name", src: "backend_config: [\"=x\"]\n", want: []string{"backend_config[0]:", "no attribute name"}},
		{name: "absolute backend_config file", src: "backend_config: [/etc/state.tfbackend]\n", want: []string{"backend_config[0]:", "repository relative"}},
		{name: "backend_config template", src: "backend_config: [\"key={{ .Nope }}\"]\n", want: []string{"backend_config[0]:", "Nope"}},
		{name: "empty var_files entry", src: "var_files: [\"\"]\n", want: []string{"var_files[0]: empty path"}},
		{name: "absolute var_files entry", src: "var_files: [/x.tfvars]\n", want: []string{"var_files[0]:", "stack relative"}},
		{name: "reserved env prefix", src: "env:\n  GITHUB_TOKEN: x\n", want: []string{"env.GITHUB_TOKEN:", "reserved prefix GITHUB_"}},
		{name: "reserved env prefix in any case", src: "env:\n  stackorder_x: x\n", want: []string{"env.stackorder_x:", "reserved prefix STACKORDER_"}},
		{name: "reserved env name", src: "env:\n  PATH: x\n", want: []string{"env.PATH:", "reserved"}},
		{name: "invalid env name", src: "env:\n  1X: x\n", want: []string{"env.1X:", "must match"}},
		{name: "empty env object", src: "env:\n  X: {}\n", want: []string{"env.X: set at least one of plan, apply and drift"}},
		{name: "unknown env mode", src: "env:\n  X: { deploy: x }\n", want: []string{"field deploy not found"}},
		{name: "env value template", src: "env:\n  X: { drift: \"{{ .Nope }}\" }\n", want: []string{"env.X.drift:"}},
		{name: "env list value", src: "env:\n  X: [a]\n", want: []string{"env value must be a string or a map"}},
		{name: "environments key with an empty instance", src: "environments:\n  \"infra/:\": x\n", want: []string{`environments["infra/:"]: empty instance name`}},
		{name: "environments key with a reserved instance", src: "environments:\n  \":default\": x\n", want: []string{`environments[":default"]:`, "reserved"}},
		{name: "environments value template", src: "environments:\n  infra/: \"{{ .Nope }}\"\n", want: []string{`environments["infra/"]:`, "Nope"}},
		{name: "environments key ./ without an instance", src: "environments:\n  ./: x\n", want: []string{`environments["./"]: empty path prefix`}},
		{name: "environments key / without an instance", src: "environments:\n  /: x\n", want: []string{`environments["/"]: empty path prefix`}},
		{name: "environments prefix with a colon", src: "environments:\n  \"a:b:c\": x\n", want: []string{`environments["a:b:c"]: path prefix "a:b" contains ":"`}},
		{
			name: "environments keys that normalise to the same prefix",
			src:  "environments:\n  infra: a\n  infra/: b\n  ./infra: c\n  \"infra:x\": d\n  \"./infra/:x\": e\n",
			want: []string{
				`environments: keys "./infra" and "infra" name the same path prefix and instance`,
				`environments: keys "./infra" and "infra/" name the same path prefix and instance`,
				`environments: keys "./infra/:x" and "infra:x" name the same path prefix and instance`,
			},
		},
		{name: "range in a template", src: "env:\n  X: \"{{ range 20000000 }}x{{ end }}\"\n", want: []string{"env.X:", "range is not supported"}},
		{
			name: "every error is reported with its key",
			src:  "var_files: [\"\"]\nbackend_config: [\"\"]\nenv:\n  HOME: x\n",
			want: []string{"var_files[0]", "backend_config[0]", "env.HOME"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte("version: 1\n" + tt.src))
			require.Error(t, err)
			for _, want := range tt.want {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}

func TestParseStackValidation(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{name: "reserved instance name", src: "instances: [Default]\n", want: []string{`instances: instance name "Default" is reserved`}},
		{name: "instance name alphabet", src: "instances: [\"-a\"]\n", want: []string{`instances: instance name "-a" must match`}},
		{name: "instance name with a space", src: "instances: [\"a b\"]\n", want: []string{`instance name "a b" must match`}},
		{name: "long instance name", src: "instances: [" + strings.Repeat("a", 65) + "]\n", want: []string{"longer than 64 characters"}},
		{name: "duplicate instance", src: "instances: [a, a]\n", want: []string{`"a" is listed twice`}},
		{name: "instances scalar", src: "instances: a\n", want: []string{"instances must be a list of names or a map"}},
		{name: "unknown override key", src: "instances:\n  a: { tool: tofu }\n", want: []string{"field tool not found"}},
		{name: "legacy workspace is an instance name", src: "workspace: a+b\n", want: []string{`workspace: "a+b" names the stack's only instance`}},
		{name: "workspace template", src: "workspace: \"{{ .Nope }}\"\n", want: []string{"workspace:", "Nope"}},
		{name: "templated legacy workspace is an instance name", src: "workspace: \"{{ .Instance }}-x\"\n", want: []string{`workspace: "-x" names the stack's only instance`}},
		{name: "environment template", src: "environment: \"{{ nope }}\"\n", want: []string{"environment:", `function "nope" not defined`}},
		{name: "depends_on template", src: "depends_on: [\"/{{ .Instance }}\"]\n", want: []string{"depends_on[0]:", "repository relative"}},
		{name: "ignore_inferred template", src: "ignore_inferred: [\"{{ .Nope }}\"]\n", want: []string{"ignore_inferred[0]:"}},
		{name: "stack env name", src: "env:\n  RUNNER_TEMP: x\n", want: []string{"env.RUNNER_TEMP:", "reserved prefix RUNNER_"}},
		{name: "stack var_files", src: "var_files: [\"\"]\n", want: []string{"var_files[0]: empty path"}},
		{name: "stack backend_config", src: "backend_config: [\"\"]\n", want: []string{"backend_config[0]: empty entry"}},
		{name: "stack allowed_teams", src: "apply: { allowed_teams: [\"\"] }\n", want: []string{"apply.allowed_teams[0]: empty team"}},
		{
			name: "instance overrides are validated with their key",
			src: `instances:
  prod:
    environment: "{{ .Nope }}"
    workspace: "a b"
    backend_config: [""]
    var_files: [/abs.tfvars]
    env: { ACTIONS_X: y }
    plan_output: none
    apply: { allowed_teams: [""] }
    depends_on: [../escape]
    ignore_inferred: [""]
`,
			want: []string{
				"instances.prod.environment:",
				`instances.prod.workspace: "a b" contains invalid characters`,
				"instances.prod.backend_config[0]: empty entry",
				"instances.prod.var_files[0]:",
				"instances.prod.env.ACTIONS_X:",
				`instances.prod.plan_output: "none"`,
				"instances.prod.apply.allowed_teams[0]: empty team",
				"instances.prod.depends_on[0]:",
				"instances.prod.ignore_inferred[0]: empty stack key",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStack([]byte(tt.src))
			require.Error(t, err)
			for _, want := range tt.want {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}

func TestParseStackAccepts(t *testing.T) {
	tests := map[string]string{
		"templated workspace with instances": "workspace: \"{{ .Instance }}\"\ninstances: [prod, eu-west-1, v1.2_a]\n",
		"workspace default":                  "workspace: default\n",
		"templated legacy workspace":         "workspace: \"{{ .Name }}\"\n",
		"workspace that renders empty":       "workspace: \"{{ .Instance }}\"\n",
		"literal workspace with instances":   "workspace: shared\ninstances: [a, b]\n",
		"templated depends_on":               "depends_on: [\"infra/vpc:{{ .Instance }}\", \"acme/net//{{ dir .Path }}/tgw\"]\n",
		"env string and object":              "env:\n  TF_VAR_a: \"{{ upper .Instance }}\"\n  _b: { plan: x, drift: \"\" }\n",
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseStack([]byte(src))
			require.NoError(t, err)
		})
	}
}

func TestValidateInstanceName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr string
	}{
		{"prod", ""},
		{"eu-west-1", ""},
		{"v1.2_a", ""},
		{"0", ""},
		{strings.Repeat("a", 64), ""},
		{"", "empty instance name"},
		{strings.Repeat("a", 65), "longer than 64"},
		{"default", "reserved"},
		{"DEFAULT", "reserved"},
		{".hidden", "must match"},
		{"a:b", "must match"},
		{"a/b", "must match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInstanceName(tt.name)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateEnvName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr string
	}{
		{"TF_VAR_role", ""},
		{"_x", ""},
		{"AWS_PROFILE", ""},
		{"GITHUBX", ""},
		{"", "must match"},
		{"1X", "must match"},
		{"A-B", "must match"},
		{"STACKORDER_STACK", "reserved prefix"},
		{"GITHUB_TOKEN", "reserved prefix"},
		{"ACTIONS_ID_TOKEN_REQUEST_URL", "reserved prefix"},
		{"RUNNER_TEMP", "reserved prefix"},
		{"PATH", "reserved"},
		{"HOME", "reserved"},
		{"home", "reserved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvName(tt.name)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParseRefusesNullOutputs(t *testing.T) {
	tests := []struct {
		name  string
		parse func() error
		want  string
	}{
		{name: "root", parse: func() error { _, err := Parse([]byte("version: 1\noutputs:\n")); return err }, want: "stackorder.yaml: outputs: no value; write { readers: [] }"},
		{name: "stack", parse: func() error { _, err := ParseStack([]byte("outputs: ~\n")); return err }, want: ".stackorder.yaml: outputs: no value; write { readers: [] }"},
		{
			name:  "instance",
			parse: func() error { _, err := ParseStack([]byte("instances:\n  blue:\n    outputs:\n")); return err },
			want:  ".stackorder.yaml: instances.blue.outputs: no value; write { readers: [] }",
		},
		{name: "root without readers", parse: func() error { _, err := Parse([]byte("version: 1\noutputs: {}\n")); return err }},
		{name: "stack without outputs", parse: func() error { _, err := ParseStack([]byte("instances: [blue]\n")); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.parse()
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateOutputsReaders(t *testing.T) {
	levels := []struct {
		name, prefix string
		parse        func(outputs string) error
	}{
		{name: "root", prefix: "outputs.", parse: func(outputs string) error {
			_, err := Parse([]byte("version: 1\noutputs: " + outputs + "\n"))
			return err
		}},
		{name: "stack", prefix: "outputs.", parse: func(outputs string) error {
			_, err := ParseStack([]byte("outputs: " + outputs + "\n"))
			return err
		}},
		{name: "instance", prefix: "instances.blue.outputs.", parse: func(outputs string) error {
			_, err := ParseStack([]byte("instances:\n  blue:\n    outputs: " + outputs + "\n"))
			return err
		}},
	}
	tests := []struct {
		name    string
		outputs string
		want    []string
	}{
		{name: "same repository patterns", outputs: `{readers: [stacks/prod/eks, "stacks/prod/*", "stacks/**", "stacks/prod/apps:*", "**", "stacks/{eks,dns}"]}`},
		{name: "another repository", outputs: `{readers: ["acme/platform//clusters/**", "Acme/Platform//stacks/prod/eks:blue"]}`},
		{name: "templated entries", outputs: `{readers: ["stacks/prod/eks:{{ .Instance }}", "{{ dir .Path }}/**"]}`},
		{name: "no reader, sensitive shared", outputs: `{readers: [], sensitive: share}`},
		{name: "sensitive withheld", outputs: `{sensitive: withhold}`},
		{name: "bad glob", outputs: `{readers: ["stacks/prod/[eks"]}`, want: []string{`readers[0]: "stacks/prod/[eks": "stacks/prod/[eks" is not a valid glob`}},
		{
			name:    "wildcard in the repository",
			outputs: `{readers: ["acme/*//stacks/**", "acme/{a,b}//stacks/**"]}`,
			want: []string{
				`readers[0]: "acme/*//stacks/**": repository "acme/*" must be literal`,
				`readers[1]: "acme/{a,b}//stacks/**": repository "acme/{a,b}" must be literal`,
			},
		},
		{
			name:    "repository that is not owner/repo",
			outputs: `{readers: ["infra//stacks/**", "//stacks/**", "acme/infra/x//stacks/**"]}`,
			want: []string{
				`readers[0]: "infra//stacks/**": repository must be owner/repo`,
				`readers[1]: "//stacks/**": repository must be owner/repo`,
				`readers[2]: "acme/infra/x//stacks/**": repository must be owner/repo`,
			},
		},
		{name: "empty entry", outputs: `{readers: ["", " "]}`, want: []string{"readers[0]: empty entry", "readers[1]: empty entry"}},
		{name: "empty pattern", outputs: `{readers: ["acme/infra//"]}`, want: []string{`readers[0]: "acme/infra//": empty stack key pattern`}},
		{name: "absolute pattern", outputs: `{readers: ["/stacks/**"]}`, want: []string{`readers[0]: "/stacks/**": stack key pattern must be repository relative`}},
		{
			name:    "pattern outside the repository",
			outputs: `{readers: ["..", "../stacks/**", "stacks/../../eks", ".", "acme/platform//../clusters/**"]}`,
			want: []string{
				`readers[0]: "..": stack key pattern must be repository relative`,
				`readers[1]: "../stacks/**": stack key pattern must be repository relative`,
				`readers[2]: "stacks/../../eks": stack key pattern must be repository relative`,
				`readers[3]: ".": stack key pattern must be repository relative`,
				`readers[4]: "acme/platform//../clusters/**": stack key pattern must be repository relative`,
			},
		},
		{
			name:    "pattern spelled unlike a stack key",
			outputs: `{readers: ["./stacks/prod/eks", "stacks/prod/eks/", "acme/platform//clusters//eu", "stacks/./prod/eks", "stacks/../eks", "acme/platform//./clusters/**"]}`,
			want: []string{
				`readers[0]: "./stacks/prod/eks": never matches a stack key; write "stacks/prod/eks"`,
				`readers[1]: "stacks/prod/eks/": never matches a stack key; write "stacks/prod/eks"`,
				`readers[2]: "acme/platform//clusters//eu": never matches a stack key; write "acme/platform//clusters/eu"`,
				`readers[3]: "stacks/./prod/eks": never matches a stack key; write "stacks/prod/eks"`,
				`readers[4]: "stacks/../eks": never matches a stack key; write "eks"`,
				`readers[5]: "acme/platform//./clusters/**": never matches a stack key; write "acme/platform//clusters/**"`,
			},
		},
		{
			name:    "default or empty instance",
			outputs: `{readers: ["stacks/prod/eks:default", "stacks/prod/*:DEFAULT", "stacks/prod/eks:", "stacks/prod/eks/:blue"]}`,
			want: []string{
				`readers[0]: "stacks/prod/eks:default": never matches a stack key; write "stacks/prod/eks"`,
				`readers[1]: "stacks/prod/*:DEFAULT": never matches a stack key; write "stacks/prod/*"`,
				`readers[2]: "stacks/prod/eks:": never matches a stack key; write "stacks/prod/eks"`,
				`readers[3]: "stacks/prod/eks/:blue": never matches a stack key; write "stacks/prod/eks:blue"`,
			},
		},
		{
			name:    "surrounding space",
			outputs: `{readers: [" stacks/prod/eks", "stacks/prod/eks ", "acme/platform //clusters/**"]}`,
			want: []string{
				`readers[0]: " stacks/prod/eks": never matches a stack key; write "stacks/prod/eks"`,
				`readers[1]: "stacks/prod/eks ": never matches a stack key; write "stacks/prod/eks"`,
				`readers[2]: "acme/platform //clusters/**": repository must be owner/repo`,
			},
		},
		{name: "broken template", outputs: `{readers: ["stacks/{{ .Nope }}"]}`, want: []string{`readers[0]: template "stacks/{{ .Nope }}"`}},
		{name: "unknown sensitive", outputs: `{sensitive: maybe}`, want: []string{`sensitive: "maybe" is not one of share, withhold`}},
		{
			name:    "every error with its key",
			outputs: `{readers: ["stacks/**", "stacks/[", ""], sensitive: all}`,
			want:    []string{"readers[1]:", "readers[2]: empty entry", `sensitive: "all"`},
		},
	}
	for _, level := range levels {
		for _, tt := range tests {
			t.Run(level.name+"/"+tt.name, func(t *testing.T) {
				err := level.parse(tt.outputs)
				if len(tt.want) == 0 {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				for _, want := range tt.want {
					assert.ErrorContains(t, err, level.prefix+want)
				}
			})
		}
	}
}
