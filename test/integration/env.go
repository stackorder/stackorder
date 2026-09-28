//go:build integration

// Package integration runs the stackorder server in-process against a
// Postgres database of its own, the fake GitHub API of internal/testutil/ghfake
// and the fake OIDC issuer of internal/testutil/oidcfake, and exercises it
// over HTTP the way GitHub, runners and people do.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/server"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

// Fixed credentials of every Env.
const (
	WebhookSecret     = "integration-webhook-secret"
	OAuthClientID     = "Iv1.integration"
	OAuthClientSecret = "integration-oauth-secret"
)

// SessionKey is the fixed STACKORDER_SESSION_KEY of every Env, hex encoded.
var SessionKey = strings.Repeat("5e", server.SessionKeySize)

const (
	startTimeout = time.Minute
	stopTimeout  = 90 * time.Second
	pollInterval = 20 * time.Millisecond
)

var (
	keyOnce sync.Once
	appKey  *rsa.PrivateKey
	keyErr  error
)

// AppKey returns the App private key every Env configures, generated once
// per test binary; its public half is registered with each Env's fake
// GitHub, which verifies App JWTs against it.
func AppKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() { appKey, keyErr = rsa.GenerateKey(rand.Reader, 2048) })
	if keyErr != nil {
		t.Fatalf("integration: generate App key: %v", keyErr)
	}
	return appKey
}

// Env is one running server with its collaborators.
type Env struct {
	// Store is a connection of the test's own to the server's database.
	Store *store.Store
	// GH is the fake GitHub API the server talks to.
	GH *ghfake.Server
	// OIDC is the fake Actions OIDC issuer the server trusts, its clock
	// set to the time the Env started.
	OIDC *oidcfake.Issuer
	// Server is the server under test, running.
	Server *server.Server
	// BaseURL is the server's STACKORDER_BASE_URL, where it listens.
	BaseURL string
	// Client is an HTTP client for BaseURL.
	Client *http.Client
	// DSN is the server's DATABASE_URL.
	DSN string
	// Vars are the environment variables the server was configured from.
	Vars map[string]string

	t        testing.TB
	cancel   context.CancelFunc
	done     chan error
	stopOnce sync.Once
	stopErr  error
}

// EnvOption changes how NewEnv configures the server.
type EnvOption func(*envSetup)

type envSetup struct {
	setupMode bool
	vars      map[string]string
	github    []func(*ghfake.Server)
}

// SetupMode starts the server without any GitHub App variable.
func SetupMode() EnvOption {
	return func(s *envSetup) { s.setupMode = true }
}

// WithVar sets an environment variable of the server, replacing the
// Env's value; an empty value unsets it.
func WithVar(name, value string) EnvOption {
	return func(s *envSetup) { s.vars[name] = value }
}

// WithGitHub prepares the fake GitHub before the server starts, such as
// installations the start-up sync should learn.
func WithGitHub(fn func(*ghfake.Server)) EnvOption {
	return func(s *envSetup) { s.github = append(s.github, fn) }
}

// NewEnv starts a server on a random port with a fresh database, the fake
// GitHub as GITHUB_API_URL and GITHUB_WEB_URL, the fake issuer's JWKS, a
// fixed webhook secret and App key, and waits until it answers /readyz
// and, outside setup mode, has finished its start-up installation sync.
// The server is stopped when the test ends.
func NewEnv(t testing.TB, opts ...EnvOption) *Env {
	t.Helper()
	fake := ghfake.New(t)
	key := AppKey(t)
	fake.SetAppPublicKey(&key.PublicKey)
	issuer := oidcfake.New(t)
	if lag := time.Since(issuer.Now()); lag > 0 {
		issuer.Advance(lag)
	}
	dsn := pgtest.DSN(t)
	lc := net.ListenConfig{}
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("integration: listen: %v", err)
	}
	base := "http://" + ln.Addr().String()

	setup := envSetup{vars: map[string]string{
		server.EnvDatabaseURL:       dsn,
		server.EnvBaseURL:           base,
		server.EnvListen:            ln.Addr().String(),
		server.EnvAppID:             strconv.Itoa(1),
		server.EnvAppPrivateKey:     string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		server.EnvWebhookSecret:     WebhookSecret,
		server.EnvOAuthClientID:     OAuthClientID,
		server.EnvOAuthClientSecret: OAuthClientSecret,
		server.EnvGitHubAPIURL:      fake.URL(),
		server.EnvGitHubWebURL:      fake.URL(),
		server.EnvOIDCIssuer:        issuer.URL(),
		server.EnvOIDCJWKSURL:       issuer.JWKSURL(),
		server.EnvSessionKey:        SessionKey,
		server.EnvWorkers:           "2",
		server.EnvLogLevel:          "debug",
		server.EnvLogFormat:         server.LogFormatText,
	}}
	for _, o := range opts {
		o(&setup)
	}
	if setup.setupMode {
		for _, name := range []string{server.EnvAppID, server.EnvAppPrivateKey, server.EnvWebhookSecret, server.EnvOAuthClientID, server.EnvOAuthClientSecret} {
			delete(setup.vars, name)
		}
	}
	for _, fn := range setup.github {
		fn(fake)
	}
	cfg, err := server.LoadConfig(func(name string) string { return setup.vars[name] })
	if err != nil {
		_ = ln.Close()
		t.Fatalf("integration: %v", err)
	}

	logs := &testWriter{t: t}
	t.Cleanup(logs.stop)
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: cfg.LogLevel}))
	startCtx, cancelStart := context.WithTimeout(context.Background(), startTimeout)
	defer cancelStart()
	srv, err := server.New(startCtx, cfg, server.WithListener(ln), server.WithLogger(logger))
	if err != nil {
		t.Fatalf("integration: start the server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e := &Env{
		GH:      fake,
		OIDC:    issuer,
		Server:  srv,
		BaseURL: base,
		Client:  &http.Client{Timeout: 30 * time.Second},
		DSN:     dsn,
		Vars:    setup.vars,
		t:       t,
		cancel:  cancel,
		done:    make(chan error, 1),
	}
	go func() { e.done <- srv.Run(ctx) }()
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("integration: server stopped with an error: %v", err)
		}
	})

	if e.Store, err = store.Open(startCtx, dsn); err != nil {
		t.Fatalf("integration: open the test's store: %v", err)
	}
	t.Cleanup(e.Store.Close)
	e.WaitFor(func() bool {
		status, _ := e.Get("/readyz")
		return status == http.StatusOK
	}, startTimeout, "the server answers /readyz")
	if !setup.setupMode {
		e.WaitFor(func() bool {
			var done bool
			err := e.Store.Pool().QueryRow(context.Background(),
				`SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = 'sync_installations' AND done_at IS NOT NULL)`).Scan(&done)
			return err == nil && done
		}, startTimeout, "the start-up installation sync finishes")
	}
	return e
}

