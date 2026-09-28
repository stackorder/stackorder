# Drift detection

Drift is a difference between what a stack's code on the default branch says and what is deployed. Stackorder finds it by running a plan on a schedule. It never applies to fix drift; that stays a pull request.

## Enable it

Set a schedule in `stackorder.yaml`. Empty, the default, disables drift runs.

```yaml
drift:
  schedule: "0 6 * * 1-5"   # 06:00, Monday to Friday
  open_issue: true
```

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `drift.schedule` | five-field cron expression | empty | When drift checks run: minute, hour, day of month, month, day of week. |
| `drift.open_issue` | boolean | `false` | Open or update one GitHub issue per drifted stack. |

`open_issue` needs the App's optional `Issues: Write` permission.

## What happens on schedule {#flow}

1. The server's scheduler dispatches `stackorder-run.yml` with `mode: drift` for each stack, with `sha` set to the head of the default branch. Dispatches are staggered across the hour, so a large repository does not start every drift job at once.
2. The job runs `stackorder drift --stack <key>`, which runs `plan -detailed-exitcode` and uploads the result.
3. Exit code 0 means no drift. Exit code 2 marks the stack drifted and records the plan summary.
4. With `open_issue: true`, the server opens a GitHub issue for a drifted stack, or updates the one it already opened. There is one issue per stack.

Only one server instance schedules at a time; the scheduler is elected leader with a Postgres advisory lock, and any instance can execute the work.

## Where drift shows up {#where}

- The web UI's org overview counts drifted stacks; each stack page shows its latest drift result.
- `GET /v1/stacks/{id}` returns the latest observation under `drift`: `checked_at`, `drifted`, the plan `summary`, and the issue number and URL when there is one.
- `GET /v1/overview` and `GET /v1/repos` report drifted counts.
- The drifted-stacks gauge in [metrics](/reference/metrics).

The UI shows the latest row per stack. Drift history is kept 90 days by default, set by `STACKORDER_DRIFT_RETENTION`.

## Drift from upstream changes {#cross-repo}

A stack can drift because a stack it depends on in another repository changed. With `propagate.cross_repo: plan`, the server dispatches a plan-only run on each external dependent right after the upstream apply, so this kind of drift shows up within minutes. See [Cross-repo dependencies](./cross-repo).

## Checking drift locally {#local}

The same command works on a laptop, with credentials that can read the stack's state:

```sh
stackorder drift --stack stacks/prod/vpc
echo $?   # 0: no drift, 2: drift, 1: error
```

With `--format json` the result is machine readable. See the [CLI reference](/reference/cli#drift).
