---
description: 'Stackorder''s security model: what a compromised server, PR author or workflow can and cannot do, how plan output is redacted, and how long data is kept.'
---

# Security model

No single compromise reaches infrastructure while [shared outputs](#shared-outputs) are off, as they are by default. The server has no cloud access, the runner has no server secrets, and GitHub's own controls gate the one action that changes anything. Shared outputs are the one exception an operator can choose, and with them a compromised server can reach infrastructure: they give it read access to the state objects the operator lists, and so to any secret in them and whatever those secrets reach, and they let it choose the output values that the reading stacks' plans and applies use. An ephemeral read is made again at apply time, so a reviewed plan does not show the values its apply uses.

## What each party can do if compromised {#compromise}

| Compromised | Can | Cannot |
| --- | --- | --- |
| Stackorder server | Dispatch `stackorder-run.yml` in installed repos, post checks and comments, read the contents of installed repos through the App's `Contents: Read` permission, read plan summaries and capped plan text, answer its own deployment protection rule where one is configured; with [shared outputs](#shared-outputs) on, read the whole state objects its role may read, secrets included, and answer reads of outputs with values of its choosing, which the reading stacks' plans and applies use | Write Terraform state; read any without shared outputs, or beyond the objects its role may read with them; assume any AWS role other than the state role of shared outputs; change workflow files, read repo secrets, approve pull requests, pass an environment's required reviewers |
| A PR author with write access | Trigger plans on their PR, comment `stackorder apply` if policy allows; add a stack or instance that nothing on the default branch maps, so that an instance applies under an unprotected environment of its own name and a stack without instances under `default`, which has no reviewers; only an [IAM trust policy pinned to the environment](/configuration/environments-and-authorization#instances) stops such an apply | Bypass required approvals, branch protection or GitHub environment reviewers; skip policy checks recorded on the stack |
| A modified workflow in a PR | Change what runs in the plan job on that PR | Post results the server accepts, when `STACKORDER_REQUIRED_WORKFLOW_REF` pins the reusable workflow; assume the AWS role, when the role's trust policy pins `job_workflow_ref` or the environment |
| A leaked App private key | Everything the server can, except reading state for shared outputs, which takes the server's AWS role | Everything the server cannot; rotate in the App settings and redeploy |

## Setup {#setup}

Whoever creates the GitHub App owns it. Between the first deploy and the App's creation, the server in [setup mode](/reference/server-configuration#setup-mode) is reachable by anyone who can reach its URL, so `/setup` opens only with a one-time [setup token](/reference/server-configuration#setup-token):

- The server generates the token from 32 random bytes at start, or takes `STACKORDER_SETUP_TOKEN`, and logs the setup URL with it. Only people who can read the server's log, or the access logs of a load balancer or proxy in front of it that record the query string, can open the page.
- The token is compared in constant time and traded for a signed cookie before the page loads, so it never reaches GitHub. The manifest callback converts a code only for the browser that opened the page.
- Once an App is created, the token stops working on the instance that created it, until that instance restarts. Other instances that share the token keep accepting it until they restart, so run one instance during setup.
- With `STACKORDER_ALLOW_RESETUP=true` a configured server also serves the token-gated setup, so anyone who can read its log can create a replacement App. Set it only while replacing the App.

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
| GitHub to server | Webhook payloads (HMAC-signed); from runners, JSON manifests and result summaries. From the GitHub API, the contents of `stackorder.yaml`, `.stackorder.yaml` files and `CODEOWNERS`; no other repository contents beyond file paths and parsed dependency edges. |
| Server to GitHub | App installation tokens, scoped to one installation and valid for an hour, used for `workflow_dispatch`, check runs and comments. |
| Runner to AWS | Your role, assumed with `aws-actions/configure-aws-credentials` and a trust policy pinned to the repository and environment. State, lock and plan files never leave this zone, except the state objects the server reads for [shared outputs](#shared-outputs). |
| Server to your state buckets | Only with [shared outputs](#shared-outputs) on: `s3:GetObject` on the state objects under `STACKORDER_STATE_BUCKETS`, each entry granted to one repository, with the server's own role or the role `STACKORDER_STATE_ROLE_ARN` names, assumed through STS. The server keeps the `outputs` block in memory and returns the outputs the producer shares, with the state's serial, lineage and Terraform version, to the reading job or API key; nothing else of the object leaves the server, and nothing is written back. |
| Server to anything else | Nothing, except Postgres, GitHub's OIDC signing keys, and, when enabled, the artifact bucket and the OTLP endpoint. |

## Shared outputs {#shared-outputs}

[Shared outputs](/configuration/outputs) let a stack read another stack's root-module outputs through the server, so the reading stack's role needs no access to the other stack's state. They are off by default, and whatever on this page does not name them describes a server without them. An operator turns them on with `STACKORDER_STATE_BUCKETS`, or the Terraform module's `shared_outputs_state`, and that moves read access to state from the reading stacks to the server:

- **The reading stacks lose it.** With `terraform_remote_state`, every job that assumes the reading stack's role, pull request plans included, can read the producer's whole state object. With shared outputs that role needs no access to it, and the producer names its readers on its default branch.
- **The server gains it.** Its AWS role, or the role `STACKORDER_STATE_ROLE_ARN` names, can read the whole state objects under the listed buckets and prefixes, secrets included. IAM grants whole objects: the filtering to outputs happens in the server, which keeps only the `outputs` block and stores, caches and logs nothing it reads.
- **The server cannot write state.** It only reads state objects, and neither the grant the Terraform module creates nor the one [Security hardening](/operations/security-hardening#shared-outputs) recommends allows a write or a delete. It never takes the state lock and never decrypts state that OpenTofu encrypted.
- **Each grant belongs to one repository.** An entry of `STACKORDER_STATE_BUCKETS` is `owner/repo=bucket/prefix`. The server reads a stack's state from the location that the stack's last apply or drift job reported, and both when it records that location and when it reads it, the location must lie under an entry of the stack's own repository, compared at path segment boundaries; otherwise a reported location is not recorded and clears the one recorded before, so reads answer `409 state_location_unknown`, and a read of a location recorded before the entries changed answers `403 state_bucket_not_allowed` before any request to S3, audited and logged. An entry for `*` is the operator's explicit opt-in to letting every repository use it.

What a repository's jobs can and cannot make the server read follows from that. An apply or drift job runs the repository's code, and in the default `before_merge` mode an apply runs a pull request's code, so it can report any object for its stack and use the job's OIDC token to post the result itself:

- It **cannot** make the server read an object outside its own repository's entries and the `*` entries: not another repository's state, in the same installation or another, as long as no entry of its repository, or of `*`, covers that state's location too; not a sibling of a granted prefix such as `stacks/production/` for `stacks/prod`; and not a key with a `.` or `..` segment.
- It **can** point one of its stacks at any object under its repository's entries, another stack's state included, and that stack's readers then get the other stack's outputs. Within one repository the grant, not the stack, is the boundary of what can be read; `readers` decides who reads each stack, and the apply that reports a location is a step to review like its plan. Grant a repository only the prefixes that hold the state of its stacks that share outputs.
- With an entry for `*`, every repository can point its stacks at objects under that entry.
- Entries of different repositories that overlap, where one's location lies under the other's, as with `acme/infra=acme-tfstate/stacks/` beside `acme/network=acme-tfstate/stacks/network/`, give the repository of the wider entry the other's state as well. The server logs a warning at start-up for each such pair; keep the prefixes of different repositories apart.
- IAM grants the server's role every entry at once and cannot tell repositories apart, so the per-repository check is the server's own; a compromised server is bound only by its role, as below.

If the server is compromised while shared outputs are on, the attacker can, beyond the [table above](#compromise):

- read every state object the server's credentials reach, and every secret in those objects, such as a database password or an access key a stack created, and use whatever those secrets open;
- answer any read of outputs with values of its choosing, which the reading stack's plan, and the apply of that plan, then use; an [ephemeral `stackorder_outputs`](/configuration/outputs#consumer) is read again at apply time and kept out of the saved plan, so a reviewed plan does not show the values its apply uses.

It still cannot write state while its role grants only reads, nor assume the roles your jobs use. These bound the exposure:

| Control | Effect |
| --- | --- |
| Narrow prefixes, one per repository, each ending in `/`, none under another repository's, and holding only the state of stacks that share outputs | Limits which state objects the server can read at all, and which ones each repository's stacks can be pointed at |
| A dedicated role in `STACKORDER_STATE_ROLE_ARN`, with only `s3:GetObject` on those prefixes, `s3:ListBucket` limited to them by `s3:prefix`, and `kms:Decrypt` through S3 only | Limits what the server's credentials allow, whatever the server does |
| CloudTrail data events on the state buckets | Records every read of the objects, including reads that leave no row in the server's own audit log, such as a compromised server's |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | Refuses reads by a copy of the workflow edited in a pull request |
| `apply.max_parallel: 1` | Makes a read at apply time name its reader exactly, since each dispatch then holds one stack |
| API keys kept out of CI | Keeps an administrator key, which reads the outputs of any stack whatever its `readers`, out of reach of the code a job runs |

[Security hardening](/operations/security-hardening#shared-outputs) has the policies. A runner token does not prove which stack's job calls: within one repository, a pull request's plan jobs can read whatever is shared with any stack key of the repository. [Who may read](/configuration/outputs#who-may-read) says what the server can and cannot prove about a reader.

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

The server is not in the path of `terraform plan`, except for a stack that reads [shared outputs](/configuration/outputs#consumer): its provider calls the server during the plan, so the plan fails while the server is unreachable. Otherwise a server outage degrades to plans with `unconfirmed` checks and refused applies. Postgres is the only stateful dependency; point-in-time recovery on RDS covers it. If GitHub webhooks are delayed, the reconciliation that runs every minute catches finished waves. If GitHub Actions is down, nothing runs, exactly as with any Actions-based tool.

## Abuse limits {#abuse}

- Comment commands are accepted only from users with push permission, and rate-limited to 10 per minute per PR.
- Webhook deliveries are deduplicated by delivery id.
- The API refuses OIDC tokens issued more than 10 minutes ago and tokens it has already seen, results for stacks that already finished or runs that were superseded, and tokens from a workflow run whose dispatch already completed or is bound to another workflow run.

## Human access {#human-access}

People sign in with GitHub through the App's OAuth client, with the `read:org` scope only. A session is issued only to a user whose own account, or one of whose organisations, has the App installed, and the UI shows only those accounts' repositories. API keys, created by an operator, see every repository. The UI is read-only except for unlock and re-run, which go through the API and are audited.

## Reporting a vulnerability {#reporting}

Report vulnerabilities privately, through GitHub's private vulnerability reporting, rather than in a public issue: [stackorder/stackorder](https://github.com/stackorder/stackorder/security/advisories/new) for the server, the CLI, the web UI and the Terraform module, [stackorder/actions](https://github.com/stackorder/actions/security/advisories/new) for the workflows and actions. The [security policy](https://github.com/stackorder/stackorder/blob/main/SECURITY.md) lists the supported versions, what to include, what is in scope and what happens after a report.
