// Package cli implements the stackorder command line: resolve, plan, apply,
// drift, check, graph, affected, unlock and version.
//
// The same commands run in GitHub Actions and on a laptop. In Actions they
// read the repository, commit, event and workflow run from the GITHUB_*
// variables and the event payload, report to the server with the runner's
// OIDC token, write step outputs to GITHUB_OUTPUT and a Markdown summary to
// GITHUB_STEP_SUMMARY. Outside Actions they read the same facts from git and
// work without a server, except apply --local and unlock, which need an
// automation API key.
//
// Exit codes: 0 success, 1 error, 2 changes found (affected, drift), 3
// refused (by the server, or by the CLI failing closed when it cannot
// confirm an apply).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stackorder/stackorder/internal/client"
)

const (
	// ExitSuccess is the exit code of a command that did what it was asked.
	ExitSuccess = 0
	// ExitFailure is the exit code of a command that failed.
	ExitFailure = 1
	// ExitChanges is the exit code of affected when stacks are affected and
	// of drift when the stack drifted.
	ExitChanges = 2
	// ExitRefused is the exit code of a command refused by the server, or of
	// an apply the CLI refuses because it cannot confirm it.
	ExitRefused = 3
)

const (
	// EnvServerURL is the server base URL; unset means local mode.
	EnvServerURL = "STACKORDER_SERVER_URL"
	// EnvAPIKey is the automation key used by apply --local and unlock.
	EnvAPIKey = "STACKORDER_API_KEY" //nolint:gosec
	// EnvRunID is the run id from the resolve step or the dispatch input.
	EnvRunID = "STACKORDER_RUN_ID"
	// EnvTool overrides the configured tool, terraform or tofu.
	EnvTool = "STACKORDER_TOOL"
	// EnvToolVersion overrides the configured tool version.
	EnvToolVersion = "STACKORDER_TOOL_VERSION"
	// EnvBackendConfig holds extra -backend-config values for init, comma
	// separated.
	EnvBackendConfig = "STACKORDER_BACKEND_CONFIG"
	// EnvPlanDir is where plan files are written; relative paths are taken
	// from the repository root.
	EnvPlanDir = "STACKORDER_PLAN_DIR"
	// EnvOIDCAudience overrides the audience of the runner's OIDC token,
	// which defaults to the server URL; it must equal the server's
	// STACKORDER_OIDC_AUDIENCE.
	EnvOIDCAudience = "STACKORDER_OIDC_AUDIENCE"
	// EnvLogFormat selects the log format: "json", or text otherwise.
	EnvLogFormat = "STACKORDER_LOG_FORMAT"
)

// DefaultPlanDir is the repository relative directory plan files are
// written to when STACKORDER_PLAN_DIR is unset.
const DefaultPlanDir = ".stackorder/plans"

const (
	formatText = "text"
	formatJSON = "json"
	formatDOT  = "dot"
)

// ExitError ends a command with a specific process exit code. Err may be nil
// for a code that is a result rather than a failure, such as ExitChanges.
type ExitError struct {
	// Code is the process exit code.
	Code int
	// Err is the reason, printed to stderr when non-nil.
	Err error
}

// Error implements the error interface.
func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

// Unwrap returns the reason.
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode maps an error returned by Run or Execute to the process exit
// code: the code of an *ExitError, ExitRefused for a server refusal, and
// ExitFailure for anything else.
func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	if errors.Is(err, client.ErrRefused) {
		return ExitRefused
	}
	return ExitFailure
}

func refused(format string, args ...any) error {
	return &ExitError{Code: ExitRefused, Err: fmt.Errorf(format, args...)}
}

func failed(format string, args ...any) error {
	return &ExitError{Code: ExitFailure, Err: fmt.Errorf(format, args...)}
}

// Execute runs the command line in os.Args with the process's standard
// streams. SIGINT and SIGTERM cancel the command's context, which forwards
// a single interrupt to a running terraform or tofu so it can release its
// state lock. Pass the result to ExitCode for the process exit code.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

