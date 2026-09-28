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
- **Jobs.** The reusable `plan.yml` runs a `resolve` job, which scans the repository, posts the graph and gets the matrix back, and a `plan` job with one matrix entry per affected stack. For a pull request from a fork it runs neither; see [Fork pull requests](#forks).

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
      aws-role-arn-map: '{"stacks/prod/": "arn:aws:iam::123456789012:role/stackorder-apply-prod", "stacks/staging/": "arn:aws:iam::123456789012:role/stackorder-apply-staging"}'
    secrets: inherit
```

The server dispatches this workflow by its file name, `stackorder-run.yml`, so keep that name. The file has no `concurrency` group and is never cancelled in progress. Inside `run.yml`, each stack's job joins the concurrency group `stackorder-stack-<key>` without `cancel-in-progress`, so two jobs never run on the same stack at once; keep your own concurrency groups clear of that prefix.

| Dispatch input | Meaning |
| --- | --- |
| `run_id` | The Stackorder run id, a UUID. |
| `mode` | `plan`, `apply` or `drift`. |
| `wave` | The wave index this dispatch covers. |
| `sha` | The commit to check out. For drift it is the head of the default branch. |
| `stacks` | A JSON array of matrix entries, one per stack. Each entry carries the stack's GitHub environment, tool and, for applies, the workflow run and artifact that hold its plan file. |

The server dispatches once per wave and environment. In `run.yml` the job for each stack declares `environment: ${{ matrix.environment }}`, so that environment's protection rules gate the job and its OIDC token carries the environment in its subject.

A `stackorder plan` comment dispatches `mode: plan` for the named stacks. The scheduler dispatches `mode: drift` per stack. These jobs run under the stack's environment too, whatever the mode, so an environment with required reviewers also holds a drift check or a comment-requested plan until someone approves it.

## Reusable workflow inputs {#inputs}

Both `plan.yml` and `run.yml` accept:

| Input | Default | Meaning |
| --- | --- | --- |
| `server-url` | required | The server's base URL. |
| `aws-role-arn` | empty | The IAM role for stacks that match no prefix in `aws-role-arn-map`. |
| `aws-role-arn-map` | empty | A JSON object from stack path prefix to IAM role ARN, for one role per environment. The longest matching prefix wins. |
| `aws-region` | `us-east-1` | The AWS region for the credentials. |
| `tool` | `terraform` | `terraform` or `tofu`, for stacks whose matrix entry names no tool. |
| `tool-version` | `latest` | The tool version to install, for stacks whose matrix entry pins none. |
| `stackorder-version` | `latest` | The `stackorder` CLI release to install. |
| `runner` | `ubuntu-latest` | The runner label jobs run on, or a JSON array or object for `runs-on`. |
| `max-parallel` | `6` | The most matrix jobs that run at once. |
| `working-directory` | `.` | The directory the jobs run the CLI in. |

When neither `aws-role-arn` nor `aws-role-arn-map` yields a role for a stack, the job skips AWS credentials, which suits self-hosted runners with an instance role. Reading `aws-role-arn-map` needs `jq` on the runner.

`plan.yml` also takes `base-ref`, the ref to diff against (default: the pull request base), and `stacks`, comma separated stack keys to restrict the plan to. `run.yml` also takes the dispatch inputs, passed through as `run-id`, `mode`, `wave`, `sha` and `stacks`.

`secrets: inherit` passes the repository's secrets to the reusable workflow, for providers that need credentials besides AWS.

The files above read `server-url` from the Actions variable `STACKORDER_SERVER_URL`. Set it for the organization or the repository. If it is empty, the CLI runs in local mode and marks every result `unconfirmed`.

## Permissions {#permissions}

The reusable workflows declare the minimum job-level permissions:

```yaml
permissions:
  id-token: write      # OIDC to AWS and to the Stackorder server
  contents: read       # checkout
  actions: read        # download the plan artifact from the plan run
  checks: write        # fallback check when the server is unreachable
```

The `resolve` job of `plan.yml` also needs `pull-requests: read`, to read the pull request's base and head.

A called workflow can only keep or narrow the permissions of the `GITHUB_TOKEN` its caller grants. Grant them on the calling job, as the files above do: all five for `stackorder-plan.yml`, the four above for `stackorder-run.yml`. A called job that asks for more than its caller grants does not start, and `id-token: write` is never part of the default token permissions.

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

By default Stackorder posts a single neutral check explaining this and runs nothing. The reusable `plan.yml` skips its `resolve` and `plan` jobs when the head repository is a fork, and explains why in the job summary.

To get plans for a fork's change, a maintainer pushes the branch to the repository itself and opens the pull request from there. Switching the plan workflow to `pull_request_target` does not help: `plan.yml` skips forks whatever the event, and the server's OIDC binding accepts plan results only from `pull_request` events. It would also run code from the fork with the base repository's permissions and secrets.
