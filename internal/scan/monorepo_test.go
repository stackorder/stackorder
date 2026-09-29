package scan

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func acmeState(key string) *v1.Backend {
	return &v1.Backend{Type: "s3", Bucket: "acme-tfstate", Key: key, Region: "eu-west-1", UseLockfile: true}
}

func lockedState(key string) *v1.Backend {
	return &v1.Backend{Type: "s3", Bucket: "acme-tfstate", Key: key, Region: "eu-west-1", DynamoDBTable: "tf-locks"}
}

func fixtureStack(key, env string, backend *v1.Backend, cfg *v1.StackConfig) v1.Stack {
	p, instance := v1.SplitStackKey(key)
	return v1.Stack{
		Key: key, Path: p, Instance: instance, Workspace: instance, Backend: backend,
		Environment: fallbackEnvironment(env, instance),
		Tool:        v1.ToolTofu, ToolVersion: "1.9.0", PlanOutput: "full", Config: cfg,
	}
}

func monorepoGraph() *v1.Graph {
	apps := fixtureStack("stacks/prod/apps", "production", acmeState("prod/apps.tfstate"), &v1.StackConfig{
		PlanOutput:     v1.PlanOutputSummary,
		Apply:          &v1.StackApplyConfig{AllowedTeams: []string{"platform-prod"}},
		IgnoreInferred: []string{"stacks/legacy/dns"},
	})
	apps.PlanOutput = "summary"
	eks := fixtureStack("stacks/prod/eks", "production", acmeState("prod/eks.tfstate"), &v1.StackConfig{
		DependsOn: []string{"stacks/prod/vpc"}, Tool: v1.ToolTerraform, ToolVersion: "1.14.0",
	})
	eks.Tool, eks.ToolVersion = v1.ToolTerraform, "1.14.0"
	network := acmeState("prod/network.tfstate")
	network.WorkspaceKeyPrefix = "ws"

	return &v1.Graph{
		Repo: fixtureRepo,
		SHA:  "0123456789abcdef0123456789abcdef01234567",
		Stacks: []v1.Stack{
			{Key: "acme/network-infra//stacks/prod/tgw", Path: "stacks/prod/tgw", Repo: "acme/network-infra", External: true},
			fixtureStack("legacy/bootstrap", "", nil, nil),
			fixtureStack("stacks/legacy/dns", "", acmeState("legacy/dns.tfstate"), nil),
			apps,
			eks,
			fixtureStack("stacks/prod/network:blue", "production-blue", network, &v1.StackConfig{
				Workspace: "blue", Environment: "production-blue",
			}),
			fixtureStack("stacks/prod/vpc", "production", acmeState("prod/vpc.tfstate"), &v1.StackConfig{
				DependsOn: []string{"acme/network-infra//stacks/prod/tgw"},
			}),
			fixtureStack("stacks/prod/vpc/peering", "production", acmeState("prod/vpc-peering.tfstate"), &v1.StackConfig{
				DependsOn: []string{"stacks/prod/vpc"},
			}),
			fixtureStack("stacks/staging/dynamic", "staging", &v1.Backend{Type: "s3", Bucket: "acme-tfstate", Region: "eu-west-1"}, nil),
			fixtureStack("stacks/staging/eks", "staging", lockedState("staging/eks.tfstate"), &v1.StackConfig{
				DependsOn: []string{"stacks/staging/vpc", "stacks/staging/missing"},
			}),
			fixtureStack("stacks/staging/vpc", "staging", lockedState("staging/vpc.tfstate"), nil),
		},
		Modules: []v1.Module{
			{Key: "acme/infra//modules/common", Kind: v1.ModuleLocal, Path: "modules/common", Source: "../common"},
			{Key: "acme/infra//modules/eks", Kind: v1.ModuleLocal, Path: "modules/eks", Source: "../../../modules/eks"},
			{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc", Source: "../../../modules/vpc"},
			{Key: "acme/tf-modules//alb@v1.2.0", Kind: v1.ModuleGit, Source: "git::https://github.com/acme/tf-modules.git//alb?ref=v1.2.0", Ref: "v1.2.0"},
			{Key: "acme/tf-modules//dns@v1.3.0", Kind: v1.ModuleGit, Source: "git@github.com:acme/tf-modules.git//dns?ref=v1.3.0", Ref: "v1.3.0"},
			{Key: "acme/tf-modules//flowlogs@v1.2.0", Kind: v1.ModuleGit, Source: "github.com/acme/tf-modules//flowlogs?ref=v1.2.0", Ref: "v1.2.0"},
			{Key: "gitlab.com/acme/shared//cdn@v0.4.0", Kind: v1.ModuleGit, Source: "git::ssh://git@gitlab.com/acme/shared.git//cdn?ref=v0.4.0", Ref: "v0.4.0"},
			{Key: "registry:terraform-aws-modules/security-group/aws@5.1.0", Kind: v1.ModuleRegistry, Source: "terraform-aws-modules/security-group/aws", Ref: "5.1.0"},
		},
		Edges: []v1.Edge{
			usesModule(v1.ModuleRef("acme/infra//modules/eks"), "acme/infra//modules/common", "", "../common"),
			readsState("stacks/prod/apps", "stacks/prod/eks", "acme-tfstate", "prod/eks.tfstate"),
			readsState("stacks/prod/apps", "stacks/prod/network:blue", "acme-tfstate", "ws/blue/prod/network.tfstate"),
			usesModule(v1.StackRef("stacks/prod/apps"), "acme/tf-modules//alb@v1.2.0", "v1.2.0", "git::https://github.com/acme/tf-modules.git//alb?ref=v1.2.0"),
			usesModule(v1.StackRef("stacks/prod/apps"), "acme/tf-modules//dns@v1.3.0", "v1.3.0", "git@github.com:acme/tf-modules.git//dns?ref=v1.3.0"),
			usesModule(v1.StackRef("stacks/prod/apps"), "gitlab.com/acme/shared//cdn@v0.4.0", "v0.4.0", "git::ssh://git@gitlab.com/acme/shared.git//cdn?ref=v0.4.0"),
			usesModule(v1.StackRef("stacks/prod/apps"), "registry:terraform-aws-modules/security-group/aws@5.1.0", "5.1.0", "terraform-aws-modules/security-group/aws"),
			dependsOn("stacks/prod/eks", "stacks/prod/vpc"),
			usesModule(v1.StackRef("stacks/prod/eks"), "acme/infra//modules/eks", "", "../../../modules/eks"),
			dependsOn("stacks/prod/vpc", "acme/network-infra//stacks/prod/tgw"),
			usesModule(v1.StackRef("stacks/prod/vpc"), "acme/infra//modules/vpc", "", "../../../modules/vpc"),
			dependsOn("stacks/prod/vpc/peering", "stacks/prod/vpc"),
			dependsOn("stacks/staging/eks", "stacks/staging/missing"),
			dependsOn("stacks/staging/eks", "stacks/staging/vpc"),
			usesModule(v1.StackRef("stacks/staging/eks"), "acme/infra//modules/eks", "", "../../../modules/eks"),
			usesModule(v1.StackRef("stacks/staging/vpc"), "acme/infra//modules/vpc", "", "../../../modules/vpc"),
			usesModule(v1.StackRef("stacks/staging/vpc"), "acme/tf-modules//flowlogs@v1.2.0", "v1.2.0", "github.com/acme/tf-modules//flowlogs?ref=v1.2.0"),
		},
		Warnings: []string{
			"legacy/bootstrap: listed in stacks.include but has no s3 backend",
			`stacks/legacy/dns/main.tf:10: module "outside": source "../../../../outside" is outside the repository; skipped`,
			`stacks/legacy/dns/main.tf:14: module "archive": unsupported source "s3::https://s3-eu-west-1.amazonaws.com/acme-modules/dns.zip"; skipped`,
			"stacks/prod/apps: inferred reads_state edge to stacks/legacy/dns suppressed by ignore_inferred",
			`stacks/sandbox/gcs: backend "gcs" is not s3; not a stack`,
			`stacks/staging/dynamic/main.tf:9: backend "s3" attribute "key" is not a literal value; left empty`,
			"stacks/staging/eks/main.tf:18: data.terraform_remote_state.vpc: cannot infer reads_state edge: config.bucket is not a literal value",
			"stacks/staging/eks: depends_on stacks/staging/missing: no such stack in this repository",
		},
	}
}

func scanFixture(t *testing.T, root string) *v1.Graph {
	t.Helper()
	g, err := Scan(context.Background(), root, Options{Repo: fixtureRepo, SHA: "0123456789abcdef0123456789abcdef01234567"})
	if err != nil {
		t.Fatalf("Scan(%s): %v", root, err)
	}
	return g
}

func copyFixture(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("testdata", "monorepo"))); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestScanMonorepo(t *testing.T) {
	got := scanFixture(t, filepath.Join("testdata", "monorepo"))
	if diff := cmp.Diff(monorepoGraph(), got, ignoreTreeHash); diff != "" {
		t.Errorf("Scan mismatch (-want +got):\n%s", diff)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got.TreeHash) {
		t.Errorf("TreeHash = %q, want 64 hex characters", got.TreeHash)
	}
}