// Run executes the stackorder command line with args, which exclude the
// program name. Tool output and text results go to stdout, logs and errors
// to stderr; with --format json, stdout carries only the JSON result. The
// error, if any, has already been printed to stderr.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	a := &app{stdout: stdout, stderr: stderr, log: slog.New(slog.DiscardHandler)}
	cmd := a.rootCommand()
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	err := cmd.ExecuteContext(ctx)
	if err != nil {
		a.printError(err)
	}
	return err
}

type app struct {
	stdout, stderr io.Writer
	log            *slog.Logger

	server   string
	repoRoot string
	verbose  bool
	format   string

	root string
	gh   *Context
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "stackorder",
		Short:         "Order and run Terraform and OpenTofu stacks on GitHub Actions",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return a.setup()
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	f := root.PersistentFlags()
	f.StringVar(&a.server, "server", os.Getenv(EnvServerURL), "server base URL; empty means local mode (env "+EnvServerURL+")")
	f.StringVar(&a.repoRoot, "repo-root", "", "repository root (default GITHUB_WORKSPACE, else the git top level)")
	f.BoolVarP(&a.verbose, "verbose", "v", false, "log debug output")
	f.StringVar(&a.format, "format", formatText, "output format: text, json, or dot where it applies")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &ExitError{Code: ExitFailure, Err: err}
	})
	root.AddCommand(a.commands()...)
	return root
}

func (a *app) commands() []*cobra.Command {
	return []*cobra.Command{
		a.versionCommand(),
	}
}

func (a *app) setup() error {
	level := slog.LevelInfo
	if a.verbose {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(os.Getenv(EnvLogFormat), "json") {
		h = slog.NewJSONHandler(a.stderr, opts)
	} else {
		opts.ReplaceAttr = func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		}
		h = slog.NewTextHandler(a.stderr, opts)
	}
	a.log = slog.New(h)
	a.server = strings.TrimSpace(a.server)
	switch a.format {
	case formatText, formatJSON, formatDOT:
		return nil
	}
	return failed("--format: %q is not one of text, json, dot", a.format)
}

func (a *app) requireFormat(dot bool) error {
	if a.format == formatDOT && !dot {
		return failed("--format dot is only supported by graph and affected")
	}
	return nil
}

func (a *app) toolOut() io.Writer {
	if a.format == formatJSON {
		return a.stderr
	}
	return a.stdout
}

func (a *app) repositoryRoot(ctx context.Context) (string, error) {
	if a.root != "" {
		return a.root, nil
	}
	dir := a.repoRoot
	if dir == "" {
		dir = os.Getenv("GITHUB_WORKSPACE")
	}
	if dir == "" {
		top, err := gitOutput(ctx, ".", "rev-parse", "--show-toplevel")
		if err != nil {
			a.log.Debug("not inside a git work tree; using the current directory as the repository root", "error", err)
			top = "."
		}
		dir = top
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("repository root %s: %w", dir, err)
	}
	info, err := os.Stat(abs) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("repository root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository root %s is not a directory", abs)
	}
	a.root = abs
	return abs, nil
}

func (a *app) github(ctx context.Context) (*Context, error) {
	if a.gh != nil {
		return a.gh, nil
	}
	root, err := a.repositoryRoot(ctx)
	if err != nil {
		return nil, err
	}
	gh, err := LoadContext(ctx, root)
	if err != nil {
		return nil, err
	}
	a.gh = gh
	return gh, nil
}

func (a *app) printError(err error) {
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Err == nil {
		return
	}
	msg := err.Error()
	if errors.Is(err, context.Canceled) {
		msg = "interrupted: " + msg
	}
	if inActions() {
		_, _ = fmt.Fprintf(a.stderr, "::error::%s\n", escapeCommand(msg))
		return
	}
	_, _ = fmt.Fprintf(a.stderr, "stackorder: %s\n", msg)
}

func (a *app) annotate(level, msg string) {
	if inActions() {
		_, _ = fmt.Fprintf(a.stderr, "::%s::%s\n", level, escapeCommand(msg))
	}
}

func (a *app) writeJSON(v any) error {
	return writeJSON(a.stdout, v)
}

func inActions() bool { return os.Getenv("GITHUB_ACTIONS") == "true" }

func escapeCommand(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}
