// Package faketf is a stand-in for the terraform and tofu binaries in tests
// that run the stackorder CLI end to end. Build compiles it into a directory
// holding both names, to be put first on PATH. On every call the binary
// reads a Config from the JSON file named by EnvConfig and answers version,
// init, workspace, plan, show, apply and output with the Behavior configured
// for the stack it runs for, named by EnvStack or else by its directory:
// plan and apply exit codes, a canned show -json fixture, and an apply that
// can wait for a file so a test can observe an apply in flight. Every call is
// recorded with its arguments and the TF_VAR_ and STACKORDER_ variables of
// its environment.
package faketf

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	// EnvConfig names the variable holding the path of the Config file.
	EnvConfig = "STACKORDER_FAKETF_CONFIG"
	// EnvStack names the variable the CLI sets to the stack key.
	EnvStack = "STACKORDER_STACK"
	// DefaultVersion is the version reported when Config.Version is empty.
	DefaultVersion = "1.14.0"
	// ApplyWaitTimeout bounds how long an apply waits for its
	// Behavior.ApplyWaitFile.
	ApplyWaitTimeout = 2 * time.Minute
	// ExitMisconfigured is the exit code of a call the Config cannot answer,
	// such as a missing config file or show -json fixture.
	ExitMisconfigured = 97
)

// Package is the import path of the command Build compiles.
const Package = "github.com/stackorder/stackorder/internal/testutil/faketf/cmd/faketf"

// Config is what the fake binary does, read afresh on every call.
type Config struct {
	// Log, when set, receives one JSON encoded Call per invocation.
	Log string `json:"log,omitempty"`
	// Tofu makes version answer as OpenTofu instead of Terraform.
	Tofu bool `json:"tofu,omitempty"`
	// Version is the reported version, DefaultVersion when empty.
	Version string `json:"version,omitempty"`
	// Default is the behaviour of stacks Stacks does not name.
	Default Behavior `json:"default"`
	// Stacks maps stack keys ("path" or "path:instance") and stack
	// directories, slash separated and relative to the repository root, to
	// their behaviour. A call whose EnvStack names a key of Stacks uses it;
	// any other call uses the longest key its working directory ends with.
	Stacks map[string]Behavior `json:"stacks,omitempty"`
}

// Behavior is how the fake answers in one stack directory.
type Behavior struct {
	// InitExit is the exit code of init.
	InitExit int `json:"init_exit,omitempty"`
	// PlanExit is the exit code of plan: 0 for no changes, 2 for changes
	// under -detailed-exitcode, anything else a failure. A plan that does
	// not fail writes its -out file.
	PlanExit int `json:"plan_exit,omitempty"`
	// ShowJSON is the file whose contents show -json prints.
	ShowJSON string `json:"show_json,omitempty"`
	// ApplyExit is the exit code of apply.
	ApplyExit int `json:"apply_exit,omitempty"`
	// ApplyWaitFile, when set, makes apply wait until the file exists.
	ApplyWaitFile string `json:"apply_wait_file,omitempty"`
}

// Call is one recorded invocation.
type Call struct {
	// Dir is the working directory, slash separated.
	Dir string `json:"dir"`
	// Args are the arguments after the program name.
	Args []string `json:"args"`
	// Env holds the variables of the call's environment whose names start
	// with TF_VAR_ or STACKORDER_, except EnvConfig.
	Env map[string]string `json:"env,omitempty"`
}

// Stack returns the stack key the call ran for, from EnvStack, or "".
func (c Call) Stack() string { return c.Env[EnvStack] }

// For returns the behaviour for a stack key, when Stacks names it, else for
// the working directory.
func (c Config) For(key, dir string) Behavior {
	if b, ok := c.Stacks[key]; ok && key != "" {
		return b
	}
	return c.Stack(dir)
}

