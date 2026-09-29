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
    secrets: inherit
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
    secrets: inherit
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
| `sha` | The commit the run is for, which each job checks out: the pull request head for a pull request's plans and applies, the head of the default branch for drift checks and cross-repository plans. |
| `stacks` | A JSON array of [matrix entries](/reference/actions#matrix-entry), one per stack. Each entry carries the stack's GitHub environment, tool and, for applies, the workflow run and artifact that hold its plan file. |

### Which environment and role a job gets {#environments}

| Mode | Dispatched for | Environment | AWS role |
| --- | --- | --- | --- |
| `apply` | A `stackorder apply` comment, a merge in `on_merge` mode | The stack's environment, from `environments` or its `.stackorder.yaml`; `default` when nothing matches | The longest matching prefix of `aws-role-arn-map`, else `aws-role-arn` |
| `plan` | A `stackorder plan` comment, a re-run from the UI or the checks tab, a cross-repository plan | Always `default` | `aws-plan-role-arn`, else `aws-role-arn` |
| `drift` | The drift schedule | Always `default` | `aws-plan-role-arn`, else `aws-role-arn` |

An apply is dispatched once per wave and environment, at most `apply.max_parallel` stacks per dispatch. Each job declares `environment: ${{ matrix.environment }}`, so that environment's protection rules gate the job and its OIDC token carries `environment:<name>` in its subject. Plan and drift dispatches run under `default` so that an environment with required reviewers never holds a read-only job, and so that the plan role's trust policy covers them: see [Security hardening](/operations/security-hardening#trust-policies).

## Reusable workflow inputs {#inputs}

| Input | Workflows | Default | Meaning |
| --- | --- | --- | --- |
| `server-url` | both | required | The server's base URL. The CLI also requests it as the OIDC audience, so it must equal the server's `STACKORDER_OIDC_AUDIENCE`, the base URL by default. |
| `aws-role-arn` | both | empty | The IAM role for stacks that match no prefix in `aws-role-arn-map`, and for plan and drift dispatches when `aws-plan-role-arn` is empty. |
| `aws-role-arn-map` | both | empty | A JSON object from stack path prefix to IAM role ARN; the longest matching prefix wins. In `run.yml` only apply jobs use it. |
| `aws-plan-role-arn` | `run.yml` | empty | The IAM role for plan and drift dispatches, which run under the `default` environment. |
| `aws-region` | both | `us-east-1` | The AWS region for the credentials. |
| `tool` | both | `terraform` | `terraform` or `tofu`, for stacks whose matrix entry names no tool. |
| `tool-version` | both | `latest` | The tool version to install, for stacks whose matrix entry pins none. |
| `stackorder-version` | both | `latest` | The `stackorder` CLI release to install: `1.2.3`, `v1.2.3` or `latest`. `plan.yml` resolves `latest` once, in the resolve job. |
| `runner` | both | `ubuntu-latest` | The runner label jobs run on, or a JSON array or object for `runs-on`. |
| `max-parallel` | both | `6` | The most matrix jobs that run at once. |
| `working-directory` | both | `.` | The directory the jobs run the CLI in, relative to the repository root. |
| `base-ref` | `plan.yml` | empty | The ref to diff against; empty uses the pull request base. |
| `stacks` | `plan.yml` | empty | Comma separated stack keys to restrict the plan to. |
| `run-id`, `mode`, `wave`, `sha`, `stacks` | `run.yml` | | The dispatch inputs, passed through. `run-id`, `mode` and `stacks` are required. |

When no role applies to a stack, the job logs a notice and skips AWS credentials, which suits self-hosted runners with an instance role. Reading `aws-role-arn-map` needs `jq` on the runner.

Every job installs the stack's tool with `hashicorp/setup-terraform@v3` or `opentofu/setup-opentofu@v1` (wrapper disabled), sets `STACKORDER_TOOL` to it, installs `stackorder` with the [`setup` action](/reference/actions#setup), and caches providers in `$RUNNER_TEMP/terraform-plugin-cache` with `actions/cache@v4`, keyed on the stack's `.terraform.lock.hcl`.

`secrets: inherit` passes the repository's secrets to the reusable workflow, for providers that need credentials besides AWS.

The files above read `server-url` from the Actions variable `STACKORDER_SERVER_URL`. Set it for the organization or the repository. If it is empty, the CLI runs in local mode: plans are `unconfirmed` and applies are refused.

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

Each hook runs with `bash`, from the repository root, with the job's environment plus:

| Variable | Value |
| --- | --- |
| `STACKORDER_STACK` | The stack key. |
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

As a `post-plan.sh` hook, this produces the check `stackorder/policy: stacks/prod/vpc` on every planned stack. `--details-url` can link to a full report and `--details-file` attaches one. `stackorder check` needs a run id, so skip it when `STACKORDER_RUN_ID` is empty if the hook also runs locally. See the [CLI reference](/reference/cli#check).

## Fork pull requests {#forks}

The `pull_request` trigger gives workflows from forks a read-only `GITHUB_TOKEN` and no `id-token` permission. Neither the AWS role nor the Stackorder API is reachable from a fork.

The reusable `plan.yml` skips its `resolve` and `plan` jobs when `github.event.pull_request.head.repo.fork` is true and runs only `fork-notice`, which explains why in the job summary. The server, which receives the `pull_request` webhook, posts a single neutral check, `Not run: pull request from a fork`. The CLI does the same if `stackorder resolve` runs on a fork's pull request some other way.

To get plans for a fork's change, a maintainer pushes the branch to the repository itself and opens the pull request from there. Switching the plan workflow to `pull_request_target` does not help: `plan.yml` skips forks whatever the event, and the server's OIDC binding accepts plan results only from `pull_request` events. It would also run code from the fork with the base repository's permissions and secrets.
