# Metrics and tracing

The server exposes Prometheus metrics at `GET /metrics` and exports OpenTelemetry traces when an OTLP endpoint is configured. Logs are structured JSON by default.

## What is measured {#measured}

These are the measurements the server provides from day one:

- runs by status and trigger;
- plan and apply duration per stack;
- a gauge of drifted stacks;
- a gauge of locks held;
- webhook lag;
- GitHub API rate limit remaining.

## Metric names {#names}

::: info Proposed names
The names below are the proposed contract for the measurements above, plus supporting metrics. All carry the `stackorder_` prefix.
:::

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `stackorder_runs_total` | counter | `repo`, `trigger`, `mode`, `status` | Runs reaching each status, by trigger and mode |
| `stackorder_stack_duration_seconds` | histogram | `repo`, `stack`, `mode` | Plan, apply and drift duration per stack, from the CLI's reported `duration_ms` |
| `stackorder_drifted_stacks` | gauge | `repo` | Stacks whose latest drift check found drift |
| `stackorder_locks_held` | gauge | `repo` | Orchestration locks currently held |
| `stackorder_webhook_lag_seconds` | histogram | `event` | Time from receiving a webhook to a worker finishing it |
| `stackorder_github_rate_limit_remaining` | gauge | `installation` | The rate limit remaining on the last GitHub API response |
| `stackorder_webhook_deliveries_total` | counter | `event`, `result` | Deliveries received; `result` is `accepted`, `duplicate` or `rejected` |
| `stackorder_queue_depth` | gauge | `queue` | Unclaimed rows in the `events` and `jobs` queues |
| `stackorder_dispatches_total` | counter | `repo`, `mode` | `workflow_dispatch` calls made to `stackorder-run.yml` |
| `stackorder_http_request_duration_seconds` | histogram | `route`, `method`, `code` | API request latency, by route pattern |
| `stackorder_build_info` | gauge | `version`, `commit` | Always 1; carries the build version |
| `stackorder_scheduler_leader` | gauge | | 1 on the instance that holds the scheduler's advisory lock |

The Go runtime and process collectors of `prometheus/client_golang` are registered too, as `go_*` and `process_*`.

The `stack` label has one value per stack. For a few hundred stacks that is a modest number of series.

## Scraping {#scraping}

```yaml
scrape_configs:
  - job_name: stackorder
    scheme: https
    metrics_path: /metrics
    static_configs:
      - targets: ["stackorder.example.com"]
```

With more than one instance, scrape each task directly rather than through the load balancer, so every instance's counters are collected. If the load balancer is internet-facing, restrict `/metrics` to your monitoring network.

## Alerts worth having {#alerts}

```yaml
groups:
  - name: stackorder
    rules:
      - alert: StackorderStacksDrifted
        expr: sum(stackorder_drifted_stacks) > 0
        for: 1h
      - alert: StackorderWebhookLag
        expr: histogram_quantile(0.95, sum by (le) (rate(stackorder_webhook_lag_seconds_bucket[10m]))) > 60
        for: 10m
      - alert: StackorderGitHubRateLimitLow
        expr: min(stackorder_github_rate_limit_remaining) < 500
        for: 5m
      - alert: StackorderQueueBacklog
        expr: sum(stackorder_queue_depth) > 100
        for: 15m
```

Also alert on `/readyz`: it fails when the database is unreachable.

## Tracing {#tracing}

Tracing uses OpenTelemetry with the OTLP HTTP exporter. It is enabled only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; the exporter reads the standard OTLP environment variables.

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
```

Every span carries the run id as an attribute, so a run can be followed from the webhook that started it, through the workers and GitHub API calls, to the check run it updated. The proposed attribute name is `stackorder.run_id`.

## Logs {#logs}

The server logs with `log/slog`: JSON by default, text with `STACKORDER_LOG_FORMAT=text`. `STACKORDER_LOG_LEVEL` sets the level, `info` by default.
