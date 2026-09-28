package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
)

type runsMetrics interface {
	RunStatusChanged(status v1.RunStatus, trigger v1.Trigger, mode v1.RunMode)
	StackFinished(mode v1.RunMode, status v1.StackStatus, d time.Duration)
	Dispatched(mode v1.RunMode, ok bool)
	SetDriftedStacks(n int)
	SetLocksHeld(n int)
	CommandReceived(verb string, accepted bool)
}

var (
	_ runsMetrics = (*metrics.Registry)(nil)
	_ gh.Metrics  = (*metrics.Registry)(nil)
)

func touchAll(r *metrics.Registry) {
	r.RunStatusChanged(v1.RunPlanning, v1.TriggerPullRequest, v1.ModePlan)
	r.StackFinished(v1.ModeApply, v1.StackApplied, 42*time.Second)
	r.Dispatched(v1.ModeApply, true)
	r.SetDriftedStacks(3)
	r.SetLocksHeld(2)
	r.CommandReceived("apply", true)
	r.WebhookReceived("push")
	r.WebhookDuplicate()
	r.ObserveWebhookLag(150 * time.Millisecond)
	r.EventProcessed("push", metrics.ResultDead)
	r.JobProcessed("reconcile", metrics.ResultOK)
	r.SetQueueDepth(metrics.QueueEvents, 7)
	r.SetSchedulerLeader(true)
	r.ObserveRequest(http.MethodPost, "/repos/{owner}/{repo}/check-runs", http.StatusCreated, 80*time.Millisecond)
	r.ObserveRateLimit(4999, time.Unix(1_800_000_000, 0))
	r.HTTPMiddleware("GET /v1/me")(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/me", nil))
}

func TestRegistryExportsEveryMetric(t *testing.T) {
	first, second := metrics.New(), metrics.New()
	touchAll(first)
	touchAll(second)

	families, err := first.Registry().Gather()
	require.NoError(t, err)
	var names []string
	goRuntime := false
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), metrics.Namespace+"_") {
			names = append(names, f.GetName())
		}
		goRuntime = goRuntime || f.GetName() == "go_goroutines"
	}
	want := []string{
		"stackorder_build_info",
		"stackorder_commands_total",
		"stackorder_dispatches_total",
		"stackorder_drifted_stacks",
		"stackorder_events_dead_total",
		"stackorder_events_processed_total",
		"stackorder_github_rate_limit_remaining",
		"stackorder_github_request_duration_seconds",
		"stackorder_github_requests_total",
		"stackorder_http_request_duration_seconds",
		"stackorder_http_requests_total",
		"stackorder_jobs_processed_total",
		"stackorder_locks_held",
		"stackorder_queue_depth",
		"stackorder_runs_total",
		"stackorder_scheduler_leader",
		"stackorder_stack_duration_seconds",
		"stackorder_stack_finished_total",
		"stackorder_webhook_duplicates_total",
		"stackorder_webhook_lag_seconds",
		"stackorder_webhook_received_total",
	}
	assert.Equal(t, want, names)
	assert.True(t, goRuntime, "the Go runtime collector is registered")

	problems, err := testutil.GatherAndLint(first.Registry(), want...)
	require.NoError(t, err)
	assert.Empty(t, problems)
}

