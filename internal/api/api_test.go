package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/version"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	rec := e.do(newRequest(t, http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, healthBody{Status: "ok", Version: version.Version}, decodeBody[healthBody](t, rec))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "same-origin", rec.Header().Get("Referrer-Policy"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Empty(t, rec.Header().Get("Content-Security-Policy"), "JSON responses carry no CSP")

	rec = e.do(newRequest(t, http.MethodHead, "/healthz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestReadyz(t *testing.T) {
	e := newEnv(t)
	rec := e.do(newRequest(t, http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", decodeBody[healthBody](t, rec).Status)
	assert.Positive(t, e.db.pingDeadline)
	assert.LessOrEqual(t, e.db.pingDeadline, readyTimeout)

	e.db.pingErr = errors.New("connection refused")
	rec = e.do(newRequest(t, http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, codeUnavailable, errorOf(t, rec).Code)
	assert.Contains(t, e.logs.String(), "connection refused")
}

func TestMetrics(t *testing.T) {
	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "stackorder_up 1\n")
	})

	open := newEnv(t, func(_ *Config, d *Deps) { d.Metrics = metrics })
	rec := open.do(newRequest(t, http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "stackorder_up 1\n", rec.Body.String())

	guarded := newEnv(t, func(c *Config, d *Deps) {
		c.MetricsToken = "s3cret"
		d.Metrics = metrics
	})
	for name, header := range map[string]string{
		"missing":     "",
		"wrong token": "Bearer nope",
		"prefix":      "Bearer s3cre",
		"scheme":      "Basic s3cret",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRequest(t, http.MethodGet, "/metrics", nil)
			if header != "" {
				r.Header.Set("Authorization", header)
			}
			rec := guarded.do(r)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, codeUnauthorized, errorOf(t, rec).Code)
			assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"))
		})
	}
	r := newRequest(t, http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer s3cret")
	rec = guarded.do(r)
	assert.Equal(t, http.StatusOK, rec.Code)

	disabled := newEnv(t)
	rec = disabled.do(newRequest(t, http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUIWebhookAndUnknownEndpoints(t *testing.T) {
	var webhookHits int
	e := newEnv(t, func(_ *Config, d *Deps) {
		d.Webhook = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			webhookHits++
			w.WriteHeader(http.StatusAccepted)
		})
	})

	for _, path := range []string{"/", "/repos/acme/infra", "/runs/" + uuid.NewString(), "/setup-guide"} {
		rec := e.do(newRequest(t, http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Contains(t, rec.Body.String(), "<title>ui</title>", path)
		csp := rec.Header().Get("Content-Security-Policy")
		assert.Contains(t, csp, "default-src 'self'", path)
		assert.Contains(t, csp, "frame-ancestors 'none'", path)
		assert.Contains(t, csp, "https://avatars.githubusercontent.com", path)
	}

	rec := e.do(newRequest(t, http.MethodPost, "/webhooks/github", "{}"))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, 1, webhookHits)

	for _, target := range []string{"/v1/nope", "/v1/", "/auth/nope", "/v1/runs/x/y/z/w"} {
		rec := e.do(newRequest(t, http.MethodGet, target, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, target)
		assert.Equal(t, codeNotFound, errorOf(t, rec).Code, target)
	}

	noWebhook := newEnv(t)
	rec = noWebhook.do(newRequest(t, http.MethodPost, "/webhooks/github", "{}"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	noUI := newEnv(t, func(_ *Config, d *Deps) { d.UI = nil })
	rec = noUI.do(newRequest(t, http.MethodGet, "/repos", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGHESImageSources(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *Deps) { c.GitHubWebURL = "https://ghe.example.com/" })
	csp := e.do(newRequest(t, http.MethodGet, "/", nil)).Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "img-src 'self' data: https://avatars.githubusercontent.com https://ghe.example.com https://avatars.ghe.example.com;")
}

func TestSetupModeServesOnlySetupAndHealth(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *Deps) { c.SetupMode = true })

	rec := e.do(newRequest(t, http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, decodeBody[healthBody](t, rec).SetupMode)
	rec = e.do(newRequest(t, http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	for _, target := range []string{"/", "/v1/me", "/v1/runs", "/metrics", "/auth/login", "/repos/acme/infra", "/webhooks/github"} {
		rec := e.do(newRequest(t, http.MethodGet, target, nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, target)
		body := errorOf(t, rec)
		assert.Equal(t, codeUnavailable, body.Code, target)
		assert.Contains(t, body.Message, "setup is required", target)
		assert.Contains(t, body.Message, testBaseURL+"/setup", target)
	}
}

func TestPanicsAreRecovered(t *testing.T) {
	e := newEnv(t, func(_ *Config, d *Deps) {
		d.UI = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	})
	r := newRequest(t, http.MethodGet, "/repos", nil)
	r.Header.Set(requestIDHeader, "req-123")
	rec := e.do(r)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	body := errorOf(t, rec)
	assert.Equal(t, codeInternal, body.Code)
	assert.Equal(t, "internal server error", body.Message)
	assert.Equal(t, "req-123", rec.Header().Get(requestIDHeader))
	logs := e.logs.String()
	assert.Contains(t, logs, `"msg":"panic serving request"`)
	assert.Contains(t, logs, `"panic":"boom"`)
	assert.Contains(t, logs, "runtime/debug.Stack")
	assert.Contains(t, logs, `"status":500`)

	abort := newEnv(t, func(_ *Config, d *Deps) {
		d.UI = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	})
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() { abort.do(newRequest(t, http.MethodGet, "/", nil)) })
}

func TestRequestIDAndAccessLog(t *testing.T) {
	e := newEnv(t)
	r := newRequest(t, http.MethodGet, "/healthz", nil)
	r.Header.Set(requestIDHeader, "abc-123")
	rec := e.do(r)
	assert.Equal(t, "abc-123", rec.Header().Get(requestIDHeader))

	for _, bad := range []string{"", strings.Repeat("x", maxRequestID+1), "has space", "tab\there", "ünïcode"} {
		r := newRequest(t, http.MethodGet, "/healthz", nil)
		r.Header.Set(requestIDHeader, bad)
		got := e.do(r).Header().Get(requestIDHeader)
		_, err := uuid.Parse(got)
		assert.NoError(t, err, "%q is replaced by a generated id, got %q", bad, got)
	}

	logs := e.logs.String()
	assert.Contains(t, logs, `"msg":"http request"`)
	assert.Contains(t, logs, `"request_id":"abc-123"`)
	assert.Contains(t, logs, `"route":"GET /healthz"`)
	assert.Contains(t, logs, `"status":200`)
	assert.Contains(t, logs, `"principal":"none"`)
}

func TestInstrumentWrapsEveryRoute(t *testing.T) {
	var (
		mu     sync.Mutex
		routes []string
		served []string
	)
	e := newEnv(t, func(_ *Config, d *Deps) {
		d.Instrument = func(route string) func(http.Handler) http.Handler {
			mu.Lock()
			routes = append(routes, route)
			mu.Unlock()
			return func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					served = append(served, route)
					mu.Unlock()
					next.ServeHTTP(w, r)
				})
			}
		}
	})
	assert.Contains(t, routes, "GET /healthz")
	assert.Contains(t, routes, "/")
	e.do(newRequest(t, http.MethodGet, "/healthz", nil))
	e.do(newRequest(t, http.MethodGet, "/some/page", nil))
	assert.Equal(t, []string{"GET /healthz", "/"}, served)
}

func TestNewValidatesDependencies(t *testing.T) {
	assert.PanicsWithValue(t, "api: Deps.Store is required", func() { New(Config{BaseURL: testBaseURL}, Deps{}) })
	assert.Panics(t, func() { newServer(Config{BaseURL: "stackorder.test"}, Deps{}, newFakeStore()) })
	assert.Panics(t, func() { newServer(Config{BaseURL: "ftp://stackorder.test"}, Deps{}, newFakeStore()) })

	e := newEnv(t, func(c *Config, _ *Deps) { c.SessionKey = nil })
	assert.Len(t, e.srv.sessionKey, 32)
	assert.Contains(t, e.logs.String(), "no session key configured")
	assert.Equal(t, DefaultSessionTTL, e.srv.cfg.SessionTTL)

	e = newEnv(t, func(c *Config, _ *Deps) {
		c.BaseURL = "HTTP://Stackorder.Test:8080/"
		c.SessionTTL = time.Hour
	})
	assert.Equal(t, "http://stackorder.test:8080", e.srv.origin)
	assert.False(t, e.srv.secure)
	assert.Equal(t, "HTTP://Stackorder.Test:8080", e.srv.cfg.BaseURL)
	assert.Equal(t, time.Hour, e.srv.cfg.SessionTTL)
}