func TestScanTreeHash(t *testing.T) {
	base := scanFixture(t, filepath.Join("testdata", "monorepo")).TreeHash
	if again := scanFixture(t, filepath.Join("testdata", "monorepo")).TreeHash; again != base {
		t.Fatalf("TreeHash is not stable across scans: %s then %s", base, again)
	}
	tests := []struct {
		name    string
		mutate  func(t *testing.T, root string)
		changed bool
	}{
		{name: "identical copy elsewhere", mutate: func(*testing.T, string) {}},
		{
			name:    "stack .tf content",
			mutate:  func(t *testing.T, root string) { appendFile(t, root, "stacks/prod/vpc/main.tf", "\n# edit\n") },
			changed: true,
		},
		{
			name:    "module .tf content",
			mutate:  func(t *testing.T, root string) { appendFile(t, root, "modules/common/main.tf", "\n# edit\n") },
			changed: true,
		},
		{
			name: ".tf.json content",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "stacks/staging/eks/eks.tf.json", `{"module": {}}`)
			},
			changed: true,
		},
		{
			name:    "stack .stackorder.yaml",
			mutate:  func(t *testing.T, root string) { appendFile(t, root, "stacks/prod/vpc/.stackorder.yaml", "# edit\n") },
			changed: true,
		},
		{
			name:    "root stackorder.yaml",
			mutate:  func(t *testing.T, root string) { appendFile(t, root, "stackorder.yaml", "# edit\n") },
			changed: true,
		},
		{
			name:    "new .tf in a directory that is not a stack",
			mutate:  func(t *testing.T, root string) { writeFile(t, root, "docs/example/main.tf", "# example\n") },
			changed: true,
		},
		{
			name: "renamed .tf file",
			mutate: func(t *testing.T, root string) {
				if err := os.Rename(filepath.Join(root, "stacks/prod/apps/modules.tf"), filepath.Join(root, "stacks/prod/apps/mods.tf")); err != nil {
					t.Fatal(err)
				}
			},
			changed: true,
		},
		{
			name:   "README",
			mutate: func(t *testing.T, root string) { appendFile(t, root, "README.md", "more\n") },
		},
		{
			name:    "a .tfvars file",
			mutate:  func(t *testing.T, root string) { writeFile(t, root, "stacks/prod/vpc/prod.tfvars", "x = 1\n") },
			changed: true,
		},
		{
			name:    "a .tfvars.json file",
			mutate:  func(t *testing.T, root string) { writeFile(t, root, "vars/prod.tfvars.json", "{}\n") },
			changed: true,
		},
		{
			name:    "a .tfbackend file",
			mutate:  func(t *testing.T, root string) { writeFile(t, root, "infra/state.s3.tfbackend", "bucket = \"b\"\n") },
			changed: true,
		},
		{
			name:   "lock files",
			mutate: func(t *testing.T, root string) { writeFile(t, root, "stacks/prod/vpc/.terraform.lock.hcl", "# lock\n") },
		},
		{
			name: "files under .terraform, node_modules and .git",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "stacks/prod/vpc/.terraform/modules/vpc/main.tf", "# cached\n")
				writeFile(t, root, "node_modules/pkg/main.tf", "# vendored\n")
				writeFile(t, root, ".git/info/main.tf", "# git internals\n")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := copyFixture(t)
			tt.mutate(t, root)
			got := scanFixture(t, root).TreeHash
			if changed := got != base; changed != tt.changed {
				t.Errorf("TreeHash changed = %v, want %v (base %s, got %s)", changed, tt.changed, base, got)
			}
		})
	}
}

