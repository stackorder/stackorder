package tf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tfjson "github.com/hashicorp/terraform-json"
)

// DefaultInterruptTimeout is how long a command may take to exit after an
// interrupt before it is killed, when Runner.InterruptTimeout is zero.
const DefaultInterruptTimeout = 2 * time.Minute

var automationEnv = []string{"TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1"}

// Runner runs terraform or tofu commands in one stack directory.
//
// Every command runs with TF_IN_AUTOMATION=1, TF_INPUT=0 and
// CHECKPOINT_DISABLE=1 on top of the process environment, then Env. When the
// context passed to a method is cancelled the command receives a single
// interrupt, so Terraform can release locks and persist state, and is killed
// only if it has not exited after InterruptTimeout. On Unix the command runs
// in its own process group so that a terminal Ctrl-C reaches it only through
// that forwarded interrupt; callers must cancel the context on SIGINT and
// SIGTERM.
type Runner struct {
	// Bin is the terraform or tofu binary, as returned by Detect.
	Bin string
	// Dir is the stack directory commands run in.
	Dir string
	// Env holds extra KEY=VALUE entries; they take precedence over the process
	// environment and the automation defaults.
	Env []string
	// Stdout and Stderr receive the command's streams as they are produced,
	// for job logs. Nil discards them. ShowJSON, ShowText and Output never
	// stream stdout, because it holds unredacted values.
	Stdout, Stderr io.Writer
	// Workspace is the workspace SelectWorkspace selects; empty means
	// "default".
	Workspace string
	// InterruptTimeout bounds the wait after an interrupt; zero means
	// DefaultInterruptTimeout.
	InterruptTimeout time.Duration
}

// InitOptions tunes Runner.Init.
type InitOptions struct {
	// Upgrade passes -upgrade.
	Upgrade bool
	// PluginCacheDir, when set, is created if needed and passed to init as
	// TF_PLUGIN_CACHE_DIR.
	PluginCacheDir string
	// Reconfigure passes -reconfigure.
	Reconfigure bool
}

// PlanOptions tunes Runner.Plan.
type PlanOptions struct {
	// Out is the plan file to write, relative to Runner.Dir unless absolute.
	Out string
	// DetailedExitCode reports a plan with changes as ExitCode 2. Without it
	// a successful plan reports ExitCode 0. HasChanges is set either way.
	DetailedExitCode bool
	// Refresh passes -refresh=<value> when non-nil.
	Refresh *bool
	// RefreshOnly passes -refresh-only.
	RefreshOnly bool
	// Targets are passed as -target=<address> each.
	Targets []string
	// Lock passes -lock=<value> when non-nil; nil keeps the tool default of
	// taking the state lock.
	Lock *bool
	// LockTimeout passes -lock-timeout=<duration> when positive.
	LockTimeout time.Duration
}

// PlanResult is the outcome of a plan that ran to completion or failed.
type PlanResult struct {
	// ExitCode is the tool's exit code, with 2 mapped to 0 unless
	// DetailedExitCode was requested.
	ExitCode int
	// HasChanges is true when the plan proposes changes.
	HasChanges bool
	// Output is stdout and stderr interleaved as produced.
	Output string
}

// ApplyOptions tunes Runner.Apply.
type ApplyOptions struct {
	// Lock passes -lock=<value> when non-nil; nil keeps the tool default.
	Lock *bool
	// LockTimeout passes -lock-timeout=<duration> when positive.
	LockTimeout time.Duration
}

// ApplyResult is the outcome of an apply that ran to completion or failed.
type ApplyResult struct {
	// Output is stdout and stderr interleaved as produced.
	Output string
	// ExitCode is the tool's exit code.
	ExitCode int
}

// ExitError reports a command that ran and exited unsuccessfully.
type ExitError struct {
	// Command is the binary name and subcommand, such as "terraform plan".
	Command string
	// ExitCode is the process exit code.
	ExitCode int
	// Output is the captured output: stdout and stderr interleaved for
	// commands whose stdout is streamed, stderr alone otherwise.
	Output string
}

// Error implements the error interface, quoting the first diagnostic line.
func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s exited with code %d", e.Command, e.ExitCode)
	if line := errorLine(e.Output); line != "" {
		msg += ": " + line
	}
	return msg
}

