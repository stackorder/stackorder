package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

type driftOptions struct {
	stack string
	runID string
}

func (a *app) driftCommand() *cobra.Command {
	var o driftOptions
	cmd := &cobra.Command{
		Use:   "drift --stack <key>",
		Short: "Check one stack for drift; exits 2 when it drifted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runDrift(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.stack, "stack", "", "stack key: path or path:workspace")
	f.StringVar(&o.runID, "run-id", "", "server run id (env "+EnvRunID+")")
	_ = cmd.MarkFlagRequired("stack")
	return cmd
}

func (a *app) runDrift(ctx context.Context, o driftOptions) error {
	if err := a.requireFormat(false); err != nil {
		return err
	}
	s, err := a.newSession(ctx, o.stack, o.runID)
	if err != nil {
		return err
	}
	res := v1.StackResult{Mode: v1.ModeDrift}
	driftErr := s.drift(ctx, &res)
	if driftErr != nil {
		s.fail(&res, driftErr)
	} else {
		res.Status = v1.ResultSuccess
	}
	s.finalize(&res)
	s.flush()

	unconfirmed, reason, repErr := s.report(ctx, res)
	if unconfirmed && reason != "" && s.gh.CI {
		s.a.warn(fmt.Sprintf("the drift check of %s is unconfirmed: %s", s.key, reason))
	}
	summary := "null"
	if res.Summary != nil {
		summary = jsonLine(res.Summary)
	}
	drifted := driftErr == nil && res.HasChanges
	outErr := a.setOutputs(
		output{"drifted", strconv.FormatBool(drifted)},
		output{"summary", summary},
	)
	a.stepSummary(stackMarkdown(v1.ModeDrift, s.key, &res, ""))
	line := "no drift"
	switch {
	case driftErr != nil:
		line = "drift check " + string(res.Status)
	case drifted:
		line = "drifted: " + summaryLine(res.Summary)
	}
	printErr := s.print(res, "", unconfirmed, line)
	switch {
	case driftErr != nil:
		if repErr != nil {
			a.warn(repErr.Error())
		}
		return &ExitError{Code: ExitFailure, Err: fmt.Errorf("drift check of %s failed: %w", s.key, driftErr)}
	case repErr != nil:
		return repErr
	case outErr != nil:
		return outErr
	case printErr != nil:
		return printErr
	case drifted:
		return &ExitError{Code: ExitChanges}
	}
	return nil
}

func (s *session) drift(ctx context.Context, res *v1.StackResult) error {
	if err := s.loadStack(); err != nil {
		return err
	}
	if err := s.detectTool(ctx); err != nil {
		return err
	}
	r := s.runner()
	if err := s.init(ctx, r); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "stackorder-drift-")
	if err != nil {
		return fmt.Errorf("creating a temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	planFile := filepath.Join(tmp, "drift.tfplan")
	pr, err := r.Plan(ctx, tf.PlanOptions{Out: planFile, DetailedExitCode: true})
	if pr != nil {
		res.ExitCode, res.HasChanges = pr.ExitCode, pr.HasChanges
	}
	if err != nil {
		return err
	}
	p, err := s.writePlanJSON(ctx, r, planFile, planJSONPath(planFile))
	if err != nil {
		return err
	}
	summary := tf.Summarize(p)
	res.Summary = &summary
	res.PlanText, res.Truncated, err = s.planText(ctx, r, planFile)
	return err
}
