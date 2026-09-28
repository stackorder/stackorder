package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func (a *app) graphCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "graph",
		Short: "Scan the repository and print its dependency graph (--format text, json or dot)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runGraph(cmd.Context())
		},
	}
}

func (a *app) affectedCommand() *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:   "affected --base <ref>",
		Short: "Print the stacks a change affects, by wave; exits 2 when any stack is affected",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runAffected(cmd.Context(), base)
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "base ref to diff HEAD against (default: the pull request base, else the merge base with origin/<default branch>)")
	return cmd
}

func (a *app) runGraph(ctx context.Context) error {
	if err := a.requireFormat(true); err != nil {
		return err
	}
	sr, err := a.scan(ctx, false, "")
	if err != nil {
		return err
	}
	switch a.format {
	case formatJSON:
		return a.writeJSON(sr.graph)
	case formatDOT:
		return a.writeDOT(sr.graph, nil)
	}
	return writeGraphText(a.stdout, sr.graph)
}

func (a *app) runAffected(ctx context.Context, base string) error {
	if err := a.requireFormat(true); err != nil {
		return err
	}
	sr, err := a.scan(ctx, true, base)
	if err != nil {
		return err
	}
	resp, err := resolveLocal(sr.graph, sr.changed, sr.cfg, nil)
	if resp != nil && len(resp.Cycles) > 0 {
		return a.cycleError(resp)
	}
	if err != nil {
		return failed("resolving: %w", err)
	}
	for _, w := range resp.Warnings {
		a.warn(w)
	}
	switch a.format {
	case formatJSON:
		err = a.writeJSON(resp)
	case formatDOT:
		highlight := make(map[string]int, len(resp.Affected))
		for _, s := range resp.Affected {
			highlight[s.Key] = s.Wave
		}
		err = a.writeDOT(sr.graph, highlight)
	default:
		err = writeAffectedTable(a.stdout, resp)
	}
	if err != nil {
		return err
	}
	if len(resp.Affected) > 0 {
		return &ExitError{Code: ExitChanges}
	}
	return nil
}

func (a *app) writeDOT(g *v1.Graph, highlight map[string]int) error {
	dot := renderDOT(g, highlight)
	if dot == "" {
		return failed("%w", ErrGraphNotLinked)
	}
	_, err := fmt.Fprint(a.stdout, dot)
	return err
}
