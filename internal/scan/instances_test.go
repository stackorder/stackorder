package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const stateBackendFile = "infra/state.s3.tfbackend"

func instanceState(key string) *v1.Backend {
	return &v1.Backend{Type: "s3", Bucket: "acme-state", Key: key, Region: "eu-west-1", UseLockfile: true}
}

func instanceStack(key string, backend *v1.Backend, cfg *v1.StackConfig, watch ...string) v1.Stack {
	p, instance := v1.SplitStackKey(key)
	return v1.Stack{
		Key: key, Path: p, Instance: instance, Backend: backend,
		Environment: fallbackEnvironment("", instance), Tool: v1.ToolTerraform, PlanOutput: "full",
		Config: cfg, WatchPaths: append([]string{stateBackendFile}, watch...),
	}
}

func instancesGraph() *v1.Graph {
	kyc := &v1.StackConfig{
		BackendConfig: []string{"key=kyc/{{ .Instance }}.tfstate"},
		VarFiles:      []string{"../../vars/common.tfvars", "missing.tfvars"},
	}
	dup := &v1.StackConfig{
		Instances:     v1.Instances{"blue": {}, "green": {}},
		BackendConfig: []string{"key=dup.tfstate"},
	}
	web := &v1.StackConfig{
		BackendConfig: []string{"key=web/{{ .Instance }}.tfstate"},
		DependsOn:     []string{"infra/kyc", "infra/single", "infra/dup", "acme/network//infra/tgw:{{ .Instance }}"},
	}
	return &v1.Graph{
		Repo: fixtureRepo,
		SHA:  "sha",
		Stacks: []v1.Stack{
			{Key: "acme/network//infra/tgw:production", Path: "infra/tgw", Instance: "production", Repo: "acme/network", External: true},
			{Key: "acme/network//infra/tgw:staging", Path: "infra/tgw", Instance: "staging", Repo: "acme/network", External: true},
			instanceStack("infra/dup:blue", instanceState("dup.tfstate"), dup),
			instanceStack("infra/dup:green", instanceState("dup.tfstate"), dup),
			instanceStack("infra/kyc:production", instanceState("kyc/production.tfstate"), kyc, "vars/common.tfvars"),
			instanceStack("infra/kyc:staging", instanceState("kyc/staging.tfstate"), kyc, "vars/common.tfvars"),
			instanceStack("infra/reader", instanceState("reader.tfstate"), nil),
			instanceStack("infra/single:main", instanceState("single.tfstate"), &v1.StackConfig{Instances: v1.Instances{"main": {}}}),
			instanceStack("infra/web:production", instanceState("web/production.tfstate"), web),
			instanceStack("infra/web:staging", instanceState("web/staging.tfstate"), web),
		},
		Modules: []v1.Module{},
		Edges: []v1.Edge{
			readsState("infra/reader", "infra/kyc:production", "acme-state", "kyc/production.tfstate"),
			dependsOn("infra/web:production", "acme/network//infra/tgw:production"),
			dependsOn("infra/web:production", "infra/dup"),
			dependsOn("infra/web:production", "infra/kyc:production"),
			dependsOn("infra/web:production", "infra/single:main"),
			dependsOn("infra/web:staging", "acme/network//infra/tgw:staging"),
			dependsOn("infra/web:staging", "infra/dup"),
			dependsOn("infra/web:staging", "infra/kyc:staging"),
			dependsOn("infra/web:staging", "infra/single:main"),
		},
		Warnings: []string{
			`infra/dup/workspaces/red.tfvars.json: names instance "red", which infra/dup/.stackorder.yaml does not declare; the file is not used`,
			"infra/kyc:production: var file infra/kyc/missing.tfvars does not exist",
			"infra/kyc:staging: var file infra/kyc/missing.tfvars does not exist",
			"infra/web:production: depends_on infra/dup: no such stack in this repository",
			"infra/web:staging: depends_on infra/dup: no such stack in this repository",
			"stacks infra/dup:blue, infra/dup:green share the state object s3://acme-state/dup.tfstate",
		},
	}
}

