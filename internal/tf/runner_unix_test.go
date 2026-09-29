//go:build unix

package tf

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitArgv(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "plugins", "cache")
	tests := []struct {
		name          string
		backendConfig []string
		opts          InitOptions
		wantArgs      []string
		wantEnv       map[string]string
	}{
		{
			name:     "defaults",
			wantArgs: []string{"init", "-input=false", "-no-color"},
		},
		{
			name:          "backend config and flags",
			backendConfig: []string{"bucket=state", "", "key=stacks/prod/vpc.tfstate", "region=eu-west-1"},
			opts:          InitOptions{Upgrade: true, Reconfigure: true},
			wantArgs: []string{
				"init", "-input=false", "-no-color", "-upgrade", "-reconfigure",
				"-backend-config=bucket=state", "-backend-config=key=stacks/prod/vpc.tfstate", "-backend-config=region=eu-west-1",
			},
		},
		{
			name:     "plugin cache",
			opts:     InitOptions{PluginCacheDir: cacheDir},
			wantArgs: []string{"init", "-input=false", "-no-color"},
			wantEnv:  map[string]string{"TF_PLUGIN_CACHE_DIR": cacheDir},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTF(t, "terraform")
			require.NoError(t, f.runner().Init(f.ctx(), tt.backendConfig, tt.opts))
			call := f.lastCall()
			assert.Equal(t, tt.wantArgs, call.Args)
			assert.Equal(t, "1", call.Env["TF_IN_AUTOMATION"])
			assert.Equal(t, "0", call.Env["TF_INPUT"])
			assert.Equal(t, "1", call.Env["CHECKPOINT_DISABLE"])
			for k, v := range tt.wantEnv {
				assert.Equal(t, v, call.Env[k], k)
			}
			if tt.opts.PluginCacheDir == "" {
				assert.NotContains(t, call.Env, "TF_PLUGIN_CACHE_DIR")
			} else {
				assert.DirExists(t, tt.opts.PluginCacheDir)
			}
		})
	}
}

func TestRunnerDirAndEnv(t *testing.T) {
	f := newFakeTF(t, "tofu")
	r := f.runner("FAKE_EXTRA=value", "TF_IN_AUTOMATION=override")
	require.NoError(t, r.Init(f.ctx(), nil, InitOptions{}))
	call := f.lastCall()
	wantDir, err := filepath.EvalSymlinks(f.dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(call.Dir)
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)
	assert.Equal(t, "value", call.Env["FAKE_EXTRA"])
	assert.Equal(t, "override", call.Env["TF_IN_AUTOMATION"])
}

func TestSelectWorkspaceArgv(t *testing.T) {
	tests := []struct {
		workspace string
		want      []string
	}{
		{"", []string{"workspace", "select", "-or-create", "default"}},
		{"default", []string{"workspace", "select", "-or-create", "default"}},
		{"blue", []string{"workspace", "select", "-or-create", "blue"}},
	}
	for _, tt := range tests {
		t.Run(tt.workspace, func(t *testing.T) {
			f := newFakeTF(t, "terraform")
			r := f.runner()
			r.Workspace = tt.workspace
			require.NoError(t, r.SelectWorkspace(f.ctx()))
			assert.Equal(t, tt.want, f.lastCall().Args)
		})
	}
}

