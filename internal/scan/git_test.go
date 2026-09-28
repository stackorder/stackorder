package scan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "stackorder")
	t.Setenv("GIT_AUTHOR_EMAIL", "stackorder@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "stackorder")
	t.Setenv("GIT_COMMITTER_EMAIL", "stackorder@example.com")
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeFile(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "--no-gpg-sign", "-m", msg)
}

func newDivergedRepo(t *testing.T) string {
	t.Helper()
	isolateGit(t)
	root := t.TempDir()
	gitRun(t, root, "init", "-q", "-b", "main")
	writeFile(t, root, ".gitignore", "*.log\n")
	writeFile(t, root, "stacks/prod/vpc/main.tf", "# vpc\n")
	writeFile(t, root, "stacks/prod/eks/main.tf", "# eks\n")
	writeFile(t, root, "modules/old/main.tf", "# module\n")
	writeFile(t, root, "infra/keep.tf", "# keep\n")
	writeFile(t, root, "README.md", "# readme\n")
	commitAll(t, root, "base")

	gitRun(t, root, "checkout", "-q", "-b", "feature")
	gitRun(t, root, "mv", "modules/old", "modules/new")
	writeFile(t, root, "stacks/prod/vpc/main.tf", "# vpc changed\n")
	writeFile(t, root, "stacks/prod/apps/main.tf", "# apps\n")
	writeFile(t, root, "stacks/with space/ünïcode.tf", "# quoted by git without -z\n")
	writeFile(t, root, "infra/stacks/sub.tf", "# sub\n")
	removeFile(t, root, "stacks/prod/eks/main.tf")
	commitAll(t, root, "feature")

	gitRun(t, root, "checkout", "-q", "main")
	writeFile(t, root, "stacks/staging/vpc/main.tf", "# only on main\n")
	writeFile(t, root, "README.md", "# readme changed on main\n")
	commitAll(t, root, "main moves on")
	gitRun(t, root, "checkout", "-q", "feature")
	return root
}

