---
description: 'The five layers that gate a Terraform apply in Stackorder, from allowed teams and code owners to GitHub environments and IAM trust policies pinned by OIDC.'
---

# Environments and authorization

Write access to the repository is the floor, not the ceiling. An apply passes up to five layers, and only the two that GitHub and AWS enforce are security boundaries. The server-side checks give fast, readable refusals; the environment gate and the IAM trust policy are what actually stop an unauthorised apply.

| Layer | Enforced by | What it gates | Availability |
| --- | --- | --- | --- |
| 1. `apply.allowed_teams` | The server | Who may *request* an apply | Repositories owned by an organisation; needs the App's `Members: Read` |
| 2. Code-owner approval | GitHub (merge) and the server (before-merge apply) | Who must *approve* the change | The server check on all plans; the merge rule needs branch protection, which private repos on GitHub Free lack |
| 3. GitHub Environment with required reviewers | GitHub | Whether the apply *job may start* | Public repos on all plans; private repos need GitHub Enterprise |
| 4. Custom deployment protection rule | GitHub, with the server's logic | Same as 3, decided automatically | Same as 3; extra App permission and event |
| 5. IAM trust policy pinned to the environment | AWS | Whether the job can obtain *credentials* at all | All plans |

## Recommended default {#recommended}

- **Layer 1** for the error message.
- **Layer 3**, with a team as required reviewer, for the hard stop.
- **Layer 5** to make it airtight.

Layer 4 is the upgrade for teams that find the manual approval redundant with code review. The plan role stays open to anyone with write access: planning is read-only, and the plan role has no write permissions.

