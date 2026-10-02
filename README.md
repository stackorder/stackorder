<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/lockup-dark.svg">
    <img alt="Stackorder" src=".github/assets/lockup-light.svg" height="60">
  </picture>
</h1>

<p align="center">Which stacks, in what order. Terraform and OpenTofu orchestration on GitHub Actions.</p>

<p align="center">
  <a href="https://stackorder.io">Website</a>
  ·
  <a href="https://docs.stackorder.io">Docs</a>
  ·
  <a href="https://github.com/stackorder/stackorder/releases">Releases</a>
</p>

<p align="center">
  <a href="https://github.com/stackorder/stackorder/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/stackorder/stackorder"></a>
  <a href="LICENSE"><img alt="Licence: Apache-2.0" src="https://img.shields.io/badge/licence-Apache--2.0-blue"></a>
  <a href="https://github.com/stackorder/stackorder/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/stackorder/stackorder/actions/workflows/ci.yml/badge.svg"></a>
</p>

Stackorder plans every stack a pull request affects and applies them in dependency waves on your GitHub Actions runners. Its server never holds cloud credentials or state.

**Try it:** the [local demo](docs/guide/local-demo.md) (no GitHub App, no AWS account) · [getting started](docs/guide/getting-started.md) (GitHub App and AWS)

