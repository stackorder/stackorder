# Comparison

Stackorder keeps the execution model of an Actions-based tool and shrinks everything else. The server coordinates and remembers; it never runs Terraform and never holds cloud credentials.

::: info Last reviewed 2026-09-30
Competitor facts on this page come from each vendor's own documentation, linked under [Sources](#sources). These products change often; check the sources before you decide. The website's comparison is at [stackorder.io/compare](https://stackorder.io/compare/).
:::

|  | HCP Terraform | Terrakube | Stategraph (formerly Terrateam) | Stackorder |
| --- | --- | --- | --- | --- |
| Where Terraform runs | HashiCorp-hosted VMs by default, or self-hosted agents | Its own executors: a pod pool, Kubernetes Jobs or self-hosted agents | Your GitHub Actions or GitLab CI runners | GitHub Actions |
| State backend | Built in | Built in, on its configured object storage | Bring your own | Bring your own S3 |
| Module registry and tracking | Built-in private registry; the Explorer shows module usage | Built-in private module and provider registry | A module-aware indexer, off by default, plans the directories that use a changed local module | No registry; lists each module's consumers, and for git modules how many releases they are behind |
| Runtime footprint | SaaS; self-hosted Terraform Enterprise runs containers with PostgreSQL, object storage and Vault | API, executor, registry, UI, Dex (with OpenLDAP by default), a Redis-compatible store, object storage and Postgres | Server + Postgres behind a public HTTPS URL; Docker container action on the runner | One container + Postgres; non-Docker actions |
| Cross-stack dependencies | Run triggers between workspaces; linked Stacks | Shared remote state in the stable 2.33 line; run triggers only in 2.34 pre-releases | Layered runs within one repository | A graph of stacks, modules and cross-repo edges; applies in waves |
| Cloud credentials held by server | Yes: stored as variables, or short-lived per-run credentials through OIDC | Yes: stored as variables; with dynamic credentials it holds an OIDC signing key and mints tokens | No; they stay on the runner | No |

HCP Terraform is the SaaS formerly called Terraform Cloud, renamed on April 22, 2024; Terraform Enterprise is its self-hosted distribution, and HashiCorp has been an IBM company since February 27, 2025. The Stategraph column is Stategraph Orchestration. It reads `.stategraph/config.yml`, and still reads `.terrateam/config.yml` when that file is absent.

## Row by row

### Where Terraform runs

Terraform runs in your GitHub Actions jobs, on GitHub-hosted or self-hosted runners. Stackorder manages no runners and no agents. A plan on a hosted `ubuntu-latest` runner spends about 5 s on checkout, 2 s on tool setup and 1 s on `stackorder`; the rest is `init` and `plan`. Stackorder adds under 10 s to what Terraform itself needs.

### State backend

State stays in your S3 bucket, locked with `use_lockfile = true` (Terraform or OpenTofu 1.10 and later) or a DynamoDB table. Each stack's `backend "s3"` block is the source of truth. The CLI reads `bucket`, `key` and `region` from it to run `init` and to report the location, so the UI can link a stack to its state object. Stackorder never takes or releases the state lock; its own [orchestration lock](./concepts#locks) sits above it.

### Module registry and tracking

There is none, and Stackorder hosts no module code. Modules are referenced by local path, git source or registry address. Stackorder indexes those references so it can answer "who consumes this module, at which version", and the UI lists the consumers of every module, registry modules included. When a git module's repository pushes a semver tag, the server records the version, so the UI shows how many releases each consumer is behind. Only local modules propagate changes to the stacks that use them. Bumping versions is left to Renovate or Dependabot. See [Module version tracking](./concepts#module-versions).

### Runtime footprint

One Go binary in one distroless image of roughly 30 MB, plus Postgres. The web UI is embedded in the same binary. It runs in a 0.25 vCPU / 512 MB Fargate task for an org with a few hundred stacks.

On the runner side, the actions are one JavaScript action that installs a static binary and four composite actions that call it. Nothing is Docker-based, so a job pays about one second of overhead instead of an image pull, and self-hosted runners without a Docker socket work unchanged.

### Cross-stack dependencies

The dependency graph is the server's core data structure, not an add-on. It has stacks and modules as nodes and three edge kinds: explicit `depends_on`, `uses_module` parsed from module sources, and `reads_state` inferred from `terraform_remote_state`. Applies run in [waves](./concepts#waves) layered by the longest path through the affected part of the graph, and a failed stack blocks its dependents. Edges can point at stacks in other repositories; they appear in the graph and can trigger plan-only runs downstream, but each run applies one repository. See [Concepts](./concepts#edges).

### Cloud credentials held by server

None. The runner assumes your IAM role with its own GitHub OIDC token. The server's only outbound calls are to GitHub, for its API and its OIDC signing keys, and to Postgres, plus two optional ones: an S3 bucket of its own for full plan text, and an OTLP endpoint for traces. A compromised server can dispatch workflows and post comments, but cannot read state or assume your roles. See the [security model](/reference/security-model).

## When something else fits better

- **You are not on GitHub.** Stackorder is GitHub only, by design.
- **You want the tool to host state or modules.** Stackorder stores neither. Stack discovery looks for `backend "s3"` blocks, and state links assume S3.
- **You want a policy engine built in.** Stackorder records the verdicts of the tools you run; it does not evaluate policy itself.
- **You want managed runners or agents.** Use GitHub's own runner mechanisms; Stackorder does not manage them.

## Sources {#sources}

Each competitor claim above, with the page it comes from. Last reviewed 2026-09-30.

### HCP Terraform

- Renamed from Terraform Cloud on April 22, 2024; Terraform Enterprise is the self-hosted distribution of the same application: [Terraform Enterprise docs](https://developer.hashicorp.com/terraform/enterprise).
- IBM completed its acquisition of HashiCorp on February 27, 2025: [IBM newsroom](https://newsroom.ibm.com/2025-02-27-ibm-completes-acquisition-of-hashicorp,-creates-comprehensive,-end-to-end-hybrid-cloud-platform).
- Runs execute on disposable VMs in HashiCorp's cloud by default, or on agents in your infrastructure, and HCP Terraform is the state backend: [HCP Terraform overview](https://developer.hashicorp.com/terraform/cloud-docs/overview).
- Self-hosted agents need only outbound connectivity: [agent requirements](https://developer.hashicorp.com/terraform/cloud-docs/agents/requirements).
- Private module registry and the Explorer's module usage view: [Explorer](https://developer.hashicorp.com/terraform/cloud-docs/workspaces/explorer).
- Terraform Enterprise runs as containers and stores data in PostgreSQL, S3-compatible object storage and Vault (internal by default): [deployment](https://developer.hashicorp.com/terraform/enterprise/deploy), [storage](https://developer.hashicorp.com/terraform/enterprise/deploy/configuration/storage).
- Run triggers queue a downstream run after a successful apply in up to 20 source workspaces: [run triggers](https://developer.hashicorp.com/terraform/cloud-docs/workspaces/settings/run-triggers).
- Terraform Stacks, generally available since September 25, 2025, and linked Stacks: [Stacks](https://developer.hashicorp.com/terraform/cloud-docs/stacks).
- Static provider credentials stored as variables or variable sets: [variables](https://developer.hashicorp.com/terraform/cloud-docs/variables).
- Dynamic provider credentials: HCP Terraform receives short-lived credentials for each run and places them in the run environment: [dynamic provider credentials](https://developer.hashicorp.com/terraform/cloud-docs/workspaces/dynamic-provider-credentials).

### Terrakube

- Runs execute in Terrakube's executors: a persistent pod pool, ephemeral Kubernetes Jobs, or self-hosted executor agents: [self-hosted agents](https://docs.terrakube.io/getting-started/deployment/self-hosted-agents).
- State is managed by Terrakube in its configured storage (Azure Storage, S3, GCS or MinIO): [Terraform state](https://docs.terrakube.io/user-guide/workspaces/terraform-state).
- Private module and provider registry: [publishing private modules](https://docs.terrakube.io/user-guide/private-registry/publishing-private-modules).
- The Helm chart deploys API, executor, registry and UI, plus Dex, OpenLDAP (on by default), MinIO, PostgreSQL and Valkey: [Chart.yaml](https://github.com/terrakube-io/terrakube-helm-chart/blob/main/charts/terrakube/Chart.yaml).
- The stable line shares state between workspaces through `terraform_remote_state`: [share workspace state](https://docs.terrakube.io/user-guide/workspaces/share-workspace-state). Workspace run triggers ship only in 2.34.0 pre-releases: [pull request #3547](https://github.com/terrakube-io/terrakube/pull/3547), [releases](https://github.com/terrakube-io/terrakube/releases).
- Static cloud credentials as sensitive variables: [variables](https://docs.terrakube.io/user-guide/workspaces/variables). Dynamic provider credentials, with the API as an OIDC issuer: [dynamic provider credentials](https://docs.terrakube.io/user-guide/workspaces/dynamic-provider-credentials).

### Stategraph

- Terrateam was renamed Stategraph Orchestration: [about Stategraph](https://stategraph.com/about). The configuration file is `.stategraph/config.yml`, with `.terrateam/config.yml` as the fallback: [configuration](https://stategraph.com/docs/orchestration/configuration).
- Plans and applies run on your GitHub Actions or GitLab CI runners; the server dispatches workflows and does not run Terraform: [Orchestration docs](https://stategraph.com/docs/orchestration).
- Orchestration uses your existing state backend; database-backed state is a separate commercial product: [stategraph/stategraph](https://github.com/stategraph/stategraph).
- The module-aware indexer is off by default: [indexer](https://stategraph.com/docs/reference/orchestration/configuration/indexer).
- The open-source self-host is a server plus PostgreSQL with a public HTTPS URL: [open-source self-hosting](https://stategraph.com/docs/admin/self-hosting/open-source). The runner uses `terrateamio/action@v1`, a Docker container action: [action.yml](https://github.com/terrateamio/action/blob/main/action.yml).
- Layered runs order plans and applies within one repository: [layered runs](https://stategraph.com/docs/orchestration/workflows/advanced/layered-runs).
- The server never holds cloud credentials; the runner authenticates with OIDC or with CI secrets: [AWS](https://stategraph.com/docs/orchestration/cloud-providers/aws).
