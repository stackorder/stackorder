# Metrics and tracing

The server exposes Prometheus metrics at `GET /metrics` and exports OpenTelemetry traces when an OTLP endpoint is configured. Logs are structured JSON by default.

## Metric names {#names}

Every metric is defined in `internal/metrics` and carries the `stackorder_` prefix. Label values that could come from outside, such as event names or HTTP methods, are bounded: a value outside the known set is counted as `other` (`OTHER` for methods).

### Runs and stacks {#runs}

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_runs_total` | counter | `status`, `trigger`, `mode` | Runs entering each status, by trigger and mode |
| `stackorder_stack_finished_total` | counter | `mode`, `status` | Stacks reaching a terminal status within a run: `applied`, `planned` for plan runs, `failed`, `blocked`, `noop`, `unconfirmed`, `unknown`, `skipped` |
| `stackorder_stack_duration_seconds` | histogram | `mode` | Duration of each stack's plan, apply or drift job, from the row's start and finish times or the CLI's reported `duration_ms`; buckets from 5 s to 2 h |
| `stackorder_dispatches_total` | counter | `mode`, `result` | `workflow_dispatch` calls to `stackorder-run.yml`; `result` is `ok` or `error` |
| `stackorder_drifted_stacks` | gauge | | Stacks whose latest drift check found drift |
| `stackorder_locks_held` | gauge | | Orchestration locks held |
| `stackorder_commands_total` | counter | `verb`, `accepted` | Pull request comment commands; `verb` is `plan`, `apply`, `unlock`, `help` or `other`, `accepted` is `true` or `false` |

`stackorder_drifted_stacks` and `stackorder_locks_held` are read from the database by the instance that records a drift result or takes or releases a lock. An instance that has done neither since it started reports `0`, so aggregate them with `max()` across instances.

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
| `stackorder_github_rate_limit_remaining` | gauge | | `X-RateLimit-Remaining` of the last GitHub API response |

### HTTP and build {#http}

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_http_requests_total` | counter | `route`, `method`, `status` | Requests served; `route` is the route pattern, such as `POST /v1/runs/{id}/graph`, or `/` for the UI |
| `stackorder_http_request_duration_seconds` | histogram | `route`, `method` | Request latency, with the Prometheus default buckets |
| `stackorder_build_info` | gauge | `version`, `commit` | Always `1`; the labels carry the build |

The Go runtime and process collectors of `prometheus/client_golang` are registered too, as `go_*` and `process_*`. Counters and histograms with labels appear only once they have been observed.

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
        expr: min(stackorder_github_rate_limit_remaining) < 500
        for: 5m
      - alert: StackorderQueueBacklog
        expr: max by (queue) (stackorder_queue_depth) > 100
        for: 15m
      - alert: StackorderDeadLetters
        expr: sum(increase(stackorder_events_dead_total[1h])) > 0
      - alert: StackorderDispatchErrors
        expr: sum(increase(stackorder_dispatches_total{result="error"}[15m])) > 0
      - alert: StackorderNoSchedulerLeader
        expr: max(stackorder_scheduler_leader) < 1
        for: 10m
```

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

## Logs {#logs}

The server logs with `log/slog` to standard error: JSON by default, text with `STACKORDER_LOG_FORMAT=text`. `STACKORDER_LOG_LEVEL` sets the level, `info` by default. Every request is logged once, as `http request`, with its `request_id` (also returned in `X-Request-Id`), route, status, duration and the kind of caller (`oidc`, `apikey`, `session` or `none`).
