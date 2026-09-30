---
title: Stackorder JSON API reference
description: 'Reference for the Stackorder JSON API used by runners, people and automation: conventions, authentication, errors, paging, runs, stacks, graphs and audit.'
---

# API

The server exposes one JSON API for three callers: runners, people and automation. All request and response bodies are the types in the Go package `github.com/stackorder/stackorder/api/v1`, which automation can import to decode responses.

Fields are only ever added within `v1`. Clients must ignore fields they do not know. Fields that are empty are left out of responses, except where a table below says otherwise.

## Conventions {#conventions}

- Bodies are JSON. A request with a body must send `Content-Type: application/json` (or no content type), one JSON value and nothing after it. Unknown request fields are ignored.
- Times are RFC 3339 in UTC, such as `2026-09-28T09:14:03Z`.
- Run, stack and module ids are UUIDs. Repository and installation ids are GitHub's numeric ids.
- Stack keys in paths are percent-encoded, so slashes become `%2F`: `POST /v1/runs/{id}/stacks/stacks%2Fprod%2Fvpc/result`.
- Every response carries `X-Request-Id`, echoing a valid incoming one or a new UUID. JSON responses carry `Cache-Control: no-store`.
- Unknown paths under `/v1/` and `/auth/` answer a JSON `404 not_found`. Every other unknown path serves the embedded web UI, with a fallback to its `index.html`.

### Lists {#lists}

List endpoints take `?limit=` and `?cursor=` and return a page:

```json
{
  "items": [],
  "next_cursor": "c3RhY2tzL3Byb2QvdnBj",
  "total": 42
}
```

| Parameter | Default | Meaning |
| --- | --- | --- |
| `limit` | `50` | Items per page, a positive integer; values above `500` are treated as `500`. |
| `cursor` | first page | The `next_cursor` of the previous page, opaque. A malformed cursor is `400 invalid` with `details.field` `cursor`. |

`next_cursor` is absent on the last page. `total` counts every item across pages and is present only on `GET /v1/repos`, `GET /v1/repos/{owner}/{repo}/stacks` and, for API keys, `GET /v1/modules`, and only when it is not zero. Pages of `GET /v1/audit` and `GET /v1/modules` may hold fewer than `limit` items, or none, and still carry `next_cursor`, because entries the caller may not see are removed after paging.

## Authentication {#authentication}

