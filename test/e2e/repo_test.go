//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

const (
	envExampleDir = "STACKORDER_E2E_EXAMPLE_INFRA_DIR"
	envExampleURL = "STACKORDER_E2E_EXAMPLE_INFRA_URL"
	gitTimeout    = 2 * time.Minute
)

var toolSetting = regexp.MustCompile(`(?m)^tool:.*$`)

func exampleSource(t *testing.T) string {
	t.Helper()
	if u := os.Getenv(envExampleURL); u != "" {
		dir := filepath.Join(t.TempDir(), "example-infra")
		runGit(t, "", "clone", "--quiet", "--depth", "1", u, dir)
		return dir
	}
	if d := os.Getenv(envExampleDir); d != "" {
		abs, err := filepath.Abs(d)
		require.NoError(t, err)
		requireExample(t, abs)
		return abs
	}
	root := strings.TrimSpace(runGit(t, "", "rev-parse", "--show-toplevel"))
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "example-infra")
		if isExample(candidate) {
			return candidate
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	t.Fatalf("e2e: no example-infra checkout next to %s; set %s to a checkout of stackorder/example-infra or %s to a URL to clone it from", root, envExampleDir, envExampleURL)
	return ""
}

func isExample(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "stackorder.yaml"))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	info, err = os.Stat(filepath.Join(dir, "stacks"))
	return err == nil && info.IsDir()
}

func requireExample(t *testing.T, dir string) {
	t.Helper()
	require.True(t, isExample(dir), "%s holds stackorder.yaml and stacks/", dir)
}

func exampleFiles(t *testing.T, src string) []string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
		out := runGit(t, src, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		var files []string
		for _, f := range strings.Split(out, "\x00") {
			if f == "" {
				continue
			}
			if info, err := os.Lstat(filepath.Join(src, f)); err == nil && info.Mode().IsRegular() {
				files = append(files, f)
			}
		}
		return files
	}
	var files []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		switch {
		case d.IsDir() && (d.Name() == ".git" || d.Name() == ".terraform"):
			return filepath.SkipDir
		case d.Type().IsRegular() && !strings.HasSuffix(rel, ".tfstate") && !strings.HasSuffix(rel, ".tfplan"):
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	require.NoError(t, err)
	return files
}

type gitRepo struct {
	dir string
}

func newExampleRepo(t *testing.T, src string, tool v1.Tool) (*gitRepo, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "origin")
	for _, f := range exampleFiles(t, src) {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(f)))
		require.NoError(t, err)
		info, err := os.Stat(filepath.Join(src, filepath.FromSlash(f)))
		require.NoError(t, err)
		dst := filepath.Join(dir, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o750))
		require.NoError(t, os.WriteFile(dst, data, info.Mode().Perm()))
	}
	cfgPath := filepath.Join(dir, "stackorder.yaml")
	cfg, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Regexp(t, toolSetting, string(cfg), "the example's stackorder.yaml sets tool")
	require.NoError(t, os.WriteFile(cfgPath, toolSetting.ReplaceAll(cfg, []byte("tool: "+string(tool))), 0o600))
	r := &gitRepo{dir: dir}
	r.git(t, "init", "--quiet", "--initial-branch=main")
	r.git(t, "add", "--all")
	r.git(t, "commit", "--quiet", "--message", "chore: import example-infra for "+string(tool))
	r.git(t, "checkout", "--quiet", "--detach")
	return r, r.head(t)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(cleanEnv(),
		"GIT_AUTHOR_NAME=Stackorder E2E", "GIT_AUTHOR_EMAIL=e2e@stackorder.invalid",
		"GIT_COMMITTER_NAME=Stackorder E2E", "GIT_COMMITTER_EMAIL=e2e@stackorder.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "git %s: %s", strings.Join(args, " "), stderr.String())
	return stdout.String()
}

func (r *gitRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	return runGit(t, r.dir, args...)
}

func (r *gitRepo) head(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
}

type edit func(t *testing.T, dir string)

func writeFile(path, content string) edit {
	return func(t *testing.T, dir string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
}

func appendFile(path, content string) edit {
	return func(t *testing.T, dir string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(path))
		old, err := os.ReadFile(p)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, append(old, []byte(content)...), 0o600))
	}
}

func replaceIn(path, old, replacement string) edit {
	return func(t *testing.T, dir string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(path))
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		require.Contains(t, string(data), old, "%s holds the text to replace", path)
		require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(data), old, replacement, 1)), 0o600))
	}
}

func (r *gitRepo) commit(t *testing.T, branch, from, message string, edits ...edit) string {
	t.Helper()
	r.git(t, "checkout", "--quiet", "-B", branch, from)
	for _, e := range edits {
		e(t, r.dir)
	}
	r.git(t, "add", "--all")
	r.git(t, "commit", "--quiet", "--message", message)
	head := r.head(t)
	r.git(t, "checkout", "--quiet", "--detach")
	return head
}

func (r *gitRepo) setBranch(t *testing.T, branch, sha string) {
	t.Helper()
	r.git(t, "branch", "--force", branch, sha)
}

func (r *gitRepo) mergeCommit(t *testing.T, base, head string, pr int) string {
	t.Helper()
	tree := strings.TrimSpace(r.git(t, "merge-tree", "--write-tree", base, head))
	return strings.TrimSpace(r.git(t, "commit-tree", tree, "-p", base, "-p", head, "-m", fmt.Sprintf("Merge %s into %s for #%d", head, base, pr)))
}

func (r *gitRepo) changedPaths(t *testing.T, base, head string) []string {
	t.Helper()
	var out []string
	for _, p := range strings.Split(strings.TrimSpace(r.git(t, "diff", "--name-only", base, head)), "\n") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r *gitRepo) checkout(t *testing.T, sha string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "workspace")
	runGit(t, "", "clone", "--quiet", "--shared", "--no-checkout", r.dir, dir)
	runGit(t, dir, "checkout", "--quiet", "--detach", sha)
	return dir
}

func (r *gitRepo) mirror(t *testing.T, fake *ghfake.Server, repo, sha string) {
	t.Helper()
	for _, f := range strings.Split(r.git(t, "ls-tree", "-r", "-z", "--name-only", sha), "\x00") {
		if f == "" {
			continue
		}
		fake.SetContents(repo, sha, f, []byte(r.git(t, "show", sha+":"+f)))
	}
}
