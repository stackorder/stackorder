package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

var (
	scpRemote = regexp.MustCompile(`^[^@/]+@[^:/]+:(.+)$`)
	hexSHA    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, firstLine(msg))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func parseRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	var p string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		p = u.Path
	case scpRemote.MatchString(remote):
		p = scpRemote.FindStringSubmatch(remote)[1]
	default:
		return ""
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	parts := strings.Split(p, "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

func revParse(ctx context.Context, dir, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid git revision %q", ref)
	}
	return gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

var errNoBase = errors.New("no base to diff against; pass --base")

func (a *app) baseRef(ctx context.Context, gh *Context, flag string) (string, error) {
	if flag != "" {
		if strings.HasPrefix(flag, "-") {
			return "", fmt.Errorf("--base: invalid git revision %q", flag)
		}
		return flag, nil
	}
	if gh.BaseSHA != "" {
		return gh.BaseSHA, nil
	}
	branch := gh.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	mb, err := gitOutput(ctx, a.root, "merge-base", "origin/"+branch, "HEAD")
	if err != nil {
		return "", fmt.Errorf("%w: merge base of origin/%s and HEAD: %w", errNoBase, branch, err)
	}
	return mb, nil
}

func (a *app) baseSHA(ctx context.Context, ref string) string {
	if sha, err := revParse(ctx, a.root, ref); err == nil {
		return sha
	}
	if hexSHA.MatchString(ref) {
		return ref
	}
	return ""
}
