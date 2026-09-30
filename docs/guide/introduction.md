# Introduction

Stackorder decides **which** Terraform or OpenTofu stacks a change affects and **in what order** to apply them. GitHub Actions does all of the running.

It is a GitHub App plus a small control-plane server. Execution, credentials, state and modules stay inside your GitHub org and your AWS account. The server only ever sees metadata.

::: info About these pages
The guide, configuration, reference and operations pages describe the code as released. The [design document](/design/) explains why it works this way, with implementation notes where the code departs from it, and the [architecture contract](/design/architecture) pins the names and shapes.
:::

## The two jobs of the server

The server does exactly two things.

1. **Dependency resolution.** It holds the graph of stacks, shared modules and the edges between them. For a change it computes the affected set, orders applies into waves, and serializes conflicting work with stack-level locks.
2. **Observability.** It records every plan and apply per stack and per commit, surfaces drift, and shows the dependency graph and which stacks consume each module at which version. All of it is available through a small web UI, a JSON API and Prometheus metrics.

<Screenshot
  name="ui-overview"
  alt="The Stackorder web UI overview: counts of repositories, stacks, drifted stacks and locks held, bars of stacks and runs by status, and a table of recent plan, apply and drift runs."
  :width="880"
  :height="691"
  caption="The web UI's overview page, with sample data for an acme organization."
/>

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

The execution model of Stategraph (formerly Terrateam), where Terraform runs on your own CI runners, with one server container plus Postgres, no Docker on the runner side, and dependencies (stack to stack, stack to module, across repos) as the server's core data structure.

## How it compares

|  | HCP Terraform | Terrakube | Stategraph | Stackorder |
| --- | --- | --- | --- | --- |
| Where Terraform runs | HashiCorp-hosted VMs by default, or self-hosted agents | Its own executors: a pod pool, Kubernetes Jobs or self-hosted agents | Your GitHub Actions or GitLab CI runners | GitHub Actions |
| State backend | Built in | Built in, on its configured object storage | Bring your own | Bring your own S3 |
| Module registry and tracking | Built-in private registry; the Explorer shows module usage | Built-in private module and provider registry | A module-aware indexer, off by default, plans the directories that use a changed local module | No registry; tracks module consumers from git sources only |
| Runtime footprint | SaaS; self-hosted Terraform Enterprise runs containers with PostgreSQL, object storage and Vault | API, executor, registry, UI, Dex (with OpenLDAP by default), a Redis-compatible store, object storage and Postgres | Server + Postgres behind a public HTTPS URL; Docker container action on the runner | One container + Postgres; non-Docker actions |
| Cross-stack dependencies | Run triggers between workspaces; linked Stacks | Shared remote state in the stable 2.33 line; run triggers only in 2.34 pre-releases | Layered runs within one repository | First-class graph incl. modules and cross-repo edges |
| Cloud credentials held by server | Yes: stored as variables, or short-lived per-run credentials through OIDC | Yes: stored as variables; with dynamic credentials it holds an OIDC signing key and mints tokens | No; they stay on the runner | No |

The [comparison page](./comparison) goes through each row and links the source of every competitor claim, last reviewed 2026-09-30.

## What a repository needs

- A root [`stackorder.yaml`](/configuration/stackorder-yaml). The minimum is `version: 1`.
- An optional [`.stackorder.yaml`](/configuration/stack-yaml) in any stack that has dependencies or overrides.
- Two [workflow files](/configuration/workflows), each a thin wrapper around a reusable workflow.
- The Stackorder [GitHub App](/reference/github-app) installed on the repository.

## Next steps

- [How it works](./how-it-works): the trust zones, the execution model and the pull request lifecycle.
- [Concepts](./concepts): stacks, modules, edges, waves, locks and drift.
- [Getting started](./getting-started): from an empty AWS account to a first `stackorder apply`.
- [Local demo](./local-demo): the server, the CLI and real plans on one machine, with no GitHub App or AWS account.
