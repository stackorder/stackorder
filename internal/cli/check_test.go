package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestCheckPostsTheVerdict(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "pull_request", prPayload())
	details := writeFile(t, filepath.Join(t.TempDir(), "report.txt"), "db_password = \"hunter2hunter2\"\n3 findings\n")

	r := h.run("check", "--stack", "./stacks/app:blue", "--run-id", "run-1", "--name", "policy/opa",
		"--status", "FAIL", "--summary", "3 findings", "--details-url", "https://ci.example/report", "--details-file", details)
	require.Equal(t, 0, r.code, r.stderr)
	assert.Equal(t, "stackorder/policy/opa: stacks/app:blue: fail\n", r.stdout)
	require.Len(t, fs.checks, 1)
	c := fs.checks[0]
	assert.Equal(t, "run-1", c.RunID)
	assert.Equal(t, "stacks/app:blue", c.Key)
	assert.Equal(t, "policy/opa", c.Name)
	assert.Equal(t, v1.CheckVerdict{
		Status:     v1.CheckFail,
		Summary:    "3 findings",
		Details:    "db_password = \"***\"\n3 findings\n",
		DetailsURL: "https://ci.example/report",
	}, c.Verdict)
	assert.Equal(t, []string{"Bearer oidc-token"}, fs.auth["check"])
}

func TestCheckJSONOutput(t *testing.T) {
	h := newHarness(t)
	fs := newFakeServer(t)
	h.ci(fs, "pull_request", prPayload())
	t.Setenv(EnvRunID, "run-env")
	r := h.run("--format", "json", "check", "--stack", "stacks/app", "--name", "cost", "--status", "warn")
	require.Equal(t, 0, r.code, r.stderr)
	var chk v1.Check
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &chk))
	assert.Equal(t, v1.CheckWarn, chk.Status)
	assert.Equal(t, "run-env", fs.checks[0].RunID)
}

func TestCheckErrors(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		setup func(t *testing.T, fs *fakeServer)
		want  int
		msg   string
	}{
		{name: "bad status", args: []string{"--run-id", "run-1", "--status", "maybe"}, want: ExitFailure, msg: `"maybe" is not one of pass, fail, warn`},
		{name: "no run id", args: []string{"--status", "pass"}, want: ExitFailure, msg: "check needs --run-id"},
		{name: "blank name", args: []string{"--run-id", "run-1", "--status", "pass", "--name", " "}, want: ExitFailure, msg: "--name is required"},
		{name: "missing details file", args: []string{"--run-id", "run-1", "--status", "pass", "--details-file", "/nonexistent/report"}, want: ExitFailure, msg: "--details-file"},
		{
			name:  "no server",
			args:  []string{"--run-id", "run-1", "--status", "pass"},
			setup: func(t *testing.T, _ *fakeServer) { t.Setenv(EnvServerURL, "") },
			want:  ExitFailure,
			msg:   "no server is configured",
		},
		{
			name:  "refused",
			args:  []string{"--run-id", "run-1", "--status", "pass"},
			setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("check", 409, "conflict") },
			want:  ExitRefused,
			msg:   "injected conflict",
		},
		{
			name:  "unreachable",
			args:  []string{"--run-id", "run-1", "--status", "pass"},
			setup: func(t *testing.T, _ *fakeServer) { t.Setenv(EnvServerURL, deadURL(t)) },
			want:  ExitFailure,
			msg:   "server unreachable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			fs := newFakeServer(t)
			h.ci(fs, "pull_request", prPayload())
			if tt.setup != nil {
				tt.setup(t, fs)
			}
			args := append([]string{"check", "--stack", "stacks/app", "--name", "policy"}, tt.args...)
			r := h.run(args...)
			assert.Equal(t, tt.want, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.msg)
		})
	}
}
