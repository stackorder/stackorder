# Concepts

Stackorder works on a dependency graph. The graph has two node kinds, stacks and modules, and three edge kinds. The runner builds the graph; the server stores it and computes the affected set and the apply order from it.

## Stacks {#stacks}

A stack is a directory that Terraform or OpenTofu runs in, with its own state.

- **Discovery.** A directory matching `stacks.discover` (default `stacks/**`) is a stack when it contains a `terraform` block with a `backend "s3"`. Directories listed in `stacks.include` are stacks regardless.
- **Identity.** Inside a repository a stack is identified by its key: the repository-relative path, slash separated, with no `./` and no trailing `/`. A workspace other than `default` is appended after a colon.
- **Environment.** Each stack runs its applies under one GitHub environment, taken from the `environments` prefix map or the stack's own `environment` setting. A stack that matches nothing runs under the environment named `default`, never under an empty name.
- **Tool.** Each stack runs with `terraform` or `tofu`, set at the root and overridable per stack. The CLI expects that binary on `PATH`.

```text
stacks/prod/vpc                        a stack in this repository
stacks/prod/vpc:blue                   the same directory, workspace "blue"
acme/network-infra//stacks/prod/tgw    a stack in another repository
```

The server keeps a stable identity per stack across graphs, so a stack's history survives from commit to commit.

## Modules {#modules}

A module is anything a `module` block points at. Its key depends on where the source lives.

| Kind | Key | Discovered by |
| --- | --- | --- |
| Local | `owner/repo//path` | A `module` block whose `source` is a relative path |
| Git | `owner/repo//path@ref`, or `host/owner/repo//path@ref` off GitHub | A `module` block with a `git::` or `github.com/` source; the `ref` is part of the identity |
| Registry | `registry:namespace/name/provider@version` | A registry `module` block; recorded so the UI can list consumers, and never "changed by a PR" |

```text
acme/infra//modules/vpc                      local module
acme/modules//vpc@v1.2.0                     git module on GitHub
gitlab.com/acme/modules//vpc@v1.2.0          git module on another host
registry:terraform-aws-modules/vpc/aws@5.1   registry module
```

Only local modules propagate changes: a changed path under a local module's directory affects every stack that reaches the module over `uses_module` edges, wherever the module lives in the repository. Directories matching `modules.paths` (default `modules/**`) are never discovered as stacks, and appear in the graph as local modules even when no stack uses them yet.

## Edges {#edges}

| Edge | Direction | Source | Affects order | Propagates change |
| --- | --- | --- | --- | --- |
| `depends_on` | stack to stack | `.stackorder.yaml` in the stack directory; may name a stack in another repo | Yes | Yes |
| `uses_module` | stack or module to module | Parsed from `module` block sources, transitively through nested local modules | No | Yes |
| `reads_state` | stack to stack | Inferred from `terraform_remote_state` data sources whose S3 bucket and key match another stack's backend | Yes (soft) | Yes |

A `uses_module` edge carries the pinned git `ref` of the module. A `reads_state` edge carries the matched bucket and key.

### Inferred edges {#inferred-edges}

`reads_state` is the only inferred edge. The UI draws it dashed and the API marks it `inferred: true`. A stack can promote an inferred edge by listing the stack in `depends_on`, or suppress it with `ignore_inferred`.

No other inference is attempted. Stackorder does not parse `aws_ssm_parameter` lookups or similar; the aim is a graph people can predict.

## An example graph {#example-graph}

Two modules and five stacks. Arrows point from a node to what it depends on. The changed module is outlined in amber, affected stacks in teal, and the untouched module and its edges in grey.

```mermaid
flowchart BT
  subgraph mods ["modules"]
    mvpc{{"modules/vpc"}}
    meks{{"modules/eks"}}
  end
  subgraph w0 ["wave 0"]
    svpc["stacks/staging/vpc"]
    pvpc["stacks/prod/vpc"]
  end
  subgraph w1 ["wave 1"]
    seks["stacks/staging/eks"]
    peks["stacks/prod/eks"]
  end
  subgraph w2 ["wave 2"]
    papps["stacks/prod/apps"]
  end
  svpc -->|uses_module| mvpc
  pvpc -->|uses_module| mvpc
  seks -->|uses_module| meks
  peks -->|uses_module| meks
  seks ==>|depends_on| svpc
  peks ==>|depends_on| pvpc
  papps -. "reads_state (inferred)" .-> peks
  classDef changed stroke:#f59e0b,stroke-width:3px
  classDef affected stroke:#0d9488,stroke-width:2px
  classDef untouched stroke:#9ca3af,stroke-dasharray:4 3
  class mvpc changed
  class svpc,pvpc,seks,peks,papps affected
  class meks untouched
  linkStyle 2,3 stroke:#9ca3af
```

