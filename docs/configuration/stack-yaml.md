# `.stackorder.yaml`

A `.stackorder.yaml` in a stack directory declares the stack's dependencies and instances, and overrides root settings for that stack. It is optional; add one only to stacks that have dependencies, instances or overrides.

## Example

```yaml
depends_on:
  - stacks/prod/vpc                       # same repo
  - acme/network-infra//stacks/prod/tgw   # another repo, same installation
tool: terraform                           # override
environment: production                   # override the prefix mapping
apply:
  allowed_teams: [platform-prod]          # replaces the root list for this stack
plan_output: summary                      # this stack's plans hold secrets
ignore_inferred: [stacks/legacy/dns]      # suppress a remote_state edge
```

A stack with instances:

```yaml
backend_config:
  - 'key=kyc/{{ .Instance }}.tfstate'
var_files:
  - "regions/{{ .Instance }}.tfvars"
env:
  TF_VAR_environment: "{{ .Instance }}"
instances:
  staging: {}
  production:
    apply:
      allowed_teams: [platform-prod]
```

## Keys

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `depends_on` | list of stack keys | `[]` | Stacks this stack depends on. Each entry creates a `depends_on` edge, which orders applies and propagates change. Entries may be templates. |
| `instances` | list of names, or map of name to overrides | none | The instances of this directory. It replaces the instances the root `from_var_files` would derive. See [Stack instances](./instances#declaring). |
| `workspace` | string | none | The Terraform workspace the CLI selects after `init`. On a stack with instances it is a template, such as `"{{ .Instance }}"`. On a stack without instances, a workspace other than `default` makes the stack one instance of that name; `default`, or a value that renders empty, is the same as none. |
| `backend_config` | list of strings | `[]` | `-backend-config` values for `init`, after the root's: `name=value`, or a file relative to the repository root. See [`backend_config`](./instances#backend-config). |
| `var_files` | list of paths | `[]` | `-var-file` values for plans, after the root's, relative to the stack directory. See [`var_files`](./instances#var-files). |
| `env` | map of name to string or `{plan, apply, drift}` | `{}` | Environment variables for the tool and the hooks, over the root's. See [`env`](./instances#env). |
| `tool` | `terraform` or `tofu` | root `tool` | The binary this stack runs with. |
| `tool_version` | string | root `tool_version` | The tool version for this stack. |
| `environment` | string | from the root `environments` map | The GitHub environment this stack's applies run under. It may be a template. |
| `apply.allowed_teams` | list of teams | root `apply.allowed_teams` | Who may request an apply that touches this stack, in the same form as the root key. A non-empty list replaces the root list. The server reads it from the default branch. |
| `plan_output` | `full` or `summary` | root `plan_output` | How much of this stack's plan reaches the server and the PR comment. A `summary` on the default branch wins over a pull request's `full`. |
| `ignore_inferred` | list of stack keys | `[]` | Inferred `reads_state` edges to suppress. Each suppressed edge is reported as a warning by the scan. Entries may be templates. |

An entry of the `instances` map may set `environment`, `workspace`, `backend_config`, `var_files`, `env`, `plan_output`, `apply.allowed_teams`, `depends_on` and `ignore_inferred` for that instance.

## `depends_on` entries {#depends-on}

An entry is a stack key, optionally qualified with a repository:

| Entry | Points at |
| --- | --- |
| `stacks/prod/vpc` | A stack in the same repository; for a directory with instances, see below |
| `infra/network:production` | One instance of a directory |
| `acme/network-infra//stacks/prod/tgw` | A stack in another repository covered by the same App installation |
| `acme/network-infra//infra/tgw:production` | An instance in another repository |

Paths are repository relative. Leading `./` and trailing `/` are removed. An entry that is absolute, starts with `..`, or names a repository that is not `owner/repo` is rejected.

An entry without a suffix that names a directory with instances resolves to the instance of the same name as the depending instance when the target has one, else to the target's only stack when it has exactly one. Otherwise it is kept as written, with a warning, like any unknown target. `infra/kyc:production` listing `infra/network` depends on `infra/network:production`. See [Dependencies between instances](./instances#depends-on).

A cross-repo entry is stored in the graph but cannot order a single-repository run. See [Cross-repo dependencies](./cross-repo).

## How settings merge {#merge}

For each stack, and for each instance of a stack with instances, the effective settings are:

| Setting | Effective value |
| --- | --- |
| `tool`, `tool_version` | The stack's value if set, otherwise the root value. |
| `plan_output` | The instance's value, else the stack's, else the root value. |
| `workspace` | The instance's value, else the stack's; none when neither is set. |
| `environment` | The instance's `environment`, else the stack's, else the most specific match in the root `environments` map, else the instance name, else `default`. |
| `apply.allowed_teams` | The most specific non-empty list: the instance's, else the stack's, else the root list. |
| `backend_config` | The root list, then the stack's, then the instance's. |
| `var_files` | The root list, the stack's, the instance's `from_var_files` file, then the instance's list. |
| `env` | Per variable name, the instance's value, else the stack's, else the root's. |
| `depends_on`, `ignore_inferred` | The stack's list followed by the instance's; the root file has no equivalent. |

A run that touches several stacks requires the commenter to satisfy **every** affected stack's `allowed_teams`. To apply a subset, name it: `stackorder apply stacks/staging/vpc`.

## Inferred edges {#inferred}

When a stack reads another stack's state through `terraform_remote_state` with a matching S3 bucket and key, Stackorder infers a `reads_state` edge. The backend compared is the stack's effective backend, after its `backend_config`, so instances with a partial `backend "s3" {}` block take part. You can:

- **keep it**, and let it order applies softly and propagate change;
- **promote it**, by listing the other stack in `depends_on`;
- **suppress it**, by listing the other stack in `ignore_inferred`.

## Validation

The file is parsed strictly. It is rejected when it has:

- unknown keys, or a `tool` or `plan_output` outside its allowed values;
- an invalid `depends_on` entry, or an empty `ignore_inferred` entry;
- a `workspace` containing `/`, `:`, a space or a tab, or, on a stack without instances, one that renders to an invalid instance name;
- an instance name that is not a letter or digit followed by letters, digits, `.`, `_` and `-`, is longer than 64 characters, or is `default` in any letter case, or a null item in the `instances` list;
- an `env` name that is not a valid variable name or is reserved in any letter case, a null `env` value, or an `env` object with a key other than `plan`, `apply` and `drift`;
- a template that does not parse, uses an action or builtin outside the [allowed set](./instances#templates), fails to render, or renders more than 4096 bytes.

An empty file is valid and changes nothing.

A CLI or server older than the instance keys rejects a file that uses them. Upgrade the server first.
