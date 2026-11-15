package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func (a *app) validateCommand() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check stackorder.yaml, the .stackorder.yaml files and the dependency graph; needs no server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runValidate(cmd.Context(), strict)
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "exit 1 on warnings as well as errors")
	return cmd
}

func (a *app) runValidate(ctx context.Context, strict bool) error {
	if err := a.requireFormat(false); err != nil {
		return err
	}
	gh, err := a.github(ctx)
	if err != nil {
		return err
	}
	report, err := validateRepo(ctx, a.root, gh.Repository, gh.SHA)
	if err != nil {
		return failed("%w", err)
	}
	for _, d := range report.Diagnostics {
		a.annotateDiagnostic(d)
	}
	if a.format == formatJSON {
		if err := a.writeJSON(report); err != nil {
			return err
		}
	} else if err := writeValidateText(a.stdout, report); err != nil {
		return err
	}
	if report.Errors > 0 || (strict && report.Warnings > 0) {
		return &ExitError{Code: ExitFailure}
	}
	return nil
}

func (a *app) annotateDiagnostic(d v1.Diagnostic) {
	if !inActions() {
		return
	}
	level := "warning"
	if d.Severity == v1.SeverityError {
		level = "error"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "::%s", level)
	if d.File != "" {
		fmt.Fprintf(&b, " file=%s", escapeProperty(d.File))
		if d.Line > 0 {
			fmt.Fprintf(&b, ",line=%d", d.Line)
			if d.Column > 0 {
				fmt.Fprintf(&b, ",col=%d", d.Column)
			}
		}
	}
	fmt.Fprintf(&b, "::%s", escapeCommand(d.Message))
	_, _ = fmt.Fprintln(a.stderr, b.String())
}

func writeValidateText(w io.Writer, report *v1.ValidationReport) error {
	var b strings.Builder
	for _, d := range report.Diagnostics {
		b.WriteString(formatDiagnostic(d))
		b.WriteByte('\n')
	}
	switch {
	case report.Errors == 0 && report.Warnings == 0:
		b.WriteString("valid: no problems found\n")
	case report.Errors == 0:
		fmt.Fprintf(&b, "valid: %s\n", countsText(report))
	default:
		fmt.Fprintf(&b, "invalid: %s\n", countsText(report))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func formatDiagnostic(d v1.Diagnostic) string {
	loc := d.File
	if loc != "" && d.Line > 0 {
		loc = fmt.Sprintf("%s:%d", loc, d.Line)
		if d.Column > 0 {
			loc = fmt.Sprintf("%s:%d", loc, d.Column)
		}
	}
	if loc != "" {
		return fmt.Sprintf("%s: %s: %s", loc, d.Severity, d.Message)
	}
	return fmt.Sprintf("%s: %s", d.Severity, d.Message)
}

func countsText(report *v1.ValidationReport) string {
	return strings.Join([]string{plural(report.Errors, "error"), plural(report.Warnings, "warning")}, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
