---
title: Cross-repository Terraform dependencies
description: 'Declare Terraform dependencies on stacks in other repositories with owner/repo//key, see them in the graph, and plan downstream stacks after an upstream apply.'
---

# Cross-repo dependencies

A stack can depend on a stack in another repository, read the outputs of a stack in another repository, and use a module from another repository. Stackorder records an edge for each of them, so the graph and the UI span repositories. What it cannot do is order a single run across repositories: each run applies the stacks of one repository.

## Declaring a cross-repo dependency {#declaring}

Qualify the stack key with the repository, as `owner/repo//key`, in the dependent stack's `.stackorder.yaml`:

```yaml
# acme/infra: stacks/prod/vpc/.stackorder.yaml
depends_on:
  - acme/network-infra//stacks/prod/tgw
```

Requirements:

- Both repositories are covered by the same App installation.
- The upstream stack's key is its repository-relative path, with `:instance` when the upstream directory has [instances](./instances), as in `acme/network-infra//infra/tgw:production`. Name the instance explicitly: the scanner cannot see another repository's instances, so a bare path is not resolved to the instance of the same name as it is within a repository. A `depends_on` entry may use a template, such as `acme/network-infra//infra/tgw:{{ .Instance }}`.

The server stores the edge and marks the upstream stack `external` in the dependent repository's graph.

## What happens when the upstream stack applies {#propagation}

`depends_on` edges to other repositories are stored but cannot order a single-repository run. Instead:

1. When an upstream stack applies, the server lists its external dependents on the run. The run's resolution response carries them in `external`.
2. With `propagate.cross_repo: plan` in the upstream repository's default-branch `stackorder.yaml`, the server also starts a plan-only run in each downstream repository once the upstream apply finishes, for the dependent stacks of the stacks that applied. The run has trigger `push`, is for the head of the downstream default branch, carries a warning naming the upstream run, and is dispatched through the downstream repository's `stackorder-run.yml` with `mode: plan`, under the environment `default`. There is one such run per upstream run and downstream repository.

Drift caused by the upstream change then shows up within minutes, rather than at the next scheduled drift check.

```yaml
# stackorder.yaml of the repository whose stacks others depend on
propagate:
  cross_repo: plan   # off | plan
```

The default is `off`: external dependents are listed on the run, and nothing is dispatched.

A cross-repo plan run never applies. Applying the downstream stack is a change in its own repository, through its own pull request and apply gate.

## Reading outputs across repositories {#outputs}

A stack can read the root-module outputs of a stack in another repository through the server, with [shared outputs](./outputs), instead of reading its state. The producer lists the reader under `outputs.readers`, qualified with the reader's repository, on its default branch:

```yaml
# acme/network-infra: stacks/prod/tgw/.stackorder.yaml
outputs:
  readers:
    - acme/infra//stacks/prod/*
```

```hcl
# acme/infra: stacks/prod/vpc/main.tf
data "stackorder_outputs" "tgw" {
  stack = "acme/network-infra//stacks/prod/tgw"
}
```

- Both repositories are covered by the same App installation. A reader of another installation is refused.
- `owner/repo` in a reader is literal and compares without regard to case; the stack key part is a pattern. An entry without `//` never matches a stack of another repository.
- Across repositories, a stack that does not exist, does not list the reader or has an `outputs` configuration that does not resolve answers `404 not_found`, with one message for every cause, so a reader cannot tell them apart. Within one repository a reader that is not listed gets `403 outputs_not_shared`.
- A `stackorder_outputs` block whose `stack` is a literal `owner/repo//key` makes the scanner add an inferred `reads_state` edge to that stack, marked `external`, as a cross-repo `depends_on` does. The reading stack is then one of the upstream stack's external dependents, and `propagate.cross_repo: plan` plans it after an upstream apply.

## Modules from other repositories {#modules}

A `module` block with a `git::` or `github.com/` source creates a `uses_module` edge to a git module whose identity includes the `ref`:

```hcl
module "vpc" {
  source = "git::https://github.com/acme/modules.git//vpc?ref=v1.2.0"
}
```

Git-pinned modules never make a consumer "changed" in a pull request. A change in the module repository does not change consumers until they bump `ref`.

When the module repository has the App installed and pushes a semver tag, the server records the version. The module page in the UI then lists every consumer stack, the ref it pins, and how many releases it is behind. Bumping is left to Renovate or Dependabot.

<Screenshot
  name="ui-module-consumers"
  alt="A module page in the web UI for the git module acme/terraform-modules//eks-addons: four released versions from v0.7.2 to v0.10.0, and three consumer stacks in two repositories, two pinned to v0.8.0 and two versions behind, one pinned to v0.10.0 and up to date."
  :width="780"
  :height="653"
  caption="The module page of a git module in the web UI, with sample data. One consumer is in another repository, acme/platform-infra."
/>

## Viewing the cross-repo graph {#viewing}

- The repository graph, in the UI and from `GET /v1/repos/{owner}/{repo}/graph`, includes upstream stacks from other repositories, marked `external: true`.
- `GET /v1/stacks/{id}` lists a stack's `depends_on` and `dependents`.
- `GET /v1/modules/{id}` lists a module's versions and consumers across repositories.

See the [API reference](/reference/api).
