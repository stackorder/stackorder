# Actions and reusable workflows

The `stackorder/actions` repository holds one JavaScript action that installs the CLI, four composite actions that call it, and two reusable workflows. Nothing is Docker-based, so a job pays about one second of overhead, and self-hosted runners without a Docker socket work unchanged.

Releases are tagged `vX.Y.Z`, and the major tag `v1` moves to the newest stable `v1.x.y`. The tags move independently of the server and CLI releases.

| Path | Kind | What it does |
| --- | --- | --- |
| [`setup`](#setup) | JavaScript (`node24`) | Installs the `stackorder` CLI and adds it to `PATH` |
| [`resolve`](#resolve) | Composite | Runs `stackorder resolve` and exposes the matrix |
| [`plan`](#plan) | Composite | Runs `stackorder plan` for one stack and uploads the plan file |
| [`apply`](#apply) | Composite | Downloads a stack's plan artifact, then runs `stackorder apply` |
| [`drift`](#drift) | Composite | Runs `stackorder drift` for one stack; drift is an output, not a failure |
| [`.github/workflows/plan.yml`](#plan-yml) | Reusable workflow | Pull request plans: resolve, then one plan job per affected stack |
| [`.github/workflows/run.yml`](#run-yml) | Reusable workflow | Server-dispatched plan, apply or drift for one wave of stacks |

The composite actions contain no logic beyond passing inputs to the CLI, through environment variables, never interpolated into scripts. They expect `stackorder` on `PATH`, installed by `setup`, and each is replaceable with a direct `run: stackorder …` step; the flags and outputs of each command are in the [CLI reference](/reference/cli).

## `setup` {#setup}

Maps the runner to `linux`, `darwin` or `windows` and `amd64` or `arm64`, downloads `stackorder_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) from the `stackorder/stackorder` release `v<version>`, checks its SHA-256 against `stackorder_<version>_checksums.txt`, extracts it, caches it with `@actions/tool-cache` and adds it to `PATH`. A version already in the runner's tool cache is used without downloading. It is built with esbuild into a committed `dist/index.js`.

| Input | Default | Meaning |
| --- | --- | --- |
| `version` | `latest` | The release to install, as `1.2.3` or `v1.2.3`, or `latest` for the newest published release |
| `token` | `github.token` on github.com, empty elsewhere | The github.com token sent when resolving `latest`. A GitHub Enterprise Server token is never sent to github.com; pass a github.com token there, or pin `version`, to avoid anonymous rate limits |
| `checksum` | `true` | Verify the archive against the release's checksum file: `true` or `false`; any other value fails the step |

| Output | Meaning |
| --- | --- |
| `version` | The installed version, without the leading `v` |
| `path` | The absolute path of the installed binary |

```yaml
- uses: stackorder/actions/setup@v1
  with:
    version: 1.2.3
```

## `resolve` {#resolve}

Runs `stackorder resolve --server <server-url> [--base <base-ref>] [--stacks <stacks>]`. The checkout needs enough history to find the merge base, so check out with `fetch-depth: 0`.

| Input | Default | Meaning |
| --- | --- | --- |
| `server-url` | required | The server's base URL |
| `base-ref` | empty | The ref to diff against; empty lets the CLI use the pull request base |
| `stacks` | empty | Comma separated stack keys to restrict the run to |
| `working-directory` | `.` | The directory to run `stackorder` in |
| `github-token` | `${{ github.token }}` | The token for the neutral check when the server is unreachable |

| Output | Meaning |
| --- | --- |
| `matrix` | `{"include": [...]}`, one [entry](#matrix-entry) per affected stack, for `strategy.matrix` |
| `waves` | The stack keys by wave, as a JSON array of arrays |
| `affected` | The affected stack keys, as a JSON array |
| `count` | The number of affected stacks |
| `run-id` | The Stackorder run id |
| `unconfirmed` | `true` when the server was unreachable and the CLI resolved locally |

```yaml
- uses: actions/checkout@v5
  with:
    fetch-depth: 0
- uses: stackorder/actions/setup@v1
- id: resolve
  uses: stackorder/actions/resolve@v1
  with:
    server-url: ${{ vars.STACKORDER_SERVER_URL }}
```

## `plan` {#plan}

Runs `stackorder plan --stack <stack> --run-id <run-id> --server <server-url>` with `STACKORDER_PLAN_DIR` set to `$GITHUB_WORKSPACE/.stackorder/plans`, then uploads the plan file with `actions/upload-artifact@v4` under the name the CLI reports, `stackorder-plan-<slug>-<sha>`, where the slug is the key with `/` and `:` replaced by `-`, then `-` and the first 8 hex characters of the key's SHA-256 (`stackorder-plan-stacks-prod-vpc-69df0ef0-<sha>`), so keys such as `a/b` and `a-b` get different artifacts. A missing plan file fails the upload.

| Input | Default | Meaning |
| --- | --- | --- |
| `stack` | required | The stack key, `path` or `path:instance` |
| `run-id` | required | The Stackorder run id |
| `server-url` | required | The server's base URL |
| `working-directory` | `.` | The directory to run `stackorder` in |
| `upload-artifact` | `true` | Upload the plan file; only `true` uploads |
| `retention-days` | `5` | Days to keep the plan artifact |
| `github-token` | `${{ github.token }}` | The token for the neutral check when the server is unreachable |

| Output | Meaning |
| --- | --- |
| `has-changes` | `true` when the plan changes resources or outputs |
| `artifact` | The plan artifact's name |
| `plan-file` | The path of the binary plan file |
| `summary` | The plan summary, as JSON |
| `unconfirmed` | `true` when the result was not confirmed by the server |

```yaml
- uses: stackorder/actions/plan@v1
  with:
    stack: ${{ matrix.key }}
    run-id: ${{ needs.resolve.outputs.run-id }}
    server-url: ${{ vars.STACKORDER_SERVER_URL }}
```

## `apply` {#apply}

Downloads the artifact `artifact` from workflow run `plan-run-id` with `actions/download-artifact@v4` into `$GITHUB_WORKSPACE/.stackorder/plans`, then runs `stackorder apply --stack <stack> --run-id <run-id> --server <server-url> --plan-file $GITHUB_WORKSPACE/.stackorder/plans/<artifact>.tfplan`. The dispatched matrix entry names the plan run (`plan_run_id`) and the artifact (`artifact`); reading another run's artifacts is why the job needs `actions: read`. The download continues on error: when the artifact has expired the CLI finds no plan file, re-plans, and refuses to apply unless the new plan's resource addresses match the recorded plan.

| Input | Default | Meaning |
| --- | --- | --- |
| `stack` | required | The stack key |
| `run-id` | required | The Stackorder run id |
| `server-url` | required | The server's base URL |
| `plan-run-id` | required | The Actions workflow run that uploaded the plan artifact |
| `artifact` | required | The plan artifact's name; the file inside is `<artifact>.tfplan` |
| `working-directory` | `.` | The directory to run `stackorder` in |
| `token` | `${{ github.token }}` | The token for the artifact download; needs `actions: read` |

| Output | Meaning |
| --- | --- |
| `summary` | The applied plan's summary, as JSON |

`apply` has no `github-token` input: an apply never falls back to a neutral check, it fails closed.

```yaml
- uses: stackorder/actions/apply@v1
  with:
    stack: ${{ matrix.key }}
    run-id: ${{ inputs.run-id }}
    server-url: ${{ vars.STACKORDER_SERVER_URL }}
    plan-run-id: ${{ matrix.plan_run_id }}
    artifact: ${{ matrix.artifact }}
```

## `drift` {#drift}

Runs `stackorder drift --stack <stack> --run-id <run-id> --server <server-url>`. Exit code 2, drift found, is recorded in the outputs and the step succeeds; any other non-zero exit code fails it.

| Input | Default | Meaning |
| --- | --- | --- |
| `stack` | required | The stack key |
| `run-id` | required | The Stackorder run id |
| `server-url` | required | The server's base URL |
| `working-directory` | `.` | The directory to run `stackorder` in |
| `github-token` | `${{ github.token }}` | The token for the neutral check when the server is unreachable |

| Output | Meaning |
| --- | --- |
| `drifted` | `true` when real infrastructure differs from the configuration |
| `summary` | The plan summary, as JSON |
| `exit-code` | The exit code of `stackorder drift`: `0` for no drift, `2` for drift |

```yaml
- uses: stackorder/actions/drift@v1
  with:
    stack: ${{ matrix.key }}
    run-id: ${{ inputs.run-id }}
    server-url: ${{ vars.STACKORDER_SERVER_URL }}
```

## Reusable workflows {#reusable-workflows}

`plan.yml` and `run.yml` combine the actions into complete jobs, so a repository's own workflow files are a few lines each. See [Workflows](/configuration/workflows) for the calling files, the inputs with their defaults, and the permissions the caller must grant.

### `plan.yml` {#plan-yml}

Called from `stackorder-plan.yml` on `pull_request`.

| Job | Runs when | Does |
| --- | --- | --- |
| `resolve` | The head repository is not a fork | Checks out with full history, installs `stackorder`, runs the `resolve` action, and exposes `matrix`, `count`, `run-id`, `unconfirmed` and the installed `stackorder-version` |
| `plan` | `count` is above zero | One job per matrix entry, named `plan <key>`, with `fail-fast: false` and `max-parallel`: checks out the entry's `sha`, installs the tool, installs the same `stackorder` version, selects and assumes the AWS role, restores the plugin cache, runs the `plan` action |
| `fork-notice` | The head repository is a fork | Writes the reason nothing was planned to the job summary |

Inputs: `server-url` (required), `aws-role-arn`, `aws-role-arn-map`, `aws-role-session-name`, `aws-region`, `tool`, `tool-version`, `stackorder-version`, `runner`, `max-parallel`, `working-directory`, `base-ref`, `stacks`.

### `run.yml` {#run-yml}

Called from `stackorder-run.yml`, which the server dispatches once per wave and environment. One job, `run`, fans out over the `stacks` input with `fail-fast: false` and `max-parallel`, named `<mode> wave <n> <key>`. Each job:

- runs under `environment: ${{ matrix.environment }}` and in the concurrency group `stackorder-stack-<key>`, without cancel-in-progress;
- fails before checkout unless `mode` is `plan`, `apply` or `drift`;
- checks out `sha`, falling back to the entry's `sha`, then to the dispatched ref;
- installs the entry's tool and `stackorder`, selects and assumes the AWS role, restores the plugin cache;
- runs exactly one of the `plan`, `apply` or `drift` actions, according to `mode`.

Inputs: `run-id`, `mode` and `stacks` (required), `wave`, `sha`, `server-url` (required), `aws-role-arn-map`, `aws-role-arn`, `aws-plan-role-arn`, `aws-role-session-name`, `aws-region`, `tool`, `tool-version`, `stackorder-version`, `runner`, `max-parallel`, `working-directory`.

**Role selection.** For `mode: apply` the role comes from `aws-role-arn-map`, whose keys are path prefixes (`infra/`), exact stack keys (`infra/network:production`) or instances in any directory (`:production`). The first match wins, in this order: the exact key, then `:instance`, then the longest prefix the stack path starts with, whole segments only (a key containing `:` is never a prefix); else `aws-role-arn`. The `:instance` rule reads the entry's `instance`, or its `workspace` for an entry from an older CLI. For `mode: plan` and `mode: drift` the map is ignored: those dispatches run under the `default` environment and assume `aws-plan-role-arn`, else `aws-role-arn`. When no role results, the job logs a notice and skips AWS credentials. `plan.yml` selects roles the same way as an apply, from the map and then `aws-role-arn`.

**Session name.** `aws-role-session-name` is passed to `configure-aws-credentials` as `role-session-name`. It is a name, or a JSON object, recognised by its leading `{`, with `plan`, `apply` and `drift` keys from which the job's mode picks one: `drift` falls back to `plan`, and `plan.yml` always uses `plan`. Every byte outside `[A-Za-z0-9_+=,.@-]` becomes `-`, and the name is cut at 64 characters. Nothing is added, so every stack of a job's mode gets the same name. An empty result keeps the action's default, `GitHubActions`.

### Hooks {#hooks}

The CLI runs `.stackorder/hooks/pre-plan.sh`, `post-plan.sh`, `pre-apply.sh` and `post-apply.sh` itself when they exist, so they need no workflow step. See [Hooks](/configuration/workflows#hooks).

## Matrix entry {#matrix-entry}

Each element of the `matrix` output, and of the `stacks` dispatch input, has this shape (`v1.MatrixEntry`):

```json
{
  "stack": "infra/network",
  "key": "infra/network:production",
  "instance": "production",
  "workspace": "",
  "environment": "production",
  "wave": 0,
  "tool": "tofu",
  "tool_version": "1.10.0",
  "plan_output": "full",
  "sha": "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c",
  "plan_run_id": 12345678901,
  "artifact": "stackorder-plan-infra-network-production-2ebae875-9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c"
}
```

| Field | Meaning |
| --- | --- |
| `stack` | The stack directory |
| `key` | The stack key, `path` or `path:instance` |
| `instance` | The [instance](/configuration/instances) name; empty for a stack with no instances |
| `workspace` | The Terraform workspace the CLI selects; empty for none |
| `environment` | The GitHub environment the job runs under: the stack's own for an apply, `default` for plan and drift dispatches. A stack's own environment is its instance name when nothing maps it, and `default` for an unmapped stack with no instance |
| `wave` | The wave index |
| `tool`, `tool_version` | The tool and version for the stack |
| `plan_output` | `full` or `summary` |
| `sha` | The commit the job checks out |
| `plan_run_id`, `artifact` | Apply dispatches only: the Actions run that uploaded the plan, and the artifact's name |

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
  pull-requests: read
jobs:
  resolve:
    if: github.event.pull_request.head.repo.fork == false
    runs-on: ubuntu-latest
    outputs:
      matrix: ${{ steps.resolve.outputs.matrix }}
      count: ${{ steps.resolve.outputs.count }}
      run-id: ${{ steps.resolve.outputs.run-id }}
    steps:
      - uses: actions/checkout@v5
        with:
          fetch-depth: 0
      - uses: stackorder/actions/setup@v1
      - id: resolve
        uses: stackorder/actions/resolve@v1
        with:
          server-url: ${{ vars.STACKORDER_SERVER_URL }}
  plan:
    needs: resolve
    if: needs.resolve.outputs.count > 0
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.resolve.outputs.matrix) }}
    steps:
      - uses: actions/checkout@v5
        with:
          ref: ${{ matrix.sha }}
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::123456789012:role/stackorder-plan
          aws-region: us-east-1
      - uses: opentofu/setup-opentofu@v1
        with:
          tofu_version: ${{ matrix.tool_version }}
          tofu_wrapper: false
      - uses: stackorder/actions/setup@v1
      - uses: stackorder/actions/plan@v1
        with:
          stack: ${{ matrix.key }}
          run-id: ${{ needs.resolve.outputs.run-id }}
          server-url: ${{ vars.STACKORDER_SERVER_URL }}
```

If the server sets `STACKORDER_REQUIRED_WORKFLOW_REF`, it accepts runner tokens only from the canonical reusable workflows, so hand-built jobs like this one work only without that pin.
