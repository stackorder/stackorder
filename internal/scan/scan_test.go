package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const fixtureRepo = "acme/infra"

var ignoreTreeHash = cmpopts.IgnoreFields(v1.Graph{}, "TreeHash")

func usesModule(from v1.NodeRef, module, ref, source string) v1.Edge {
	return v1.Edge{
		From: from, To: v1.ModuleRef(module), Type: v1.EdgeUsesModule,
		Meta: map[string]string{"ref": ref, "source": source},
	}
}

func dependsOn(from, to string) v1.Edge {
	return v1.Edge{From: v1.StackRef(from), To: v1.StackRef(to), Type: v1.EdgeDependsOn}
}

func readsState(from, to, bucket, key string) v1.Edge {
	return v1.Edge{
		From: v1.StackRef(from), To: v1.StackRef(to), Type: v1.EdgeReadsState, Inferred: true,
		Meta: map[string]string{"bucket": bucket, "key": key},
	}
}

func s3Block(bucket, key string) string {
	return "terraform {\n  backend \"s3\" {\n    bucket = \"" + bucket + "\"\n    key    = \"" + key + "\"\n  }\n}\n"
}

func remoteStateBlock(name, bucket, key string) string {
	return "data \"terraform_remote_state\" \"" + name + "\" {\n  backend = \"s3\"\n  config = {\n    bucket = \"" + bucket + "\"\n    key    = \"" + key + "\"\n  }\n}\n"
}

func defaultStack(key string, backend *v1.Backend, cfg *v1.StackConfig) v1.Stack {
	p, ws := v1.SplitStackKey(key)
	return v1.Stack{Key: key, Path: p, Workspace: ws, Backend: backend, Tool: v1.ToolTerraform, PlanOutput: "full", Config: cfg}
}

func state(bucket, key string) *v1.Backend {
	return &v1.Backend{Type: "s3", Bucket: bucket, Key: key}
}