A PR that edits `modules/vpc` affects both VPC stacks through their module edges. The change then reaches the EKS stacks through `depends_on`, and `stacks/prod/apps` through its inferred remote-state edge. `modules/eks` and its edges are recorded but untouched. Waves are the longest path from the roots of the affected subgraph, so the VPC stacks apply first and `stacks/prod/apps` last.

## The affected set {#affected-set}

Given a graph at a commit and the list of changed paths, the server resolves the affected set in five steps.

1. **Directly changed stacks.** Any changed path under a stack directory, after the `stacks.ignore` globs. A path belongs to the deepest stack directory that contains it, so a change inside a nested stack does not affect the stack around it; every workspace of that directory is affected. Paths with a `.terraform` segment are ignored. The defaults ignore `**/*.md` and `**/README*`; `.terraform.lock.hcl` is ignored only when `stacks.ignore_lockfile` is set.
2. **Module-affected stacks.** For every changed path under a local module directory (the deepest one containing it), every stack with a path to that module over `uses_module` edges, through any number of nested modules. Git and registry modules have no directory in the repository and never match here: a change in the module repository does not change consumers until they bump `ref`, which is a change to the consumer itself.
3. **Propagation.** Dependents of the set above over `depends_on` and `reads_state`, transitively, when `propagate.dependents` is true (the default). These stacks are planned so reviewers see the downstream effect. At apply time a propagated stack whose plan is a no-op is recorded as `noop` and skipped.
4. **Ordering.** A topological sort of the affected set over `depends_on` and `reads_state`. Every edge points from the dependent to what it depends on, so for an edge from A to B, B applies first: `wave(B) < wave(A)`. Edges to unaffected or external stacks are dropped. Waves are assigned by longest path from a root. A cycle fails the resolve check with the cycle spelled out, as `a -> b -> a`.
5. **Cross-repo.** `depends_on` edges to stacks in other repositories are stored but cannot order a single-repository run. See [Cross-repo dependencies](/configuration/cross-repo).

Each affected stack carries its reasons: `changed`, `module`, `dependent`, `reads_state` or `requested`, and the node keys the change travelled through.

## Waves {#waves}

A wave is a set of stacks that can apply at the same time. Wave 0 holds the stacks with no affected predecessors; wave n holds the stacks whose longest path from a root has length n. The server dispatches one wave at a time and starts wave n+1 only when every stack of wave n has finished and none failed.

A comment that names a subset, such as `stackorder apply stacks/prod/vpc`, still honours dependency waves within the subset. Stacks outside the subset are `skipped`.

## Locks {#locks}

A lock is an orchestration lock on one stack, held in Postgres. It is not the S3 state lock; Stackorder never touches that.

- Taken on all affected stacks before the first wave of an apply is dispatched.
- Released when the PR merges (`before_merge`) or when the run completes (`on_merge` and manual runs).
- Unique by construction: one lock per stack.
- An apply is refused while another PR holds a lock on any affected stack. Plans still run, with a warning.
- Released explicitly with a `stackorder unlock` comment, the UI, the API, or the `stackorder unlock` command with an API key. Every release is audited.

## Drift {#drift}

Drift is a difference between a stack's state and its code on the default branch. The server's scheduler dispatches a drift run per stack on `drift.schedule`; the CLI runs `plan -detailed-exitcode`, and exit code 2 marks the stack drifted. The UI shows the latest result per stack, and the server can keep one GitHub issue open per drifted stack.

## Module version tracking {#module-versions}

A git module edge carries its `ref`. When a module repository that has the App installed pushes a semver tag, the server records the version. The UI then shows every consumer stack with the ref it pins and how many releases it is behind.

Bumping is left to Renovate or Dependabot. Stackorder only makes the lag visible.

## Runs {#runs}

A run is one pass over a set of stacks at one commit: a plan for a PR, an apply, a drift check or a manual run. A run has a trigger (`pull_request`, `comment`, `push`, `schedule`, `rerequest` or `manual`), a mode (`plan`, `apply` or `drift`) and a status. See [Run states](./how-it-works#run-states).

## Identities and names {#identities}

| Thing | Form |
| --- | --- |
| Stack key | `path` or `path:workspace` |
| Qualified stack key | `owner/repo//key` |
| Run id | A UUID, used in URLs, workflow inputs and check run output |
| Check runs | `stackorder/resolve`, `stackorder/plan`, `stackorder/plan: <key>`, `stackorder/apply`, `stackorder/apply: <key>`, and `stackorder/<check-name>: <key>` for named checks |
| Sticky PR comment | One per PR, found by the hidden marker `<!-- stackorder:sticky -->` on its first line |
| Plan artifact | `stackorder-plan-<key>-<sha>` with `/` and `:` replaced by `-`; the file inside is `<artifact name>.tfplan` |
| Default environment | `default`, for stacks that match no prefix |
