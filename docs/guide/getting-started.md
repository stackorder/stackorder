---
title: Getting started with Stackorder
description: 'Set up Stackorder on one repository: deploy the server, create the GitHub App, add plan and apply IAM roles, stackorder.yaml and two workflows, then apply.'
---

# Getting started

This guide takes one repository from nothing to a first `stackorder apply`. You deploy the server, create the GitHub App, create two kinds of AWS role, and add three files to the repository.

## Before you start

You need:

- a GitHub organization or personal account where you can create and install GitHub Apps;
- an AWS account with an S3 bucket for Terraform or OpenTofu state;
- somewhere to run one container behind a public HTTPS URL, and a Postgres database;
- Terraform or OpenTofu 1.10 or later if you want S3-native state locking with `use_lockfile`.

The examples use the organization `acme`, the repository `acme/infra`, AWS account `123456789012` and the server URL `https://stackorder.example.com`. On a personal account, read your user name for `acme`. A personal account has no teams, and a private repository on GitHub Free has no environment protection or branch protection; [Personal accounts and GitHub Free](/configuration/environments-and-authorization#free-plan) lists what changes. The repository looks like this:

```text
acme/infra
├── stackorder.yaml
├── .github/workflows/
│   ├── stackorder-plan.yml
│   └── stackorder-run.yml
├── modules/
│   └── vpc/
└── stacks/
    ├── prod/
    │   ├── vpc/
    │   └── apps/
    │       └── .stackorder.yaml
    └── staging/
        └── vpc/
```

Each stack directory has a `backend "s3"` block. That block is what makes a directory under `stacks/**` a stack, and it is where the CLI reads the state location from.

```hcl
terraform {
  backend "s3" {
    bucket       = "acme-terraform-state"
    key          = "stacks/prod/vpc/terraform.tfstate"
    region       = "us-east-1"
    use_lockfile = true
  }
}
```

## 1. Deploy the server {#deploy}

The server is one container and one Postgres database. It listens on port 8080; put TLS in front of it with a load balancer or a reverse proxy. GitHub must be able to reach `https://stackorder.example.com/webhooks/github`.

Generate a session key once and keep it. It signs session cookies, so changing it signs everyone out.

```sh
openssl rand -hex 32 > session.key
```

### Option A: Docker

```sh
docker run -d --name stackorder -p 8080:8080 \
  -e DATABASE_URL='postgres://stackorder:change-me@db.internal:5432/stackorder?sslmode=require' \
  -e STACKORDER_BASE_URL='https://stackorder.example.com' \
  -e STACKORDER_SESSION_KEY="$(cat session.key)" \
  ghcr.io/stackorder/stackorder:latest
```

The server refuses to start without `DATABASE_URL` and `STACKORDER_BASE_URL`. It runs its database migrations at start-up. Without the GitHub App variables it starts in **setup mode** and serves only `/setup`, `/healthz` and `/readyz`.

```sh
curl -fsS https://stackorder.example.com/readyz
```

[Deploy as a container](/operations/deploy-container) covers Compose, other platforms and Postgres requirements.

### Option B: the Terraform module

The repository ships a Terraform module in `deploy/terraform` that creates an ECS Fargate service behind an ALB with an ACM certificate, an RDS Postgres instance, and the secrets wiring. See [Deploy on AWS](/operations/deploy-aws).

## 2. Create the GitHub App {#create-app}

At start-up the server logs a `setup_url` line: the setup page's URL with a one-time [setup token](/reference/server-configuration#setup-token). Take it from the container's log:

```sh
docker logs stackorder 2>&1 | grep setup_url
```

On AWS, read it from the task logs as [Deploy on AWS](/operations/deploy-aws#first-deploy) shows. Open that URL in a browser. `/setup` without the token answers `403`, so nobody else who reaches the server can create the App under their own account.

The page renders a GitHub App manifest with the webhook URL, [permissions](/reference/github-app#permissions) and [events](/reference/github-app#events) already filled in, and posts it to GitHub. Confirm the App on GitHub. GitHub returns the App id, private key, webhook secret and OAuth client id and secret in one exchange, and the page prints them **once** as environment variables:

| Variable | Holds |
| --- | --- |
| `GITHUB_APP_ID` | The App id |
| `GITHUB_APP_PRIVATE_KEY` | The App private key (PEM) |
| `GITHUB_WEBHOOK_SECRET` | The secret GitHub signs webhooks with |
| `GITHUB_OAUTH_CLIENT_ID` | The client id for human sign-in |
| `GITHUB_OAUTH_CLIENT_SECRET` | The client secret for human sign-in |

Store them in your secret store straight away; the page does not show them again. Then restart the server with them. With Docker, keep the single-line values in an env file and pass the key separately:

```sh
docker rm -f stackorder
docker run -d --name stackorder -p 8080:8080 \
  --env-file stackorder.env \
  -e GITHUB_APP_PRIVATE_KEY="$(cat stackorder-app.pem)" \
  ghcr.io/stackorder/stackorder:latest
```

Here `stackorder.env` holds `DATABASE_URL`, `STACKORDER_BASE_URL`, `STACKORDER_SESSION_KEY`, `GITHUB_APP_ID`, `GITHUB_WEBHOOK_SECRET`, `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET`.

For GitHub Enterprise Server, set `GITHUB_API_URL`, `GITHUB_WEB_URL` and `GITHUB_OIDC_ISSUER` before opening `/setup`: the page sends the manifest to `GITHUB_WEB_URL`, which is github.com unless you set it. The flow is otherwise the same. See [Server configuration](/reference/server-configuration#ghes).

## 3. Install the App {#install}

On GitHub, open the App's settings page and choose **Install App**. Install it on the `acme` organization, or on your personal account, and select:

- the repositories that hold stacks, such as `acme/infra`;
- repositories that hold shared git modules, so the server records their version tags;
- repositories named by cross-repo `depends_on` entries.

In the App's settings, on the **General** tab under **Display information**, choose **Upload a logo** and upload the Stackorder logo from `https://stackorder.example.com/setup/logo.png`. Until the App has a logo, GitHub shows the avatar of the account that owns it on its comments and checks. See [Logo](/reference/github-app#logo).

Then sign in at `https://stackorder.example.com` with GitHub. The UI shows the repositories of the accounts where the App is installed.

## 4. Create the AWS roles {#aws-roles}

Stackorder never holds AWS credentials. Each job assumes a role with its own GitHub OIDC token. You create a **plan role**, for pull request plans and for the plans and drift checks the server dispatches, and one **apply role per environment**.

If the account has no GitHub OIDC provider yet, create it:

```sh
aws iam create-open-id-connect-provider \
  --url https://token.actions.githubusercontent.com \
  --client-id-list sts.amazonaws.com
```

The trust policies below match the subject GitHub puts in each job's token. The subject starts with a prefix that names the repository, and a policy written with the wrong prefix matches no token. Read the prefix first:

```sh
gh api repos/acme/infra/actions/oidc/customization/sub
```

A repository created after July 15, 2026 answers with an immutable prefix that carries the owner and repository ids, such as `"sub_claim_prefix":"repo:acme@123456/infra@456789"`. Write that prefix wherever the policies below say `repo:acme/infra`. See [Immutable subjects](/operations/security-hardening#immutable-subjects).

### The plan role

Trusted by two kinds of job of `acme/infra`: pull request plan jobs, whose token subject is `repo:acme/infra:pull_request`, and the plan and drift jobs the server dispatches to `stackorder-run.yml`, which always run under the environment `default` and so carry `repo:acme/infra:environment:default`. Give it read access to state and the read-only permissions your providers need to plan. `plan` takes the state lock by default, so it also needs to write the lock: the `<key>.tflock` object with `use_lockfile`, or the DynamoDB table.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": [
            "repo:acme/infra:pull_request",
            "repo:acme/infra:environment:default"
          ]
        }
      }
    }
  ]
}
```

### The apply role

Trusted only by jobs of `acme/infra` that run under the `production` GitHub environment. GitHub puts `environment:production` in the token's subject only when the job actually ran under that environment, which means after its protection rules passed. Give this role the write permissions your stacks need.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": "repo:acme/infra:environment:production"
        }
      }
    }
  ]
}
```

