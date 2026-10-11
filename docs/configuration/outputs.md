---
title: Share Terraform outputs without sharing state
description: 'Let one stack read another stack''s Terraform outputs through the Stackorder server, so its role needs no access to that stack''s state, and choose the readers.'
---

# Shared outputs

A stack that needs another stack's values usually reads them with `terraform_remote_state`. Its AWS role then needs read access to the other stack's whole state object, secrets included, and so does every job that runs with that role.

With shared outputs, the reading stack, the **consumer**, gets the root-module outputs of the other stack, the **producer**, through the Stackorder server instead. The server reads the producer's state object with its own AWS credentials, keeps only the `outputs` block and returns it. The consumer's role needs no access to the producer's state, and the producer decides which stacks may read its outputs.

The trade is on the server's side. When an operator turns the feature on, the server has read access to the whole state objects the operator lists, secrets included, even though it returns only the outputs, and it stores, caches and logs nothing it reads. The [security model](/reference/security-model) covers what the server can reach.

Shared outputs are off until an operator [turns them on](#server); until then every read answers `404 outputs_disabled`. A read goes like this:

1. The consumer's job runs Terraform with the [`stackorder` provider](#consumer). A `stackorder_outputs` block names the producer, and the provider calls [`GET /v1/repos/{owner}/{repo}/stacks/{key}/outputs`](/reference/api#stack-outputs) with the job's OIDC token, the run id and the consumer's stack key.
2. The server checks that the job runs the consumer in that run ([Who may read](#who-may-read)), reads the producer's `outputs` configuration from the producer's default branch, and checks that it lists the consumer.
3. It reads the state object that the producer's last apply or drift job reported ([Where the state is read from](#state-location)), provided the operator granted that location to the producer's repository.
4. It returns the outputs, without the sensitive ones unless the producer shares them, and records the read in the [audit log](#audit) with counts only.

## Sharing a stack's outputs {#producer}

Nothing is readable until the producer opts in, inside one repository too. On the producer's default branch, list the stacks that may read its outputs:

```yaml
# acme/infra: stacks/prod/vpc/.stackorder.yaml
outputs:
  readers:
    - stacks/prod/eks               # a stack of this repository
    - stacks/prod/apps:*            # every instance of a stack
    - acme/platform//clusters/**    # stacks of another repository, same installation
  sensitive: share                  # default: withhold
```

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `outputs.readers` | list of patterns | unset | The stacks that may read the outputs. Unset means the outputs are not shared, and `[]` shares them with no stack. Entries may be [templates](./instances#templates). See [Readers](#readers). |
| `outputs.sensitive` | `share` or `withhold` | `withhold` | Whether the outputs marked `sensitive` are served. See [Sensitive outputs](#sensitive). |

Upgrade the CLI before adding `outputs` to a configuration: an older CLI rejects the unknown key, which fails the resolve job of every pull request. The reusable workflows install the latest release unless [`stackorder-version`](./workflows#inputs) pins one.

### Readers {#readers}

A reader is a pattern over stack keys:

| Entry | Matches |
| --- | --- |
| `stacks/prod/eks` | That stack of the producer's repository |
| `stacks/prod/apps:*` | Every instance of `stacks/prod/apps` |
| `stacks/prod/*` | Every stack directly under `stacks/prod`, instances included |
| `stacks/**/*:blue` | Every `blue` instance under `stacks` |
| `**` | Every stack of the producer's repository |
| `acme/platform//clusters/**` | Every stack under `clusters` in `acme/platform` |

- A pattern is a [doublestar](https://github.com/bmatcuk/doublestar) glob over the whole key. `*` does not cross `/`, and `**` crosses `/` only as a whole path segment, so `stacks/**:blue` matches only the `blue` instances one level under `stacks`; write `stacks/**/*:blue` for every level. `:` is an ordinary character.
- An entry without `//` matches stacks of the producer's own repository only. `owner/repo//pattern` matches stacks of another repository covered by the same App installation. `owner/repo` is literal, never a pattern, and compares without regard to case. A stack of another installation never reads.
- Spell a pattern as a stack key is spelled: no surrounding spaces, no `./`, `//`, `..` or trailing `/`, and no `:default` or empty instance. Validation names the spelling to write instead.
- An entry may be a template, rendered for each instance of the producer, as `depends_on` entries are. `'infra/kyc:{{ .Instance }}'` in the `.stackorder.yaml` of `infra/network` lets `infra/kyc:production` read `infra/network:production`, and `infra/kyc:staging` read `infra/network:staging`.
- Readers name keys, not stacks. A renamed reader no longer matches, and a new stack that takes a listed key inherits the grant. The [audit log](#audit) records every read.

### Where to set it {#levels}

`outputs` exists at three levels:

| Level | File | Shares the outputs of |
| --- | --- | --- |
| Root | `stackorder.yaml` | Every stack of the repository |
| Stack | The stack's `.stackorder.yaml` | Every instance of the stack |
| Instance | An override under the stack's [`instances`](./instances#overrides) | One instance |

`readers` comes from the most specific level that sets it, instance over stack over root, and replaces the others rather than adding to them. Unlike `apply.allowed_teams`, an empty list counts as set: `readers: []` on a stack or an instance withdraws a broader grant. Unset at every level, the outputs are not shared. `sensitive` takes the most specific value that is set.

```yaml
# stackorder.yaml: every stack shares its outputs with the stacks under stacks/prod
outputs:
  readers: ["stacks/prod/**"]
```

```yaml
# stacks/prod/secrets/.stackorder.yaml: this stack shares with no stack
outputs:
  readers: []
```

```yaml
# infra/network/.stackorder.yaml: only the production instance sets its own readers
instances:
  staging: {}
  production:
    outputs:
      readers: ["infra/kyc:production"]
```

An `outputs` key with no value, a `readers` key with no value and an empty item in `readers` are errors, so that none of them can pass for unset or for no reader. Write `readers: []` to share with no stack.

### Sensitive outputs {#sensitive}

With `sensitive: withhold`, the default, the outputs marked `sensitive` in the producer's state are left out of the response, and their names are listed in `withheld`, so that the provider can warn about them. With `sensitive: share` they are served, with `sensitive: true`.

The default protects the values from where a consumer keeps them. The values a data source reads land in the consumer's saved plan, which Stackorder uploads as an Actions artifact, and in the consumer's state, and both are often readable by more people than the producer's state. When a producer shares its sensitive outputs, read them with the [ephemeral resource](#consumer), which keeps them out of both.

### Read from the default branch {#default-branch}

The server reads `outputs` from the producer's default branch only: the root level from the `stackorder.yaml` it stored for the default branch, and the stack's `.stackorder.yaml` through the GitHub Contents API at the commit that configuration was read at. It never takes `outputs` from a pull request, a graph upload or the CLI, so a pull request cannot change who a producer shares with. A change to `outputs` takes effect once it reaches the default branch. A pull request still chooses the stacks of its own plan run, so it can read whatever is shared with any stack key of its repository: see [Who may read](#who-may-read).

- The server reads `stackorder.yaml` again on every push to the default branch. An invalid file keeps the previous configuration, and the stack files are then still read at that configuration's commit, so an invalid push changes no grant.
- A stack's `.stackorder.yaml` that does not parse or render shares nothing.
- The producer must be a stack the default branch still has at that same commit: `stackorder.yaml` still discovers its directory, the directory exists, and the key names one of the stack's instances, or none for a stack without instances. A stack that only an open pull request adds cannot be read, and neither can one whose directory the default branch has deleted, even while its last state object is still in the bucket.
- The instances are those the stack's `.stackorder.yaml` declares at that commit, else those `stacks.instances.from_var_files` derives from the var files there, which the server lists through the Contents API. An instance whose var file the default branch has deleted cannot be read.
- The directory and the instance come from the producer's key, and the levels merge as for any [instance](./instances#merge).

## Who may read {#who-may-read}

A consumer's job proves who it is with its GitHub Actions OIDC token, which the server checks as for every [runner call](/reference/api#oidc-binding). With the token, the provider names the run it belongs to and the reading stack, from the `STACKORDER_RUN_ID` and `STACKORDER_STACK` variables the CLI sets for Terraform. The server accepts the read only when that stack is a stack of the run whose job has not reported yet, the run is still in progress, and the producer's `readers` match the stack.

| Caller | What the server proves | What it cannot tell |
| --- | --- | --- |
| A pull request plan job | The repository, the pull request, and that the reader is a stack of that pull request's plan run | Which job of the run calls. The pull request chooses the stacks of its plan run, so the boundary is the repository: any of its plan jobs can read what is shared with any stack key of the repository, a key the pull request adds included |
| A dispatched plan or drift job | That the reader is a stack of the dispatch its workflow run is bound to | Which job of the dispatch; exact when the dispatch holds one stack, as a scheduled drift check's does |
| A dispatched apply job | That the reader is a stack of the chunk its workflow run is bound to, under the stack's environment | Which job of the chunk; exact with [`apply.max_parallel: 1`](./stackorder-yaml#apply) |
| An API key | Nothing about a stack: API keys are administrators | Which stack, if any, the caller acts for; the key reads the outputs of any stack, whatever its `readers` |

- Between repositories, `readers` is a hard boundary: the token's signed `repository` and `repository_id` claims name the reader's repository.
- Within one repository, at pull request plan time, the boundary is the repository. A pull request chooses the stacks of its plan run: it can change any stack's directory or add a stack under any key, and a reader only has to be a stack of the run, not of the default branch as a producer must. So any pull request whose jobs get an OIDC token, which a fork's do not, can read whatever is shared with any stack key of its repository, including a key it only adds in that pull request. That matches per-stack AWS roles, where any job of a pull request can assume any role whose trust policy admits `pull_request`.
- Only for a dispatched job does the boundary narrow, to the stacks of its dispatch or, in an apply, of its chunk.
- A dispatched job reads only as a stack of the dispatch that carries it, of its own wave and environment; a stack an apply skips has none. A read binds a workflow run to a dispatch on first contact only when the workflow run's jobs name a stack of that dispatch.
- A superseded run answers `409 superseded`, and a finished run, or a pull request's plan run once it is `planned`, `409 conflict`.
- An API key needs neither a run nor a reader, and its reads are audited like any other. Keep API keys out of CI.

## Where the state is read from {#state-location}

The server reads the S3 object that the producer's last apply or drift job found its state in. The CLI reports that object with every result, as [`state`](/reference/api#stack-result): the bucket, the full key with the workspace prefix, and the region, taken from what `init` recorded, after `backend_config` and `STACKORDER_BACKEND_CONFIG`, and from the workspace the tool uses. The server records it only from apply and drift results, never from a plan result, a graph or the backend the scan computes, which pull request jobs can write.

- Until the producer has had an apply or drift job on a CLI that reports the state object, reads answer `409 state_location_unknown`. A [scheduled drift check](./drift) records it without changing anything.
- Only a producer whose backend `init` recorded as `s3`, with a bucket and a key, can share its outputs. For any other backend the CLI reports no state object, and reads answer `409 state_location_unknown` however many jobs run.
- The object must fall under an entry of `STACKORDER_STATE_BUCKETS` that the operator granted to the producer's repository, or to every repository with `*`. A result whose object no such entry covers does not record it: it clears the location the server had for the stack, unless a newer run recorded that location, and the server logs it at warn level. Reads then answer `409 state_location_unknown` instead of serving the outputs of the object the producer left, until an apply or drift job reports an object that an entry covers. A read of a recorded location that no such entry covers, such as one recorded before the entries changed, answers `403 state_bucket_not_allowed` without a request to S3.
- The server reads state format version 4, which Terraform and OpenTofu write. State that OpenTofu encrypted is refused with `state_encrypted`; the server never decrypts state.
- A read returns the outputs the producer's state holds now. A pull request that changes both stacks plans the consumer against the outputs of the producer's last apply, as `terraform_remote_state` does.

Between repositories the grant is a hard boundary, unless the operator grants an entry to `*` or lets the entries of two repositories overlap: a repository's jobs can make the server read only what the operator granted that repository. An entry whose location covers another repository's, such as `acme/infra=acme-tfstate/stacks/` beside `acme/network=acme-tfstate/stacks/network/`, grants its repository that state too, and the server logs a warning at start-up for each such pair. Within one repository the grant is not a boundary between stacks. An apply or drift job reports the object its own code initialised, and the apply of a pull request in the default [`before_merge`](./stackorder-yaml#apply) mode runs the pull request's code, so a pull request of the producer's repository can point one stack at another stack's state under the same grant, and the readers of the first stack then get the second stack's outputs. That is why `readers` is set per stack and why the apply that reports the location is a step to review like the plan. Grant a repository only the prefixes that hold the state of its stacks that share outputs, so that the state of a stack that shares nothing lies outside every grant and no stack can be pointed at it.

## Reading outputs in Terraform {#consumer}

A consumer reads outputs with the `stackorder` Terraform provider, from [stackorder/terraform-provider-stackorder](https://github.com/stackorder/terraform-provider-stackorder). Its publication to the Terraform and OpenTofu registries is pending; the provider repository has the usage documentation.

```hcl
terraform {
  required_providers {
    stackorder = {
      source = "stackorder/stackorder"
    }
  }
}

provider "stackorder" {}

data "stackorder_outputs" "vpc" {
  stack = "stacks/prod/vpc"
}

resource "aws_security_group" "cluster" {
  name   = "eks-cluster"
  vpc_id = data.stackorder_outputs.vpc.nonsensitive_values.vpc_id
}
```

- Inside a Stackorder job the provider block needs no settings. The CLI sets `STACKORDER_SERVER_URL`, `STACKORDER_RUN_ID`, `STACKORDER_REPOSITORY` and `STACKORDER_STACK` for Terraform (see [Environment](/reference/cli#environment)), and the reusable workflows grant `id-token: write`. Elsewhere the provider needs the server URL and an API key; its documentation has the settings.
- `stack` is the producer's key: `path` or `path:instance` for a stack of the consumer's repository, `owner/repo//key` for a stack of another repository.
- `values` holds every output served and is sensitive as a whole; `nonsensitive_values` leaves out the outputs marked sensitive.
- `ephemeral "stackorder_outputs"` takes the same `stack` and returns the same values, but Terraform keeps them out of the saved plan and the state. Use it for sensitive outputs. Ephemeral resources need Terraform 1.10 or later, or OpenTofu 1.11 or later.

## Ordering {#ordering}

The scanner infers a `reads_state` edge from the consumer to the producer for each `data` or `ephemeral` `stackorder_outputs` block whose `stack` is a literal string, in a `.tf` or `.tf.json` file of the consumer's directory or of a [local module](#modules) the consumer calls. The edge orders applies and propagates change like any [`reads_state` edge](/guide/concepts#edges), and carries `source: stackorder_outputs`.

- A `path` or `path:instance` must equal a stack key of the repository exactly. A bare path of a directory with instances names no stack and only warns: name the instance.
- `owner/repo//key` adds the producer to the graph as an external stack, as a cross-repository `depends_on` does. See [Cross-repo dependencies](./cross-repo).
- A `stack` built from variables or expressions infers no edge and warns: list the producer in [`depends_on`](./stack-yaml#depends-on). In a module, a `stack` built only from the module's variables can take them from the `module` block that calls it: see [Blocks in modules](#modules).
- A `depends_on` entry for the same producer replaces the inferred edge, and an [`ignore_inferred`](./stack-yaml#inferred) entry suppresses it. Qualify the entry with `owner/repo//` for a producer in another repository.

### Blocks in modules {#modules}

The scanner also reads the `stackorder_outputs` blocks of every local module a stack calls through a relative `source` inside the repository, and of the local modules those call in turn. Each block infers the edge from the stack under the rules above, the stack's `depends_on` and `ignore_inferred` included. A module that no stack calls infers nothing.

```hcl
# acme/infra: modules/vpc-lookup/main.tf
variable "producer" {
  type = string
}

data "stackorder_outputs" "vpc" {
  stack = var.producer
}

output "vpc_id" {
  value = data.stackorder_outputs.vpc.nonsensitive_values.vpc_id
}
```

```hcl
# acme/infra: stacks/prod/eks/main.tf
module "vpc" {
  source   = "../../../modules/vpc-lookup"
  producer = "stacks/prod/vpc"
}
```

- A `stack` that refers only to `var.<name>` values and calls no function, such as `var.producer` or `"stacks/${var.env}/vpc"`, takes them from the arguments of each `module` block that calls the module, when those arguments are literals; a variable's `default` is not used. A function call stops the resolution even over literal arguments, so `lower(var.producer)` or `"stacks/${lower(var.env)}/vpc"` resolves nothing and warns. The values come from the calling `module` block only, one level up: an argument that is itself a variable of the calling module, such as `producer = var.producer` in a module that wraps the lookup, resolves nothing. Above, `stacks/prod/eks` reads `stacks/prod/vpc`.
- A `stack` that does not resolve, is malformed or names no stack of the repository infers nothing and warns that only `depends_on` orders the stacks that call the module: list the producer in the `depends_on` of each of them. Like the warning about a non-literal `stack` in a stack's own directory, it stays once the entries are there.
- A warning about a block in a module names the module's directory, as in `data.stackorder_outputs.vpc in module modules/vpc-lookup`. One about a value that a `module` block passed points at that `module` block, once per call; any other points at the `stackorder_outputs` block, once per block.
- A block whose `stack` is a stack that calls the module is ignored for that stack, with a warning; the other stacks that call the module still get the edge.
- A module directory that is also a stack directory does not repeat the warnings that stack gives about its own blocks, and a module that calls back into the stack's own directory does not read it twice.
- Registry and git modules are not fetched, so a block inside one infers nothing and warns nothing: list the producer in the `depends_on` of the stack that calls the module.
- `terraform_remote_state` blocks are read only in the stack's own directory, never in a module.

## Turning it on {#server}

An operator turns shared outputs on in the [server configuration](/reference/server-configuration#shared-outputs):

| Variable | Meaning |
| --- | --- |
| `STACKORDER_STATE_BUCKETS` | Comma-separated `owner/repo=bucket` or `owner/repo=bucket/prefix` entries: the state objects the server may read for the stacks of each repository. `*=bucket` or `*=bucket/prefix` grants an entry to every repository. Setting it turns shared outputs on; unset, every read answers `404 outputs_disabled`. |
| `STACKORDER_STATE_ROLE_ARN` | An IAM role the server assumes to read them, such as a role in the account of the state buckets. Optional; it requires `STACKORDER_STATE_BUCKETS`. |

- Each entry belongs to one repository, compared without regard to case, and the server records and reads a stack's state object only under an entry of the stack's own repository. A repository therefore cannot point its stacks at another repository's state to serve that state's outputs to the stacks it lists, as long as no entry of its own, or of `*`, covers that state too. Keep the prefixes of different repositories apart: at start-up the server logs a warning for each pair of entries of different repositories where one's location lies under the other's.
- `*` in place of the repository is an explicit opt-in to exposure across repositories: every repository the server knows may then point its stacks at any object under that entry and serve its outputs. Use it only for state that every repository may expose; `*` is accepted only as the whole repository.
- A prefix covers a key only at a path segment boundary: the key is the prefix, the prefix ends with `/`, or the key goes on with `/` after it. `acme/infra=acme-tfstate/stacks/prod` allows `stacks/prod/vpc.tfstate` but not `stacks/production/vpc.tfstate`. A key with a `.` or `..` segment is never allowed, and an entry whose prefix has one stops the server at start.
- An entry without a repository, such as a bare `acme-tfstate/stacks/prod/`, stops the server at start with a message that shows these forms.
- The [Terraform module](/operations/deploy-aws#shared-outputs) sets both variables from its `shared_outputs_state` and `shared_outputs_role_arn` inputs. Without a role it grants the task role the reads: `s3:GetObject` on the objects under each prefix, `s3:ListBucket` limited to the same prefixes, so that a missing object answers `state_not_found` rather than `state_access_denied`, and `kms:Decrypt` through S3 only. With a role it grants only `sts:AssumeRole` on it, and the role's own policies grant the reads; its trust policy must admit the task role, or every read answers `403 state_access_denied`. Nothing grants write access.
- List the narrowest prefixes that hold the state of the stacks that share outputs, one per repository, not whole buckets: the server has read access to whole state objects, secrets included. IAM grants the server's role every entry's prefix at once; the per-repository check is the server's.
- The server reads each object in the region the producer's backend names, else in its own default region, and asks once more in the region S3 names when the bucket is elsewhere.

| Limit | Value | Beyond it |
| --- | --- | --- |
| State scanned before the outputs | 64 MiB | `422 state_too_large` |
| The outputs, and any single string or number scanned before them | 4 MiB | `422 state_too_large` |
| One read, from the moment it has a slot | 30 s | `503 unavailable` |
| Reads at a time, per server instance | 4 | Further reads wait for a slot |
| Waiting for a slot | 20 s | `503 unavailable` |

The server decodes only the `outputs` block and stops reading once it has it. Terraform and OpenTofu write the outputs before the resources, so the scan limit matters only for a state written in another order. Nothing it reads is stored, cached or logged: responses carry `Cache-Control: no-store`, and decoding errors name byte offsets, never state content.

## Audit {#audit}

Every read is recorded in the [audit log](/reference/api#audit) as `outputs_read`, with the number of outputs served, of sensitive outputs served and of outputs withheld. A refusal of the caller, the run, the producer or the producer's policy is recorded as `outputs_refused`, with the error code as `reason`, `state_location_unknown` included, and so is a location that no entry of `STACKORDER_STATE_BUCKETS` grants the producer's repository, with `state_bucket_not_allowed`; the server also logs that refusal at warn level with the repository, the stack, the bucket and the key. `outputs_disabled` is not recorded, and neither is any other code of the state read itself, nor a read that fails at S3 or while decoding the state. Neither row holds a value.

```json
{
  "at": "2026-10-10T09:14:03Z",
  "actor": "octocat",
  "action": "outputs_read",
  "target": "acme/infra//stacks/prod/vpc",
  "details": {
    "repo": "acme/infra",
    "stack": "stacks/prod/vpc",
    "reader_repo": "acme/infra",
    "reader": "stacks/prod/eks",
    "run_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
    "outputs": 5,
    "sensitive": 0,
    "withheld": 1
  }
}
```

`actor` is the job's `actor` claim, or `apikey:<name>` for an API key. `target` is the producer's qualified key. `details.repo` is the producer too, so the producer's organisation sees the reads of its stacks, except when a runner job is refused before the server has found the producer: that row carries the job's own repository.

## Errors {#errors}

The [API reference](/reference/api#stack-outputs) lists every code a read can answer, with its HTTP status, and [Troubleshooting](/operations/troubleshooting#shared-outputs) says what to do about each.
