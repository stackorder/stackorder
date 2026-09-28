# Architecture

This file is the contract between the packages of this repository. The
[design document](https://claude.ai/artifact/W3gQnvGu5Fw9DSXApYE766) is the
source of truth for behaviour; this file pins the shapes, names and library
choices that let packages be built independently and still fit together.
Change it in the same commit as the code it describes.

## Repositories

| Repository | Contents |
| --- | --- |
| `stackorder/stackorder` | Go module `github.com/stackorder/stackorder`: server, CLI, embedded UI, migrations, docs site, Terraform deployment module |
| `stackorder/actions` | `setup` JavaScript action, `resolve` / `plan` / `apply` / `drift` composite actions, reusable `plan.yml` and `run.yml` |
| `stackorder/example-infra` | Demo monorepo used by the end-to-end tests |

## Layout of this repository

```text
api/v1/                    wire types shared by CLI, server, UI and tooling (JSON + YAML tags, no logic)
cmd/stackorder/            CLI main; all commands live in internal/cli
cmd/stackorder-server/     server main; wiring only
internal/config/           stackorder.yaml and .stackorder.yaml: defaults, validation, per-stack merge
internal/scan/             HCL scanning: discover stacks, parse backends, module sources, remote state; git diff
internal/graph/            pure resolution algorithm: affected set, propagation, waves, cycles, DOT output
internal/report/           pure rendering of check-run output and the sticky PR comment from v1 types
internal/tf/               terraform / tofu process wrapper, plan JSON summary, redaction
internal/client/           HTTP client for the server API used by the CLI, incl. runner OIDC token fetch
internal/cli/              cobra commands: resolve, plan, apply, drift, check, graph, affected, unlock, version
internal/store/            pgx queries; migrations embedded from /migrations
internal/gh/               GitHub App client: JWT, installation tokens, checks, comments, dispatch, reviews, teams
internal/oidc/             verification of GitHub Actions OIDC tokens and claim binding
internal/runs/             run state machine, apply gate, locks, wave dispatch, superseding, cross-repo plans
internal/webhook/          HMAC verification, delivery dedup, event persistence
internal/worker/           queue: claim events and jobs with SKIP LOCKED, retries, idempotent handlers
internal/sched/            cron scheduler for drift and stale-lock reminders; leader by advisory lock
internal/api/              HTTP handlers, auth middleware, sessions and OAuth, API keys, /setup manifest flow, metrics
internal/ui/               embed.FS serving of the built UI with SPA fallback
internal/server/           composition of all of the above into one http.Server; used by cmd and by tests
internal/testutil/         shared test helpers: Postgres via testcontainers, fake GitHub API, fake JWKS
migrations/                golang-migrate SQL files, NNNN_name.up.sql / .down.sql
ui/                        Preact + Vite + TypeScript app; `npm run build` writes internal/ui/dist
docs/                      VitePress documentation site
deploy/terraform/          ECS Fargate + RDS + ALB module; examples/ and tests/
test/integration/          server + Postgres + fake GitHub, whole-flow tests (build tag `integration`)
test/e2e/                  real terraform + LocalStack + example-infra + server (build tag `e2e`)
Dockerfile                 multi-stage: node (ui) -> go -> gcr.io/distroless/static:nonroot
.goreleaser.yaml           CLI releases for linux/darwin/windows, amd64/arm64, checksums; server image
docker-compose.yml         local Postgres and LocalStack for development and tests
```

Dependency direction: `api/v1` <- `config` <- `scan`, `graph`, `report`, `tf`
<- `client`, `cli` (runner side); `api/v1` <- `store`, `gh`, `oidc` <- `runs`
<- `webhook`, `worker`, `sched`, `api` <- `server` <- `cmd`. Nothing under
`internal/` imports `internal/cli` or `internal/server` except `cmd` and tests.
`internal/graph`, `internal/report` and `internal/config` import nothing but
`api/v1` and the standard library (plus the YAML, glob and cron libraries).

## Libraries

These are fixed. Do not introduce an alternative for the same job.

| Job | Library |
| --- | --- |
| HTTP routing | `net/http` ServeMux with method and wildcard patterns (`"POST /v1/runs/{id}/graph"`) |
| Postgres | `github.com/jackc/pgx/v5` with `pgxpool`; no ORM, no sqlx |
| Migrations | `github.com/golang-migrate/migrate/v4` with the `pgx/v5` database driver and the `iofs` source over an embedded FS |
| YAML | `gopkg.in/yaml.v3` |
| HCL | `github.com/hashicorp/hcl/v2` (`hclparse`, `hclsyntax`) and `github.com/hashicorp/terraform-config-inspect/tfconfig` |
| Plan JSON | `github.com/hashicorp/terraform-json` |
| Globs | `github.com/bmatcuk/doublestar/v4` |
| JWT | `github.com/golang-jwt/jwt/v5` (App JWT signing and OIDC token parsing) |
| Cron | `github.com/robfig/cron/v3` parser and scheduler |
| CLI | `github.com/spf13/cobra` |
| Metrics | `github.com/prometheus/client_golang` |
| Tracing | `go.opentelemetry.io/otel` with the OTLP HTTP exporter, enabled only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set |
| Logging | `log/slog`, JSON handler in production, text handler when `STACKORDER_LOG_FORMAT=text` |
| IDs | `github.com/google/uuid` |
| Tests | standard `testing`, `github.com/stretchr/testify/require` and `assert`, `github.com/google/go-cmp/cmp` for diffs |
| Postgres in tests | `github.com/testcontainers/testcontainers-go/modules/postgres`, skipped when `TEST_DATABASE_URL` is set and used directly instead |
| GitHub API | hand written in `internal/gh` on `net/http`; no go-github |
| UI | Preact, TypeScript, Vite, `preact-iso` router, `@dagrejs/dagre` layout with hand written SVG, Vitest and Testing Library, Playwright for browser tests |
| Docs | VitePress |
| Actions repo | `@actions/core`, `@actions/tool-cache`, `@actions/exec`, esbuild, Vitest |

Go version is the one in `go.mod`; `GOTOOLCHAIN=auto` downloads it. Node 24.

## Identities and names

- Stack key inside a repo: `path` or `path:workspace` (`v1.StackKey`). Paths
  are repository relative, slash separated, no `./`, no trailing `/`.
- Qualified stack key: `owner/repo//key`.
- Module keys: see `api/v1/doc.go`.
- Server side primary keys are UUIDs (`runs.id`, `stacks.id`, `modules.id`);
  GitHub ids (`installations.id`, `repos.id`) are the GitHub numeric ids.
- Run ids appear in URLs, workflow inputs and check run output as the UUID
  string.
- Check run names: `stackorder/resolve`, `stackorder/plan`, `stackorder/plan: <key>`,
  `stackorder/apply`, `stackorder/apply: <key>`, `stackorder/<check-name>: <key>`
  for named policy checks.
- Sticky PR comment: one per PR, found by the hidden marker
  `<!-- stackorder:sticky -->` on its first line.
- Plan artifact name: `stackorder-plan-<key with / and : replaced by ->-<sha>`.
- Workflow files in user repos: `.github/workflows/stackorder-plan.yml` and
  `.github/workflows/stackorder-run.yml` (id `stackorder-run.yml` is what the
  server dispatches).
- Comment commands: `stackorder plan [key…]`, `stackorder apply [key…]`,
  `stackorder unlock [key…]`, `stackorder help`. The first token must be
  exactly `stackorder`, case insensitive, at the start of a line.

## Runner endpoints

Stack keys in paths are percent-encoded with `url.PathEscape` (slashes become
`%2F`); Go's ServeMux matches the escaped path and `r.PathValue` returns the
decoded key.

| Method and path | Body | Response |
| --- | --- | --- |
| `POST /v1/runs` | `v1.CreateRunRequest` | `v1.CreateRunResponse` |
| `POST /v1/runs/{id}/graph` | `v1.GraphUploadRequest` | `v1.ResolveResponse` |
| `POST /v1/runs/{id}/stacks/{key}/result` | `v1.StackResult` | `v1.RunStack` |
| `POST /v1/runs/{id}/stacks/{key}/checks/{name}` | `v1.CheckVerdict` | `v1.Check` |
| `GET /v1/runs/{id}` | | `v1.Run` |

Authentication: `Authorization: Bearer <GitHub OIDC token>` for runners,
`Authorization: Bearer sk_<key>` for automation, cookie `stackorder_session`
for humans. Errors are `v1.Error` with codes `unauthorized`, `forbidden`,
`not_found`, `conflict`, `invalid`, `locked`, `unconfirmed`, `internal`.

## Human and automation endpoints

`GET /v1/me`, `GET /v1/overview`, `GET /v1/repos`,
`GET /v1/repos/{owner}/{repo}/graph?ref=&run=`, `GET /v1/repos/{owner}/{repo}/runs`,
`GET /v1/repos/{owner}/{repo}/stacks`, `GET /v1/stacks/{id}`, `GET /v1/stacks/{id}/runs`,
`GET /v1/modules`, `GET /v1/modules/{id}`, `GET /v1/runs/{id}`,
`POST /v1/stacks/{id}/unlock`, `POST /v1/runs/{id}/rerun`,
`GET /auth/login`, `GET /auth/callback`, `POST /auth/logout`,
`GET /setup`, `GET /setup/callback`, `POST /webhooks/github`,
`GET /healthz`, `GET /readyz`, `GET /metrics`. Everything else under `/`
serves the embedded UI with SPA fallback to `index.html`.

## Run state machine

Run status: `pending -> planning -> planned -> applying -> applied`, with
`failed` reachable from `planning` and `applying`, `unconfirmed` reachable
from `pending` and `planning`, and `superseded` reachable from any non
terminal state when a new head SHA arrives for the same PR.

Stack status within a run: `pending -> planning -> planned -> applying -> applied`;
`failed` from `planning` and `applying`; `blocked` when a transitive
`depends_on` or `reads_state` predecessor failed; `noop` when a propagated
stack's plan is empty at apply time; `unconfirmed` when the CLI reported
without a confirmed server round trip; `unknown` when the job vanished;
`skipped` when the requester named a subset that excludes the stack.

Roll-up: a run is `planned` when every stack is `planned`, `noop` or
`skipped`; `applied` when every stack is `applied`, `noop` or `skipped`;
`failed` when any stack is `failed` or `blocked` and no stack is still
running.

## Apply gate, in order

1. Commenter may apply every affected stack: `allowed_teams` membership
   (`active`, nested teams count, cached 60 s) or push permission when the
   list is empty.
2. PR approvals >= `require_approvals`, PR mergeable, `four_eyes` and
   `require_codeowner_review` when set.
3. Every affected stack has a `planned` row for the current head SHA.
4. Every named check on those stacks is `pass` (`warn` passes, `fail` refuses).
5. No affected stack is locked by another PR.

Each refusal is a PR comment naming the failing layer and the exact reason.
Policy for the gate is read from the default branch `stackorder.yaml`
(`repos.config`), never from the PR's copy.

## Waves and dispatch

Waves are the longest-path layering of the affected subgraph over
`depends_on` and `reads_state` edges. The server dispatches
`stackorder-run.yml` once per (wave, environment) with inputs `run_id`,
`mode`, `wave` and `stacks` (JSON array of `v1.MatrixEntry`). Wave n+1 is
dispatched when every stack of wave n is terminal and none failed. Locks are
taken on all affected stacks before wave 0 is dispatched and released on
merge (`before_merge`) or run completion (`on_merge`).

## OIDC binding

The server verifies the runner token signature against the GitHub JWKS
(cached 1 h, refreshed on unknown `kid`), then checks: `aud` equals
`STACKORDER_OIDC_AUDIENCE`; `iss` is `https://token.actions.githubusercontent.com`
(or `GITHUB_OIDC_ISSUER`); `repository` belongs to a known installation;
`sha` matches the run; for dispatched runs `run_id` matches the dispatched
workflow run, `event_name` is `workflow_dispatch` and `environment` matches
the stack's environment; for plan runs `event_name` is `pull_request` and
`ref` is `refs/pull/<n>/merge`; `job_workflow_ref` matches
`STACKORDER_REQUIRED_WORKFLOW_REF` when set; `iat` is within 10 minutes;
`jti` has not been seen. `actor` is recorded as `requested_by`.

## Server configuration

| Variable | Purpose |
| --- | --- |
| `STACKORDER_BASE_URL` | Public URL, used for OAuth callbacks, OIDC audience default and links |
| `STACKORDER_LISTEN` | Listen address, default `:8080` |
| `DATABASE_URL` | Postgres DSN |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET` | App identity |
| `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | Human sign-in |
| `GITHUB_API_URL` | Default `https://api.github.com`; GHES sets its own |
| `GITHUB_OIDC_ISSUER`, `GITHUB_OIDC_JWKS_URL` | Overrides for GHES and tests |
| `STACKORDER_OIDC_AUDIENCE` | Default `STACKORDER_BASE_URL` |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | Optional glob pinning `job_workflow_ref` |
| `STACKORDER_ARTIFACT_BUCKET` | Optional S3 bucket for full plan text |
| `STACKORDER_SESSION_KEY` | 32 byte hex key for cookie signing |
| `STACKORDER_PLAN_TEXT_RETENTION`, `STACKORDER_EVENT_RETENTION`, `STACKORDER_DRIFT_RETENTION` | Durations, defaults `720h`, `168h`, `2160h` |
| `STACKORDER_WORKERS` | Worker goroutines, default 4 |
| `STACKORDER_LOG_LEVEL`, `STACKORDER_LOG_FORMAT` | `info` / `json` by default |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Enables tracing |

The server refuses to start without `DATABASE_URL`. Without the GitHub App
variables it starts in setup mode and serves only `/setup`, `/healthz` and
`/readyz`.

## CLI environment

| Variable | Purpose |
| --- | --- |
| `STACKORDER_SERVER_URL` | Server base URL; unset means local mode |
| `STACKORDER_API_KEY` | Automation key for local `apply` and `unlock` |
| `STACKORDER_RUN_ID` | Run id from the resolve step or the dispatch input |
| `STACKORDER_TOOL`, `STACKORDER_TOOL_VERSION` | Override the configured tool |
| `STACKORDER_BACKEND_CONFIG` | Extra `-backend-config` values, comma separated, for `init` |
| `STACKORDER_PLAN_DIR` | Where plan files are written, default `.stackorder/plans` |
| `GITHUB_*`, `ACTIONS_ID_TOKEN_REQUEST_URL`, `ACTIONS_ID_TOKEN_REQUEST_TOKEN`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY` | Provided by the runner |

Exit codes: 0 success, 1 error, 2 plan has changes (`affected`, `drift`),
3 refused by the server (gate, lock, unconfirmed).

## Conventions

- Errors wrap with `%w` and a short context; sentinel errors are exported
  variables named `ErrX`. HTTP handlers map errors to `v1.Error` codes in one
  place.
- Every exported type and function has a doc comment. Code comments are
  otherwise avoided; rationale goes in commit messages.
- All times are UTC `timestamptz`. JSON times are RFC 3339.
- Handlers and workers take `context.Context` first and are idempotent.
- Tests are table driven where there is a table. Integration tests carry the
  `integration` build tag; end-to-end tests carry `e2e`. `go test ./...`
  must pass with no Docker, no network and no Postgres.
- Commits follow Conventional Commits with a scope: `feat(graph): …`,
  `fix(cli): …`, `test(store): …`, `docs(site): …`, `chore(deps): …`,
  `ci: …`, `build(docker): …`.