> **Status:** v0.3.0. The [changelog](CHANGELOG.md) lists what ships and where the code departs from the [design](https://docs.stackorder.io/design/); [How it is tested](#how-it-is-tested) says what the tests cover and what they do not.

## What it does

Stackorder is a GitHub App plus a small control-plane server that decides **which** stacks to run and **in what order**, then lets GitHub Actions do all of the running. Execution, credentials, state and modules stay inside your GitHub organization or personal account and your AWS account; the server receives metadata and redacted, size-capped plan text, never cloud credentials or Terraform state.

The server has exactly two jobs:

- **Dependency resolution.** It holds the graph of stacks, shared modules and the edges between them. From that graph it computes the affected set for a change, orders applies into waves, and serializes conflicting work with stack-level locks.
- **Observability.** It records every plan and apply per stack and per commit, surfaces drift, and shows the dependency graph and which stacks consume each module at which version. All of this is exposed through a small web UI, a JSON API and Prometheus metrics.

It is deliberately **not** a state backend, module registry, secrets store, policy engine or runner. State stays in your S3 bucket, modules stay in git, and Terraform runs on your Actions runners under your own OIDC-federated AWS role.

## How it compares

|  | Atlantis | HCP Terraform | Terrakube | Stategraph (formerly Terrateam) | Stackorder |
| --- | --- | --- | --- | --- | --- |
| Where Terraform runs | On the Atlantis server itself | HashiCorp-hosted VMs or self-hosted agents | Its own executors | Your GitHub Actions or GitLab CI runners | GitHub Actions |
| State backend | Bring your own; any except local state | Built in | Built in | Bring your own | Bring your own S3 |
| Runtime footprint | One Go binary or container, no external database; a persistent disk for plans and locks, or Redis for locks | SaaS, or self-hosted Terraform Enterprise | API, executor, registry, UI, Dex, Redis-compatible store, object storage, Postgres | Server + Postgres; Docker action on the runner | One container + Postgres; non-Docker actions |
| Cross-stack dependencies | `execution_order_group` and `depends_on` within one `atlantis.yaml` | Run triggers; linked Stacks | Shared remote state; run triggers only in 2.34 pre-releases | Layered runs within one repository | A graph of stacks, modules and cross-repo edges; applies in waves |
| Server holds cloud creds | Yes: the server runs Terraform | Yes: stored, or short-lived per run through OIDC | Yes: stored, or mints OIDC tokens | No | No |

Last reviewed 2026-09-30. The [comparison page](docs/guide/comparison.md) cites a source for every HCP Terraform, Terrakube and Stategraph claim, and [Stackorder vs Atlantis](https://stackorder.io/compare/atlantis/) cites the Atlantis column. The [full comparison](https://stackorder.io/compare/) also covers Spacelift, env zero, Scalr and OpenTaco.

## Design principles

1. **The server coordinates; it never executes.** It holds no cloud credentials, no state and no plan files with secrets. If it is compromised, the attacker can trigger workflows and post comments but cannot touch infrastructure. If it is down, PR plans still run and only applies pause.
2. **GitHub already provides most of the control plane.** OIDC, repo permissions, CODEOWNERS, environments, check runs, secrets and compute all come from GitHub. Stackorder adds only what GitHub lacks: a graph across stacks, modules and repos, and a record of what ran.
3. **Heavy work happens on the runner.** HCL parsing, diffing, `terraform plan`, redaction and artifact upload all run in the Actions job. The server receives a small JSON manifest and returns decisions.
4. **One binary on each side.** The server is one Go binary in a distroless image with an embedded web UI. The runner side is one Go CLI, `stackorder`, that also works locally.
5. **Degrade gracefully, never silently.** A step that cannot reach the server falls back to a local decision and marks the check `unconfirmed`, so nobody mistakes a fallback for a green light.

## How it works

1. A pull request push triggers `.github/workflows/stackorder-plan.yml`, which calls the reusable `plan.yml`. Its `resolve` job checks out the pull request head, scans the repository, uploads the dependency graph to the server and gets back the affected stacks as a job matrix.
2. One plan job runs per affected stack, with no GitHub environment, under the plan role. The CLI runs `init` and `plan`, runs the repository's hooks, redacts the output, reports the summary to the server and the `plan` action uploads the plan file as an artifact. The server posts the `stackorder/resolve` and `stackorder/plan` checks, one `stackorder/plan: <key>` check per stack, and one sticky pull request comment.
3. A `stackorder apply` comment (`before_merge`, the default), or the merge itself (`on_merge`), goes through the apply gate: who asked, pull request state and approvals, fresh plans on the head commit, named policy checks, and locks. All failures are reported together in one comment. When the gate passes, the server takes locks on every affected stack and dispatches `.github/workflows/stackorder-run.yml` once per wave and GitHub environment, with the inputs `run_id`, `mode`, `wave`, `sha` and `stacks`.
4. Each job of the reusable `run.yml` runs under the stack's GitHub environment, so environment reviewers and the AWS role's trust policy are the hard gates, and applies the plan file from the plan run. Wave n+1 is dispatched only when every stack of wave n has finished and none failed; a failed stack blocks its dependents.
5. On a `drift.schedule`, the server dispatches `mode: drift` per stack under the environment `default` and can open one GitHub issue per drifted stack.

The runner authenticates to the server with its GitHub OIDC token, bound to the pull request or to the dispatch it belongs to. There are no shared secrets between the runner and the server.

## Repositories

| Repo | Contents |
| --- | --- |
| `stackorder/stackorder` (this repo) | Go module for the server and the CLI, the embedded UI, migrations, the docs site, and the Terraform module that deploys the server |
| [`stackorder/actions`](https://github.com/stackorder/actions) | The `setup` JavaScript action, the `resolve`, `plan`, `apply` and `drift` composite actions, and the reusable `plan.yml` and `run.yml` workflows |
| [`stackorder/example-infra`](https://github.com/stackorder/example-infra) | Demo monorepo used by the end-to-end tests |

## Quick start

- [Getting started](docs/guide/getting-started.md) takes one repository from nothing to a first `stackorder apply`: deploy the server, create the GitHub App from `/setup`, create the plan and apply roles, add `stackorder.yaml` and the two workflow files.
- [Local demo](docs/guide/local-demo.md) runs everything on one machine with no GitHub App, no AWS account and no Go toolchain: Postgres, LocalStack and the server in setup mode in Docker, and the released CLI planning, applying and checking drift in `example-infra`.

Both pages are published on the documentation site, [docs.stackorder.io](https://docs.stackorder.io), as [Getting started](https://docs.stackorder.io/guide/getting-started) and [Local demo](https://docs.stackorder.io/guide/local-demo); its sources are in [`docs/`](docs).

## Install

### CLI

Every `vX.Y.Z` tag publishes `stackorder_X.Y.Z_<os>_<arch>.tar.gz` (`.zip` on Windows) for `linux`, `darwin` and `windows` on `amd64` and `arm64`, and `stackorder_X.Y.Z_checksums.txt` with their SHA-256 sums, on the [releases page](https://github.com/stackorder/stackorder/releases):

```sh
gh release download v0.3.0 --repo stackorder/stackorder \
  --pattern 'stackorder_0.3.0_linux_amd64.tar.gz' \
  --pattern 'stackorder_0.3.0_checksums.txt'
sha256sum --check --ignore-missing stackorder_0.3.0_checksums.txt
tar -xzf stackorder_0.3.0_linux_amd64.tar.gz stackorder
./stackorder version
```

In a workflow, the `setup` action downloads the archive for the runner, verifies it against the checksums file, caches it and puts it on `PATH`. The reusable workflows call it themselves; their `stackorder-version` input pins the release (default `latest`).

```yaml
- uses: stackorder/actions/setup@v1
  with:
    version: 0.3.0
```

### Server image

`ghcr.io/stackorder/stackorder:0.3.0` (also `:0.3` and `:latest`), for `linux/amd64` and `linux/arm64`, built from the [Dockerfile](Dockerfile) on `gcr.io/distroless/static:nonroot`. It needs `DATABASE_URL` and `STACKORDER_BASE_URL`, runs its migrations at start-up, and starts in setup mode until the GitHub App variables are set. See [Deploy as a container](docs/operations/deploy-container.md) and [Server configuration](docs/reference/server-configuration.md).

### Terraform module

[`deploy/terraform`](deploy/terraform) runs the server on ECS Fargate behind an ALB with RDS PostgreSQL or Aurora Serverless v2, with Terraform or OpenTofu 1.11 or later:

```hcl
module "stackorder" {
  source = "github.com/stackorder/stackorder//deploy/terraform?ref=v0.3.0"

  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
  image_tag       = "0.3.0"
}
```

See [Deploy on AWS](docs/operations/deploy-aws.md) and the [module README](deploy/terraform/README.md).

## Using it in a repo

A repo needs a root `stackorder.yaml`, an optional `.stackorder.yaml` in any stack with dependencies or overrides, and two workflow files. Everything has a default, so the smallest valid config is:

```yaml
version: 1
```

These are the two workflow files of [`stackorder/example-infra`](https://github.com/stackorder/example-infra), which read the server URL and the role ARNs from repository variables. The calling jobs must grant the permissions, because a called workflow can lower them but never raise them.

`.github/workflows/stackorder-plan.yml`:

```yaml
name: stackorder plan
on:
  pull_request:
    types: [opened, synchronize, reopened]
concurrency:
  group: stackorder-plan-${{ github.event.pull_request.number }}
  cancel-in-progress: true
jobs:
  plan:
    uses: stackorder/actions/.github/workflows/plan.yml@v1
    permissions:
      id-token: write
      contents: read
      actions: read
      checks: write
      pull-requests: read
    with:
      server-url: ${{ vars.STACKORDER_SERVER_URL }}
      aws-role-arn: ${{ vars.STACKORDER_PLAN_ROLE_ARN }}
      tool: terraform
```

`.github/workflows/stackorder-run.yml`, which the server dispatches. The `run-name` is how the server recognises the workflow runs it dispatched, and `sha` is the commit every job checks out:

```yaml
name: stackorder run
run-name: stackorder ${{ inputs.mode }} ${{ inputs.run_id }} wave ${{ inputs.wave }}
on:
  workflow_dispatch:
    inputs:
      run_id: { description: Stackorder run id, type: string, required: true }
      mode: { description: "plan, apply or drift", type: string, required: true }
      wave: { description: Wave number within the run, type: string, required: false }
      sha: { description: Commit to check out, type: string, required: true }
      stacks: { description: "JSON array of matrix entries, each carrying its environment", type: string, required: true }
jobs:
  run:
    uses: stackorder/actions/.github/workflows/run.yml@v1
    permissions:
      id-token: write
      contents: read
      actions: read
      checks: write
    with:
      server-url: ${{ vars.STACKORDER_SERVER_URL }}
      run-id: ${{ inputs.run_id }}
      mode: ${{ inputs.mode }}
      wave: ${{ inputs.wave }}
      sha: ${{ inputs.sha }}
      stacks: ${{ inputs.stacks }}
      aws-plan-role-arn: ${{ vars.STACKORDER_PLAN_ROLE_ARN }}
      aws-role-arn-map: '{"stacks/prod/": "${{ vars.STACKORDER_APPLY_ROLE_ARN_PROD }}", "stacks/staging/": "${{ vars.STACKORDER_APPLY_ROLE_ARN_STAGING }}", "stacks/legacy/": "${{ vars.STACKORDER_APPLY_ROLE_ARN_DEFAULT }}"}'
```

Applies assume the role `aws-role-arn-map` gives their stack: an exact key such as `infra/network:production`, then an instance in any directory such as `:production`, then the longest matching path prefix; server-dispatched plans and drift checks run under the environment `default` and assume `aws-plan-role-arn`, so the plan role must trust both the repository's `pull_request` tokens and `environment:default`. A repository created after July 15, 2026 carries numeric ids in its OIDC subjects; [Security hardening](docs/operations/security-hardening.md#immutable-subjects) shows how to read the prefix a trust policy must match. Branch protection on the default branch, where your GitHub plan offers it, should require the `stackorder/plan` and `stackorder/apply` checks; [Personal accounts and GitHub Free](docs/configuration/environments-and-authorization.md#free-plan) covers repositories without it. Credentials for providers besides AWS go in the reusable workflows' `env` secret, passed by name, since `secrets: inherit` passes nothing to a workflow in another organization. [Workflows](docs/configuration/workflows.md) documents every input and [provider credentials](docs/configuration/workflows.md#env).

A stack declares cross-stack dependencies in its own `.stackorder.yaml`:

```yaml
depends_on:
  - stacks/prod/vpc
  - acme/network-infra//stacks/prod/tgw
```

A directory deployed several times, such as a component with one var file per environment, declares **instances**: `infra/network:production` and `infra/network:staging` are two stacks with their own state key, var files, environment variables, GitHub environment and apply role, all rendered from templates in `stackorder.yaml`. `stacks.instances.from_var_files: "workspaces/*.tfvars.json"` derives them from the files, and `backend_config`, `var_files` and `env` replace per-directory scripts. [Stack instances](docs/configuration/instances.md) has the keys, a worked example of one bootstrap role with a provider role per account, and a migration table from Stategraph.

## Layout

```text
api/v1/                 wire types shared by the CLI, the server, the UI and tooling
cmd/stackorder/         CLI main
cmd/stackorder-server/  server main, with the healthcheck and version subcommands
internal/
  config/               stackorder.yaml and .stackorder.yaml
  scan/                 HCL scanning: stacks, backends, module sources, remote state; git diff
  graph/                affected set, propagation, waves, cycles, DOT output
  report/               check run output, the sticky PR comment, refusals, drift issues
  command/              `stackorder …` PR comment commands
  tf/                   terraform / tofu wrapper, plan JSON summary, redaction, hooks
  client/               server API client for the CLI, with runner OIDC tokens
  cli/                  cobra commands
  store/                Postgres queries and migrations
  gh/                   GitHub App client; gh/codeowners/ for CODEOWNERS matching
  oidc/                 GitHub Actions OIDC verification and run binding
  principal/            caller identity and the error vocabulary shared by runs and api
  runs/                 run state machine, apply gate, locks, waves, drift, reconciliation
  webhook/              webhook verification, deduplication and persistence
  worker/               queue workers
  sched/                cron scheduler with leader election
  metrics/              Prometheus metrics
  api/                  HTTP handlers, sessions and OAuth, API keys, /setup
  ui/                   embedded UI with SPA fallback
  server/               composition of all of the above into one http.Server
  artifacts/            optional S3 store for full plan text
  version/              build information set at link time
  testutil/             pgtest, ghfake, oidcfake and faketf test helpers
migrations/             golang-migrate SQL files, embedded in the server
ui/                     Preact + Vite + TypeScript app, built into internal/ui/dist
docs/                   VitePress documentation site
deploy/terraform/       ECS Fargate + RDS + ALB module, with examples/ and tests/
test/integration/       whole-flow tests on Postgres and a fake GitHub (build tag integration)
test/e2e/               real Terraform and OpenTofu on LocalStack with example-infra (build tag e2e)
Dockerfile              node (UI) -> go -> distroless/static:nonroot
.goreleaser.yaml        CLI archives and checksums
docker-compose.yml      Postgres and LocalStack for development and tests
```

[ARCHITECTURE.md](ARCHITECTURE.md) is the contract between these packages and the other two repositories: layout, dependency direction, libraries, names, endpoints, configuration and the recorded departures from the design.

## Development

Go (the version in `go.mod`; `export GOTOOLCHAIN=auto` downloads it), Node 24 with npm, and Docker for the integration and end-to-end tests. Terraform or OpenTofu on `PATH` for the end-to-end tests.

```sh
make build            # bin/stackorder and bin/stackorder-server
make test             # unit tests, no Docker, no network
make test-integration # server + Postgres + fake GitHub + the CLI on a fake terraform, needs Docker
make test-e2e         # real terraform and tofu + LocalStack + example-infra, needs Docker
make lint             # gofmt, go vet, golangci-lint
make ui               # build the UI into internal/ui/dist
make ui-test          # UI unit tests
make docs             # build the docs site
make dev              # Postgres and LocalStack with docker compose, then the server
make down             # stop them and drop their volumes
make docker           # build the server image
make sync-example     # refresh the vendored copy of example-infra used by the integration tests
```

[CONTRIBUTING.md](CONTRIBUTING.md) covers the test suites, their environment variables, commit conventions and releases.

## How it is tested

- **Unit tests** for every package (`go test ./...`), with no Docker and no network.
- **Integration tests** run the server on Postgres, with an in-memory fake of the GitHub API, and the CLI against a fake `terraform` binary, through whole pull request, apply, drift and cross-repo flows.
- **End-to-end tests** run the real Terraform 1.14 and OpenTofu 1.12 binaries against a LocalStack 4.0 S3 bucket and the [`stackorder/example-infra`](https://github.com/stackorder/example-infra) monorepo, with the server in-process and the CLI as a separate process for every job.
- **The UI** has Vitest and Playwright tests.
- **The Terraform module** has `terraform test` suites and is validated, with its examples, on Terraform 1.11 and 1.14.

Not covered by the default suites: a real GitHub organisation (`TestLiveGitHub` runs only when one is configured), real AWS, and GitHub Enterprise Server, which is supported through configuration but has not been run against.

## Documentation

- [docs.stackorder.io](https://docs.stackorder.io): guide, configuration, reference and operations, built from the sources in [`docs/`](docs).
- [Design document](https://docs.stackorder.io/design/): the behaviour Stackorder implements, with notes where the code departs from it. Its source is [docs/design](docs/design/index.md).
- [ARCHITECTURE.md](ARCHITECTURE.md): the contract between packages and repositories.
- [CONTRIBUTING.md](CONTRIBUTING.md): building, testing and contributing.
- [SECURITY.md](SECURITY.md): supported versions and how to report a vulnerability privately.
- [CHANGELOG.md](CHANGELOG.md): what each release contains.

## License

[Apache License 2.0](LICENSE)
