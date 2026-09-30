package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/worker"
)

func TestOptions(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	logger := slog.New(slog.DiscardHandler)

	var o options
	for _, opt := range []Option{WithListener(ln), WithClock(func() time.Time { return at }), WithLogger(logger)} {
		opt(&o)
	}
	assert.Same(t, ln, o.listener)
	require.NotNil(t, o.clock)
	assert.Equal(t, at, o.clock())
	assert.Same(t, logger, o.logger)

	var empty options
	assert.Nil(t, empty.listener)
	assert.Nil(t, empty.clock)
	assert.Nil(t, empty.logger)
}

func closedListener(t *testing.T) (net.Listener, func() bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln, func() bool {
		_, err := ln.Accept()
		return errors.Is(err, net.ErrClosed)
	}
}

func TestNewRejectsAnInvalidConfigBeforeConnecting(t *testing.T) {
	ln, closed := closedListener(t)
	_, err := New(t.Context(), Config{AppID: 1}, WithListener(ln), WithLogger(slog.New(slog.DiscardHandler)))
	require.Error(t, err)
	for _, name := range []string{EnvDatabaseURL, EnvBaseURL, EnvAppPrivateKey, EnvWebhookSecret} {
		assert.Contains(t, err.Error(), name)
	}
	assert.True(t, closed(), "the server owns the listener and closes it when New fails")
}

func TestNewFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	ln, closed := closedListener(t)
	cfg := Config{BaseURL: testBaseURL, DatabaseURL: "postgres://stackorder@127.0.0.1:1/stackorder?sslmode=disable&connect_timeout=2", SetupMode: true}
	start := time.Now()
	_, err := New(t.Context(), cfg, WithListener(ln), WithLogger(slog.New(slog.DiscardHandler)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server: open the database")
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.True(t, closed())
}

func TestNewWarnsAboutAGeneratedSessionKey(t *testing.T) {
	var logs bytes.Buffer
	cfg := Config{BaseURL: testBaseURL, DatabaseURL: "postgres://stackorder@127.0.0.1:1/stackorder?connect_timeout=2", SetupMode: true}
	_, err := New(t.Context(), cfg, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	require.Error(t, err)
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), EnvSessionKey+" is not set")

	logs.Reset()
	cfg.SessionKey = bytes.Repeat([]byte{1}, SessionKeySize)
	_, err = New(t.Context(), cfg, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	require.Error(t, err)
	assert.NotContains(t, logs.String(), EnvSessionKey, "a configured key is not warned about")
}

func TestRunLogsTheSetupURL(t *testing.T) {
	logged := func(cfg Config) []map[string]any {
		t.Helper()
		var out bytes.Buffer
		s := &Server{cfg: cfg, log: NewLogger(&out, slog.LevelInfo, LogFormatJSON)}
		s.logSetupURL(t.Context())
		var lines []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &m))
			lines = append(lines, m)
		}
		return lines
	}

	generated := Config{BaseURL: testBaseURL, SetupMode: true}
	require.NoError(t, generated.withSetupToken())
	lines := logged(generated)
	require.Len(t, lines, 1)
	assert.Equal(t, "WARN", lines[0]["level"], "shown at the default level and at warn")
	assert.Equal(t, testBaseURL+"/setup?token="+generated.SetupToken, lines[0]["setup_url"])

	resetup := Config{BaseURL: testBaseURL, AllowResetup: true}
	require.NoError(t, resetup.withSetupToken())
	lines = logged(resetup)
	require.Len(t, lines, 1)
	assert.Equal(t, testBaseURL+"/setup?force=1&token="+resetup.SetupToken, lines[0]["setup_url"])

	fromEnv := Config{BaseURL: testBaseURL, SetupMode: true, SetupToken: testSetupToken}
	lines = logged(fromEnv)
	require.Len(t, lines, 1)
	assert.Equal(t, testBaseURL+"/setup?token=<"+EnvSetupToken+">", lines[0]["setup_url"], "a token the operator passed is not written to the log")

	assert.Empty(t, logged(Config{BaseURL: testBaseURL}), "nothing to log when /setup cannot create an App")
}

func TestNewLogger(t *testing.T) {
	var out bytes.Buffer
	NewLogger(&out, slog.LevelInfo, LogFormatJSON).Info("hello", "k", "v")
	var line map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &line))
	assert.Equal(t, "hello", line["msg"])
	assert.Equal(t, "v", line["k"])

	out.Reset()
	text := NewLogger(&out, slog.LevelWarn, LogFormatText)
	text.Info("hidden")
	text.Warn("shown")
	assert.NotContains(t, out.String(), "hidden")
	assert.Contains(t, out.String(), "level=WARN msg=shown")
}