// Stack returns the behaviour for a working directory.
func (c Config) Stack(dir string) Behavior {
	dir = strings.TrimRight(filepath.ToSlash(dir), "/")
	best, found := -1, c.Default
	for key, b := range c.Stacks {
		key = strings.Trim(key, "/")
		if (dir == key || strings.HasSuffix(dir, "/"+key)) && len(key) > best {
			best, found = len(key), b
		}
	}
	return found
}

// Main runs the fake with the process's arguments, environment, working
// directory and standard streams, and returns the exit code.
func Main() int {
	dir, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "faketf:", err)
		return ExitMisconfigured
	}
	return Run(os.Args[1:], os.Environ(), dir, os.Stdout, os.Stderr)
}

// Run answers one invocation with args in dir under the environment
// environ, as "NAME=value" entries, and returns its exit code.
func Run(args []string, environ []string, dir string, stdout, stderr io.Writer) int {
	env := recordedEnv(environ)
	cfg, err := readConfig(lookup(environ, EnvConfig))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "faketf:", err)
		return ExitMisconfigured
	}
	if err := record(cfg.Log, Call{Dir: filepath.ToSlash(dir), Args: args, Env: env}); err != nil {
		_, _ = fmt.Fprintln(stderr, "faketf:", err)
		return ExitMisconfigured
	}
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "Usage: terraform [global options] <subcommand> [args]")
		return 1
	}
	b := cfg.For(env[EnvStack], dir)
	switch args[0] {
	case "version":
		return version(cfg, stdout)
	case "init":
		_, _ = fmt.Fprintln(stdout, "Terraform has been successfully initialized!")
		return failWith(stderr, b.InitExit, "Failed to initialize the backend")
	case "workspace":
		_, _ = fmt.Fprintf(stdout, "Switched to workspace %q.\n", args[len(args)-1])
	case "plan":
		return plan(args, b, stdout, stderr)
	case "show":
		return show(args, b, stdout, stderr)
	case "apply":
		return apply(args, b, stdout, stderr)
	case "output":
		_, _ = fmt.Fprintln(stdout, "{}")
	}
	return 0
}

func lookup(environ []string, name string) string {
	value := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value = v
		}
	}
	return value
}

func recordedEnv(environ []string) map[string]string {
	var out map[string]string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == EnvConfig || !strings.HasPrefix(k, "TF_VAR_") && !strings.HasPrefix(k, "STACKORDER_") {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

func readConfig(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, fmt.Errorf("%s is not set", EnvConfig)
	}
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return cfg, fmt.Errorf("reading the config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("decoding %s: %w", path, err)
	}
	return cfg, nil
}

func record(log string, c Call) error {
	if log == "" {
		return nil
	}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec
	if err != nil {
		return fmt.Errorf("opening the call log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing the call log: %w", err)
	}
	return f.Close()
}

func failWith(stderr io.Writer, code int, msg string) int {
	if code != 0 {
		_, _ = fmt.Fprintf(stderr, "\nError: %s\n", msg)
	}
	return code
}

func version(cfg Config, stdout io.Writer) int {
	v := cfg.Version
	if v == "" {
		v = DefaultVersion
	}
	out := map[string]any{"terraform_version": v, "platform": runtime.GOOS + "_" + runtime.GOARCH, "provider_selections": map[string]string{}}
	if !cfg.Tofu {
		out["terraform_outdated"] = false
	}
	data, _ := json.Marshal(out)
	_, _ = fmt.Fprintln(stdout, string(data))
	return 0
}

func plan(args []string, b Behavior, stdout, stderr io.Writer) int {
	switch b.PlanExit {
	case 0:
		_, _ = fmt.Fprintln(stdout, "No changes. Your infrastructure matches the configuration.")
	case 2:
		_, _ = fmt.Fprintln(stdout, "Terraform will perform the actions described in the saved plan.")
	default:
		return failWith(stderr, b.PlanExit, "Invalid provider configuration")
	}
	for _, a := range args {
		if out, ok := strings.CutPrefix(a, "-out="); ok {
			if err := os.WriteFile(out, []byte("faketf plan\n"), 0o600); err != nil { //nolint:gosec
				_, _ = fmt.Fprintln(stderr, "faketf:", err)
				return ExitMisconfigured
			}
		}
	}
	return b.PlanExit
}

func planFileArg(args []string) (string, bool) {
	if len(args) < 2 {
		return "", false
	}
	f := args[len(args)-1]
	if strings.HasPrefix(f, "-") {
		return "", false
	}
	info, err := os.Stat(f) //nolint:gosec
	return f, err == nil && info.Mode().IsRegular()
}

func show(args []string, b Behavior, stdout, stderr io.Writer) int {
	f, ok := planFileArg(args)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "Error: Failed to read the given file %s as a state or plan file\n", f)
		return 1
	}
	data, err := os.ReadFile(b.ShowJSON)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "faketf: show fixture:", err)
		return ExitMisconfigured
	}
	if slices.Contains(args, "-json") {
		_, _ = stdout.Write(data)
		return 0
	}
	text, err := planText(data)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "faketf: show fixture:", err)
		return ExitMisconfigured
	}
	_, _ = io.WriteString(stdout, text)
	return 0
}

