# Actions and reusable workflows

The `stackorder/actions` repository holds one JavaScript action that installs the CLI, four composite actions that call it, and two reusable workflows. Nothing is Docker-based, so a job pays about one second of overhead, and self-hosted runners without a Docker socket work unchanged.

Releases are tagged `v1` and `v1.x.y`. The `v1` tag moves independently of the server and CLI releases.

| Action | Kind | What it does |
| --- | --- | --- |
| [`setup`](#setup) | JavaScript (`node24`) | Installs the `stackorder` CLI and adds it to `PATH` |
| [`resolve`](#resolve) | Composite | Runs `stackorder resolve` and exposes `matrix`, `waves` and `run-id` |
| [`plan`](#plan) | Composite | Runs `stackorder plan --stack` and uploads the plan file |
| [`apply`](#apply) | Composite | Downloads the plan artifact, then runs `stackorder apply --stack` |
| [`drift`](#drift) | Composite | Runs `stackorder drift --stack` |

The composite actions are each under 30 lines of YAML and contain no logic beyond argument passing. Each is replaceable with a direct `run: stackorder …` step; the flags and outputs of each command are in the [CLI reference](/reference/cli).

## `setup` {#setup}

Downloads the pinned `stackorder` release for the runner's OS and architecture, verifies its SHA-256 against the release's checksum file, caches it with `@actions/tool-cache`, and adds it to `PATH`. It is built with esbuild into a single committed `dist/index.js`.

```yaml
- uses: stackorder/actions/setup@v1
```

## `resolve` {#resolve}

Runs `stackorder resolve`: scans the repository, diffs base to head, posts the graph, and exposes the server's answer.

| Output | Meaning |
| --- | --- |
| `matrix` | The matrix JSON, `{"include": [...]}`, one [entry](#matrix-entry) per affected stack |
| `waves` | The stack keys by wave, as JSON |
| `run-id` | The Stackorder run id |

```yaml
- uses: actions/checkout@v7
  with:
    fetch-depth: 0
- uses: stackorder/actions/setup@v1
- id: resolve
  uses: stackorder/actions/resolve@v1
```

## `plan` {#plan}

Runs `stackorder plan --stack ${{ inputs.stack }}`, then uploads the plan file with `actions/upload-artifact` as `stackorder-plan-<key>-<sha>`.

| Input | Meaning |
| --- | --- |
| `stack` | The stack key to plan |

```yaml
- uses: stackorder/actions/plan@v1
  with:
    stack: ${{ matrix.key }}
```

## `apply` {#apply}

Downloads the plan artifact from the plan run with `actions/download-artifact`, then runs `stackorder apply --stack`. The dispatched matrix entry names the plan run (`plan_run_id`) and the artifact (`artifact`); reading another run's artifacts is why the job needs `actions: read`.

```yaml
- uses: stackorder/actions/apply@v1
  with:
    stack: ${{ matrix.key }}
```

## `drift` {#drift}

Runs `stackorder drift --stack`, which runs `plan -detailed-exitcode` and uploads the result.

```yaml
- uses: stackorder/actions/drift@v1
  with:
    stack: ${{ matrix.key }}
```

## Reusable workflows {#reusable-workflows}

`plan.yml` and `run.yml` in `stackorder/actions/.github/workflows/` combine the actions into complete jobs, so a repository's own workflow files are a dozen lines each. See [Workflows](/configuration/workflows) for the calling files.

| Workflow | Jobs |
| --- | --- |
| `plan.yml` | `resolve`, then `plan` with one matrix entry per affected stack |
| `run.yml` | One job per stack in the dispatched `stacks`, running `plan`, `apply` or `drift` according to `mode`, under the stack's GitHub environment |

### Inputs {#inputs}

| Input | Workflows | Meaning |
| --- | --- | --- |
| `aws-role-arn` | both | The IAM role every stack job assumes |
| `aws-role-arn-map` | both | A JSON object from stack path prefix to IAM role ARN; use this or `aws-role-arn` |
| `tool` | both | `terraform` or `tofu` |
| `tool-version` | both | The tool version to install |
| `stackorder-version` | both | The CLI release to install |
| `runner` | both | The runner label |
| `max-parallel` | both | The most matrix jobs at once |
| `working-directory` | both | The directory the jobs run the CLI in |
| `run-id`, `mode`, `wave`, `sha`, `stacks` | `run.yml` | The dispatch inputs of `stackorder-run.yml`, passed through |

### Permissions {#permissions}

```yaml
permissions:
  id-token: write      # OIDC to AWS and to the Stackorder server
  contents: read       # checkout
  actions: read        # download the plan artifact from the plan run
  checks: write        # fallback check when the server is unreachable
```

The calling job must grant at least these, since a called workflow cannot widen its caller's token.

### Environments {#environments}

In `run.yml` the stack job declares `environment: ${{ matrix.environment }}`, taking each stack's GitHub environment from the dispatched `stacks` JSON. That is what lets environment protection rules gate applies per stack, and what puts the environment into the job's OIDC subject for the AWS trust policy.

### Hooks {#hooks}

The CLI runs `.stackorder/hooks/pre-plan.sh`, `post-plan.sh`, `pre-apply.sh` and `post-apply.sh` when they exist, with `STACKORDER_STACK`, `STACKORDER_RUN_ID`, `STACKORDER_PLAN_JSON` and `STACKORDER_PLAN_FILE` set. See [Hooks](/configuration/workflows#hooks).

## Matrix entry {#matrix-entry}

Each element of the `matrix` output, and of the `stacks` dispatch input, has this shape:

```json
{
  "stack": "stacks/prod/vpc",
  "key": "stacks/prod/vpc",
  "workspace": "",
  "environment": "production",
  "wave": 0,
  "tool": "tofu",
  "tool_version": "1.10.0",
  "plan_output": "full",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "plan_run_id": 12345678901,
  "artifact": "stackorder-plan-stacks-prod-vpc-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c"
}
```

| Field | Meaning |
| --- | --- |
| `stack` | The stack directory |
| `key` | The stack key, `path` or `path:workspace` |
| `workspace` | The workspace; empty for `default` |
| `environment` | The GitHub environment the job runs under; `default` when unmapped |
| `wave` | The wave index |
| `tool`, `tool_version` | The tool and version for the stack |
| `plan_output` | `full` or `summary` |
| `sha` | The commit the job checks out |
| `plan_run_id`, `artifact` | For applies: the Actions run that uploaded the plan, and the artifact's name |

## Building your own jobs {#custom}

The reusable workflows are a convenience. A plan workflow built from the actions directly looks like this:

```yaml
name: stackorder plan
on:
  pull_request:
    types: [opened, synchronize, reopened]
concurrency:
  group: stackorder-plan-${{ github.event.pull_request.number }}
  cancel-in-progress: true
permissions:
  id-token: write
  contents: read
  actions: read
  checks: write
env:
  STACKORDER_SERVER_URL: ${{ vars.STACKORDER_SERVER_URL }}
jobs:
  resolve:
    runs-on: ubuntu-latest
    outputs:
      matrix: ${{ steps.resolve.outputs.matrix }}
      run-id: ${{ steps.resolve.outputs.run-id }}
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0
      - uses: stackorder/actions/setup@v1
      - id: resolve
        uses: stackorder/actions/resolve@v1
  plan:
    needs: resolve
    if: fromJSON(needs.resolve.outputs.matrix).include[0] != null
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.resolve.outputs.matrix) }}
    env:
      STACKORDER_RUN_ID: ${{ needs.resolve.outputs.run-id }}
    steps:
      - uses: actions/checkout@v7
      - uses: aws-actions/configure-aws-credentials@v6
        with:
          role-to-assume: arn:aws:iam::123456789012:role/stackorder-plan
          aws-region: us-east-1
      - uses: opentofu/setup-opentofu@v2
        with:
          tofu_version: ${{ matrix.tool_version }}
          tofu_wrapper: false
      - uses: stackorder/actions/setup@v1
      - uses: stackorder/actions/plan@v1
        with:
          stack: ${{ matrix.key }}
```

If the server sets `STACKORDER_REQUIRED_WORKFLOW_REF`, it accepts results only from the canonical reusable workflows, so hand-built jobs like this one work only without that pin.