Create a second apply role for `staging` the same way, with `environment:staging`. Only apply jobs of `stackorder-run.yml` use these roles, through `aws-role-arn-map`; plans requested with a `stackorder plan` comment and scheduled drift checks use the plan role, passed as `aws-plan-role-arn`.

To pin the roles to the canonical reusable workflow as well, see [the AWS trust policy layer](/configuration/environments-and-authorization#layer-5).

## 5. Create the GitHub environments {#environments}

In the repository settings, under **Environments**, create `production`:

- **Required reviewers**: a team, such as `acme/platform-prod`, or on a personal account, the users who may approve.
- **Prevent self-review**: on, so the requester cannot approve their own deployment.
- **Deployment branches**: the default branch only. Server-dispatched runs start from the default branch and check out the commit they are given.

Create `staging` the same way, with fewer or no reviewers. The environment `default`, which GitHub creates on first use with no protection rules, is where the server's plan and drift dispatches run, and where stacks without an instance that match no prefix apply. Give it no reviewers, or every `stackorder plan` comment waits for an approval.

Required reviewers on private repositories need GitHub Enterprise. [Environments and authorization](/configuration/environments-and-authorization) covers the alternatives.

A private repository on GitHub Free has none of these settings: GitHub still creates each environment on first use, with no protection rules. For that case, and for a repository owned by a personal account, see [Personal accounts and GitHub Free](/configuration/environments-and-authorization#free-plan).

## 6. Add `stackorder.yaml` {#stackorder-yaml}

At the repository root:

```yaml
version: 1

tool: tofu
tool_version: "1.12.6"

environments:
  "stacks/prod/": production
  "stacks/staging/": staging

apply:
  require_approvals: 1
```

If you work alone, set `require_approvals: 0`: nobody else can approve your pull requests, and your own review never counts. See [Settings for a single owner](/configuration/environments-and-authorization#single-owner).

Everything else keeps its default: stacks under `stacks/**`, `modules/**` treated as modules rather than stacks, applies before merge, dependents propagated. The full list is on the [`stackorder.yaml` page](/configuration/stackorder-yaml). If your stacks are top-level directories rather than under `stacks/`, see [Layouts](/configuration/stackorder-yaml#layouts).

Declare dependencies between stacks in the dependent stack's directory. In `stacks/prod/apps/.stackorder.yaml`:

```yaml
depends_on:
  - stacks/prod/vpc
```

## 7. Add the workflow files {#workflows}

`.github/workflows/stackorder-plan.yml` runs on every pull request push:

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
    permissions:
      id-token: write
      contents: read
      actions: read
      checks: write
      pull-requests: read
    uses: stackorder/actions/.github/workflows/plan.yml@v1
    with:
      server-url: ${{ vars.STACKORDER_SERVER_URL }}
      aws-role-arn: arn:aws:iam::123456789012:role/stackorder-plan
      tool: tofu
```

`.github/workflows/stackorder-run.yml` is dispatched by the server only:

```yaml
name: stackorder run
run-name: stackorder ${{ inputs.mode }} ${{ inputs.run_id }} wave ${{ inputs.wave }}
on:
  workflow_dispatch:
    inputs:
      run_id: { type: string, required: true }
      mode: { type: string, required: true }
      wave: { type: string, required: false }
      sha: { type: string, required: false }
      stacks: { type: string, required: true }
jobs:
  run:
    permissions:
      id-token: write
      contents: read
      actions: read
      checks: write
    uses: stackorder/actions/.github/workflows/run.yml@v1
    with:
      server-url: ${{ vars.STACKORDER_SERVER_URL }}
      run-id: ${{ inputs.run_id }}
      mode: ${{ inputs.mode }}
      wave: ${{ inputs.wave }}
      sha: ${{ inputs.sha }}
      stacks: ${{ inputs.stacks }}
      aws-plan-role-arn: arn:aws:iam::123456789012:role/stackorder-plan
      aws-role-arn-map: '{"stacks/prod/": "arn:aws:iam::123456789012:role/stackorder-apply-prod", "stacks/staging/": "arn:aws:iam::123456789012:role/stackorder-apply-staging"}'
```

The `run-name` line lets the server recognise the workflow runs it dispatched, and the five inputs must all be declared, because the server sends all five. The `permissions` blocks matter: a called workflow can only narrow the permissions its caller grants. [Workflows](/configuration/workflows) explains each input and permission.

Both reusable workflows require `server-url`, the server's base URL. The files above read it from the Actions variable `STACKORDER_SERVER_URL`; if it is empty, the CLI runs in local mode and every check ends up `unconfirmed`. In an organization, set it once for every repository:

```sh
gh variable set STACKORDER_SERVER_URL --org acme --visibility all \
  --body https://stackorder.example.com
```

A personal account has no organization variables, so set it on the repository instead. The same command sets it for a single repository in an organization, and a repository variable takes precedence over an organization variable of the same name:

```sh
gh variable set STACKORDER_SERVER_URL --repo acme/infra \
  --body https://stackorder.example.com
```

AWS credentials come from the roles of step 4. If a provider needs another credential, such as a Cloudflare API token, pass it through the `env` secret of the reusable workflows: `secrets: inherit` passes nothing to them, since they belong to the `stackorder` organization, not to yours. Store a read-only token for plans as a repository secret, `STACKORDER_ENV` with the value `CLOUDFLARE_API_TOKEN=<token>`, and add to both files:

```yaml
    secrets:
      env: ${{ secrets.STACKORDER_ENV }}
```

A token that can change infrastructure goes in an environment secret named `ENV` on `production`, which replaces the repository secret for applies under that environment. A private repository on GitHub Free has no environment secrets, so keep its provider tokens read-only; see [Personal accounts and GitHub Free](/configuration/environments-and-authorization#free-plan). See [Provider credentials](/configuration/workflows#env).

## 8. Protect the default branch {#branch-protection}

Add a branch protection rule or ruleset on the default branch that requires:

- the status checks `stackorder/plan` and `stackorder/apply` (in `on_merge` mode, only `stackorder/plan`, since the apply runs after the merge);
- at least the number of approvals set in `apply.require_approvals`;
- review from code owners, if you use `CODEOWNERS`.

Stackorder only reports checks. GitHub enforces the merge.

Private repositories on GitHub Free have neither branch protection nor rulesets, so nothing but write permission guards the merge. See [Personal accounts and GitHub Free](/configuration/environments-and-authorization#free-plan).

## 9. Open a pull request {#first-pr}

Change a file in `stacks/prod/vpc` and open a pull request. Once the plan workflow finishes you see:

- a `stackorder/resolve` check listing the affected stacks and their waves;
- one check per stack, `stackorder/plan: stacks/prod/vpc` and `stackorder/plan: stacks/prod/apps`, the second one because it depends on the first;
- the roll-up check `stackorder/plan`;
- one sticky comment with a collapsible plan summary per stack.

Every new push re-plans and replaces the previous results.

## 10. Apply {#first-apply}

Approve the pull request, then comment:

```text
stackorder apply
```

The App adds an eyes reaction when it receives the command and a rocket reaction when it dispatches, so a dropped command is visible. If the [apply gate](/guide/how-it-works#apply-gate) refuses, a comment names the failing check and the reason.

Otherwise the server locks both stacks and dispatches wave 0, `stacks/prod/vpc`, to `stackorder-run.yml` under the `production` environment. Approve the deployment in the Actions UI; the sticky comment links straight to it. When wave 0 is green the server dispatches wave 1, `stacks/prod/apps`, which asks for approval again. A green last wave turns `stackorder/apply` green.

Merge the pull request. The server releases the locks.

## Try the CLI locally {#local-cli}

The same CLI runs on a laptop. Download a release for your platform and verify it against the checksum file:

```sh
gh release download --repo stackorder/stackorder \
  --pattern 'stackorder_*_linux_amd64.tar.gz' \
  --pattern 'stackorder_*_checksums.txt'
sha256sum --check --ignore-missing stackorder_*_checksums.txt
tar -xzf stackorder_*_linux_amd64.tar.gz stackorder
```

Release archives exist for `linux`, `darwin` and `windows` (as `.zip`) on `amd64` and `arm64`. Then, from the repository root:

```sh
stackorder affected --base main
stackorder graph --format dot | dot -Tsvg > graph.svg
```

The [local demo](./local-demo) runs the whole thing on one machine, with Postgres and LocalStack in Docker and the example repository.

## Next steps

- [Concepts](./concepts): edges, the affected set, waves and locks in detail.
- [Environments and authorization](/configuration/environments-and-authorization): the five layers that gate an apply.
- [Drift detection](/configuration/drift): scheduled plans on the default branch.
- [Troubleshooting](/operations/troubleshooting): what to do when a check says `unconfirmed`.
