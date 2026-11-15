// Package validate checks a repository's stackorder.yaml and .stackorder.yaml
// files and the dependency graph the scanner builds, and reports every
// problem as a v1.Diagnostic. It is the composition the validate CLI command
// binds, the way cmd/stackorder binds the scanner and the graph.
package validate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/scan"
)

// Options configures a Run. Repo, SHA and GitHubURL are passed to the
// scanner; Logger receives the scanner's debug output.
type Options struct {
	Repo      string
	SHA       string
	GitHubURL string
	Logger    *slog.Logger
}

var skippedDirs = map[string]bool{".git": true, ".terraform": true, "node_modules": true}

// Run validates the repository rooted at root. It parses the root
// stackorder.yaml and every .stackorder.yaml file, collecting every error
// rather than stopping at the first, and, when the files all parse, scans the
// repository and validates the graph, collecting the scanner's warnings and
// the graph's structural errors and cycles. The report lists every
// diagnostic, sorted and deduplicated. Run itself errors only when the
// repository cannot be read.
func Run(ctx context.Context, root string, opts Options) (*v1.ValidationReport, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("validate %s: %w", root, err)
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, fmt.Errorf("validate %s: %w", root, err)
	}
	diags, err := validateConfigs(abs)
	if err != nil {
		return nil, fmt.Errorf("validate %s: %w", root, err)
	}
	if !hasErrors(diags) {
		diags = append(diags, validateGraph(ctx, abs, opts)...)
	}
	return report(diags), nil
}

func validateGraph(ctx context.Context, root string, opts Options) []v1.Diagnostic {
	g, err := scan.Scan(ctx, root, scan.Options{Repo: opts.Repo, SHA: opts.SHA, GitHubURL: opts.GitHubURL, Logger: opts.Logger})
	if err != nil {
		return []v1.Diagnostic{{Severity: v1.SeverityError, Message: cleanMessage(strings.TrimPrefix(err.Error(), fmt.Sprintf("scan %s: ", root)))}}
	}
	var diags []v1.Diagnostic
	for _, w := range g.Warnings {
		diags = append(diags, v1.Diagnostic{Severity: v1.SeverityWarning, Message: w})
	}
	_, verr := graph.Validate(g)
	for _, leaf := range flatten(verr) {
		if errors.Is(leaf, graph.ErrInvalidGraph) {
			continue
		}
		diags = append(diags, v1.Diagnostic{Severity: v1.SeverityError, Message: cleanMessage(leaf.Error())})
	}
	return diags
}

func validateConfigs(root string) ([]v1.Diagnostic, error) {
	var diags []v1.Diagnostic
	rootFile := filepath.Join(root, config.RootFile)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Name() == config.RootFile && p == rootFile:
			diags = append(diags, diagnoseFile(root, p, parseRoot)...)
		case d.Name() == config.StackFile:
			diags = append(diags, diagnoseFile(root, p, parseStack)...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return diags, nil
}

func parseRoot(data []byte) error {
	_, err := config.Parse(data)
	return err
}

func parseStack(data []byte) error {
	_, err := config.ParseStack(data)
	return err
}

func diagnoseFile(root, path string, parse func([]byte) error) []v1.Diagnostic {
	rel := relPath(root, path)
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return []v1.Diagnostic{{Severity: v1.SeverityError, File: rel, Message: cleanMessage(err.Error())}}
	}
	return diagnostics(rel, parse(data))
}

func diagnostics(file string, err error) []v1.Diagnostic {
	var out []v1.Diagnostic
	for _, leaf := range flatten(err) {
		var te *yaml.TypeError
		if errors.As(leaf, &te) {
			for _, m := range te.Errors {
				line, msg := splitLine(m)
				out = append(out, v1.Diagnostic{Severity: v1.SeverityError, File: file, Line: line, Message: cleanMessage(msg)})
			}
			continue
		}
		line, msg := splitLine(stripFilePrefix(leaf.Error()))
		out = append(out, v1.Diagnostic{Severity: v1.SeverityError, File: file, Line: line, Message: cleanMessage(msg)})
	}
	return out
}

func flatten(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range joined.Unwrap() {
			out = append(out, flatten(e)...)
		}
		return out
	}
	return []error{err}
}

var (
	lineRe         = regexp.MustCompile(`^(?:yaml:\s*)?line (\d+):\s*(.*)$`)
	unknownFieldRe = regexp.MustCompile(`^field (\S+) not found in type .*$`)
)

func splitLine(s string) (int, string) {
	s = strings.TrimSpace(s)
	if m := lineRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, m[2]
	}
	return 0, strings.TrimPrefix(s, "yaml: ")
}

func stripFilePrefix(s string) string {
	for _, p := range []string{config.RootFile + ": ", config.StackFile + ": "} {
		if strings.HasPrefix(s, p) {
			return s[len(p):]
		}
	}
	return s
}

func cleanMessage(s string) string {
	s = strings.TrimSpace(s)
	if m := unknownFieldRe.FindStringSubmatch(s); m != nil {
		return fmt.Sprintf("unknown field %q", m[1])
	}
	return s
}

func relPath(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}

func hasErrors(diags []v1.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == v1.SeverityError {
			return true
		}
	}
	return false
}

func report(diags []v1.Diagnostic) *v1.ValidationReport {
	slices.SortFunc(diags, compareDiagnostics)
	diags = slices.CompactFunc(diags, func(a, b v1.Diagnostic) bool { return a == b })
	r := &v1.ValidationReport{Diagnostics: diags}
	if r.Diagnostics == nil {
		r.Diagnostics = []v1.Diagnostic{}
	}
	for _, d := range r.Diagnostics {
		switch d.Severity {
		case v1.SeverityError:
			r.Errors++
		case v1.SeverityWarning:
			r.Warnings++
		}
	}
	r.Valid = r.Errors == 0
	return r
}

func compareDiagnostics(a, b v1.Diagnostic) int {
	if c := strings.Compare(a.File, b.File); c != 0 {
		return c
	}
	if c := a.Line - b.Line; c != 0 {
		return c
	}
	if c := a.Column - b.Column; c != 0 {
		return c
	}
	if c := strings.Compare(string(a.Severity), string(b.Severity)); c != 0 {
		return c
	}
	return strings.Compare(a.Message, b.Message)
}