func planText(fixture []byte) (string, error) {
	var p struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(fixture, &p); err != nil {
		return "", err
	}
	verbs := map[string]string{"create": "created", "update": "updated in-place", "delete": "destroyed", "delete,create": "replaced", "create,delete": "replaced"}
	var b strings.Builder
	for _, rc := range p.ResourceChanges {
		if verb, ok := verbs[strings.Join(rc.Change.Actions, ",")]; ok {
			fmt.Fprintf(&b, "  # %s will be %s\n", rc.Address, verb)
		}
	}
	if b.Len() == 0 {
		return "No changes. Your infrastructure matches the configuration.\n", nil
	}
	return "Terraform will perform the following actions:\n\n" + b.String(), nil
}

func apply(args []string, b Behavior, stdout, stderr io.Writer) int {
	f, ok := planFileArg(args)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "Error: Failed to load %q as a plan file\n", f)
		return 1
	}
	if b.ApplyWaitFile != "" {
		if err := waitFor(b.ApplyWaitFile, ApplyWaitTimeout); err != nil {
			_, _ = fmt.Fprintln(stderr, "faketf:", err)
			return ExitMisconfigured
		}
	}
	if b.ApplyExit != 0 {
		return failWith(stderr, b.ApplyExit, "creating resource: operation error: AccessDenied")
	}
	_, _ = fmt.Fprintln(stdout, "Apply complete!")
	return 0
}

func waitFor(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear within %s", path, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Build compiles the fake into a directory of t's and returns the
// directory, which holds it as terraform and as tofu.
func Build(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	bin := filepath.Join(dir, "terraform"+exe)
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, Package) //nolint:gosec
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("faketf: go build %s: %v\n%s", Package, err, out)
	}
	data, err := os.ReadFile(bin) //nolint:gosec
	if err != nil {
		t.Fatalf("faketf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tofu"+exe), data, 0o755); err != nil { //nolint:gosec
		t.Fatalf("faketf: %v", err)
	}
	return dir
}

// WriteConfig writes cfg to a new file in a directory of t's and returns
// its path, the value for EnvConfig.
func WriteConfig(t testing.TB, cfg Config) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("faketf: encoding the config: %v", err)
	}
	f, err := os.CreateTemp(t.TempDir(), "faketf-*.json")
	if err != nil {
		t.Fatalf("faketf: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatalf("faketf: writing the config: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("faketf: writing the config: %v", err)
	}
	return f.Name()
}

// Calls reads the calls recorded in log, oldest first; a missing log holds
// none.
func Calls(t testing.TB, log string) []Call {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("faketf: reading the call log: %v", err)
	}
	var out []Call
	for line := range strings.Lines(string(data)) {
		var c Call
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("faketf: decoding the call log: %v", err)
		}
		out = append(out, c)
	}
	return out
}
