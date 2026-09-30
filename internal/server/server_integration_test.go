//go:build integration

package server_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/server"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

const waitFor = 20 * time.Second

type running struct {
	srv  *server.Server
	st   *store.Store
	gh   *ghfake.Server
	stop func() error
}

func start(t *testing.T, setupMode bool, opts ...server.Option) *running {
	t.Helper()
	fake := ghfake.New(t)
	dsn := pgtest.DSN(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	vars := map[string]string{
		server.EnvDatabaseURL:  dsn,
		server.EnvBaseURL:      "http://" + ln.Addr().String(),
		server.EnvGitHubAPIURL: fake.URL(),
		server.EnvWorkers:      "2",
	}
	if !setupMode {
		cfg := fake.AppConfig()
		vars[server.EnvAppID] = "1"
		vars[server.EnvAppPrivateKey] = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(cfg.PrivateKey)}))
		vars[server.EnvWebhookSecret] = "secret"
	}
	cfg, err := server.LoadConfig(func(name string) string { return vars[name] })
	require.NoError(t, err)
	srv, err := server.New(t.Context(), cfg, append([]server.Option{server.WithListener(ln), server.WithLogger(slog.New(slog.DiscardHandler))}, opts...)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	stopped := false
	var stopErr error
	stop := func() error {
		if !stopped {
			stopped = true
			cancel()
			stopErr = <-done
		}
		return stopErr
	}
	t.Cleanup(func() { require.NoError(t, stop()) })
	st, err := store.Open(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(st.Close)
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + srv.Addr() + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, waitFor, 20*time.Millisecond)
	return &running{srv: srv, st: st, gh: fake, stop: stop}
}

func TestEveryJobKindIsExecuted(t *testing.T) {
	r := start(t, false)
	ok := map[string]any{
		runs.JobReconcile:         runs.ReconcileJob{},
		runs.JobPrune:             runs.PruneJob{},
		runs.JobStaleLocks:        runs.StaleLocksJob{},
		runs.JobSyncInstallations: runs.SyncInstallationsJob{},
	}
	failing := map[string]any{
		runs.JobDispatchWave:  runs.DispatchWaveJob{RunID: uuid.NewString(), Wave: 0},
		runs.JobDrift:         runs.DriftJob{RepoID: 404, StackID: uuid.NewString()},
		runs.JobScheduleDrift: runs.ScheduleDriftJob{RepoID: 404},
		runs.JobCrossRepoPlan: runs.CrossRepoPlanJob{Repo: "acme/missing", StackKeys: []string{"stacks/a"}, UpstreamRunID: uuid.NewString()},
	}
	ids := map[string]uuid.UUID{}
	for kind, payload := range merge(ok, failing) {
		b, err := json.Marshal(payload)
		require.NoError(t, err)
		job, inserted, err := r.st.EnqueueJob(t.Context(), kind, b, time.Time{}, "test:"+kind)
		require.NoError(t, err)
		require.True(t, inserted)
		ids[kind] = job.ID
	}
	require.Eventually(t, func() bool {
		for _, id := range ids {
			job, err := r.st.GetJob(t.Context(), id)
			if err != nil || job.Attempts == 0 && job.DoneAt == nil {
				return false
			}
		}
		return true
	}, waitFor, 20*time.Millisecond, "the pool runs every job once")

	for kind := range ok {
		job, err := r.st.GetJob(t.Context(), ids[kind])
		require.NoError(t, err)
		assert.NotNil(t, job.DoneAt, "%s completes", kind)
		assert.Empty(t, job.LastError, kind)
	}
	for kind := range failing {
		job, err := r.st.GetJob(t.Context(), ids[kind])
		require.NoError(t, err)
		assert.NotContains(t, job.LastError, "unknown job kind", "%s reaches its runs handler", kind)
	}
}

func merge(maps ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestPoolIsSizedFromTheWorkers(t *testing.T) {
	r := start(t, true)
	assert.Equal(t, int32(2+server.PoolConnsBeyondWorkers), r.srv.Store().Pool().Config().MaxConns,
		"a DSN without pool_max_conns gets STACKORDER_WORKERS plus eight connections")
}

func TestSetupModeRunsNoWorkers(t *testing.T) {
	r := start(t, true)
	assert.True(t, r.srv.Config().SetupMode)
	inserted, err := r.st.InsertEvent(t.Context(), "delivery-1", gh.EventPing, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, inserted)
	time.Sleep(500 * time.Millisecond)
	ev, err := r.st.GetEvent(t.Context(), "delivery-1")
	require.NoError(t, err)
	assert.Nil(t, ev.ClaimedAt, "no worker claims events in setup mode")
	assert.Nil(t, ev.DoneAt)

	require.ErrorIs(t, r.srv.Run(t.Context()), server.ErrRunning)
	require.NoError(t, r.stop())
	require.NoError(t, r.srv.Close(), "closing after Run is a no-op")
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSetupModeLogsTheSetupURL(t *testing.T) {
	var logs lockedBuffer
	r := start(t, true, server.WithLogger(server.NewLogger(&logs, slog.LevelInfo, server.LogFormatJSON)))
	token := r.srv.Config().SetupToken
	require.NotEmpty(t, token)
	assert.True(t, r.srv.Config().SetupTokenGenerated)

	var setupURL string
	require.Eventually(t, func() bool {
		for _, line := range strings.Split(logs.String(), "\n") {
			var entry struct {
				Level    string `json:"level"`
				SetupURL string `json:"setup_url"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.SetupURL != "" {
				setupURL = entry.SetupURL
				return entry.Level == "WARN"
			}
		}
		return false
	}, waitFor, 20*time.Millisecond, "the start-up log carries the setup URL")
	base := "http://" + r.srv.Addr()
	assert.Equal(t, base+"/setup?token="+token, setupURL)

	resp, err := http.Get(base + "/setup")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "the setup page needs the token")

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err = client.Get(setupURL)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Equal(t, "/setup", resp.Request.URL.RequestURI(), "the browser lands on the URL without the token")
	assert.Contains(t, string(body), `id="manifest-form"`)
	assert.NotContains(t, string(body), token)
}
