package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestUnlock(t *testing.T) {
	taken := time.Date(2026, 9, 27, 8, 30, 0, 0, time.UTC)
	newUnlock := func(t *testing.T) (*harness, *fakeServer) {
		h := newHarness(t)
		fs := newFakeServer(t)
		fs.released["stacks/prod/vpc"] = []v1.LockInfo{{RunID: "run-7", PRNumber: 12, TakenAt: taken}}
		initGit(t, h.root)
		gitRun(t, h.root, "remote", "add", "origin", "https://github.com/acme/network.git")
		t.Setenv(EnvServerURL, fs.url())
		t.Setenv(EnvAPIKey, "sk_admin")
		return h, fs
	}
	t.Run("text", func(t *testing.T) {
		h, fs := newUnlock(t)
		r := h.run("unlock", "stacks/prod/vpc/", "stacks/dev/vpc", "--reason", "runner died", "--force-state")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, "released stacks/prod/vpc (run run-7, PR #12, taken 2026-09-27T08:30:00Z)\nstacks/dev/vpc: no lock held\n", r.stdout)
		assert.Equal(t, []v1.UnlockRequest{
			{Repo: "acme/network", StackKey: "stacks/prod/vpc", Reason: "runner died", ForceState: true},
			{Repo: "acme/network", StackKey: "stacks/dev/vpc", Reason: "runner died", ForceState: true},
		}, fs.unlocks)
		assert.Equal(t, []string{"Bearer sk_admin", "Bearer sk_admin"}, fs.auth["unlock"])
	})
	t.Run("json", func(t *testing.T) {
		h, _ := newUnlock(t)
		r := h.run("--format", "json", "unlock", "stacks/prod/vpc")
		require.Equal(t, 0, r.code, r.stderr)
		var resp v1.UnlockResponse
		require.NoError(t, json.Unmarshal([]byte(r.stdout), &resp))
		assert.Equal(t, []v1.LockInfo{{StackKey: "stacks/prod/vpc", RunID: "run-7", PRNumber: 12, TakenAt: taken}}, resp.Released)
	})
	t.Run("repository from the actions context", func(t *testing.T) {
		h, fs := newUnlock(t)
		t.Setenv("GITHUB_REPOSITORY", "acme/infra")
		r := h.run("unlock", "stacks/prod/vpc")
		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, "acme/infra", fs.unlocks[0].Repo)
	})
	tests := []struct {
		name  string
		setup func(t *testing.T, fs *fakeServer)
		want  int
		msg   string
	}{
		{name: "locked by an apply in progress", setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("unlock", 423, "locked") }, want: ExitRefused, msg: "injected locked"},
		{name: "forbidden", setup: func(_ *testing.T, fs *fakeServer) { fs.failWith("unlock", 403, "forbidden") }, want: ExitRefused, msg: "injected forbidden"},
		{name: "unreachable", setup: func(t *testing.T, _ *fakeServer) { t.Setenv(EnvServerURL, deadURL(t)) }, want: ExitFailure, msg: "server unreachable"},
		{name: "no server", setup: func(t *testing.T, _ *fakeServer) { t.Setenv(EnvServerURL, "") }, want: ExitFailure, msg: "no server is configured"},
		{name: "no api key", setup: func(t *testing.T, _ *fakeServer) { t.Setenv(EnvAPIKey, "") }, want: ExitFailure, msg: "needs an automation API key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := newUnlock(t)
			tt.setup(t, fs)
			r := h.run("unlock", "stacks/prod/vpc", "stacks/dev/vpc")
			assert.Equal(t, tt.want, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.msg)
		})
	}
	t.Run("no repository", func(t *testing.T) {
		h := newHarness(t)
		t.Setenv(EnvServerURL, "http://127.0.0.1:1")
		t.Setenv(EnvAPIKey, "sk_admin")
		r := h.run("unlock", "stacks/prod/vpc")
		assert.Equal(t, ExitFailure, r.code)
		assert.Contains(t, r.stderr, "cannot tell the repository")
	})
	t.Run("needs a key", func(t *testing.T) {
		h := newHarness(t)
		r := h.run("unlock")
		assert.Equal(t, ExitFailure, r.code)
		assert.Contains(t, r.stderr, "requires at least 1 arg")
	})
}