func TestChangedPaths(t *testing.T) {
	root := newDivergedRepo(t)
	featureDiff := []string{
		"infra/stacks/sub.tf",
		"modules/new/main.tf",
		"modules/old/main.tf",
		"stacks/prod/apps/main.tf",
		"stacks/prod/eks/main.tf",
		"stacks/prod/vpc/main.tf",
		"stacks/with space/ünïcode.tf",
	}
	tests := []struct {
		name       string
		dir        string
		base, head string
		want       []string
	}{
		{name: "merge base ignores commits only on base", dir: root, base: "main", head: "feature", want: featureDiff},
		{name: "head as HEAD", dir: root, base: "main", head: "HEAD", want: featureDiff},
		{
			name: "reverse direction reports only base side",
			dir:  root, base: "feature", head: "main",
			want: []string{"README.md", "stacks/staging/vpc/main.tf"},
		},
		{name: "same revision", dir: root, base: "feature", head: "feature", want: []string{}},
		{
			name: "subdirectory root reports relative paths",
			dir:  filepath.Join(root, "infra"), base: "main", head: "feature",
			want: []string{"stacks/sub.tf"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ChangedPaths(context.Background(), tt.dir, tt.base, tt.head)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ChangedPaths mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestChangedPathsErrors(t *testing.T) {
	root := newDivergedRepo(t)
	tests := []struct {
		name       string
		dir        string
		base, head string
		invalid    bool
	}{
		{name: "empty base", dir: root, base: "", head: "feature", invalid: true},
		{name: "blank head", dir: root, base: "main", head: " ", invalid: true},
		{name: "option injection", dir: root, base: "--output=/tmp/x", head: "feature", invalid: true},
		{name: "unknown revision", dir: root, base: "main", head: "does-not-exist"},
		{name: "not a repository", dir: t.TempDir(), base: "main", head: "feature"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ChangedPaths(context.Background(), tt.dir, tt.base, tt.head)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrInvalidRevision); got != tt.invalid {
				t.Errorf("errors.Is(err, ErrInvalidRevision) = %v, want %v: %v", got, tt.invalid, err)
			}
		})
	}
}

func TestChangedPathsCanceled(t *testing.T) {
	root := newDivergedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ChangedPaths(ctx, root, "main", "feature"); err == nil {
		t.Fatal("expected an error from a canceled context")
	}
}

func TestChangedPathsWorkingTree(t *testing.T) {
	root := newDivergedRepo(t)
	writeFile(t, root, "stacks/prod/vpc/main.tf", "# unstaged edit\n")
	writeFile(t, root, "stacks/prod/staged/main.tf", "# staged\n")
	gitRun(t, root, "add", "stacks/prod/staged/main.tf")
	writeFile(t, root, "stacks/prod/untracked/main.tf", "# untracked\n")
	writeFile(t, root, "stacks/prod/untracked/debug.log", "ignored\n")
	removeFile(t, root, "infra/keep.tf")

	uncommitted := []string{
		"infra/keep.tf",
		"stacks/prod/staged/main.tf",
		"stacks/prod/untracked/main.tf",
		"stacks/prod/vpc/main.tf",
	}
	tests := []struct {
		name string
		dir  string
		base string
		want []string
	}{
		{
			name: "branch changes plus working tree",
			dir:  root,
			base: "main",
			want: []string{
				"infra/keep.tf",
				"infra/stacks/sub.tf",
				"modules/new/main.tf",
				"modules/old/main.tf",
				"stacks/prod/apps/main.tf",
				"stacks/prod/eks/main.tf",
				"stacks/prod/staged/main.tf",
				"stacks/prod/untracked/main.tf",
				"stacks/prod/vpc/main.tf",
				"stacks/with space/ünïcode.tf",
			},
		},
		{name: "empty base means uncommitted only", dir: root, base: "", want: uncommitted},
		{name: "explicit HEAD", dir: root, base: "HEAD", want: uncommitted},
		{
			name: "subdirectory root",
			dir:  filepath.Join(root, "infra"),
			base: "main",
			want: []string{"keep.tf", "stacks/sub.tf"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ChangedPathsWorkingTree(context.Background(), tt.dir, tt.base)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ChangedPathsWorkingTree mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestChangedPathsWorkingTreeErrors(t *testing.T) {
	root := newDivergedRepo(t)
	tests := []struct {
		name    string
		dir     string
		base    string
		invalid bool
	}{
		{name: "option injection", dir: root, base: "-p", invalid: true},
		{name: "unknown base", dir: root, base: "nope"},
		{name: "not a repository", dir: t.TempDir(), base: "main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ChangedPathsWorkingTree(context.Background(), tt.dir, tt.base)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrInvalidRevision); got != tt.invalid {
				t.Errorf("errors.Is(err, ErrInvalidRevision) = %v, want %v: %v", got, tt.invalid, err)
			}
		})
	}
}

func TestFilterIgnored(t *testing.T) {
	paths := []string{
		"README.md",
		"stacks/prod/vpc/README.md",
		"stacks/prod/vpc/README",
		"stacks/prod/vpc/main.tf",
		"stacks/prod/vpc/.terraform.lock.hcl",
		"modules/vpc/docs/usage.txt",
	}
	tests := []struct {
		name  string
		globs []string
		want  []string
	}{
		{name: "no globs keeps everything", globs: nil, want: paths},
		{
			name:  "default ignore list",
			globs: []string{"**/*.md", "**/README*"},
			want:  []string{"stacks/prod/vpc/main.tf", "stacks/prod/vpc/.terraform.lock.hcl", "modules/vpc/docs/usage.txt"},
		},
		{
			name:  "lock file glob",
			globs: []string{"**/.terraform.lock.hcl"},
			want:  []string{"README.md", "stacks/prod/vpc/README.md", "stacks/prod/vpc/README", "stacks/prod/vpc/main.tf", "modules/vpc/docs/usage.txt"},
		},
		{
			name:  "globs match the full path, not the base name",
			globs: []string{"*.md", "docs/**"},
			want:  []string{"stacks/prod/vpc/README.md", "stacks/prod/vpc/README", "stacks/prod/vpc/main.tf", "stacks/prod/vpc/.terraform.lock.hcl", "modules/vpc/docs/usage.txt"},
		},
		{
			name:  "directory glob",
			globs: []string{"modules/**"},
			want:  []string{"README.md", "stacks/prod/vpc/README.md", "stacks/prod/vpc/README", "stacks/prod/vpc/main.tf", "stacks/prod/vpc/.terraform.lock.hcl"},
		},
		{name: "invalid glob matches nothing", globs: []string{"stacks/[prod"}, want: paths},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, FilterIgnored(paths, tt.globs)); diff != "" {
				t.Errorf("FilterIgnored mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if got := FilterIgnored(nil, []string{"**"}); got == nil || len(got) != 0 {
		t.Errorf("FilterIgnored(nil) = %#v, want an empty non-nil slice", got)
	}
}
