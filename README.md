# Stackorder

Lightweight Terraform and OpenTofu orchestration on GitHub Actions.

Stackorder is a GitHub App plus a small control-plane server that decides **which** stacks to run and **in what order**, then lets GitHub Actions do all of the running. Execution, credentials, state and modules stay inside your GitHub org and AWS account; the server only ever sees metadata.

> **Status:** pre-alpha. The design is settled and implementation is starting. Nothing here is usable yet. The design doc is the source of truth: [Stackorder design](https://claude.ai/artifact/W3gQnvGu5Fw9DSXApYE766).

## What it does

The server has exactly two jobs:

- **Dependency resolution.** It holds the graph of stacks, shared modules and the edges between them. From that graph it computes the affected set for a change, orders applies into waves, and serializes conflicting work with stack-level locks.
- **Observability.** It records every plan and apply per stack and per commit, surfaces drift, and shows the dependency graph and which stacks consume each module at which version. All of this is exposed through a small web UI, a JSON API and Prometheus metrics.

It is deliberately **not** a state backend, module registry, secrets store, policy engine or runner. State stays in your S3 bucket, modules stay in git, and Terraform runs on your Actions runners under your own OIDC-federated AWS role.

## How it compares

|                          | Terraform Cloud / HCP | Terrakube            | Terrateam                     | Stackorder                                         |
| ------------------------ | --------------------- | -------------------- | ----------------------------- | -------------------------------------------------- |
| Where Terraform runs     | HashiCorp workers     | Own executor pods    | GitHub Actions                | GitHub Actions                                     |
| State backend            | Built in              | Built in             | Bring your own                | Bring your own S3                                  |
| Runtime footprint        | SaaS                  | API, executor, UI, Redis, Minio, Postgres | Server + Postgres; Docker action image | One container + Postgres; non-Docker actions |
| Cross-stack dependencies | Run triggers          | Workspace triggers   | Layered runs                  | First-class graph incl. modules and cross-repo edges |
| Server holds cloud creds | Yes (or agent)        | Yes                  | No                            | No                                                 |

## Design principles

1. **The server coordinates; it never executes.** It holds no cloud credentials, no state and no plan files with secrets. If it is compromised, the attacker can trigger workflows and post comments but cannot touch infrastructure. If it is down, PR plans still run and only applies pause.
2. **GitHub already provides most of the control plane.** OIDC, repo permissions, CODEOWNERS, environments, check runs, secrets and compute all come from GitHub. Stackorder adds only what GitHub lacks: a graph across stacks, modules and repos, and a record of what ran.
3. **Heavy work happens on the runner.** HCL parsing, diffing, `terraform plan`, redaction and artifact upload all run in the Actions job. The server receives a small JSON manifest and returns decisions.
4. **One binary on each side.** The server is one Go binary in a distroless image with an embedded web UI. The runner side is one Go CLI, `stackorder`, that also works locally.
5. **Degrade gracefully, never silently.** A step that cannot reach the server falls back to a local decision and marks the check `unconfirmed`, so nobody mistakes a fallback for a green light.

## How it works

1. A PR push triggers `stackorder-plan.yml`. Its `resolve` job scans the repo, posts the dependency graph to the server, and gets back the affected stacks as a job matrix.
2. One plan job runs per affected stack. The server posts a check run per stack and one sticky PR comment.
3. A `stackorder apply` comment, or a merge in `on_merge` mode, goes through the apply gate. The server checks permissions, approvals, fresh plans, policy checks and locks. It then takes stack locks and dispatches `stackorder-run.yml` one dependency wave at a time.
4. Each apply job runs under the stack's GitHub environment, so environment reviewers and the AWS role's trust policy are the hard gates. A failed stack blocks its dependents.
5. On a schedule, the server dispatches drift checks and can open one GitHub issue per drifted stack.

The runner authenticates to the server with its GitHub OIDC token. There are no shared secrets between the runner and the server.

## Repositories

| Repo | Contents |
| --- | --- |
| `stackorder/stackorder` (this repo) | Go module for the server and the CLI, the embedded UI, migrations, and the Terraform module that deploys the server |
| `stackorder/actions` | The `setup` JavaScript action, the `resolve`, `plan`, `apply` and `drift` composite actions, and the reusable `plan.yml` and `run.yml` workflows |
| `stackorder/example-infra` | Demo monorepo used by the end-to-end tests |

Planned layout of this repo:

```text
cmd/stackorder-server/   server main
cmd/stackorder/          CLI main
internal/                webhook, worker, graph, runs, gh, oidc, sched, api, store, scan
ui/                      Preact app, built into internal/ui/dist and embedded
migrations/              Postgres migrations, run at start-up
deploy/terraform/        ECS Fargate + RDS + ALB module
Dockerfile               multi-stage, distroless/static, non-root
.goreleaser.yaml         CLI releases for linux/darwin/windows, amd64/arm64
```

## Using it in a repo (planned)

A repo needs a root `stackorder.yaml`, an optional `.stackorder.yaml` in any stack with dependencies or overrides, and two thin workflow files. Everything has a default, so the smallest valid config is:

```yaml
version: 1
```

A plan workflow is a dozen lines:

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
    with:
      aws-role-arn: arn:aws:iam::123456789012:role/stackorder-plan
      tool: tofu
    secrets: inherit
```

A stack declares cross-stack dependencies in its own `.stackorder.yaml`:

```yaml
depends_on:
  - stacks/prod/vpc
  - acme/network-infra//stacks/prod/tgw
```

## Roadmap

Four phases, each gated by an end-to-end demonstration. No dates are committed yet.

| Phase | Scope | Gate |
| --- | --- | --- |
| 1. Core loop | Resolve, plan and apply; checks and PR comment; one repo with `depends_on` | Plan to apply in one repo |
| 2. Graph depth | Module and state edges; propagation and waves; graph page in the UI | A module change ripples through in waves |
| 3. Observability | Drift runs and issues; metrics and run history; module version lag | Drift visible in the UI and in issues |
| 4. Cross-repo | Cross-repo edges; workflow-ref pinning; GHES; the self-deploying Terraform module | Public v1 |

Phase 1 alone is a usable Atlantis-style tool. Phase 2 is where Stackorder starts doing something the others do not.

## Development

Requirements (planned): Go, Node for the UI build, and Postgres. End-to-end tests run against a throwaway GitHub org and a LocalStack S3 bucket. The `graph` package gets exhaustive unit tests, because that is where the correctness risk sits.