// Stop cancels the server's context and waits for Run to return, and
// returns its error. Later calls return the same error.
func (e *Env) Stop() error {
	e.stopOnce.Do(func() {
		e.cancel()
		select {
		case e.stopErr = <-e.done:
		case <-time.After(stopTimeout):
			e.stopErr = fmt.Errorf("integration: the server did not stop within %s", stopTimeout)
		}
	})
	return e.stopErr
}

// URL returns the absolute URL of path on the server.
func (e *Env) URL(path string) string {
	return e.BaseURL + path
}

// Do sends req and returns the status and body; a transport error fails
// the test.
func (e *Env) Do(req *http.Request) (int, string) {
	e.t.Helper()
	resp, err := e.Client.Do(req)
	if err != nil {
		e.t.Fatalf("integration: %s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("integration: read %s %s: %v", req.Method, req.URL, err)
	}
	return resp.StatusCode, string(body)
}

// Get fetches path and returns the status and body; a transport error
// is status 0.
func (e *Env) Get(path string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.URL(path), nil)
	if err != nil {
		return 0, err.Error()
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Delivery is one webhook POST and GitHub's view of it.
type Delivery struct {
	// ID is the X-GitHub-Delivery id.
	ID string
	// Status and Body are the server's answer.
	Status int
	Body   string
	// Payload and Header are what was sent, for redelivering.
	Payload []byte
	Header  http.Header
}

// Deliver signs payload, any JSON value or raw []byte, with the Env's
// webhook secret and POSTs it to /webhooks/github as event, as GitHub
// does.
func (e *Env) Deliver(event string, payload any) Delivery {
	e.t.Helper()
	return e.DeliverSigned([]byte(WebhookSecret), event, payload)
}

// DeliverSigned is Deliver with another secret, for signature tests.
func (e *Env) DeliverSigned(secret []byte, event string, payload any) Delivery {
	e.t.Helper()
	body, header := ghfake.SignedWebhook(secret, event, payload)
	return e.Redeliver(Delivery{Payload: body, Header: header})
}

// Redeliver POSTs a delivery again with the same id, payload and
// signature, as GitHub's "Redeliver" button does.
func (e *Env) Redeliver(d Delivery) Delivery {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.URL("/webhooks/github"), bytes.NewReader(d.Payload))
	if err != nil {
		e.t.Fatalf("integration: webhook request: %v", err)
	}
	req.Header = d.Header.Clone()
	status, body := e.Do(req)
	return Delivery{ID: d.Header.Get(gh.HeaderDelivery), Status: status, Body: body, Payload: d.Payload, Header: d.Header}
}

// WaitFor polls cond until it holds and fails the test when timeout
// passes first; what describes the condition in the failure.
func (e *Env) WaitFor(cond func() bool, timeout time.Duration, what string) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("integration: timed out after %s waiting until %s", timeout, what)
		}
		time.Sleep(pollInterval)
	}
}

// EventDone reports whether the worker pool finished the delivery with
// id, and the error it recorded.
func (e *Env) EventDone(id string) (bool, string) {
	ev, err := e.Store.GetEvent(context.Background(), id)
	if err != nil {
		return false, ""
	}
	return ev.DoneAt != nil, ev.LastError
}

type testWriter struct {
	t       testing.TB
	mu      sync.Mutex
	stopped bool
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (w *testWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
}
