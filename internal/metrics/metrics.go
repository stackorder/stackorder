// Package metrics owns the server's Prometheus registry and every metric
// name the server exports, all prefixed with stackorder_. A Registry is
// the one place that records run outcomes, dispatches, drift and lock
// gauges, comment commands, webhook deliveries, queue processing, GitHub
// API calls, scheduler leadership and HTTP requests; it implements the
// metrics interfaces that internal/runs and internal/gh accept.
//
// A nil *Registry discards every observation, so packages can take one as
// an optional dependency.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/stackorder/stackorder/internal/version"
)

// Namespace prefixes every metric name.
const Namespace = "stackorder"

// Queue names reported by the queue_depth gauge.
const (
	QueueEvents = "events"
	QueueJobs   = "jobs"
)

// Result is the outcome of processing one queued event or job.
type Result string

const (
	// ResultOK means the handler succeeded and the row is done.
	ResultOK Result = "ok"
	// ResultError means the handler failed and the row will be retried.
	ResultError Result = "error"
	// ResultDead means the row ran out of attempts and was given up.
	ResultDead Result = "dead"
	// ResultIgnored means no handler is registered for the kind and the
	// row was completed without work.
	ResultIgnored Result = "ignored"
)

var (
	stackDurationBuckets = []float64{5, 10, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200}
	webhookLagBuckets    = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}
	githubBuckets        = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
)

// Registry is the server's Prometheus registry with every stackorder_
// metric registered on it, plus the Go runtime and process collectors.
// It is safe for concurrent use.
type Registry struct {
	reg *prometheus.Registry

	runs          *prometheus.CounterVec
	stackFinished *prometheus.CounterVec
	stackDuration *prometheus.HistogramVec
	dispatches    *prometheus.CounterVec
	driftedStacks prometheus.Gauge
	locksHeld     prometheus.Gauge
	commands      *prometheus.CounterVec

	webhookReceived   *prometheus.CounterVec
	webhookDuplicates prometheus.Counter
	webhookLag        prometheus.Histogram
	eventsProcessed   *prometheus.CounterVec
	eventsDead        *prometheus.CounterVec
	jobsProcessed     *prometheus.CounterVec
	queueDepth        *prometheus.GaugeVec
	schedulerLeader   prometheus.Gauge

	githubRequests  *prometheus.CounterVec
	githubDuration  *prometheus.HistogramVec
	githubRateLimit prometheus.Gauge

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	buildInfo *prometheus.GaugeVec
}

// New returns a Registry with every metric registered and build_info set
// from internal/version.
func New() *Registry {
	r := &Registry{
		reg: prometheus.NewRegistry(),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "runs_total",
			Help: "Runs entering each status, by trigger and mode.",
		}, []string{"status", "trigger", "mode"}),
		stackFinished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "stack_finished_total",
			Help: "Stacks reaching a terminal status within a run, by mode.",
		}, []string{"mode", "status"}),
		stackDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "stack_duration_seconds",
			Help:    "Plan, apply and drift duration per stack as reported by the CLI.",
			Buckets: stackDurationBuckets,
		}, []string{"mode"}),
		dispatches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "dispatches_total",
			Help: "workflow_dispatch calls to stackorder-run.yml, by mode and result.",
		}, []string{"mode", "result"}),
		driftedStacks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "drifted_stacks",
			Help: "Stacks whose latest drift check found drift.",
		}),
		locksHeld: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "locks_held",
			Help: "Orchestration locks currently held.",
		}),
		commands: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "commands_total",
			Help: "PR comment commands received, by verb and whether they were accepted.",
		}, []string{"verb", "accepted"}),
		webhookReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "webhook_received_total",
			Help: "Webhook deliveries with a valid signature, by event.",
		}, []string{"event"}),
		webhookDuplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace, Name: "webhook_duplicates_total",
			Help: "Webhook deliveries whose delivery id was already queued.",
		}),
		webhookLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "webhook_lag_seconds",
			Help:    "Time from receiving a webhook to a worker first starting on it.",
			Buckets: webhookLagBuckets,
		}),
		eventsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "events_processed_total",
			Help: "Queued webhook events processed, by kind and result.",
		}, []string{"kind", "result"}),
		eventsDead: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "events_dead_total",
			Help: "Queued webhook events given up after their last attempt, by kind.",
		}, []string{"kind"}),
		jobsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "jobs_processed_total",
			Help: "Queued jobs processed, by kind and result.",
		}, []string{"kind", "result"}),
		queueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "queue_depth",
			Help: "Due rows no worker has claimed yet, by queue.",
		}, []string{"queue"}),
		schedulerLeader: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "scheduler_leader",
			Help: "1 while this instance holds the scheduler's advisory lock.",
		}),
		githubRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "github_requests_total",
			Help: "GitHub API request attempts, by route and HTTP status (0 when no response arrived).",
		}, []string{"route", "status"}),
		githubDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "github_request_duration_seconds",
			Help:    "GitHub API request attempt latency, by route.",
			Buckets: githubBuckets,
		}, []string{"route"}),
		githubRateLimit: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "github_rate_limit_remaining",
			Help: "Rate limit remaining on the last GitHub API response.",
		}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "http_requests_total",
			Help: "HTTP requests served, by route, method and status.",
		}, []string{"route", "method", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "http_request_duration_seconds",
			Help:    "HTTP request latency, by route and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method"}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "build_info",
			Help: "Always 1; labels carry the server's build version and commit.",
		}, []string{"version", "commit"}),
	}
	r.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		r.runs, r.stackFinished, r.stackDuration, r.dispatches, r.driftedStacks, r.locksHeld, r.commands,
		r.webhookReceived, r.webhookDuplicates, r.webhookLag,
		r.eventsProcessed, r.eventsDead, r.jobsProcessed, r.queueDepth, r.schedulerLeader,
		r.githubRequests, r.githubDuration, r.githubRateLimit,
		r.httpRequests, r.httpDuration,
		r.buildInfo,
	)
	r.buildInfo.WithLabelValues(version.Version, version.Commit).Set(1)
	return r
}

// Registry returns the underlying Prometheus registry, for registering
// further collectors and for tests.
func (r *Registry) Registry() *prometheus.Registry {
	return r.reg
}

// Handler serves the registry in the Prometheus text exposition format.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}