// Bool returns a pointer to v, for the optional flags of PlanOptions and
// ApplyOptions.
func Bool(v bool) *bool { return &v }

// Init runs `init -input=false -no-color` with one -backend-config=<v> per
// non-empty backendConfig value.
func (r *Runner) Init(ctx context.Context, backendConfig []string, opts InitOptions) error {
	args := []string{"init", "-input=false", "-no-color"}
	if opts.Upgrade {
		args = append(args, "-upgrade")
	}
	if opts.Reconfigure {
		args = append(args, "-reconfigure")
	}
	for _, v := range backendConfig {
		if v != "" {
			args = append(args, "-backend-config="+v)
		}
	}
	var env []string
	if opts.PluginCacheDir != "" {
		if err := os.MkdirAll(opts.PluginCacheDir, 0o750); err != nil {
			return fmt.Errorf("tf: creating plugin cache dir: %w", err)
		}
		env = append(env, "TF_PLUGIN_CACHE_DIR="+opts.PluginCacheDir)
	}
	_, err := r.stream(ctx, args, env)
	return err
}

// SelectWorkspace runs `workspace select -or-create <Workspace>`, creating
// the workspace when it does not exist yet.
func (r *Runner) SelectWorkspace(ctx context.Context) error {
	ws := r.Workspace
	if ws == "" {
		ws = "default"
	}
	_, err := r.stream(ctx, []string{"workspace", "select", "-or-create", ws}, nil)
	return err
}

// Plan runs `plan -input=false -no-color -detailed-exitcode` with the given
// options. An exit code of 2 means changes and is not an error. On failure the
// result is returned together with an *ExitError.
func (r *Runner) Plan(ctx context.Context, opts PlanOptions) (*PlanResult, error) {
	args := []string{"plan", "-input=false", "-no-color", "-detailed-exitcode"}
	if opts.Out != "" {
		args = append(args, "-out="+opts.Out)
	}
	if opts.Refresh != nil {
		args = append(args, "-refresh="+strconv.FormatBool(*opts.Refresh))
	}
	if opts.RefreshOnly {
		args = append(args, "-refresh-only")
	}
	for _, t := range opts.Targets {
		args = append(args, "-target="+t)
	}
	args = appendLock(args, opts.Lock, opts.LockTimeout)
	res, err := r.stream(ctx, args, nil)
	if res == nil {
		return nil, err
	}
	out := &PlanResult{ExitCode: res.code, Output: res.output}
	var exitErr *ExitError
	if res.code == 2 && errors.As(err, &exitErr) {
		out.HasChanges = true
		if !opts.DetailedExitCode {
			out.ExitCode = 0
		}
		return out, nil
	}
	return out, err
}

// ShowJSONRaw runs `show -json -no-color <planFile>` and returns stdout. The
// JSON holds sensitive values in clear text.
func (r *Runner) ShowJSONRaw(ctx context.Context, planFile string) ([]byte, error) {
	if planFile == "" {
		return nil, errors.New("tf: show: plan file is required")
	}
	return r.capture(ctx, []string{"show", "-json", "-no-color", planFile})
}

// ShowJSON runs ShowJSONRaw and decodes the result.
func (r *Runner) ShowJSON(ctx context.Context, planFile string) (*tfjson.Plan, error) {
	data, err := r.ShowJSONRaw(ctx, planFile)
	if err != nil {
		return nil, err
	}
	return ParsePlanJSON(data)
}

// ShowText runs `show -no-color <planFile>` and returns the human readable
// plan.
func (r *Runner) ShowText(ctx context.Context, planFile string) (string, error) {
	if planFile == "" {
		return "", errors.New("tf: show: plan file is required")
	}
	out, err := r.capture(ctx, []string{"show", "-no-color", planFile})
	return string(out), err
}