func copyInstancesFixture(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("testdata", "instances"))); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestScanInstances(t *testing.T) {
	got, err := Scan(context.Background(), filepath.Join("testdata", "instances"), Options{Repo: fixtureRepo, SHA: "sha"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(instancesGraph(), got, ignoreTreeHash); diff != "" {
		t.Errorf("Scan mismatch (-want +got):\n%s", diff)
	}
}

func TestScanInstancesTreeHash(t *testing.T) {
	base, err := Scan(context.Background(), filepath.Join("testdata", "instances"), Options{Repo: fixtureRepo})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		mutate  func(t *testing.T, root string)
		changed bool
	}{
		{name: "identical copy", mutate: func(*testing.T, string) {}},
		{name: "backend config file", mutate: func(t *testing.T, root string) { appendFile(t, root, stateBackendFile, "# edit\n") }, changed: true},
		{name: "shared var file", mutate: func(t *testing.T, root string) { appendFile(t, root, "vars/common.tfvars", "# edit\n") }, changed: true},
		{
			name: "from_var_files file",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "infra/web/workspaces/production.tfvars.json", `{"x": 1}`)
			},
			changed: true,
		},
		{name: "unrelated text file", mutate: func(t *testing.T, root string) { writeFile(t, root, "vars/README.md", "notes\n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := copyInstancesFixture(t)
			tt.mutate(t, root)
			g, err := Scan(context.Background(), root, Options{Repo: fixtureRepo})
			if err != nil {
				t.Fatal(err)
			}
			if changed := g.TreeHash != base.TreeHash; changed != tt.changed {
				t.Errorf("TreeHash changed = %v, want %v", changed, tt.changed)
			}
		})
	}
}