func TestPlanArgv(t *testing.T) {
	base := []string{"plan", "-input=false", "-no-color", "-detailed-exitcode"}
	tests := []struct {
		name string
		opts PlanOptions
		want []string
	}{
		{name: "defaults", want: base},
		{
			name: "out and detailed exit code",
			opts: PlanOptions{Out: "plan.tfplan", DetailedExitCode: true},
			want: append(append([]string{}, base...), "-out=plan.tfplan"),
		},
		{
			name: "refresh false, targets and lock",
			opts: PlanOptions{
				Refresh:     Bool(false),
				Targets:     []string{"aws_s3_bucket.logs", `module.vpc.aws_subnet.private["a"]`},
				Lock:        Bool(false),
				LockTimeout: 90 * time.Second,
			},
			want: append(append([]string{}, base...),
				"-refresh=false", "-target=aws_s3_bucket.logs", `-target=module.vpc.aws_subnet.private["a"]`,
				"-lock=false", "-lock-timeout=1m30s"),
		},
		{
			name: "refresh only with explicit refresh and lock",
			opts: PlanOptions{Refresh: Bool(true), RefreshOnly: true, Lock: Bool(true)},
			want: append(append([]string{}, base...), "-refresh=true", "-refresh-only", "-lock=true"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTF(t, "terraform")
			_, err := f.runner().Plan(f.ctx(), tt.opts)
			require.NoError(t, err)
			if diff := cmp.Diff(tt.want, f.lastCall().Args); diff != "" {
				t.Fatalf("argv mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPlanExitCodes(t *testing.T) {
	tests := []struct {
		name         string
		exit         string
		detailed     bool
		wantCode     int
		wantChanges  bool
		wantErr      bool
		wantErrorMsg string
	}{
		{name: "no changes", exit: "0", detailed: true, wantCode: 0},
		{name: "changes detailed", exit: "2", detailed: true, wantCode: 2, wantChanges: true},
		{name: "changes not detailed", exit: "2", detailed: false, wantCode: 0, wantChanges: true},
		{name: "no changes not detailed", exit: "0", detailed: false, wantCode: 0},
		{name: "error", exit: "1", detailed: true, wantCode: 1, wantErr: true, wantErrorMsg: "terraform plan exited with code 1: Error: Unsupported argument"},
		{name: "error not detailed", exit: "1", detailed: false, wantCode: 1, wantErr: true, wantErrorMsg: "terraform plan exited with code 1: Error: Unsupported argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTF(t, "terraform")
			var stdout, stderr bytes.Buffer
			r := f.runner(
				"FAKE_TF_EXIT="+tt.exit,
				f.stdoutFile("Terraform will perform the following actions:\n"),
				"FAKE_TF_STDERR=╷\n│ Error: Unsupported argument\n╵",
			)
			r.Stdout, r.Stderr = &stdout, &stderr
			res, err := r.Plan(f.ctx(), PlanOptions{DetailedExitCode: tt.detailed})
			require.NotNil(t, res)
			assert.Equal(t, tt.wantCode, res.ExitCode)
			assert.Equal(t, tt.wantChanges, res.HasChanges)
			assert.Contains(t, res.Output, "Terraform will perform the following actions:")
			assert.Contains(t, res.Output, "Error: Unsupported argument")
			assert.Equal(t, "Terraform will perform the following actions:\n", stdout.String())
			assert.Contains(t, stderr.String(), "Error: Unsupported argument")
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, 1, exitErr.ExitCode)
			assert.Equal(t, "terraform plan", exitErr.Command)
			assert.Equal(t, res.Output, exitErr.Output)
			assert.EqualError(t, err, tt.wantErrorMsg)
		})
	}
}

func TestShowJSON(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "create.json"))
	require.NoError(t, err)
	f := newFakeTF(t, "tofu")
	var stdout bytes.Buffer
	r := f.runner(f.stdoutFile(string(fixture)))
	r.Stdout = &stdout
	p, err := r.ShowJSON(f.ctx(), "plan.tfplan")
	require.NoError(t, err)
	assert.Equal(t, []string{"show", "-json", "-no-color", "plan.tfplan"}, f.lastCall().Args)
	assert.Equal(t, 2, Summarize(p).Adds)
	assert.Empty(t, stdout.String(), "plan JSON must not be streamed to the log")
}

func TestShowJSONErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     func(f *fakeTF) []string
		file    string
		wantErr string
	}{
		{
			name:    "missing plan file argument",
			env:     func(*fakeTF) []string { return nil },
			wantErr: "plan file is required",
		},
		{
			name: "tool failure",
			env: func(*fakeTF) []string {
				return []string{"FAKE_TF_EXIT=1", "FAKE_TF_STDERR=Error: Failed to read the given file as a state or plan file"}
			},
			file:    "missing.tfplan",
			wantErr: "tofu show exited with code 1: Error: Failed to read the given file as a state or plan file",
		},
		{
			name:    "not JSON",
			env:     func(f *fakeTF) []string { return []string{f.stdoutFile("not json")} },
			file:    "plan.tfplan",
			wantErr: "decoding plan JSON",
		},
		{
			name:    "unsupported format version",
			env:     func(f *fakeTF) []string { return []string{f.stdoutFile(`{"format_version":"9.0"}`)} },
			file:    "plan.tfplan",
			wantErr: "unsupported plan format version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTF(t, "tofu")
			_, err := f.runner(tt.env(f)...).ShowJSON(f.ctx(), tt.file)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestShowText(t *testing.T) {
	f := newFakeTF(t, "terraform")
	var stdout bytes.Buffer
	r := f.runner(f.stdoutFile("  # terraform_data.example will be created\n"))
	r.Stdout = &stdout
	text, err := r.ShowText(f.ctx(), "/abs/plan.tfplan")
	require.NoError(t, err)
	assert.Equal(t, "  # terraform_data.example will be created\n", text)
	assert.Equal(t, []string{"show", "-no-color", "/abs/plan.tfplan"}, f.lastCall().Args)
	assert.Empty(t, stdout.String())

	_, err = r.ShowText(f.ctx(), "")
	require.Error(t, err)
}

func TestApply(t *testing.T) {
	tests := []struct {
		name     string
		opts     ApplyOptions
		exit     string
		wantArgs []string
		wantCode int
		wantErr  bool
	}{
		{
			name:     "defaults",
			exit:     "0",
			wantArgs: []string{"apply", "-input=false", "-no-color", "plan.tfplan"},
		},
		{
			name:     "lock options",
			opts:     ApplyOptions{Lock: Bool(true), LockTimeout: 5 * time.Minute},
			exit:     "0",
			wantArgs: []string{"apply", "-input=false", "-no-color", "-lock=true", "-lock-timeout=5m0s", "plan.tfplan"},
		},
		{
			name:     "failure",
			exit:     "1",
			wantArgs: []string{"apply", "-input=false", "-no-color", "plan.tfplan"},
			wantCode: 1,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTF(t, "terraform")
			r := f.runner("FAKE_TF_EXIT="+tt.exit, f.stdoutFile("Apply complete! Resources: 1 added, 0 changed, 0 destroyed.\n"))
			res, err := r.Apply(f.ctx(), "plan.tfplan", tt.opts)
			require.NotNil(t, res)
			assert.Equal(t, tt.wantArgs, f.lastCall().Args)
			assert.Equal(t, tt.wantCode, res.ExitCode)
			assert.Contains(t, res.Output, "Apply complete!")
			if tt.wantErr {
				var exitErr *ExitError
				require.ErrorAs(t, err, &exitErr)
				assert.Equal(t, "terraform apply", exitErr.Command)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestApplyRequiresPlanFile(t *testing.T) {
	f := newFakeTF(t, "terraform")
	res, err := f.runner().Apply(f.ctx(), "", ApplyOptions{})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.calls())
}

func TestOutput(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   map[string]any
	}{
		{name: "empty object", stdout: "{}\n", want: map[string]any{}},
		{name: "empty stdout", stdout: "", want: map[string]any{}},
		{
			name:   "values",
			stdout: `{"vpc_id":{"sensitive":false,"type":"string","value":"vpc-0abc"},"db_password":{"sensitive":true,"type":"string","value":"hunter2"},"subnets":{"sensitive":false,"type":["list","string"],"value":["a","b"]}}`,
			want: map[string]any{
				"vpc_id":      "vpc-0abc",
				"db_password": "hunter2",
				"subnets":     []any{"a", "b"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTF(t, "terraform")
			env := []string{}
			if tt.stdout != "" {
				env = append(env, f.stdoutFile(tt.stdout))
			}
			outputs, err := f.runner(env...).Output(f.ctx())
			require.NoError(t, err)
			assert.Equal(t, []string{"output", "-json", "-no-color"}, f.lastCall().Args)
			got := map[string]any{}
			for k, v := range outputs {
				got[k] = v.Value
			}
			assert.Equal(t, tt.want, got)
			if o, ok := outputs["db_password"]; ok {
				assert.True(t, o.Sensitive)
			}
		})
	}
}

func TestRunnerMissingBinary(t *testing.T) {
	tests := []struct {
		name string
		bin  string
	}{
		{name: "empty", bin: ""},
		{name: "nonexistent", bin: filepath.Join(t.TempDir(), "terraform")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{Bin: tt.bin, Dir: t.TempDir()}
			res, err := r.Plan(execContext(t), PlanOptions{})
			require.Error(t, err)
			assert.Nil(t, res)
			var exitErr *ExitError
			assert.False(t, errors.As(err, &exitErr))
		})
	}
}

func TestRunnerInterruptOnCancel(t *testing.T) {
	f := newFakeTF(t, "terraform")
	var stderr bytes.Buffer
	r := f.runner("FAKE_TF_SLEEP=30")
	r.Stderr = &stderr
	r.InterruptTimeout = time.Minute
	ctx, cancel := context.WithCancel(f.ctx())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			data, _ := os.ReadFile(f.log)
			if bytes.Contains(data, []byte("--\n")) {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	res, err := r.Plan(ctx, PlanOptions{})
	require.ErrorIs(t, err, context.Canceled, stderr.String())
	require.NotNil(t, res)
	assert.Equal(t, 130, res.ExitCode, "the command exits through its interrupt trap, not the kill after InterruptTimeout")
	assert.Contains(t, res.Output, "interrupted")
}
