package faketf

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const createPlan = `{"format_version":"1.2","resource_changes":[` +
	`{"address":"terraform_data.vpc","change":{"actions":["create"]}},` +
	`{"address":"terraform_data.old","change":{"actions":["delete","create"]}},` +
	`{"address":"data.x.y","change":{"actions":["read"]}}]}`

type fixture struct {
	t    *testing.T
	root string
	cfg  Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	show := filepath.Join(dir, "create.json")
	require.NoError(t, os.WriteFile(show, []byte(createPlan), 0o600))
	root := filepath.Join(dir, "repo")
	for _, d := range []string{"stacks/prod/vpc", "stacks/prod/apps", "other"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o750))
	}
	return &fixture{t: t, root: root, cfg: Config{
		Log:     filepath.Join(dir, "calls.jsonl"),
		Default: Behavior{PlanExit: 0, ShowJSON: show},
		Stacks: map[string]Behavior{
			"stacks/prod/vpc": {PlanExit: 2, ShowJSON: show, ApplyExit: 1},
			"vpc":             {PlanExit: 1},
		},
	}}
}

type result struct {
	code           int
	stdout, stderr string
}

func (f *fixture) run(dir string, args ...string) result {
	f.t.Helper()
	path := WriteConfig(f.t, f.cfg)
	env := map[string]string{EnvConfig: path}
	var stdout, stderr bytes.Buffer
	code := Run(args, func(k string) string { return env[k] }, filepath.Join(f.root, dir), &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestVersion(t *testing.T) {
	f := newFixture(t)
	r := f.run("other", "version", "-json")
	require.Equal(t, 0, r.code, r.stderr)
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &v))
	assert.Equal(t, DefaultVersion, v["terraform_version"])
	assert.Contains(t, v, "terraform_outdated", "terraform reports terraform_outdated")

	f.cfg.Tofu, f.cfg.Version = true, "1.12.0"
	r = f.run("other", "version", "-json")
	v = nil
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &v))
	assert.Equal(t, "1.12.0", v["terraform_version"])
	assert.NotContains(t, v, "terraform_outdated", "tofu does not")
}

func TestStackBehaviorByLongestSuffix(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		dir  string
		want int
	}{
		{"stacks/prod/vpc", 2},
		{"stacks/prod/apps", 0},
		{"other", 0},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "plan.tfplan")
			r := f.run(tt.dir, "plan", "-input=false", "-detailed-exitcode", "-out="+out)
			assert.Equal(t, tt.want, r.code, r.stderr)
			assert.FileExists(t, out, "a plan that does not fail writes its -out file")
		})
	}
	assert.Equal(t, 1, f.cfg.Stack("/x/vpc").PlanExit, "a shorter key matches a directory that ends with it")
}

func TestPlanFailure(t *testing.T) {
	f := newFixture(t)
	f.cfg.Default.PlanExit = 1
	out := filepath.Join(t.TempDir(), "plan.tfplan")
	r := f.run("other", "plan", "-detailed-exitcode", "-out="+out)
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "Error: ")
	assert.NoFileExists(t, out)
}

func TestShow(t *testing.T) {
	f := newFixture(t)
	plan := filepath.Join(t.TempDir(), "p.tfplan")
	require.NoError(t, os.WriteFile(plan, []byte("x"), 0o600))

	r := f.run("stacks/prod/vpc", "show", "-json", "-no-color", plan)
	require.Equal(t, 0, r.code, r.stderr)
	assert.JSONEq(t, createPlan, r.stdout)

	r = f.run("stacks/prod/vpc", "show", "-no-color", plan)
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "# terraform_data.vpc will be created")
	assert.Contains(t, r.stdout, "# terraform_data.old will be replaced")
	assert.NotContains(t, r.stdout, "data.x.y")

	r = f.run("stacks/prod/vpc", "show", "-json", filepath.Join(t.TempDir(), "missing"))
	assert.Equal(t, 1, r.code)
}

func TestApply(t *testing.T) {
	f := newFixture(t)
	plan := filepath.Join(t.TempDir(), "p.tfplan")
	require.NoError(t, os.WriteFile(plan, []byte("x"), 0o600))

	r := f.run("stacks/prod/apps", "apply", "-input=false", plan)
	assert.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "Apply complete!")

	r = f.run("stacks/prod/vpc", "apply", "-input=false", plan)
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "Error: ")

	r = f.run("stacks/prod/apps", "apply", filepath.Join(t.TempDir(), "missing"))
	assert.Equal(t, 1, r.code, "apply needs a saved plan")
}

func TestApplyWaitsForFile(t *testing.T) {
	f := newFixture(t)
	plan := filepath.Join(t.TempDir(), "p.tfplan")
	require.NoError(t, os.WriteFile(plan, []byte("x"), 0o600))
	gate := filepath.Join(t.TempDir(), "go")
	f.cfg.Default.ApplyWaitFile = gate
	done := make(chan result, 1)
	go func() { done <- f.run("other", "apply", plan) }()
	select {
	case r := <-done:
		t.Fatalf("apply returned before the file appeared: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, os.WriteFile(gate, nil, 0o600))
	select {
	case r := <-done:
		assert.Equal(t, 0, r.code, r.stderr)
	case <-time.After(10 * time.Second):
		t.Fatal("apply did not finish after the file appeared")
	}
}

func TestCallsAreRecorded(t *testing.T) {
	f := newFixture(t)
	f.run("stacks/prod/vpc", "init", "-input=false")
	f.run("other", "workspace", "select", "blue")
	calls := Calls(t, f.cfg.Log)
	require.Len(t, calls, 2)
	assert.Equal(t, []string{"init", "-input=false"}, calls[0].Args)
	assert.Equal(t, filepath.ToSlash(filepath.Join(f.root, "stacks/prod/vpc")), calls[0].Dir)
	assert.Equal(t, "workspace", calls[1].Args[0])
	assert.Empty(t, Calls(t, filepath.Join(t.TempDir(), "none")))
}

func TestMisconfigured(t *testing.T) {
	var stderr bytes.Buffer
	code := Run([]string{"version"}, func(string) string { return "" }, t.TempDir(), &bytes.Buffer{}, &stderr)
	assert.Equal(t, ExitMisconfigured, code)
	assert.Contains(t, stderr.String(), EnvConfig)
}

func TestBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go command is not on PATH")
	}
	dir := Build(t)
	cfg := WriteConfig(t, Config{Version: "1.14.2"})
	for _, name := range []string{"terraform", "tofu"} {
		cmd := exec.CommandContext(t.Context(), filepath.Join(dir, name), "version", "-json")
		cmd.Env = append(os.Environ(), EnvConfig+"="+cfg)
		out, err := cmd.Output()
		require.NoError(t, err, name)
		assert.Contains(t, string(out), `"terraform_version":"1.14.2"`, name)
	}
}
