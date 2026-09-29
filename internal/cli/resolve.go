package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
)

const forkNote = "Fork pull requests get a read-only GITHUB_TOKEN and no OIDC token, so Stackorder cannot reach its server or the cloud role from them; nothing is planned. A maintainer can re-run the change from a branch in this repository."

type resolveOptions struct {
	base   string
	stacks []string
}

func (a *app) resolveCommand() *cobra.Command {
	var o resolveOptions
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Scan the repository, resolve the affected stacks with the server and write the plan matrix",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runResolve(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.base, "base", "", "base ref to diff against (default: the pull request base, else the merge base with origin/<default branch>)")
	f.StringSliceVar(&o.stacks, "stacks", nil, "restrict the run to these stack keys, comma separated")
	return cmd
}

type scanResult struct {
	gh      *Context
	cfg     *v1.RepoConfig
	graph   *v1.Graph
	changed []string
	base    string
}

func (a *app) scan(ctx context.Context, withChanges bool, base string) (*scanResult, error) {
	gh, err := a.github(ctx)
	if err != nil {
		return nil, err
	}
	cfg, _, err := config.Load(a.root)
	if err != nil {
		return nil, failed("loading %s: %w", config.RootFile, err)
	}
	g, err := scanRepo(ctx, a.root, gh.Repository, gh.SHA, cfg)
	if err != nil {
		return nil, failed("scanning %s: %w", a.root, err)
	}
	for _, w := range g.Warnings {
		a.warn(w)
	}
	res := &scanResult{gh: gh, cfg: cfg, graph: g}
	if !withChanges {
		return res, nil
	}
	if res.base, err = a.baseRef(ctx, gh, base); err != nil {
		return nil, failed("%w", err)
	}
	if res.changed, err = changedPaths(ctx, a.root, res.base, "HEAD"); err != nil {
		return nil, failed("listing the paths changed since %s: %w", res.base, err)
	}
	a.log.Debug("changed paths", "base", res.base, "count", len(res.changed))
	return res, nil
}

func (a *app) runResolve(ctx context.Context, o resolveOptions) error {
	if err := a.requireFormat(false); err != nil {
		return err
	}
	gh, err := a.github(ctx)
	if err != nil {
		return err
	}
	if gh.CI && gh.IsFork {
		return a.resolveFork(ctx, gh)
	}
	sr, err := a.scan(ctx, true, o.base)
	if err != nil {
		return err
	}
	requested := normalizeKeys(o.stacks)
	resp, reason, err := a.resolveWithServer(ctx, sr, requested)
	if err != nil {
		return err
	}
	if resp == nil {
		a.log.Info("resolving locally", "reason", reason)
		if resp, err = a.resolveLocally(sr, requested); err != nil {
			return err
		}
	}
	if len(resp.Cycles) > 0 {
		return a.cycleError(resp)
	}
	note := ""
	if resp.Unconfirmed && gh.CI {
		note = "Unconfirmed: " + reason + ". The affected set was computed locally; plans run, applies are refused until the server confirms a plan of this commit."
		a.warn("the resolution is unconfirmed: " + reason)
		a.neutralCheck(ctx, gh, "stackorder/resolve", resolveTitle(resp), resolveMarkdown(resp, note))
	}
	for _, w := range resp.Warnings {
		a.warn(w)
	}
	if err := a.resolveOutputs(resp); err != nil {
		return err
	}
	a.stepSummary(resolveMarkdown(resp, note))
	return a.printResolve(resp)
}

