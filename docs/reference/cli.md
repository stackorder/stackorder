# CLI

All runner-side logic lives in one static Go binary, `stackorder`. The Actions are thin wrappers around it, and the same binary works on a laptop: `stackorder graph`, `stackorder affected --base main` and `stackorder plan --stack stacks/prod/vpc` behave the same locally and in CI.

## Commands

| Command | In CI | Locally |
| --- | --- | --- |
| [`resolve`](#resolve) | Scans the repository, diffs base to head, posts the graph, writes the matrix to `GITHUB_OUTPUT` | Prints the affected set and waves for a base ref |
| [`plan --stack`](#plan) | `init` with the S3 backend, `plan -out`, `show -json`, summary, redaction, result upload | Same, without the upload |
| [`apply --stack`](#apply) | Downloads the plan artifact, verifies its SHA and lock, `apply`, result upload | Refuses unless `--local` and an API key are given, and still takes the lock through the server |
| [`drift --stack`](#drift) | `plan -detailed-exitcode`, result upload | Same |
| [`check`](#check) | Posts a named check verdict for a stack | Used by hooks |
| [`graph`](#graph), [`affected`](#affected) | Used by `resolve` internally | Inspection and debugging; `--format dot` for Graphviz |
| [`unlock`](#unlock) | Not used | Releases orchestration locks through the API, audited |
| [`version`](#version) | Prints the version | Same |

## Global flags {#global-flags}

| Flag | Default | Meaning |
| --- | --- | --- |
| `--server` | `STACKORDER_SERVER_URL` | Server base URL. Unset means local mode. |
| `--repo-root` | `GITHUB_WORKSPACE`, or the git top level | The repository to scan. |
| `--verbose` | off | More logging. |
| `--format` | `text` | Output format: `text`, `json`, or `dot` where it applies. |

In Actions the CLI reads the repository, SHA, event, PR number, run id and attempt from the `GITHUB_*` variables and the event payload. It writes outputs to `GITHUB_OUTPUT` and a Markdown summary to `GITHUB_STEP_SUMMARY`.

## `resolve` {#resolve}

```sh
stackorder resolve [--base <ref>] [--stacks a,b]
```

Scans the `stacks.discover` paths and module directories, parses `module` sources and `terraform_remote_state` blocks, and diffs base to head. In CI it registers the run, posts the graph and changed paths, and writes the server's answer as outputs. If the server is unreachable it computes the affected set locally and marks the result unconfirmed.

| Flag | Meaning |
| --- | --- |
| `--base <ref>` | The ref to diff against. |
| `--stacks a,b` | Restrict the run to these stack keys, as for a `stackorder plan stacks/a stacks/b` comment. |

| Output | Meaning |
| --- | --- |
| `run-id` | The Stackorder run id. |
| `matrix` | The GitHub Actions matrix JSON, `{"include": [...]}`, one entry per affected stack. |
| `waves` | The stack keys by wave index, as JSON. |
| `affected` | The affected stacks, as JSON. |
| `count` | The number of affected stacks. |
| `unconfirmed` | `true` when the CLI resolved locally without the server. |

## `plan` {#plan}

```sh
stackorder plan --stack <key> [--run-id <id>] [--out <file>]
```

Runs `init` against the stack's S3 backend, `plan -out` and `show -json`. Builds the summary of adds, changes, destroys and replaced addresses, redacts the output, and posts the summary and a plan text capped at 256 KB. In CI the plan file is uploaded as the artifact `stackorder-plan-<key>-<sha>`, with `/` and `:` in the key replaced by `-`. Runs the `pre-plan` and `post-plan` [hooks](/configuration/workflows#hooks).

| Flag | Meaning |
| --- | --- |
| `--stack <key>` | The stack key. Required. |
| `--run-id <id>` | The run to report to. Defaults to `STACKORDER_RUN_ID`. |
| `--out <file>` | Where to write the plan file. Defaults to a file under `STACKORDER_PLAN_DIR`. |

| Output | Meaning |
| --- | --- |
| `has-changes` | `true` when the plan is not a no-op. |
| `plan-file` | The path of the plan file. |
| `artifact` | The artifact name for the plan file. |
| `summary` | The plan summary, as JSON. |
| `unconfirmed` | `true` when the result could not be confirmed with the server. |

## `apply` {#apply}

```sh
stackorder apply --stack <key> --run-id <id> [--plan-file <file>] [--local]
```

Verifies the plan file's commit and the stack's orchestration lock with the server, runs `apply`, and uploads the result. If the server cannot confirm the lock, it refuses to apply. With `apply.from_plan` and an expired artifact, it re-plans and refuses unless the resource-address set matches the recorded plan. Runs the `pre-apply` and `post-apply` hooks.

| Flag | Meaning |
| --- | --- |
| `--stack <key>` | The stack key. Required. |
| `--run-id <id>` | The run this apply belongs to. Required. |
| `--plan-file <file>` | The saved plan to apply. |
| `--local` | Allow an apply from outside Actions. Needs `STACKORDER_API_KEY`. |

| Output | Meaning |
| --- | --- |
| `summary` | The applied summary, as JSON. |

Outside Actions, `apply` refuses unless `--local` and an API key are given. It then starts a manual run through the server, which takes the stack's lock before answering and releases it when the result arrives.

## `drift` {#drift}

```sh
stackorder drift --stack <key> [--run-id <id>]
```

Runs `plan -detailed-exitcode` and uploads the result. Exits 2 when the stack has drifted.

| Output | Meaning |
| --- | --- |
| `drifted` | `true` when the plan found changes. |
| `summary` | The plan summary, as JSON. |

## `check` {#check}

```sh
stackorder check --stack <key> --run-id <id> --name <name> --status pass|fail|warn \
  [--summary <text>] [--details-url <url>]
```

Posts a named check verdict for a stack, usually from a [hook](/configuration/workflows#named-checks). The server shows it as the check run `stackorder/<name>: <key>`. In the apply gate `pass` and `warn` pass and `fail` refuses.

| Flag | Meaning |
| --- | --- |
| `--stack <key>` | The stack key. |
| `--run-id <id>` | The run the verdict belongs to. |
| `--name <name>` | The check name, such as `policy` or `cost`. |
| `--status` | `pass`, `fail` or `warn`. |
| `--summary <text>` | A one-line summary shown on the check run. |
| `--details-url <url>` | A link to the full report. |

## `graph` {#graph}

```sh
stackorder graph [--format text|json|dot]
```

Scans the repository and prints the dependency graph: stacks, modules and edges. `--format dot` writes Graphviz:

```sh
stackorder graph --format dot | dot -Tsvg > graph.svg
```

## `affected` {#affected}

```sh
stackorder affected --base <ref> [--format text|json|dot]
```

Prints the stacks affected by the changes since `<ref>`, with their waves and reasons. Exits 2 when the affected set is not empty.

## `unlock` {#unlock}

```sh
stackorder unlock <key>… [--reason <text>] [--force-state]
```

Releases the orchestration locks on the named stacks through the API. Needs `STACKORDER_API_KEY`, and the release is audited.

| Flag | Meaning |
| --- | --- |
| `--reason <text>` | Why the lock is released; recorded with the release. |
| `--force-state` | Release a stack left `unknown` after a runner died mid-apply. Check the S3 state lock yourself first; Stackorder never touches it. |

## `version` {#version}

```sh
stackorder version
```

Prints the version, commit, build date, Go version and platform on one line.

## Exit codes {#exit-codes}

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Error. |
| `2` | The plan has changes: `affected` found affected stacks, `drift` found drift. |
| `3` | Refused by the server: the apply gate, a lock, or an unconfirmed result. |

## Environment {#environment}

| Variable | Purpose |
| --- | --- |
| `STACKORDER_SERVER_URL` | Server base URL; unset means local mode. |
| `STACKORDER_API_KEY` | Automation key for local `apply` and `unlock`. |
| `STACKORDER_RUN_ID` | Run id from the resolve step or the dispatch input. |
| `STACKORDER_TOOL`, `STACKORDER_TOOL_VERSION` | Override the configured tool. |
| `STACKORDER_BACKEND_CONFIG` | Extra `-backend-config` values for `init`, comma separated. |
| `STACKORDER_PLAN_DIR` | Where plan files are written; default `.stackorder/plans`. |
| `GITHUB_*`, `ACTIONS_ID_TOKEN_REQUEST_URL`, `ACTIONS_ID_TOKEN_REQUEST_TOKEN`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY` | Provided by the runner. |

In CI the CLI requests an OIDC token from `ACTIONS_ID_TOKEN_REQUEST_URL` with the server's base URL as audience, and sends it as `Authorization: Bearer <token>`.

## Tool selection

The CLI runs `terraform` or `tofu` according to `tool` in the stack's effective configuration, or `STACKORDER_TOOL` when set. It expects the binary on `PATH`; install it with `hashicorp/setup-terraform` or `opentofu/setup-opentofu`.

## Installing

Releases on `stackorder/stackorder` tags `vX.Y.Z` ship `stackorder_X.Y.Z_<os>_<arch>.tar.gz` (`.zip` on Windows) for `linux`, `darwin` and `windows` on `amd64` and `arm64`, plus `stackorder_X.Y.Z_checksums.txt` with SHA-256 sums. In Actions, the [`setup` action](/reference/actions#setup) downloads and verifies the binary.
