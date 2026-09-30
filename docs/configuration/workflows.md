---
description: 'The two GitHub Actions workflow files Stackorder needs, stackorder-plan.yml and stackorder-run.yml: triggers, inputs, AWS roles, permissions and hooks.'
---

# Workflows

A repository has two workflow files. Each is a short wrapper that calls a reusable workflow from `stackorder/actions`. The plan workflow runs on every pull request push; the run workflow runs only when the server dispatches it.

## `stackorder-plan.yml` {#plan}

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

- **Trigger.** `pull_request` on `opened`, `synchronize` and `reopened`. Plans never wait for the server: GitHub starts them.
- **Concurrency.** One group per PR with `cancel-in-progress: true`, so a new push cancels the plan of the previous one.
- **Jobs.** The reusable `plan.yml` runs a `resolve` job, which checks out the full history, scans the repository, posts the graph and gets the matrix back, and a `plan` job with one matrix entry per affected stack, which checks out the entry's `sha`. Plan jobs declare no `environment`, so planning is never held behind an environment's reviewers. For a pull request from a fork it runs neither; see [Fork pull requests](#forks).

## `stackorder-run.yml` {#run}

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

The server dispatches this workflow by its file name, `stackorder-run.yml`, on the default branch, so keep that name and keep the file on the default branch. It sends all five inputs, `run_id`, `mode`, `wave`, `sha` and `stacks`, so the file must declare all five: GitHub refuses a dispatch with an input the workflow does not declare, and the server then records a warning on the run naming the inputs it expects.

The `run-name` line matters. The server recognises the workflow runs it dispatched by their title, `stackorder <mode> <run id> wave <n>`, and binds each one to its dispatch as soon as GitHub reports it. Without it, a dispatch is bound only when one of its jobs first calls the server, which is too late in two cases:

- An App registered as a [deployment protection rule](/configuration/environments-and-authorization#layer-4) rejects the deployment, because GitHub asks it before any job has run.
- A dispatch that no job has called within 30 minutes, such as an apply waiting for an environment's reviewers, is taken for lost: its stacks become `unknown` and the run gets the warning `workflow run not found`.

The file has no `concurrency` group and is never cancelled in progress. Inside `run.yml`, each stack's job joins the concurrency group `stackorder-stack-<key>` without `cancel-in-progress`, so two jobs never run on the same stack at once; keep your own concurrency groups clear of that prefix.

| Dispatch input | Meaning |
| --- | --- |
| `run_id` | The Stackorder run id, a UUID. |
| `mode` | `plan`, `apply` or `drift`. Any other value fails the job before checkout. |
| `wave` | The wave index this dispatch covers, `0` for plan and drift dispatches. |
| `sha` | The commit the run is for, which each job checks out: the pull request head for a pull request's plans and `before_merge` applies, the merge commit for an `on_merge` apply, the head of the default branch for drift checks and cross-repository plans. |
| `stacks` | A JSON array of [matrix entries](/reference/actions#matrix-entry), one per stack. Each entry carries the stack's key, directory and instance, its GitHub environment, tool and, for applies, the workflow run and artifact that hold its plan file. |

### Which environment and role a job gets {#environments}

| Mode | Dispatched for | Environment | AWS role |
| --- | --- | --- | --- |
| `apply` | A `stackorder apply` comment, a merge in `on_merge` mode | The stack's environment: from its instance override or `.stackorder.yaml`, else the `environments` map, else its instance name, else `default` | The first match in `aws-role-arn-map` (exact key, then `:instance`, then the longest prefix), else `aws-role-arn` |
| `plan` | A `stackorder plan` comment, a re-run from the UI or the checks tab, a cross-repository plan | Always `default` | `aws-plan-role-arn`, else `aws-role-arn` |
| `drift` | The drift schedule | Always `default` | `aws-plan-role-arn`, else `aws-role-arn` |

An apply is dispatched once per wave and environment, at most `apply.max_parallel` stacks per dispatch. Each job declares `environment: ${{ matrix.environment }}`, so that environment's protection rules gate the job and its OIDC token carries `environment:<name>` in its subject. Plan and drift dispatches run under `default` so that an environment with required reviewers never holds a read-only job, and so that the plan role's trust policy covers them: see [Security hardening](/operations/security-hardening#trust-policies).

## Reusable workflow inputs {#inputs}

| Input | Workflows | Default | Meaning |
| --- | --- | --- | --- |
| `server-url` | both | required | The server's base URL. The CLI also requests it as the OIDC audience, so it must equal the server's `STACKORDER_OIDC_AUDIENCE`, the base URL by default. |
| `aws-role-arn` | both | empty | The IAM role for stacks that match nothing in `aws-role-arn-map`, and for plan and drift dispatches when `aws-plan-role-arn` is empty. |
| `aws-role-arn-map` | both | empty | A JSON object from key to IAM role ARN. A key is a path prefix (`stacks/prod/`), an exact stack key (`infra/network:production`) or an instance in any directory (`:production`). The exact key wins, then `:instance`, then the longest prefix; a key with `:` is never a prefix. In `run.yml` only apply jobs use it. |
| `aws-plan-role-arn` | `run.yml` | empty | The IAM role for plan and drift dispatches, which run under the `default` environment. |
| `aws-role-session-name` | both | empty | The AWS role session name: a name, or a JSON object with `plan`, `apply` and `drift` keys, `drift` falling back to `plan`; `plan.yml` uses `plan`. Every character outside `[A-Za-z0-9_+=,.@-]` becomes `-` and the name is cut at 64 characters. Empty keeps the credentials action's default, `GitHubActions`. |
| `aws-region` | both | `us-east-1` | The AWS region for the credentials. |
| `tool` | both | `terraform` | `terraform` or `tofu`, for stacks whose matrix entry names no tool. |
| `tool-version` | both | `latest` | The tool version to install, for stacks whose matrix entry pins none. |
| `stackorder-version` | both | `latest` | The `stackorder` CLI release to install: `1.2.3`, `v1.2.3` or `latest`. `plan.yml` resolves `latest` once, in the resolve job. |
| `runner` | both | `ubuntu-latest` | The runner label jobs run on, or a JSON array or object for `runs-on`. |
| `max-parallel` | both | `6` | The most matrix jobs that run at once. |
| `working-directory` | both | `.` | The directory the jobs run the CLI in, relative to the repository root. |
| `base-ref` | `plan.yml` | empty | The ref to diff against; empty uses the pull request base. |
| `stacks` | `plan.yml` | empty | Comma separated stack keys to restrict the plan to. |
| `env` | both | empty | Environment variables that are not secret, as `KEY=VALUE` lines or `KEY<<DELIMITER` multi-line values. See [Provider credentials](#env). |
| `run-id`, `mode`, `wave`, `sha`, `stacks` | `run.yml` | | The dispatch inputs, passed through. `run-id`, `mode` and `stacks` are required. |

Instances select their apply role with the key forms of `aws-role-arn-map`, and name their AWS session with `aws-role-session-name`; see [AWS roles](./instances#aws-roles). When no role applies to a stack, the job logs a notice and skips AWS credentials, which suits self-hosted runners with an instance role. Reading `aws-role-arn-map` needs `jq` on the runner.

Both workflows also declare one optional secret, `env`, for environment variables whose values are secret, in the syntax of the `env` input. See [Provider credentials](#env).

Every job installs the stack's tool with `hashicorp/setup-terraform@v3` or `opentofu/setup-opentofu@v1` (wrapper disabled), sets `STACKORDER_TOOL` to it, installs `stackorder` with the [`setup` action](/reference/actions#setup), and caches providers in `$RUNNER_TEMP/terraform-plugin-cache` with `actions/cache@v4`, keyed on the stack's `.terraform.lock.hcl`.

The files above read `server-url` from the Actions variable `STACKORDER_SERVER_URL`. Set it for the organization or the repository. If it is empty, the CLI runs in local mode: plans are `unconfirmed` and applies are refused.

## Provider credentials and other environment variables {#env}

AWS credentials come from the OIDC role. A provider that needs another credential, such as a Cloudflare API token, gets it from the `env` secret of the reusable workflows. The [`env` key](./instances#env) of `stackorder.yaml` is not the place: its values are literal and committed to the repository.

The `env` input and the `env` secret take the syntax of `$GITHUB_ENV`: one `KEY=VALUE` per line, or a multi-line value such as a PEM key between `KEY<<DELIMITER` and a line holding only `DELIMITER`. Put values that are not secret, such as an account id or `TF_LOG`, in the input and credentials in the secret.

```yaml
    with:
      env: |
        CLOUDFLARE_ACCOUNT_ID=0123456789abcdef0123456789abcdef
    secrets:
      env: ${{ secrets.STACKORDER_ENV }}
```

Here the repository secret `STACKORDER_ENV` holds `CLOUDFLARE_API_TOKEN=<token>`, and more lines for more variables.

- **Where they apply.** The plan jobs of `plan.yml` and the jobs of `run.yml` export both, the input first, right after installing `stackorder` and before the AWS credentials step. Every later step sees them: the credentials step, the CLI, Terraform or OpenTofu and the [hooks](#hooks). The `resolve` job never gets them. For a name in both, the secret's value wins, and a variable from a stack's `env` key wins over both for the processes the CLI runs.
- **Masking.** Every line of every value of the secret is registered with `::add-mask::` before anything is exported, so the job log shows `***` instead. Keep values that are not secret in the input: a short value such as `1` or `true` in the secret would be replaced wherever it appears in the log. What the CLI sends to the server is redacted by variable name, not by this mask: give a secret variable a name with a `TOKEN`, `SECRET`, `PASSWORD` or `API_KEY` component, or mark the Terraform variable `sensitive` (see [Secrets](/reference/cli#secrets)).
- **Reserved names.** Names starting with `GITHUB_`, `RUNNER_`, `ACTIONS_`, `STACKORDER_` or `LD_`, and `PATH`, `HOME`, `NODE_OPTIONS`, `BASH_ENV`, `BASHOPTS`, `SHELLOPTS` and `PS4`, are refused in any letter case: they would redirect the runner, the later steps or the CLI. A reserved name fails the job with an error naming it; a malformed line fails it with the line's number, never its text. Nothing is exported when there is an error. The list guards against mistakes, not against the stack's own code, which runs in the same job.
- **Set later.** When the job assumes a role, the credentials step sets `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION` and `AWS_DEFAULT_REGION` after the export, replacing any of the same name. Without a role the step is skipped and those names keep their `env` values. The job sets `TF_PLUGIN_CACHE_DIR` after the export too. Other `AWS_` variables, such as `AWS_PROFILE`, stay set and change how the AWS SDKs and the S3 backend find credentials.

### Passing the secret {#env-secret}

`secrets: inherit` passes nothing to these workflows. GitHub passes inherited secrets only to a reusable workflow in the caller's own organization or enterprise, and `stackorder/actions` is in the `stackorder` organization, so pass the secret by name as above. The files on this page leave `secrets: inherit` out for that reason.

GitHub also gives a called job that declares a GitHub environment that environment's secret in place of the caller's secret of the same name. The jobs of `run.yml` declare the stack's environment, so an environment secret named `ENV`, when it exists, replaces the whole `env` secret, without merging, for the jobs under that environment: applies under `production` read the `ENV` secret of `production`, and plan and drift dispatches, which run under `default`, read the one of `default`. Plan jobs of `plan.yml` declare no environment and get only what the calling workflow passes.

The environment secret takes effect only for a secret the caller passes, so pass `env` even when `STACKORDER_ENV` is unset. Without it, the job gets an empty `env` secret. This is the runner's observed behaviour, not a guarantee: GitHub's documentation does not cover it.

### What a plan may hold {#env-plans}

A pull request plan runs the pull request's code, and so does a plan the server dispatches for a pull request. That code can read every variable of its job and send it anywhere, and a pull request can edit its own calling workflow to pass any repository or organization secret. Treat provider tokens like the AWS roles:

- Pass plans a read-only token, from a repository secret or the `ENV` secret of `default`. The `ENV` secret of `default` replaces the passed secret for dispatched plans and drift checks, when it exists, so keep it read-only too.
- Keep a token that can change infrastructure only in the `ENV` secret of the environments whose protection rules gate applies. The job gets it after those rules pass. A private repository on GitHub Free has no environment secrets, so keep every token it passes through `env` read-only; see [Personal accounts and GitHub Free](/configuration/environments-and-authorization#free-plan).

A credential read by the provider from its own environment variable, such as `CLOUDFLARE_API_TOKEN`, never reaches the plan file. A credential passed as a Terraform variable through `TF_VAR_` does, unless the variable is [ephemeral](./instances#ephemeral): the saved plan stores the value, the `plan` action uploads the plan file as a workflow artifact that anyone who can read the repository can download, and the apply reuses the plan-time value. Declare such a variable `ephemeral`:

```hcl
variable "cloudflare_api_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

provider "cloudflare" {
  api_token = var.cloudflare_api_token
}
```

The secret then holds `TF_VAR_cloudflare_api_token=<token>`.

## Permissions {#permissions}

The reusable workflows set `permissions: {}` at the top and the minimum on each job:

| Workflow | Job | `id-token` | `contents` | `checks` | `pull-requests` | `actions` |
| --- | --- | --- | --- | --- | --- | --- |
| `plan.yml` | `resolve` | write | read | write | read | |
| `plan.yml` | `plan` | write | read | write | | read |
| `plan.yml` | `fork-notice` | | | | | |
| `run.yml` | `run` | write | read | write | | read |

`id-token: write` is for OIDC to AWS and to the server, `contents: read` for checkout, `checks: write` for the neutral fallback check when the server is unreachable, `pull-requests: read` for the pull request's base and head, and `actions: read` for downloading the plan artifact from the plan run.

A called workflow can only keep or narrow the permissions of the `GITHUB_TOKEN` its caller grants. Grant the union on the calling job, as the files above do: all five for `stackorder-plan.yml`, the four of `run` for `stackorder-run.yml`. A called job that asks for more than its caller grants does not start, and `id-token: write` is never part of the default token permissions.

There are no shared secrets between the runner and the server. The CLI requests an OIDC token with the server's base URL as audience and sends it as a bearer token; the server checks its claims against the run. See [OIDC binding](/reference/api#oidc-binding).

## Hooks {#hooks}

If any of these files exist, the CLI runs them around plan and apply, in CI and locally:

```text
.stackorder/hooks/pre-plan.sh
.stackorder/hooks/post-plan.sh
.stackorder/hooks/pre-apply.sh
.stackorder/hooks/post-apply.sh
```

| Hook | Runs | A non-zero exit |
| --- | --- | --- |
| `pre-plan.sh` | After the tool is detected, before `init` | Fails the plan |
| `post-plan.sh` | After `plan` and `show -json` | Fails the plan |
| `pre-apply.sh` | After `init` and the choice of plan file, before `apply` | Fails the apply; nothing is applied |
| `post-apply.sh` | After a successful `apply` | The apply is reported as applied, with the hook's failure as its error text, and the command exits 1 |

`stackorder drift` runs no hooks.

Each hook runs with `bash`, from the repository root, with the job's environment, the stack's [`env`](./instances#env) for the command's mode, and:

| Variable | Value |
| --- | --- |
| `STACKORDER_STACK` | The stack key, such as `infra/network:production`. |
| `STACKORDER_STACK_PATH` | The stack directory, such as `infra/network`. |
| `STACKORDER_INSTANCE` | The instance name, such as `production`; empty for a stack with no instances. |
| `STACKORDER_RUN_ID` | The Stackorder run id; empty when there is none, such as a local plan. |
| `STACKORDER_PLAN_FILE` | The path of the binary plan file; empty for `pre-plan`. |
| `STACKORDER_PLAN_JSON` | The path of the plan as JSON, from `show -json`; empty for `pre-plan`. |

Commit hooks with the executable bit set (`git update-index --chmod=+x`): a hook file without it is an error, not a skipped hook. Their output goes to the job log, through the same redaction as Terraform's in Actions.

Hooks are where OPA or conftest, Checkov or Infracost run.

### Named checks {#named-checks}

A hook reports a verdict with `stackorder check`. The server shows it as its own check run on the stack, `stackorder/<name>: <key>`, and the apply gate honours it: `pass` and `warn` let the apply through, `fail` refuses it.

```sh
#!/usr/bin/env bash
set -euo pipefail

if conftest test --policy policy "$STACKORDER_PLAN_JSON"; then
  status=pass
else
  status=fail
fi

stackorder check \
  --stack "$STACKORDER_STACK" \
  --run-id "$STACKORDER_RUN_ID" \
  --name policy \
  --status "$status" \
  --summary "conftest: $status"
```

As a `post-plan.sh` hook, this produces the check `stackorder/policy: stacks/prod/vpc` on every planned stack. The hook runs before `stackorder plan` reports the stack's result, which is what makes the verdict count: once every stack of a pull request's plan run has reported, the run is final and a later verdict from the pull request's jobs is refused. `--details-url` can link to a full report and `--details-file` attaches one. `stackorder check` needs a run id, so skip it when `STACKORDER_RUN_ID` is empty if the hook also runs locally. See the [CLI reference](/reference/cli#check).

## Fork pull requests {#forks}

The `pull_request` trigger gives workflows from forks a read-only `GITHUB_TOKEN` and no `id-token` permission. Neither the AWS role nor the Stackorder API is reachable from a fork.

The reusable `plan.yml` skips its `resolve` and `plan` jobs when `github.event.pull_request.head.repo.fork` is true and runs only `fork-notice`, which explains why in the job summary. The server, which receives the `pull_request` webhook, posts a single neutral `stackorder/plan` check titled `Not run: pull request from a fork`, unless the commit already has one. The CLI posts a neutral `stackorder/resolve` check titled `Fork pull request: not planned` if `stackorder resolve` runs on a fork's pull request some other way.

To get plans for a fork's change, a maintainer pushes the branch to the repository itself and opens the pull request from there. Switching the plan workflow to `pull_request_target` does not help: `plan.yml` skips forks whatever the event, and the server's OIDC binding accepts plan results only from `pull_request` events. It would also run code from the fork with the base repository's permissions and secrets.
