# Architecture

This file is the contract between the packages of this repository and the
`stackorder/actions` and `stackorder/example-infra` repositories. The
[design document](https://claude.ai/artifact/W3gQnvGu5Fw9DSXApYE766) is the
source of truth for behaviour; this file pins the shapes, names and library
choices that let packages be built independently and still fit together,
and records where the implementation deliberately departs from the design.
Change it in the same commit as the code it describes.

## Repositories

| Repository | Contents |
| --- | --- |
| `stackorder/stackorder` | Go module `github.com/stackorder/stackorder`: server, CLI, embedded UI, migrations, docs site, Terraform deployment module |
| `stackorder/actions` | `setup` JavaScript action, `resolve` / `plan` / `apply` / `drift` composite actions, reusable `plan.yml` and `run.yml` |
| `stackorder/example-infra` | Demo monorepo used by the end-to-end tests |

## Layout of this repository

```text
api/v1/                    wire types shared by CLI, server, UI and tooling (JSON + YAML tags, no logic)
cmd/stackorder/            CLI main; binds internal/scan and internal/graph into internal/cli
cmd/stackorder-server/     server main; wiring only, plus the `healthcheck` subcommand
internal/config/           stackorder.yaml and .stackorder.yaml: defaults, validation, per-stack merge
internal/scan/             HCL scanning: discover stacks, parse backends, module sources, remote state; git diff
internal/graph/            pure resolution algorithm: affected set, propagation, waves, cycles, DOT output
internal/report/           pure rendering of check-run output, the sticky PR comment, refusals, drift issues
internal/command/          parsing of `stackorder …` PR comment commands
internal/tf/               terraform / tofu process wrapper, plan JSON summary, redaction, hooks
internal/client/           HTTP client for the server API used by the CLI, incl. runner OIDC token fetch
internal/cli/              cobra commands: resolve, plan, apply, drift, check, graph, affected, unlock, version
internal/store/            pgx queries; migrations embedded from /migrations (package `migrations`, `migrations.FS`)
internal/gh/               GitHub App client: JWT, installation tokens, checks, comments, dispatch, reviews, teams
internal/gh/codeowners/    CODEOWNERS parsing with GitHub's matching semantics
internal/oidc/             verification of GitHub Actions OIDC tokens and claim binding
internal/principal/        caller identity and the error vocabulary shared by runs and api
internal/runs/             run state machine, apply gate, locks, wave dispatch, superseding, drift, reconciliation
internal/webhook/          HMAC verification, delivery dedup, event persistence
internal/worker/           queue: claim events and jobs with SKIP LOCKED, retries, idempotent handlers
internal/sched/            cron scheduler for drift and housekeeping; leader by advisory lock
internal/metrics/          Prometheus registry, all metric names, HTTP and GitHub client instrumentation
internal/api/              HTTP handlers, auth middleware, sessions and OAuth, API keys, /setup manifest flow
internal/ui/               embed.FS serving of the built UI with SPA fallback
internal/server/           composition of all of the above into one http.Server; used by cmd and by tests
internal/artifacts/        optional S3 store for full plan text (STACKORDER_ARTIFACT_BUCKET)
internal/testutil/pgtest/  Postgres for tests: TEST_DATABASE_URL or testcontainers, one database per test
internal/testutil/ghfake/  in-memory fake of the GitHub API with inspection and payload builders
internal/testutil/oidcfake/ fake OIDC issuer and runner token endpoint
internal/testutil/faketf/  fake terraform and tofu binary answering per stack from a JSON config, for CLI end-to-end tests
migrations/                golang-migrate SQL files, NNNN_name.up.sql / .down.sql, embedded as migrations.FS
ui/                        Preact + Vite + TypeScript app; `npm run build` writes internal/ui/dist
docs/                      VitePress documentation site
deploy/terraform/          ECS Fargate + RDS + ALB module; examples/ and tests/
test/integration/          server + Postgres + fake GitHub + the CLI on faketf, whole-flow tests (build tag `integration`)
test/e2e/                  real terraform + LocalStack + example-infra + server (build tag `e2e`)
Dockerfile                 multi-stage: node (ui) -> go -> gcr.io/distroless/static:nonroot
.goreleaser.yaml           CLI releases for linux/darwin/windows, amd64/arm64, checksums; server image
docker-compose.yml         local Postgres and LocalStack for development and tests
```

Dependency direction: `api/v1` <- `config` <- `scan`, `graph`, `command`,
`report`, `tf` <- `client`, `cli` (runner side); `api/v1` <- `store`, `gh`,
`oidc` <- `principal` <- `runs` <- `webhook`, `worker`, `sched`, `api` <-
`server` <- `cmd`. `runs` talks to GitHub through its `GitHub` interface
(`Client`, `ListInstallations`, `InstallationRepos`), satisfied by `*gh.App`. Nothing under `internal/` imports `internal/cli` or
`internal/server` except `cmd` and tests. `internal/graph`, `internal/report`
and `internal/command` import nothing but `api/v1`, `internal/config` and
the standard library (plus the YAML, glob and cron libraries config pulls
in). `internal/runs` never imports `internal/api` and vice versa; they share
`internal/principal`.

## Libraries

These are fixed. Do not introduce an alternative for the same job.

| Job | Library |
| --- | --- |
| HTTP routing | `net/http` ServeMux with method and wildcard patterns (`"POST /v1/runs/{id}/graph"`) |
| Postgres | `github.com/jackc/pgx/v5` with `pgxpool`; no ORM, no sqlx |
| Migrations | `github.com/golang-migrate/migrate/v4` with the `pgx/v5` database driver and the `iofs` source over an embedded FS |
| YAML | `gopkg.in/yaml.v3` |
| HCL | `github.com/hashicorp/hcl/v2` (`hclparse`, `hclsyntax`) and `github.com/hashicorp/terraform-config-inspect/tfconfig` |
| Plan JSON | `github.com/hashicorp/terraform-json` |
| Globs | `github.com/bmatcuk/doublestar/v4` |
| JWT | `github.com/golang-jwt/jwt/v5` (App JWT signing and OIDC token parsing) |
| Cron | `github.com/robfig/cron/v3` parser and scheduler |
| CLI | `github.com/spf13/cobra` |
| Metrics | `github.com/prometheus/client_golang` |
| Tracing | `go.opentelemetry.io/otel` with the OTLP HTTP exporter, enabled only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set |
| S3 (optional artifact bucket only) | `github.com/aws/aws-sdk-go-v2` `service/s3` and `config` |
| Logging | `log/slog`. Server: JSON handler unless `STACKORDER_LOG_FORMAT=text`. CLI: text handler unless `STACKORDER_LOG_FORMAT=json` |
| IDs | `github.com/google/uuid` |
| Tests | standard `testing`, `github.com/stretchr/testify/require` and `assert`, `github.com/google/go-cmp/cmp` for diffs |
| Postgres in tests | `github.com/testcontainers/testcontainers-go/modules/postgres`, skipped when `TEST_DATABASE_URL` is set and used directly instead |
| GitHub API | hand written in `internal/gh` on `net/http`; no go-github |
| UI | Preact, TypeScript, Vite, `preact-iso` router, `@dagrejs/dagre` layout with hand written SVG, Vitest and Testing Library, Playwright for browser tests |
| Docs | VitePress, `vitepress-plugin-mermaid` with `mermaid` 11 |
| Actions repo | `@actions/core`, `@actions/tool-cache`, `@actions/http-client`, esbuild, TypeScript, ESLint with typescript-eslint, Vitest |
| Terraform module providers | `hashicorp/aws` ~> 6.0, `hashicorp/random` ~> 3.6, `hashicorp/http` ~> 3.4 (webhook IP ranges only) |

Go version is the one in `go.mod`; `GOTOOLCHAIN=auto` downloads it. Node 24.
`go.mod` carries `ignore node_modules` so `./...` skips the UI and docs
dependencies.

## Identities and names

- Stack key inside a repo: `path` or `path:instance` (`v1.StackKey(path,
  instance)`, `v1.SplitStackKey`). Paths are repository relative, slash
  separated, no `./`, no trailing `/`. The suffix names an instance of the
  directory (see [Stack instances](#stack-instances)); it is never, by
  itself, a Terraform workspace. A stack that sets `workspace: blue` and
  declares no instances is one instance named `blue`, keyed `path:blue`,
  whose Terraform workspace is `blue`.
- Qualified stack key: `owner/repo//key`. A cross-repo `depends_on` target
  becomes a `v1.Stack` with `External: true`, `Repo` set, `Path` and
  `Instance` split out, and `Key` set to the qualified key, so it can never
  collide with a local stack; the edge's `To.Key` is that qualified key.
  External stacks are never scheduled.
- Module keys: see `api/v1/doc.go`. `uses_module` edge `Meta` always has
  `ref` (empty for local modules) and `source` (the raw spelling at that
  call). The store keeps a ref-less family row per module (`modules.base_key`)
  that module versions attach to; `v1.ModuleDetail.Key` and
  `v1.ModuleConsume.ModuleKey` are the family key without `@ref`, while
  graph nodes and `v1.ModuleConsumer.Ref` carry the pinned ref.
- Inferred `reads_state` edges have `Inferred: true` and `Meta` `bucket` and
  `key` (the full S3 object key, workspace prefix included). A stack's
  `ignore_inferred` suppresses the edge in the scanner with a warning; an
  explicit `depends_on` to the same target replaces it.
- Resolution rules the graph package fixes: a changed path belongs to the
  deepest enclosing stack directory only, and affects every workspace of that
  directory; paths under `.terraform` are ignored; git and registry modules
  never match changed paths; edge direction is `From` depends on `To`, so
  `To` applies first and `wave(To) < wave(From)`.
- Server side primary keys are UUIDs (`runs.id`, `stacks.id`, `modules.id`);
  GitHub ids (`installations.id`, `repos.id`) are the GitHub numeric ids.
- Run ids appear in URLs, workflow inputs and check run output as the UUID
  string.
- Check run names: `stackorder/resolve`, `stackorder/plan`, `stackorder/plan: <key>`,
  `stackorder/apply`, `stackorder/apply: <key>`, `stackorder/<check-name>: <key>`
  for named policy checks.
- Sticky PR comment: one per PR, found by the hidden marker
  `<!-- stackorder:sticky -->` on its first line (and by the App's own login).
- Plan artifact name: `v1.PlanArtifactName(key, sha)`, i.e.
  `stackorder-plan-<slug>-<sha>`, where `v1.StackKeySlug(key)` is the key
  with `/` and `:` replaced by `-`, then `-` and the first 8 hex characters
  of `sha256(key)`, so keys such as `a/b` and `a-b` never share a name
  (the design's name has no hash suffix). The plan file
  inside it is `<artifact name>.tfplan`, written under `STACKORDER_PLAN_DIR`
  (default `.stackorder/plans`) and reported as an absolute path.
- Workflow files in user repos: `.github/workflows/stackorder-plan.yml` and
  `.github/workflows/stackorder-run.yml` (id `stackorder-run.yml` is what the
  server dispatches). `stackorder-run.yml` inputs: `run_id`, `mode`
  (`plan`, `apply` or `drift`), `wave`, `sha` (commit to check out) and
  `stacks` (JSON array of `v1.MatrixEntry`). The wrapper sets
  `run-name: stackorder ${{ inputs.mode }} ${{ inputs.run_id }} wave ${{ inputs.wave }}`
  so the server can correlate the `workflow_run` event with its dispatch.
- Stacks with no environment mapping run under `v1.DefaultEnvironment`
  (`default`), never under an empty environment name. Server-dispatched
  `plan` and `drift` jobs always run under `default` and assume the plan
  role; only `apply` dispatches carry the stack's environment.
- Hooks `.stackorder/hooks/{pre-plan,post-plan,pre-apply,post-apply}.sh` are
  run by the CLI itself, in CI and locally, with `STACKORDER_STACK`,
  `STACKORDER_STACK_PATH`, `STACKORDER_INSTANCE`, `STACKORDER_RUN_ID`,
  `STACKORDER_PLAN_JSON` and `STACKORDER_PLAN_FILE` (both file paths) set,
  plus the stack's configured `env` variables for the mode (see
  [Stack instances](#stack-instances)). The design assigns hooks to the
  reusable workflow; the CLI owns them so local and CI behaviour match.
- Release assets on `stackorder/stackorder` tags `vX.Y.Z`:
  `stackorder_X.Y.Z_<os>_<arch>.tar.gz` (`.zip` on windows) containing the
  `stackorder` binary, `os` in `linux`, `darwin`, `windows`, `arch` in
  `amd64`, `arm64`, plus `stackorder_X.Y.Z_checksums.txt` (sha256). The
  server image is `ghcr.io/stackorder/stackorder:X.Y.Z` and `:latest`.

## Stack instances

A stack directory can be deployed several times, once per **instance**: the
same code with its own state object, var files, environment variables and
GitHub environment. Every instance is a stack of its own everywhere a stack
appears: the graph, affected sets, waves, locks, runs, checks, artifacts,
drift and the UI. Nothing below needs the server to read the repository;
the CLI renders everything from the checkout and the server re-resolves
policy from the default branch with the same pure functions.

### Identity

- `v1.Stack.Instance` is the key suffix. `v1.Stack.Workspace` is the
  Terraform workspace the CLI selects after `init`; empty means none.
  `v1.RunStack`, `v1.AffectedStack`, `v1.StackDetail` and `v1.MatrixEntry`
  carry `instance` next to `workspace`. `v1.Stack.WatchPaths` lists
  repository-relative files that the stack reads at `init` or `plan`
  (backend config files, var files) and that its own directory does not
  own under the deepest-directory rule, so a file inside a nested stack
  directory is a watch path of the enclosing stack that reads it.
- Compatibility: an object whose `Instance` is empty but whose `Workspace`
  is set came from a CLI older than this contract; readers treat the
  workspace as the instance. Nothing derives `Workspace` from a key any
  more (`graph/resolve.go`, `graph/matrix.go`, `graph/validate.go`,
  `runs/dispatch.go`, `runs/resolve.go`, `scan.go`, `store/graphs.go` use
  `Instance`).
- Instance names match `[A-Za-z0-9][A-Za-z0-9._-]*`, are at most 64
  characters, and are never `default`. Job-name matching, comment parsing,
  artifact names and GitHub environment names all tolerate that alphabet.
- The store adds no column: `stacks.key` carries the instance and
  `store.Stack.ToV1` splits it out; `stacks.workspace` stays the Terraform
  workspace.

### Configuration

Root `stackorder.yaml` has these instance keys:

```yaml
stacks:
  exclude: ["infra/state-backend"]              # directory globs, never stacks
  instances:
    from_var_files: "workspaces/*.tfvars.json"  # stack-relative glob
backend_config:                                 # -backend-config values for init; at the root only
  - infra/state.s3.tfbackend                    # a file, repository relative        when every stack
  - 'key={{ trimPrefix "infra/" .Path }}/{{ .Instance }}.tfstate'  #                 has a partial backend
var_files: []                                   # -var-file values for plan, stack relative
env:                                            # for the tool and the hooks
  TF_VAR_environment: "{{ .Instance }}"
  TF_VAR_role: { plan: reader, apply: deployer }  # per mode; drift falls back to plan
environments:
  "infra/": "infra-{{ .Instance }}"             # prefix, prefix:instance or :instance
```

`.stackorder.yaml` has `instances`, `backend_config`, `var_files` and
`env`. `instances` is a list of names or a map from name to overrides;
JSON always uses the map form (`v1.StackConfig.Instances`, custom
(un)marshalling). An override (`v1.InstanceConfig`) may set `environment`,
`workspace`, `backend_config`, `var_files`, `env`, `plan_output`,
`apply.allowed_teams`, `depends_on` and `ignore_inferred`. `workspace` on
a stack with instances is a template such as `"{{ .Instance }}"`.

- The instance set of a directory (`config.InstanceNames(root, path,
  stack, matchedVarFiles) ([]string, error)`, with `matchedVarFiles` from
  `config.MatchVarFiles(root, dir)`) is, in order of precedence: the names
  in `instances`; else one per file matched by `from_var_files`, named by
  the file's base name up to its first `.`, sorted; else `[workspace]` when
  the stack's rendered `workspace` is neither empty nor `default` (the
  single workspace instance; the template renders with an empty `.Instance`);
  else one instance with an empty name and a suffix-less key. A derived name that
  is not a valid instance name (`default.tfvars.json`, say), two matched
  files that derive the same name, and a stack workspace that renders to
  an invalid instance name are errors naming the file; a matched file whose
  derived name is not in an explicit `instances` list is a warning
  (`config.VarFileWarnings`) and is not used. Matches are ignored when
  `from_var_files` is unset.
- Var files of an instance, in order: root `var_files`, stack `var_files`,
  the matched `from_var_files` file whose derived name equals the instance
  name, if any, then instance `var_files`, so that a later, more specific
  file wins in Terraform. Paths are relative to the stack directory.
  `backend_config` concatenates root, stack, instance; a value containing
  `=` is `name=value`, anything else is a file path relative to the
  repository root. `env` merges per variable name, instance over stack over
  root; a value is a string or an object with `plan`, `apply` and `drift`
  keys; `drift` falls back to `plan`, and a mode with no value leaves the
  variable as the job already has it. The re-plan inside `apply` runs with
  the `apply` values. `depends_on` and `ignore_inferred` concatenate stack
  and instance lists.
  `workspace`, `plan_output`, `tool` and `tool_version` take the most
  specific value set; `apply.allowed_teams` takes the most specific
  non-empty list.
- Templates are Go `text/template` with `missingkey=error`, data `.Path`,
  `.Name` (the last path segment), `.Instance` and `.Key`, and functions
  `trimPrefix prefix s`, `trimSuffix suffix s`, `base`, `dir`, `replace old
  new s`, `lower` and `upper`, plus the builtins `and`, `or`, `not`, `eq`,
  `ne`, `lt`, `le`, `gt` and `ge`, and `if` and `with`; `range`, `define`,
  `template`, `block` and every other builtin are refused, and a template
  or function whose output exceeds `config.MaxRenderedLength` (4096
  bytes) fails, so a configuration value cannot stall the server. They
  apply to `environments` values, `environment`, `workspace`,
  `backend_config`, `var_files`, `env` values, `depends_on` and
  `ignore_inferred`. A string without `{{` is used as is.
  A rendering error is a validation error of the file that holds the
  template. `config.Render` is the one implementation.
- `environments` keys are `prefix`, `prefix:instance` or `:instance`. A
  key matches when its prefix matches the path (whole segments;
  an empty prefix, allowed only with an instance part, matches every path)
  and, when it has an instance part, that part equals the instance. The
  most specific key wins: one with an instance part before one without,
  then the longest prefix. The instance part must be a valid instance name;
  a prefix may not contain `:`, and two keys whose prefixes normalise to
  the same path (`infra`, `infra/`, `./infra`) with the same instance part
  are an error.
  (The actions' `aws-role-arn-map` deliberately has no `prefix:instance`
  form; an exact `path:instance` key covers that case there.)
- The GitHub environment of a stack is, in order: the instance override,
  the stack's `environment`, the `environments` match (the first of these
  that renders non-empty; `Effective.EnvironmentConfigured` reports that
  one did), the instance name, `v1.DefaultEnvironment`. An instance is
  therefore protected by the GitHub environment of its own name unless
  mapped elsewhere. A `workspace: blue` stack without instances therefore
  applies under `blue`; it sets `environment: default` to apply under
  `default`.
- `config.Resolve(root, path, stack, instance) (Effective, error)` is pure
  and, through `config.ResolveMatched(root, path, stack, instance,
  matchedVarFiles)`, which the scanner and the CLI call to add the matched
  var file, is the one place that merges and renders. `path` carries no
  suffix and an instance `default` means none. `Effective` carries
  `Instance`, `EnvironmentConfigured`, `BackendConfig`, `VarFiles` and
  `Env` (rendered, per mode; `EnvFor(mode)`). It does not need to know
  where an instance came from: the server calls it with the instance it
  splits off a key. `config.EnvironmentFor` returns the raw, unrendered
  `environments` value and never decides an environment. Environment
  variable names match `[A-Za-z_][A-Za-z0-9_]*` and may not start with
  `STACKORDER_`, `GITHUB_`, `ACTIONS_` or `RUNNER_`, nor be `PATH` or
  `HOME`, compared without regard to case; `default` is refused as an
  instance name in any letter case, since GitHub environment names ignore
  case. A null list item under `instances` or a null `env` value is an
  error.
- Validation is strict (`KnownFields`): an unknown key is an error.

### Scan and graph

- `stacks.exclude` beats both `stacks.discover` and `stacks.include`.
- The scanner globs `from_var_files` in each stack directory, computes the
  instance set and calls `addStack` once per instance.
- The effective `v1.Backend` of an instance starts from the literals of the
  `backend "s3"` block and overlays the rendered `backend_config` entries in
  order: a file is parsed as HCL attributes and `name=value` sets one
  attribute; only `bucket`, `key`, `region`, `dynamodb_table`,
  `use_lockfile` and `workspace_key_prefix` are read, everything else is
  ignored. A missing file is a scan error. Inferred `reads_state` edges and
  the shared-state-object warning therefore work per instance, including
  two instances that render the same key.
- `WatchPaths` holds the backend config files and the var files that the
  stack's own directory does not own, sorted and unique. `TreeHash` covers
  `*.tfvars`, `*.tfvars.json` and `*.tfbackend` files, and every
  backend config file, var file and `from_var_files` match the scan read,
  whatever its name. A changed path equal to a stack's watch path affects
  that stack with reason `watch_path`.
- A rendered `depends_on` or `ignore_inferred` entry without a suffix that
  names a directory with instances resolves to the instance of the same
  name when the target has one, else to the target's only stack when it
  has exactly one; a `depends_on` entry that resolves to nothing is kept as
  written with a warning, as any unknown target is today, and an
  `ignore_inferred` entry that resolves to nothing suppresses nothing. A
  cross-repository entry is kept as written; it names its instance itself,
  with a template if it wants the same one.
- A listed var file that does not exist is a scan warning; the CLI errors
  at plan.
- Affected reasons, in order: `changed`, `watch_path`
  (`v1.ReasonWatchPath`), `module`, `reads_state`, `dependent`,
  `requested`.

### CLI

- `--stack path:instance`, help text `stack key: path or path:instance`.
  With instances, declared in `instances` or derived from `from_var_files`,
  the suffix must name one and a bare `path` is an error listing them; with
  none, the suffix is a Terraform workspace (equal to `workspace` when set,
  ad hoc otherwise, and a valid instance name either way). `check --stack` resolves the key the same way,
  but only warns when the stack cannot be loaded for another reason, so a
  verdict can still be posted from a job without a checkout.
- `init` passes `Effective.BackendConfig`, files resolved to absolute paths
  against the repository root, followed by `STACKORDER_BACKEND_CONFIG`, and
  `-reconfigure` whenever `Effective.BackendConfig` is not empty, so two
  instances can share one checkout; in that case it also resets the
  selected workspace to the instance's own (or `default`), since the
  checkout's `.terraform` may have been left on another instance's.
- `plan`, the re-plan inside `apply` and `drift` pass one `-var-file` per
  `Effective.VarFiles`; each must exist, and the error names it.
  `apply <planfile>` passes none. Plan-time values of non-ephemeral variables are
  frozen in the plan file; only `ephemeral` variables take the `apply`
  value of `env` on an apply from a saved plan.
- The tool process and the hooks get `EnvFor(mode)` after the automation
  variables, and `STACKORDER_STACK` (the key), `STACKORDER_STACK_PATH` and
  `STACKORDER_INSTANCE`. Values of configured variables whose names match
  the secret pattern of `envSecrets` join the redaction set.

### Server

- Apply-time environment, `allowed_teams` and `plan_output` come from
  `config.Resolve(defaultBranchRoot, path, defaultBranchStackConfig,
  instance)`, never from a field read off the raw file. The stack config
  is nil when the default branch has no stack file in that directory; the
  environment recorded on the plan row is never used. Dispatch grouping
  by environment, binding and deployment protection work as for any stack.
- Runs whose keys share a directory are independent: separate locks,
  separate concurrency groups, separate artifacts.

### Actions

- `aws-role-arn-map` keys are `prefix/`, `path:instance` (an
  exact key) or `:instance` (that instance in any directory). Precedence:
  exact key, then `:instance`, then the longest prefix, then
  `aws-role-arn`. Plan and drift jobs keep the single plan role.
- New input `aws-role-session-name` on both reusable workflows: a name or
  a JSON object with `plan`, `apply` and `drift` keys, passed as
  `role-session-name` after sanitising to `[\w+=,.@-]` and 64 characters.
- The runner's credentials model for one OIDC bootstrap role and a
  provider `assume_role` chosen by an `env` variable is documented, not
  coded: the plan and apply roles are the two OIDC roles the workflows
  already take, the provider's role is `TF_VAR_<name>` with a `plan` and an
  `apply` value, and the variable is `ephemeral` so the apply value is used
  when applying a saved plan.

## Actions repository interface

Composite actions `resolve`, `plan`, `apply`, `drift` take `stack` (except
resolve), `run-id`, `server-url` (required everywhere), `working-directory`
and `github-token` (default `github.token`, used only for the CLI's neutral
fallback checks; not on apply). `plan` adds `upload-artifact` and
`retention-days` (5); `apply` adds `plan-run-id` and `artifact`. `resolve`
outputs `matrix`, `waves`, `affected` (JSON array of stack keys), `count`,
`run-id`, `unconfirmed`, `stackorder-version`.

Reusable `plan.yml` inputs: `server-url` (required), `aws-role-arn`,
`aws-role-arn-map`, `aws-role-session-name`, `aws-region` (us-east-1),
`tool`, `tool-version`,
`stackorder-version`, `runner`, `max-parallel`, `working-directory`,
`base-ref`, `stacks`. Its `resolve` job needs `pull-requests: read` in
addition to the design's four permissions; the caller's job must carry the
permissions block because a called workflow cannot raise them. Fork pull
requests are skipped with a step summary; the server posts the neutral check.

Reusable `run.yml` inputs: `run-id`, `mode`, `wave`, `sha`, `stacks`
(required), `server-url` (required), `aws-role-arn-map`, `aws-role-arn`,
`aws-plan-role-arn`, `aws-role-session-name`, `aws-region`, `tool`,
`tool-version`, `stackorder-version`, `runner`, `max-parallel`,
`working-directory`. Every job runs under
`environment: ${{ matrix.environment }}` and in concurrency group
`stackorder-stack-<key>` without cancel-in-progress. Role selection: `apply` uses the first match of
`aws-role-arn-map`, whose keys are `prefix/`, `path:instance` or
`:instance`, in the order exact key, `:instance`, longest prefix (a key
with `:` is never a prefix), falling back to `aws-role-arn`; `plan` and `drift` use `aws-plan-role-arn`, falling
back to `aws-role-arn`. `plan.yml` selects from the map the same way.
`aws-role-session-name` is a name or a JSON object with `plan`, `apply` and
`drift` keys (`drift` falls back to `plan`; `plan.yml` uses `plan`),
sanitised to `[\w+=,.@-]` and 64 characters and passed as
`role-session-name`; empty keeps `GitHubActions`. The key forms and the session name ship in
`stackorder/actions` v1.0.0.

Job names carry the stack key last: `run.yml` names its jobs
`<mode> wave <n> <key>` and `plan.yml` its plan jobs `plan <key>`. The
server reads the key from these names, after the caller's `<job> / `
prefix, or from GitHub's default matrix form `<job> (<key>, …)`, to match
`workflow_job` events and to choose among dispatches that share a title.

`apply.max_parallel` in `stackorder.yaml` caps the number of stacks the
server puts in one dispatch; job level parallelism is the caller's
`max-parallel` input, because dispatch inputs are fixed by the wrapper file.

## Runner endpoints

Stack keys in paths are percent-encoded with `url.PathEscape` (slashes become
`%2F`); Go's ServeMux matches the escaped path and `r.PathValue` returns the
decoded key.

| Method and path | Body | Response |
| --- | --- | --- |
| `POST /v1/runs` | `v1.CreateRunRequest` | `v1.CreateRunResponse` |
| `POST /v1/runs/{id}/graph` | `v1.GraphUploadRequest` | `v1.ResolveResponse` (200 with `cycles` set when resolution fails on a cycle) |
| `POST /v1/runs/{id}/stacks/{key}/result` | `v1.StackResult` | `v1.RunStack` |
| `POST /v1/runs/{id}/stacks/{key}/checks/{name}` | `v1.CheckVerdict` | `v1.Check` |
| `GET /v1/runs/{id}` | | `v1.Run` |
| `POST /v1/unlock` | `v1.UnlockRequest` with `repo` and `stack_key` | `v1.UnlockResponse` |

A manual run (`POST /v1/runs` with an API key, `trigger: manual`, `mode:
apply` and `stacks`) makes the server take the locks before answering and
release them when the results arrive; this is how `stackorder apply --local`
works.

Authentication: `Authorization: Bearer <GitHub OIDC token>` for runners,
`Authorization: Bearer sk_<key>` for automation, cookie `stackorder_session`
for humans. Every runner token is accepted once (its `jti` is recorded), so
the CLI requests a fresh token for every call. Errors are `v1.Error` with
codes `unauthorized` (401), `forbidden` (403), `not_found` (404), `invalid`
(400), `conflict` (409), `refused` (409, apply gate; `details.failures`),
`locked` (423, `details.conflicts`), `superseded` (409),
`internal` (500), `unavailable` (503, JWKS or database unreachable). The CLI
exits 3 on `forbidden`, `conflict`, `refused`, `locked` and `superseded`,
and on its own fail-closed decisions (an unreachable server during apply). `invalid` carries `details.field` and `details.reason` when a
field is at fault. A body over its route's limit (8 MB for graph uploads,
1 MB for every other POST under `/v1` and `/auth`) is 413 with code
`invalid`; `POST /webhooks/github` has no API limit, `internal/webhook`
bounds it.

## Human and automation endpoints

Sessions or API keys. Lists take `?limit=&cursor=` and return `v1.Page[T]`
with `next_cursor`.

| Method and path | Response |
| --- | --- |
| `GET /v1/me` | `v1.Whoami`; for an API key `login` is `apikey:<name>` and `admin` is true |
| `GET /v1/overview` | `v1.Overview` |
| `GET /v1/repos` | `v1.Page[v1.RepoSummary]` |
| `GET /v1/repos/{owner}/{repo}/graph?ref=&run=` | `v1.GraphView`; `ref` is a full commit SHA, a unique prefix of at least 7 characters of a recorded graph's SHA (ambiguous: 400 `invalid`) or `default` (the default-branch graph), never a branch name; without `ref` the repo's default-branch graph when known, else the latest |
| `GET /v1/repos/{owner}/{repo}/runs?status=&pr=&mode=` | `v1.Page[v1.Run]`, newest first |
| `GET /v1/repos/{owner}/{repo}/stacks` | `v1.Page[v1.StackDetail]` |
| `GET /v1/stacks/{id}` | `v1.StackDetail` |
| `GET /v1/stacks/{id}/runs` | `v1.Page[v1.RunStackRef]`, newest first |
| `GET /v1/modules?q=` | `v1.Page[v1.ModuleDetail]` |
| `GET /v1/modules/{id}` | `v1.ModuleDetail` |
| `GET /v1/runs/{id}` | `v1.Run` |
| `GET /v1/runs/{id}/stacks/{key}/plan` | `text/plain`: the run stack's full plan text, read from the artifact bucket at `store.PlanTextArtifactKey` (`runs/<run id>/<slug>/plan.txt`); 404 `not_found` when the row has no `plan_url`, the server has no artifact bucket or the object is gone |
| `POST /v1/stacks/{id}/unlock` | `v1.UnlockResponse` |
| `POST /v1/runs/{id}/rerun` | `v1.CreateRunResponse` |
| `GET /v1/audit` | `v1.Page[v1.AuditEntry]` |
| `GET /auth/login?next=`, `GET /auth/callback` | redirects |
| `POST /auth/logout` | 204 |
| `GET /setup`, `GET /setup/callback`, `GET /setup/installed` | HTML |
| `POST /webhooks/github` | 202 |
| `GET /healthz`, `GET /readyz`, `GET /metrics` | see below |

A session user sees only repos whose installation account is one of the
user's organisations (or the user's own login); API keys see everything.
A session records the organisations at sign-in, so a membership change
takes effect at the next sign-in. For a person, `GET /v1/audit` keeps the
entries they made and those whose `details.repo`, or `target` (`owner/repo…`
or `kind:owner/repo…`), names a repository they see, so a page may hold
fewer than `limit` items and still carry `next_cursor`; `GET /v1/modules`
sets `total` only for API keys. State-changing human endpoints require the
session cookie plus a same-origin `Origin` or `Sec-Fetch-Site` check.
Unknown `/v1/*` and `/auth/*` paths return a JSON 404, not the UI.
Everything else under `/` serves the embedded UI with SPA fallback to
`index.html`. `GET /metrics` is open unless
`STACKORDER_METRICS_TOKEN` is set, in which case it requires
`Authorization: Bearer <token>`. `GET /readyz` returns 200 when the database
answers, in setup mode too.

## Run state machine

Run status: `pending -> planning -> planned -> applying -> applied`, with
`failed` reachable from `planning` and `applying`, `unconfirmed` reachable
from `pending` and `planning`, and `superseded` reachable from any non
terminal state when a new head SHA arrives for the same PR.

Stack status within a run: `pending -> planning -> planned -> applying -> applied`;
`failed` from `planning` and `applying`; `blocked` when a transitive
`depends_on` or `reads_state` predecessor failed (`v1.RunStack.BlockedBy`
names them); `noop` when a propagated stack's plan is empty at apply time;
`unconfirmed` when the CLI reported without a confirmed server round trip;
`unknown` when the job vanished; `skipped` when the requester named a subset
that excludes the stack.

Roll-up: a run is `planned` when every stack is `planned`, `noop` or
`skipped`; `applied` when every stack is `applied`, `noop` or `skipped`;
`failed` when any stack is `failed`, `blocked` or `unknown` and no stack is
still running.

When a PR merges into the default branch, its latest graph becomes the
repository's default-branch graph (`repos.default_graph_id`), which the
graph endpoint and module consumer queries prefer over the newest PR graph.
The default branch is the one the event's repository names, which also
updates `repos.default_branch`; a merge into any other branch counts as a
close without merge.

Per-stack policy (`allowed_teams`, `plan_output`) comes from the default
branch: the default-branch graph when known, else the `.stackorder.yaml`
files read through the Contents API at the default-branch head. A
default-branch `plan_output: summary` always wins over the PR's copy.
A pull request is resolved under the `stackorder.yaml` it uploads; when
that differs from `repos.config` after defaults, it is resolved under both
and the affected sets are united, stacks of the default-branch graph (else
the latest graph) that only `repos.config` discovers and finds affected
are added to the commit's graph, and the run warns with the keys that
differ. Such a stack whose directory the pull request deletes (a changed
path under it, and the directory gone at the head per the Contents API) is
left out of the graph and the run warns that it is recorded as removed. `workflow_job` events of the pull-request plan workflow, matched
through `runs.workflow_run_id`, move stacks to `planning`. Named check verdicts may
not use the reserved names `resolve`, `plan` or `apply`. A pull request has
at most one apply in flight. An apply of a PR that affects nothing is
answered with a comment, while a `before_merge` PR whose head affects
nothing still gets a successful `stackorder/apply` check so branch
protection can pass.

## Apply gate, in order

1. A requester is required (an empty requester fails; API keys are trusted)
   and may apply every affected stack: `allowed_teams` membership (`active`,
   nested teams count, cached 60 s) or push permission when the list is
   empty.
2. The PR is open and not merged, `mergeable` is not false and
   `mergeable_state` is not `dirty` (`blocked` is not a refusal, because
   `stackorder/apply` is itself a required check); approvals on the head SHA
   from users with push permission (a later `CHANGES_REQUESTED` or a
   dismissal cancels one) >= `require_approvals`; `four_eyes` and
   `require_codeowner_review` (owner reviews also only from users with push
   permission) when set.
3. Every affected stack has a `planned` row for the current head SHA, with
   a plan artifact when `apply.from_plan` is true.
4. Every named check on those stacks is `pass` (`warn` passes, `fail` refuses).
5. No affected stack is locked by another PR, and no other apply of this PR
   is in flight.

All failures are collected and reported together in one PR comment naming
the failing layer and the exact reason. Policy for the gate is read from the
default branch `stackorder.yaml` (`repos.config`), never from the PR's copy.
In `on_merge` repositories `stackorder apply` comments are refused; the
merge evaluates layers 1 (with the merger), 3, 4 and 5, applies the merge
commit with the head commit's plans, and releases the locks on completion.
Comment commands are accepted only from users with push permission, then
rate limited to 10 per PR per minute (counted from audit rows); a command
runs at most once, and one that fails part way is answered with a comment.

## Waves and dispatch

Waves are the longest-path layering of the affected subgraph over
`depends_on` and `reads_state` edges. The server dispatches
`stackorder-run.yml` once per (wave, environment) with inputs `run_id`,
`mode`, `wave`, `sha` and `stacks` (JSON array of `v1.MatrixEntry` carrying
`plan_run_id` and `artifact` for applies), at most `apply.max_parallel`
stacks per dispatch. Wave n+1 is dispatched when every stack of wave n is
terminal and none failed. Locks are taken on all affected stacks before wave
0 is dispatched and released on merge (`before_merge`) or run completion
(`on_merge` and manual runs). A `stackorder plan` comment dispatches
`mode: plan` for the named stacks under environment `default`; the
scheduler dispatches `mode: drift` per stack under `default` with `sha` set
to the default branch head.

Dispatches are unique per (run, wave, environment, mode, chunk), where
chunks split one (wave, environment) by `apply.max_parallel`; `sent_at`
marks a dispatch GitHub accepted, and Reconcile resends unsent ones after
60 s. An apply run (manual runs excepted) with no dispatch row two minutes
after it was created, still `pending` or `applying` with stacks of its
current wave `planned`, has that wave dispatched by Reconcile while it is
the pull request's newest apply, a comment apply's pull request is still
open at the run's commit and every planned stack keeps the head's plan;
otherwise it is failed, its locks are released (unless an earlier apply of
the pull request changed what is deployed) and the pull request gets a
comment. Each dispatch row binds at most one Actions `workflow_run_id`: a
`workflow_run` event's `display_title` binds only when it names a single
dispatch of that (run, wave, mode); otherwise the stacks named by the run's
jobs choose the dispatch, or the environment of a `deployment_protection_rule`
does, and Reconcile never binds by position. The first OIDC call of a job
also binds; a call that names no stack, where the environment has several
dispatches in the wave, binds by the stacks the workflow run's jobs name. A workflow run whose dispatch is already bound binds nothing and
its jobs are refused, which makes a duplicated dispatch (a retried POST
after a processed 5xx) harmless. Cross-repo plan runs use trigger `push` and
are deduplicated per upstream run and repository.

## OIDC binding

GitHub's `sha` claim is the commit that triggered the workflow: the merge
commit of `refs/pull/<n>/merge` for `pull_request` events and the default
branch head for `workflow_dispatch`. It is therefore never compared with a
run's SHA. The server verifies the runner token signature against the GitHub
JWKS (cached 1 h, refreshed on unknown `kid`), then checks: `aud` equals
`STACKORDER_OIDC_AUDIENCE`; `iss` is `https://token.actions.githubusercontent.com`
(or `GITHUB_OIDC_ISSUER`); `repository` and `repository_id` belong to a known
installation; `iat` is within 10 minutes; `jti` has not been seen;
`job_workflow_ref` matches `STACKORDER_REQUIRED_WORKFLOW_REF` when set. Then
per run kind:

- Plan runs: `event_name` is `pull_request`, `ref` is
  `refs/pull/<run.pr_number>/merge`, and the PR's current head SHA fetched
  from GitHub equals the run's SHA (the CLI registers plan runs with the
  head SHA from the event payload). `pull_request` tokens reach only plan
  runs registered by a pull-request resolve job; every server-dispatched
  run, plan runs included, needs the `workflow_dispatch` binding. Results
  for a terminal run are refused, and for a `pull_request` token a plan run
  that is `planned` is final too: its results, check verdicts and
  `GET /v1/runs/{id}` answer 409 `conflict` (`superseded` for a superseded
  run), except a repeat of the recorded result or verdict, which gets the
  stored row. Superseding compares against the PR head
  fetched from GitHub, never against an event's SHA, and default-branch
  pushes load `stackorder.yaml` at the branch head.
- Dispatched runs: `event_name` is `workflow_dispatch`, `ref` is
  `refs/heads/<default branch>`, `run_id` matches the dispatch bound to the
  run (binding it on first contact), `run_attempt` is recorded, and for
  `apply` the `environment` claim equals the stack's environment; for `plan`
  and `drift` dispatches it equals `default`.

`actor` is recorded as `requested_by`. `pull_request_target` plans are not
supported; fork pull requests get a neutral check from the server.

## Metrics

All names carry the `stackorder_` prefix: `runs_total{status,trigger,mode}`,
`stack_finished_total{mode,status}`, `stack_duration_seconds{mode}`,
`dispatches_total{mode,result}`, `drifted_stacks`, `locks_held`,
`commands_total{verb,accepted}`, `webhook_received_total{event}`,
`webhook_duplicates_total`, `webhook_lag_seconds` (received to a worker
starting on the event), `events_processed_total{kind,result}`,
`events_dead_total{kind}`, `jobs_processed_total{kind,result}`,
`queue_depth{queue}` (refreshed once a minute), `github_requests_total{route,status}`,
`github_request_duration_seconds{route}` (routes as `METHOD /template`),
`github_rate_limit_remaining`, `scheduler_leader`,
`http_requests_total{route,method,status}`, `http_request_duration_seconds`,
`build_info{version,commit}`. Label values outside a known set are counted
as `other`.

## Server configuration

| Variable | Purpose |
| --- | --- |
| `STACKORDER_BASE_URL` | Public URL, used for OAuth callbacks, OIDC audience default and links |
| `STACKORDER_LISTEN` | Listen address, default `:8080` |
| `DATABASE_URL` | Postgres DSN; `sslmode=require` is accepted |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET` | App identity; all three absent puts the server in setup mode |
| `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | Human sign-in |
| `GITHUB_API_URL`, `GITHUB_WEB_URL` | Default `https://api.github.com` and `https://github.com`; GHES sets its own |
| `GITHUB_OIDC_ISSUER`, `GITHUB_OIDC_JWKS_URL` | Overrides for GHES and tests |
| `STACKORDER_OIDC_AUDIENCE` | Default `STACKORDER_BASE_URL` |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | Optional glob pinning `job_workflow_ref`, validated at start-up |
| `STACKORDER_ARTIFACT_BUCKET`, `STACKORDER_ARTIFACT_PREFIX` | Optional S3 bucket (and key prefix) for full plan text |
| `STACKORDER_SESSION_KEY` | 32 byte hex key for cookie signing; generated and logged as a warning when absent |
| `STACKORDER_METRICS_TOKEN` | When set, `/metrics` requires this bearer token |
| `STACKORDER_ALLOW_RESETUP` | `true` lets a server with App credentials create another App through `/setup?force=1`; default `false`, and then `/setup?force=1` and `/setup/callback` answer 404 outside setup mode |
| `STACKORDER_PLAN_TEXT_RETENTION`, `STACKORDER_EVENT_RETENTION`, `STACKORDER_DRIFT_RETENTION` | Durations, defaults `720h`, `168h`, `2160h` |
| `STACKORDER_WORKERS` | Worker goroutines, default 4; the pgx pool gets this many connections plus 8 unless `DATABASE_URL` sets `pool_max_conns` |
| `STACKORDER_LOG_LEVEL`, `STACKORDER_LOG_FORMAT` | `info` / `json` by default |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Enables tracing; spans carry `stackorder.run_id` |

The server refuses to start without `DATABASE_URL` and `STACKORDER_BASE_URL`;
retentions must be positive durations; `STACKORDER_ARTIFACT_PREFIX` without a
bucket is an error; a partial set of the three `GITHUB_APP_*` variables is an
error. Without all three it starts in setup mode, runs migrations, and serves
only `/setup*`, `/healthz` and `/readyz`. The default OIDC audience is the
base URL with trailing slashes removed, and the `/setup` page tells operators
to use that exact value as the Actions `server-url`. A generated session key
is warned about, never logged. With a bucket, `AWS_ENDPOINT_URL_S3` switches
the artifact store to path-style addressing (LocalStack); artifact URLs are
`s3://bucket/key`. `stackorder-server healthcheck` GETs `/healthz` on the
listen port (an unspecified host maps to `127.0.0.1`) and exits 0 on 200,
for container health checks in images without a shell; the Dockerfile
declares it as `HEALTHCHECK`. At start-up, and daily at 04:00 UTC, the server
syncs installations and their repositories from the App API (adding and
refreshing, reading `stackorder.yaml` only for repositories not seen before)
so installations made in setup mode or lost webhooks are learned; when the
installation listing and the repository listing of every unsuspended
installation succeed, it also forgets stored installations the App no
longer lists and stored repositories of an unsuspended installation that
no listing names, and when any listing fails it forgets nothing. Traces
carry `stackorder.run_id`, `stackorder.event`, `stackorder.delivery`,
`stackorder.job` and `stackorder.job_id`; `/healthz`, `/readyz` and
`/metrics` are not traced. The pgx pool has `STACKORDER_WORKERS` + 8
connections (`server.PoolConnsBeyondWorkers`) unless the DSN's
`pool_max_conns` sizes it; the scheduler's leader lock holds one connection.

## Store notes

`/migrations` is a Go package exposing `migrations.FS`. `store.Migrate` takes
`store.MigrationLockKey`; the scheduler takes `store.SchedulerLockKey`
through `store.TryAdvisoryLock`, which returns `(*AdvisoryLock, acquired,
err)` with `Held(ctx)` and `Release()`. Dispatches are unique per (run, wave,
environment, mode) and a workflow run id belongs to at most one dispatch.
Sessions store `sha256(token)`; API keys are `sk_` plus 43 base64url
characters with the sha256 stored and a 10 character display prefix. Edges
store `from_key` and `to_key` text so they can point at external stacks;
backends are a `backend` jsonb column. Job `dedupe_key` stays taken until the
job is pruned, so recurring jobs put a time slot in the key.
`store.ReleaseLockOfPR` checks the holder and deletes in one statement.
Queue: worker ids are `host:pid:n`; a failed row retries after 1 m, 5 m,
30 m and 2 h and becomes a dead letter after 5 attempts; shutdown hands
unstarted claims back through `store.ReleaseClaims` and cancels handlers
after a 30 s drain; the stale claim age (10 m) exceeds the handler timeout
(5 m); `store.QueueDepth` feeds the gauges. Scheduler: cron runs in UTC
unless the expression carries a `CRON_TZ=` prefix; a new leader catches up
at most one hour, keeping the latest fire per window; dedupe keys are
`reconcile:<minute>`, `prune:<hour>`, `stale_locks:<day>`,
`schedule_drift:<repo>:<fire unix>`, `drift:<stack>:<hour>`,
`dispatch_wave:<run>:<wave>`, `sync_installations:<fire unix>` and
`sync_installations:start:<minute unix>`. `store.UpsertRepo` takes a
transaction-level advisory lock on the lowercased full name so two workers
recording the same new repository do not race the unique index.
`store.SetDefaultGraph` records `repos.default_graph_id` (the runs service
calls it when a PR merges) and `store.GetDefaultGraph` reads it back;
`ModuleConsumers` and `StackModules` read each repository's default-branch
graph, falling back to its latest graph.

## CLI environment

| Variable | Purpose |
| --- | --- |
| `STACKORDER_SERVER_URL` | Server base URL; unset means local mode |
| `STACKORDER_API_KEY` | Automation key for local `apply`, `unlock` and result posting outside Actions |
| `STACKORDER_OIDC_AUDIENCE` | Audience requested for the runner token; defaults to the server URL exactly as given |
| `STACKORDER_RUN_ID` | Run id from the resolve step or the dispatch input |
| `STACKORDER_TOOL`, `STACKORDER_TOOL_VERSION` | Override the configured tool |
| `STACKORDER_TERRAFORM_BIN`, `STACKORDER_TOFU_BIN` | Binary name on `PATH` or path, overriding detection |
| `STACKORDER_BACKEND_CONFIG` | Extra `-backend-config` values, comma separated, for `init`, after the stack's `backend_config` |
| `STACKORDER_PLAN_DIR` | Where plan files are written, default `.stackorder/plans` |
| `STACKORDER_LOG_FORMAT` | `json` switches the CLI's text logs to JSON |
| `GITHUB_*`, `ACTIONS_ID_TOKEN_REQUEST_URL`, `ACTIONS_ID_TOKEN_REQUEST_TOKEN`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY`, `GITHUB_TOKEN` | Provided by the runner; `GITHUB_TOKEN` is only used for neutral fallback checks |

Exit codes: 0 success, 1 error, 2 plan has changes (`affected`, `drift`),
3 refused by the server (gate, lock, unconfirmed, fail closed).

## CLI commands

Global flags: `--server` (`STACKORDER_SERVER_URL`), `--repo-root` (default
`GITHUB_WORKSPACE` or the git top level), `--verbose`, `--format`
(`text`, `json`, `dot` where it applies). In Actions the CLI reads the
repository, SHA, event, PR number, run id and attempt from the `GITHUB_*`
variables and the event payload; for `pull_request` events the run SHA is
the PR head SHA from the payload, not `GITHUB_SHA`. Outputs go to
`GITHUB_OUTPUT` and a Markdown summary to `GITHUB_STEP_SUMMARY`.

| Command | Flags | Outputs |
| --- | --- | --- |
| `resolve` | `--base <ref>` (default: PR base SHA, then the push's `before`, then the merge base with `origin/<default branch>`), `--stacks a,b` | `run-id`, `matrix`, `waves`, `affected` (JSON array of keys), `count`, `unconfirmed` |
| `plan --stack <key>` | `--run-id`, `--out <file>` | `has-changes`, `plan-file` (absolute), `artifact`, `summary`, `unconfirmed` |
| `apply --stack <key> --run-id <id>` | `--plan-file`, `--local` | `summary` |
| `drift --stack <key>` | `--run-id` | `drifted`, `summary` |
| `check --stack <key> --run-id <id> --name <n> --status pass\|fail\|warn` | `--summary`, `--details-url`, `--details-file` | |
| `graph` | `--format` | |
| `affected --base <ref>` | `--format` | |
| `unlock <key>…` | `--reason`, `--force-state` | |
| `version` | | |

`apply` compares the run's SHA with the dispatch `sha` input (falling back
to `GITHUB_SHA`) and with the checkout's HEAD, never with `GITHUB_SHA` alone,
and refuses unless the stack row's `lock` (`store.RunDetail` fills it with
the stack's current lock) names the run.
With `--plan-file` missing (an expired artifact) it plans again and applies
only when the new plan's resource address set matches the recorded summary;
`apply.from_plan: false` always re-plans and compares. Outside Actions,
`plan` and `drift` post results only when a server, a run id and an API key
are all set.

## Conventions

- Errors wrap with `%w` and a short context; sentinel errors are exported
  variables named `ErrX`, except `oidc.ErrBinding`, a struct type matched
  with `errors.As`. HTTP handlers map errors to `v1.Error` codes in one
  place.
- Every exported type and function has a doc comment. Code comments are
  otherwise avoided; rationale goes in commit messages.
- All times are UTC `timestamptz`. JSON times are RFC 3339.
- Handlers and workers take `context.Context` first and are idempotent.
- Tests are table driven where there is a table. Integration tests carry the
  `integration` build tag; end-to-end tests carry `e2e`. `go test ./...`
  must pass with no Docker, no network and no Postgres.
- Commits follow Conventional Commits with a scope: `feat(graph): …`,
  `fix(cli): …`, `test(store): …`, `docs(site): …`, `chore(deps): …`,
  `ci: …`, `build(docker): …`.
