# Troubleshooting

Stackorder degrades gracefully but never silently. Every refusal is a PR comment or a check with the reason, and every fallback is marked `unconfirmed`. Start from what you see.

| You see | Go to |
| --- | --- |
| A neutral `unconfirmed` check | [Unconfirmed checks](#unconfirmed) |
| A comment refusing `stackorder apply` | [Apply refused](#apply-refused) |
| `stackorder/apply` red, stacks `blocked` | [Apply failed in a wave](#apply-failed) |
| Applies refused with `locked` after a PR was closed | [Locks after a closed PR](#locks-after-close) |
| A stack stuck in `unknown` | [Runner died mid-apply](#force-state) |
| A wave finished but nothing happened next | [Lost webhooks](#lost-webhooks) |
| No reaction to a comment command | [Command ignored](#command-ignored) |
| `stackorder/resolve` failed | [Dependency cycle](#cycle) |
| API errors `unauthorized` or `forbidden` in a job log | [OIDC rejected](#oidc) |
| The server serves only `/setup` | [Server start-up](#server-start) |

## Where to look {#where-to-look}

| Source | What it tells you |
| --- | --- |
| The check run output and the sticky PR comment | What the server decided, and why |
| The Actions job log | What the CLI did, and the error it got from the server or from Terraform |
| The web UI run page | Waves, per-stack status, and links to job logs |
| The App's delivery log on GitHub | Whether GitHub delivered a webhook, and the server's response |
| Server logs | JSON by default; set `STACKORDER_LOG_LEVEL=debug` for more detail. Every request logs its `request_id`, which the response carries as `X-Request-Id` |
| The run's `warnings` in `GET /v1/runs/{id}` | Dispatches GitHub refused, external dependents that were not scheduled |
| `GET /v1/audit` | Commands ignored or rate limited, unlocks, `config_invalid` for a default-branch `stackorder.yaml` the server could not parse |
| `GET /readyz` and `GET /metrics` | Whether the database is reachable; queue depth, webhook lag and rate limit. `/metrics` needs `Authorization: Bearer <STACKORDER_METRICS_TOKEN>` when the token is set, which the Terraform module always does |

## Unconfirmed checks {#unconfirmed}

**Symptom.** Plans ran, but the checks are neutral and say `unconfirmed`. `stackorder apply` is refused.

**Why.** The CLI could not reach the server: a network error, a timeout or a 5xx answer, three retries later, or an empty `server-url`. It computed the affected set locally, planned, and set a neutral check with the job's `GITHUB_TOKEN`, so nobody mistakes the fallback for a green light. Applies are refused because the server never confirmed the plans.

**Fix.**

1. Check the server: `curl -fsS https://stackorder.example.com/readyz`.
2. Read the resolve step in the job log. It shows whether the CLI ran in local mode or got an error from the server.
3. If the CLI ran in local mode, the workflow passed an empty `server-url`: check that the `STACKORDER_SERVER_URL` Actions variable is set for the repository or its organization.
4. A server that answers `unauthorized` or `forbidden` does not produce an unconfirmed check: the job fails. See [OIDC rejected](#oidc).
5. Once the cause is fixed, re-plan: push to the PR or comment `stackorder plan`.

## Apply refused {#apply-refused}

**Symptom.** A comment on the PR refuses `stackorder apply`, naming the failing layer and the reason. The CLI exits with code 3 when the server refuses.

The gate checks every layer and reports all failures in one comment:

| Refusal | Fix |
| --- | --- |
| The commenter is not in a required team, or lacks push permission | Ask a member of every affected stack's `allowed_teams`, or apply a subset you are allowed to: `stackorder apply stacks/staging/vpc`. |
| Not enough approvals, or the PR is not mergeable | Get the approvals `apply.require_approvals` asks for, on the head commit, from users with push permission; resolve conflicts. |
| No code-owner approval on the head SHA | With `require_codeowner_review`, an approval from the owning team must be on the current head commit. Re-request review after a push. |
| The requester is the PR author | With `four_eyes`, someone else must comment `stackorder apply`. |
| A stack has no plan for the head SHA | Wait for the plans of the latest push, or fix the stack whose plan failed. |
| A named check failed | Fix what the check reported. `warn` does not block; `fail` does. |
| A stack is locked by another PR | Wait for that PR to merge, or see [Locks after a closed PR](#locks-after-close). |
| Another apply of this PR is in flight | Wait for it to finish. An apply the server recorded but never dispatched is dispatched by the server within about three minutes, or, when its plans are no longer the head's, failed with its locks released and a comment. The locks stay held when an earlier apply of the PR changed what is deployed. |
| `apply.mode` is `on_merge` | Merge the PR; the merge starts the apply. |

Gate policy comes from `stackorder.yaml` on the default branch. Changing it in the PR does not change the gate for that PR.

## Apply failed in a wave {#apply-failed}

**Symptom.** One stack's apply job failed. `stackorder/apply` is red; its dependents are `blocked`; unrelated stacks in the same wave finished.

**Why.** A failed stack blocks every transitive dependent, and the run ends after the current wave. The PR keeps its locks, so nobody else can apply those stacks meanwhile.

**Fix.** Read the failed job's log, fix the code and push. The push invalidates the old plans and re-plans. Comment `stackorder apply` again; stacks that already applied plan as no-ops or small diffs, and the waves run from the start.

## Locks after a closed PR {#locks-after-close}

**Symptom.** A PR that applied was closed without merging. It got a warning comment, and other PRs touching the same stacks are refused with `locked`.

**Why.** After an apply, the deployed infrastructure matches the closed PR, not the default branch. The locks stay so nobody builds on a mismatch without noticing.

**Fix.**

1. Decide how to bring the default branch and the deployment back in line: reopen and merge the PR, or open a new PR that changes the stacks back to what the default branch should deploy.
2. Release the locks. With push permission, comment `stackorder unlock` on the closed PR, which releases its locks, or use the UI. With an [API key](/reference/server-configuration#api-keys), run the CLI:

   ```sh
   export STACKORDER_SERVER_URL=https://stackorder.example.com STACKORDER_API_KEY=sk_...
   stackorder unlock stacks/prod/vpc stacks/prod/apps --reason "PR 41 closed; reverting in PR 43"
   ```

3. Apply the reconciling PR. Until then, a drift check reports the stacks as drifted.

## Runner died mid-apply {#force-state}

**Symptom.** A stack is `unknown` in the run. Its orchestration lock is held, and the S3 state lock may be too.

**Why.** The job vanished before reporting a result: its workflow run completed without the stack reporting, or no workflow run picked the dispatch up within 30 minutes. If the stacks became `unknown` while the apply was still waiting for an environment's reviewers, with the warning `workflow run not found`, the wrapper lacks the [`run-name` line](/configuration/workflows#run) and nothing was applied. Otherwise nobody knows how far `apply` got. Stackorder leaves the S3 lock as is; it never touches state locks. A result that arrives late still lands on an `unknown` stack.

**Fix.**

1. Read what the job log shows of the apply.
2. Check the state lock. With `use_lockfile`, a `<key>.tflock` object sits next to the state in the bucket; with DynamoDB, there is a lock item. If the lock is stale, release it from the stack directory with credentials that can write it:

   ```sh
   tofu force-unlock <LOCK_ID>
   ```

   The lock id is in the error any later `plan` prints while the lock is held.

3. Release the orchestration lock. `--force-state` releases it like any unlock, and records in the audit log and in the PR comment that a runner died mid-apply and the state lock needs checking:

   ```sh
   stackorder unlock stacks/prod/vpc --force-state --reason "runner lost during apply"
   ```

4. Re-plan the PR, push or comment `stackorder plan`, and apply again to converge.

## Lost webhooks {#lost-webhooks}

**Symptom.** A wave's jobs finished but the next wave did not start straight away, or a check stayed pending.

**Why.** GitHub delayed or dropped a webhook. The server does not depend on it: every minute it reconciles open dispatches with GitHub, resending dispatches GitHub never accepted and binding workflow runs by their `run-name`, and the CLI's result call is the source of truth. A run moves on within about a minute even with no webhooks at all.

**Fix.** Usually none. If a comment command got no reaction, open the App's delivery log on GitHub. A delivery that failed can be redelivered; the server deduplicates by delivery id, so an event it already processed is not processed twice. Deliveries that fail the signature check point at a `GITHUB_WEBHOOK_SECRET` that does not match the App's.

## Command ignored {#command-ignored}

The App adds an eyes reaction when it receives a command, and a rocket when it dispatches.

| What you see | Cause |
| --- | --- |
| No reaction | The webhook did not arrive (see above); the commenter has no push permission (audited as `command_ignored`); the command is inside a code block or a quote; or the first two words of the line are not `stackorder` and a known verb |
| A comment asking to wait | More than 10 commands in a minute on this PR |
| Eyes, no rocket | The command was refused, or needed no dispatch (`help`, `unlock`); look for the reply comment |
| Rocket, nothing in Actions | Check that `.github/workflows/stackorder-run.yml` exists on the default branch and declares the inputs `run_id`, `mode`, `wave`, `sha` and `stacks`. A dispatch GitHub refuses is recorded as a warning on the run. |

The command is the first line whose first word is `stackorder` and second word a known verb, both case insensitive.

## Dependency cycle {#cycle}

**Symptom.** `stackorder/resolve` failed, and its output spells out the stacks that form a cycle.

**Fix.** Remove one `depends_on` entry in the cycle. If an inferred `reads_state` edge closes the cycle, suppress it with `ignore_inferred` in the reading stack's `.stackorder.yaml`. Check locally with `stackorder graph`.

## Plans invalidated by a push {#head-sha}

**Symptom.** `stackorder apply` is refused right after a push.

**Why.** Plans belong to a head SHA. A new push invalidates them, supersedes the run, and re-plans.

**Fix.** Wait for the new plans, then comment `stackorder apply` again.

## Expired plan artifacts {#artifact-expired}

**Symptom.** An apply re-planned instead of using the saved plan, then refused.

**Why.** With `apply.from_plan: true`, the CLI applies the saved plan file. When the artifact has expired it re-plans, and refuses unless the new plan touches the same resource addresses as the recorded one.

**Fix.** Re-plan the PR (push or comment `stackorder plan`), review the new plan, and apply again. The reusable `plan.yml` keeps plan artifacts for 5 days, so an apply requested later than that always meets an expired artifact; the repository's artifact retention setting cannot extend it.

## OIDC rejected {#oidc}

**Symptom.** A job log shows the server answering `unauthorized` or `forbidden`.

| Cause | Answer | Fix |
| --- | --- | --- |
| The job has no `id-token: write` | The CLI cannot get a token | Grant it on the job that calls the reusable workflow. A called workflow cannot widen its caller's permissions. |
| Wrong audience | `401` | The CLI requests the `server-url` it was given as audience, exactly as written; it must equal `STACKORDER_OIDC_AUDIENCE`, which defaults to `STACKORDER_BASE_URL` without a trailing slash. |
| Token too old, or reused | `401` | Tokens issued more than 10 minutes ago and tokens already seen are refused. The CLI requests a new one for every call, so this points at a clock problem or a replay. |
| Repository unknown or suspended | `403` | Install the App on the repository, or unsuspend the installation. The server learns installations from webhooks, at start-up and daily. |
| `job_workflow_ref` does not match | `403` | With `STACKORDER_REQUIRED_WORKFLOW_REF` set, only the pinned reusable workflows can register runs and post results. |
| Wrong environment | `403` | A dispatched apply job must run under the stack's environment, and a plan or drift job under `default`. |
| Re-running a completed workflow run | `403` | A re-run of a workflow run whose dispatch already completed is refused. Start a new run: push, comment `stackorder plan` or `stackorder apply`. |
| A superseded run | `409 superseded` | A newer commit replaced the run; its jobs' results are refused. |
| A finished plan run | `409 conflict` | Once a pull request's plan run is `planned` or `failed`, its `pull_request` jobs can no longer post results or check verdicts. Post verdicts from a `post-plan.sh` hook, before the result. "Re-run failed jobs" reports to the finished run; re-run all jobs, push or comment `stackorder plan` to plan again. |
| Fork pull request | | Forks get no `id-token` permission. Stackorder posts a neutral check and runs nothing. |

## Server start-up {#server-start}

| Symptom | Cause |
| --- | --- |
| The server exits at start | The log lists every invalid variable: `DATABASE_URL` or `STACKORDER_BASE_URL` missing, only some of the three `GITHUB_APP_*` variables set, a malformed private key, `STACKORDER_ARTIFACT_PREFIX` without a bucket, a bad duration or pattern. Or the database cannot be reached or migrated. |
| Only `/setup`, `/healthz` and `/readyz` respond | Setup mode: the GitHub App variables are not set. |
| `/healthz` passes, `/readyz` fails | The database is unreachable. |
| Everyone was signed out | `STACKORDER_SESSION_KEY` changed, or differs between instances. |
