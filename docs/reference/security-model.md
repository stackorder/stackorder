---
description: 'Stackorder''s security model: what a compromised server, PR author or workflow can and cannot do, how plan output is redacted, and how long data is kept.'
---

# Security model

No single compromise reaches infrastructure. The server has no cloud access, the runner has no server secrets, and GitHub's own controls gate the one action that changes anything.

## What each party can do if compromised {#compromise}

| Compromised | Can | Cannot |
| --- | --- | --- |
| Stackorder server | Dispatch `stackorder-run.yml` in installed repos, post checks and comments, read `stackorder.yaml`, read plan summaries and capped plan text, answer its own deployment protection rule where one is configured | Read or write Terraform state, assume any AWS role, change workflow files, read repo secrets, approve pull requests, pass an environment's required reviewers |
| A PR author with write access | Trigger plans on their PR, comment `stackorder apply` if policy allows; add a stack or instance that nothing on the default branch maps, so that an instance applies under an unprotected environment of its own name and a stack without instances under `default`, which has no reviewers; only an [IAM trust policy pinned to the environment](/configuration/environments-and-authorization#instances) stops such an apply | Bypass required approvals, branch protection or GitHub environment reviewers; skip policy checks recorded on the stack |
| A modified workflow in a PR | Change what runs in the plan job on that PR | Post results the server accepts, when `STACKORDER_REQUIRED_WORKFLOW_REF` pins the reusable workflow; assume the AWS role, when the role's trust policy pins `job_workflow_ref` or the environment |
| A leaked App private key | Everything the server can | Everything the server cannot; rotate in the App settings and redeploy |

## Setup {#setup}

Whoever creates the GitHub App owns it. Between the first deploy and the App's creation, the server in [setup mode](/reference/server-configuration#setup-mode) is reachable by anyone who can reach its URL, so `/setup` opens only with a one-time [setup token](/reference/server-configuration#setup-token):

- The server generates the token from 32 random bytes at start, or takes `STACKORDER_SETUP_TOKEN`, and logs the setup URL with it. Only people who can read the server's log can open the page.
- The token is compared in constant time and traded for a signed cookie before the page loads, so it never reaches GitHub. The manifest callback converts a code only for the browser that opened the page.
- Once an App is created, the token stops working until the server restarts.

## Controls that belong to GitHub {#github-controls}

Stackorder reports; GitHub and AWS enforce.

- **Branch protection** requires the `stackorder/plan` check, the `stackorder/apply` check in `before_merge` mode, and the configured approvals. GitHub enforces the merge.
- **GitHub Environments** with required reviewers on the apply job add a human gate the server cannot skip, because the server cannot approve deployments.
- **The AWS role trust policy** restricts `sub` to `repo:org/repo:environment:production` for an apply role, and optionally to the `job_workflow_ref` of the canonical reusable workflow, so only the gated job in that repository can obtain credentials. The read-only plan role trusts `repo:org/repo:pull_request` and `repo:org/repo:environment:default`. On a repository with [immutable subjects](/operations/security-hardening#immutable-subjects), `repo:org/repo` carries the owner and repository ids. See [Security hardening](/operations/security-hardening#trust-policies).
- **Runner OIDC tokens** are short-lived and bound to one run. There is nothing to rotate on the runner side.

The five layers that gate an apply, and which of them are real security boundaries, are on [Environments and authorization](/configuration/environments-and-authorization).

## Trust between runner and server {#runner-trust}

There are no shared secrets between the runner and the server. The CLI sends the job's GitHub OIDC token, requested with the server's base URL as audience. The server verifies its signature against GitHub's keys and binds it to the run with its claims: repository and repository id, event and ref, workflow run, environment and, optionally, `job_workflow_ref`. It never compares the token's `sha` claim, which is GitHub's merge or dispatch commit rather than the commit being planned; it checks the pull request head through the GitHub API instead. See [OIDC binding](/reference/api#oidc-binding).

Rotating anything means rotating the App's private key, which runners never see.

## What crosses each boundary {#boundaries}

| Boundary | What crosses it |
| --- | --- |
| GitHub to server | Webhook payloads (HMAC-signed); from runners, JSON manifests and result summaries. No repository contents beyond file paths and parsed dependency edges. |
| Server to GitHub | App installation tokens, scoped to one installation and valid for an hour, used for `workflow_dispatch`, check runs and comments. |
| Runner to AWS | Your role, assumed with `aws-actions/configure-aws-credentials` and a trust policy pinned to the repository and environment. State, lock and plan files never leave this zone. |
| Server to anything else | Nothing, except Postgres, GitHub's OIDC signing keys, and, when enabled, the artifact bucket and the OTLP endpoint. |

## Secrets in plan output {#plan-secrets}

- Terraform and OpenTofu already mask values marked `sensitive`.
- The CLI redacts everything it sends to the server, the step summary or a fallback check: private keys, tokens, password assignments and the values of secret-named environment variables. In Actions it also registers those values with `::add-mask::`. See [CLI secrets](/reference/cli#secrets).
- Plan text sent to the server is truncated at 256 KB.
- The API returns that redacted text, from Postgres or from the [artifact bucket](/reference/server-configuration#artifact-bucket), only to people who can see the repository, to API keys and to the jobs of the run itself.
- `plan_output: summary`, per repository or per stack, sends only resource counts and addresses to the server and the PR comment.

The full plan lives only in the job log and the plan artifact, both governed by the repository's own access rules and artifact retention.

## Data retention {#retention}

Plan text is kept 30 days by default, summaries and run history indefinitely, queue events 7 days and drift history 90 days. The periods are set with `STACKORDER_PLAN_TEXT_RETENTION`, `STACKORDER_EVENT_RETENTION` and `STACKORDER_DRIFT_RETENTION`. See [Data model](/reference/data-model#retention).

## Availability {#availability}

The server is not in the path of `terraform plan`. A server outage degrades to plans with `unconfirmed` checks and refused applies. Postgres is the only stateful dependency; point-in-time recovery on RDS covers it. If GitHub webhooks are delayed, the reconciliation that runs every minute catches finished waves. If GitHub Actions is down, nothing runs, exactly as with any Actions-based tool.

## Abuse limits {#abuse}

- Comment commands are accepted only from users with push permission, and rate-limited to 10 per minute per PR.
- Webhook deliveries are deduplicated by delivery id.
- The API refuses OIDC tokens issued more than 10 minutes ago and tokens it has already seen, results for stacks that already finished or runs that were superseded, and tokens from a workflow run whose dispatch already completed or is bound to another workflow run.

## Human access {#human-access}

People sign in with GitHub through the App's OAuth client, with the `read:org` scope only. A session is issued only to a user whose own account, or one of whose organisations, has the App installed, and the UI shows only those accounts' repositories. API keys, created by an operator, see every repository. The UI is read-only except for unlock and re-run, which go through the API and are audited.

## Reporting a vulnerability {#reporting}

Report vulnerabilities privately, through GitHub's private vulnerability reporting, rather than in a public issue: [stackorder/stackorder](https://github.com/stackorder/stackorder/security/advisories/new) for the server, the CLI, the web UI and the Terraform module, [stackorder/actions](https://github.com/stackorder/actions/security/advisories/new) for the workflows and actions. The [security policy](https://github.com/stackorder/stackorder/blob/main/SECURITY.md) lists the supported versions, what to include, what is in scope and what happens after a report.
