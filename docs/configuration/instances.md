# Stack instances

A stack directory can be deployed several times, once per **instance**. Each instance runs the same code with its own state object, var files, environment variables and GitHub environment. A `network` component deployed to `staging` and `production` is one directory and two instances.

Every instance is a stack of its own wherever a stack appears: the graph, the affected set, waves, locks, runs, check runs, plan artifacts, drift and the UI. The CLI renders every setting on this page from the checkout it runs in. The server re-resolves the policy of an apply, its environment, `allowed_teams` and `plan_output`, from the default branch with the same rules.

## Keys and names {#keys}

An instance is keyed `path:instance`. The suffix names an instance of the directory; it is never, by itself, a Terraform workspace.

```text
infra/network                 a directory with no instances
infra/network:production      the production instance of infra/network
infra/network:staging         the staging instance of the same directory
acme/infra//infra/dns:shared  an instance in another repository
```

| Rule | Value |
| --- | --- |
| Characters | A letter or digit, then letters, digits, `.`, `_` and `-` |
| Length | At most 64 characters |
| Reserved | `default` is never an instance name |

The same name is safe in check run names, comment commands, plan artifact names and GitHub environment names.

An instance selects a Terraform workspace only when its `workspace` says so. Most instances need none: they differ by their backend key. See [Workspaces](#workspace).

## Declaring instances {#declaring}

There are four ways to declare the instances of a directory. The first that applies wins:

1. The names in the stack's `instances` key.
2. One instance per file that the root `stacks.instances.from_var_files` glob matches in the stack directory.
3. The stack's `workspace`, as one instance of that name: the legacy form. A `workspace` of `default`, or one that renders empty, is no workspace.
4. Otherwise one instance with an empty name, keyed by the bare path.

### From var files {#from-var-files}

A monorepo that keeps one var file per environment in each component can derive the instances from those files:

```yaml
# stackorder.yaml
stacks:
  instances:
    from_var_files: "workspaces/*.tfvars.json"
```

```text
infra/network/workspaces/production.tfvars.json   ->  infra/network:production
infra/network/workspaces/staging.tfvars.json      ->  infra/network:staging
```

- The glob is relative to each stack directory.
- An instance is named by the file's base name up to its first `.`, so `production.tfvars.json` gives `production`. Instances are sorted by name.
- The matched file is one of the instance's [var files](#var-files), after the root and stack `var_files`.
- A stack directory with no matching file falls through to the next rule.
- A derived name that is not a valid instance name, such as `default` from `default.tfvars.json`, is an error that names the file.

Adding a file adds an instance, which the pull request plans. Deleting one removes the instance from the graph; nothing is destroyed.

### An explicit list {#list}

A stack's `.stackorder.yaml` can name its instances:

```yaml
# infra/dns/.stackorder.yaml
instances: [shared]
```

The list replaces anything `from_var_files` would derive for the directory. A matched var file whose name equals a listed instance is still that instance's var file. A matched file whose name is not listed is not used, and the scan warns about it.

### A map with overrides {#overrides}

The map form names the instances and overrides settings for some of them:

```yaml
# infra/kyc/.stackorder.yaml
instances:
  staging: {}
  production:
    environment: infra-production
    apply:
      allowed_teams: [platform-prod]
    env:
      TF_VAR_replicas: "3"
```

An override may set `environment`, `workspace`, `backend_config`, `var_files`, `env`, `plan_output`, `apply.allowed_teams`, `depends_on` and `ignore_inferred`. The map is the instance set, like the list, so name every instance, even those with nothing to override. A map that lists only `production` drops `staging` from the directory.

In JSON, such as a stack's `config` in the API, `instances` is always a map.

### The legacy `workspace` stack {#legacy-workspace}

A stack that sets `workspace: blue` and declares no instances is one instance named `blue` whose Terraform workspace is `blue`. Its key, `path:blue`, and its state object are what they were before instances existed.

Its default GitHub environment is not. An instance runs its applies under the environment of its own name unless something maps it elsewhere, so this stack's applies move from `default` to `blue`. Set `environment: default` in its `.stackorder.yaml` to keep the old environment. See [GitHub environments](#environments).

## Workspaces {#workspace}

An instance selects no Terraform workspace unless its `workspace` is set. Instances then differ by their state key, which [`backend_config`](#backend-config) renders per instance.

To give each instance a workspace of its own name instead, set a template in the stack's `.stackorder.yaml`, or in an instance override:

```yaml
workspace: "{{ .Instance }}"
```

The CLI then runs `workspace select -or-create` after `init`, and the state object is `<workspace_key_prefix>/<workspace>/<key>`, `env:/production/network.tfstate` for the key `network.tfstate` and the default prefix. Two instances with neither a workspace nor a key of their own share one state object, and the scan warns about it.

## How settings merge {#merge}

The settings of an instance come from three levels: the root `stackorder.yaml`, the stack's `.stackorder.yaml`, and the instance's override in `instances`.

| Setting | Effective value |
| --- | --- |
| `backend_config` | Root entries, then stack entries, then instance entries, in that order |
| `var_files` | Root entries, then stack entries, then the instance's `from_var_files` file if any, then instance entries, in that order |
| `env` | Per variable name: the instance's value, else the stack's, else the root's |
| `depends_on`, `ignore_inferred` | The stack's list followed by the instance's |
| `environment` | The instance's `environment`, else the stack's, else the best match in the root `environments` map, else the instance name, else `default` |
| `workspace` | The instance's, else the stack's; none when neither is set |
| `plan_output` | The instance's, else the stack's, else the root's |
| `tool`, `tool_version` | The stack's, else the root's |
| `apply.allowed_teams` | The most specific non-empty list: the instance's, else the stack's, else the root's |

An `env` value is replaced as a whole. An instance that sets `TF_VAR_role: admin` replaces a stack's `{ plan: reader, apply: deployer }`, for every mode.

The server reads `environment`, `plan_output` and `apply.allowed_teams` of an instance from the default branch when an apply starts, as it does for any stack. The CLI reads everything else from the checkout it runs in.

## Templates {#templates}

Values can refer to the stack and the instance they are rendered for:

```yaml
backend_config:
  - 'key={{ trimPrefix "infra/" .Path }}/{{ .Instance }}.tfstate'
env:
  TF_VAR_environment: "{{ .Instance }}"
```

Templates are Go [`text/template`](https://pkg.go.dev/text/template). They render once per instance.

| Data | Value for `infra/network:production` |
| --- | --- |
| `.Path` | `infra/network` |
| `.Name` | `network`, the last path segment |
| `.Instance` | `production`; empty for a stack with no instances |
| `.Key` | `infra/network:production` |

| Function | Example | Result |
| --- | --- | --- |
| `trimPrefix prefix s` | `trimPrefix "infra/" .Path` | `network` |
| `trimSuffix suffix s` | `trimSuffix "/network" .Path` | `infra` |
| `base s` | `base .Path` | `network` |
| `dir s` | `dir .Path` | `infra` |
| `replace old new s` | `replace "/" "-" .Path` | `infra-network` |
| `lower s`, `upper s` | `upper .Instance` | `PRODUCTION` |

Templates apply to `environments` values, `environment`, `workspace`, `backend_config`, `var_files`, `env` values, `depends_on` and `ignore_inferred`. A string without `{{` is used as it is.

- **Quoting.** A YAML value that starts with `{` is read as a map, so quote every template. Use single quotes around a template that contains double quotes: `'key={{ trimPrefix "infra/" .Path }}.tfstate'`.
- **Errors.** A name that does not exist, such as `.Instanse`, is an error, not an empty string. A template that fails to parse or render is a validation error of the file that holds it.
- **Empty instance.** `.Instance` is empty for a stack with no instances, so `{{ .Instance }}.tfstate` renders as `.tfstate` there. Give such stacks an instance, or keep instance templates out of the settings they read.

## `backend_config` {#backend-config}

`backend_config` is a list of `-backend-config` values for `init`. It exists at the root, in a stack's `.stackorder.yaml` and in an instance override, and the lists concatenate in that order.

```yaml
backend_config:
  - infra/state.s3.tfbackend                                     # a file
  - 'key={{ trimPrefix "infra/" .Path }}/{{ .Instance }}.tfstate' # one attribute
```

- A value that contains `=` is `name=value`. Anything else is a file, relative to the **repository root**.
- The CLI passes files as absolute paths, then the values of `STACKORDER_BACKEND_CONFIG`. Terraform takes the last value of an attribute, so `STACKORDER_BACKEND_CONFIG` can override the configuration.
- When `backend_config` is not empty, `init` also gets `-reconfigure`, so two instances of one directory can run one after the other in the same checkout.
- A root `backend_config` applies to every stack. See [Where to put it](#backend-config-scope).

The stack still declares its backend in code, usually as an empty block:

```hcl
terraform {
  backend "s3" {}
}
```

### Where to put it {#backend-config-scope}

A root `backend_config` is concatenated into every stack and overlays each stack's `backend` block at `init`, exactly as if every `terraform init` of the repository got those `-backend-config` values. A `key=` entry therefore changes the state key of a stack that sets `key` in its block, and `init -reconfigure` then points that stack at a new, empty state object.

- Use a root `backend_config` only in a repository where every discovered stack takes its backend from it: every stack has a partial `backend "s3" {}` block.
- In a repository that mixes partial and full backend blocks, put `backend_config` in the `.stackorder.yaml` of each stack that needs it.
- Keep directories with a full backend block, or with none, out of a root-configured repository with `stacks.exclude`.

### The effective backend {#effective-backend}

The scanner computes each instance's S3 backend without running Terraform. It starts from the literal attributes of the `backend "s3"` block and applies the rendered `backend_config` entries in order: a file is parsed as HCL attributes, and `name=value` sets one attribute. It reads `bucket`, `key`, `region`, `dynamodb_table`, `use_lockfile` and `workspace_key_prefix`, and ignores the rest. A `backend_config` file that does not exist fails the scan.

That backend is what the graph records for the instance. Inferred [`reads_state` edges](./stack-yaml#inferred) and the warning about two stacks sharing one state object therefore work per instance, and catch two instances whose templates render the same key.

## `var_files` {#var-files}

`var_files` is a list of `-var-file` values. Paths are relative to the **stack directory**, whichever file declares them.

```yaml
# stackorder.yaml
var_files:
  - ../common.tfvars
# infra/kyc/.stackorder.yaml
var_files:
  - "regions/{{ .Instance }}.tfvars"
```

An instance's var files are, in order: the root entries, the stack entries, its `from_var_files` file, then the instance entries. Terraform takes the last value of a variable, so a later, more specific file wins: the environment's file beats shared defaults, and the instance's own entries beat the environment's file. Every var file wins over a `TF_VAR_` variable from [`env`](#env). `terraform.tfvars` and `*.auto.tfvars` in the stack directory still load as usual, before the var files.

| Command | Var files |
| --- | --- |
| `stackorder plan` | Passed to `plan` |
| `stackorder apply` from a saved plan | Not passed: the values are in the plan file |
| `stackorder apply` when it re-plans | Passed to the re-plan |
| `stackorder drift` | Passed to `plan` |

A listed var file that does not exist is a warning in the scan, and an error at plan time that names the file.

## `env` {#env}

`env` sets environment variables for the Terraform or OpenTofu process and for the [hooks](./workflows#hooks). A value is a string, or an object with a value per mode.

```yaml
env:
  TF_VAR_environment: "{{ .Instance }}"
  TF_VAR_role:
    plan: reader
    apply: deployer
```

| Mode | Used by |
| --- | --- |
| `plan` | `stackorder plan` |
| `apply` | `stackorder apply`, including its re-plan when the plan file is missing or `apply.from_plan` is `false` |
| `drift` | `stackorder drift`; without a `drift` value the variable takes the `plan` value |

A mode the object leaves out, other than `drift`, leaves the variable as the job already has it: Stackorder neither sets nor removes it.

- **Merging.** Variables merge by name: the instance's value, else the stack's, else the root's.
- **Order.** The variables are set on top of the job's environment and the automation variables. Then the CLI sets `STACKORDER_STACK` (the key), `STACKORDER_STACK_PATH` and `STACKORDER_INSTANCE`, which `env` cannot override.
- **Names.** A name matches `[A-Za-z_][A-Za-z0-9_]*`. It may not start with `STACKORDER_`, `GITHUB_`, `ACTIONS_` or `RUNNER_`, nor be `PATH` or `HOME`.
- **Redaction.** A value whose variable name looks like a secret, such as `DB_PASSWORD`, is masked in everything the CLI reports, under the same rule as the process environment (see [Secrets](/reference/cli#secrets)). The values are still in the repository: never put a secret in `env`.
- **Where it applies.** `env` reaches only what the CLI runs. The workflow steps before it, such as the AWS credentials step, never see it.

### Plan-time values and ephemeral variables {#ephemeral}

A saved plan freezes the value of every input variable it was planned with. `stackorder apply` applies the saved plan, so the `apply` value of a `TF_VAR_` variable has no effect on an ordinary variable: the plan's value is used.

Only an [ephemeral variable](https://developer.hashicorp.com/terraform/language/values/variables) takes its value at apply time. Declare a variable `ephemeral` when its `apply` value must differ from its `plan` value, such as the role a provider assumes:

```hcl
variable "role" {
  type      = string
  ephemeral = true
}
```

Ephemeral input variables need Terraform 1.10 or later, or an OpenTofu release that supports them. An ephemeral variable can be used in provider configuration, but not stored in a resource. The [bootstrap role model](#bootstrap-roles) depends on this.

## Change detection {#change-detection}

A changed file in a stack directory affects every instance of that directory. Instances cannot be told apart by path: `infra/network/main.tf` is code they all run.

A stack also reads files outside its directory: `backend_config` files and var files such as `../common.tfvars`. These are the stack's **watch paths**. A changed path equal to a watch path affects that stack with the reason `watch_path`, so a change to `infra/state.s3.tfbackend` plans every stack that reads it. The API lists them as the stack's `watch_paths`.

The graph's tree hash covers `*.tfvars`, `*.tfvars.json` and `*.tfbackend` files as well as Terraform and Stackorder configuration, so changing a var file never reuses a cached graph.

## Dependencies between instances {#depends-on}

A `depends_on` entry may name an instance, and may use a template:

```yaml
# infra/kyc/.stackorder.yaml
depends_on:
  - infra/network                 # the same instance: infra/network:production for infra/kyc:production
  - infra/dns:shared              # one named instance
  - "infra/iam:{{ .Instance }}"   # the same as a bare path, spelled out
```

A rendered entry without a suffix that names a directory with instances resolves:

1. to the instance of the same name as the depending instance, when the target has one;
2. else to the target's only stack, when it has exactly one;
3. else it is kept as written, with a warning, like any unknown target.

The stack's list and the instance's list concatenate, so an instance can add a dependency the others do not have. `ignore_inferred` works the same way.

A cross-repository entry, `owner/repo//path:instance`, names the instance explicitly: the scanner cannot look inside another repository.

## GitHub environments {#environments}

The apply job of an instance runs under one GitHub environment, chosen in this order:

1. the instance override's `environment`;
2. the stack's `environment`;
3. the best match in the root `environments` map;
4. the instance name;
5. `default`.

An instance is therefore protected by the GitHub environment of its own name unless it is mapped elsewhere. `infra/network:production` applies under `production` with no configuration at all.

`environments` keys take three forms:

| Key | Matches |
| --- | --- |
| `infra/` | Every stack under `infra/`, as before |
| `infra/:production` | The `production` instance of every stack under `infra/` |
| `:production` | The `production` instance in any directory |

Prefixes match whole path segments, and an empty prefix matches every path. The most specific key wins: a key with an instance part before one without, then the longest prefix. Values are templates.

To map instances to GitHub environments with other names, use one of:

```yaml
environments:
  "infra/": "infra-{{ .Instance }}"   # infra/network:production -> infra-production
  ":production": prod-apply           # any production instance -> prod-apply
```

or an override in the stack's `instances` map. A stack with no instance name that matches nothing still runs under `default`.

Only applies run under the instance's environment. Pull request plans have no environment, and the plans and drift checks the server dispatches run under `default`, as for every stack. See [Which environment and role a job gets](./workflows#environments).

## AWS roles {#aws-roles}

The reusable workflows assume one AWS role per job, before the CLI starts. For apply jobs, `aws-role-arn-map` selects it from the stack key:

| Key | Matches |
| --- | --- |
| `infra/` | Every stack under `infra/` |
| `infra/network:production` | That instance only |
| `:production` | The `production` instance in any directory |

The first match wins, in this order: the exact key, then `:instance`, then the longest prefix, then `aws-role-arn`. A key that contains `:` never acts as a prefix. Unlike `environments` there is no `prefix:instance` form; an exact key covers that case. Pull request plan jobs in `plan.yml` select the same way. Plan and drift jobs that the server dispatches ignore the map and keep the single plan role, `aws-plan-role-arn`.

```yaml
aws-role-arn-map: '{":production": "arn:aws:iam::123456789012:role/stackorder-apply-production", ":staging": "arn:aws:iam::123456789012:role/stackorder-apply-staging"}'
```

`aws-role-session-name` names the AWS session, which shows in CloudTrail. It is a name, or a JSON object with a name per mode; `drift` falls back to `plan`, and `plan.yml` always uses `plan`:

```yaml
aws-role-session-name: '{"plan": "stackorder-plan", "drift": "stackorder-drift", "apply": "stackorder-apply"}'
```

Every character outside `[A-Za-z0-9_+=,.@-]` becomes `-`, and the name is cut at 64 characters. Nothing is added to it, so every stack of a job's mode gets the same name. Empty, or an object with no value for the mode, keeps the credentials action's default, `GitHubActions`. A session name is chosen by the workflow, so it is a label, not a security boundary.

Both inputs ship in `stackorder/actions` v1.1.0. See [Reusable workflow inputs](./workflows#inputs).

## One bootstrap role and a provider role per account {#bootstrap-roles}

Many organisations keep state in one account and resources in several. The jobs then assume one **bootstrap** role through OIDC, which can read and write state, and the AWS provider assumes a role in the target account. The provider's role differs between plan and apply, and Stackorder chooses it with `env`.

The example below is for the fictional organisation `acme`, with the repository `acme/infra`:

| Account | Id | Holds |
| --- | --- | --- |
| `shared` | `123456789012` | The state bucket, the two bootstrap roles, and shared resources with their provider roles |
| `staging` | `345678901234` | Staging resources and its provider roles |
| `production` | `210987654321` | Production resources and its provider roles |

### The roles

| Role | Account | Trusted by | Permissions |
| --- | --- | --- | --- |
| `stackorder-plan` | `shared` | OIDC: `repo:acme/infra:pull_request` and `repo:acme/infra:environment:default` | Read state, write the state lock, `sts:AssumeRole` on `arn:aws:iam::*:role/stackorder-reader` |
| `stackorder-apply` | `shared` | OIDC: `repo:acme/infra:environment:<name>` for `shared`, `staging` and `production` | Read and write state, `sts:AssumeRole` on `arn:aws:iam::*:role/stackorder-deployer` |
| `stackorder-reader` | each target account | `arn:aws:iam::123456789012:role/stackorder-plan` | The read-only permissions the providers need to plan |
| `stackorder-deployer` | each target account | `arn:aws:iam::123456789012:role/stackorder-apply` only | The write permissions the stacks need |

The S3 backend uses the bootstrap credentials, not the provider's, so the bootstrap roles need state access and the provider roles need none.

### The provider

Each stack takes its environment and its provider role as variables. The role is `ephemeral`, so an apply of a saved plan uses the apply value instead of the one frozen at plan time.

```hcl
variable "environment" {
  type = string
}

variable "role" {
  type      = string
  ephemeral = true
}

locals {
  account_id = {
    shared     = "123456789012"
    staging    = "345678901234"
    production = "210987654321"
  }[var.environment]
}

provider "aws" {
  region = "us-east-1"

  assume_role {
    role_arn     = "arn:aws:iam::${local.account_id}:role/${var.role}"
    session_name = "stackorder-${var.environment}"
  }
}
```

Without `ephemeral = true`, the apply would reuse the plan's `stackorder-reader` and fail on its first write.

### The configuration

```yaml
# stackorder.yaml
env:
  TF_VAR_environment: "{{ .Instance }}"
  TF_VAR_role:
    plan: stackorder-reader     # drift takes this value too
    apply: stackorder-deployer
```

### The workflows

`stackorder-plan.yml` passes the plan bootstrap role:

```yaml
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
      aws-role-session-name: stackorder-plan
    secrets: inherit
```

`stackorder-run.yml` passes both, with a session name per mode:

```yaml
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
      aws-plan-role-arn: arn:aws:iam::123456789012:role/stackorder-plan
      aws-role-arn: arn:aws:iam::123456789012:role/stackorder-apply
      aws-role-session-name: '{"plan": "stackorder-plan", "drift": "stackorder-plan", "apply": "stackorder-apply"}'
    secrets: inherit
```

The rest of `stackorder-run.yml`, its `run-name` and its dispatch inputs, is on [Workflows](./workflows#run).

### What the trust policies still enforce {#bootstrap-trust}

- **Plan jobs cannot deploy.** A pull request controls its own `.stackorder.yaml` and can set `TF_VAR_role` to `stackorder-deployer` for its plans. The plan bootstrap role may not assume it, because each deployer role trusts only `stackorder-apply`. Keep that trust narrow: it is the boundary.
- **The environment gate decides who gets `stackorder-apply`.** Its trust policy lists the subject of every environment an instance applies under, and nothing else. Never add `environment:default`.
- **AWS no longer tells staging from production.** Once a job holds `stackorder-apply`, it can assume the deployer role of every account. A staging apply runs the pull request's code, and that code can name the production account. To keep the accounts apart, create one apply bootstrap role per environment, each trusting only its own environment's subject, select it with `aws-role-arn-map` keys such as `:production`, and let each account's deployer role trust only its own bootstrap role.
- **Session names are labels.** The workflow chooses them, so a trust condition on `sts:RoleSessionName` adds nothing.

### Limits {#bootstrap-limits}

- **One hour.** A session obtained with another role's session is role chaining, and AWS limits a chained session to one hour whatever the role's maximum session duration. The bootstrap session from `configure-aws-credentials` also lasts one hour by default. An apply that runs longer fails with expired credentials; split stacks that take that long.
- **Plans cannot use environment secrets.** Pull request plans have no GitHub environment and dispatched plans run under `default`, so the provider role, like everything else a plan needs, must come from configuration or the repository's own secrets.

## Migrating from Terrateam {#terrateam}

| `.terrateam/config.yml` | Stackorder |
| --- | --- |
| `dirs` with a list of `workspaces` | Stack discovery with `stacks.discover` or `stacks.include`, and instances from `stacks.instances.from_var_files` or a stack's `instances` list |
| `create_and_select_workspace: false` | The default: an instance selects no workspace and differs by its backend key |
| `create_and_select_workspace: true` | `workspace: "{{ .Instance }}"` at the stack or in an instance override |
| `when_modified` file patterns | Not needed: a stack is affected by its directory, the local modules it uses at any depth, and its [watch paths](#change-detection). `stacks.ignore` removes files; there is no way to add arbitrary patterns |
| Directories that must never run | `stacks.exclude` |
| `apply_requirements` `approved` | `apply.require_approvals`, and `apply.require_codeowner_review` for code owners |
| `apply_requirements` `merge_conflicts` | Always on: a pull request whose merge state is `dirty` is refused |
| `apply_requirements` `status_checks` | No equivalent for arbitrary status checks; a [named check](./workflows#named-checks) from a hook gates the apply |
| `access_control` | `apply.allowed_teams`, per stack or per instance, and the GitHub environment of each instance |
| An `env` step setting `TF_VAR_*` | `env`, with a `plan` and an `apply` value where they differ |
| An `env` step computing the state key | A `key=` entry in `backend_config` with a template |
| `init` `extra_args: ["-backend-config=…"]` | `backend_config`: at the root only when every stack has a partial backend block, otherwise in each stack's `.stackorder.yaml` |
| `TF_CLI_ARGS_plan="-var-file=…"` | `var_files`, or `from_var_files` for one file per instance |
| An `oidc` step with a role per mode | `aws-plan-role-arn` and `aws-role-arn` on the workflows, and `aws-role-session-name` |
| `TERRATEAM_DIR`, `TERRATEAM_WORKSPACE` in scripts | `STACKORDER_STACK_PATH` and `STACKORDER_INSTANCE` in hooks, `.Path` and `.Instance` in templates |
| Hooks and custom workflow steps | `.stackorder/hooks/pre-plan.sh` and the other [hooks](./workflows#hooks) |
| `default_branch_overrides` | Apply policy (`apply`, `environments`, `allowed_teams`, `plan_output`) always comes from the default branch; `env`, `backend_config` and `var_files` come from the commit being planned or applied |
| `engine` and its version | `tool` and `tool_version` |

The last row matters for review. A pull request can change the `env`, `backend_config` and `var_files` its own apply uses, as it can change the code. Put `stackorder.yaml` and every `.stackorder.yaml` under a code owner you trust with production.

## Complete example {#example}

A monorepo with one directory per component, one var file per environment, and one shared backend file:

```text
infra/
  state.s3.tfbackend
  modules/
    vpc/
  network/
    backend.tf                  terraform { backend "s3" {} }
    main.tf
    workspaces/
      production.tfvars.json
      staging.tfvars.json
  kyc/
    .stackorder.yaml
    backend.tf
    main.tf
    workspaces/
      production.tfvars.json
      staging.tfvars.json
  dns/
    .stackorder.yaml
    backend.tf
    main.tf
  state-backend/
    main.tf                     creates the state bucket; local state, applied by hand
  bootstrap/
    backend.tf                  a full backend "s3" block with its own bucket and key
    main.tf                     the OIDC bootstrap roles; applied by hand
```

Every stack that Stackorder runs has a partial `backend "s3" {}` block, so the backend configuration can live at the root. `infra/bootstrap` sets its own key and must not get the root's, so it is excluded with `infra/state-backend`; in a repository where such stacks must run too, move `backend_config` into the `.stackorder.yaml` of each stack that needs it.

`infra/state.s3.tfbackend` holds everything but the key:

```hcl
bucket       = "acme-terraform-state"
region       = "us-east-1"
encrypt      = true
use_lockfile = true
```

`stackorder.yaml`:

```yaml
version: 1

stacks:
  discover: ["infra/**"]
  exclude: ["infra/state-backend", "infra/bootstrap"]
  instances:
    from_var_files: "workspaces/*.tfvars.json"

modules:
  paths: ["infra/modules/**"]

tool: terraform
tool_version: "1.13.0"

backend_config:
  - infra/state.s3.tfbackend
  - 'key={{ trimPrefix "infra/" .Path }}/{{ .Instance }}.tfstate'

env:
  TF_VAR_environment: "{{ .Instance }}"
  TF_VAR_role:
    plan: stackorder-reader
    apply: stackorder-deployer

environments:
  "infra/": "infra-{{ .Instance }}"

apply:
  mode: before_merge
  require_approvals: 1
  require_codeowner_review: true
  four_eyes: true

drift:
  schedule: "0 6 * * 1-5"
  open_issue: true
```

`infra/dns/.stackorder.yaml` has no var files, so it names its one instance:

```yaml
instances: [shared]
```

`infra/kyc/.stackorder.yaml` depends on the network of its own environment and restricts production applies:

```yaml
depends_on:
  - infra/network
instances:
  staging: {}
  production:
    apply:
      allowed_teams: [platform-prod]
```

The graph then holds five stacks:

| Key | State object | GitHub environment |
| --- | --- | --- |
| `infra/network:production` | `network/production.tfstate` | `infra-production` |
| `infra/network:staging` | `network/staging.tfstate` | `infra-staging` |
| `infra/kyc:production` | `kyc/production.tfstate` | `infra-production` |
| `infra/kyc:staging` | `kyc/staging.tfstate` | `infra-staging` |
| `infra/dns:shared` | `dns/shared.tfstate` | `infra-shared` |

A pull request that edits `infra/network/main.tf` plans both network instances in wave 0 and both KYC instances in wave 1. One that edits `infra/state.s3.tfbackend` plans all five. Create the environments `infra-production`, `infra-staging` and `infra-shared` in the repository settings, and let the apply bootstrap role trust each of their subjects.
