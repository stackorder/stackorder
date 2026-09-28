package cli

import (
	"context"
	"errors"

	v1 "github.com/stackorder/stackorder/api/v1"
)

var (
	// ErrScanNotLinked is returned by the default scanRepo and changedPaths
	// until Bind supplies internal/scan.
	ErrScanNotLinked = errors.New("cli: scan not linked; this build of stackorder cannot scan repositories")
	// ErrGraphNotLinked is returned by the default resolveLocal and by the
	// graph command's DOT output until Bind supplies internal/graph.
	ErrGraphNotLinked = errors.New("cli: graph not linked; this build of stackorder cannot resolve graphs")
)

// Deps are the functions the CLI needs from internal/scan and
// internal/graph. They are bound in cmd/stackorder/main.go with Bind once
// those packages are merged; until then the defaults return
// ErrScanNotLinked or ErrGraphNotLinked.
type Deps struct {
	// ScanRepo builds the dependency graph of the checkout at root.
	ScanRepo func(ctx context.Context, root, repo, sha string, cfg *v1.RepoConfig) (*v1.Graph, error)
	// ChangedPaths lists the repository relative paths changed between the
	// merge base of base and head, and head.
	ChangedPaths func(ctx context.Context, root, base, head string) ([]string, error)
	// ResolveLocal computes the affected stacks and waves of a change. On a
	// cycle it returns the response with Cycles filled and an error.
	ResolveLocal func(g *v1.Graph, changed []string, cfg *v1.RepoConfig, requested []string) (*v1.ResolveResponse, error)
	// RenderDOT renders the graph as Graphviz DOT, highlighting the given
	// stack keys with their wave.
	RenderDOT func(g *v1.Graph, highlight map[string]int) string
}

// Bind replaces the scanning and resolution functions with d's non-nil
// fields. It is meant to be called once from main, before Execute.
func Bind(d Deps) {
	if d.ScanRepo != nil {
		scanRepo = d.ScanRepo
	}
	if d.ChangedPaths != nil {
		changedPaths = d.ChangedPaths
	}
	if d.ResolveLocal != nil {
		resolveLocal = d.ResolveLocal
	}
	if d.RenderDOT != nil {
		renderDOT = d.RenderDOT
	}
}

var (
	scanRepo = func(context.Context, string, string, string, *v1.RepoConfig) (*v1.Graph, error) {
		return nil, ErrScanNotLinked
	}
	changedPaths = func(context.Context, string, string, string) ([]string, error) {
		return nil, ErrScanNotLinked
	}
	resolveLocal = func(*v1.Graph, []string, *v1.RepoConfig, []string) (*v1.ResolveResponse, error) {
		return nil, ErrGraphNotLinked
	}
	renderDOT = func(*v1.Graph, map[string]int) string {
		return ""
	}
)
