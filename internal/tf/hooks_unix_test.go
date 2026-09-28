//go:build unix

package tf

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeHook(t *testing.T, root, name, body string, mode os.FileMode) {
	t.Helper()
	p := HookPath(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), mode))
}

func TestRunHook(t *testing.T) {
	tests := []struct {
		name       string
		hook       string
		setup      func(t *testing.T, root string)
		env        map[string]string
		wantRan    bool
		wantErr    error
		wantExit   int
		wantStdout string
		wantStderr string
	}{
		{
			name:  "missing hook",
			hook:  HookPrePlan,
			setup: func(*testing.T, string) {},
		},
		{
			name: "success with env and cwd",
			hook: HookPostPlan,
			setup: func(t *testing.T, root string) {
				writeHook(t, root, HookPostPlan, "set -eu\necho \"stack=$STACKORDER_STACK run=$STACKORDER_RUN_ID\"\n[ -f stackorder.yaml ] && echo in-root\necho warn >&2\n", 0o755)
				require.NoError(t, os.WriteFile(filepath.Join(root, "stackorder.yaml"), []byte("version: 1\n"), 0o600))
			},
			env:        map[string]string{"STACKORDER_STACK": "stacks/prod/vpc", "STACKORDER_RUN_ID": "run-1"},
			wantRan:    true,
			wantStdout: "stack=stacks/prod/vpc run=run-1\nin-root\n",
			wantStderr: "warn\n",
		},
		{
			name: "bash features without shebang",
			hook: HookPreApply,
			setup: func(t *testing.T, root string) {
				writeHook(t, root, HookPreApply, "arr=(a b c)\necho \"${#arr[@]}\"\n", 0o755)
			},
			wantRan:    true,
			wantStdout: "3\n",
		},
		{
			name: "failing hook",
			hook: HookPostApply,
			setup: func(t *testing.T, root string) {
				writeHook(t, root, HookPostApply, "echo policy failed >&2\nexit 3\n", 0o755)
			},
			wantRan:    true,
			wantExit:   3,
			wantStderr: "policy failed\n",
		},
		{
			name: "not executable",
			hook: HookPrePlan,
			setup: func(t *testing.T, root string) {
				writeHook(t, root, HookPrePlan, "echo should not run\n", 0o644)
			},
			wantErr: ErrHookNotExecutable,
		},
		{
			name:    "invalid name",
			hook:    "../escape",
			setup:   func(*testing.T, string) {},
			wantErr: ErrInvalidHookName,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			var stdout, stderr bytes.Buffer
			ran, err := RunHook(context.Background(), root, tt.hook, tt.env, &stdout, &stderr)
			assert.Equal(t, tt.wantRan, ran)
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantExit != 0:
				var exitErr *ExitError
				require.ErrorAs(t, err, &exitErr)
				assert.Equal(t, tt.wantExit, exitErr.ExitCode)
				assert.Equal(t, "hook "+tt.hook, exitErr.Command)
			default:
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantStdout, stdout.String())
			assert.Equal(t, tt.wantStderr, stderr.String())
		})
	}
}

func TestRunHookDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(HookPath(root, HookPrePlan), 0o755))
	ran, err := RunHook(context.Background(), root, HookPrePlan, nil, nil, nil)
	assert.False(t, ran)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}

func TestRunHookNilWriters(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, HookPrePlan, "echo out\necho err >&2\n", 0o755)
	ran, err := RunHook(context.Background(), root, HookPrePlan, nil, nil, nil)
	require.NoError(t, err)
	assert.True(t, ran)
}

func TestHookPath(t *testing.T) {
	assert.Equal(t, filepath.Join("/repo", ".stackorder", "hooks", "pre-plan.sh"), HookPath("/repo", HookPrePlan))
}
