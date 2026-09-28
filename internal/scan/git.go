package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ErrInvalidRevision is returned for an empty git revision or one that could
// be mistaken for a command line option.
var ErrInvalidRevision = errors.New("invalid git revision")

var diffArgs = []string{"diff", "--name-only", "--no-renames", "--no-color", "--no-ext-diff", "--relative", "-z"}

// ChangedPaths lists the repository relative paths that changed between the
// merge base of base and head and head itself, as "git diff base...head"
// reports them. A rename reports both the old and the new path. The result is
// sorted, de-duplicated and slash separated.
func ChangedPaths(ctx context.Context, root, base, head string) ([]string, error) {
	if err := checkRevisions(base, head); err != nil {
		return nil, err
	}
	out, err := runGit(ctx, root, append(slices.Clone(diffArgs), base+"..."+head, "--")...)
	if err != nil {
		return nil, err
	}
	return sortedUnique(splitNUL(out)), nil
}

// ChangedPathsWorkingTree lists the paths that differ between the merge base
// of base and HEAD and the working tree, including staged, unstaged and
// untracked files that are not ignored. An empty base means HEAD, so only
// uncommitted changes are reported.
func ChangedPathsWorkingTree(ctx context.Context, root, base string) ([]string, error) {
	if base == "" {
		base = "HEAD"
	}
	if err := checkRevisions(base); err != nil {
		return nil, err
	}
	mergeBase, err := runGit(ctx, root, "merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	changed, err := runGit(ctx, root, append(slices.Clone(diffArgs), strings.TrimSpace(string(mergeBase)), "--")...)
	if err != nil {
		return nil, err
	}
	untracked, err := runGit(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	return sortedUnique(append(splitNUL(changed), splitNUL(untracked)...)), nil
}

// FilterIgnored returns the paths that match none of the doublestar globs.
// Globs are matched against the full repository relative path, so
// "**/*.md" drops "README.md" and "stacks/prod/vpc/README.md" alike; an
// invalid glob matches nothing.
func FilterIgnored(paths, globs []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !matchAny(globs, p) {
			out = append(out, p)
		}
	}
	return out
}

func checkRevisions(revs ...string) error {
	for _, r := range revs {
		if strings.TrimSpace(r) == "" || strings.HasPrefix(r, "-") {
			return fmt.Errorf("%w: %q", ErrInvalidRevision, r)
		}
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func splitNUL(out []byte) []string {
	var paths []string
	for p := range strings.SplitSeq(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func sortedUnique(paths []string) []string {
	slices.Sort(paths)
	paths = slices.Compact(paths)
	if paths == nil {
		return []string{}
	}
	return paths
}

func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, p); err == nil && ok {
			return true
		}
	}
	return false
}