func TestScanCases(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		links  map[string]string
		config *v1.RepoConfig
		want   *v1.Graph
	}{
		{
			name: "missing stackorder.yaml uses the defaults",
			files: map[string]string{
				"stacks/a/main.tf": s3Block("b", "a.tfstate"),
				"infra/x/main.tf":  s3Block("b", "x.tfstate"),
			},
			want: &v1.Graph{Stacks: []v1.Stack{defaultStack("stacks/a", state("b", "a.tfstate"), nil)}},
		},
		{
			name: "options config replaces the file and is defaulted",
			files: map[string]string{
				"stackorder.yaml":  "version: 1\ntool: tofu\n",
				"stacks/a/main.tf": s3Block("b", "a.tfstate"),
				"infra/x/main.tf":  s3Block("b", "x.tfstate"),
			},
			config: &v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"infra/*"}}},
			want:   &v1.Graph{Stacks: []v1.Stack{defaultStack("infra/x", state("b", "x.tfstate"), nil)}},
		},
		{
			name: "tool, vendored and hidden files are skipped",
			files: map[string]string{
				".git/x/main.tf":                     s3Block("b", "git.tfstate"),
				"stacks/a/.terraform/modules/m/m.tf": s3Block("b", "cache.tfstate"),
				"node_modules/p/main.tf":             s3Block("b", "npm.tfstate"),
				"stacks/hidden/.backend.tf":          s3Block("b", "hidden.tfstate"),
				"stacks/a/main.tf":                   s3Block("b", "a.tfstate"),
			},
			config: &v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"**"}}},
			want:   &v1.Graph{Stacks: []v1.Stack{defaultStack("stacks/a", state("b", "a.tfstate"), nil)}},
		},
		{
			name: "directories below a module path are modules, not stacks",
			files: map[string]string{
				"modules/vpc/main.tf":     "variable \"cidr\" {}\n",
				"modules/vpc/sub/main.tf": s3Block("b", "sub.tfstate"),
				"stacks/a/main.tf":        s3Block("b", "a.tfstate") + "module \"vpc\" {\n  source = \"../../modules/vpc\"\n}\n",
			},
			config: &v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"**"}}, Modules: v1.ModulesConfig{Paths: []string{"modules/*"}}},
			want: &v1.Graph{
				Stacks: []v1.Stack{defaultStack("stacks/a", state("b", "a.tfstate"), nil)},
				Modules: []v1.Module{
					{Key: fixtureRepo + "//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc", Source: "../../modules/vpc"},
					{Key: fixtureRepo + "//modules/vpc/sub", Kind: v1.ModuleLocal, Path: "modules/vpc/sub", Source: "./modules/vpc/sub"},
				},
				Edges: []v1.Edge{usesModule(v1.StackRef("stacks/a"), fixtureRepo+"//modules/vpc", "", "../../modules/vpc")},
			},
		},
		{
			name: "shared state objects, self reads and unknown state",
			files: map[string]string{
				"stacks/a/main.tf": s3Block("b", "same.tfstate") + remoteStateBlock("self", "b", "same.tfstate"),
				"stacks/b/main.tf": s3Block("b", "same.tfstate"),
				"stacks/c/main.tf": s3Block("b", "c.tfstate") + remoteStateBlock("shared", "b", "same.tfstate") +
					remoteStateBlock("elsewhere", "other-bucket", "x.tfstate") +
					"data \"terraform_remote_state\" \"local\" {\n  backend = \"local\"\n}\n",
			},
			want: &v1.Graph{
				Stacks: []v1.Stack{
					defaultStack("stacks/a", state("b", "same.tfstate"), nil),
					defaultStack("stacks/b", state("b", "same.tfstate"), nil),
					defaultStack("stacks/c", state("b", "c.tfstate"), nil),
				},
				Edges: []v1.Edge{
					readsState("stacks/a", "stacks/b", "b", "same.tfstate"),
					readsState("stacks/c", "stacks/a", "b", "same.tfstate"),
					readsState("stacks/c", "stacks/b", "b", "same.tfstate"),
				},
				Warnings: []string{"stacks stacks/a, stacks/b share the state object s3://b/same.tfstate"},
			},
		},
		{
			name: "module calls that cannot be followed and spellings that collapse",
			files: map[string]string{
				"stacks/a/main.tf": s3Block("b", "a.tfstate") + `module "gone" {
  source = "./gone"
}
module "file" {
  source = "./main.tf"
}
module "nosrc" {
}
module "a_short" {
  source = "github.com/o/r//m?ref=v1"
}
module "b_long" {
  source = "git::https://github.com/o/r.git//m?ref=v1"
}
`,
			},
			want: &v1.Graph{
				Stacks:  []v1.Stack{defaultStack("stacks/a", state("b", "a.tfstate"), nil)},
				Modules: []v1.Module{{Key: "o/r//m@v1", Kind: v1.ModuleGit, Source: "git::https://github.com/o/r.git//m?ref=v1", Ref: "v1"}},
				Edges:   []v1.Edge{usesModule(v1.StackRef("stacks/a"), "o/r//m@v1", "v1", "git::https://github.com/o/r.git//m?ref=v1")},
				Warnings: []string{
					`stacks/a/main.tf:10: module "file": source "./main.tf" is not a directory; skipped`,
					`stacks/a/main.tf:13: module "nosrc" has no source; skipped`,
					`stacks/a/main.tf:7: module "gone": source "./gone" is not a directory; skipped`,
				},
			},
		},
		{
			name: "a stack used as a module keeps its module edges as a module too",
			files: map[string]string{
				"modules/m/main.tf": "variable \"x\" {}\n",
				"stacks/a/main.tf":  s3Block("b", "a.tfstate") + "module \"b\" {\n  source = \"../b\"\n}\n",
				"stacks/b/main.tf":  s3Block("b", "b.tfstate") + "module \"m\" {\n  source = \"../../modules/m\"\n}\n",
			},
			want: &v1.Graph{
				Stacks: []v1.Stack{
					defaultStack("stacks/a", state("b", "a.tfstate"), nil),
					defaultStack("stacks/b", state("b", "b.tfstate"), nil),
				},
				Modules: []v1.Module{
					{Key: fixtureRepo + "//modules/m", Kind: v1.ModuleLocal, Path: "modules/m", Source: "../../modules/m"},
					{Key: fixtureRepo + "//stacks/b", Kind: v1.ModuleLocal, Path: "stacks/b", Source: "../b"},
				},
				Edges: []v1.Edge{
					usesModule(v1.ModuleRef(fixtureRepo+"//stacks/b"), fixtureRepo+"//modules/m", "", "../../modules/m"),
					usesModule(v1.StackRef("stacks/a"), fixtureRepo+"//stacks/b", "", "../b"),
					usesModule(v1.StackRef("stacks/b"), fixtureRepo+"//modules/m", "", "../../modules/m"),
				},
			},
		},
		{
			name: "depends_on spellings and cross-repository workspaces",
			files: map[string]string{
				"stacks/a/main.tf":          s3Block("b", "a.tfstate") + remoteStateBlock("b", "b", "b.tfstate"),
				"stacks/a/.stackorder.yaml": "depends_on:\n  - acme/infra//stacks/b\n  - ./stacks/b/\n  - other/repo//stacks/x:blue\n  - other/repo//stacks/x:blue\n",
				"stacks/b/main.tf":          s3Block("b", "b.tfstate"),
				"stacks/b/.stackorder.yaml": "workspace: default\n",
			},
			want: &v1.Graph{
				Stacks: []v1.Stack{
					{Key: "other/repo//stacks/x:blue", Path: "stacks/x", Workspace: "blue", Repo: "other/repo", External: true},
					defaultStack("stacks/a", state("b", "a.tfstate"), &v1.StackConfig{
						DependsOn: []string{"acme/infra//stacks/b", "./stacks/b/", "other/repo//stacks/x:blue", "other/repo//stacks/x:blue"},
					}),
					defaultStack("stacks/b", state("b", "b.tfstate"), &v1.StackConfig{Workspace: "default"}),
				},
				Edges: []v1.Edge{
					dependsOn("stacks/a", "other/repo//stacks/x:blue"),
					dependsOn("stacks/a", "stacks/b"),
				},
			},
		},
		{
			name: "include entries",
			files: map[string]string{
				"stackorder.yaml": "version: 1\nstacks:\n  include: [\"missing/dir\", \"./tools/x/\"]\n",
				"tools/x/main.tf": s3Block("b", "x.tfstate"),
				"tools/y/main.tf": s3Block("b", "y.tfstate"),
			},
			want: &v1.Graph{
				Stacks:   []v1.Stack{defaultStack("tools/x", state("b", "x.tfstate"), nil)},
				Warnings: []string{"stacks.include: missing/dir: directory not found"},
			},
		},
		{
			name:  "the repository root can be included",
			files: map[string]string{"stackorder.yaml": "version: 1\nstacks:\n  include: [\".\"]\n"},
			want: &v1.Graph{
				Stacks:   []v1.Stack{defaultStack("", nil, nil)},
				Warnings: []string{".: listed in stacks.include but has no s3 backend"},
			},
		},
		{
			name:  "unreadable configuration files are reported",
			files: map[string]string{"stacks/a/main.tf": s3Block("b", "a.tfstate")},
			links: map[string]string{"stacks/a/dangling.tf": "missing.tf", "stacks/a/escape.tf": "../../../outside.tf"},
			want: &v1.Graph{
				Stacks: []v1.Stack{defaultStack("stacks/a", state("b", "a.tfstate"), nil)},
				Warnings: []string{
					"stacks/a/dangling.tf: no such file or directory",
					"stacks/a/escape.tf: path escapes from parent",
					"stacks/a: Failed to read file",
				},
			},
		},
		{
			name: "syntax errors are reported once per location",
			files: map[string]string{
				"stacks/a/main.tf":    "terraform {\n  backend \"s3\" {\n",
				"modules/bad/main.tf": "module \"x\" {\n  source = \n}\n",
			},
			want: &v1.Graph{
				Stacks: []v1.Stack{defaultStack("stacks/a", &v1.Backend{Type: "s3"}, nil)},
				Modules: []v1.Module{
					{Key: fixtureRepo + "//modules/bad", Kind: v1.ModuleLocal, Path: "modules/bad", Source: "./modules/bad"},
				},
				Warnings: []string{
					`modules/bad/main.tf:1: module "x" has no source; skipped`,
					"modules/bad/main.tf:2: Invalid expression",
					"modules/bad/main.tf:2: Unsuitable value type",
					"stacks/a/main.tf:2: Unclosed configuration block",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tt.files {
				writeFile(t, root, rel, content)
			}
			for rel, target := range tt.links {
				if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Scan(context.Background(), root, Options{Repo: fixtureRepo, SHA: "sha", Config: tt.config})
			if err != nil {
				t.Fatal(err)
			}
			want := *tt.want
			want.Repo, want.SHA = fixtureRepo, "sha"
			if want.Stacks == nil {
				want.Stacks = []v1.Stack{}
			}
			if want.Modules == nil {
				want.Modules = []v1.Module{}
			}
			if want.Edges == nil {
				want.Edges = []v1.Edge{}
			}
			if diff := cmp.Diff(&want, got, ignoreTreeHash); diff != "" {
				t.Errorf("Scan mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScanDoesNotMutateOptionsConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "infra/x/main.tf", s3Block("b", "x.tfstate"))
	cfg := &v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"infra/*"}}}
	if _, err := Scan(context.Background(), root, Options{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	want := &v1.RepoConfig{Stacks: v1.StacksConfig{Discover: []string{"infra/*"}}}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("Options.Config was mutated (-want +got):\n%s", diff)
	}
}

func TestTreeHashFormat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "stacks/a/main.tf", "a\n")
	writeFile(t, root, "stacks/a/.stackorder.yaml", "workspace: blue\n")
	writeFile(t, root, "stacks/a.tf.json", "{}")
	writeFile(t, root, "stackorder.yaml", "version: 1\n")
	writeFile(t, root, "notes.txt", "not scanned\n")
	g, err := Scan(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "9c1f335ef8adb44c1c363bb8ef20ef398afbbfb7ac40ee24861bf10bcab36e0b"
	if g.TreeHash != want {
		t.Errorf("TreeHash = %s, want %s", g.TreeHash, want)
	}
}

func TestScanErrors(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		root   func(root string) string
		ctx    func() context.Context
		target error
	}{
		{
			name:  "invalid root configuration",
			files: map[string]string{"stackorder.yaml": "version: 2\n"},
		},
		{
			name: "invalid stack configuration",
			files: map[string]string{
				"stacks/a/main.tf":          "terraform {\n  backend \"s3\" {}\n}\n",
				"stacks/a/.stackorder.yaml": "tool: pulumi\n",
			},
		},
		{
			name:   "root is a file",
			files:  map[string]string{"file.tf": "# x\n"},
			root:   func(root string) string { return filepath.Join(root, "file.tf") },
			target: ErrNotDirectory,
		},
		{
			name:   "root does not exist",
			root:   func(root string) string { return filepath.Join(root, "missing") },
			target: os.ErrNotExist,
		},
		{
			name:  "canceled context",
			files: map[string]string{"stacks/a/main.tf": "terraform {\n  backend \"s3\" {}\n}\n"},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			target: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tt.files {
				writeFile(t, root, rel, content)
			}
			if tt.root != nil {
				root = tt.root(root)
			}
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}
			g, err := Scan(ctx, root, Options{Repo: fixtureRepo})
			if err == nil {
				t.Fatalf("expected an error, got graph %+v", g)
			}
			if tt.target != nil && !errors.Is(err, tt.target) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.target)
			}
		})
	}
}
