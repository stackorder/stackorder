---
description: 'Prometheus metrics with the stackorder_ prefix for runs, stacks, dispatches, drift, locks and GitHub requests, plus OpenTelemetry tracing and structured logs.'
---

# Metrics and tracing

The server exposes Prometheus metrics at `GET /metrics` and exports OpenTelemetry traces when an OTLP endpoint is configured. Logs are structured JSON by default.

## Metric names {#names}

Every metric is defined in `internal/metrics` and carries the `stackorder_` prefix. Label values that could come from outside, such as event names or HTTP methods, are bounded: a value outside the known set is counted as `other` (`OTHER` for methods). The `installation` label on `stackorder_github_rate_limit_remaining` is the exception: it is recorded verbatim, so its cardinality is unbounded.

### Runs and stacks {#runs}

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_runs_created_total` | counter | `trigger`, `mode` | Runs created, by trigger and mode; incremented once per run row, at every trigger (pull request, comment, manual and scheduled drift) |
| `stackorder_runs_total` | counter | `status`, `trigger`, `mode` | Each status a run enters, by trigger and mode; a run contributes to several series over its life, so this counts status transitions, not runs. Count runs with `stackorder_runs_created_total` |
| `stackorder_stack_finished_total` | counter | `mode`, `status` | Stacks reaching a terminal status within a run: `applied`, `planned` for plan runs, `failed`, `blocked`, `noop`, `unconfirmed`, `unknown`, `skipped` |
| `stackorder_stack_duration_seconds` | histogram | `mode` | Duration of each stack's plan, apply or drift job, from the row's start and finish times or the CLI's reported `duration_ms`; buckets from 5 s to 2 h |
| `stackorder_dispatches_total` | counter | `mode`, `result` | `workflow_dispatch` calls to `stackorder-run.yml`; `result` is `ok` or `error` |
| `stackorder_drifted_stacks` | gauge | | Stacks whose latest drift check found drift |
| `stackorder_locks_held` | gauge | | Orchestration locks held |
| `stackorder_pending_object_deletions` | gauge | | Plan objects waiting to be removed from the [artifact bucket](/reference/server-configuration#artifact-bucket) after the last prune; stays above zero when the server's role lacks `s3:DeleteObject` |
| `stackorder_commands_total` | counter | `verb`, `accepted` | Pull request comment commands; `verb` is `plan`, `apply`, `unlock`, `help` or `other`, `accepted` is `true` or `false` |

`stackorder_drifted_stacks`, `stackorder_locks_held` and `stackorder_pending_object_deletions` are read from the database by the instance that records a drift result, takes or releases a lock, or last ran the prune job. An instance that has not done so since it started reports `0`, so aggregate them with `max()` across instances.

### Artifact store {#artifacts}

These appear only when `STACKORDER_ARTIFACT_BUCKET` is set; see [Server configuration](/reference/server-configuration#artifact-bucket).

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_artifact_operations_total` | counter | `operation`, `result` | Reads and writes against the artifact store; `operation` is `write` or `read`, `result` is `ok` or `error` |

The server writes a stack's full plan text and plan JSON to the bucket and reads the plan text back to serve `GET /v1/runs/{id}/stacks/{key}/plan`. A failed write (`operation="write",result="error"`) marks any bucket `Put` that failed, of the plan text or the plan JSON, which a broken bucket policy or credentials cause silently; only a failed plan-text write makes the server fall back to keeping the truncated plan text in Postgres, while a failed plan-JSON write still serves the full plan text from the bucket. A read of an object that is gone counts as `ok`, as the store answered. Compute an error rate with `sum by (operation) (rate(stackorder_artifact_operations_total{result="error"}[5m])) / sum by (operation) (rate(stackorder_artifact_operations_total[5m]))`; aggregating the `result` label away on both sides keeps the division from matching the error series against itself.

### Webhooks and the queue {#queue}

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_webhook_received_total` | counter | `event` | Deliveries whose signature verified, by event |
| `stackorder_webhook_duplicates_total` | counter | | Deliveries whose id was already queued |
| `stackorder_webhook_lag_seconds` | histogram | | Time from receiving a webhook to a worker first starting on it; buckets from 10 ms to 10 min |
| `stackorder_events_processed_total` | counter | `kind`, `result` | Queued webhook events processed; `result` is `ok`, `error` (retried), `dead` or `ignored` (no handler) |
| `stackorder_events_dead_total` | counter | `kind` | Events given up after their last attempt |
| `stackorder_jobs_processed_total` | counter | `kind`, `result` | Queued jobs processed, by job kind, with the same results |
| `stackorder_queue_depth` | gauge | `queue` | Due rows no worker has claimed yet, for `events` and `jobs`; refreshed once a minute |
| `stackorder_scheduler_leader` | gauge | | `1` while this instance holds the scheduler's advisory lock |

A failed event or job is retried after 1 min, 5 min, 30 min and 2 h, and becomes a dead letter after 5 attempts. Job kinds are `dispatch_wave`, `drift_stack`, `schedule_drift`, `cross_repo_plan`, `reconcile`, `prune`, `stale_locks` and `sync_installations`.

### GitHub API {#github}

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_github_requests_total` | counter | `route`, `status` | GitHub API request attempts; `route` is `METHOD /template`, such as `POST /repos/{owner}/{repo}/check-runs`, and `status` is the HTTP status, `0` when no response arrived |
| `stackorder_github_request_duration_seconds` | histogram | `route` | Latency of each attempt; buckets from 50 ms to 30 s |
| `stackorder_github_rate_limit_remaining` | gauge | `installation` | `X-RateLimit-Remaining` of the last GitHub API response drawn from that budget; `installation` is the installation id, or `app` for the App JWT. Absent until the first response, and so never zero on a freshly started server. Requests made with a user access token (the sign-in flow) are not tracked |

