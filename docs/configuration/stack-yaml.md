# `.stackorder.yaml`

A `.stackorder.yaml` in a stack directory declares the stack's dependencies and overrides root settings for that stack. It is optional; add one only to stacks that have dependencies or overrides.

## Example

```yaml
depends_on:
  - stacks/prod/vpc                       # same repo
  - acme/network-infra//stacks/prod/tgw   # another repo, same installation
workspace: default
tool: terraform                           # override
environment: production                   # override the prefix mapping
apply:
  allowed_teams: [platform-prod]          # replaces the root list for this stack
plan_output: summary                      # this stack's plans hold secrets
ignore_inferred: [stacks/legacy/dns]      # suppress a remote_state edge
```

## Keys

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `depends_on` | list of stack keys | `[]` | Stacks this stack depends on. Each entry creates a `depends_on` edge, which orders applies and propagates change. |
| `workspace` | string | `default` | The Terraform workspace the CLI selects after `init`. A workspace other than `default` becomes part of the stack key, as `path:workspace`. A stack key passed to the CLI with `:workspace` overrides it. |
| `tool` | `terraform` or `tofu` | root `tool` | The binary this stack runs with. |
| `tool_version` | string | root `tool_version` | The tool version for this stack. |
| `environment` | string | from the root `environments` map | The GitHub environment this stack's applies run under. |
| `apply.allowed_teams` | list of teams | root `apply.allowed_teams` | Who may request an apply that touches this stack, in the same form as the root key. A non-empty list replaces the root list. The server reads it from the default branch. |
| `plan_output` | `full` or `summary` | root `plan_output` | How much of this stack's plan reaches the server and the PR comment. A `summary` on the default branch wins over a pull request's `full`. |
| `ignore_inferred` | list of stack keys | `[]` | Inferred `reads_state` edges to suppress. Each suppressed edge is reported as a warning by the scan. |

## `depends_on` entries {#depends-on}

An entry is a stack key, optionally qualified with a repository:

| Entry | Points at |
| --- | --- |
| `stacks/prod/vpc` | A stack in the same repository |
| `stacks/prod/vpc:blue` | The same directory, workspace `blue` |
| `acme/network-infra//stacks/prod/tgw` | A stack in another repository covered by the same App installation |

Paths are repository relative. Leading `./` and trailing `/` are removed. An entry that is absolute, starts with `..`, or names a repository that is not `owner/repo` is rejected.

A cross-repo entry is stored in the graph but cannot order a single-repository run. See [Cross-repo dependencies](./cross-repo).

## How settings merge {#merge}

For each stack, the effective settings are:

| Setting | Effective value |
| --- | --- |
| `tool`, `tool_version`, `plan_output` | The stack's value if set, otherwise the root value. |
| `environment` | The stack's `environment` if set; otherwise the longest matching prefix in the root `environments` map; otherwise `default`. |
| `apply.allowed_teams` | The stack's list if it is non-empty, replacing the root list; otherwise the root list. |
| `depends_on`, `ignore_inferred` | The stack's lists; the root file has no equivalent. |

A run that touches several stacks requires the commenter to satisfy **every** affected stack's `allowed_teams`. To apply a subset, name it: `stackorder apply stacks/staging/vpc`.

## Inferred edges {#inferred}

When a stack reads another stack's state through `terraform_remote_state` with a matching S3 bucket and key, Stackorder infers a `reads_state` edge. You can:

- **keep it**, and let it order applies softly and propagate change;
- **promote it**, by listing the other stack in `depends_on`;
- **suppress it**, by listing the other stack in `ignore_inferred`.

## Validation

The file is parsed strictly. It is rejected when it has unknown keys, a `tool` or `plan_output` outside its allowed values, an invalid `depends_on` entry, an empty `ignore_inferred` entry, or a `workspace` containing `/`, `:`, a space or a tab. An empty file is valid and changes nothing.
