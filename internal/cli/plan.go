package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

type planOptions struct {
	stack string
	runID string
	out   string
}

func (a *app) planCommand() *cobra.Command {
	var o planOptions
	cmd := &cobra.Command{
		Use:   "plan --stack <key>",
		Short: "Plan one stack, summarize and redact the plan, and report it to the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runPlan(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.stack, "stack", "", "stack key: path or path:workspace")
	f.StringVar(&o.runID, "run-id", "", "server run id (env "+EnvRunID+")")
	f.StringVar(&o.out, "out", "", "plan file to write (default <plan dir>/<artifact>.tfplan)")
	_ = cmd.MarkFlagRequired("stack")
	return cmd
}

type planOutcome struct {
	res      v1.StackResult
	planFile string
	jsonFile string
	err      error
}

func (a *app) runPlan(ctx context.Context, o planOptions) error {
	if err := a.requireFormat(false); err != nil {
		return err
	}
	s, err := a.newSession(ctx, o.stack, o.runID)
	if err != nil {
		return err
	}
	oc := &planOutcome{res: v1.StackResult{Mode: v1.ModePlan}}
	oc.err = s.plan(ctx, o.out, oc)
	if oc.err != nil {
		s.fail(&oc.res, oc.err)
	} else {
		oc.res.Status = v1.ResultSuccess
	}
	s.finalize(&oc.res)
	s.flush()
	return s.finishPlan(ctx, oc)
}

func (s *session) plan(ctx context.Context, out string, oc *planOutcome) error {
	if err := s.loadStack(); err != nil {
		return err
	}
	oc.res.Artifact = s.artifact(s.gh.SHA)
	dir, err := s.planDir()
	if err != nil {
		return err
	}
	oc.planFile = filepath.Join(dir, oc.res.Artifact+".tfplan")
	if out != "" {
		if oc.planFile, err = filepath.Abs(out); err != nil {
			return fmt.Errorf("--out: %w", err)
		}
	}
	oc.jsonFile = planJSONPath(oc.planFile)
	if err := s.detectTool(ctx); err != nil {
		return err
	}
	if err := s.runHook(ctx, tf.HookPrePlan, "", ""); err != nil {
		return err
	}
	r := s.runner()
	if err := s.init(ctx, r); err != nil {
		return err
	}
	pr, err := r.Plan(ctx, tf.PlanOptions{Out: oc.planFile, DetailedExitCode: true})
	if pr != nil {
		oc.res.ExitCode, oc.res.HasChanges = pr.ExitCode, pr.HasChanges
	}
	if err != nil {
		return err
	}
	p, err := s.writePlanJSON(ctx, r, oc.planFile, oc.jsonFile)
	if err != nil {
		return err
	}
	summary := tf.Summarize(p)
	oc.res.Summary = &summary
	if oc.res.PlanText, oc.res.Truncated, err = s.planText(ctx, r, oc.planFile); err != nil {
		return err
	}
	return s.runHook(ctx, tf.HookPostPlan, oc.planFile, oc.jsonFile)
}

func (s *session) finishPlan(ctx context.Context, oc *planOutcome) error {
	unconfirmed, reason, repErr := s.report(ctx, oc.res)
	note := ""
	if unconfirmed && reason != "" && s.gh.CI {
		note = "Unconfirmed: " + reason + ". Applies are refused until a plan of this commit is confirmed by the server."
		s.a.warn(fmt.Sprintf("the plan of %s is unconfirmed: %s", s.key, reason))
		title := "Unconfirmed: " + summaryLine(oc.res.Summary)
		if oc.res.Status != v1.ResultSuccess {
			title = "Unconfirmed: plan " + string(oc.res.Status)
		}
		s.a.neutralCheck(ctx, s.gh, "stackorder/plan: "+s.key, title, stackMarkdown(v1.ModePlan, s.key, &oc.res, note))
	}
	summary := "null"
	if oc.res.Summary != nil {
		summary = jsonLine(oc.res.Summary)
	}
	outErr := s.a.setOutputs(
		output{"has-changes", strconv.FormatBool(oc.res.HasChanges)},
		output{"plan-file", oc.planFile},
		output{"artifact", oc.res.Artifact},
		output{"summary", summary},
		output{"unconfirmed", strconv.FormatBool(unconfirmed)},
	)
	s.a.stepSummary(stackMarkdown(v1.ModePlan, s.key, &oc.res, note))
	line := summaryLine(oc.res.Summary)
	if oc.res.Status != v1.ResultSuccess {
		line = "plan " + string(oc.res.Status)
	}
	printErr := s.print(oc.res, oc.planFile, unconfirmed, line)
	if oc.err != nil {
		if repErr != nil {
			s.a.warn(repErr.Error())
		}
		return &ExitError{Code: ExitFailure, Err: fmt.Errorf("plan of %s failed: %w", s.key, oc.err)}
	}
	if repErr != nil {
		return repErr
	}
	if outErr != nil {
		return outErr
	}
	return printErr
}
