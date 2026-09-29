# CLI

All runner-side logic lives in one static Go binary, `stackorder`. The actions in `stackorder/actions` are thin wrappers around it, and the same binary works on a laptop: `stackorder graph`, `stackorder affected --base main` and `stackorder plan --stack stacks/prod/vpc` behave the same locally and in CI.

The help blocks on this page are the output of `stackorder --help` and `stackorder <command> --help`, copied verbatim from the binary.

## Commands

```text
$ stackorder --help
Order and run Terraform and OpenTofu stacks on GitHub Actions

Usage:
  stackorder [command]

Available Commands:
  affected    Print the stacks a change affects, by wave; exits 2 when any stack is affected
  apply       Apply one stack's plan after confirming the run, its commit and its lock with the server
  check       Record a named policy or cost check verdict on a stack
  drift       Check one stack for drift; exits 2 when it drifted
  graph       Scan the repository and print its dependency graph (--format text, json or dot)
  help        Help about any command
  plan        Plan one stack, summarize and redact the plan, and report it to the server
  resolve     Scan the repository, resolve the affected stacks with the server and write the plan matrix
  unlock      Release orchestration locks through the server, audited
  version     Print the stackorder version

Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
  -h, --help               help for stackorder
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output

Use "stackorder [command] --help" for more information about a command.
```