func memoryTracing(t *testing.T) (*tracing, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tr, err := newTracingWith(sdktrace.WithSyncer(exp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.shutdown(t.Context()) })
	return tr, exp
}

func spanNamed(t *testing.T, exp *tracetest.InMemoryExporter, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range exp.GetSpans() {
		if s.Name == name {
			return s
		}
	}
	names := []string{}
	for _, s := range exp.GetSpans() {
		names = append(names, s.Name)
	}
	t.Fatalf("no span %q among %v", name, names)
	return tracetest.SpanStub{}
}

func attr(s tracetest.SpanStub, key string) (string, bool) {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value.String(), true
		}
	}
	return "", false
}

func TestHTTPSpansCarryTheRouteAndRunID(t *testing.T) {
	tr, exp := memoryTracing(t)
	s := &Server{tracing: tr, metrics: metrics.New()}
	mux := http.NewServeMux()
	for _, route := range []string{"GET /v1/runs/{id}", "GET /healthz", "GET /v1/stacks/{id}"} {
		mux.Handle(route, s.instrument(route)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})))
	}
	srv := httptest.NewServer(tr.wrap(mux))
	t.Cleanup(srv.Close)
	runID := uuid.NewString()
	for _, path := range []string{"/v1/runs/" + runID, "/healthz", "/v1/stacks/" + runID} {
		resp, err := srv.Client().Get(srv.URL + path)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	}

	run := spanNamed(t, exp, "GET /v1/runs/{id}")
	got, ok := attr(run, string(AttrRunID))
	require.True(t, ok)
	assert.Equal(t, runID, got)
	route, _ := attr(run, "http.route")
	assert.Equal(t, "GET /v1/runs/{id}", route)
	assert.Equal(t, ServiceName, resourceServiceName(run))

	stack := spanNamed(t, exp, "GET /v1/stacks/{id}")
	_, ok = attr(stack, string(AttrRunID))
	assert.False(t, ok, "only run routes carry a run id")
	for _, sp := range exp.GetSpans() {
		assert.NotEqual(t, "GET /healthz", sp.Name, "health checks are not traced")
	}
}

func resourceServiceName(s tracetest.SpanStub) string {
	for _, kv := range s.Resource.Attributes() {
		if kv.Key == "service.name" {
			return kv.Value.AsString()
		}
	}
	return ""
}

func TestQueueSpans(t *testing.T) {
	tr, exp := memoryTracing(t)
	svc := &fakeRuns{err: errors.New("dispatch refused")}
	s := &Server{tracing: tr, log: slog.New(slog.DiscardHandler)}
	r := &registry{events: map[string]worker.EventHandler{}, jobs: map[string]worker.JobHandler{}}
	s.registerOn(r, svc)

	runID := uuid.NewString()
	jobID := uuid.New()
	err := r.jobs[runs.JobDispatchWave](t.Context(), store.Job{ID: jobID, Kind: runs.JobDispatchWave, Payload: json.RawMessage(`{"run_id":"` + runID + `","wave":1}`)})
	require.Error(t, err)
	job := spanNamed(t, exp, "job "+runs.JobDispatchWave)
	got, _ := attr(job, string(AttrRunID))
	assert.Equal(t, runID, got)
	got, _ = attr(job, string(AttrJobID))
	assert.Equal(t, jobID.String(), got)
	assert.Equal(t, "Error", job.Status.Code.String())
	require.NotEmpty(t, job.Events, "the error is recorded on the span")

	svc.err = nil
	require.NoError(t, r.events["push"](t.Context(), store.Event{ID: "d-1", Kind: "push", Payload: json.RawMessage(`{}`)}))
	ev := spanNamed(t, exp, "event push")
	got, _ = attr(ev, string(AttrDelivery))
	assert.Equal(t, "d-1", got)
}

func TestTracesAreExportedOverOTLPHTTP(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies int
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type"))
		if len(body) > 0 {
			bodies++
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)

	tr, err := newTracing(t.Context(), collector.URL+"/")
	require.NoError(t, err)
	_, span := tr.tracer.Start(t.Context(), "probe")
	span.End()
	require.NoError(t, tr.shutdown(t.Context()), "shutting down flushes pending spans")

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, paths)
	assert.Equal(t, "POST /v1/traces application/x-protobuf", paths[0])
	assert.Positive(t, bodies)
	assert.Equal(t, "http://collector:4318/v1/traces", tracesURL("http://collector:4318/"))
	assert.True(t, strings.HasSuffix(tracesURL("https://otlp.example.com/otlp"), "/otlp/v1/traces"))
}