| Caller | Credential | Sees |
| --- | --- | --- |
| Runner | `Authorization: Bearer <GitHub Actions OIDC token>` | Its own repository |
| Automation | `Authorization: Bearer sk_<key>` | Every repository; treated as an administrator |
| Person | The `stackorder_session` cookie, set by [GitHub sign-in](#sign-in) | Repositories whose installation account is one of the person's organisations, or their own login |

Runners never hold a shared secret: the token is the job's own OIDC token. A runner token is accepted only once, so the CLI requests a fresh one for every call. See [OIDC binding](#oidc-binding). API keys are created by an operator; see [API keys](/reference/server-configuration#api-keys).

Anything a caller may not see answers `404 not_found`, as if it did not exist. State-changing requests made with the session cookie (unlock, re-run, sign-out) must also come from the server's own origin: `Origin` must equal the origin of `STACKORDER_BASE_URL`, or, without `Origin`, `Sec-Fetch-Site` must be `same-origin`; otherwise they are `403 forbidden`. API keys are exempt. A `401` response carries `WWW-Authenticate: Bearer realm="stackorder"`.

## Errors {#errors}

Every error response has the same body. `details` is present only for the codes that define it.

```json
{
  "code": "locked",
  "message": "stacks/prod/vpc is locked by pull request 41",
  "details": {
    "conflicts": [
      {
        "stack_id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
        "stack_key": "stacks/prod/vpc",
        "run_id": "5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b",
        "pr_number": 41,
        "taken_at": "2026-09-27T16:02:11Z"
      }
    ]
  }
}
```

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid` | The body or a parameter failed validation. `details.field` and `details.reason` name the field when one is at fault. |
| 401 | `unauthorized` | Missing or unusable credentials: no token, a bad signature, the wrong audience or issuer, a token older than 10 minutes or already used, an unknown or revoked API key, an expired session. |
| 403 | `forbidden` | Valid credentials that may not do this: a repository the server does not know, a suspended installation, a `job_workflow_ref` that does not match, a claim that does not match the run, a person without push permission, a cross-origin request. |
| 404 | `not_found` | The run, stack, module, repository or route does not exist or is not visible to the caller. |
| 409 | `conflict` | The request conflicts with the current state, such as a result for a stack that already finished. |
| 409 | `refused` | The apply gate refused. `details.failures` lists each failure as `{"layer", "name", "reason", "stacks"}`. |
| 409 | `superseded` | A newer commit replaced the run. |
| 413 | `invalid` | The body is larger than the route allows: 8 MB for a graph upload, 1 MB for every other `POST` under `/v1` and `/auth`. |
| 423 | `locked` | A stack is locked by another pull request or run. `details.conflicts` lists the locks. |
| 500 | `internal` | An unexpected server error; the message is generic and the details are in the server log. |
| 503 | `unavailable` | GitHub's signing keys, GitHub itself or the database cannot be reached, or the server is in [setup mode](/reference/server-configuration#setup-mode). |

The CLI maps these to [exit codes](/reference/exit-codes#server-answers).

## Endpoints {#endpoints}

| Method and path | Caller | Request | Response |
| --- | --- | --- | --- |
| [`POST /v1/runs`](#create-run) | Runner; API key for manual runs | `CreateRunRequest` | `201` or `200`, `CreateRunResponse` |
| [`POST /v1/runs/{id}/graph`](#upload-graph) | Runner | `GraphUploadRequest` | `ResolveResponse` |
| [`POST /v1/runs/{id}/stacks/{key}/result`](#stack-result) | Runner; API key for manual runs | `StackResult` | `RunStack` |
| [`POST /v1/runs/{id}/stacks/{key}/checks/{name}`](#check-verdict) | Runner; API key for manual runs | `CheckVerdict` | `Check` |
| [`GET /v1/runs/{id}`](#get-run) | Runner, person, API key | | `Run` |
| [`GET /v1/runs/{id}/stacks/{key}/plan`](#plan-text) | Person, API key | | `text/plain`, the full plan text |
| [`POST /v1/runs/{id}/rerun`](#rerun) | Person with push permission, API key | | `201` or `200`, `CreateRunResponse` |
| [`POST /v1/unlock`](#unlock-by-key) | Person with push permission, API key | `UnlockRequest` | `UnlockResponse` |
| [`POST /v1/stacks/{id}/unlock`](#unlock-by-id) | Person with push permission, API key | `UnlockRequest`, optional | `UnlockResponse` |
| [`GET /v1/me`](#me) | Person, API key | | `Whoami` |
| [`GET /v1/overview`](#overview) | Person, API key | | `Overview` |
| [`GET /v1/repos`](#repos) | Person, API key | | `Page` of `RepoSummary` |
| [`GET /v1/repos/{owner}/{repo}/graph`](#repo-graph) | Person, API key | `?ref=&run=` | `GraphView` |
| [`GET /v1/repos/{owner}/{repo}/runs`](#repo-runs) | Person, API key | `?status=&pr=&mode=` | `Page` of `Run` |
| [`GET /v1/repos/{owner}/{repo}/stacks`](#repo-stacks) | Person, API key | | `Page` of `StackDetail` |
| [`GET /v1/stacks/{id}`](#stack) | Person, API key | | `StackDetail` |
| [`GET /v1/stacks/{id}/runs`](#stack-runs) | Person, API key | | `Page` of `RunStackRef` |
| [`GET /v1/modules`](#modules) | Person, API key | `?q=` | `Page` of `ModuleDetail` |
| [`GET /v1/modules/{id}`](#module) | Person, API key | | `ModuleDetail` |
| [`GET /v1/audit`](#audit) | Person, API key | | `Page` of `AuditEntry` |
| [`GET /auth/login`](#sign-in) | Browser | `?next=` | `302` to GitHub |
| [`GET /auth/callback`](#sign-in) | Browser, from GitHub | | `302` to `next`, sets the session cookie |
| [`POST /auth/logout`](#sign-in) | Person | | `204` |
| [`GET /setup`](#setup) | Browser | `?org=&name=&force=1` | HTML, the App manifest form |
| [`GET /setup/callback`](#setup) | Browser, from GitHub | | HTML, the App credentials, once |
| [`GET /setup/installed`](#setup) | Browser, from GitHub | | HTML |
| [`POST /webhooks/github`](#webhooks) | GitHub | Webhook payload | `202` |
| [`GET /healthz`](#health) | Load balancer | | `200` |
| [`GET /readyz`](#health) | Load balancer | | `200` or `503` |
| [`GET /metrics`](#health) | Prometheus | | Prometheus text format |

## Runner endpoints {#runner}

### `POST /v1/runs` {#create-run}

Finds or creates the run a job belongs to, and answers `201` for a new run or `200` for an existing one. What it does depends on the credential:

- **A `pull_request` token** registers the plan run of the pull request's head commit. `mode` must be `plan` (or empty), `repo` must be the token's repository, `pr_number` must match `refs/pull/<n>/merge` in the token's `ref`, and `sha` must be the pull request's current head, which the server reads from GitHub; an older head is `409 superseded`. A new run supersedes the plan runs of older heads of the same pull request.
- **A `workflow_dispatch` token** registers a job the server dispatched. `run_id` is required and names the existing run; the job is [bound](#oidc-binding) to its dispatch.
- **An API key** starts a manual apply run, as `stackorder apply --local` does. `trigger` must be `manual`, `mode` `apply`, `sha` set and `stacks` non-empty; the server takes the stacks' locks before it answers (`423 locked` when one is held) and releases them when the results arrive.

A pull request plan job:

```json
{
  "repo": "acme/infra",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "base_sha": "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
  "pr_number": 42,
  "mode": "plan",
  "trigger": "pull_request",
  "workflow_run_id": 12345678901,
  "workflow_run_attempt": 1
}
```

A dispatched job:

```json
{
  "repo": "acme/infra",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "mode": "apply",
  "run_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "workflow_run_id": 12345679999,
  "workflow_run_attempt": 1
}
```

A manual run:

```json
{
  "repo": "acme/infra",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "mode": "apply",
  "trigger": "manual",
  "stacks": ["stacks/prod/vpc"]
}
```

The response always embeds the run:

```json
{
  "run_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "status": "pending",
  "existing": false,
  "run": {
    "id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
    "repo": "acme/infra",
    "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
    "base_sha": "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
    "pr_number": 42,
    "trigger": "pull_request",
    "mode": "plan",
    "status": "pending",
    "requested_by": "octocat",
    "created_at": "2026-09-28T09:13:58Z",
    "waves": 0,
    "current_wave": 0,
    "html_url": "https://stackorder.example.com/runs/7c9e6679-7425-40de-944b-e07fc1f90ae7"
  }
}
```

`mode` is `plan`, `apply` or `drift`. `trigger` is `pull_request`, `comment`, `push`, `schedule`, `rerequest` or `manual`.

### `POST /v1/runs/{id}/graph` {#upload-graph}

Uploads the scanned graph and the changed paths, and answers with the affected stacks, their waves, warnings and the matrix. Only the resolve job of the pull request that created the run may upload, and only while the run is `pending` or `planning`; `graph.repo` and `graph.sha` must match the run. A `tree_hash` equal to that of a stored graph is a cache hit, reported as `cached: true`. The body limit is 8 MB.

```json
{
  "graph": {
    "repo": "acme/infra",
    "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
    "tree_hash": "5d41402abc4b2a76b9719d911017c5925d41402abc4b2a76b9719d911017c592",
    "stacks": [
      {
        "key": "stacks/prod/vpc",
        "path": "stacks/prod/vpc",
        "backend": {
          "type": "s3",
          "bucket": "acme-terraform-state",
          "key": "stacks/prod/vpc/terraform.tfstate",
          "region": "us-east-1",
          "use_lockfile": true
        },
        "environment": "production",
        "tool": "tofu",
        "tool_version": "1.10.0",
        "plan_output": "full"
      },
      {
        "key": "stacks/prod/apps",
        "path": "stacks/prod/apps",
        "backend": {
          "type": "s3",
          "bucket": "acme-terraform-state",
          "key": "stacks/prod/apps/terraform.tfstate",
          "region": "us-east-1",
          "use_lockfile": true
        },
        "environment": "production",
        "tool": "tofu",
        "tool_version": "1.10.0",
        "plan_output": "full",
        "config": { "depends_on": ["stacks/prod/vpc"] }
      }
    ],
    "modules": [
      {
        "key": "acme/infra//modules/vpc",
        "kind": "local",
        "path": "modules/vpc",
        "source": "../../../modules/vpc"
      }
    ],
    "edges": [
      {
        "from": { "kind": "stack", "key": "stacks/prod/vpc" },
        "to": { "kind": "module", "key": "acme/infra//modules/vpc" },
        "type": "uses_module",
        "meta": { "ref": "", "source": "../../../modules/vpc" }
      },
      {
        "from": { "kind": "stack", "key": "stacks/prod/apps" },
        "to": { "kind": "stack", "key": "stacks/prod/vpc" },
        "type": "depends_on"
      }
    ]
  },
  "changed_paths": ["modules/vpc/main.tf"],
  "base_sha": "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
  "config": { "version": 1, "tool": "tofu" }
}
```

The request may also carry a `stacks` list that restricts the run, as a `stackorder plan stacks/a stacks/b` comment does. Edges point from the dependent to what it depends on. An inferred edge has `inferred: true`; `meta` carries `ref` and `source` on `uses_module` edges, and the matched `bucket` and `key` on `reads_state` edges. A cross-repository `depends_on` target appears as a stack with `external: true`, `repo` set and `key` qualified as `owner/repo//key`. A stack that is an [instance](/configuration/instances) of its directory has the key `path:instance` and carries `instance`; `workspace` is set only when the stack selects a Terraform workspace; and `watch_paths` lists the repository-relative files outside the stack directory that it reads at `init` or `plan`, its backend configuration files and var files. `backend` is the effective backend, after the stack's `backend_config`. A node from a CLI older than instances has `workspace` and no `instance`; the server takes the workspace as the instance.

The uploaded `config`, the pull request's own `stackorder.yaml` after defaults and validation, decides the affected set of the plan, but cannot shrink it: when it differs from the default branch's configuration, the graph is resolved under both and the affected sets are united. Stacks of the default-branch graph (before the first merge, the latest graph) that only the default branch's configuration discovers are added to the commit's graph when it finds them affected, and `warnings` names the keys that differ and the stacks planned because of them. Such a stack whose directory the pull request deletes is not added; `warnings` names it as removed. Policy does not come from it either: a default-branch `plan_output: summary` always wins over it, and the apply gate, `allowed_teams` and the environment an apply runs under are read from the default branch when the apply starts.

```json
{
  "run_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "affected": [
    {
      "key": "stacks/prod/vpc",
      "path": "stacks/prod/vpc",
      "wave": 0,
      "reasons": ["module"],
      "environment": "production",
      "tool": "tofu",
      "tool_version": "1.10.0",
      "plan_output": "full",
      "via": ["acme/infra//modules/vpc"]
    },
    {
      "key": "stacks/prod/apps",
      "path": "stacks/prod/apps",
      "wave": 1,
      "reasons": ["dependent"],
      "environment": "production",
      "tool": "tofu",
      "tool_version": "1.10.0",
      "plan_output": "full",
      "via": ["stacks/prod/vpc"]
    }
  ],
  "waves": [["stacks/prod/vpc"], ["stacks/prod/apps"]],
  "matrix": {
    "include": [
      {
        "stack": "stacks/prod/vpc",
        "key": "stacks/prod/vpc",
        "workspace": "",
        "environment": "production",
        "wave": 0,
        "tool": "tofu",
        "tool_version": "1.10.0",
        "plan_output": "full",
        "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c"
      },
      {
        "stack": "stacks/prod/apps",
        "key": "stacks/prod/apps",
        "workspace": "",
        "environment": "production",
        "wave": 1,
        "tool": "tofu",
        "tool_version": "1.10.0",
        "plan_output": "full",
        "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c"
      }
    ]
  },
  "cached": false
}
```

Other fields: `warnings`; `cycles`, set when resolution failed on a dependency cycle, each entry spelling one cycle (the response is still `200`, and the run fails); `external`, the cross-repository stacks with an ordering edge to a scheduled stack; `unconfirmed`, set only when the CLI produced the response locally; and `locked_by` on an affected stack another pull request holds a lock on. `reasons` are `changed`, `watch_path` (a file outside the stack directory that the stack reads changed), `module`, `reads_state`, `dependent` and `requested`, in that order. Affected stacks and matrix entries carry `instance` next to `workspace`.

### `POST /v1/runs/{id}/stacks/{key}/result` {#stack-result}

Reports a plan, apply or drift outcome for one stack and answers with the stack's updated row. `mode` must match the run's mode. Posting the same result twice changes nothing. A result for a stack that already finished is `409 conflict`, and one for a superseded run is `409 superseded`. For a `pull_request` token a plan run is final once it is `planned`, `failed` or otherwise finished: any result other than a repeat of the recorded one is `409 conflict`, so a re-run of only the failed jobs of the plan workflow cannot report to it; re-run all jobs, push or comment `stackorder plan` for a new plan run. An API key may report only results of manual runs.

```json
{
  "mode": "plan",
  "status": "success",
  "exit_code": 2,
  "has_changes": true,
  "summary": {
    "adds": 1,
    "changes": 2,
    "destroys": 0,
    "replaces": 0,
    "added": ["aws_route_table.private[2]"],
    "changed": ["aws_subnet.private[0]", "aws_subnet.private[1]"]
  },
  "plan_text": "OpenTofu will perform the following actions: ...",
  "plan_artifact": "stackorder-plan-stacks-prod-vpc-69df0ef0-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "job_url": "https://github.com/acme/infra/actions/runs/12345678901",
  "tool": "tofu",
  "tool_version": "1.10.0",
  "duration_ms": 48210,
  "backend": {
    "type": "s3",
    "bucket": "acme-terraform-state",
    "key": "stacks/prod/vpc/terraform.tfstate",
    "region": "us-east-1",
    "use_lockfile": true
  }
}
```

`status` is `success`, `failure` (the tool or a hook failed, or the CLI refused) or `error`. `exit_code` is the tool's exit code, `2` for a plan with changes. Other fields: `truncated` when the plan text was cut at 256 KB, `error_text`, and `unconfirmed`, which records the stack as `unconfirmed`. The summary can also count `imports`, `moves` and `output_changes`, and list `destroyed` and `replaced` addresses. Plan text of a stack whose default-branch `plan_output` is `summary` is never stored.

```json
{
  "stack_id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
  "key": "stacks/prod/vpc",
  "path": "stacks/prod/vpc",
  "environment": "production",
  "wave": 0,
  "status": "planned",
  "reasons": ["module"],
  "summary": {
    "adds": 1,
    "changes": 2,
    "destroys": 0,
    "replaces": 0,
    "added": ["aws_route_table.private[2]"],
    "changed": ["aws_subnet.private[0]", "aws_subnet.private[1]"]
  },
  "exit_code": 2,
  "job_url": "https://github.com/acme/infra/actions/runs/12345678901",
  "plan_artifact": "stackorder-plan-stacks-prod-vpc-69df0ef0-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "plan_text": "OpenTofu will perform the following actions: ...",
  "plan_output": "full",
  "started_at": "2026-09-28T09:14:03Z",
  "finished_at": "2026-09-28T09:14:51Z"
}
```

A row can also carry `instance`, `workspace`, `truncated`, `checks` (the stack's named checks), `blocked_by` (the failed predecessors of a `blocked` stack), `lock` (the orchestration lock on the stack now, whichever run holds it; `stackorder apply` applies only while it names its own run) and `plan_url` (the full plan text in the [artifact bucket](/reference/server-configuration#artifact-bucket), when `plan_text` holds only its beginning).

### `POST /v1/runs/{id}/stacks/{key}/checks/{name}` {#check-verdict}

Records a named policy or cost check verdict from any tool the workflow runs, and answers with the stored check. The same caller rules as for results apply. The server shows the verdict as the check run `stackorder/<name>: <key>` and the apply gate honours it. `name` is letters, digits, `-`, `_` and `.`, up to 64 characters; `resolve`, `plan` and `apply` are reserved. Posting again replaces the verdict until, for a `pull_request` token, the plan run is `planned` or finished; after that only a repeat of the recorded verdict is answered and anything else is `409 conflict`. Post verdicts before the stack's result, as a [`post-plan.sh` hook](/configuration/workflows#named-checks) does.

```json
{
  "status": "fail",
  "summary": "2 policy violations",
  "details": "deny: aws_s3_bucket.logs must block public access",
  "details_url": "https://github.com/acme/infra/actions/runs/12345678901"
}
```

```json
{
  "name": "policy",
  "status": "fail",
  "summary": "2 policy violations",
  "details_url": "https://github.com/acme/infra/actions/runs/12345678901",
  "updated_at": "2026-09-28T09:15:10Z"
}
```

`status` is `pass`, `fail` or `warn`. In the gate `warn` passes and `fail` refuses; on GitHub `warn` is a neutral check.

### `GET /v1/runs/{id}` {#get-run}

The run with its per-stack rows. A `pull_request` token may read only its own pull request's plan run, and only until it is `planned` or finished (`409 conflict`, or `409 superseded`); a `workflow_dispatch` token is bound to its dispatch on first contact, as for `POST /v1/runs`.

```json
{
  "id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "repo": "acme/infra",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "base_sha": "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
  "pr_number": 42,
  "trigger": "comment",
  "mode": "apply",
  "status": "applying",
  "requested_by": "octocat",
  "created_at": "2026-09-28T09:20:00Z",
  "started_at": "2026-09-28T09:20:04Z",
  "waves": 2,
  "current_wave": 0,
  "stacks": [
    {
      "stack_id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
      "key": "stacks/prod/vpc",
      "path": "stacks/prod/vpc",
      "environment": "production",
      "wave": 0,
      "status": "applying",
      "reasons": ["module"],
      "plan_artifact": "stackorder-plan-stacks-prod-vpc-69df0ef0-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
      "plan_output": "full"
    },
    {
      "stack_id": "8a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d",
      "key": "stacks/prod/apps",
      "path": "stacks/prod/apps",
      "environment": "production",
      "wave": 1,
      "status": "planned",
      "reasons": ["dependent"],
      "plan_artifact": "stackorder-plan-stacks-prod-apps-4b32b4c4-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
      "plan_output": "full"
    }
  ],
  "html_url": "https://stackorder.example.com/runs/7c9e6679-7425-40de-944b-e07fc1f90ae7"
}
```

`html_url` links to the run's page in the web UI; `warnings` lists anything the server wants people to see, such as a dispatch GitHub refused. Run `status` is `pending`, `planning`, `planned`, `applying`, `applied`, `failed`, `unconfirmed` or `superseded`. Stack `status` adds `blocked`, `noop`, `unknown` and `skipped`. See [Run states](/guide/how-it-works#run-states).

## Human and automation endpoints {#human}

### `GET /v1/runs/{id}/stacks/{key}/plan` {#plan-text}

The full plan text of a stack of a run, as `text/plain; charset=utf-8`, read from the [artifact bucket](/reference/server-configuration#artifact-bucket). Use it when the run's stack row has `truncated` and `plan_url` set: its `plan_text` then holds only the first 8 KB, and this endpoint returns everything the CLI sent, up to 256 KB. `{key}` is percent-encoded like the runner endpoints' keys. A person sees the runs of the repositories they see; API keys see every run.

`404` with code `not_found` when the run is unknown or hidden, the stack is not part of the run, the row has no `plan_url` (its `plan_text` is all the server keeps, as for every stack without a bucket and for `plan_output: summary` stacks), the server has no artifact bucket, or the object has expired from the bucket. Runner tokens are refused with `401`.

### `POST /v1/runs/{id}/rerun` {#rerun}

Plans every stack of a plan run again, in a new run on the same commit with trigger `rerequest`, dispatched to `stackorder-run.yml` with `mode: plan`. The body is empty. A person needs push permission on the repository. Only plan runs can be re-run; a superseded run is `409 superseded`. Audited as `rerun`. The response is a `CreateRunResponse` for the new run, `201`:

```json
{
  "run_id": "0d9c8b7a-6f5e-4d3c-8b2a-1f0e9d8c7b6a",
  "status": "planning",
  "existing": false,
  "run": {
    "id": "0d9c8b7a-6f5e-4d3c-8b2a-1f0e9d8c7b6a",
    "repo": "acme/infra",
    "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
    "pr_number": 42,
    "trigger": "rerequest",
    "mode": "plan",
    "status": "planning",
    "requested_by": "octocat",
    "created_at": "2026-09-28T10:02:00Z",
    "waves": 1,
    "current_wave": 0,
    "html_url": "https://stackorder.example.com/runs/0d9c8b7a-6f5e-4d3c-8b2a-1f0e9d8c7b6a"
  }
}
```

### `POST /v1/unlock` {#unlock-by-key}

Releases a stack's orchestration lock, whoever holds it, addressed by repository and key. This is what `stackorder unlock` calls. `repo` and `stack_key` are required. A person needs push permission on the repository. Audited as `unlock`; the pull request that held the lock gets a comment.

```json
{
  "repo": "acme/infra",
  "stack_key": "stacks/prod/vpc",
  "reason": "PR 41 closed; reverted in PR 43",
  "force_state": false
}
```

```json
{
  "released": [
    {
      "stack_id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
      "stack_key": "stacks/prod/vpc",
      "run_id": "5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b",
      "pr_number": 41,
      "taken_at": "2026-09-27T16:02:11Z",
      "reason": "apply of #41"
    }
  ]
}
```

`released` is empty when the stack held no lock. `force_state: true` releases the lock the same way; it marks the release as following a runner that died mid-apply, and the comment asks for the S3 state lock to be checked. The server never touches the state lock.

### `POST /v1/stacks/{id}/unlock` {#unlock-by-id}

The same release, addressed by stack id, as the UI does it. The body is optional and carries `reason` and `force_state`:

```json
{ "reason": "runner lost during apply", "force_state": true }
```

The response is an `UnlockResponse`, as above.

### `GET /v1/me` {#me}

For a person:

```json
{
  "login": "octocat",
  "avatar_url": "https://avatars.githubusercontent.com/u/583231",
  "orgs": ["acme"]
}
```

For an API key, `login` is `apikey:<key name>` and `admin` is `true`:

```json
{ "login": "apikey:ci", "orgs": [], "admin": true }
```

### `GET /v1/overview` {#overview}

Counts over the repositories the caller sees.

```json
{
  "repos": 3,
  "stacks": 57,
  "drifted": 2,
  "locks_held": 4,
  "runs_by_status": { "applied": 120, "failed": 3, "planned": 14, "superseded": 31 },
  "stacks_by_status": { "applied": 48, "planned": 6, "failed": 1, "blocked": 2 },
  "recent_runs": [
    {
      "id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "repo": "acme/infra",
      "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
      "pr_number": 42,
      "trigger": "comment",
      "mode": "apply",
      "status": "applied",
      "requested_by": "octocat",
      "created_at": "2026-09-28T09:20:00Z",
      "finished_at": "2026-09-28T09:22:40Z",
      "waves": 2,
      "current_wave": 1,
      "html_url": "https://stackorder.example.com/runs/7c9e6679-7425-40de-944b-e07fc1f90ae7"
    }
  ]
}
```

`runs_by_status` and `stacks_by_status` are always present, `{}` when empty.

### `GET /v1/repos` {#repos}

A page of repositories, ordered by full name.

```json
{
  "items": [
    {
      "id": 123456789,
      "full_name": "acme/infra",
      "default_branch": "main",
      "stacks": 42,
      "drifted": 1,
      "locks_held": 2,
      "last_run_at": "2026-09-28T09:20:00Z"
    }
  ],
  "total": 1
}
```

### `GET /v1/repos/{owner}/{repo}/graph` {#repo-graph}

The repository's graph as JSON, for tooling, with the server's stack ids.

| Parameter | Meaning |
| --- | --- |
| `ref` | The commit: a full commit SHA the server has a graph for, a prefix of at least 7 characters that only one recorded graph starts with, or `default` for the default-branch graph (`404` before the first merge). A prefix several graphs share, or anything else, is refused with `invalid` (`details.field` is `ref`); branch names are not supported, because the server records graphs by commit only. Without it: the default-branch graph (the latest graph of a merged pull request) when known, else the latest graph. |
| `run` | A run of this repository. The graph is then that run's graph, unless `ref` is given, and the response adds the run's `affected` stacks and `waves`, to replay its resolution. |

```json
{
  "repo": "acme/infra",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "graph": {
    "repo": "acme/infra",
    "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
    "stacks": [{ "key": "stacks/prod/vpc", "path": "stacks/prod/vpc", "environment": "production" }],
    "modules": [{ "key": "acme/infra//modules/vpc", "kind": "local", "path": "modules/vpc", "source": "../../../modules/vpc" }],
    "edges": [
      {
        "from": { "kind": "stack", "key": "stacks/prod/vpc" },
        "to": { "kind": "module", "key": "acme/infra//modules/vpc" },
        "type": "uses_module",
        "meta": { "ref": "", "source": "../../../modules/vpc" }
      }
    ]
  },
  "stack_ids": { "stacks/prod/vpc": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f" }
}
```

A repository with no recorded graph, or a `ref` without one, is `404 not_found`.

### `GET /v1/repos/{owner}/{repo}/runs` {#repo-runs}

A page of the repository's runs, newest first, in the `Run` shape without `stacks`. `status` (a run status), `pr` (a positive number) and `mode` (`plan`, `apply` or `drift`) filter the list; an unknown value is `400 invalid`.

```json
{
  "items": [
    {
      "id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "repo": "acme/infra",
      "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
      "pr_number": 42,
      "trigger": "comment",
      "mode": "apply",
      "status": "applied",
      "requested_by": "octocat",
      "created_at": "2026-09-28T09:20:00Z",
      "started_at": "2026-09-28T09:20:04Z",
      "finished_at": "2026-09-28T09:22:40Z",
      "waves": 2,
      "current_wave": 1,
      "html_url": "https://stackorder.example.com/runs/7c9e6679-7425-40de-944b-e07fc1f90ae7"
    }
  ],
  "next_cursor": "MjAyNi0wOS0yOFQwOToyMDowMFo"
}
```

### `GET /v1/repos/{owner}/{repo}/stacks` {#repo-stacks}

A page of the repository's current stacks, ordered by key, each in the [`StackDetail`](#stack) shape, with `total`.

```json
{
  "items": [
    {
      "id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
      "repo": "acme/infra",
      "key": "stacks/prod/vpc",
      "path": "stacks/prod/vpc",
      "environment": "production",
      "tool": "tofu",
      "dependents": ["stacks/prod/apps"]
    }
  ],
  "total": 1
}
```

### `GET /v1/stacks/{id}` {#stack}

Stack detail: last plan and apply, the latest drift result, the lock, dependencies both ways (over `depends_on` and `reads_state` in the default-branch graph) and pinned modules.

```json
{
  "id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
  "repo": "acme/infra",
  "key": "stacks/prod/vpc",
  "path": "stacks/prod/vpc",
  "environment": "production",
  "backend": {
    "type": "s3",
    "bucket": "acme-terraform-state",
    "key": "stacks/prod/vpc/terraform.tfstate",
    "region": "us-east-1",
    "use_lockfile": true
  },
  "tool": "tofu",
  "last_apply": {
    "run_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
    "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
    "pr_number": 42,
    "status": "applied",
    "summary": { "adds": 1, "changes": 2, "destroys": 0, "replaces": 0 },
    "finished_at": "2026-09-28T09:22:40Z",
    "job_url": "https://github.com/acme/infra/actions/runs/12345679999"
  },
  "drift": {
    "checked_at": "2026-09-28T06:07:12Z",
    "drifted": false
  },
  "dependents": ["stacks/prod/apps"],
  "modules": [
    { "module_key": "acme/modules//tags", "ref": "v1.2.0", "latest": "v1.4.1", "behind": 2 }
  ]
}
```

`last_plan` has the same shape as `last_apply`. A stack detail also carries `instance` and `workspace` when they are set. `lock` is a `LockInfo` while the stack is locked. A drifted stack's `drift` carries the plan `summary` and, with drift issues enabled, `issue_number` and `issue_url`.

### `GET /v1/stacks/{id}/runs` {#stack-runs}

A page of the stack's history, newest first, each item in the `RunStackRef` shape:

```json
{
  "items": [
    {
      "run_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
      "pr_number": 42,
      "status": "applied",
      "summary": { "adds": 1, "changes": 2, "destroys": 0, "replaces": 0 },
      "finished_at": "2026-09-28T09:22:40Z",
      "job_url": "https://github.com/acme/infra/actions/runs/12345679999"
    }
  ],
  "next_cursor": "MjAyNi0wOS0yOFQwOToyMDowMFo"
}
```

### `GET /v1/modules` {#modules}

A page of modules, ordered by key, each in the [`ModuleDetail`](#module) shape. A git or registry module is listed once, under its family `key` without `@ref`. `?q=` keeps the modules whose key contains the text, ignoring case. A person sees a module that belongs to one of their accounts or has a consumer they can see.

```json
{
  "items": [
    {
      "id": "0f1e2d3c-4b5a-4968-8776-655443322110",
      "key": "acme/modules//tags",
      "kind": "git",
      "source": "git::https://github.com/acme/modules.git//tags?ref=v1.4.1",
      "latest": "v1.4.1",
      "consumers": [
        { "stack_id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f", "repo": "acme/infra", "stack_key": "stacks/prod/vpc", "ref": "v1.2.0", "behind": 2 }
      ]
    }
  ],
  "total": 1
}
```

### `GET /v1/modules/{id}` {#module}

One module with its released versions and its consumers across repositories. `latest` is the newest stable version by semantic version precedence, whatever order the tags were pushed in, or the newest pre-release when no stable version is released. Each consumer carries the `ref` it pins and how many released versions it is `behind`. An id of a pinned module (`key@ref`) answers with its family.

```json
{
  "id": "0f1e2d3c-4b5a-4968-8776-655443322110",
  "key": "acme/modules//tags",
  "kind": "git",
  "source": "git::https://github.com/acme/modules.git//tags?ref=v1.4.1",
  "latest": "v1.4.1",
  "versions": [
    { "version": "v1.4.1", "sha": "c0ffee1234567890c0ffee1234567890c0ffee12", "tagged_at": "2026-09-20T12:00:00Z" },
    { "version": "v1.3.0", "sha": "decafbad1234567890decafbad1234567890deca", "tagged_at": "2026-08-02T08:30:00Z" }
  ],
  "consumers": [
    {
      "stack_id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
      "repo": "acme/infra",
      "stack_key": "stacks/prod/vpc",
      "ref": "v1.2.0",
      "behind": 2
    }
  ]
}
```

### `GET /v1/audit` {#audit}

A page of audited actions, newest first: unlocks (`unlock`), re-runs (`rerun`), manual applies (`manual_apply`), comment commands (`command`, `command_ignored`, `command_rate_limited`), lock warnings and reminders, cross-repository plans and invalid configuration notices.

```json
{
  "items": [
    {
      "at": "2026-09-28T09:31:00Z",
      "actor": "octocat",
      "action": "unlock",
      "target": "acme/infra//stacks/prod/vpc",
      "details": {
        "repo": "acme/infra",
        "stack": "stacks/prod/vpc",
        "stack_id": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
        "pr": 41,
        "run_id": "5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b",
        "reason": "PR 41 closed",
        "force_state": false
      }
    }
  ],
  "next_cursor": "MTIz"
}
```

Unlocks target the qualified stack key `owner/repo//key` and keep the stack's id in `details.stack_id`; re-runs and comment commands target `owner/repo` and keep the pull request in `details.pr`. Every one of them carries `details.repo`. A person sees the entries they made and those whose `details.repo`, or whose `target` (`owner/repo…` or `kind:owner/repo…`), names a repository they see.

## Sign-in {#sign-in}

`GET /auth/login` redirects to GitHub's authorization page for the App's OAuth client with the `read:org` scope, and remembers `next` (a path on this server, `/` by default) for 10 minutes in a signed cookie. `GET /auth/callback` exchanges the code, reads the user and their organisations, and issues a session only when the App is installed, and not suspended, on the user's account or on one of their organisations; otherwise it shows a page explaining that the App is not installed. The session lasts 7 days, is stored hashed in Postgres, records the user's organisations at sign-in (a membership change takes effect at the next sign-in), and is sent as the `stackorder_session` cookie: `HttpOnly`, `SameSite=Lax`, and `Secure` when the base URL is `https`. `POST /auth/logout` ends the session and answers `204`.

Without `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET`, `/auth/login` answers `503` with a page saying sign-in is not configured.

## Setup {#setup}

`GET /setup` renders a page that posts the GitHub App manifest (webhook URL, callback URLs, [permissions and events](/reference/github-app#permissions)) to GitHub's App creation page, on your personal account or, with `?org=<organisation>`, on an organisation. `?name=` chooses the App name, up to 34 characters; the default is `stackorder-<host>`. Outside setup mode the page explains that an App is already configured; `?force=1` creates another App only when [`STACKORDER_ALLOW_RESETUP`](/reference/server-configuration#setup-mode) is `true`, and otherwise `GET /setup?force=1` and `GET /setup/callback` answer `404`.

GitHub redirects back to `GET /setup/callback`, which converts the one-time code into the App's credentials and prints them once as environment variables: `GITHUB_APP_ID`, `GITHUB_WEBHOOK_SECRET`, `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` and the PEM for `GITHUB_APP_PRIVATE_KEY`, plus `GITHUB_API_URL` and `GITHUB_WEB_URL` on Enterprise Server, with a link to install the App and the next steps. The code works once and for an hour, and only from the browser that started the flow. GitHub sends the browser to `GET /setup/installed` after an installation.

In setup mode, when the App variables are not set, the server serves only these three pages, `/healthz` and `/readyz`, and answers `503` with code `unavailable` everywhere else:

```json
{
  "code": "unavailable",
  "message": "setup is required: the GitHub App is not configured yet; open https://stackorder.example.com/setup"
}
```

## Webhooks {#webhooks}

`POST /webhooks/github` receives every App event. It verifies the `X-Hub-Signature-256` HMAC against `GITHUB_WEBHOOK_SECRET`, writes the delivery to the `events` queue keyed by its `X-GitHub-Delivery` id, and answers `202` before any work is done. A redelivery of a stored id is acknowledged with `"queued": false` and not processed again.

```json
{ "queued": true }
```

| Answer | When |
| --- | --- |
| `202 {"queued": true}` | A new delivery |
| `202 {"queued": false}` | A delivery id already stored |
| `200 {"ok": true}` | A `ping` |
| `401 unauthorized` | Missing or wrong signature |
| `400 invalid` | No event or delivery header, or a body that is not JSON |
| `413 invalid` | A body over 5 MB |

This route has no API body limit; the webhook receiver bounds it. See [GitHub App](/reference/github-app#events) for the events.

## Health and metrics {#health}

| Endpoint | Answer |
| --- | --- |
| `GET /healthz` | Always `200` while the process serves |
| `GET /readyz` | `200` when the database answers within 2 s, in setup mode too; `503 unavailable` otherwise |
| `GET /metrics` | Prometheus metrics; with `STACKORDER_METRICS_TOKEN` set, `401` without `Authorization: Bearer <token>`. See [Metrics and tracing](/reference/metrics). |

```json
{ "status": "ok", "version": "0.1.0" }
```

In setup mode both health endpoints add `"setup_mode": true`.

## OIDC binding {#oidc-binding}

A runner authenticates with the job's GitHub Actions OIDC token, requested for the audience `STACKORDER_OIDC_AUDIENCE`. Before any handler runs, the server checks the token itself:

| Claim | Check | Failure |
| --- | --- | --- |
| signature | RS256 against the issuer's JWKS, cached for an hour and refetched on an unknown `kid` | `401`; `503` when the keys cannot be fetched |
| `iss` | `https://token.actions.githubusercontent.com`, or `GITHUB_OIDC_ISSUER` | `401` |
| `aud` | Contains `STACKORDER_OIDC_AUDIENCE`, which defaults to the base URL | `401` |
| `iat`, `exp` | Issued within the last 10 minutes, not expired (60 s clock skew allowed) | `401` |
| `jti` | Never seen before; every accepted `jti` is recorded until the token expires | `401` |
| `repository`, `repository_id` | A repository the server knows, with the same GitHub id, whose installation is not suspended | `403` |
| `job_workflow_ref` | Matches `STACKORDER_REQUIRED_WORKFLOW_REF`, when set | `403` |

Then the run service binds the token to the run, according to how the job started:

| Job | Claims | Binding |
| --- | --- | --- |
| Pull request plan job | `event_name` is `pull_request`, `ref` is `refs/pull/<n>/merge` | `<n>` is the run's pull request, the run is a plan run started by a pull request, and, when the run is created, the pull request's head SHA read from GitHub equals the SHA the CLI sent. Such a token can reach only plan runs registered by a pull request resolve job. |
| Server-dispatched job | `event_name` is `workflow_dispatch`, `ref` is `refs/heads/<default branch>` | `run_id` is the workflow run of the dispatch that carries the stack, bound on first contact; `run_attempt` is recorded; `environment` equals the stack's environment for an apply, and `default` for a plan or drift dispatch. A workflow run whose dispatch is already bound to another workflow run is refused. |

A failed binding is `403 forbidden`. `actor` is recorded as `requested_by`. Results for a finished stack are `409 conflict`, and for a superseded run `409 superseded`. A `pull_request` token's plan run is final once it is `planned` or finished: its results, verdicts and reads are `409 conflict`, except a repeat of the recorded result or verdict.

**Why `sha` is never compared.** GitHub's `sha` claim is the commit that triggered the workflow, not the commit being planned. For a `pull_request` event it is the merge commit of `refs/pull/<n>/merge`, which GitHub creates and changes whenever the base branch moves; for `workflow_dispatch` it is the head of the default branch the workflow was dispatched on, while the job checks out the `sha` input. Neither equals the run's SHA, the pull request head, so comparing them would refuse every legitimate job. The server binds to the commit through GitHub instead: it reads the pull request's current head when a plan run is registered, supersedes runs whose head moved, and dispatches applies only for the head that was planned, or, in `on_merge` mode, for the merge commit of that head. The CLI, in turn, refuses to apply unless the run's SHA equals the dispatch `sha` input and the checkout's `HEAD`.

`pull_request_target` plans are not supported. A fork pull request gets a neutral check from the server and no plans; see [Fork pull requests](/configuration/workflows#forks).
