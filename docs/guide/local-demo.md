# Local demo

This page runs Stackorder on one machine, with no GitHub App and no AWS account: Postgres and LocalStack in Docker, the server in setup mode, and the CLI against the [`stackorder/example-infra`](https://github.com/stackorder/example-infra) repository with its state in a LocalStack S3 bucket. The commands are the ones the repository's `make dev` target and the example repository's end-to-end setup use.

What it shows: the server starting, migrating and answering in setup mode; the dependency graph of a real monorepo; the affected set and waves of a change; real Terraform plans, applies and drift checks against S3 state, with hooks. What it cannot show: pull request checks, comment commands and dispatched applies, which need a GitHub App and a server GitHub can reach. [Getting started](./getting-started) covers those.

## Before you start {#prerequisites}

- Docker with Compose.
- Go, for building the CLI and running the server. The version is the one in `go.mod`; with `GOTOOLCHAIN=auto` Go downloads it.
- Terraform or OpenTofu 1.10 or later, since the example stacks use S3-native locking (`use_lockfile = true`). The example's `stackorder.yaml` says `tool: terraform`; with only OpenTofu installed, `export STACKORDER_TOOL=tofu` and use `tofu` where the page says `terraform`.
- git, and optionally `jq` (for the example hook) and Graphviz (for `dot`).

Ports 5432, 4566 and 8080 must be free.

## 1. Start Postgres and LocalStack {#services}

From a checkout of `stackorder/stackorder`:

```sh
git clone https://github.com/stackorder/stackorder.git
cd stackorder
export GOTOOLCHAIN=auto

docker compose up -d postgres localstack
docker compose ps
```

`docker-compose.yml` starts `postgres:17-alpine` with the user, password and database `stackorder` on port 5432, and `localstack/localstack:4.0` with `SERVICES=s3,sts` on port 4566. Wait until both are `healthy`.

Create the state bucket the example stacks use:

```sh
docker compose exec localstack awslocal s3 mb s3://stackorder-example-state
```

## 2. Run the server in setup mode {#server}

In a second terminal, from the same directory:

```sh
DATABASE_URL='postgres://stackorder:stackorder@localhost:5432/stackorder?sslmode=disable' \
STACKORDER_BASE_URL=http://localhost:8080 \
go run ./cmd/stackorder-server
```

This is what `make dev` runs, after `docker compose up -d postgres localstack`. Without the GitHub App variables the server starts in [setup mode](/reference/server-configuration#setup-mode): it runs the migrations, warns that `STACKORDER_SESSION_KEY` is not set, and logs `stackorder server started` with `"setup_mode":true`. Add `STACKORDER_LOG_FORMAT=text` for readable logs.

Back in the first terminal:

```sh
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
curl -s http://localhost:8080/v1/me
go run ./cmd/stackorder-server healthcheck
```

```text
{"status":"ok","version":"dev","setup_mode":true}
{"status":"ok","version":"dev","setup_mode":true}
{"code":"unavailable","message":"setup is required: the GitHub App is not configured yet; open http://localhost:8080/setup"}
stackorder-server healthcheck: GET http://127.0.0.1:8080/healthz: 200 OK
```

Open `http://localhost:8080/setup` in a browser to see the App the server would create: its name (`stackorder-localhost-8080`), webhook URL, permissions and events. Do not submit it for this demo: GitHub could not deliver webhooks to `localhost`.

## 3. Build the CLI {#cli}

```sh
make build-cli
export PATH="$PWD/bin:$PATH"
stackorder version
```

`make build-cli` writes `bin/stackorder`. The CLI needs no server for anything below: without `STACKORDER_SERVER_URL` it runs in local mode.

## 4. Scan the example repository {#graph}

Clone the example next to the checkout:

```sh
git clone https://github.com/stackorder/example-infra.git ../example-infra
cd ../example-infra
stackorder graph
```

```text
6 stacks, 3 modules, 7 edges

stacks/legacy/dns

stacks/prod/apps
  reads_state  stacks/prod/vpc (inferred)

stacks/prod/eks
  depends_on   stacks/prod/vpc
  uses_module  stackorder/example-infra//modules/eks

stacks/prod/vpc
  uses_module  stackorder/example-infra//modules/vpc

stacks/staging/apps
  depends_on   stacks/staging/vpc

stacks/staging/vpc
  uses_module  stackorder/example-infra//modules/vpc

stackorder/example-infra//modules/common (local module)

stackorder/example-infra//modules/eks (local module)
  uses_module  stackorder/example-infra//modules/common

stackorder/example-infra//modules/vpc (local module)

warning: stacks/legacy/dns: inferred reads_state edge to stacks/prod/vpc suppressed by ignore_inferred
```

Module keys start with `owner/repo`, taken from the `origin` remote. `stacks/prod/apps` has no `.stackorder.yaml`: its edge exists only because its `terraform_remote_state` block reads `prod/vpc.tfstate` in `stackorder-example-state`, the backend of `stacks/prod/vpc`. `stacks/legacy/dns` reads the same state but suppresses the edge with `ignore_inferred`, which the scan reports as a warning.

```sh
stackorder graph --format dot | dot -Tsvg > graph.svg
stackorder graph --format json | jq '.edges[] | select(.inferred)'
```

## 5. See what a change affects {#affected}

`affected` compares committed changes with the merge base of a base ref, so commit the change on a branch:

```sh
git switch -c try-vpc
echo '# touch' >> modules/vpc/main.tf
git commit -am "chore: touch the vpc module"
stackorder affected --base main
echo "exit $?"
```

```text
WAVE  STACK                REASONS      ENVIRONMENT
0     stacks/prod/vpc      module       production
0     stacks/staging/vpc   module       staging
1     stacks/prod/apps     reads_state  production
1     stacks/prod/eks      dependent    production
1     stacks/staging/apps  dependent    staging
exit 2
```

Exit code 2 means stacks are affected. `--format json` prints the full resolution, with each stack's `via` and the plan matrix; `--format dot` highlights the affected stacks with their waves. The [scenarios](#scenarios) below list what other changes affect.

## 6. Plan against LocalStack {#plan}

The stacks' backends name the real AWS bucket and region; nothing in the HCL points at LocalStack. Redirect them from the environment, as the end-to-end setup does:

```sh
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1
export AWS_ENDPOINT_URL_S3=http://s3.localhost.localstack.cloud:4566
export AWS_ENDPOINT_URL_STS=http://localhost:4566
export STACKORDER_BACKEND_CONFIG=use_path_style=true,skip_credentials_validation=true,skip_requesting_account_id=true
```

| Variable | Used by |
| --- | --- |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` | The backend and the remote state reads |
| `AWS_ENDPOINT_URL_S3` | The backend and the remote state reads |
| `AWS_ENDPOINT_URL_STS` | The remote state reads, which call `sts:GetCallerIdentity` |
| `STACKORDER_BACKEND_CONFIG` | The backend only; the CLI passes each item to `init` as `-backend-config` |

`STACKORDER_BACKEND_CONFIG` does not reach the `config` block of a `terraform_remote_state`, so the remote state reads validate credentials against STS and use virtual-hosted addressing. That is why LocalStack serves STS too, and why the S3 endpoint is `s3.localhost.localstack.cloud`, whose public DNS resolves to `127.0.0.1`. Offline, add `127.0.0.1 s3.localhost.localstack.cloud stackorder-example-state.s3.localhost.localstack.cloud` to `/etc/hosts`.

Plan the VPC stack:

```sh
stackorder plan --stack stacks/prod/vpc
echo "exit $?"
```

The CLI runs `init` against LocalStack, `plan`, `show -json`, the example `post-plan` hook, and prints a summary line:

```text
post-plan: stacks/prod/vpc: 4 resources in plan, 4 with changes
level=INFO msg="ran hook" hook=post-plan stack=stacks/prod/vpc
stacks/prod/vpc: 4 to add, 0 to change, 0 to destroy, 0 to replace, 2 output changes
exit 0
```

The plan file and its JSON are in `.stackorder/plans/`, named after the artifact the `plan` action would upload, `stackorder-plan-stacks-prod-vpc-<sha>`. With no server the result is `unconfirmed` and nothing is posted; `--format json` shows the [`StackResult`](/reference/api#stack-result) the CLI would have sent.

## 7. Apply, then plan a dependent {#apply}

`stacks/prod/apps` reads the VPC's state, so it cannot plan until that state exists. `stackorder apply` refuses outside GitHub Actions unless it can take the lock through a configured server, so apply the saved plan with Terraform itself:

```sh
terraform -chdir=stacks/prod/vpc apply \
  "$PWD/.stackorder/plans/stackorder-plan-stacks-prod-vpc-$(git rev-parse HEAD).tfplan"
docker compose -f ../stackorder/docker-compose.yml exec localstack \
  awslocal s3 ls s3://stackorder-example-state --recursive
stackorder plan --stack stacks/prod/apps
```

The CLI's `init` has already configured the backend in `stacks/prod/vpc/.terraform`, so Terraform (or `tofu`) applies the saved plan directly. The bucket now holds `prod/vpc.tfstate`, and the `stacks/prod/apps` plan reads the VPC id and subnets from it.

To see the fail-closed rule, try the CLI's own apply against the setup-mode server:

```sh
STACKORDER_API_KEY=sk_demo stackorder apply --local --stack stacks/prod/vpc --server http://localhost:8080
echo "exit $?"
```

It exits 3: it cannot take the lock through a server that is not configured, so it refuses to apply.

## 8. Check for drift {#drift}

```sh
stackorder drift --stack stacks/prod/vpc;    echo "exit $?"
stackorder drift --stack stacks/staging/vpc; echo "exit $?"
```

The first stack matches its state (`no drift`, exit 0). The second has never been applied, so its plan has changes (`drifted: 4 to add, …`, exit 2), the same code a scheduled drift check reports as drift.

## 9. Clean up {#clean-up}

Stop the server with Ctrl-C, then, from the `stackorder` checkout:

```sh
docker compose down -v
```

`make down` does the same. In `example-infra`, `git clean -fdX` removes the `.stackorder/plans` and `.terraform` directories the demo created.

## The example repository {#example-infra}

`stackorder/example-infra` is a small Terraform monorepo wired for Stackorder. Every resource is a `terraform_data` and no configuration needs a provider, so `init` downloads nothing and the only network traffic is to the state bucket and STS.

| Path | What it shows |
| --- | --- |
| `stackorder.yaml` | `tool: terraform`, environments `stacks/prod/` → `production` and `stacks/staging/` → `staging`, `before_merge` with one approval, `propagate.cross_repo: plan`, a weekday drift schedule with issues |
| `modules/vpc`, `modules/eks`, `modules/common` | Local modules; `modules/eks` calls `../common`, so a change to `common` reaches `stacks/prod/eks` through two `uses_module` edges |
| `stacks/prod/vpc`, `stacks/staging/vpc` | Use `modules/vpc` |
| `stacks/prod/eks` | Uses `modules/eks` and `depends_on: [stacks/prod/vpc]` |
| `stacks/prod/apps` | Reads `prod/vpc.tfstate`, an inferred `reads_state` edge |
| `stacks/staging/apps` | `depends_on: [stacks/staging/vpc]` and `plan_output: summary` |
| `stacks/legacy/dns` | Reads the same state, with `ignore_inferred`; matches no environment prefix, so it applies under `default` |
| `.stackorder/hooks/post-plan.sh` | Prints the resource count from `STACKORDER_PLAN_JSON` |
| `.github/workflows/stackorder-plan.yml`, `stackorder-run.yml` | The two wrappers, reading the repository variables below |

Every stack has an S3 backend in `stackorder-example-state`, region `us-east-1`, with `use_lockfile = true`; the state key is the stack path without `stacks/`, such as `prod/vpc.tfstate`.

### Scenarios {#scenarios}

| Change | Wave 0 | Wave 1 |
| --- | --- | --- |
| `modules/vpc` | `stacks/prod/vpc`, `stacks/staging/vpc` (`module`) | `stacks/prod/apps` (`reads_state`), `stacks/prod/eks` (`dependent`), `stacks/staging/apps` (`dependent`) |
| `stacks/prod/eks` only | `stacks/prod/eks` (`changed`) | |
| `modules/eks` or `modules/common` | `stacks/prod/eks` (`module`) | |
| Any Markdown file, a `README` | nothing | |

`stacks/legacy/dns` is never affected by `modules/vpc`, because its edge is suppressed. An apply of the first scenario is four dispatches: wave 0 for `production` and for `staging`, then wave 1 for each.

### Repository settings for its workflows {#example-settings}

To run the example against a real server, its workflows read these Actions variables:

| Variable | Used by | Value |
| --- | --- | --- |
| `STACKORDER_SERVER_URL` | plan, run | The server's base URL |
| `STACKORDER_PLAN_ROLE_ARN` | plan, run | The read-only plan role, trusted for `pull_request` tokens and for `environment:default` |
| `STACKORDER_APPLY_ROLE_ARN_PROD` | run | Apply role for `stacks/prod/`, trusted for `environment:production` |
| `STACKORDER_APPLY_ROLE_ARN_STAGING` | run | Apply role for `stacks/staging/`, trusted for `environment:staging` |
| `STACKORDER_APPLY_ROLE_ARN_DEFAULT` | run | Apply role for `stacks/legacy/`, trusted for `environment:default` |

The last role shares its subject with the plan and drift jobs; see the warning in [Security hardening](/operations/security-hardening#trust-policies) before copying that layout. The repository also needs the environments `production` and `staging`, branch protection requiring `stackorder/plan` and `stackorder/apply`, and the teams its `CODEOWNERS` names.
