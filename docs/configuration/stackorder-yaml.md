# `stackorder.yaml`

The root `stackorder.yaml` sets the repository's discovery rules, tool, environment mapping, apply policy, propagation and drift schedule. Every key has a default, so the smallest valid file is one line:

```yaml
version: 1
```

The apply gate reads its policy from the copy of this file on the **default branch**, never from the copy in a pull request. A PR cannot loosen its own gate.

The server reads the file through the GitHub Contents API when it first sees a repository and on every push to the default branch. A missing file means the defaults. An invalid file on the default branch is ignored: the server keeps the previous configuration, logs a warning and records a `config_invalid` entry in the [audit log](/reference/api#audit). In a pull request, the CLI parses the PR's own copy and fails the resolve job when it is invalid.

| Setting | Read from |
| --- | --- |
| `stacks`, `modules`, `tool`, `tool_version`, `propagate.dependents` | The pull request's copy, for that pull request's plans |
| `apply`, `environments` for applies, `plan_output: summary`, `propagate.cross_repo`, `drift` | The default branch |

## Full example

```yaml
version: 1

stacks:
  discover: ["stacks/**"]        # dirs with a terraform { backend "s3" {} } block
  ignore: ["**/*.md", "**/README*"]

modules:
  paths: ["modules/**"]           # local modules whose changes propagate

tool: tofu                        # or terraform; per-stack override allowed
tool_version: "1.9.0"

environments:                     # stack path prefix -> GitHub environment
  "stacks/prod/": production
  "stacks/staging/": staging

apply:
  mode: before_merge              # or on_merge
  require_approvals: 1
  require_codeowner_review: true  # an APPROVED review from the owning team, on head SHA
  allowed_teams: [platform-eng]   # who may comment `stackorder apply`; default is anyone with write
  four_eyes: true                 # applier must not be the PR author
  from_plan: true
  max_parallel: 6

propagate:
  dependents: true
  cross_repo: plan                # off | plan

drift:
  schedule: "0 6 * * 1-5"
  open_issue: true

plan_output: full                 # or summary
```

## Keys

### Top level

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `version` | integer | `1` | Schema version. Only `1` is supported. |
| `tool` | `terraform` or `tofu` | `terraform` | The binary stacks run with. The CLI expects it on `PATH`. Overridable per stack. |
| `tool_version` | string | empty | The tool version for this repository's stacks. It travels in each stack's matrix entry, where the reusable workflows install it, and the CLI warns when the binary it runs reports another version. Overridable per stack. |
| `environments` | map of path prefix to environment name | `{}` | The GitHub environment each stack's applies run under. See [Environment mapping](#environments). |
| `plan_output` | `full` or `summary` | `full` | How much of a plan reaches the server and the PR comment. `summary` sends only resource counts and addresses. Overridable per stack. |

### `stacks`

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `stacks.discover` | list of directory globs | `["stacks/**"]` | A matching directory is a stack when it contains a `terraform` block with a `backend "s3"`. An empty list falls back to the default. |
| `stacks.include` | list of paths | `[]` | Directories that are stacks regardless of discovery. Paths are repository relative. |
| `stacks.ignore` | list of file globs | `["**/*.md", "**/README*"]` | Changed files matching these never affect a stack. Set `[]` to ignore nothing. |
| `stacks.ignore_lockfile` | boolean | `false` | Adds `**/.terraform.lock.hcl` to `stacks.ignore`, so provider lock file updates alone do not trigger plans. |

### `modules`

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `modules.paths` | list of directory globs | `["modules/**"]` | Local module directories whose changes propagate to the stacks that use them. An empty list falls back to the default. |

### `apply`

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `apply.mode` | `before_merge` or `on_merge` | `before_merge` | When applies happen. `before_merge` applies on the PR after a `stackorder apply` comment and keeps the default branch deployable. `on_merge` applies after the merge. |
| `apply.require_approvals` | integer, 0 or more | `0` | Approving reviews on the head commit the PR needs before an apply. Only reviews from users with push permission count, never the author's own, and a later `CHANGES_REQUESTED` review or a dismissal on the same commit cancels an approval. |
| `apply.require_codeowner_review` | boolean | `false` | Each affected stack needs an `APPROVED` review on the current head SHA from one of its owners in the default branch's `CODEOWNERS` (a listed user, or a member of a listed team) who also has push permission. A stack no rule owns passes, and a missing `CODEOWNERS` file is a refusal. |
| `apply.allowed_teams` | list of teams | `[]` | Who may request an apply: an active member of any listed team, nested teams included. A team is `slug` (in the repository owner's organisation) or `org/slug`, with or without a leading `@`. Empty means anyone with push permission on the repository. Needs the App's `Members: Read` permission. |
| `apply.four_eyes` | boolean | `false` | Refuse an apply requested by the PR author. |
| `apply.from_plan` | boolean | `true` | Apply the saved plan file, and require every affected stack to have a plan artifact before an apply starts. If the artifact has expired, the CLI re-plans and refuses unless the resource-address set matches the recorded plan. `false` always re-plans at apply time and compares the same way. |
| `apply.max_parallel` | integer, 1 or more | `6` | The most stacks the server puts in one dispatch of `stackorder-run.yml`. A wave with more stacks for one environment is split into several dispatches. How many jobs of one dispatch run at once is the calling workflow's `max-parallel` input. |

### `propagate`

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `propagate.dependents` | boolean | `true` | Add the transitive dependents of changed stacks, over `depends_on` and `reads_state`, to the affected set. They are planned so reviewers see the downstream effect, and skipped as `noop` at apply time if their plan is empty. |
| `propagate.cross_repo` | `off` or `plan` | `off` | After an upstream stack applies, `plan` dispatches a plan-only run on each dependent stack in other repositories. See [Cross-repo dependencies](./cross-repo). |

### `drift`

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `drift.schedule` | five-field cron expression | empty | When the server dispatches drift checks: minute, hour, day of month, month, day of week, in UTC unless the expression starts with `CRON_TZ=<zone> `. Descriptors such as `@daily` are not accepted. Empty disables drift runs. See [Drift detection](./drift). |
| `drift.open_issue` | boolean | `false` | Open or update one GitHub issue per drifted stack. Needs the App's optional `Issues: Write` permission. |

## Environment mapping {#environments}

`environments` maps a stack path prefix to the GitHub environment its applies run under.

- Prefixes match whole path segments. `stacks/prod/` matches `stacks/prod/vpc` but not `stacks/production/vpc`. The trailing slash is optional.
- The longest matching prefix wins.
- A stack's own `environment` in `.stackorder.yaml` overrides the map.
- A stack that matches nothing runs under the environment `default`, which GitHub creates on first use with no protection rules.

The environment is what lets GitHub environment protection rules and the AWS trust policy gate applies per stack. See [Environments and authorization](./environments-and-authorization).

## Globs

Globs use `**` for any number of directories and `*` within one path segment. Stack paths are repository relative, slash separated, with no leading `./` and no trailing `/`.

## Validation

The file is parsed strictly. The CLI and the server reject:

- an empty file (the minimum is `version: 1`);
- unknown keys;
- a `version` other than `1`;
- a `tool`, `apply.mode`, `propagate.cross_repo` or `plan_output` outside its allowed values;
- a negative `apply.require_approvals`, or an `apply.max_parallel` below 1;
- a `drift.schedule` that is not a valid five-field cron expression;
- an empty environment prefix or environment name;
- an empty `stacks.discover` glob or `apply.allowed_teams` entry;
- a `stacks.include` path that is absolute or starts with `..`.

All problems are reported together, each with the key that caused it.

## Per-stack overrides

A `.stackorder.yaml` in a stack directory declares dependencies and overrides `tool`, `tool_version`, `environment`, `plan_output` and `apply.allowed_teams` for that stack. See [`.stackorder.yaml`](./stack-yaml).
