package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/client"
	"github.com/stackorder/stackorder/internal/version"
)

func TestExitCode(t *testing.T) {
	refusal := fmt.Errorf("posting: %w", &client.Error{Status: 423, Code: client.CodeLocked})
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", err: nil, want: ExitSuccess},
		{name: "exit error", err: &ExitError{Code: ExitChanges}, want: ExitChanges},
		{name: "wrapped exit error", err: fmt.Errorf("x: %w", refused("no")), want: ExitRefused},
		{name: "server refusal", err: refusal, want: ExitRefused},
		{name: "joined refusal", err: errors.Join(errors.New("a"), refusal), want: ExitRefused},
		{name: "plain error", err: errors.New("boom"), want: ExitFailure},
		{name: "unreachable", err: fmt.Errorf("x: %w", client.ErrUnreachable), want: ExitFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExitCode(tt.err))
		})
	}
}

func TestExitError(t *testing.T) {
	assert.Equal(t, "exit status 2", (&ExitError{Code: 2}).Error())
	inner := errors.New("inner")
	e := &ExitError{Code: 1, Err: inner}
	assert.Equal(t, "inner", e.Error())
	assert.ErrorIs(t, e, inner)
}

func TestVersion(t *testing.T) {
	clearEnv(t)
	r := runCLI(t, "version")
	require.Equal(t, 0, r.code)
	assert.Equal(t, "stackorder "+version.String()+"\n", r.stdout)

	r = runCLI(t, "--format", "json", "version")
	require.Equal(t, 0, r.code)
	var v versionInfo
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &v))
	assert.Equal(t, versionInfo{Version: version.Version, Commit: version.Commit, Date: version.Date, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}, v)

	r = runCLI(t, "--format", "dot", "version")
	assert.Equal(t, ExitFailure, r.code)
}

func TestGlobalFlagsAndErrors(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		ci    bool
		want  int
		check func(t *testing.T, r result)
	}{
		{
			name: "unknown format",
			args: []string{"--format", "yaml", "version"},
			want: ExitFailure,
			check: func(t *testing.T, r result) {
				assert.Equal(t, "stackorder: --format: \"yaml\" is not one of text, json, dot\n", r.stderr)
			},
		},
		{
			name: "unknown command",
			args: []string{"deploy"},
			want: ExitFailure,
			check: func(t *testing.T, r result) {
				assert.Contains(t, r.stderr, `unknown command "deploy"`)
			},
		},
		{
			name: "unknown flag",
			args: []string{"version", "--nope"},
			want: ExitFailure,
			check: func(t *testing.T, r result) {
				assert.Contains(t, r.stderr, "unknown flag: --nope")
			},
		},
		{
			name: "errors are workflow commands in actions",
			args: []string{"--format", "100%\nbad", "version"},
			ci:   true,
			want: ExitFailure,
			check: func(t *testing.T, r result) {
				assert.Equal(t, "::error::--format: \"100%25\\nbad\" is not one of text, json, dot\n", r.stderr)
			},
		},
		{
			name: "help lists every command",
			args: []string{"--help"},
			want: 0,
			check: func(t *testing.T, r result) {
				cmds := (&app{}).commands()
				require.NotEmpty(t, cmds)
				for _, cmd := range cmds {
					assert.Contains(t, r.stdout, "  "+cmd.Name()+" ")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			if tt.ci {
				t.Setenv("GITHUB_ACTIONS", "true")
			}
			r := runCLI(t, tt.args...)
			require.Equal(t, tt.want, r.code, r.stderr)
			tt.check(t, r)
		})
	}
}

func TestBindIgnoresNilFields(t *testing.T) {
	oldScan, oldChanged, oldResolve, oldDOT := scanRepo, changedPaths, resolveLocal, renderDOT
	t.Cleanup(func() {
		scanRepo, changedPaths, resolveLocal, renderDOT = oldScan, oldChanged, oldResolve, oldDOT
	})
	Bind(Deps{})
	_, err := scanRepo(context.Background(), ".", "", "", nil)
	require.ErrorIs(t, err, ErrScanNotLinked)
	_, err = changedPaths(context.Background(), ".", "a", "b")
	require.ErrorIs(t, err, ErrScanNotLinked)
	_, err = resolveLocal(nil, nil, nil, nil)
	require.ErrorIs(t, err, ErrGraphNotLinked)
	assert.Empty(t, renderDOT(nil, nil))
}

func TestExecute(t *testing.T) {
	clearEnv(t)
	oldArgs, oldStdout := os.Args, os.Stdout
	t.Cleanup(func() { os.Args, os.Stdout = oldArgs, oldStdout })
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)
	os.Args = []string{"stackorder", "version"}
	os.Stdout = out
	require.NoError(t, Execute())
	require.NoError(t, out.Close())
	data, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	assert.Equal(t, "stackorder "+version.String()+"\n", string(data))
}
