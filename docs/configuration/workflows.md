# Workflows

A repository has two workflow files. Each is a dozen lines that call a reusable workflow from `stackorder/actions`. The plan workflow runs on every pull request push; the run workflow runs only when the server dispatches it.

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
    uses: stackorder/actions/.github/workflows/plan.yml@v1
    with:
      aws-role-arn: arn:aws:iam::123456789012:role/stackorder-plan
      tool: tofu
    secrets: inherit
```

- **Trigger.** `pull_request` on `opened`, `synchronize` and `reopened`. Plans never wait for the server: GitHub starts them.
- **Concurrency.** One group per PR with `cancel-in-progress: true`, so a new push cancels the plan of the previous one.
- **Jobs.** The reusable `plan.yml` runs a `resolve` job, which scans the repository, posts the graph and gets the matrix back, and a `plan` job with one matrix entry per affected stack.

## `stackorder-run.yml` {#run}

```yaml
name: stackorder run
on:
  workflow_dispatch:
    inputs:
      run_id: { type: string, required: true }
      mode: { type: string, required: true }
      wave: { type: string, required: false }
      sha: { type: string, required: false }
      stacks: { type: string, required: false }
jobs:
  run:
    permissions:
      id-token: write
      contents: read
      actions: read
      checks: write
    uses: stackorder/actions/.github/workflows/run.yml@v1
    with:
      run-id: ${{ inputs.run_id }}
      mode: ${{ inputs.mode }}
      wave: ${{ inputs.wave }}
      sha: ${{ inputs.sha }}
      stacks: ${{ inputs.stacks }}
      aws-role-arn-map: '{"stacks/prod/": "arn:aws:iam::123456789012:role/stackorder-apply-prod", "stacks/staging/": "arn:aws:iam::123456789012:role/stackorder-apply-staging"}'
    secrets: inherit
```

The server dispatches this workflow by its file name, `stackorder-run.yml`, so keep that name. It has no `concurrency` group and is never cancelled in progress.

| Dispatch input | Meaning |
| --- | --- |
| `run_id` | The Stackorder run id, a UUID. |
| `mode` | `plan`, `apply` or `drift`. |
| `wave` | The wave index this dispatch covers. |
| `sha` | The commit to check out. For drift it is the head of the default branch. |
| `stacks` | A JSON array of matrix entries, one per stack. Each entry carries the stack's GitHub environment, tool and, for applies, the workflow run and artifact that hold its plan file. |

The server dispatches once per wave and environment. In `run.yml` the job for each stack declares `environment: ${{ matrix.environment }}`, so that environment's protection rules gate the job and its OIDC token carries the environment in its subject.

A `stackorder plan` comment dispatches `mode: plan` for the named stacks. The scheduler dispatches `mode: drift` per stack.

## Reusable workflow inputs {#inputs}

Both `plan.yml` and `run.yml` accept:

| Input | Meaning |
| --- | --- |
| `aws-role-arn` | The IAM role every stack job assumes. |
| `aws-role-arn-map` | A JSON object from stack path prefix to IAM role ARN, for one role per environment. Use this or `aws-role-arn`. |
| `tool` | `terraform` or `tofu`. |
| `tool-version` | The version of the tool to install. |
| `stackorder-version` | The `stackorder` CLI release to install. |
| `runner` | The runner label jobs run on. |
| `max-parallel` | The most matrix jobs that run at once. |
| `working-directory` | The directory the jobs run the CLI in. |

`run.yml` also takes the dispatch inputs, passed through as `run-id`, `mode`, `wave`, `sha` and `stacks`.

`secrets: inherit` passes the repository's secrets to the reusable workflow, for providers that need credentials besides AWS.

The CLI inside the jobs finds the server through `STACKORDER_SERVER_URL`. With it unset, the CLI runs in local mode and marks every result `unconfirmed`. Set it as an organization or repository Actions variable.

## Permissions {#permissions}

The reusable workflows declare the minimum job-level permissions:

```yaml
permissions:
  id-token: write      # OIDC to AWS and to the Stackorder server
  contents: read       # checkout
  actions: read        # download the plan artifact from the plan run
  checks: write        # fallback check when the server is unreachable
```

A called workflow can only keep or narrow the permissions of the `GITHUB_TOKEN` its caller grants. Grant the same four on the calling job, as the files above do, or `id-token: write` is missing whenever the repository's default token permissions are read-only.

There are no shared secrets between the runner and the server. The CLI requests an OIDC token with the server's base URL as audience and sends it as a bearer token; the server checks its claims against the run. See [OIDC binding](/reference/api#oidc-binding).

## Tool installation and caching

The CLI does not install Terraform or OpenTofu. It expects the binary on `PATH`, installed by `hashicorp/setup-terraform` or `opentofu/setup-opentofu`. Provider plugins are cached with `actions/cache`, keyed on `.terraform.lock.hcl`.

## Hooks {#hooks}

If any of these files exist, the CLI runs them around plan and apply, in CI and locally:

```text
.stackorder/hooks/pre-plan.sh
.stackorder/hooks/post-plan.sh
.stackorder/hooks/pre-apply.sh
.stackorder/hooks/post-apply.sh
```

Each hook runs with these variables set:

| Variable | Value |
| --- | --- |
| `STACKORDER_STACK` | The stack key. |
| `STACKORDER_RUN_ID` | The Stackorder run id. |
| `STACKORDER_PLAN_JSON` | The plan as JSON, from `show -json`. |
| `STACKORDER_PLAN_FILE` | The binary plan file. |

Hooks are where OPA or conftest, Checkov or Infracost run. Commit them with a shebang line and the executable bit set.

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

As a `post-plan.sh` hook, this produces the check `stackorder/policy: stacks/prod/vpc` on every planned stack. `--details-url` can link to a full report. See the [CLI reference](/reference/cli#check).

## Fork pull requests {#forks}

The `pull_request` trigger gives workflows from forks a read-only `GITHUB_TOKEN` and no `id-token` permission. Neither the AWS role nor the Stackorder API is reachable from a fork.

By default Stackorder posts a single neutral check explaining this and runs nothing.

Maintainers who want fork plans can switch the plan workflow to `pull_request_target` behind a label gate. Stackorder does not encourage it. `pull_request_target` runs with the base repository's permissions and secrets, so the label is all that stands between code from the fork and your plan role. Note also that the server's OIDC binding accepts plan results only from `pull_request` events.
