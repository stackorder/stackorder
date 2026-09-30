---
title: Terraform drift detection on a schedule
description: 'Run terraform plan -detailed-exitcode for every stack on a cron schedule from GitHub Actions, and keep one GitHub issue per drifted stack until it is fixed.'
---

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
| `drift.schedule` | five-field cron expression | empty | When drift checks run: minute, hour, day of month, month, day of week, in UTC. Prefix `CRON_TZ=Europe/Paris ` for another time zone. |
| `drift.open_issue` | boolean | `false` | Open or update one GitHub issue per drifted stack. |

`open_issue` needs the App's optional `Issues: Write` permission.

## What happens on schedule {#flow}

1. At each time the cron expression fires (in UTC, unless it starts with `CRON_TZ=`), the server's scheduler takes the repository's current graph, the default-branch graph when known, and enqueues one drift check per stack in it, each [instance](./instances) counting as a stack, spread evenly across the next hour, so a large repository does not start every job at once.
2. Each check reads the head of the default branch and creates a drift run for the stack, trigger `schedule`, or reuses the one it created in the same hour. It dispatches `stackorder-run.yml` with `mode: drift`, wave `0` and `sha` set to that head.
3. The job runs `stackorder drift --stack <key>`, which runs `plan -detailed-exitcode` with the stack's var files and the `drift` values of its `env` (the `plan` values where a variable has no `drift` value), and posts the result.
4. Exit code 0 means no drift. Exit code 2 marks the stack drifted and records the plan summary.
5. With `open_issue: true`, the server opens an issue titled `Drift detected in <key>`, labelled `stackorder-drift`, for a drifted stack, or updates the open one. When a later check finds no drift, it comments on the issue and closes it. There is at most one open issue per stack.

Drift jobs run in `stackorder-run.yml` under the environment `default`, whatever the stack's own environment, and assume `aws-plan-role-arn` (falling back to `aws-role-arn`), never an apply role. They never wait for an environment's reviewers, and the plan role's trust policy must admit `repo:<owner>/<repo>:environment:default`, written with the repository's own [subject prefix](/operations/security-hardening#immutable-subjects); see [Security hardening](/operations/security-hardening#trust-policies).

Only one server instance schedules at a time; the scheduler is elected leader with a Postgres advisory lock, and any instance can execute the work. A new leader catches up on at most the last hour of missed schedules, enqueuing only the latest fire of each.

## Where drift shows up {#where}

- The web UI's org overview counts drifted stacks; each stack page shows its latest drift result.
- `GET /v1/stacks/{id}` returns the latest observation under `drift`: `checked_at`, `drifted`, the plan `summary`, and the issue number and URL when there is one.
- `GET /v1/overview` and `GET /v1/repos` report drifted counts.
- The drifted-stacks gauge in [metrics](/reference/metrics).

<Screenshot
  name="ui-stack-drift"
  alt="A stack page in the web UI for stacks/prod/eks: its environment, tool and state location, then cards for the last apply, the last plan, a drift result marked drifted with one resource to change, checked two days ago, with issue #57, and the stack lock held by PR #42."
  :width="768"
  :height="603"
  caption="A stack page in the web UI, with sample data. The Drift card shows the latest drift check and its issue."
/>

The UI shows the latest row per stack. Drift history is kept 90 days by default, set by `STACKORDER_DRIFT_RETENTION`; the latest result of each stack is always kept.

## Drift from upstream changes {#cross-repo}

A stack can drift because a stack it depends on in another repository changed. With `propagate.cross_repo: plan`, the server dispatches a plan-only run on each external dependent right after the upstream apply, so this kind of drift shows up within minutes. See [Cross-repo dependencies](./cross-repo).

## Checking drift locally {#local}

The same command works on a laptop, with credentials that can read the stack's state:

```sh
stackorder drift --stack stacks/prod/vpc
echo $?   # 0: no drift, 2: drift, 1: error, 3: refused by the server
```

With `--format json` the result is machine readable. See the [CLI reference](/reference/cli#drift).