func (a *app) resolveWithServer(ctx context.Context, sr *scanResult, requested []string) (*v1.ResolveResponse, string, error) {
	if a.server == "" {
		return nil, "no server is configured", nil
	}
	if !sr.gh.CI {
		return nil, "outside GitHub Actions the server takes graphs only from pull request resolve jobs", nil
	}
	cl, err := a.newClient(sr.gh, false)
	if err != nil {
		return nil, "", failed("%w", err)
	}
	baseSHA := a.baseSHA(ctx, sr.base)
	cr, err := cl.CreateRun(ctx, v1.CreateRunRequest{
		Repo:          sr.gh.Repository,
		SHA:           sr.gh.SHA,
		BaseSHA:       baseSHA,
		PRNumber:      sr.gh.PRNumber,
		Mode:          v1.ModePlan,
		Trigger:       sr.gh.Trigger(),
		WorkflowRunID: sr.gh.RunID,
		Attempt:       sr.gh.RunAttempt,
		RunID:         sr.gh.DispatchRunID,
	})
	var resp *v1.ResolveResponse
	if err == nil {
		resp, err = cl.UploadGraph(ctx, cr.RunID, v1.GraphUploadRequest{
			Graph:        *sr.graph,
			ChangedPaths: sr.changed,
			BaseSHA:      baseSHA,
			Config:       sr.cfg,
			Stacks:       requested,
		})
	}
	switch {
	case err == nil:
		if resp.RunID == "" {
			resp.RunID = cr.RunID
		}
		return resp, "", nil
	case isUnreachable(err):
		return nil, "the server is unreachable: " + err.Error(), nil
	}
	return nil, "", fmt.Errorf("resolving with the server: %w", err)
}

func (a *app) resolveLocally(sr *scanResult, requested []string) (*v1.ResolveResponse, error) {
	resp, err := resolveLocal(sr.graph, sr.changed, sr.cfg, requested)
	if resp != nil && len(resp.Cycles) > 0 {
		resp.Unconfirmed = true
		return resp, nil
	}
	if err != nil {
		return nil, failed("resolving locally: %w", err)
	}
	resp.RunID = ""
	resp.Unconfirmed = true
	return resp, nil
}

func (a *app) cycleError(resp *v1.ResolveResponse) error {
	lines := cycleLines(resp.Cycles)
	if a.format == formatJSON {
		_ = a.writeJSON(resp)
	} else {
		for _, l := range lines {
			_, _ = fmt.Fprintln(a.stdout, l)
		}
	}
	return &ExitError{Code: ExitFailure, Err: fmt.Errorf("dependency cycle: %s", strings.Join(lines, "; "))}
}

func (a *app) resolveFork(ctx context.Context, gh *Context) error {
	a.annotate("notice", forkNote)
	a.log.Info("fork pull request; nothing is planned")
	a.neutralCheck(ctx, gh, "stackorder/resolve", "Fork pull request: not planned", forkNote)
	resp := &v1.ResolveResponse{
		Affected:    []v1.AffectedStack{},
		Waves:       [][]string{},
		Matrix:      v1.Matrix{Include: []v1.MatrixEntry{}},
		Unconfirmed: true,
	}
	if err := a.resolveOutputs(resp); err != nil {
		return err
	}
	a.stepSummary(resolveMarkdown(resp, forkNote))
	return a.printResolve(resp)
}

func (a *app) resolveOutputs(resp *v1.ResolveResponse) error {
	matrix := resp.Matrix
	if matrix.Include == nil {
		matrix.Include = []v1.MatrixEntry{}
	}
	waves := resp.Waves
	if waves == nil {
		waves = [][]string{}
	}
	keys := make([]string, 0, len(resp.Affected))
	for _, s := range sortedAffected(resp) {
		keys = append(keys, s.Key)
	}
	return a.setOutputs(
		output{"run-id", resp.RunID},
		output{"matrix", jsonLine(matrix)},
		output{"waves", jsonLine(waves)},
		output{"affected", jsonLine(keys)},
		output{"count", strconv.Itoa(len(resp.Affected))},
		output{"unconfirmed", strconv.FormatBool(resp.Unconfirmed)},
	)
}

func (a *app) printResolve(resp *v1.ResolveResponse) error {
	if a.format == formatJSON {
		return a.writeJSON(resp)
	}
	if resp.RunID != "" {
		if _, err := fmt.Fprintf(a.stdout, "run %s\n", resp.RunID); err != nil {
			return err
		}
	}
	return writeAffectedTable(a.stdout, resp)
}

func resolveTitle(resp *v1.ResolveResponse) string {
	switch n := len(resp.Affected); n {
	case 0:
		return "Unconfirmed: no stacks affected"
	case 1:
		return "Unconfirmed: 1 stack affected"
	default:
		return fmt.Sprintf("Unconfirmed: %d stacks affected", n)
	}
}

func normalizeKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		if k = config.NormalizePath(strings.TrimSpace(k)); k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}
