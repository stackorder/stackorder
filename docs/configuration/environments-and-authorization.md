# Environments and authorization

Write access to the repository is the floor, not the ceiling. An apply passes up to five layers, and only the two that GitHub and AWS enforce are security boundaries. The server-side checks give fast, readable refusals; the environment gate and the IAM trust policy are what actually stop an unauthorised apply.

| Layer | Enforced by | What it gates | Availability |
| --- | --- | --- | --- |
| 1. `apply.allowed_teams` | The server | Who may *request* an apply | All plans; needs the App's `Members: Read` |
| 2. Code-owner approval | GitHub (merge) and the server (before-merge apply) | Who must *approve* the change | All plans |
| 3. GitHub Environment with required reviewers | GitHub | Whether the apply *job may start* | Public repos on all plans; private repos need GitHub Enterprise |
| 4. Custom deployment protection rule | GitHub, with the server's logic | Same as 3, decided automatically | Same as 3; extra App permission and event |
| 5. IAM trust policy pinned to the environment | AWS | Whether the job can obtain *credentials* at all | All plans |

## Recommended default {#recommended}

- **Layer 1** for the error message.
- **Layer 3**, with a team as required reviewer, for the hard stop.
- **Layer 5** to make it airtight.

Layer 4 is the upgrade for teams that find the manual approval redundant with code review. The plan role stays open to anyone with write access: planning is read-only, and the plan role has no write permissions.

## Layer 1: team check on the request {#layer-1}

On `stackorder apply`, the server checks the commenter's membership of each team in `apply.allowed_teams` and refuses with a comment naming the team unless the membership is `active`. Members of nested child teams count. Results are cached for 60 s to stay clear of rate limits.

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
- It needs the App's optional `Members: Read` permission.

This layer fails fast with a good message, and nothing more. A bug or a compromised server skips it.

## Layer 2: code-owner approval {#layer-2}

Map stack paths to owning teams in `CODEOWNERS`, and require code-owner review in branch protection:

```text
# .github/CODEOWNERS
/stacks/prod/    @acme/platform-prod
/stacks/staging/ @acme/platform-eng
```

- In `on_merge` mode that alone is a hard gate: apply follows merge, and GitHub will not merge without the owning team's approval.
- In `before_merge` mode, set `apply.require_codeowner_review: true`. The apply gate then requires, for each affected stack, at least one `APPROVED` review on the current head SHA from a member of the owning team. Reviews on older commits do not count.
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

The apply job in the reusable `run.yml` declares `environment: ${{ matrix.environment }}`. The server assigns each stack an environment from the `environments` prefix map in `stackorder.yaml`, overridable per stack with `environment` in `.stackorder.yaml`.

```yaml
environments:
  "stacks/prod/": production
  "stacks/staging/": staging
```

In the repository settings, under **Environments**, configure `production`:

- **Required reviewers**: a team. The job pauses until a listed user or team member approves in the Actions UI.
- **Prevent self-review**: on, so the requester cannot approve their own deployment.
- **Deployment branches**: the default branch only. Server-dispatched runs start from the default branch.

The server cannot approve: the App has no Environments permission, and an App cannot be a required reviewer. This gate holds even if the server is fully compromised.

Two consequences of the design:

- The server dispatches each wave as one run per environment it touches, so a mixed run does not hold staging behind the production reviewer. The sticky PR comment links straight to the pending approval.
- With one dispatch per wave, a three-wave production apply asks for three approvals.

## Layer 4: the App as a deployment protection rule {#layer-4}

Instead of a human clicking approve, register the App as a custom deployment protection rule on the environment. When the apply job wants to start, GitHub sends a `deployment_protection_rule` webhook. The server approves or rejects the deployment with exactly the checks from layers 1 and 2.

That turns the server's policy into something GitHub enforces: the job does not run until the App says yes, and the App says yes only for a request from the right team on a PR with the right approvals.

To enable it:

1. In the App's settings, grant the optional `Deployments: Read and write` permission and subscribe to the `deployment_protection_rule` event. Each installation must accept the new permission.
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

### Pinning the reusable workflow

To pin the role further to the canonical reusable workflow at a `v1` tag, add `job_workflow_ref` to the subject with the repository's OIDC subject customization endpoint:

```sh
gh api --method PUT repos/acme/infra/actions/oidc/customization/sub \
  --input - <<'EOF'
{"use_default": false, "include_claim_keys": ["repo", "context", "job_workflow_ref"]}
EOF
```

The subject then ends with the workflow reference, and the trust policy can require it:

```json
"Condition": {
  "StringEquals": {
    "token.actions.githubusercontent.com:aud": "sts.amazonaws.com"
  },
  "StringLike": {
    "token.actions.githubusercontent.com:sub": "repo:acme/infra:environment:production:job_workflow_ref:stackorder/actions/.github/workflows/run.yml@refs/tags/v1*"
  }
}
```

The customization applies to every workflow in the repository, so update the plan role's condition too, to `repo:acme/infra:pull_request:job_workflow_ref:stackorder/actions/.github/workflows/plan.yml@refs/tags/v1*`.

Now the only path to production credentials runs through the environment gate, whatever the server or the PR's workflow file says. Pair it with `STACKORDER_REQUIRED_WORKFLOW_REF` on the server, so results are accepted only from the canonical workflows. See [Server configuration](/reference/server-configuration).

## Roles at a glance {#roles}

| Role | Trusted `sub` | Permissions |
| --- | --- | --- |
| Plan | `repo:acme/infra:pull_request` | Read state, write the state lock, and the read-only permissions providers need to plan |
| Apply, per environment | `repo:acme/infra:environment:<name>` | Read and write state, and the write permissions the stacks need |
