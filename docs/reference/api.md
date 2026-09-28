# API

The server exposes one JSON API for three callers: runners, people and automation. All request and response bodies are the types in the Go package `github.com/stackorder/stackorder/api/v1`, which automation can import to decode responses.

Fields are only ever added within `v1`. Clients must ignore fields they do not know.

## Conventions {#conventions}

- Bodies are JSON. Times are RFC 3339 in UTC, such as `2026-09-28T09:14:03Z`.
- Run ids and stack ids are UUIDs. Repository ids are GitHub's numeric ids.
- Stack keys in paths are percent-encoded, so slashes become `%2F`: `POST /v1/runs/{id}/stacks/stacks%2Fprod%2Fvpc/result`.
- List endpoints return a page: `{"items": [...], "next_cursor": "...", "total": 3}`. `next_cursor` and `total` are omitted when empty.

## Authentication {#authentication}

| Caller | Credential |
| --- | --- |
| Runner | `Authorization: Bearer <GitHub OIDC token>` |
| Automation | `Authorization: Bearer sk_<key>` |
| Person | The `stackorder_session` cookie, set by GitHub sign-in |

Runners never hold a shared secret. The token is the job's own GitHub Actions OIDC token, requested with the server's base URL as audience. See [OIDC binding](#oidc-binding).

## Errors {#errors}

Every non-2xx response has this body:

```json
{
  "code": "locked",
  "message": "stacks/prod/vpc is locked by pull request 41",
  "details": null
}
```

| Code | Meaning |
| --- | --- |
| `unauthorized` | Missing or invalid credentials: a bad signature, an expired or replayed token, the wrong audience. |
| `forbidden` | Valid credentials that may not do this: a claim that does not match the run, or no write permission. |
| `not_found` | The run, stack, module or repository does not exist or is not visible to the caller. |
| `conflict` | The request conflicts with the current state, such as a result for a run that has already finished. |
| `invalid` | The body or parameters failed validation. |
| `refused` | The apply gate refused the request; `details.failures` lists each failing layer with its reason. |
| `locked` | A stack is locked by another pull request or run; `details.conflicts` lists the locks. |
| `superseded` | A newer commit replaced the run. |
| `unconfirmed` | The request depends on a result the server never confirmed. |
| `internal` | A server error. |
| `unavailable` | GitHub's signing keys, GitHub itself or the database cannot be reached, or the server is in setup mode. |

`invalid` carries `details.field` and `details.reason` when a field is at fault. A body larger than the route allows, 8 MB for a graph upload and 1 MB otherwise, is refused with status 413 and code `invalid`.

## Endpoints {#endpoints}

| Method and path | Caller | Request | Response |
| --- | --- | --- | --- |
| [`POST /v1/runs`](#create-run) | Runner; automation for manual runs | `CreateRunRequest` | `CreateRunResponse` |
| [`POST /v1/runs/{id}/graph`](#upload-graph) | Runner | `GraphUploadRequest` | `ResolveResponse` |
| [`POST /v1/runs/{id}/stacks/{key}/result`](#stack-result) | Runner | `StackResult` | `RunStack` |
| [`POST /v1/runs/{id}/stacks/{key}/checks/{name}`](#check-verdict) | Runner | `CheckVerdict` | `Check` |
| [`GET /v1/runs/{id}`](#get-run) | Runner, person, automation | | `Run` |
| [`POST /v1/runs/{id}/rerun`](#rerun) | Person with write permission, automation | | `CreateRunResponse` |
| [`POST /v1/unlock`](#unlock-by-key) | Person with write permission, automation | `UnlockRequest` | `UnlockResponse` |
| [`POST /v1/stacks/{id}/unlock`](#unlock-by-id) | Person with write permission, automation | `UnlockRequest` | `UnlockResponse` |
| [`GET /v1/me`](#me) | Person, automation | | `Whoami` |
| [`GET /v1/overview`](#overview) | Person, automation | | `Overview` |
| [`GET /v1/repos`](#repos) | Person, automation | | `Page` of `RepoSummary` |
| [`GET /v1/repos/{owner}/{repo}/graph`](#repo-graph) | Person, automation | `?ref=&run=` | `GraphView` |
| [`GET /v1/repos/{owner}/{repo}/runs`](#repo-runs) | Person, automation | `?status=&pr=&mode=` | `Page` of `Run` |
| [`GET /v1/repos/{owner}/{repo}/stacks`](#repo-stacks) | Person, automation | | `Page` of `StackDetail` |
| [`GET /v1/stacks/{id}`](#stack) | Person, automation | | `StackDetail` |
| [`GET /v1/stacks/{id}/runs`](#stack-runs) | Person, automation | | `Page` of `RunStackRef` |
| [`GET /v1/modules`](#modules) | Person, automation | | `Page` of `ModuleDetail` |
| [`GET /v1/modules/{id}`](#modules) | Person, automation | | `ModuleDetail` |
| [`GET /v1/audit`](#audit) | Person, automation | | `Page` of `AuditEntry` |
| [`GET /auth/login`](#sign-in) | Browser | | Redirect to GitHub |
| [`GET /auth/callback`](#sign-in) | Browser | | Sets the session cookie |
| [`POST /auth/logout`](#sign-in) | Person | | Clears the session |
| [`GET /setup`](#setup) | Browser | | The App manifest page |
| [`GET /setup/callback`](#setup) | Browser, from GitHub | | The App credentials, once |
| [`GET /setup/installed`](#setup) | Browser, from GitHub | | Confirms the installation |
| [`POST /webhooks/github`](#webhooks) | GitHub | Webhook payload | `202 Accepted` |
| [`GET /healthz`](#health) | Load balancer | | Liveness |
| [`GET /readyz`](#health) | Load balancer | | Readiness: the database is reachable |
| [`GET /metrics`](#health) | Prometheus | | Metrics in the Prometheus text format |

Every other path serves the embedded web UI, with a fallback to its `index.html`.

## Runner endpoints {#runner}

### `POST /v1/runs` {#create-run}

Finds or creates the run for this repository, SHA and pull request, and returns its id. A job dispatched by the server sends the `run_id` it was given, to register itself on the existing run.

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

```json
{
  "run_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  "status": "pending",
  "existing": false
}
```

`mode` is `plan`, `apply` or `drift`. `trigger` is `pull_request`, `comment`, `push`, `schedule`, `rerequest` or `manual`. The response can also embed the full `run`.

**Manual runs.** With an API key, `trigger: manual`, `mode: apply` and `stacks`, the server takes the stacks' locks before answering and releases them when the results arrive. This is how `stackorder apply --local` works.

```json
{
  "repo": "acme/infra",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "mode": "apply",
  "trigger": "manual",
  "stacks": ["stacks/prod/vpc"]
}
```

### `POST /v1/runs/{id}/graph` {#upload-graph}

Uploads the scanned graph and the changed paths. Returns the affected stacks, their waves, warnings and the matrix. A `tree_hash` that matches a stored graph is a cache hit, reported as `cached: true`.

```json
{
  "graph": {
    "repo": "acme/infra",
    "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
    "tree_hash": "5d41402abc4b2a76b9719d911017c592",
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
        "type": "uses_module"
      },
      {
        "from": { "kind": "stack", "key": "stacks/prod/apps" },
        "to": { "kind": "stack", "key": "stacks/prod/vpc" },
        "type": "depends_on"
      }
    ]
  },
  "changed_paths": ["modules/vpc/main.tf"],
  "base_sha": "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
}
```

The request may also carry the parsed `config` and a `stacks` list that restricts the run, for a `stackorder plan stacks/a stacks/b` comment. Edges have `inferred: true` when inferred, and `meta` with the module `ref` or the matched bucket and key.

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

Other fields: `warnings`; `cycles`, non-empty when resolution failed, each entry spelling one cycle; `external`, the cross-repo dependents recorded on the run; `unconfirmed`, set when the CLI produced the response locally; and `locked_by` on an affected stack that another PR holds a lock on.

For applies, the matrix entries the server dispatches in `stacks` also carry `plan_run_id`, the Actions run that uploaded the plan, and `artifact`, the artifact's name.

### `POST /v1/runs/{id}/stacks/{key}/result` {#stack-result}

Reports a plan, apply or drift outcome for one stack. Returns the stack's updated row in the run.

```json
{
  "mode": "plan",
  "status": "success",
  "exit_code": 0,
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
  "plan_artifact": "stackorder-plan-stacks-prod-vpc-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "job_url": "https://github.com/acme/infra/actions/runs/12345678901/job/34567890123",
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

`status` is `success`, `failure` or `error`. Other fields: `truncated` when the plan text was cut at 256 KB, `error_text`, and `unconfirmed` when the CLI is reporting late after losing the server. The summary can also count `imports`, `moves` and `output_changes`, and list `destroyed` and `replaced` addresses.

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
  "exit_code": 0,
  "job_url": "https://github.com/acme/infra/actions/runs/12345678901/job/34567890123",
  "plan_artifact": "stackorder-plan-stacks-prod-vpc-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "started_at": "2026-09-28T09:14:03Z",
  "finished_at": "2026-09-28T09:14:51Z"
}
```

### `POST /v1/runs/{id}/stacks/{key}/checks/{name}` {#check-verdict}

Records a named policy or cost check verdict from any tool the workflow runs. The server shows it as the check run `stackorder/<name>: <key>` and the apply gate honours it.

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

`status` is `pass`, `fail` or `warn`. In the gate `warn` passes and `fail` refuses.

### `GET /v1/runs/{id}` {#get-run}

The run with its per-stack rows.

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
      "reasons": ["module"]
    },
    {
      "stack_id": "8a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d",
      "key": "stacks/prod/apps",
      "path": "stacks/prod/apps",
      "environment": "production",
      "wave": 1,
      "status": "pending",
      "reasons": ["dependent"]
    }
  ]
}
```

`html_url` links to the run's page in the web UI. Run `status` is `pending`, `planning`, `planned`, `applying`, `applied`, `failed`, `unconfirmed` or `superseded`. Stack `status` adds `blocked`, `noop`, `unknown` and `skipped`. See [Run states](/guide/how-it-works#run-states).

### `POST /v1/unlock` {#unlock-by-key}

Releases a stack's orchestration lock, addressed by repository and key. This is what `stackorder unlock` calls.

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
      "taken_at": "2026-09-27T16:02:11Z"
    }
  ]
}
```

`force_state: true` releases a stack left `unknown` after a runner died mid-apply. It does not touch the S3 state lock.

## Human and automation endpoints {#human}

### `POST /v1/runs/{id}/rerun` {#rerun}

Plans the stacks of a plan run again, in a new run, from the UI or automation, and answers `201` with a `CreateRunResponse` for the new run. A person needs write permission on the repository. Audited.

### `POST /v1/stacks/{id}/unlock` {#unlock-by-id}

Releases a stack's lock by stack id; the body carries `reason` and `force_state`. Requires write permission on the repository. Audited. The response is an `UnlockResponse`, as above.

### `GET /v1/me` {#me}

```json
{
  "login": "octocat",
  "avatar_url": "https://avatars.githubusercontent.com/u/583231",
  "orgs": ["acme"]
}
```

`admin` is present and `true` for Stackorder administrators. Called with an API key, the response is `{"login": "apikey:<key name>", "orgs": [], "admin": true}`.

### `GET /v1/overview` {#overview}

```json
{
  "repos": 3,
  "stacks": 57,
  "drifted": 2,
  "locks_held": 4,
  "runs_by_status": { "applied": 120, "failed": 3, "planned": 14, "superseded": 31 },
  "stacks_by_status": { "applied": 48, "planned": 6, "failed": 1, "blocked": 2 }
}
```

`recent_runs` lists recent runs in the `Run` shape.

### `GET /v1/repos` {#repos}

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

The repository's graph as JSON, for tooling. `ref` selects the commit. `run` replays a run's resolution: the response then includes that run's `affected` stacks and `waves`.

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
        "type": "uses_module"
      }
    ]
  },
  "stack_ids": { "stacks/prod/vpc": "3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f" }
}
```

### `GET /v1/repos/{owner}/{repo}/runs` {#repo-runs}

A page of the repository's runs, newest first, each in the `Run` shape. `status`, `pr` and `mode` filter the list.

### `GET /v1/repos/{owner}/{repo}/stacks` {#repo-stacks}

A page of the repository's stacks, each in the `StackDetail` shape.

### `GET /v1/stacks/{id}` {#stack}

Stack detail: last plan and apply, drift, lock, dependencies both ways and pinned modules.

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
    "job_url": "https://github.com/acme/infra/actions/runs/12345679999/job/34567899999"
  },
  "drift": {
    "checked_at": "2026-09-28T06:07:12Z",
    "drifted": false
  },
  "depends_on": [],
  "dependents": ["stacks/prod/apps"],
  "modules": [
    { "module_key": "acme/modules//tags", "ref": "v1.2.0", "latest": "v1.4.1", "behind": 2 }
  ]
}
```

`last_plan` has the same shape as `last_apply`. `lock` is present while the stack is locked. A drifted stack's `drift` carries the plan `summary` and, with drift issues enabled, `issue_number` and `issue_url`.

### `GET /v1/stacks/{id}/runs` {#stack-runs}

A page of the stack's history, each item in the `RunStackRef` shape used by `last_apply` above.

### `GET /v1/modules` and `GET /v1/modules/{id}` {#modules}

A page of modules, and one module with its released versions and its consumers across repositories. A git or registry module is listed once, under its family `key` without `@ref`; each consumer carries the `ref` it pins and how many released versions it is `behind`. `?q=` keeps the modules whose key contains the text.

```json
{
  "id": "0f1e2d3c-4b5a-4968-8776-655443322110",
  "key": "acme/modules//tags",
  "kind": "git",
  "source": "git::https://github.com/acme/modules.git//tags?ref=v1.2.0",
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

A page of audited actions, newest first: unlocks, re-runs and the other changes people and automation make through the server.

```json
{
  "items": [
    {
      "at": "2026-09-28T09:31:00Z",
      "actor": "octocat",
      "action": "unlock",
      "target": "stack:3d6f0a2e-1b4c-4d8e-9f10-2a3b4c5d6e7f",
      "details": { "repo": "acme/infra", "stack": "stacks/prod/vpc", "reason": "PR 41 closed" }
    }
  ],
  "next_cursor": "MTIz"
}
```

A person sees the entries they made and those about repositories they can see, so a page may hold fewer than `limit` items, or none, and still carry a `next_cursor`.

## Sign-in {#sign-in}

`GET /auth/login` redirects to GitHub's user-authorization flow for the App, with `login` and `read:org` scope only. `GET /auth/callback` completes it and sets the `stackorder_session` cookie. A session is issued only to a member of an org where the App is installed. `POST /auth/logout` ends the session. Sessions are stored in Postgres and record the user's organisations at sign-in, so a membership change takes effect at the next sign-in. Unlock, re-run and sign-out requests made with the session cookie must come from the server's own origin: `Origin` must match `STACKORDER_BASE_URL`, or, without `Origin`, `Sec-Fetch-Site` must be `same-origin`.

## Setup {#setup}

`GET /setup` renders a GitHub App manifest with the right webhook URL, permissions and events, and posts it to GitHub's manifest-creation endpoint. GitHub redirects back to `GET /setup/callback`, which exchanges the code for the App id, private key, webhook secret and OAuth client id and secret, and prints them once as environment variables. GitHub sends the browser to `GET /setup/installed` after the App is installed. In setup mode, when the App variables are not set, the server serves only `/setup`, `/setup/callback`, `/setup/installed`, `/healthz` and `/readyz`, and answers `503` with code `unavailable` everywhere else.

## Webhooks {#webhooks}

`POST /webhooks/github` receives every App event. It verifies the `X-Hub-Signature-256` HMAC against `GITHUB_WEBHOOK_SECRET`, deduplicates by delivery id, writes the event to the queue and returns `202` in under 50 ms. Workers process the event afterwards. See [GitHub App](/reference/github-app#events) for the events.

## Health and metrics {#health}

| Endpoint | Meaning |
| --- | --- |
| `GET /healthz` | Liveness: the process is serving. |
| `GET /readyz` | Readiness: the database is reachable. |
| `GET /metrics` | Prometheus metrics. See [Metrics and tracing](/reference/metrics). |

## OIDC binding {#oidc-binding}

The server verifies a runner token's signature against GitHub's JWKS, cached for an hour and refreshed on an unknown `kid`. It then binds the request to the run with the token's claims:

| Claim | Server check |
| --- | --- |
| `iss` | `https://token.actions.githubusercontent.com`, or `GITHUB_OIDC_ISSUER` |
| `aud` | Equals `STACKORDER_OIDC_AUDIENCE`, which defaults to the base URL |
| `repository`, `repository_id` | The repository belongs to an installation the server knows |
| `sha` | Matches the run's SHA |
| `run_id`, `run_attempt` | For dispatched runs, matches the workflow run the server dispatched; for plan runs, recorded |
| `event_name`, `ref` | Plan runs come from `pull_request` with `ref` `refs/pull/<n>/merge`; dispatched runs come from `workflow_dispatch` on the default branch |
| `environment` | For dispatched runs, equals the environment the server assigned to the stack, so a result cannot come from a job that ran outside the gate |
| `job_workflow_ref` | When `STACKORDER_REQUIRED_WORKFLOW_REF` is set, matches it, so only the canonical reusable workflow can post results |
| `iat` | Within the last 10 minutes |
| `jti` | Not seen before |
| `actor` | Recorded as `requested_by` on the run |

The API also refuses any token whose `run_id` belongs to a run the server has already seen complete.
