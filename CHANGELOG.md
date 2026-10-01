# Changelog

All notable changes to this repository are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases follow [Semantic Versioning](https://semver.org/). The CLI, the server image and the Terraform module are released together from one `vX.Y.Z` tag.

## [Unreleased]

### Added

- The page `/setup/callback` has a step to give the App a logo, linking to the App's settings on GitHub and to the logo, served at `/setup/logo.png` in and out of setup mode. An App without a logo shows its owner's avatar on every comment and check, and GitHub sets the logo only in its settings page. See [Logo](docs/reference/github-app.md#logo).

### Fixed

- The install and settings links on `/setup/callback` open in a new tab, so following one before storing the credentials no longer leaves the only page that shows them.

## [0.2.0] - 2026-09-30

It works with [`stackorder/actions`](https://github.com/stackorder/actions) v1.0.0 or later; the `env` input and secret of the reusable workflows need v1.1.0.

### Added

- `SECURITY.md`: the supported versions and private vulnerability reporting through GitHub security advisories, linked from the README and the [security model](docs/reference/security-model.md#reporting).
- Terraform module: outputs `alb_arn`, `https_listener_arn`, `http_listener_arn` and `target_group_arn`, so a caller can attach its own listener rules, and [single sign-on in front of the UI](docs/operations/deploy-aws.md#sso) through `oidc_authentication` or `cognito_authentication`. The HTTPS listener then authenticates browsers before forwarding, while rules at priorities 1 to 5 (the module reserves 1 to 99) let GitHub webhooks, Actions jobs and the CLI with bearer tokens, the health checks and Prometheus through without a login.
- Terraform module: `alb_https_egress_cidrs`, HTTPS egress from the load balancer security group to IPv4 CIDRs, `0.0.0.0/0` by default when authentication is set, since identity providers publish no fixed ranges.
- Terraform module: `public_tasks` runs the tasks in the public subnets with public IPs and, with `create_vpc`, creates no NAT gateways; the service security group still admits only the load balancer. For a single-user deployment the NAT gateway was the largest fixed cost after the database.
- Terraform module: `verify_image` (on by default) fails the plan with an explanation when `image_tag` cannot be pulled anonymously from ghcr.io, instead of ECS retrying the pull until the deployment times out, and `deployment_timeout` bounds how long an apply waits for a steady state.
- Terraform module: `db_password_version` and `secrets_version` rotate the database password and rewrite the app and metrics token secrets, now that their values are not in state.
- Terraform module: one task runs while the GitHub App inputs are unset, so there is a single setup token, and the examples export `log_group_name`.
- `STACKORDER_SETUP_TOKEN` sets the [setup token](docs/reference/server-configuration.md#setup-token) instead of the generated one.
- A `visibility` job in the release workflow checks, from a runner without a registry login, that every pushed image tag can be pulled anonymously from ghcr.io, and the [release checklist](CONTRIBUTING.md#releasing) makes package visibility a step, since GitHub publishes a new package as private. A docs test checks that every version pin matches the latest release.
- [Provider credentials](docs/configuration/workflows.md#env) on the Workflows page: the `env` input and `env` secret of the `stackorder/actions` reusable workflows, which need `stackorder/actions` v1.1.0, carry credentials for providers besides AWS, such as a Cloudflare API token, as `KEY=VALUE` lines or `KEY<<DELIMITER` multi-line values. The page covers passing the secret by name, the `ENV` environment secret that replaces it for jobs under that GitHub environment, read-only tokens for plans and write tokens only in gated environments, and `ephemeral` for a token passed as a `TF_VAR_` variable. Getting started, the Terraform on GitHub Actions guide, the actions reference, Stack instances and Security hardening link to it.
- Security hardening explains GitHub's [immutable OIDC subjects](docs/operations/security-hardening.md#immutable-subjects), `repo:<owner>@<id>/<repo>@<id>`, used by repositories created, renamed or transferred after July 15, 2026. It shows how to read a repository's subject prefix with `gh api repos/<owner>/<repo>/actions/oidc/customization/sub`, and every page that shows a trust policy links to it.
- Environments and authorization covers [personal accounts and GitHub Free](docs/configuration/environments-and-authorization.md#free-plan): which of the five layers still hold, that private repositories on GitHub Free have no environment secrets, and a recommended `stackorder.yaml` for a single owner with `require_approvals: 0` and no `allowed_teams`, `four_eyes` or `require_codeowner_review`.
- The `stackorder.yaml` reference has a [Layouts](docs/configuration/stackorder-yaml.md#layouts) section that sets the default `stacks/**` layout beside a flat one: `stacks.discover: ["*"]` with `modules.paths: ["modules/**", "*/modules/**"]`.
- Getting started shows the repository form of `gh variable set STACKORDER_SERVER_URL` next to the organization form, since a personal account has no organization variables.

### Changed

- `/setup` opens only with a one-time setup token, so whoever reaches a fresh server before its operator can no longer register the GitHub App under their own account. The server logs `setup_url` with the token at `warn` level once at start-up; `?token=` is traded for a signed cookie and dropped from the URL before the page loads, and without it the page answers `403`. The token stops working on the instance that created an App, and a generated one changes at every start. Creating a replacement App with `STACKORDER_ALLOW_RESETUP` needs it too. See [the setup token](docs/reference/server-configuration.md#setup-token) and the [security model](docs/reference/security-model.md#setup).
- The Terraform module keeps [secrets out of Terraform state](docs/operations/deploy-aws.md#secrets): `github_app_private_key`, `github_webhook_secret`, `github_oauth_client_secret`, `session_key` and `metrics_token` are ephemeral inputs, the generated database password, session key and metrics token come from ephemeral `random_password` resources, and the Secrets Manager versions and the database password use write-only attributes. The module therefore requires Terraform or OpenTofu 1.11, the AWS provider 6.50 and the random provider 3.7, and the CI matrix validates 1.11 and 1.14. A caller must supply the ephemeral inputs at apply time as well as plan time. The first apply after upgrading from v0.1.0 writes a new database password and rewrites the secrets, so the service rolls and, unless `session_key` and `metrics_token` are set, everyone signs in again and scrapers read a new token; the `self-hosted` example now takes `github_app_id` and `github_oauth_client_id` as variables. See the [upgrade notes](docs/operations/deploy-aws.md#upgrades).
- The Terraform module's first-deploy steps take the setup URL with its token from the task logs, and the release checklist pushes the release commit and its tag together so the docs site never pins a tag that does not exist.

### Fixed

- The README, the introduction, how it works and the design document no longer say the server only sees metadata: it also receives redacted plan text capped at 256 KB, and never cloud credentials or Terraform state.
- The Workflows page no longer says `secrets: inherit` gives providers their credentials: the reusable workflows read no secret, and GitHub passes inherited secrets only to a reusable workflow in the caller's own organization or enterprise, so it passed nothing to `stackorder/actions`. The caller files in the README, the guides, the Workflows and Stack instances pages and the design document no longer pass it.
- The getting-started trust policies no longer imply that every repository's subject starts with `repo:<owner>/<repo>`.
- The README, the introduction and getting started no longer presume a GitHub organization: a personal account can deploy the server, create the App and be a required reviewer.
- The design document no longer says the module's task egress always goes through NAT.

## [0.1.0] - 2026-09-30

The first release. It works with [`stackorder/actions`](https://github.com/stackorder/actions) v1.0.0 or later.

### Added

#### Wire types and configuration

- `api/v1` wire types shared by the CLI, the server, the UI and tooling, with JSON and YAML tags: stacks, edges, graphs, runs, run stacks, checks, matrix entries (with the checkout `sha`, `plan_run_id` and `artifact`), plan summaries, audit entries and the `v1.Error` codes. Stacks, affected stacks, run stacks, stack details and matrix entries carry their `instance`, and a stack its `watch_paths`.
- Loading, defaulting and validation of `stackorder.yaml` and per-stack `.stackorder.yaml`, with unknown keys as errors and per-stack merging.
- Stack instances. A key `path:instance` names one deployment of a directory, with its own state object, var files, environment variables, GitHub environment and apply role, and every instance is a stack of its own in the graph, affected sets, waves, locks, runs, checks, plan artifacts, drift and the UI.
- `stacks.instances.from_var_files` in `stackorder.yaml`, a glob relative to each stack directory: every matching file declares an instance named by its base name up to the first `.`, and is that instance's var file after the root and stack `var_files`. A derived name that is not a valid instance name is an error; a matched file an explicit `instances` list leaves out is a warning.
- `instances` in `.stackorder.yaml`, a list of names or a map from name to overrides of `environment`, `workspace`, `backend_config`, `var_files`, `env`, `plan_output`, `apply.allowed_teams`, `depends_on` and `ignore_inferred`.
- `backend_config` at the root, per stack and per instance: `-backend-config` values for `init`, as `name=value` or a file relative to the repository root. The scanner overlays them on the `backend "s3"` block, so inferred `reads_state` edges and the shared-state warning work for partial backends and per instance.
- `var_files` at the root, per stack and per instance: `-var-file` values for `plan`, the re-plan inside `apply` and `drift`, relative to the stack directory. A missing file is a scan warning and a plan error.
- `env` at the root, per stack and per instance: environment variables for the tool and the hooks, as a string or with a `plan`, `apply` and `drift` value, `drift` falling back to `plan`. Reserved names are refused, and values of secret-looking names are redacted.
- `stacks.exclude` in `stackorder.yaml`: directory globs that are never stacks, beating `stacks.discover` and `stacks.include`.
- `environments` keys `prefix`, `prefix:instance` and `:instance`, the most specific key winning, with template values.
- Go templates in `environments` values, `environment`, `workspace`, `backend_config`, `var_files`, `env` values, `depends_on` and `ignore_inferred`, with `.Path`, `.Name`, `.Instance` and `.Key`, the functions `trimPrefix`, `trimSuffix`, `base`, `dir`, `replace`, `lower` and `upper`, the builtins `and`, `or`, `not`, `eq`, `ne`, `lt`, `le`, `gt` and `ge`, and `if` and `with`. Other actions and builtins are refused, and output over 4096 bytes is an error, so a repository's configuration cannot stall the server.
- The environment of a stack is the first of the instance's `environment`, the stack's and the `environments` match that renders non-empty, then the instance name, then `default`.
- Validation of the instance keys: `environments` prefixes may not contain `:`, an empty prefix needs an instance part, and keys whose prefixes normalise to the same path with the same instance part are refused; a null item in `instances` or a null `env` value is refused; reserved `env` names and the instance name `default` are refused in any letter case; two `from_var_files` matches that derive the same instance name are an error.
- `depends_on` entries with an instance suffix; a bare path to a directory with instances resolves to the instance of the same name.

#### CLI (`stackorder`)

- Commands `resolve`, `plan`, `apply`, `drift`, `check`, `graph`, `affected`, `unlock` and `version`, with exit codes 0, 1, 2 (changes or drift) and 3 (refused by the server or failed closed), GitHub Actions outputs and Markdown step summaries. `--stack` takes a stack key, `path` or `path:instance`; a bare path on a directory with instances is an error that lists them.
- Scanning of a checkout into the dependency graph: stack discovery, `s3` backends, module sources normalised into graph identities, inferred `reads_state` edges from `terraform_remote_state`, `depends_on` (including `owner/repo//key` across repositories), `ignore_inferred`, and changed paths from git with merge-base semantics.
- Resolution of the affected set with reasons, propagation through modules, dependents and remote state, longest-path waves, cycle reporting, validation and Graphviz DOT output.
- Watch paths: the backend configuration files and var files outside a stack directory affect the stack with the reason `watch_path`, and the tree hash covers `*.tfvars`, `*.tfvars.json` and `*.tfbackend` files.
- `resolve` falls back to a local decision and marks it `unconfirmed` when the server cannot be reached, posting a neutral check with `GITHUB_TOKEN`.
- Terraform and OpenTofu wrapper: tool detection, `init` with the stack's `backend_config` and `STACKORDER_BACKEND_CONFIG` (and `-reconfigure` when a stack has `backend_config`, so instances of one directory can share a checkout), workspaces, `plan`, `show`, `apply` and plan JSON summaries.
- Redaction of secrets from everything sent to the server, the step summary and fallback checks, `::add-mask::` registration, and truncation of plan text.
- Repository hooks `.stackorder/hooks/{pre-plan,post-plan,pre-apply,post-apply}.sh`, run by the CLI in CI and locally with `STACKORDER_STACK`, `STACKORDER_RUN_ID`, `STACKORDER_PLAN_JSON` and `STACKORDER_PLAN_FILE`. The tool and the hooks also get `STACKORDER_STACK_PATH` and `STACKORDER_INSTANCE`.
- `apply` confirms the run with the server, checks the run's SHA against the dispatch `sha` and the checkout's `HEAD`, re-plans when the plan artifact is missing or `apply.from_plan` is false, and applies only when the new plan's resource addresses match the recorded plan.
- `apply --local` for manual applies under server locks with an API key; `plan` and `drift` stay local outside Actions unless a server, a run id and an API key are all set.
- Server API client with retries, error mapping to sentinel errors and a fresh runner OIDC token for every call.

#### Server (`stackorder-server`)

- One `http.Server` composing the webhook receiver, the queue workers, the scheduler, the JSON API and the embedded UI, configured from the environment, with the `healthcheck` and `version` subcommands and graceful shutdown.
- Setup mode when no GitHub App credentials are set: `/setup` creates the App from a manifest and prints its credentials once; `STACKORDER_ALLOW_RESETUP` allows creating another App later.
- Run state machine for plan, apply and drift runs, superseding on a new pull request head, and roll-ups of per-stack status (`blocked`, `noop`, `unconfirmed`, `unknown`, `skipped`).
- Apply gate with five layers (requester and `allowed_teams`, pull request state and approvals with `four_eyes` and `require_codeowner_review`, fresh plans, named policy checks, locks), all failures reported together; policy read from the default branch.
- `before_merge` applies from `stackorder apply` comments and `on_merge` applies on merge; comment commands `plan`, `apply` and `unlock`, accepted from users with push permission and rate limited per pull request.
- Stack locks taken all or nothing before wave 0 and released on merge or run completion, with daily reminders for closed pull requests that still hold locks.
- Dispatch of `stackorder-run.yml` per wave and GitHub environment, chunked by `apply.max_parallel`, binding of workflow runs to dispatches by run name and job names, and a reconciliation every minute that resends unsent dispatches, marks vanished jobs `unknown` and recovers apply runs left without a dispatch.
- Cross-repository dependents: a merge that changes an upstream stack starts plan runs in the downstream repositories.
- Scheduled drift checks from `drift.schedule`, one GitHub issue per drifted stack, closed when a later check finds no drift.
- Pull requests are resolved under the `stackorder.yaml` they carry and under the default branch's, with the affected sets united.
- Deployment protection rule answers for dispatched runs, and a neutral check for fork pull requests.
- Start-up and daily sync of installations and repositories from the App API, forgetting what GitHub no longer lists.
- Optional S3 artifact bucket for full plan text (`STACKORDER_ARTIFACT_BUCKET`), served at `GET /v1/runs/{id}/stacks/{key}/plan`.
- Retention pruning of plan text, events and drift history.

#### GitHub integration and security

- Hand-written GitHub App client: App JWT, cached installation tokens, retrying transport with rate limit handling and pagination, check runs, the sticky pull request comment, reviews, team membership, contents, workflow dispatch, runs, jobs and deployments, and OAuth.
- CODEOWNERS parsing with GitHub's matching semantics.
- Webhook HMAC verification, delivery deduplication and persistence.
- Verification of GitHub Actions OIDC tokens against the cached GitHub JWKS, `jti` replay protection, optional `STACKORDER_REQUIRED_WORKFLOW_REF` pinning, and binding of plan tokens to the pull request head and of dispatch tokens to their dispatch, attempt and environment.
- Authentication of runners by OIDC token, automation by `sk_` API keys and people by GitHub sign-in sessions, with same-origin checks on state-changing requests and per-organisation visibility.
- Per-instance AWS roles and session names in `stackorder/actions` v1.0.0: `aws-role-arn-map` keys `path:instance` and `:instance`, matched before path prefixes, and the `aws-role-session-name` input on both reusable workflows, a name or a name per mode.

#### API, metrics and tracing

- Runner endpoints for runs, graph uploads, stack results, check verdicts and unlocks.
- Human and automation endpoints for the overview, repositories, graphs (by commit SHA, SHA prefix or `default`), runs, stacks, modules and their consumers, unlocks, re-runs and the audit log, paginated with cursors.
- `/healthz`, `/readyz` and `/metrics`, the latter optionally behind `STACKORDER_METRICS_TOKEN`.
- Prometheus metrics with the `stackorder_` prefix for runs, stacks, dispatches, drift, locks, commands, webhooks, the queue, GitHub requests and rate limit, HTTP requests and build info.
- OpenTelemetry tracing over OTLP HTTP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set.

#### Storage

- Postgres schema through embedded golang-migrate migrations, run at start-up under an advisory lock.
- Queue of events and jobs claimed with `SKIP LOCKED`, retries with backoff and dead letters; scheduler leader election by advisory lock.
- Stable stack and module identities across graphs, module families and versions, and the default-branch graph of each repository.

#### Web UI

- Preact app embedded in the server with SPA fallback: overview, repositories, the dependency graph (dagre layout drawn as SVG), stack, run and module pages, GitHub sign-in, unlock and re-run actions, and the full plan text from the artifact bucket.

#### Terraform module (`deploy/terraform`)

- ECS Fargate service behind an Application Load Balancer with TLS, RDS PostgreSQL or Aurora Serverless v2, Secrets Manager secrets for the App credentials, and an optional artifact bucket.
- Container health check through the `healthcheck` subcommand, a generated bearer token for `/metrics`, opt-in major database upgrades, optional load balancer access logs and WAF web ACL, and a 60 s drain before ECS stops a task.
- Examples `complete`, `existing-vpc` and `self-hosted` (Stackorder deploying itself), with `terraform test` suites.

#### Packaging and release

- GoReleaser configuration for `stackorder_X.Y.Z_<os>_<arch>` archives for linux, darwin and windows on amd64 and arm64, and `stackorder_X.Y.Z_checksums.txt`.
- Multi-stage Dockerfile (Node for the UI, Go, `gcr.io/distroless/static:nonroot`) with a `HEALTHCHECK`, published as `ghcr.io/stackorder/stackorder:X.Y.Z`, `:X.Y` and `:latest` for linux/amd64 and linux/arm64.
- Makefile targets for building, the three test levels, linting, the UI, the docs site, a local development stack and the image; docker compose for Postgres and LocalStack.

#### Tests and CI

- Unit tests for every package, with `go test ./...` needing no Docker, network or Postgres.
- Integration tests (`integration` tag) running the server on Postgres, an in-memory fake GitHub API, a fake OIDC issuer and the CLI on a fake `terraform` through whole plan, apply, gate, drift, fork, manual apply, installation and cross-repo flows.
- End-to-end tests (`e2e` tag) running real Terraform and OpenTofu against LocalStack S3 and `stackorder/example-infra`, and an optional live variant against a real GitHub organisation.
- CI workflows for lint, unit, integration and build, the UI, the Terraform module, the docs site, nightly end-to-end runs and releases.

#### Documentation

- VitePress documentation site: guide (including getting started and a local demo), configuration (including [Stack instances](docs/configuration/instances.md), with a worked example of one OIDC bootstrap role and a provider role per account, and a migration table from Terrateam), reference, operations, the design document with implementation notes, and the architecture contract.
- `ARCHITECTURE.md` and `CONTRIBUTING.md`.

### Design deviations

Recorded in [ARCHITECTURE.md](ARCHITECTURE.md) and in the implementation notes of the design page on the documentation site.

- The CLI runs the repository hooks itself, in CI and locally, instead of the reusable workflow, and also sets `STACKORDER_PLAN_FILE`; `STACKORDER_PLAN_JSON` and `STACKORDER_PLAN_FILE` are file paths.
- `stackorder-run.yml` takes a fifth input, `sha`, the commit every job checks out; `mode` is `plan`, `apply` or `drift`, never `resolve`; `stacks` is required; the wrapper sets `run-name: stackorder ${{ inputs.mode }} ${{ inputs.run_id }} wave ${{ inputs.wave }}` so the server can bind the workflow run to its dispatch.
- Both reusable workflows require `server-url`, and the calling jobs must carry the permissions block, since a called workflow cannot raise permissions. The `plan.yml` resolve job also needs `pull-requests: read`.
- Server-dispatched `plan` and `drift` jobs run under the environment `default` and assume the plan role (`aws-plan-role-arn`, a new `run.yml` input); only `apply` dispatches carry the stack's environment. Every `run.yml` job runs under `environment: ${{ matrix.environment }}` in the concurrency group `stackorder-stack-<key>`. A stack with no environment mapping runs under `default`, never an empty name.
- The OIDC `sha` claim is never compared with the run's SHA. Plan runs are bound by `event_name`, the `refs/pull/<n>/merge` ref and the pull request head read from GitHub; dispatched runs by `workflow_dispatch`, the default branch ref, the dispatch's `run_id` and the environment.
- `apply.max_parallel` caps the number of stacks in one dispatch rather than concurrent jobs; the caller's `max-parallel` input limits jobs.
- `pull_request_target` plans are not supported; fork pull requests get a neutral check from the server.
- The CLI redacts with its own rules instead of a list of the runner's secret patterns.
- The implementation has more packages than the design names (`config`, `report`, `command`, `tf`, `client`, `principal`, `metrics`, `artifacts` and others), more run and stack statuses (`superseded`, `unconfirmed`, `unknown`, `skipped`), the `manual` trigger, and more API endpoints.
- The schema stores a stack's backend as one `backend` jsonb column and edges by `from_key` and `to_key`, keeps a ref-less family row per module, and adds tables for graph membership, checks, dispatches, OIDC `jti`s and the audit log.
- Named checks accept `warn`, which passes; a stack's `apply.allowed_teams` replaces the root list rather than narrowing it.

[Unreleased]: https://github.com/stackorder/stackorder/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/stackorder/stackorder/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/stackorder/stackorder/releases/tag/v0.1.0
