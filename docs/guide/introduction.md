# Introduction

Stackorder decides **which** Terraform or OpenTofu stacks a change affects and **in what order** to apply them. GitHub Actions does all of the running.

It is a GitHub App plus a small control-plane server. Execution, credentials, state and modules stay inside your GitHub org and your AWS account. The server only ever sees metadata.

::: warning Status
Stackorder is pre-alpha. These pages document the designed behaviour and the contract in the [architecture document](/design/architecture). The implementation is being built against both.
:::

## The two jobs of the server

The server does exactly two things.

1. **Dependency resolution.** It holds the graph of stacks, shared modules and the edges between them. For a change it computes the affected set, orders applies into waves, and serializes conflicting work with stack-level locks.
2. **Observability.** It records every plan and apply per stack and per commit, surfaces drift, and shows the dependency graph and which stacks consume each module at which version. All of it is available through a small web UI, a JSON API and Prometheus metrics.

## What it is not

Stackorder is deliberately not a state backend, a module registry, a secrets store, a policy engine or a runner.

| Not a | Use instead |
| --- | --- |
| State backend or state viewer | The S3 backend, with S3-native or DynamoDB locking. Stackorder stores only the S3 key so it can link to it. |
| Module registry | Git or local paths. Stackorder indexes module references; it does not host code. |
| Policy engine | OPA or conftest, Checkov, Infracost or any other tool as a step in the plan job. Stackorder records the verdict as a named check on the stack. |
| Runner | GitHub-hosted or self-hosted Actions runners, under your own OIDC-federated AWS role. |
| Multi-VCS tool | Nothing. Stackorder is GitHub only: the App, the OIDC claims and the check-run model are where its leverage comes from. |
| AWS role manager | Your own IAM. You create one OIDC-trusted role per environment; Stackorder only tells the workflow which stack is running. |

## The pitch

Terrateam's execution model with a strictly smaller server, no Docker on the runner side, and dependencies (stack to stack, stack to module, across repos) as the server's core data structure rather than an add-on.

## How it compares

|  | Terraform Cloud / HCP Terraform | Terrakube | Terrateam | Stackorder |
| --- | --- | --- | --- | --- |
| Where Terraform runs | HashiCorp-hosted workers or self-hosted agents | Its own executor pods | GitHub Actions | GitHub Actions |
| State backend | Built in (remote backend) | Built in | Bring your own (S3 etc.) | Bring your own S3 |
| Module registry | Built in | Built in | None | None; tracks module consumers from git sources only |
| Runtime footprint | SaaS | API, executor, UI, Redis, Minio, Postgres | Server + Postgres; Docker-based action image | One container + Postgres; non-Docker actions |
| Cross-stack dependencies | Run triggers | Workspace triggers | Layered runs | First-class graph incl. modules and cross-repo edges |
| Cloud credentials held by server | Yes (or agent) | Yes | No | No |
| Human auth | Own accounts, SSO | Own accounts | GitHub | GitHub OAuth via the App |

The [comparison page](./comparison) goes through each row.

## What a repository needs

- A root [`stackorder.yaml`](/configuration/stackorder-yaml). The minimum is `version: 1`.
- An optional [`.stackorder.yaml`](/configuration/stack-yaml) in any stack that has dependencies or overrides.
- Two [workflow files](/configuration/workflows), each a thin wrapper around a reusable workflow.
- The Stackorder [GitHub App](/reference/github-app) installed on the repository.

## Next steps

- [How it works](./how-it-works): the trust zones, the execution model and the pull request lifecycle.
- [Concepts](./concepts): stacks, modules, edges, waves, locks and drift.
- [Getting started](./getting-started): from an empty AWS account to a first `stackorder apply`.