func appendFile(t *testing.T, root, rel, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestScanLogsToTheGivenLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := Scan(context.Background(), filepath.Join("testdata", "monorepo"), Options{Repo: fixtureRepo, Logger: logger}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`msg="stack discovered" key=stacks/prod/vpc backend=s3`, `msg="scan warning"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log output does not contain %q:\n%s", want, buf.String())
		}
	}
}

func TestStackDirs(t *testing.T) {
	tests := []struct {
		name string
		g    *v1.Graph
		want []string
	}{
		{name: "nil graph", g: nil, want: nil},
		{name: "empty graph", g: &v1.Graph{}, want: []string{}},
		{
			name: "monorepo excludes external stacks",
			g:    monorepoGraph(),
			want: []string{
				"legacy/bootstrap",
				"stacks/legacy/dns",
				"stacks/prod/apps",
				"stacks/prod/eks",
				"stacks/prod/network",
				"stacks/prod/vpc",
				"stacks/prod/vpc/peering",
				"stacks/staging/dynamic",
				"stacks/staging/eks",
				"stacks/staging/vpc",
			},
		},
		{
			name: "directories are de-duplicated",
			g:    &v1.Graph{Stacks: []v1.Stack{{Key: "a:blue", Path: "a"}, {Key: "a", Path: "a"}}},
			want: []string{"a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, StackDirs(tt.g)); diff != "" {
				t.Errorf("StackDirs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFindStack(t *testing.T) {
	g := monorepoGraph()
	tests := []struct {
		key  string
		want string
	}{
		{key: "stacks/prod/vpc", want: "stacks/prod/vpc"},
		{key: "./stacks/prod/vpc/", want: "stacks/prod/vpc"},
		{key: "acme/infra//stacks/prod/vpc", want: "stacks/prod/vpc"},
		{key: "ACME/Infra//stacks/prod/vpc", want: "stacks/prod/vpc"},
		{key: "stacks/prod/network:blue", want: "stacks/prod/network:blue"},
		{key: "acme/network-infra//stacks/prod/tgw", want: "acme/network-infra//stacks/prod/tgw"},
		{key: "stacks/prod/tgw"},
		{key: "stacks/prod/network"},
		{key: "other/repo//stacks/prod/vpc"},
		{key: ""},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := FindStack(g, tt.key)
			switch {
			case tt.want == "" && got != nil:
				t.Errorf("FindStack(%q) = %q, want nil", tt.key, got.Key)
			case tt.want != "" && (got == nil || got.Key != tt.want):
				t.Errorf("FindStack(%q) = %v, want %q", tt.key, got, tt.want)
			}
		})
	}
	if got := FindStack(nil, "stacks/prod/vpc"); got != nil {
		t.Errorf("FindStack(nil) = %v, want nil", got)
	}
	if st := FindStack(g, "stacks/prod/vpc"); st != &g.Stacks[6] {
		t.Errorf("FindStack returned a copy, want a pointer into g.Stacks")
	}
}