A repository owned by a personal account, or a private repository on GitHub Free, cannot have most of these layers. See [Personal accounts and GitHub Free](#free-plan).

## Layer 1: team check on the request {#layer-1}

On `stackorder apply`, the server checks the commenter's membership of the teams in `apply.allowed_teams` and refuses with a comment naming the teams unless the commenter is an `active` member of one of them. Members of nested child teams count. A team is written as its slug, in the repository owner's organisation, or as `org/slug`, with or without `@`. Results are cached for 60 s to stay clear of rate limits.

```yaml
# stackorder.yaml
apply:
  allowed_teams: [platform-eng]
```

```yaml
# stacks/prod/vpc/.stackorder.yaml
apply:
  allowed_teams: [platform-prod]
```

- The rule is per stack. A run touching `stacks/prod/**` and `stacks/staging/**` requires the commenter to satisfy every affected stack's `allowed_teams`, or to name a subset.
- With no `allowed_teams`, the check falls back to push permission on the repository.
- It needs the App's `Members: Read` permission, which the App created by `/setup` has. Without it the gate refuses with a reason saying so.
- A personal account has no teams. On a repository it owns, a bare slug names no team, so every apply is refused; leave `allowed_teams` empty.

This layer fails fast with a good message, and nothing more. A bug or a compromised server skips it.

## Layer 2: code-owner approval {#layer-2}

Map stack paths to owning teams in `CODEOWNERS`, and require code-owner review in branch protection:

```text
# .github/CODEOWNERS
/stacks/prod/    @acme/platform-prod
/stacks/staging/ @acme/platform-eng
```

- In `on_merge` mode that alone is a hard gate: apply follows merge, and GitHub will not merge without the owning team's approval.
- In `before_merge` mode, set `apply.require_codeowner_review: true`. The apply gate then requires, for each affected stack, at least one `APPROVED` review on the current head SHA from one of the stack's owners in the default branch's `CODEOWNERS`, a listed user or a member of a listed team, who has push permission. Reviews on older commits, and the author's own, do not count; a stack no rule owns passes.
- `apply.four_eyes: true` refuses an apply requested by the PR author.

```yaml
apply:
  mode: before_merge
  require_approvals: 1
  require_codeowner_review: true
  four_eyes: true
```

This is still a server check, but it reuses GitHub's review audit trail and pairs with the merge rule.

## Layer 3: GitHub Environments {#layer-3}

The apply job in the reusable `run.yml` declares `environment: ${{ matrix.environment }}`. The server assigns each stack an environment from the `environments` map in `stackorder.yaml`, overridable per stack with `environment` in `.stackorder.yaml`.

```yaml
environments:
  "stacks/prod/": production
  "stacks/staging/": staging
```

### Per instance {#instances}

Each [instance](./instances) of a directory has an environment of its own. Without any mapping, an instance applies under the environment of its own name, so `infra/network:production` is gated by `production`. Map instances elsewhere with a key that has an instance part, or with a template:

```yaml
environments:
  ":production": production          # the production instance of every directory
  "infra/:staging": staging          # the staging instances under infra/
  "legacy/": "legacy-{{ .Instance }}" # one environment per instance name under legacy/
```

A key with an instance part wins over one without, then the longest prefix wins. An instance override in the stack's `instances` map, or the stack's own `environment`, wins over the map. See [GitHub environments](./instances#environments).

The server reads the environment of an apply from the default branch's configuration, per instance, never from the pull request's own files. A pull request cannot remap a stack or instance the default branch declares.

It can add new ones, though. A pull request can add a stack directory, or an instance of an existing directory, that the default branch does not declare, and it chooses the name. The apply environment of such a key follows the default branch's rules as they stand: the directory's own `environment` in its default-branch `.stackorder.yaml`, else the best match in the default branch's `environments` map, else the environment of the instance's own name, else `default`. An instance nothing maps therefore runs under the environment of its own name, and GitHub creates an environment it does not know unprotected on first use. Suppose the default branch declares `infra/kyc:production` under `infra-production`. A pull request that adds an instance `hotfix` to `infra/kyc`, with the production state object in its `backend_config`, applies under `hotfix`, or under `infra-hotfix` with a template such as `"infra/": "infra-{{ .Instance }}"`. Neither has required reviewers or a protection rule, so layers 3 and 4 do not hold the job, and the `allowed_teams` of the `production` override do not apply to it; the stack's or the root's do.

What stops such an apply is [layer 5](#layer-5): the role that can write the production state trusts only the `production` environment's subject, so a job under `hotfix` gets no credentials for it. Narrow the gap further:

- Map every path prefix that holds stacks to an existing, protected environment with a fixed name, such as `"infra/": infra-apply`, rather than a template that yields a new name per instance.
- Put every `.stackorder.yaml` and every directory that holds var files under a trusted code owner in `CODEOWNERS`, with a rule broad enough to own directories a pull request adds.
- In `before_merge` mode, set `apply.require_codeowner_review: true`, so an owner of each stack approves the head commit before it applies. A stack no rule owns passes that check.

In the repository settings, under **Environments**, configure `production`:

- **Required reviewers**: a team, or on a personal account, users. The job pauses until a listed user or team member approves in the Actions UI.
- **Prevent self-review**: on, so the requester cannot approve their own deployment.
- **Deployment branches**: the default branch only. Server-dispatched runs start from the default branch.

Only apply jobs run under the stack's environment. The plans and drift checks the server dispatches run under the environment `default`, so a reviewer on `production` never holds them; give `default` no reviewers.

The server cannot approve: the App has no Environments permission, and an App cannot be a required reviewer. This gate holds even if the server is fully compromised.

Two consequences of the design:

- The server dispatches each wave as one run per environment it touches, so a mixed run does not hold staging behind the production reviewer. The sticky PR comment links straight to the pending approval.
- With one dispatch per wave, a three-wave production apply asks for three approvals.

## Layer 4: the App as a deployment protection rule {#layer-4}

Instead of a human clicking approve, register the App as a custom deployment protection rule on the environment. When the apply job wants to start, GitHub sends a `deployment_protection_rule` webhook. The server approves or rejects the deployment with exactly the checks from layers 1 and 2.

That turns the server's policy into something GitHub enforces: the job does not run until the App says yes, and the App says yes only for a request from the right team on a PR with the right approvals.

The server answers each request this way:

| Request | Answer |
| --- | --- |
| A workflow run the server did not dispatch, a re-run of a dispatch that already completed, a run that already finished, or a different environment than the dispatch's | Rejected |
| A plan or drift dispatch | Approved: read-only, nothing is applied |
| An apply | Approved when layer 1 passes for the requester and, for an apply requested by a comment, layer 2 passes on the run's commit; otherwise rejected with the refusal as the comment |

To enable it:

1. The App that `/setup` creates already has the `Deployments: Read and write` permission and the `deployment_protection_rule` event, except on GitHub Enterprise Server. Otherwise grant both in the App's settings; each installation must accept the new permission.
2. In the environment's settings, enable the App under custom deployment protection rules.

Layer 4 has the same plan constraint as layer 3. Layers 3 and 4 can be combined on one environment, in which case every rule must pass. Even with this permission the server cannot approve a human gate; it only answers for its own rule.

## Layer 5: the AWS trust policy {#layer-5}

None of the above matters if a tampered workflow can assume the apply role directly. Condition the role's trust policy on the environment:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": "repo:acme/infra:environment:production"
        }
      }
    }
  ]
}
```

`repo:acme/infra:environment:production` appears in the token's `sub` only when the job ran under that environment, and so only after its protection rules passed.

`repo:acme/infra` is the name-only form of the repository's subject prefix. A repository created after July 15, 2026 has an immutable prefix with numeric ids, such as `repo:acme@123456/infra@456789`, and a policy written with the name-only form matches none of its tokens. Read the prefix with `gh api repos/acme/infra/actions/oidc/customization/sub` and copy it into every condition; see [Immutable subjects](/operations/security-hardening#immutable-subjects).

With instances nothing changes: the subject names the instance's environment. Give each environment its apply role and select it in `aws-role-arn-map` by instance, with a key such as `:production`, or by exact key, `infra/network:production`. The map key comes from names a pull request controls, so this pin is what ties each role to its gate. See [AWS roles](./instances#aws-roles).

### Pinning the reusable workflow

To pin the role further to the canonical reusable workflow at a `v1` tag, add `job_workflow_ref` to the subject with the repository's OIDC subject customization endpoint:

```sh
gh api --method PUT repos/acme/infra/actions/oidc/customization/sub \
  --input - <<'EOF'
{"use_default": false, "include_claim_keys": ["repo", "context", "job_workflow_ref"]}
EOF
```

The subject then ends with the workflow reference, and the trust policy can require it. On a repository with immutable subjects, the ids stay in the prefix and the body needs one more field; see [Pinning the reusable workflow](/operations/security-hardening#subject-workflow-ref). The statement's `Condition` becomes:

```json
{
  "Condition": {
    "StringEquals": {
      "token.actions.githubusercontent.com:aud": "sts.amazonaws.com"
    },
    "StringLike": {
      "token.actions.githubusercontent.com:sub": "repo:acme/infra:environment:production:job_workflow_ref:stackorder/actions/.github/workflows/run.yml@refs/tags/v1*"
    }
  }
}
```

The customization applies to every workflow in the repository, so update the plan role's condition too, to `repo:acme/infra:pull_request:job_workflow_ref:stackorder/actions/.github/workflows/plan.yml@refs/tags/v1*` and `repo:acme/infra:environment:default:job_workflow_ref:stackorder/actions/.github/workflows/run.yml@refs/tags/v1*`.

Now the only path to production credentials runs through the environment gate, whatever the server or the PR's workflow file says. Pair it with `STACKORDER_REQUIRED_WORKFLOW_REF` on the server, so results are accepted only from the canonical workflows. See [Server configuration](/reference/server-configuration).

## Personal accounts and GitHub Free {#free-plan}

Two things take layers away: an account with one person, which has nobody else to approve, and a plan that does not offer GitHub's protection features on private repositories.

| Feature | Public repository, any plan | Private repository, GitHub Free | Private repository, GitHub Pro or Team |
| --- | --- | --- | --- |
| Environment created on first use by a workflow | Yes | Yes, with no protection rules | Yes |
| Required reviewers, wait timer, custom deployment protection rules (layers 3 and 4) | Yes | No | No; needs GitHub Enterprise |
| Deployment branch and tag policies | Yes | No | Yes |
| Branch protection and rulesets | Yes | No | Yes |
| `environment` in the OIDC token's `sub` and `environment` claims | When the job runs under an environment | Same | Same |

The last row is what keeps layer 5 in place. GitHub puts the environment in the token of every job that declares one, protected or not, so an apply role that trusts only `repo:acme/infra:environment:production` still refuses plan jobs and jobs under any other environment.

### Settings for a single owner {#single-owner}

- **`apply.require_approvals: 0`**, the default. GitHub does not let the author approve their own pull request, and the server ignores the author's review too, so `1` refuses every `before_merge` apply of a person working alone.
- **No `apply.allowed_teams`**. A personal account has no teams, and the check refuses every apply. Without it, the server requires push permission on the repository.
- **No `apply.four_eyes`** and **no `apply.require_codeowner_review`**. Both need someone other than the author, so both refuse every apply.

```yaml
# stackorder.yaml
version: 1