func TestScanTreeHashReadInputs(t *testing.T) {
	files := map[string]string{
		"stackorder.yaml":           "version: 1\nstacks:\n  instances:\n    from_var_files: \"envs/*.json\"\nbackend_config: [cfg/backend.hcl]\n",
		"cfg/backend.hcl":           "bucket = \"b\"\nkey = \"one.tfstate\"\n",
		"shared/common.vars":        "",
		"stacks/a/main.tf":          "terraform {\n  backend \"s3\" {}\n}\n",
		"stacks/a/.stackorder.yaml": "var_files: [../../shared/common.vars]\n",
		"stacks/a/envs/x.json":      "{}",
		"stacks/b/main.tf":          "terraform {\n  backend \"s3\" {}\n}\n",
		"stacks/b/.stackorder.yaml": "instances: [x]\n",
		"stacks/b/envs/x.json":      "{}",
	}
	scan := func(t *testing.T, mutate func(t *testing.T, root string)) *v1.Graph {
		t.Helper()
		root := t.TempDir()
		for rel, content := range files {
			writeFile(t, root, rel, content)
		}
		mutate(t, root)
		g, err := Scan(context.Background(), root, Options{Repo: fixtureRepo})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	base := scan(t, func(*testing.T, string) {})
	tests := []struct {
		name    string
		mutate  func(t *testing.T, root string)
		changed bool
	}{
		{name: "identical copy", mutate: func(*testing.T, string) {}},
		{name: "backend config file of any name", mutate: func(t *testing.T, root string) {
			writeFile(t, root, "cfg/backend.hcl", "bucket = \"b\"\nkey = \"two.tfstate\"\n")
		}, changed: true},
		{name: "new from_var_files match of any name", mutate: func(t *testing.T, root string) { writeFile(t, root, "stacks/a/envs/y.json", "{}") }, changed: true},
		{name: "unused from_var_files match of any name", mutate: func(t *testing.T, root string) { writeFile(t, root, "stacks/b/envs/z.json", "{}") }, changed: true},
		{name: "edited from_var_files match", mutate: func(t *testing.T, root string) { writeFile(t, root, "stacks/a/envs/x.json", `{"a": 1}`) }, changed: true},
		{name: "var file of any name", mutate: func(t *testing.T, root string) { writeFile(t, root, "shared/common.vars", "a = 1\n") }, changed: true},
		{name: "unreferenced file", mutate: func(t *testing.T, root string) { writeFile(t, root, "shared/other.vars", "a = 1\n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := scan(t, tt.mutate)
			if changed := g.TreeHash != base.TreeHash; changed != tt.changed {
				t.Errorf("TreeHash changed = %v, want %v", changed, tt.changed)
			}
		})
	}
}

func TestScanBackendConfig(t *testing.T) {
	partial := "terraform {\n  backend \"s3\" {}\n}\n"
	tests := []struct {
		name     string
		files    map[string]string
		want     []v1.Stack
		warnings []string
	}{
		{
			name: "entries overlay the block literals in order",
			files: map[string]string{
				"stackorder.yaml": "version: 1\nbackend_config:\n  - cfg/one.tfbackend\n  - key=from-pair.tfstate\n",
				"cfg/one.tfbackend": "bucket = \"one\"\nkey = \"from-file.tfstate\"\nregion = \"us-east-1\"\n" +
					"dynamodb_table = \"locks\"\nworkspace_key_prefix = \"envs\"\nprofile = \"ignored\"\n",
				"cfg/two.tfbackend":         "bucket = \"two\"\nuse_lockfile = true\n",
				"stacks/a/main.tf":          "terraform {\n  backend \"s3\" {\n    bucket = \"literal\"\n    key    = \"literal.tfstate\"\n  }\n}\n",
				"stacks/a/.stackorder.yaml": "instances:\n  x:\n    backend_config: [cfg/two.tfbackend, \"region=eu-west-2\", \"use_lockfile=false\", \"unknown=1\"]\n",
			},
			want: []v1.Stack{{
				Key: "stacks/a:x", Path: "stacks/a", Instance: "x",
				Backend: &v1.Backend{
					Type: "s3", Bucket: "two", Key: "from-pair.tfstate", Region: "eu-west-2",
					DynamoDBTable: "locks", WorkspaceKeyPrefix: "envs",
				},
				Environment: "x", Tool: v1.ToolTerraform, PlanOutput: "full",
				Config: &v1.StackConfig{
					Instances: v1.Instances{"x": {BackendConfig: []string{"cfg/two.tfbackend", "region=eu-west-2", "use_lockfile=false", "unknown=1"}}},
				},
				WatchPaths: []string{"cfg/one.tfbackend", "cfg/two.tfbackend"},
			}},
			warnings: []string{},
		},
		{
			name: "non-literal file values and bad booleans are skipped with a warning",
			files: map[string]string{
				"stackorder.yaml":   "version: 1\nbackend_config: [cfg/bad.tfbackend, \"use_lockfile=maybe\"]\n",
				"cfg/bad.tfbackend": "bucket = \"b\"\nkey = var.key\nuse_lockfile = local.lock\n",
				"stacks/a/main.tf":  "terraform {\n  backend \"s3\" {\n    key = \"a.tfstate\"\n  }\n}\n",
			},
			want: []v1.Stack{{
				Key: "stacks/a", Path: "stacks/a", Backend: &v1.Backend{Type: "s3", Bucket: "b", Key: "a.tfstate"},
				Environment: v1.DefaultEnvironment, Tool: v1.ToolTerraform, PlanOutput: "full",
				WatchPaths: []string{"cfg/bad.tfbackend"},
			}},
			warnings: []string{
				`cfg/bad.tfbackend:2: backend config attribute "key" is not a literal value; skipped`,
				`cfg/bad.tfbackend:3: backend config attribute "use_lockfile" is not a literal value; skipped`,
				"stacks/a: backend_config use_lockfile=maybe is not a boolean; skipped",
			},
		},
		{
			name: "watch paths keep files outside the stack directory only",
			files: map[string]string{
				"stackorder.yaml":            "version: 1\nbackend_config: [stacks/a/own.tfbackend, shared/b.tfbackend, ./shared/b.tfbackend]\nvar_files: [../../shared/all.tfvars]\n",
				"shared/b.tfbackend":         "bucket = \"b\"\n",
				"shared/all.tfvars":          "",
				"shared/z.tfvars":            "",
				"stacks/a/main.tf":           partial,
				"stacks/a/own.tfbackend":     "key = \"a.tfstate\"\n",
				"stacks/a/local.tfvars":      "",
				"stacks/a/.stackorder.yaml":  "var_files: [local.tfvars, ../../shared/z.tfvars, ../../shared/all.tfvars, ../../../outside.tfvars]\n",
				"stacks/ab/main.tf":          partial,
				"stacks/ab/.stackorder.yaml": "backend_config: [\"key=ab.tfstate\"]\n",
			},
			want: []v1.Stack{
				{
					Key: "stacks/a", Path: "stacks/a", Backend: &v1.Backend{Type: "s3", Bucket: "b", Key: "a.tfstate"},
					Environment: v1.DefaultEnvironment, Tool: v1.ToolTerraform, PlanOutput: "full",
					Config:     &v1.StackConfig{VarFiles: []string{"local.tfvars", "../../shared/z.tfvars", "../../shared/all.tfvars", "../../../outside.tfvars"}},
					WatchPaths: []string{"shared/all.tfvars", "shared/b.tfbackend", "shared/z.tfvars"},
				},
				{
					Key: "stacks/ab", Path: "stacks/ab", Backend: &v1.Backend{Type: "s3", Bucket: "b", Key: "ab.tfstate"},
					Environment: v1.DefaultEnvironment, Tool: v1.ToolTerraform, PlanOutput: "full",
					Config:     &v1.StackConfig{BackendConfig: []string{"key=ab.tfstate"}},
					WatchPaths: []string{"shared/all.tfvars", "shared/b.tfbackend", "stacks/a/own.tfbackend"},
				},
			},
			warnings: []string{
				"stacks/a: var file ../../../outside.tfvars is outside the repository",
			},
		},
		{
			name: "a file inside a nested stack directory is a watch path of the enclosing stack",
			files: map[string]string{
				"stacks/a/main.tf":          partial,
				"stacks/a/.stackorder.yaml": "backend_config: [stacks/a/b/a.tfbackend, stacks/a/own.tfbackend]\nvar_files: [b/shared.tfvars, cfg/own.tfvars]\n",
				"stacks/a/own.tfbackend":    "bucket = \"s\"\n",
				"stacks/a/cfg/own.tfvars":   "",
				"stacks/a/b/main.tf":        partial,
				"stacks/a/b/a.tfbackend":    "key = \"a.tfstate\"\n",
				"stacks/a/b/shared.tfvars":  "",
			},
			want: []v1.Stack{
				{
					Key: "stacks/a", Path: "stacks/a", Backend: &v1.Backend{Type: "s3", Bucket: "s", Key: "a.tfstate"},
					Environment: v1.DefaultEnvironment, Tool: v1.ToolTerraform, PlanOutput: "full",
					Config: &v1.StackConfig{
						BackendConfig: []string{"stacks/a/b/a.tfbackend", "stacks/a/own.tfbackend"},
						VarFiles:      []string{"b/shared.tfvars", "cfg/own.tfvars"},
					},
					WatchPaths: []string{"stacks/a/b/a.tfbackend", "stacks/a/b/shared.tfvars"},
				},
				{
					Key: "stacks/a/b", Path: "stacks/a/b", Backend: &v1.Backend{Type: "s3"},
					Environment: v1.DefaultEnvironment, Tool: v1.ToolTerraform, PlanOutput: "full",
				},
			},
			warnings: []string{},
		},
		{
			name: "a JSON backend config file is parsed as HCL JSON",
			files: map[string]string{
				"stackorder.yaml":  "version: 1\nbackend_config: [cfg/backend.json]\n",
				"cfg/backend.json": `{"bucket": "b", "key": "k.tfstate", "use_lockfile": true, "profile": "ignored"}`,
				"stacks/a/main.tf": partial,
			},
			want: []v1.Stack{{
				Key: "stacks/a", Path: "stacks/a", Backend: &v1.Backend{Type: "s3", Bucket: "b", Key: "k.tfstate", UseLockfile: true},
				Environment: v1.DefaultEnvironment, Tool: v1.ToolTerraform, PlanOutput: "full",
				WatchPaths: []string{"cfg/backend.json"},
			}},
			warnings: []string{},
		},
		{
			name: "an included directory without an s3 backend reads its files but has no backend",
			files: map[string]string{
				"stackorder.yaml": "version: 1\nstacks:\n  include: [x]\nbackend_config: [cfg/a.tfbackend]\n",
				"cfg/a.tfbackend": "bucket = \"b\"\n",
				"x/main.tf":       "resource \"null_resource\" \"x\" {}\n",
			},
			want: []v1.Stack{{
				Key: "x", Path: "x",
				Environment: v1.DefaultEnvironment, Tool: v1.ToolTerraform, PlanOutput: "full",
				WatchPaths: []string{"cfg/a.tfbackend"},
			}},
			warnings: []string{"x: listed in stacks.include but has no s3 backend"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tt.files {
				writeFile(t, root, rel, content)
			}
			g, err := Scan(context.Background(), root, Options{Repo: fixtureRepo})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, g.Stacks); diff != "" {
				t.Errorf("stacks (-want +got):\n%s", diff)
			}
			warnings := g.Warnings
			if warnings == nil {
				warnings = []string{}
			}
			if diff := cmp.Diff(tt.warnings, warnings); diff != "" {
				t.Errorf("warnings (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScanBackendConfigFileErrors(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		contains []string
		target   error
	}{
		{
			name: "missing file",
			files: map[string]string{
				"stacks/a/main.tf":          "terraform {\n  backend \"s3\" {}\n}\n",
				"stacks/a/.stackorder.yaml": "instances:\n  x:\n    backend_config: [\"infra/{{ .Instance }}.tfbackend\"]\n",
			},
			contains: []string{"stacks/a:x", "infra/x.tfbackend"},
			target:   fs.ErrNotExist,
		},
		{
			name: "missing file for an included directory without a backend",
			files: map[string]string{
				"stackorder.yaml": "version: 1\nstacks:\n  include: [x]\nbackend_config: [cfg/missing.tfbackend]\n",
				"x/main.tf":       "resource \"null_resource\" \"x\" {}\n",
			},
			contains: []string{"x", "cfg/missing.tfbackend"},
			target:   fs.ErrNotExist,
		},
		{
			name: "file outside the repository",
			files: map[string]string{
				"stackorder.yaml":  "version: 1\nbackend_config: [../state.tfbackend]\n",
				"stacks/a/main.tf": "terraform {\n  backend \"s3\" {}\n}\n",
			},
			contains: []string{"stacks/a", "../state.tfbackend"},
			target:   errOutsideRepository,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tt.files {
				writeFile(t, root, rel, content)
			}
			g, err := Scan(context.Background(), root, Options{Repo: fixtureRepo})
			if err == nil {
				t.Fatalf("expected an error, got graph %+v", g)
			}
			for _, want := range tt.contains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
			if !errors.Is(err, tt.target) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.target)
			}
		})
	}
}
