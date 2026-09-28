# Security model

No single compromise reaches infrastructure. The server has no cloud access, the runner has no server secrets, and GitHub's own controls gate the one action that changes anything.

## What each party can do if compromised {#compromise}

| Compromised | Can | Cannot |
| --- | --- | --- |
| Stackorder server | Dispatch `stackorder-run.yml` in installed repos, post checks and comments, read `stackorder.yaml`, read plan summaries and capped plan text | Read or write Terraform state, assume any AWS role, change workflow files, read repo secrets, approve PRs or environment deployments |
| A PR author with write access | Trigger plans on their PR, comment `stackorder apply` if policy allows | Bypass required approvals, branch protection or GitHub environment reviewers; skip policy checks recorded on the stack |
| A modified workflow in a PR | Change what runs in the plan job on that PR | Post results the server accepts, when `STACKORDER_REQUIRED_WORKFLOW_REF` pins the reusable workflow; assume the AWS role, when the role's trust policy pins `job_workflow_ref` or the environment |
| A leaked App private key | Everything the server can | Everything the server cannot; rotate in the App settings and redeploy |

## Controls that belong to GitHub {#github-controls}

Stackorder reports; GitHub and AWS enforce.

- **Branch protection** requires the `stackorder/plan` check, the `stackorder/apply` check in `before_merge` mode, and the configured approvals. GitHub enforces the merge.
- **GitHub Environments** with required reviewers on the apply job add a human gate the server cannot skip, because the server cannot approve deployments.
- **The AWS role trust policy** restricts `sub` to `repo:org/repo:environment:prod`, or to the `job_workflow_ref` of the canonical reusable workflow, so only that workflow in that repository can obtain credentials.
- **Runner OIDC tokens** are short-lived and bound to one run. There is nothing to rotate on the runner side.

The five layers that gate an apply, and which of them are real security boundaries, are on [Environments and authorization](/configuration/environments-and-authorization).

## Trust between runner and server {#runner-trust}

There are no shared secrets between the runner and the server. The CLI sends the job's GitHub OIDC token, requested with the server's base URL as audience. The server verifies its signature against GitHub's keys and binds it to the run with its claims: repository, SHA, workflow run, event, environment and, optionally, `job_workflow_ref`. See [OIDC binding](/reference/api#oidc-binding).

Rotating anything means rotating the App's private key, which runners never see.

## What crosses each boundary {#boundaries}

| Boundary | What crosses it |
| --- | --- |
| GitHub to server | Webhook payloads (HMAC-signed); from runners, JSON manifests and result summaries. No repository contents beyond file paths and parsed dependency edges. |
| Server to GitHub | App installation tokens, scoped to one installation and valid for an hour, used for `workflow_dispatch`, check runs and comments. |
| Runner to AWS | Your role, assumed with `aws-actions/configure-aws-credentials` and a trust policy pinned to the repository and environment. State, lock and plan files never leave this zone. |
| Server to anything else | Nothing, except Postgres and the optional artifact bucket. |

## Secrets in plan output {#plan-secrets}

- Terraform and OpenTofu already mask values marked `sensitive`.
- The CLI also masks anything matching the runner's known secret patterns with `::add-mask::`.
- Plan text sent to the server is truncated at 256 KB.
- `plan_output: summary`, per repository or per stack, sends only resource counts and addresses to the server and the PR comment.

The full plan lives only in the job log and the plan artifact, both governed by the repository's own access rules and artifact retention.

## Data retention {#retention}

Plan text is kept 30 days by default, summaries and run history indefinitely, queue events 7 days and drift history 90 days. The periods are set with `STACKORDER_PLAN_TEXT_RETENTION`, `STACKORDER_EVENT_RETENTION` and `STACKORDER_DRIFT_RETENTION`. See [Data model](/reference/data-model#retention).

## Availability {#availability}

The server is not in the path of `terraform plan`. A server outage degrades to plans with `unconfirmed` checks and refused applies. Postgres is the only stateful dependency; point-in-time recovery on RDS covers it. If GitHub webhooks are delayed, the 60-second `workflow_run` reconciliation catches finished waves. If GitHub Actions is down, nothing runs, exactly as with any Actions-based tool.

## Abuse limits {#abuse}

- Comment commands are rate-limited to 10 per minute per PR, and ignored from users without write access.
- Webhook deliveries are deduplicated by delivery id.
- The API refuses OIDC tokens issued more than 10 minutes ago, tokens it has already seen, and any token whose `run_id` belongs to a run it has already seen complete.

## Human access {#human-access}

People sign in with GitHub through the App's user-authorization flow, with `login` and `read:org` scope only. A session is issued only to a member of an org where the App is installed, and the UI shows only that org's repositories. The UI is read-only except for unlock and re-run, which go through the API and are audited.