environments:
  "stacks/prod/": production
  "stacks/staging/": staging

apply:
  mode: before_merge
  require_approvals: 0
```

Map every stack to a named environment all the same: that is what lets each apply role trust only its own environment.

### Which layers hold {#free-plan-layers}

| Layer | Personal account, public repository | Private repository on GitHub Free |
| --- | --- | --- |
| 1. `apply.allowed_teams` | No teams; the push permission check remains | Same, on a personal account; available on an organisation |
| 2. Code-owner approval | No second reviewer, unless collaborators review | The server check only, and only with a second reviewer; no branch protection enforces the merge |
| 3. Required reviewers | Available, with you as the only reviewer | Not available |
| 4. Custom deployment protection rule | Available | Not available |
| 5. IAM trust policy pinned to the environment | Holds | Holds |

What still protects an apply on a private repository under GitHub Free:

- **Write permission.** The server accepts `stackorder apply` only from someone with push permission, and only such a person can merge.
- **The trust policy pinned to the environment.** Only jobs that run under `production` obtain the production apply role. Without environment protection, though, any workflow that someone with write access pushes can declare `environment: production`, on any branch. Pin the role to the canonical reusable workflow with `job_workflow_ref`, as in [Pinning the reusable workflow](#layer-5), so that only jobs of `run.yml` obtain it, not a workflow written in the repository.
- **The server's own checks.** The apply job confirms its run, commit and lock with the server before it applies. The server accepts only a token from the default branch, for the repository id it knows, under the environment it assigned, from a workflow run it can tie to one of its own dispatches. Set `STACKORDER_REQUIRED_WORKFLOW_REF` so it also refuses tokens from any other workflow. See [Security hardening](/operations/security-hardening#workflow-ref).

Anyone with write access to such a repository can therefore apply. Keep the collaborator list to the people you would trust with the apply role.

## Roles at a glance {#roles}

| Role | Trusted `sub` | Permissions |
| --- | --- | --- |
| Plan | `repo:acme/infra:pull_request` and `repo:acme/infra:environment:default` | Read state, write the state lock, and the read-only permissions providers need to plan |
| Apply, per environment | `repo:acme/infra:environment:<name>` | Read and write state, and the write permissions the stacks need |

Replace `repo:acme/infra` with the repository's own [subject prefix](/operations/security-hardening#immutable-subjects).

When the jobs assume one bootstrap role and the provider assumes a role per account, the roles and their trust change; see [One bootstrap role and a provider role per account](./instances#bootstrap-roles).
