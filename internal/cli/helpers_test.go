package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

type result struct {
	code   int
	err    error
	stdout string
	stderr string
}

var envPrefixes = []string{"GITHUB_", "ACTIONS_", "STACKORDER_", "RUNNER_", "TF_", "GIT_"}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range envPrefixes {
			if strings.HasPrefix(name, p) {
				t.Setenv(name, "")
				require.NoError(t, os.Unsetenv(name))
				break
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func parseOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}
	}
	require.NoError(t, err)
	out := map[string]string{}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if k, delim, ok := strings.Cut(lines[i], "<<"); ok && !strings.Contains(k, "=") {
			var value []string
			for i++; i < len(lines) && lines[i] != delim; i++ {
				value = append(value, lines[i])
			}
			out[k] = strings.Join(value, "\n")
			continue
		}
		k, v, _ := strings.Cut(lines[i], "=")
		out[k] = v
	}
	return out
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func initGit(t *testing.T, dir string) string {
	t.Helper()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "initial")
	return gitRun(t, dir, "rev-parse", "HEAD")
}

const (
	headSHA  = "1111111111111111111111111111111111111111"
	baseSHA  = "2222222222222222222222222222222222222222"
	mergeSHA = "3333333333333333333333333333333333333333"
)

func prPayload() map[string]any {
	return map[string]any{
		"pull_request": map[string]any{
			"number": 7,
			"head":   map[string]any{"sha": headSHA, "repo": map[string]any{"full_name": "acme/infra", "fork": false}},
			"base":   map[string]any{"sha": baseSHA, "repo": map[string]any{"full_name": "acme/infra"}},
		},
		"repository": map[string]any{"full_name": "acme/infra", "default_branch": "main"},
	}
}

func dispatchPayload(runID, sha string) map[string]any {
	return map[string]any{
		"inputs":     map[string]any{"run_id": runID, "sha": sha, "mode": "apply"},
		"repository": map[string]any{"full_name": "acme/infra", "default_branch": "main"},
	}
}

func sampleGraph() *v1.Graph {
	return &v1.Graph{
		Repo: "acme/infra",
		SHA:  headSHA,
		Stacks: []v1.Stack{
			{Key: "stacks/b", Path: "stacks/b"},
			{Key: "stacks/a", Path: "stacks/a"},
			{Key: "acme/network//stacks/tgw", Path: "stacks/tgw", Repo: "acme/network", External: true},
		},
		Modules: []v1.Module{{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc", Source: "../../modules/vpc"}},
		Edges: []v1.Edge{
			{From: v1.StackRef("stacks/b"), To: v1.StackRef("stacks/a"), Type: v1.EdgeDependsOn},
			{From: v1.StackRef("stacks/b"), To: v1.StackRef("stacks/a"), Type: v1.EdgeReadsState, Inferred: true},
			{From: v1.StackRef("stacks/a"), To: v1.ModuleRef("acme/infra//modules/vpc"), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "v1.2.0"}},
		},
		Warnings: []string{"stacks/c: backend bucket is not a literal"},
	}
}

func sampleResolve() *v1.ResolveResponse {
	return &v1.ResolveResponse{
		Affected: []v1.AffectedStack{
			{Key: "stacks/b", Path: "stacks/b", Wave: 1, Reasons: []v1.Reason{v1.ReasonDependent}, LockedBy: &v1.LockInfo{PRNumber: 3}},
			{Key: "stacks/a", Path: "stacks/a", Wave: 0, Reasons: []v1.Reason{v1.ReasonChanged, v1.ReasonModule}, Environment: "production"},
		},
		Waves: [][]string{{"stacks/a"}, {"stacks/b"}},
		Matrix: v1.Matrix{Include: []v1.MatrixEntry{
			{Stack: "stacks/a", Key: "stacks/a", Environment: "production", Wave: 0, Tool: v1.ToolTerraform, SHA: headSHA},
			{Stack: "stacks/b", Key: "stacks/b", Environment: "default", Wave: 1, Tool: v1.ToolTerraform, SHA: headSHA},
		}},
		Warnings: []string{"stacks/b is locked by PR #3"},
	}
}

type failingWriter struct{ after int }

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("disk full")
	}
	f.after--
	return len(p), nil
}

func newTestApp(w io.Writer) *app {
	return &app{stdout: w, stderr: w, log: slog.New(slog.NewTextHandler(w, nil)), format: formatText}
}

const cliTimeout = 3 * time.Minute

func runCLI(t *testing.T, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), cliTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	err := Run(ctx, args, &stdout, &stderr)
	if ctx.Err() != nil {
		t.Fatalf("stackorder %s did not finish within %s: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), cliTimeout, err, stdout.String(), stderr.String())
	}
	return result{code: ExitCode(err), err: err, stdout: stdout.String(), stderr: stderr.String()}
}

func deadURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}