### HTTP and build {#http}

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_http_requests_total` | counter | `route`, `method`, `status` | Requests served; `route` is the route pattern, such as `POST /v1/runs/{id}/graph`, or `/` for the UI |
| `stackorder_http_request_duration_seconds` | histogram | `route`, `method` | Request latency, with the Prometheus default buckets |
| `stackorder_build_info` | gauge | `version`, `commit` | Always `1`; the labels carry the build |

The Go runtime and process collectors of `prometheus/client_golang` are registered too, as `go_*` and `process_*`. Labelled metrics, including `github_rate_limit_remaining`, appear only once a value has been observed for a label set.

## Scraping {#scraping}

```yaml
scrape_configs:
  - job_name: stackorder
    scheme: https
    metrics_path: /metrics
    static_configs:
      - targets: ["stackorder.example.com"]
```

With more than one instance, scrape each instance directly rather than through the load balancer, so every instance's counters are collected. `GET /metrics` is open unless `STACKORDER_METRICS_TOKEN` is set, in which case the scrape needs `Authorization: Bearer <token>`; see [Security hardening](/operations/security-hardening#metrics) for the Prometheus configuration.

## Alerts worth having {#alerts}

```yaml
groups:
  - name: stackorder
    rules:
      - alert: StackorderStacksDrifted
        expr: max(stackorder_drifted_stacks) > 0
        for: 1h
      - alert: StackorderWebhookLag
        expr: histogram_quantile(0.95, sum by (le) (rate(stackorder_webhook_lag_seconds_bucket[10m]))) > 60
        for: 10m
      - alert: StackorderGitHubRateLimitLow
        expr: min by (installation) (stackorder_github_rate_limit_remaining{installation!=""}) < 500
        for: 5m
      - alert: StackorderQueueBacklog
        expr: max by (queue) (stackorder_queue_depth) > 100
        for: 15m
      - alert: StackorderDeadLetters
        expr: sum(increase(stackorder_events_dead_total[1h])) > 0
      - alert: StackorderDispatchErrors
        expr: sum(increase(stackorder_dispatches_total{result="error"}[15m])) > 0
      - alert: StackorderArtifactWriteErrors
        expr: sum(increase(stackorder_artifact_operations_total{operation="write",result="error"}[15m])) > 0
      - alert: StackorderNoSchedulerLeader
        expr: max(stackorder_scheduler_leader) < 1
        for: 10m
```

`StackorderGitHubRateLimitLow` matches `installation!=""` so that a v0.5.0 replica still running during a rolling upgrade, which exports the gauge unlabelled and at `0` while idle, does not group under an empty `installation` and trip the alert; once every replica is on this release the matcher is a no-op.

Also alert on `/readyz`: it fails when the database is unreachable.

## Tracing {#tracing}

Tracing uses OpenTelemetry with the OTLP HTTP exporter. It is enabled only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, to the collector's base URL; spans are sent to `<endpoint>/v1/traces`. The exporter's other standard `OTEL_EXPORTER_OTLP_*` variables, such as headers, still apply.

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
```

| Span | Name | Attributes |
| --- | --- | --- |
| HTTP request | The route pattern, such as `POST /v1/runs/{id}/graph` | `http.route`; `stackorder.run_id` on routes under `/v1/runs/{id}` |
| Webhook event | `event <name>`, such as `event pull_request` | `stackorder.event`, `stackorder.delivery` |
| Job | `job <kind>`, such as `job dispatch_wave` | `stackorder.job`, `stackorder.job_id`, and `stackorder.run_id` when the job belongs to a run |

The resource's `service.name` is `stackorder-server`, with the build version as `service.version`. W3C trace context and baggage are propagated from incoming requests. `/healthz`, `/readyz` and `/metrics` are not traced.

A webhook delivery and the worker that later handles it, and a job and the worker that runs it, are one trace: the trace context of the request or job that queued a row is stored with it and continued when the row is claimed, so the worker span is a child of the span that queued the work and shares its trace id. A retry is a new child span under the same trace, and a span that starts long after its parent ended shows the queue delay. Rows queued before this release, and rows queued while tracing is off, carry no context, and their worker spans are roots as before.

## Logs {#logs}

The server logs with `log/slog` to standard error: JSON by default, text with `STACKORDER_LOG_FORMAT=text`. `STACKORDER_LOG_LEVEL` sets the level, `info` by default. Every request is logged once, as `http request`, with its `request_id` (also returned in `X-Request-Id`), route, status, duration and the kind of caller (`oidc`, `apikey`, `session` or `none`).

When tracing is on, every log record written inside a span, the access log and the event and job handlers included, also carries `trace_id` and `span_id`, so a log line can be matched to its trace and, through the stored trace context, to the request or job that queued the work. Records written outside any span, such as start-up and shutdown lines, carry neither.
