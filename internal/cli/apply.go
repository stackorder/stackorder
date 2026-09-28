package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

type applyOptions struct {
	stack    string
	runID    string
	planFile string
	local    bool
}

func (a *app) applyCommand() *cobra.Command {
	var o applyOptions
	cmd := &cobra.Command{
		Use:   "apply --stack <key> --run-id <id>",
		Short: "Apply one stack's plan after confirming the run, its commit and its lock with the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runApply(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.stack, "stack", "", "stack key: path or path:workspace")
	f.StringVar(&o.runID, "run-id", "", "server run id (env "+EnvRunID+")")
	f.StringVar(&o.planFile, "plan-file", "", "saved plan to apply (default <plan dir>/<artifact>.tfplan)")
	f.BoolVar(&o.local, "local", false, "apply from outside GitHub Actions through a manual run; needs "+EnvAPIKey)
	_ = cmd.MarkFlagRequired("stack")
	return cmd
}

type applyOutcome struct {
	res      v1.StackResult
	planFile string
	err      error
	hookErr  error
}

func (a *app) checkApplyUsage(gh *Context, o applyOptions) error {
	if o.local {
		switch {
		case gh.CI:
			return failed("--local applies from outside GitHub Actions; in Actions the server dispatches applies")
		case a.server == "":
			return failed("apply --local takes the lock through the server: %w", errNoServer)
		case strings.TrimSpace(os.Getenv(EnvAPIKey)) == "":
			return failed("apply --local needs an automation API key in %s", EnvAPIKey)
		case o.planFile != "":
			return failed("--plan-file cannot be combined with --local, which always plans afresh")
		case o.runID != "":
			return failed("--run-id cannot be combined with --local, which starts its own run")
		}
		return nil
	}
	switch {
	case firstNonEmpty(o.runID, os.Getenv(EnvRunID), gh.DispatchRunID) == "":
		return failed("apply needs --run-id or %s", EnvRunID)
	case !gh.CI:
		return failed("outside GitHub Actions, apply needs --local and an automation API key in %s", EnvAPIKey)
	case a.server == "":
		return refused("no server is configured, so the run and its locks cannot be confirmed; refusing to apply")
	}
	return nil
}

func (a *app) runApply(ctx context.Context, o applyOptions) error {
	if err := a.requireFormat(false); err != nil {
		return err
	}
	gh, err := a.github(ctx)
	if err != nil {
		return err
	}
	if err := a.checkApplyUsage(gh, o); err != nil {
		return err
	}
	s, err := a.newSession(ctx, o.stack, o.runID)
	if err != nil {
		return err
	}
	if err := s.loadStack(); err != nil {
		return failed("%w", err)
	}
	var recorded *v1.PlanSummary
	sha := gh.SHA
	if o.local {
		if err := s.startLocalRun(ctx); err != nil {
			return err
		}
	} else {
		run, row, err := s.confirmRun(ctx)
		if err != nil {
			return err
		}
		recorded, sha = row.Summary, run.SHA
	}
	oc := &applyOutcome{res: v1.StackResult{Mode: v1.ModeApply}}
	oc.err = s.apply(ctx, sha, o.planFile, o.local, recorded, oc)
	if oc.err != nil {
		s.fail(&oc.res, oc.err)
	} else {
		oc.res.Status = v1.ResultSuccess
		if oc.hookErr != nil {
			oc.res.ErrorText, _ = tf.Truncate(s.redactor.Redact(oc.hookErr.Error()), maxErrorText)
		}
	}
	s.finalize(&oc.res)
	s.flush()
	return s.finishApply(ctx, oc)
}

func (s *session) confirmRun(ctx context.Context) (*v1.Run, *v1.RunStack, error) {
	cl, err := s.a.newClient(s.gh, false)
	if err != nil {
		return nil, nil, refused("%w; refusing to apply", err)
	}
	run, err := cl.GetRun(ctx, s.runID)
	if err != nil {
		if isUnreachable(err) {
			return nil, nil, refused("cannot confirm run %s with the server, refusing to apply (fail closed): %w", s.runID, err)
		}
		return nil, nil, fmt.Errorf("confirming run %s: %w", s.runID, err)
	}
	if run.Status != v1.RunPlanned && run.Status != v1.RunApplying {
		return nil, nil, refused("run %s is %s, not planned or applying; refusing to apply %s", s.runID, run.Status, s.key)
	}
	idx := slices.IndexFunc(run.Stacks, func(rs v1.RunStack) bool { return rs.Key == s.key })
	if idx < 0 {
		return nil, nil, refused("stack %s is not part of run %s; refusing to apply", s.key, s.runID)
	}
	row := &run.Stacks[idx]
	if row.Status != v1.StackPlanned && row.Status != v1.StackApplying {
		return nil, nil, refused("stack %s is %s in run %s, not planned or applying; refusing to apply", s.key, row.Status, s.runID)
	}
	if run.SHA == "" || run.SHA != s.gh.SHA {
		return nil, nil, refused("run %s is for commit %q but this job is for %q; refusing to apply", s.runID, run.SHA, s.gh.SHA)
	}
	if head, err := gitOutput(ctx, s.root, "rev-parse", "HEAD"); err == nil && head != run.SHA {
		return nil, nil, refused("run %s is for commit %s but the checkout is at %s; refusing to apply", s.runID, run.SHA, head)
	}
	return run, row, nil
}

func (s *session) startLocalRun(ctx context.Context) error {
	if s.gh.Repository == "" {
		return failed("cannot tell the repository; add a git remote named origin or set GITHUB_REPOSITORY")
	}
	if s.gh.SHA == "" {
		return failed("cannot tell the commit; apply --local must run inside a git checkout")
	}
	if dirty, err := gitOutput(ctx, s.root, "status", "--porcelain"); err == nil && dirty != "" {
		s.a.warn("the working tree has uncommitted changes; the apply runs what is on disk, not commit " + s.gh.SHA)
	}
	cl, err := s.a.newClient(s.gh, true)
	if err != nil {
		return failed("%w", err)
	}
	cr, err := cl.CreateRun(ctx, v1.CreateRunRequest{
		Repo:    s.gh.Repository,
		SHA:     s.gh.SHA,
		Mode:    v1.ModeApply,
		Trigger: v1.TriggerManual,
		Stacks:  []string{s.key},
	})
	if err != nil {
		if isUnreachable(err) {
			return refused("cannot reach the server to take the lock on %s, refusing to apply (fail closed): %w", s.key, err)
		}
		return fmt.Errorf("starting a manual apply run for %s: %w", s.key, err)
	}
	if cr.RunID == "" {
		return failed("the server started no run for %s", s.key)
	}
	s.runID = cr.RunID
	s.a.log.Info("started manual apply run", "run_id", cr.RunID, "stack", s.key)
	return nil
}

func (s *session) apply(ctx context.Context, sha, planFlag string, fresh bool, recorded *v1.PlanSummary, oc *applyOutcome) error {
	oc.res.Artifact = s.artifact(sha)
	if err := s.detectTool(ctx); err != nil {
		return err
	}
	dir, err := s.planDir()
	if err != nil {
		return err
	}
	oc.planFile = filepath.Join(dir, oc.res.Artifact+".tfplan")
	if planFlag != "" {
		if oc.planFile, err = filepath.Abs(planFlag); err != nil {
			return fmt.Errorf("--plan-file: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(oc.planFile), 0o750); err != nil {
			return fmt.Errorf("--plan-file: %w", err)
		}
	}
	jsonFile := planJSONPath(oc.planFile)
	r := s.runner()
	if err := s.init(ctx, r); err != nil {
		return err
	}
	usePlan := !fresh && fileExists(oc.planFile) && (planFlag != "" || s.st.cfg.Apply.FromPlanEnabled())
	var p *tfjson.Plan
	if usePlan {
		if p, err = s.writePlanJSON(ctx, r, oc.planFile, jsonFile); err != nil {
			return err
		}
	} else {
		if p, err = s.replan(ctx, r, oc.planFile, jsonFile, fresh, recorded); err != nil {
			return err
		}
	}
	summary := tf.Summarize(p)
	oc.res.Summary, oc.res.HasChanges = &summary, !summary.Empty()
	if err := s.runHook(ctx, tf.HookPreApply, oc.planFile, jsonFile); err != nil {
		return err
	}
	ar, err := r.Apply(ctx, oc.planFile, tf.ApplyOptions{})
	if ar != nil {
		oc.res.ExitCode = ar.ExitCode
	}
	if err != nil {
		return err
	}
	oc.hookErr = s.runHook(ctx, tf.HookPostApply, oc.planFile, jsonFile)
	return nil
}

func (s *session) replan(ctx context.Context, r *tf.Runner, planFile, jsonFile string, fresh bool, recorded *v1.PlanSummary) (*tfjson.Plan, error) {
	if !fresh && recorded == nil {
		return nil, refused("the plan file %s is missing and run %s recorded no plan summary for %s to compare a new plan with; refusing to apply", planFile, s.runID, s.key)
	}
	if !fresh {
		s.a.warn(fmt.Sprintf("the plan file for %s is not available; planning again and comparing with the recorded plan", s.key))
	}
	if _, err := r.Plan(ctx, tf.PlanOptions{Out: planFile, DetailedExitCode: true}); err != nil {
		return nil, err
	}
	p, err := s.writePlanJSON(ctx, r, planFile, jsonFile)
	if err != nil || fresh {
		return p, err
	}
	got, want := tf.AddressSet(p), tf.SummaryAddressSet(*recorded)
	if !tf.SameAddressSet(got, want) {
		return nil, refused("the new plan of %s does not match the plan recorded in run %s (%s); refusing to apply", s.key, s.runID, addressDiff(want, got))
	}
	return p, nil
}

func addressDiff(want, got []string) string {
	var missing, extra []string
	for _, a := range want {
		if !slices.Contains(got, a) {
			missing = append(missing, a)
		}
	}
	for _, a := range got {
		if !slices.Contains(want, a) {
			extra = append(extra, a)
		}
	}
	var parts []string
	if len(extra) > 0 {
		parts = append(parts, "not in the recorded plan: "+strings.Join(extra, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "missing from the new plan: "+strings.Join(missing, ", "))
	}
	return strings.Join(parts, "; ")
}

func (s *session) finishApply(ctx context.Context, oc *applyOutcome) error {
	repErr := s.post(ctx, oc.res)
	summary := "null"
	if oc.res.Summary != nil {
		summary = jsonLine(oc.res.Summary)
	}
	outErr := s.a.setOutputs(output{"summary", summary})
	s.a.stepSummary(stackMarkdown(v1.ModeApply, s.key, &oc.res, ""))
	line := "applied: " + summaryLine(oc.res.Summary)
	if oc.res.Status != v1.ResultSuccess {
		line = "apply " + string(oc.res.Status)
	}
	printErr := s.print(oc.res, oc.planFile, false, line)
	if oc.err != nil {
		if repErr != nil {
			s.a.warn(repErr.Error())
		}
		var exitErr *ExitError
		if errors.As(oc.err, &exitErr) {
			return oc.err
		}
		return &ExitError{Code: ExitFailure, Err: fmt.Errorf("apply of %s failed: %w", s.key, oc.err)}
	}
	if repErr != nil {
		return failed("applied %s, but reporting the result failed: %w", s.key, repErr)
	}
	if oc.hookErr != nil {
		return failed("applied %s, but %w", s.key, oc.hookErr)
	}
	if outErr != nil {
		return outErr
	}
	return printErr
}