| Command | In GitHub Actions | On a laptop |
| --- | --- | --- |
| [`resolve`](#resolve) | Scans the repository, diffs base to head, registers the run, posts the graph and writes the matrix to `GITHUB_OUTPUT` | Resolves locally and prints the affected set and waves; use `affected` instead (see the warning under [`resolve`](#resolve)) |
| [`plan`](#plan) | `init`, `plan`, `show -json`, summary, redaction, hooks, result upload | The same; the result is posted only when a server, a run id and an API key are all set |
| [`apply`](#apply) | Confirms the run, its commit and its lock with the server, applies the saved plan, uploads the result | Refused unless `--local`, a server and `STACKORDER_API_KEY` are given; the server then takes the lock |
| [`drift`](#drift) | `plan -detailed-exitcode`, result upload; exits 2 on drift | The same |
| [`check`](#check) | Records a named check verdict, usually from a hook | The same, with `STACKORDER_API_KEY` |
| [`graph`](#graph), [`affected`](#affected) | Inspection | Inspection and debugging; `--format dot` for Graphviz |
| [`unlock`](#unlock) | Not used | Releases orchestration locks through the API, audited; needs `STACKORDER_API_KEY` |
| [`version`](#version) | Prints the version | The same |

The CLI never uploads or downloads workflow artifacts itself. The [`plan` action](/reference/actions#plan) uploads the plan file after `stackorder plan`, and the [`apply` action](/reference/actions#apply) downloads it before `stackorder apply`.

## Global flags {#global-flags}

| Flag | Default | Meaning |
| --- | --- | --- |
| `--server` | `STACKORDER_SERVER_URL` | Server base URL. Empty means local mode. |
| `--repo-root` | `GITHUB_WORKSPACE`, else the git top level, else the current directory | The repository to scan. |
| `-v`, `--verbose` | off | Log at debug level. |
| `--format` | `text` | `text` or `json`; `dot` only for `graph` and `affected`. Any other value, or `dot` on another command, exits 1. |

With `--format json`, standard output carries only the JSON result; Terraform's own output moves to standard error. Logs always go to standard error, as `slog` text lines, or JSON lines with `STACKORDER_LOG_FORMAT=json`.

### Where the facts come from {#context}

In GitHub Actions (`GITHUB_ACTIONS=true`) the CLI reads the repository, commit, event, pull request number, workflow run id and attempt from the `GITHUB_*` variables and the event payload at `GITHUB_EVENT_PATH`:

| Fact | Source |
| --- | --- |
| Repository | `GITHUB_REPOSITORY`, else the payload's `repository.full_name`, else the `origin` remote |
| Commit | The pull request head SHA from the payload for pull request events (`GITHUB_SHA` is the merge commit there), the `sha` input of a dispatched workflow, else `GITHUB_SHA` |
| Pull request | The payload's `pull_request.number`, else `refs/pull/<n>/…` in `GITHUB_REF` |
| Base | The pull request's base SHA, or the `before` commit of a push |
| Run id | `--run-id`, else `STACKORDER_RUN_ID`, else the `run_id` input of a dispatched workflow |
| Default branch | The payload's `repository.default_branch`, else `origin/HEAD` |

Outside Actions the commit is `HEAD`, the repository comes from the `origin` remote (`github.com:owner/repo.git` or `https://github.com/owner/repo`), and the default branch from `origin/HEAD`. Step outputs go to `GITHUB_OUTPUT` and a Markdown summary to `GITHUB_STEP_SUMMARY`, when those variables are set.

### Talking to the server {#server}

In Actions the CLI requests an OIDC token from `ACTIONS_ID_TOKEN_REQUEST_URL` for every call, with the audience `STACKORDER_OIDC_AUDIENCE` or, when that is unset, the server URL exactly as given, and sends it as `Authorization: Bearer <token>`. Outside Actions it sends `STACKORDER_API_KEY` instead.

Network errors, timeouts and 5xx answers are retried three times, after 200 ms, 800 ms and 2 s, each attempt bounded by 30 s. A server that still does not answer is *unreachable*: `resolve`, `plan` and `drift` degrade to a local, `unconfirmed` result, and `apply` refuses (exit 3). A 4xx answer is never retried.

When a result is unconfirmed in Actions, the CLI creates a neutral check run with `GITHUB_TOKEN` (`stackorder/resolve` or `stackorder/plan: <key>`) so the pull request shows why, and writes a warning annotation. Without `GITHUB_TOKEN` it only warns.

## `resolve` {#resolve}

```text
$ stackorder resolve --help
Scan the repository, resolve the affected stacks with the server and write the plan matrix

Usage:
  stackorder resolve [flags]

Flags:
      --base string      base ref to diff against (default: the pull request base, else the merge base with origin/<default branch>)
  -h, --help             help for resolve
      --stacks strings   restrict the run to these stack keys, comma separated

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

Loads `stackorder.yaml`, scans the `stacks.discover` paths and module directories, parses `module` sources and `terraform_remote_state` blocks, and lists the paths changed between the merge base of the base ref and `HEAD`. Only committed changes count.

The base is, in order: `--base`; the pull request's base SHA; the `before` commit of a push; the merge base of `origin/<default branch>` (or `origin/main` when the default branch is unknown) and `HEAD`. Without any of them the command fails with `no base to diff against; pass --base`.

In Actions, with a server, it registers the run (`POST /v1/runs`), uploads the graph (`POST /v1/runs/{id}/graph`) and writes the server's answer. Without a server, or when the server is unreachable, it resolves locally with the same algorithm and marks the result `unconfirmed`. Any other server error fails the command.

::: warning Outside Actions
Outside Actions, `resolve` asks the server only when both a server and `STACKORDER_API_KEY` are set, and the server refuses to register a plan run for an API key (`400 invalid`, "a manual run applies"), so the command exits 1. On a laptop, use [`affected`](#affected), which never talks to the server, or unset `STACKORDER_SERVER_URL` or `STACKORDER_API_KEY` for `resolve`.
:::

For a pull request from a fork it plans nothing: it writes empty outputs with `unconfirmed=true`, a notice, and a neutral `stackorder/resolve` check. A dependency cycle prints each cycle, `cycle: a -> b -> a`, and exits 1.

The text output is the run id, when there is one, and a table:

```text
WAVE  STACK                REASONS      ENVIRONMENT
0     stacks/prod/vpc      module       production
0     stacks/staging/vpc   module       staging
1     stacks/prod/apps     reads_state  production
1     stacks/prod/eks      dependent    production
1     stacks/staging/apps  dependent    staging
```

A stack locked by another pull request gets a fifth column, `locked by PR #41`. `--format json` prints the [`ResolveResponse`](/reference/api#upload-graph).

| Output | Meaning |
| --- | --- |
| `run-id` | The Stackorder run id; empty when resolved locally |
| `matrix` | `{"include": [...]}`, one [matrix entry](/reference/actions#matrix-entry) per affected stack |
| `waves` | The stack keys by wave index, as a JSON array of arrays |
| `affected` | The affected stack keys, as a JSON array, sorted by wave then key |
| `count` | The number of affected stacks |
| `unconfirmed` | `true` when the result was computed locally |

`resolve` exits 0 whether or not stacks are affected.

## `plan` {#plan}

```text
$ stackorder plan --help
Plan one stack, summarize and redact the plan, and report it to the server

Usage:
  stackorder plan --stack <key> [flags]

Flags:
  -h, --help            help for plan
      --out string      plan file to write (default <plan dir>/<artifact>.tfplan)
      --run-id string   server run id (env STACKORDER_RUN_ID)
      --stack string    stack key: path or path:workspace

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

1. Loads the stack's effective configuration and detects the tool (see [Tool selection](#tool)).
2. Runs the `pre-plan` [hook](/configuration/workflows#hooks).
3. Runs `init -input=false -no-color`, with one `-backend-config=<value>` per item of `STACKORDER_BACKEND_CONFIG`, then, for a stack with a workspace, `workspace select -or-create <workspace>`.
4. Runs `plan -input=false -no-color -detailed-exitcode -out=<plan file>`.
5. Writes `show -json` of the plan next to the plan file, as `<artifact>.json`, and builds the summary: adds, changes, destroys, replaces, imports, moves, output changes and the addresses of each.
6. Unless the stack's `plan_output` is `summary`, captures `show -no-color`, redacts it and cuts it at 256 KB.
7. Runs the `post-plan` hook, with `STACKORDER_PLAN_FILE` and `STACKORDER_PLAN_JSON` set.
8. Posts the result to `POST /v1/runs/{id}/stacks/{key}/result`, when a server and a run id are set (and, outside Actions, `STACKORDER_API_KEY`). Otherwise the result is `unconfirmed`.

The plan file is `<plan dir>/<artifact>.tfplan`, where the plan dir is `STACKORDER_PLAN_DIR` (default `.stackorder/plans`, relative to the repository root) and the artifact is `stackorder-plan-<key>-<sha>` with `/` and `:` in the key replaced by `-`. Outside a git checkout the SHA part is `local`.

The text output is one line, `stacks/prod/vpc: 4 to add, 0 to change, 0 to destroy, 0 to replace, 2 output changes`, after Terraform's own output. `--format json` prints `{"stack", "run_id", "plan_file", "unconfirmed", "result"}`, where `result` is the [`StackResult`](/reference/api#stack-result) that was, or would have been, posted.

| Output | Meaning |
| --- | --- |
| `has-changes` | `true` when the plan changes resources or outputs |
| `plan-file` | The absolute path of the plan file |
| `artifact` | The artifact name for the plan file |
| `summary` | The plan summary, as JSON, or `null` when the plan failed |
| `unconfirmed` | `true` when the result was not confirmed by the server |

`plan` exits 0 when the plan succeeds, whether or not it has changes, and also when the server was unreachable and the result is `unconfirmed`. A failed `init`, `plan` or hook exits 1 after the failure is reported. A result the server refuses, such as one for a superseded run, exits 3.

## `apply` {#apply}

```text
$ stackorder apply --help
Apply one stack's plan after confirming the run, its commit and its lock with the server

Usage:
  stackorder apply --stack <key> --run-id <id> [flags]

Flags:
  -h, --help               help for apply
      --local              apply from outside GitHub Actions through a manual run; needs STACKORDER_API_KEY
      --plan-file string   saved plan to apply (default <plan dir>/<artifact>.tfplan)
      --run-id string      server run id (env STACKORDER_RUN_ID)
      --stack string       stack key: path or path:workspace

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

In Actions, before touching anything, `apply` confirms with `GET /v1/runs/{id}` that:

- the run is `planned` or `applying`, and the stack is part of it and `planned` or `applying`;
- the run still holds the stack's orchestration lock (the row's `lock` names this run), so a stack unlocked or taken by another run mid-apply is not applied;
- the run's SHA equals the job's commit (the dispatch `sha` input, falling back to `GITHUB_SHA`) and the checkout's `HEAD`.

Any mismatch, no server, or an unreachable server is a refusal: exit 3, fail closed.

Then it runs `init` and chooses what to apply:

| Situation | What happens |
| --- | --- |
| The plan file exists and `apply.from_plan` is `true` (the default) | The saved plan is applied. |
| The plan file exists and `apply.from_plan` is `false` | The stack is planned again, and applied only if the new plan's resource address set equals the one recorded in the run. |
| The plan file is missing, such as an expired artifact | The same re-plan and comparison, with a warning. |

A re-plan whose addresses differ, or a run that recorded no summary to compare with, is refused with exit 3 and the differing addresses. The `pre-apply` hook runs before `apply -input=false -no-color <plan file>`, and the `post-apply` hook after it. Once the run is confirmed, the result is posted whatever happens: a failed apply exits 1 after reporting, a failed post after a successful apply exits 1, and so does a failing `post-apply` hook.

**`--local`.** Outside Actions, `apply` needs `--local`, a server and `STACKORDER_API_KEY`, and takes neither `--run-id` nor `--plan-file`. It starts a manual run with `POST /v1/runs` (`trigger: manual`, `mode: apply`, `stacks: [<key>]`), which makes the server take the stack's lock before it answers, plans afresh, applies, and posts the result, which releases the lock. It warns when the working tree has uncommitted changes, because it applies what is on disk. An unreachable server is a refusal, exit 3.

| Output | Meaning |
| --- | --- |
| `summary` | The applied plan's summary, as JSON |

## `drift` {#drift}

```text
$ stackorder drift --help
Check one stack for drift; exits 2 when it drifted

Usage:
  stackorder drift --stack <key> [flags]

Flags:
  -h, --help            help for drift
      --run-id string   server run id (env STACKORDER_RUN_ID)
      --stack string    stack key: path or path:workspace

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

Runs `init` and `plan -detailed-exitcode` into a temporary plan file, summarizes it, and posts the result like `plan` does. It runs no hooks. The text output is `stacks/prod/vpc: no drift` or `stacks/prod/vpc: drifted: …`.

| Output | Meaning |
| --- | --- |
| `drifted` | `true` when the plan found changes |
| `summary` | The plan summary, as JSON |

`drift` exits 0 without drift, 2 with drift, and 1 when the check itself failed. The [`drift` action](/reference/actions#drift) turns exit 2 into a successful step.

## `check` {#check}

```text
$ stackorder check --help
Record a named policy or cost check verdict on a stack

Usage:
  stackorder check --stack <key> --run-id <id> --name <n> --status pass|fail|warn [flags]

Flags:
      --details-file string   file whose contents are sent as the details
      --details-url string    link to the full report
  -h, --help                  help for check
      --name string           check name, shown as stackorder/<name>: <stack>
      --run-id string         server run id (env STACKORDER_RUN_ID)
      --stack string          stack key: path or path:workspace
      --status string         verdict: pass, fail or warn
      --summary string        one line summary

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

Posts a named verdict to `POST /v1/runs/{id}/stacks/{key}/checks/{name}`, usually from a [hook](/configuration/workflows#named-checks). It needs a server, a run id (`--run-id`, `STACKORDER_RUN_ID` or the dispatch input) and, outside Actions, `STACKORDER_API_KEY`. `--summary` is cut at 4 KB and the contents of `--details-file` at 64 KB, both redacted.

The server shows the verdict as the check run `stackorder/<name>: <key>`, `pass` as success, `warn` as neutral and `fail` as failure, and the apply gate honours it: `pass` and `warn` pass, `fail` refuses. Names are letters, digits, `-`, `_` and `.`, up to 64 characters; `resolve`, `plan` and `apply` are reserved.

The text output is `stackorder/policy: stacks/prod/vpc: pass`; `--format json` prints the stored [`Check`](/reference/api#check-verdict).

## `graph` {#graph}

```text
$ stackorder graph --help
Scan the repository and print its dependency graph (--format text, json or dot)

Usage:
  stackorder graph [flags]

Flags:
  -h, --help   help for graph

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

Scans the repository and prints the dependency graph. It needs no server and no credentials.

```text
6 stacks, 3 modules, 7 edges

stacks/prod/apps
  reads_state  stacks/prod/vpc (inferred)

stacks/prod/eks
  depends_on   stacks/prod/vpc
  uses_module  stackorder/example-infra//modules/eks

stackorder/example-infra//modules/eks (local module)
  uses_module  stackorder/example-infra//modules/common
```

Each node lists its outgoing edges, pointing at what it depends on. Git module edges show their `@ref`; cross-repository stacks are marked `(external)`. `--format json` prints the [`Graph`](/reference/api#upload-graph), and `--format dot` writes Graphviz:

```sh
stackorder graph --format dot | dot -Tsvg > graph.svg
```

## `affected` {#affected}

```text
$ stackorder affected --help
Print the stacks a change affects, by wave; exits 2 when any stack is affected

Usage:
  stackorder affected --base <ref> [flags]

Flags:
      --base string   base ref to diff HEAD against (default: the pull request base, else the merge base with origin/<default branch>)
  -h, --help          help for affected

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

Resolves locally, like `resolve` without a server, and prints the same table. `--format json` prints the `ResolveResponse`; `--format dot` prints the graph with the affected stacks highlighted and labelled with their wave. It never talks to the server and writes no step outputs.

`affected` exits 2 when at least one stack is affected, 0 when none is, and 1 on a cycle or an error.

## `unlock` {#unlock}

```text
$ stackorder unlock --help
Release orchestration locks through the server, audited

Usage:
  stackorder unlock <key>... [flags]

Flags:
      --force-state     release a lock left by a job that died mid-apply
  -h, --help            help for unlock
      --reason string   why the lock is released, recorded in the audit log

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

Releases the orchestration lock on each named stack with `POST /v1/unlock`, whoever holds it. It needs a server and `STACKORDER_API_KEY`, and a repository it can tell from `GITHUB_REPOSITORY` or the `origin` remote. Every release is audited, and the pull request that held the lock gets a comment.

```text
released stacks/prod/vpc (run 5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b, PR #41, taken 2026-09-27T16:02:11Z)
stacks/prod/apps: no lock held
```

`--force-state` does not change what is released: it marks the release as following a runner that died mid-apply, and the comment on the pull request asks for the S3 state lock to be checked. Stackorder never touches the state lock. `--format json` prints one [`UnlockResponse`](/reference/api#unlock-by-key) with every released lock.

## `version` {#version}

```text
$ stackorder version --help
Print the stackorder version

Usage:
  stackorder version [flags]

Flags:
  -h, --help   help for version

Global Flags:
      --format string      output format: text, json, or dot where it applies (default "text")
      --repo-root string   repository root (default GITHUB_WORKSPACE, else the git top level)
      --server string      server base URL; empty means local mode (env STACKORDER_SERVER_URL)
  -v, --verbose            log debug output
```

```text
stackorder 1.0.0 (a1b2c3d, 2026-09-28T09:00:00Z, go1.26.0, linux/amd64)
```

`--format json` prints `{"version", "commit", "date", "go", "platform"}`.

## Exit codes {#exit-codes}

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Error: bad flags, a failed Terraform command or hook, a dependency cycle, a server error other than a refusal. |
| `2` | Changes found: `affected` found affected stacks, `drift` found drift. |
| `3` | Refused: the server answered `forbidden`, `conflict`, `refused`, `locked` or `superseded`, or `apply` failed closed. |

[Exit codes](/reference/exit-codes) has the full table, command by command.

## Environment {#environment}

| Variable | Purpose |
| --- | --- |
| `STACKORDER_SERVER_URL` | Server base URL, the default of `--server`; unset means local mode. |
| `STACKORDER_API_KEY` | Automation key for `apply --local`, `unlock`, `check`, and result posting outside Actions. |
| `STACKORDER_OIDC_AUDIENCE` | Audience requested for the runner token; defaults to the server URL exactly as given. It must equal the server's `STACKORDER_OIDC_AUDIENCE`. |
| `STACKORDER_RUN_ID` | Run id from the resolve step or the dispatch input, the default of `--run-id`. |
| `STACKORDER_TOOL`, `STACKORDER_TOOL_VERSION` | Override the configured tool and the expected version. |
| `STACKORDER_TERRAFORM_BIN`, `STACKORDER_TOFU_BIN` | The binary to run, as a name on `PATH` or a path. |
| `STACKORDER_BACKEND_CONFIG` | Extra `-backend-config` values for `init`, comma separated. |
| `STACKORDER_PLAN_DIR` | Where plan files are written, default `.stackorder/plans`; relative paths are taken from the repository root. |
| `STACKORDER_LOG_FORMAT` | `json` switches the logs on standard error to JSON. |
| `GITHUB_ACTIONS`, `GITHUB_*`, `ACTIONS_ID_TOKEN_REQUEST_URL`, `ACTIONS_ID_TOKEN_REQUEST_TOKEN`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY` | Provided by the runner. |
| `GITHUB_TOKEN` | Used only for the neutral fallback checks. |

Terraform and OpenTofu always run with `TF_IN_AUTOMATION=1`, `TF_INPUT=0` and `CHECKPOINT_DISABLE=1`, on top of the process environment.

## Tool selection {#tool}

The CLI runs `terraform` or `tofu` according to `tool` in the stack's effective configuration (default `terraform`), or `STACKORDER_TOOL` when set. It expects the binary on `PATH`, or in `STACKORDER_TERRAFORM_BIN` / `STACKORDER_TOFU_BIN`; install it with `hashicorp/setup-terraform` or `opentofu/setup-opentofu`. It runs `version -json` first, and warns when the binary is the other tool or does not match `tool_version`.

## Secrets {#secrets}

Everything the CLI sends to the server, the step summary or a fallback check (plan text, error text, check summaries and details) is redacted first. The redactor masks, as `***`:

- the bodies of PEM private key blocks, JSON web tokens, AWS access key ids and secret access key assignments, GitHub tokens (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`) and Slack tokens;
- quoted values assigned to names ending in `password`, `passwd`, `secret` or `token`, and `…PASSWORD=`, `…SECRET=` or `…TOKEN=` assignments;
- the value of every environment variable whose name has a `SECRET`, `TOKEN`, `PASSWORD`, `PASSWD`, `PASSPHRASE`, `APIKEY`, `API_KEY`, `PRIVATE_KEY` or `CREDENTIAL(S)` component, such as `GITHUB_TOKEN` or `DB_PASSWORD`, unless it is shorter than 4 characters, a boolean or a number.

In Actions those environment values are also registered with `::add-mask::` before Terraform runs, and the output Terraform streams to the job log goes through the same redactor. Values marked `sensitive` are already masked by Terraform itself.

## Installing

Releases on `stackorder/stackorder` tags `vX.Y.Z` ship `stackorder_X.Y.Z_<os>_<arch>.tar.gz` (`.zip` on Windows) for `linux`, `darwin` and `windows` on `amd64` and `arm64`, plus `stackorder_X.Y.Z_checksums.txt` with SHA-256 sums. In Actions, the [`setup` action](/reference/actions#setup) downloads and verifies the binary. From a checkout, `make build-cli` writes `bin/stackorder`.
