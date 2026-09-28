# Comparison

Stackorder keeps the execution model of an Actions-based tool and shrinks everything else. The server coordinates and remembers; it never runs Terraform and never holds cloud credentials.

|  | Terraform Cloud / HCP Terraform | Terrakube | Terrateam | Stackorder |
| --- | --- | --- | --- | --- |
| Where Terraform runs | HashiCorp-hosted workers or self-hosted agents | Its own executor pods | GitHub Actions | GitHub Actions |
| State backend | Built in (remote backend) | Built in | Bring your own (S3 etc.) | Bring your own S3 |
| Module registry | Built in | Built in | None | None; tracks module consumers from git sources only |
| Runtime footprint | SaaS | API, executor, UI, Redis, Minio, Postgres | Server + Postgres; Docker-based action image | One container + Postgres; non-Docker actions |
| Cross-stack dependencies | Run triggers | Workspace triggers | Layered runs | First-class graph incl. modules and cross-repo edges |
| Cloud credentials held by server | Yes (or agent) | Yes | No | No |
| Human auth | Own accounts, SSO | Own accounts | GitHub | GitHub OAuth via the App |

## Row by row

### Where Terraform runs

Terraform runs in your GitHub Actions jobs, on GitHub-hosted or self-hosted runners. Stackorder manages no runners and no agents. A plan on a hosted `ubuntu-latest` runner spends about 5 s on checkout, 2 s on tool setup and 1 s on `stackorder`; the rest is `init` and `plan`. Stackorder adds under 10 s to what Terraform itself needs.

### State backend

State stays in your S3 bucket, locked with `use_lockfile = true` (Terraform or OpenTofu 1.10 and later) or a DynamoDB table. Each stack's `backend "s3"` block is the source of truth. The CLI reads `bucket`, `key` and `region` from it to run `init` and to report the location, so the UI can link a stack to its state object. Stackorder never takes or releases the state lock; its own [orchestration lock](./concepts#locks) sits above it.

### Module registry

There is none. Modules are referenced by git or local path. Stackorder indexes the references so it can answer "who consumes this module, at which version", and records registry modules so the UI can list their consumers. It shows version lag; bumping versions is left to Renovate or Dependabot.

### Runtime footprint

One Go binary in one distroless image of roughly 30 MB, plus Postgres. The web UI is embedded in the same binary. It runs in a 0.25 vCPU / 512 MB Fargate task for an org with a few hundred stacks.

On the runner side, the actions are one JavaScript action that installs a static binary and four composite actions that call it. Nothing is Docker-based, so a job pays about one second of overhead instead of an image pull, and self-hosted runners without a Docker socket work unchanged.

### Cross-stack dependencies

The dependency graph is the server's core data structure, not an add-on. It has stacks and modules as nodes and three edge kinds: explicit `depends_on`, `uses_module` parsed from module sources, and `reads_state` inferred from `terraform_remote_state`. Edges can point at stacks in other repositories. See [Concepts](./concepts#edges).

### Cloud credentials held by the server

None. The runner assumes your IAM role with its own GitHub OIDC token. The server's only outbound calls are to GitHub and to Postgres, plus an optional S3 bucket of its own for full plan text. A compromised server can dispatch workflows and post comments, but cannot read state or assume your roles. See the [security model](/reference/security-model).

### Human auth

People sign in to the web UI with GitHub, through the App's user-authorization flow. A session is issued only to members of an org where the App is installed, and the UI shows only that org's repositories. Authorization to apply reuses GitHub: repository permissions, teams, CODEOWNERS and environments.

## When something else fits better

- **You are not on GitHub.** Stackorder is GitHub only, by design.
- **You want the tool to host state or modules.** Stackorder stores neither. Stack discovery looks for `backend "s3"` blocks, and state links assume S3.
- **You want a policy engine built in.** Stackorder records the verdicts of the tools you run; it does not evaluate policy itself.
- **You want managed runners or agents.** Use GitHub's own runner mechanisms; Stackorder does not manage them.
