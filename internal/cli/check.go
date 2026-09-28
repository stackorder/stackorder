package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/tf"
)

const (
	maxCheckDetails     = 64 * 1024
	maxCheckSummaryText = 4 * 1024
)

type checkOptions struct {
	stack       string
	runID       string
	name        string
	status      string
	summary     string
	detailsURL  string
	detailsFile string
}

func (a *app) checkCommand() *cobra.Command {
	var o checkOptions
	cmd := &cobra.Command{
		Use:   "check --stack <key> --run-id <id> --name <n> --status pass|fail|warn",
		Short: "Record a named policy or cost check verdict on a stack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runCheck(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.stack, "stack", "", "stack key: path or path:workspace")
	f.StringVar(&o.runID, "run-id", "", "server run id (env "+EnvRunID+")")
	f.StringVar(&o.name, "name", "", "check name, shown as stackorder/<name>: <stack>")
	f.StringVar(&o.status, "status", "", "verdict: pass, fail or warn")
	f.StringVar(&o.summary, "summary", "", "one line summary")
	f.StringVar(&o.detailsURL, "details-url", "", "link to the full report")
	f.StringVar(&o.detailsFile, "details-file", "", "file whose contents are sent as the details")
	for _, name := range []string{"stack", "name", "status"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func (a *app) runCheck(ctx context.Context, o checkOptions) error {
	if err := a.requireFormat(false); err != nil {
		return err
	}
	status := v1.CheckStatus(strings.ToLower(strings.TrimSpace(o.status)))
	switch status {
	case v1.CheckPass, v1.CheckFail, v1.CheckWarn:
	default:
		return failed("--status: %q is not one of pass, fail, warn", o.status)
	}
	name := strings.TrimSpace(o.name)
	if name == "" {
		return failed("--name is required")
	}
	key := config.NormalizePath(strings.TrimSpace(o.stack))
	if key == "" {
		return failed("--stack is required")
	}
	gh, err := a.github(ctx)
	if err != nil {
		return err
	}
	runID := firstNonEmpty(o.runID, os.Getenv(EnvRunID), gh.DispatchRunID)
	if runID == "" {
		return failed("check needs --run-id or %s", EnvRunID)
	}
	cl, err := a.newClient(gh, !gh.CI)
	if err != nil {
		return failed("%w", err)
	}
	redactor := tf.NewRedactor(envSecrets())
	verdict := v1.CheckVerdict{Status: status, DetailsURL: strings.TrimSpace(o.detailsURL)}
	verdict.Summary, _ = tf.Truncate(redactor.Redact(o.summary), maxCheckSummaryText)
	if o.detailsFile != "" {
		data, err := os.ReadFile(o.detailsFile)
		if err != nil {
			return failed("--details-file: %w", err)
		}
		verdict.Details, _ = tf.Truncate(redactor.Redact(string(data)), maxCheckDetails)
	}
	chk, err := cl.PostCheck(ctx, runID, key, name, verdict)
	if err != nil {
		return fmt.Errorf("recording check %s on %s in run %s: %w", name, key, runID, err)
	}
	if a.format == formatJSON {
		return a.writeJSON(chk)
	}
	_, err = fmt.Fprintf(a.stdout, "stackorder/%s: %s: %s\n", name, key, chk.Status)
	return err
}