func TestRecording(t *testing.T) {
	cases := []struct {
		name   string
		record func(r *metrics.Registry)
		series string
		want   float64
	}{
		{
			name: "run status",
			record: func(r *metrics.Registry) {
				r.RunStatusChanged(v1.RunApplied, v1.TriggerComment, v1.ModeApply)
				r.RunStatusChanged(v1.RunApplied, v1.TriggerComment, v1.ModeApply)
				r.RunStatusChanged(v1.RunFailed, v1.TriggerComment, v1.ModeApply)
			},
			series: `stackorder_runs_total{mode="apply",status="applied",trigger="comment"}`,
			want:   2,
		},
		{
			name:   "stack finished",
			record: func(r *metrics.Registry) { r.StackFinished(v1.ModePlan, v1.StackPlanned, time.Minute) },
			series: `stackorder_stack_finished_total{mode="plan",status="planned"}`,
			want:   1,
		},
		{
			name: "stack duration observed only when known",
			record: func(r *metrics.Registry) {
				r.StackFinished(v1.ModeDrift, v1.StackPlanned, 90*time.Second)
				r.StackFinished(v1.ModeDrift, v1.StackUnknown, 0)
			},
			series: `stackorder_stack_duration_seconds_count{mode="drift"}`,
			want:   1,
		},
		{
			name:   "dispatch failed",
			record: func(r *metrics.Registry) { r.Dispatched(v1.ModeDrift, false) },
			series: `stackorder_dispatches_total{mode="drift",result="error"}`,
			want:   1,
		},
		{
			name:   "dispatch ok",
			record: func(r *metrics.Registry) { r.Dispatched(v1.ModeApply, true) },
			series: `stackorder_dispatches_total{mode="apply",result="ok"}`,
			want:   1,
		},
		{
			name:   "drifted stacks",
			record: func(r *metrics.Registry) { r.SetDriftedStacks(5); r.SetDriftedStacks(4) },
			series: "stackorder_drifted_stacks",
			want:   4,
		},
		{
			name:   "locks held",
			record: func(r *metrics.Registry) { r.SetLocksHeld(9) },
			series: "stackorder_locks_held",
			want:   9,
		},
		{
			name:   "command refused",
			record: func(r *metrics.Registry) { r.CommandReceived("unlock", false) },
			series: `stackorder_commands_total{accepted="false",verb="unlock"}`,
			want:   1,
		},
		{
			name:   "unknown command verb",
			record: func(r *metrics.Registry) { r.CommandReceived("deploy-everything-now", false) },
			series: `stackorder_commands_total{accepted="false",verb="other"}`,
			want:   1,
		},
		{
			name:   "webhook received",
			record: func(r *metrics.Registry) { r.WebhookReceived("pull_request"); r.WebhookReceived("pull_request") },
			series: `stackorder_webhook_received_total{event="pull_request"}`,
			want:   2,
		},
		{
			name:   "webhook duplicate",
			record: func(r *metrics.Registry) { r.WebhookDuplicate() },
			series: "stackorder_webhook_duplicates_total",
			want:   1,
		},
		{
			name:   "webhook lag",
			record: func(r *metrics.Registry) { r.ObserveWebhookLag(time.Second); r.ObserveWebhookLag(-time.Second) },
			series: "stackorder_webhook_lag_seconds_count",
			want:   2,
		},
		{
			name:   "event processed",
			record: func(r *metrics.Registry) { r.EventProcessed("push", metrics.ResultError) },
			series: `stackorder_events_processed_total{kind="push",result="error"}`,
			want:   1,
		},
		{
			name: "event dead",
			record: func(r *metrics.Registry) {
				r.EventProcessed("check_run", metrics.ResultDead)
				r.EventProcessed("check_run", metrics.ResultError)
			},
			series: `stackorder_events_dead_total{kind="check_run"}`,
			want:   1,
		},
		{
			name:   "job processed",
			record: func(r *metrics.Registry) { r.JobProcessed("prune", metrics.ResultIgnored) },
			series: `stackorder_jobs_processed_total{kind="prune",result="ignored"}`,
			want:   1,
		},
		{
			name:   "queue depth",
			record: func(r *metrics.Registry) { r.SetQueueDepth(metrics.QueueJobs, 12) },
			series: `stackorder_queue_depth{queue="jobs"}`,
			want:   12,
		},
		{
			name:   "scheduler leader",
			record: func(r *metrics.Registry) { r.SetSchedulerLeader(true); r.SetSchedulerLeader(false) },
			series: "stackorder_scheduler_leader",
			want:   0,
		},
		{
			name: "github request",
			record: func(r *metrics.Registry) {
				r.ObserveRequest(http.MethodGet, "/repos/{owner}/{repo}/pulls/{pull_number}", 0, time.Second)
			},
			series: `stackorder_github_requests_total{route="GET /repos/{owner}/{repo}/pulls/{pull_number}",status="0"}`,
			want:   1,
		},
		{
			name: "github request duration",
			record: func(r *metrics.Registry) {
				r.ObserveRequest(http.MethodPatch, "/repos/{owner}/{repo}/check-runs/{check_run_id}", http.StatusOK, time.Second)
				r.ObserveRequest(http.MethodPatch, "/repos/{owner}/{repo}/check-runs/{check_run_id}", http.StatusBadGateway, time.Second)
			},
			series: `stackorder_github_request_duration_seconds_count{route="PATCH /repos/{owner}/{repo}/check-runs/{check_run_id}"}`,
			want:   2,
		},
		{
			name:   "rate limit",
			record: func(r *metrics.Registry) { r.ObserveRateLimit(123, time.Now()) },
			series: "stackorder_github_rate_limit_remaining",
			want:   123,
		},
		{
			name:   "build info",
			record: func(*metrics.Registry) {},
			series: `stackorder_build_info{commit="none",version="dev"}`,
			want:   1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := metrics.New()
			tc.record(r)
			assert.InDelta(t, tc.want, sample(t, r, tc.series), 1e-9)
		})
	}
}

