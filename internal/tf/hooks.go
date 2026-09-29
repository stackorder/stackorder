package tf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
)

const (
	// HooksDir is the repository relative directory holding hook scripts.
	HooksDir = ".stackorder/hooks"
	// HookPrePlan runs before plan.
	HookPrePlan = "pre-plan"
	// HookPostPlan runs after plan, with the plan JSON available.
	HookPostPlan = "post-plan"
	// HookPreApply runs before apply.
	HookPreApply = "pre-apply"
	// HookPostApply runs after apply.
	HookPostApply = "post-apply"
)

const (
	// HookEnvStack names the variable holding the stack key.
	HookEnvStack = "STACKORDER_STACK"
	// HookEnvStackPath names the variable holding the stack directory,
	// repository relative.
	HookEnvStackPath = "STACKORDER_STACK_PATH"
	// HookEnvInstance names the variable holding the stack's instance name,
	// empty for a stack without one.
	HookEnvInstance = "STACKORDER_INSTANCE"
	// HookEnvRunID names the variable holding the server run id.
	HookEnvRunID = "STACKORDER_RUN_ID"
	// HookEnvPlanJSON names the variable holding the path of the plan JSON.
	HookEnvPlanJSON = "STACKORDER_PLAN_JSON"
	// HookEnvPlanFile names the variable holding the path of the binary plan.
	HookEnvPlanFile = "STACKORDER_PLAN_FILE"
)

var (
	// ErrHookNotExecutable reports a hook script that exists but lacks the
	// executable bit. It is an error rather than a skip so that a policy hook
	// never stops running unnoticed.
	ErrHookNotExecutable = errors.New("tf: hook is not executable")
	// ErrInvalidHookName reports a hook name that is not lower case letters,
	// digits and dashes.
	ErrInvalidHookName = errors.New("tf: invalid hook name")
)

var hookName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// HookPath returns the path of the hook script name under repoRoot.
func HookPath(repoRoot, name string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(HooksDir), name+".sh")
}

// RunHook runs HookPath(repoRoot, name) with bash in repoRoot, with env added
// to the process environment, streaming its output to stdout and stderr (nil
// discards). It returns ran == false and no error when the script does not
// exist. A script without the executable bit is refused with
// ErrHookNotExecutable, except on Windows, which has no such bit. A non-zero
// exit is returned as an *ExitError.
func RunHook(ctx context.Context, repoRoot, name string, env map[string]string, stdout, stderr io.Writer) (bool, error) {
	if !hookName.MatchString(name) {
		return false, fmt.Errorf("%w: %q", ErrInvalidHookName, name)
	}
	path := HookPath(repoRoot, name)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("tf: hook %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("tf: hook %s: %s is not a regular file", name, path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return false, fmt.Errorf("%w: %s; run chmod +x on it or remove it", ErrHookNotExecutable, path)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return false, fmt.Errorf("tf: hook %s: bash not found: %w", name, err)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	cmd := exec.CommandContext(ctx, bash, path) //nolint:gosec
	cmd.Dir = repoRoot
	cmd.Env = processEnv(repoRoot)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.Stdout = orDiscard(stdout)
	cmd.Stderr = orDiscard(stderr)
	configureInterrupt(cmd, DefaultInterruptTimeout)
	err = cmd.Run()
	if ctx.Err() != nil {
		return true, fmt.Errorf("tf: hook %s interrupted: %w", name, ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return true, &ExitError{Command: "hook " + name, ExitCode: exitErr.ExitCode()}
	}
	if err != nil {
		return true, fmt.Errorf("tf: hook %s: %w", name, err)
	}
	return true, nil
}
