// Command stackorder is the runner side CLI. It scans repositories, resolves
// affected stacks, runs plans and applies, and reports results to the server.
package main

import (
	"context"
	"os"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/cli"
	"github.com/stackorder/stackorder/internal/graph"
	"github.com/stackorder/stackorder/internal/scan"
)

func main() {
	cli.Bind(cli.Deps{
		ScanRepo: func(ctx context.Context, root, repo, sha string, cfg *v1.RepoConfig) (*v1.Graph, error) {
			return scan.Scan(ctx, root, scan.Options{Repo: repo, SHA: sha, Config: cfg})
		},
		ChangedPaths: scan.ChangedPaths,
		ResolveLocal: func(g *v1.Graph, changed []string, cfg *v1.RepoConfig, requested []string) (*v1.ResolveResponse, error) {
			return graph.Resolve(g, graph.Input{ChangedPaths: changed, Config: cfg, Requested: requested})
		},
		RenderDOT: graph.ToDOT,
	})
	os.Exit(cli.ExitCode(cli.Execute()))
}