func TestNilRegistryDiscards(t *testing.T) {
	var r *metrics.Registry
	require.NotPanics(t, func() { touchAll(r) })
	h := http.NotFoundHandler()
	wrapped := r.HTTPMiddleware("GET /x")(h)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHTTPMiddleware(t *testing.T) {
	r := metrics.New()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/runs", r.HTTPMiddleware("POST /v1/runs")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	})))
	mux.Handle("/v1/me", r.HTTPMiddleware("/v1/me")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	})))
	mux.Handle("GET /v1/runs/{id}", r.HTTPMiddleware("GET /v1/runs/{id}")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	})))
	mux.Handle("GET /v1/empty", r.HTTPMiddleware("GET /v1/empty")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/v1/runs"},
		{http.MethodPost, "/v1/runs"},
		{http.MethodGet, "/v1/me"},
		{"BREW", "/v1/me"},
		{http.MethodGet, "/v1/runs/1"},
		{http.MethodGet, "/v1/runs/2"},
		{http.MethodGet, "/v1/empty"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, nil))
	}

	counts := []struct {
		series string
		want   float64
	}{
		{`stackorder_http_requests_total{method="POST",route="POST /v1/runs",status="201"}`, 2},
		{`stackorder_http_requests_total{method="GET",route="/v1/me",status="200"}`, 1},
		{`stackorder_http_requests_total{method="OTHER",route="/v1/me",status="200"}`, 1},
		{`stackorder_http_requests_total{method="GET",route="GET /v1/runs/{id}",status="404"}`, 2},
		{`stackorder_http_requests_total{method="GET",route="GET /v1/empty",status="200"}`, 1},
	}
	for _, c := range counts {
		assert.InDelta(t, c.want, sample(t, r, c.series), 1e-9, c.series)
	}
	assert.InDelta(t, 2, sample(t, r, `stackorder_http_request_duration_seconds_count{method="GET",route="GET /v1/runs/{id}"}`), 1e-9)
	n, err := testutil.GatherAndCount(r.Registry(), "stackorder_http_requests_total")
	require.NoError(t, err)
	assert.Equal(t, 5, n, "one series per route, method and status, never per path")
}

func TestHTTPMiddlewareKeepsResponseController(t *testing.T) {
	r := metrics.New()
	var flushErr error
	h := r.HTTPMiddleware("GET /stream")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "x")
		flushErr = http.NewResponseController(w).Flush()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))
	require.NoError(t, flushErr)
	assert.True(t, rec.Flushed)
}

func TestHandlerServesTextExposition(t *testing.T) {
	r := metrics.New()
	r.WebhookReceived("push")
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/plain")
	text := string(body)
	assert.Contains(t, text, `stackorder_build_info{commit="none",version="dev"} 1`)
	assert.Contains(t, text, `stackorder_webhook_received_total{event="push"} 1`)
	assert.Contains(t, text, "# TYPE stackorder_webhook_lag_seconds histogram")
	assert.Contains(t, text, "go_goroutines")
}

func sample(t *testing.T, r *metrics.Registry, series string) float64 {
	t.Helper()
	value, ok := metricstest.Scrape(t, r)[series]
	require.True(t, ok, "no sample %s", series)
	return value
}
