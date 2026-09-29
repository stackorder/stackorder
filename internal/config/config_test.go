package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestParseMinimal(t *testing.T) {
	c, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	if c.Tool != want.Tool || c.Apply.Mode != want.Apply.Mode || c.PlanOutput != want.PlanOutput {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if got := c.Stacks.Discover; len(got) != 1 || got[0] != "stacks/**" {
		t.Fatalf("discover = %v", got)
	}
	if !c.Apply.FromPlanEnabled() || !c.Propagate.DependentsEnabled() {
		t.Fatal("boolean defaults should be true")
	}
	if c.Apply.MaxParallel != 6 || c.Propagate.CrossRepo != v1.CrossRepoOff {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestParseFull(t *testing.T) {
	src := `
version: 1
stacks:
  discover: ["stacks/**", "infra/*"]
  ignore: ["**/*.md"]
  ignore_lockfile: true
modules:
  paths: ["modules/**"]
tool: tofu
tool_version: "1.9.0"
environments:
  "stacks/prod/": production
  "stacks/staging/": staging
apply:
  mode: on_merge
  require_approvals: 2
  require_codeowner_review: true
  allowed_teams: [platform-eng]
  four_eyes: true
  from_plan: false
  max_parallel: 3
propagate:
  dependents: false
  cross_repo: plan
drift:
  schedule: "0 6 * * 1-5"
  open_issue: true
plan_output: summary
`
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if c.Tool != v1.ToolTofu || c.ToolVersion != "1.9.0" {
		t.Fatalf("tool = %s %s", c.Tool, c.ToolVersion)
	}
	if c.Apply.Mode != v1.ApplyOnMerge || c.Apply.RequireApprovals != 2 || !c.Apply.FourEyes || c.Apply.FromPlanEnabled() || c.Apply.MaxParallel != 3 {
		t.Fatalf("apply = %+v", c.Apply)
	}
	if c.Propagate.DependentsEnabled() || c.Propagate.CrossRepo != v1.CrossRepoPlan {
		t.Fatalf("propagate = %+v", c.Propagate)
	}
	if c.Drift.Schedule != "0 6 * * 1-5" || !c.Drift.OpenIssue {
		t.Fatalf("drift = %+v", c.Drift)
	}
	if c.PlanOutput != v1.PlanOutputSummary || !c.Stacks.IgnoreLockfile {
		t.Fatalf("plan_output = %s", c.PlanOutput)
	}
	if got := IgnoreGlobs(c); len(got) != 2 || got[1] != "**/.terraform.lock.hcl" {
		t.Fatalf("ignore globs = %v", got)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"":                                   "file is empty",
		"version: 2\n":                       "version",
		"version: 1\ntool: pulumi\n":         "tool",
		"version: 1\napply:\n  mode: yolo\n": "apply.mode",
		"version: 1\nplan_output: none\n":    "plan_output",
		"version: 1\npropagate:\n  cross_repo: apply\n": "propagate.cross_repo",
		"version: 1\ndrift:\n  schedule: \"nope\"\n":    "drift.schedule",
		"version: 1\nunknown_key: true\n":               "unknown_key",
		"version: 1\napply:\n  max_parallel: -1\n":      "max_parallel",
		"version: 1\napply:\n  require_approvals: -1\n": "require_approvals",
		"version: 1\nenvironments:\n  \"\": prod\n":     "empty path prefix",
	}
	for src, want := range cases {
		_, err := Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error = %v, want containing %q", src, err, want)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	c, ok, err := Load(dir)
	if err != nil || ok {
		t.Fatalf("Load = %v, %v", ok, err)
	}
	if c.Version != 1 {
		t.Fatal("expected defaults")
	}
	if err := os.WriteFile(filepath.Join(dir, RootFile), []byte("version: 1\ntool: tofu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, ok, err = Load(dir)
	if err != nil || !ok || c.Tool != v1.ToolTofu {
		t.Fatalf("Load = %+v, %v, %v", c, ok, err)
	}
}

func TestParseStack(t *testing.T) {
	src := `
depends_on:
  - stacks/prod/vpc
  - acme/network-infra//stacks/prod/tgw
workspace: blue
tool: terraform
environment: production
apply:
  allowed_teams: [platform-prod]
plan_output: summary
ignore_inferred: [stacks/legacy/dns]
`
	s, err := ParseStack([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.DependsOn) != 2 || s.Workspace != "blue" || s.Environment != "production" {
		t.Fatalf("stack = %+v", s)
	}
	if s.Apply == nil || len(s.Apply.AllowedTeams) != 1 {
		t.Fatalf("apply = %+v", s.Apply)
	}
	if _, err := ParseStack([]byte("depends_on: [\"/abs\"]\n")); err == nil {
		t.Fatal("expected error for absolute dependency")
	}
	if _, err := ParseStack([]byte("depends_on: [\"acme//x\"]\n")); err == nil {
		t.Fatal("expected error for malformed repo")
	}
	if _, err := ParseStack([]byte("workspace: \"a b\"\n")); err == nil {
		t.Fatal("expected error for workspace with space")
	}
	empty, err := ParseStack(nil)
	if err != nil || empty == nil {
		t.Fatalf("empty stack config: %v", err)
	}
}

func TestParseDependency(t *testing.T) {
	repo, key, err := ParseDependency("acme/network-infra//stacks/prod/tgw")
	if err != nil || repo != "acme/network-infra" || key != "stacks/prod/tgw" {
		t.Fatalf("got %q %q %v", repo, key, err)
	}
	repo, key, err = ParseDependency("./stacks/prod/vpc/")
	if err != nil || repo != "" || key != "stacks/prod/vpc" {
		t.Fatalf("got %q %q %v", repo, key, err)
	}
	repo, key, err = ParseDependency("stacks/prod/vpc:blue")
	if err != nil || repo != "" || key != "stacks/prod/vpc:blue" {
		t.Fatalf("got %q %q %v", repo, key, err)
	}
	if _, _, err := ParseDependency("../escape"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStackKeyRoundTrip(t *testing.T) {
	if k := v1.StackKey("./stacks/a/", ""); k != "stacks/a" {
		t.Fatal(k)
	}
	if k := v1.StackKey("stacks/a", "default"); k != "stacks/a" {
		t.Fatal(k)
	}
	p, ws := v1.SplitStackKey(v1.StackKey("stacks/a", "blue"))
	if p != "stacks/a" || ws != "blue" {
		t.Fatal(p, ws)
	}
	repo, key := v1.SplitQualifiedStackKey(v1.QualifiedStackKey("acme/infra", "stacks/a"))
	if repo != "acme/infra" || key != "stacks/a" {
		t.Fatal(repo, key)
	}
}
