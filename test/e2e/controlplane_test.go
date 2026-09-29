//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/server"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

const (
	webhookSecret = "e2e-webhook-secret"
	startTimeout  = 2 * time.Minute
	eventTimeout  = time.Minute
	pollInterval  = 50 * time.Millisecond
)

var (
	appKeyOnce sync.Once
	appKey     *rsa.PrivateKey
	appKeyErr  error
)

type controlPlane struct {
	gh      *ghfake.Server
	oidc    *oidcfake.Issuer
	store   *store.Store
	baseURL string
	apiKey  string
	client  *http.Client
	clockMu sync.Mutex
}

func startControlPlane(t *testing.T, ls *localStack, prepare func(*ghfake.Server)) *controlPlane {
	t.Helper()
	appKeyOnce.Do(func() { appKey, appKeyErr = rsa.GenerateKey(rand.Reader, 2048) })
	require.NoError(t, appKeyErr)
	fake := ghfake.New(t)
	fake.SetAppPublicKey(&appKey.PublicKey)
	prepare(fake)
	issuer := oidcfake.New(t)
	cp := &controlPlane{gh: fake, oidc: issuer, client: &http.Client{Timeout: time.Minute}}
	cp.syncClock()

	dsn := pgtest.DSN(t)
	lc := net.ListenConfig{}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cp.baseURL = "http://" + ln.Addr().String()
	vars := map[string]string{
		server.EnvDatabaseURL:       dsn,
		server.EnvBaseURL:           cp.baseURL,
		server.EnvListen:            ln.Addr().String(),
		server.EnvAppID:             "1",
		server.EnvAppPrivateKey:     string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(appKey)})),
		server.EnvWebhookSecret:     webhookSecret,
		server.EnvOAuthClientID:     "Iv1.e2e",
		server.EnvOAuthClientSecret: "e2e-oauth-secret",
		server.EnvGitHubAPIURL:      fake.URL(),
		server.EnvGitHubWebURL:      fake.URL(),
		server.EnvOIDCIssuer:        issuer.URL(),
		server.EnvOIDCJWKSURL:       issuer.JWKSURL(),
		server.EnvSessionKey:        strings.Repeat("e2", server.SessionKeySize),
		server.EnvArtifactBucket:    artifactBucket,
		server.EnvArtifactEndpoint:  ls.endpoint,
		server.EnvWorkers:           "4",
		server.EnvLogLevel:          "info",
		server.EnvLogFormat:         server.LogFormatText,
	}
	cfg, err := server.LoadConfig(func(name string) string { return vars[name] })
	if err != nil {
		_ = ln.Close()
		t.Fatalf("e2e: server configuration: %v", err)
	}
	logs := &testLog{t: t, prefix: "server: "}
	t.Cleanup(logs.stop)
	startCtx, cancelStart := context.WithTimeout(context.Background(), startTimeout)
	defer cancelStart()
	srv, err := server.New(startCtx, cfg, server.WithListener(ln),
		server.WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: cfg.LogLevel}))))
	require.NoError(t, err, "start the server")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("e2e: the server stopped with an error: %v", err)
			}
		case <-time.After(2 * time.Minute):
			t.Errorf("e2e: the server did not stop")
		}
	})

	cp.store, err = store.Open(startCtx, dsn)
	require.NoError(t, err)
	t.Cleanup(cp.store.Close)
	waitUntil(t, startTimeout, "the server answers /readyz", func() bool {
		status, _ := cp.do(t, http.MethodGet, "/readyz", nil, false)
		return status == http.StatusOK
	})
	waitUntil(t, startTimeout, "the start-up installation sync finishes", func() bool {
		var ok bool
		err := cp.store.Pool().QueryRow(t.Context(),
			`SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = 'sync_installations' AND done_at IS NOT NULL)`).Scan(&ok)
		return err == nil && ok
	})
	cp.apiKey, _, err = cp.store.CreateAPIKey(t.Context(), "e2e", "e2e")
	require.NoError(t, err)
	return cp
}

func (cp *controlPlane) syncClock() {
	cp.clockMu.Lock()
	defer cp.clockMu.Unlock()
	if lag := time.Since(cp.oidc.Now()); lag > 0 {
		cp.oidc.Advance(lag)
	}
}

func (cp *controlPlane) do(t *testing.T, method, path string, body any, auth bool) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, cp.baseURL+path, r)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+cp.apiKey)
	}
	resp, err := cp.client.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, data
}

func (cp *controlPlane) getJSON(t *testing.T, path string, out any) {
	t.Helper()
	status, body := cp.do(t, http.MethodGet, path, nil, true)
	require.Equal(t, http.StatusOK, status, "GET %s: %s", path, body)
	require.NoError(t, json.Unmarshal(body, out), "decode GET %s", path)
}

func (cp *controlPlane) deliver(t *testing.T, event string, payload any) {
	t.Helper()
	body, header := ghfake.SignedWebhook([]byte(webhookSecret), event, payload)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, cp.baseURL+"/webhooks/github", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header = header
	resp, err := cp.client.Do(req)
	require.NoError(t, err, "deliver %s", event)
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "deliver %s: %s", event, respBody)
	id := header.Get(gh.HeaderDelivery)
	var lastErr string
	waitUntil(t, eventTimeout, "the server processes the "+event+" delivery", func() bool {
		ev, err := cp.store.GetEvent(t.Context(), id)
		if err != nil || ev.DoneAt == nil {
			return false
		}
		lastErr = ev.LastError
		return true
	})
	require.Empty(t, lastErr, "the %s delivery was processed without an error", event)
}

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("e2e: timed out after %s waiting until %s", timeout, what)
		}
		time.Sleep(pollInterval)
	}
}

type testLog struct {
	t       *testing.T
	prefix  string
	mu      sync.Mutex
	stopped bool
}

func (w *testLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.t.Log(w.prefix + strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (w *testLog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
