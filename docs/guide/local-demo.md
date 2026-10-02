---
description: 'Run the Stackorder server and CLI on one machine with Docker and Terraform and see the dependency graph, affected stacks, waves, plans, applies and drift.'
---

# Local demo

This page runs Stackorder on one machine, with no GitHub App and no AWS account: Postgres, LocalStack and the server in Docker, the server in setup mode, and the CLI against the [`stackorder/example-infra`](https://github.com/stackorder/example-infra) repository with its state in a LocalStack S3 bucket. The server runs from its published image and the CLI from its release archive, so nothing is built; [From source](#from-source) runs both from a checkout instead. The LocalStack settings are the ones the example repository's end-to-end setup uses.

What it shows: the server starting, migrating and answering in setup mode; the dependency graph of a real monorepo; the affected set and waves of a change; real Terraform plans, applies and drift checks against S3 state, with hooks. What it cannot show: pull request checks, comment commands and dispatched applies, which need a GitHub App and a server GitHub can reach. [Getting started](./getting-started) covers those.

## Before you start {#prerequisites}

- Docker with Compose.
- Terraform or OpenTofu 1.10 or later, since the example stacks use S3-native locking (`use_lockfile = true`). The example's `stackorder.yaml` says `tool: terraform`; with only OpenTofu installed, `export STACKORDER_TOOL=tofu` and use `tofu` where the page says `terraform`.
- git, and optionally `jq` (for the example hook) and Graphviz (for `dot`).

Ports 5432, 4566 and 8080 must be free. Running [from source](#from-source) also needs Go.

## 1. Start Postgres, LocalStack and the server {#services}

From a checkout of `stackorder/stackorder`:

```sh
git clone https://github.com/stackorder/stackorder.git
cd stackorder
docker compose --profile server up -d --wait
```

`docker-compose.yml` starts `postgres:17-alpine` with the user, password and database `stackorder` on port 5432, `localstack/localstack:4.0` with `SERVICES=s3,sts` on port 4566 and, with the `server` profile, the server image `ghcr.io/stackorder/stackorder:latest` on port 8080 once Postgres is healthy. `--wait` returns when all three are `healthy`. `STACKORDER_VERSION` selects another tag of the image.

Create the state bucket the example stacks use:

```sh
docker compose exec localstack awslocal s3 mb s3://stackorder-example-state
```

## 2. Check the server in setup mode {#server}

Without the GitHub App variables the server starts in [setup mode](/reference/server-configuration#setup-mode): it runs the migrations, warns that `STACKORDER_SESSION_KEY` is not set, logs `stackorder server started` with `setup_mode=true`, then logs a `setup_url` line with a one-time [setup token](/reference/server-configuration#setup-token):

```sh
docker compose logs server | grep setup_url
```

Then query its endpoints and run the image's own health check:

```sh
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
curl -s http://localhost:8080/v1/me
docker compose exec server /stackorder-server healthcheck
```

```text
{"status":"ok","version":"0.3.0","setup_mode":true}
{"status":"ok","version":"0.3.0","setup_mode":true}
{"code":"unavailable","message":"setup is required: the GitHub App is not configured yet; open http://localhost:8080/setup with the setup token from the server log"}
stackorder-server healthcheck: GET http://127.0.0.1:8080/healthz: 200 OK
```

The page at `http://localhost:8080/setup` holds the manifest of the App the server would create: its name (`stackorder-localhost-8080`), webhook URL, permissions and events. Without the token it answers `403`. A browser submits the manifest to GitHub as soon as the page loads, so read it with `curl` instead, passing the `setup_url` from the server's log. `-L` follows the redirect that drops the token, and `-b ''` keeps the cookie that replaces it:

```sh
curl -sL -b '' 'http://localhost:8080/setup?token=<token from the log>'
```

If you open it in a browser, do not confirm the App on GitHub: GitHub could not deliver webhooks to `localhost`.

## 3. Install the CLI {#cli}

Download the release archive for your system and the checksums file into `bin/`, check the archive and extract the binary:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
url=https://github.com/stackorder/stackorder/releases/download/v0.3.0
mkdir -p bin && cd bin
curl -fsSLO "$url/stackorder_0.3.0_${os}_${arch}.tar.gz" -O "$url/stackorder_0.3.0_checksums.txt"
grep "_${os}_${arch}.tar.gz" stackorder_0.3.0_checksums.txt | $(command -v sha256sum || echo shasum -a 256) -c -
tar -xzf "stackorder_0.3.0_${os}_${arch}.tar.gz" stackorder
cd ..
export PATH="$PWD/bin:$PATH"
stackorder version
```

The check prints the archive name followed by `OK`. It uses `shasum -a 256` where there is no `sha256sum`, as on older macOS releases. [Installing](/reference/cli#installing) lists the archives for every platform. The CLI needs no server for anything below: without `STACKORDER_SERVER_URL` it runs in local mode.

## 4. Scan the example repository {#graph}

Clone the example next to the checkout:

```sh
git clone https://github.com/stackorder/example-infra.git ../example-infra
cd ../example-infra
stackorder graph
```

```text
level=WARN msg="stacks/legacy/dns: inferred reads_state edge to stacks/prod/vpc suppressed by ignore_inferred"
9 stacks, 3 modules, 8 edges

infra/kyc:production

infra/kyc:staging

infra/registry:shared
  depends_on   infra/kyc:production

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

Module keys start with `owner/repo`, taken from the `origin` remote. `stacks/prod/apps` has no `.stackorder.yaml`: its edge exists only because its `terraform_remote_state` block reads `prod/vpc.tfstate` in `stackorder-example-state`, the backend of `stacks/prod/vpc`. `stacks/legacy/dns` reads the same state but suppresses the edge with `ignore_inferred`, which the scan logs as a warning on standard error and `graph` repeats after the graph. The `infra/` stacks are [instances](/configuration/instances): `infra/kyc` once per var file, `infra/registry` once as `shared`.

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
level=WARN msg="stacks/legacy/dns: inferred reads_state edge to stacks/prod/vpc suppressed by ignore_inferred"
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

The CLI runs `init` against LocalStack, `plan`, `show -json` and the example `post-plan` hook, passing Terraform's own output through, and ends with the hook's line and a summary:

```text
post-plan: stacks/prod/vpc: 4 resources in plan, 4 with changes
level=INFO msg="ran hook" hook=post-plan stack=stacks/prod/vpc
stacks/prod/vpc: 4 to add, 0 to change, 0 to destroy, 0 to replace, 2 output changes
exit 0
```

The plan file and its JSON are in `.stackorder/plans/` at the repository root, named after the artifact the `plan` action would upload, `stackorder-plan-stacks-prod-vpc-69df0ef0-<sha>`. `example-infra` ignores that directory; in your own repository, add `.stackorder/plans/` to `.gitignore` so plan files, which hold every value in clear text, are never committed. With no server the result is `unconfirmed` and nothing is posted; `--format json` shows the [`StackResult`](/reference/api#stack-result) the CLI would have sent.

## 7. Apply, then plan a dependent {#apply}

`stacks/prod/apps` reads the VPC's state, so it cannot plan until that state exists. `stackorder apply` refuses outside GitHub Actions unless it can take the lock through a configured server, so apply the saved plan with Terraform itself:

```sh
terraform -chdir=stacks/prod/vpc apply \
  "$PWD/.stackorder/plans/stackorder-plan-stacks-prod-vpc-69df0ef0-$(git rev-parse HEAD).tfplan"
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

From the `stackorder` checkout:

```sh
docker compose --profile server down -v
```

`make down` does the same. In `example-infra`, `git clean -fdX` removes the `.stackorder/plans` and `.terraform` directories the demo created.

## From source {#from-source}

Contributors run the server and the CLI from the checkout instead. This needs Go, the version in `go.mod`, which Go downloads with `GOTOOLCHAIN=auto`:

```sh
export GOTOOLCHAIN=auto
make dev
```

`make dev` takes the place of the `docker compose` command of step 1: it starts Postgres and LocalStack, waits until they are healthy and runs the server with `go run ./cmd/stackorder-server` in the foreground, logging JSON to the terminal, `setup_url` line included, until Ctrl-C. Run the rest from a second terminal. In step 2, `go run ./cmd/stackorder-server healthcheck` replaces the `docker compose exec` line, and `/healthz` reports `"version":"dev"`. In step 3, `make build-cli` writes `bin/stackorder` instead of the download.

To build the image from the checkout and run it under Compose, add `--build` to the command of step 1. The image built replaces the published one under the same tag until `docker compose pull server`.

## The example repository {#example-infra}

`stackorder/example-infra` is a small Terraform monorepo wired for Stackorder. Every resource is a `terraform_data` and no configuration needs a provider, so `init` downloads nothing and the only network traffic is to the state bucket and STS.

| Path | What it shows |
| --- | --- |
| `stackorder.yaml` | `tool: terraform`, stacks discovered under `stacks/**` and `infra/**` with an instance per `workspaces/*.tfvars.json`, `env` setting `TF_VAR_environment` and `TF_VAR_role`, environments `stacks/prod/` → `production` and `stacks/staging/` → `staging`, `before_merge` with one approval, `propagate.cross_repo: plan`, a weekday drift schedule with issues |
| `modules/vpc`, `modules/eks`, `modules/common` | Local modules; `modules/eks` calls `../common`, so a change to `common` reaches `stacks/prod/eks` through two `uses_module` edges |
| `stacks/prod/vpc`, `stacks/staging/vpc` | Use `modules/vpc` |
| `stacks/prod/eks` | Uses `modules/eks` and `depends_on: [stacks/prod/vpc]` |
| `stacks/prod/apps` | Reads `prod/vpc.tfstate`, an inferred `reads_state` edge |
| `stacks/staging/apps` | `depends_on: [stacks/staging/vpc]` and `plan_output: summary` |
| `stacks/legacy/dns` | Reads the same state, with `ignore_inferred`; matches no environment prefix, so it applies under `default` |
| `infra/kyc` | Two [instances](/configuration/instances) from its var files, `infra/kyc:production` and `infra/kyc:staging`, each under the GitHub environment of its name |
| `infra/registry` | One declared instance, `infra/registry:shared`, with `depends_on: ["infra/kyc:production"]` |
| `infra/state.s3.tfbackend` | The bucket, region and locking of the `infra/` backends, passed to `init` with each instance's state key |
| `.stackorder/hooks/post-plan.sh` | Prints the resource count from `STACKORDER_PLAN_JSON` |
| `.github/workflows/stackorder-plan.yml`, `stackorder-run.yml` | The two wrappers, reading the repository variables below |

Every stack has an S3 backend in `stackorder-example-state`, region `us-east-1`, with `use_lockfile = true`; the state key of a `stacks/` stack is its path without `stacks/`, such as `prod/vpc.tfstate`, and that of an `infra/` instance is its directory without `infra/` and its instance, such as `kyc/production.tfstate`.

### Scenarios {#scenarios}

| Change | Wave 0 | Wave 1 |
| --- | --- | --- |
| `modules/vpc` | `stacks/prod/vpc`, `stacks/staging/vpc` (`module`) | `stacks/prod/apps` (`reads_state`), `stacks/prod/eks` (`dependent`), `stacks/staging/apps` (`dependent`) |
| `stacks/prod/eks` only | `stacks/prod/eks` (`changed`) | |
| `modules/eks` or `modules/common` | `stacks/prod/eks` (`module`) | |
| `infra/kyc` or one of its var files | `infra/kyc:production`, `infra/kyc:staging` (`changed`) | `infra/registry:shared` (`dependent`) |
| `infra/state.s3.tfbackend` | `infra/kyc:production`, `infra/kyc:staging` (`watch_path`) | `infra/registry:shared` (`watch_path`, `dependent`) |
| Any Markdown file, a `README` | nothing | |

`stacks/legacy/dns` is never affected by `modules/vpc`, because its edge is suppressed. An apply of the first scenario is four dispatches: wave 0 for `production` and for `staging`, then wave 1 for each.

### Repository settings for its workflows {#example-settings}

To run the example against a real server, its workflows read these Actions variables:

| Variable | Used by | Value |
| --- | --- | --- |
| `STACKORDER_SERVER_URL` | plan, run | The server's base URL |
| `STACKORDER_PLAN_ROLE_ARN` | plan, run | The read-only plan role, trusted for `pull_request` tokens and for `environment:default` |
| `STACKORDER_APPLY_ROLE_ARN_PROD` | run | Apply role for `stacks/prod/` and the `:production` instances, trusted for `environment:production` |
| `STACKORDER_APPLY_ROLE_ARN_STAGING` | run | Apply role for `stacks/staging/` and the `:staging` instances, trusted for `environment:staging` |
| `STACKORDER_APPLY_ROLE_ARN_DEFAULT` | run | Apply role for `stacks/legacy/` and the `:shared` instances, trusted for `environment:default` and `environment:shared` |

The last role shares its subject with the plan and drift jobs; see the warning in [Security hardening](/operations/security-hardening#trust-policies) before copying that layout. The repository also needs the environments `production`, `staging` and `shared`, branch protection requiring `stackorder/plan` and `stackorder/apply`, and the teams its `CODEOWNERS` names.