// Apply runs `apply -input=false -no-color <planFile>`. A saved plan never
// prompts, so no -auto-approve is passed. On failure the result is returned
// together with an *ExitError.
func (r *Runner) Apply(ctx context.Context, planFile string, opts ApplyOptions) (*ApplyResult, error) {
	if planFile == "" {
		return nil, errors.New("tf: apply: plan file is required")
	}
	args := appendLock([]string{"apply", "-input=false", "-no-color"}, opts.Lock, opts.LockTimeout)
	args = append(args, planFile)
	res, err := r.stream(ctx, args, nil)
	if res == nil {
		return nil, err
	}
	return &ApplyResult{Output: res.output, ExitCode: res.code}, err
}

// Output runs `output -json -no-color` and decodes the root module outputs.
func (r *Runner) Output(ctx context.Context) (map[string]tfjson.StateOutput, error) {
	data, err := r.capture(ctx, []string{"output", "-json", "-no-color"})
	if err != nil {
		return nil, err
	}
	outputs := map[string]tfjson.StateOutput{}
	if len(bytes.TrimSpace(data)) == 0 {
		return outputs, nil
	}
	if err := json.Unmarshal(data, &outputs); err != nil {
		return nil, fmt.Errorf("tf: decoding output -json: %w", err)
	}
	return outputs, nil
}

func appendLock(args []string, lock *bool, timeout time.Duration) []string {
	if lock != nil {
		args = append(args, "-lock="+strconv.FormatBool(*lock))
	}
	if timeout > 0 {
		args = append(args, "-lock-timeout="+timeout.String())
	}
	return args
}

type runResult struct {
	code   int
	output string
}

func (r *Runner) stream(ctx context.Context, args, env []string) (*runResult, error) {
	var mu sync.Mutex
	var combined bytes.Buffer
	stdout := &lockedWriter{mu: &mu, w: io.MultiWriter(&combined, orDiscard(r.Stdout))}
	stderr := &lockedWriter{mu: &mu, w: io.MultiWriter(&combined, orDiscard(r.Stderr))}
	code, err := r.run(ctx, args, env, stdout, stderr)
	if code < 0 && err != nil {
		return nil, err
	}
	res := &runResult{code: code, output: combined.String()}
	if err != nil {
		return res, err
	}
	if code != 0 {
		return res, &ExitError{Command: r.command(args), ExitCode: code, Output: res.output}
	}
	return res, nil
}

func (r *Runner) capture(ctx context.Context, args []string) ([]byte, error) {
	var stdout, captured bytes.Buffer
	stderr := io.MultiWriter(&captured, orDiscard(r.Stderr))
	code, err := r.run(ctx, args, nil, &stdout, stderr)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &ExitError{Command: r.command(args), ExitCode: code, Output: captured.String()}
	}
	return stdout.Bytes(), nil
}

func (r *Runner) run(ctx context.Context, args, env []string, stdout, stderr io.Writer) (int, error) {
	if r.Bin == "" {
		return -1, errors.New("tf: runner has no binary; use Detect")
	}
	cmd := exec.CommandContext(ctx, r.Bin, args...) //nolint:gosec
	cmd.Dir = r.Dir
	cmd.Env = append(append(append(processEnv(r.Dir), automationEnv...), r.Env...), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	timeout := r.InterruptTimeout
	if timeout <= 0 {
		timeout = DefaultInterruptTimeout
	}
	configureInterrupt(cmd, timeout)
	err := cmd.Run()
	if ctx.Err() != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return code, fmt.Errorf("tf: %s interrupted: %w", r.command(args), ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("tf: running %s: %w", r.command(args), err)
	}
	return 0, nil
}

func (r *Runner) command(args []string) string {
	name := strings.TrimSuffix(filepath.Base(r.Bin), ".exe")
	if len(args) == 0 {
		return name
	}
	if args[0] == "workspace" && len(args) > 1 {
		return name + " workspace " + args[1]
	}
	return name + " " + args[0]
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func processEnv(dir string) []string {
	env := os.Environ()
	if dir == "" {
		return env
	}
	if abs, err := filepath.Abs(dir); err == nil {
		env = append(env, "PWD="+abs)
	}
	return env
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func errorLine(output string) string {
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "│╷╵"))
		if strings.HasPrefix(line, "Error:") {
			return line
		}
	}
	return lastLine(output)
}

func lastLine(s string) string {
	last := ""
	for line := range strings.Lines(s) {
		if t := strings.TrimSpace(line); t != "" {
			last = t
		}
	}
	return last
}

func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
