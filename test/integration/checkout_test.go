//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	exampleDir = "testdata/example-infra"
	plansDir   = "testdata/plans"
	vpcSubnets = `["a", "b", "c"]`
)

type checkout struct {
	t    *testing.T
	dir  string
	base string
	head string
}

func newCheckout(t *testing.T, repo string, edit func(root string)) *checkout {
	t.Helper()
	root := filepath.Join(t.TempDir(), strings.ReplaceAll(repo, "/", "_"))
	require.NoError(t, os.CopyFS(root, os.DirFS(exampleDir)))
	if edit != nil {
		edit(root)
	}
	c := &checkout{t: t, dir: root}
	c.git("init", "-q", "-b", "main")
	c.git("remote", "add", "origin", "https://github.com/"+repo+".git")
	c.git("add", "-A")
	c.git("commit", "-q", "-m", "chore: import the example monorepo")
	c.base = c.git("rev-parse", "HEAD")
	c.git("checkout", "-q", "-b", "feature/vpc-subnet")
	c.head = c.edit("modules/vpc/main.tf", "feat(vpc): add a fourth subnet", func(s string) string {
		out := strings.Replace(s, vpcSubnets, `["a", "b", "c", "d"]`, 1)
		require.NotEqual(t, s, out, "modules/vpc/main.tf lists the subnet zones as %s", vpcSubnets)
		return out
	})
	return c
}

func (c *checkout) git(args ...string) string {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	full := make([]string, 0, 6+len(args))
	full = append(full, "-c", "user.name=Integration", "-c", "user.email=integration@example.com", "-c", "commit.gpgsign=false")
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(c.t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func (c *checkout) edit(path, msg string, change func(string) string) string {
	c.t.Helper()
	file := filepath.Join(c.dir, filepath.FromSlash(path))
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		require.NoError(c.t, err)
	}
	require.NoError(c.t, os.MkdirAll(filepath.Dir(file), 0o750))
	require.NoError(c.t, os.WriteFile(file, []byte(change(string(data))), 0o600))
	c.git("add", "-A")
	c.git("commit", "-q", "-m", msg)
	return c.git("rev-parse", "HEAD")
}

func (c *checkout) emptyCommit(msg string) string {
	c.t.Helper()
	c.git("commit", "-q", "--allow-empty", "-m", msg)
	return c.git("rev-parse", "HEAD")
}

func (c *checkout) branch(name, from string) {
	c.t.Helper()
	c.git("checkout", "-q", "-b", name, from)
}

func (c *checkout) at(sha string) {
	c.t.Helper()
	c.git("checkout", "-q", "--detach", sha)
}

func (c *checkout) merge(head, msg string) string {
	c.t.Helper()
	c.git("checkout", "-q", "main")
	c.git("merge", "-q", "--no-ff", "-m", msg, head)
	return c.git("rev-parse", "HEAD")
}

func (c *checkout) configFiles(ref string) map[string][]byte {
	c.t.Helper()
	out := map[string][]byte{}
	for _, path := range strings.Split(c.git("ls-tree", "-r", "--name-only", ref), "\n") {
		if path == "stackorder.yaml" || strings.HasSuffix(path, "/.stackorder.yaml") {
			out[path] = []byte(c.git("show", ref+":"+path) + "\n")
		}
	}
	return out
}

func editFile(t *testing.T, root, path string, change func(string) string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(path))
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	out := change(string(data))
	require.NotEqual(t, string(data), out, "the edit of %s changes nothing", path)
	require.NoError(t, os.WriteFile(file, []byte(out), 0o600))
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(plansDir, name))
	require.NoError(t, err)
	return p
}
