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
| Server logs | JSON by default; set `STACKORDER_LOG_LEVEL` for more detail |
| `GET /readyz` and `GET /metrics` | Whether the database is reachable; queue depth, webhook lag and rate limit |

## Unconfirmed checks {#unconfirmed}

**Symptom.** Plans ran, but the checks are neutral and say `unconfirmed`. `stackorder apply` is refused.

**Why.** The CLI could not reach the server. It computed the affected set locally, planned, and set a neutral check with the job's `GITHUB_TOKEN`, so nobody mistakes the fallback for a green light. Applies are refused because the server never confirmed the plans.

**Fix.**

1. Check the server: `curl -fsS https://stackorder.example.com/readyz`.
2. Read the resolve step in the job log. It shows whether the CLI ran in local mode or got an error from the server.
3. If the CLI ran in local mode, the workflow passed an empty `server-url`: check that the `STACKORDER_SERVER_URL` Actions variable is set for the repository or its organization.
4. If the server answered `unauthorized` or `forbidden`, see [OIDC rejected](#oidc).
5. Once the cause is fixed, re-plan: push to the PR or comment `stackorder plan`.

## Apply refused {#apply-refused}

**Symptom.** A comment on the PR refuses `stackorder apply`, naming the failing layer and the reason. The CLI exits with code 3 when the server refuses.

The gate runs in order and stops at the first failure:

| Refusal | Fix |
| --- | --- |
| The commenter is not in a required team, or lacks push permission | Ask a member of every affected stack's `allowed_teams`, or apply a subset you are allowed to: `stackorder apply stacks/staging/vpc`. |
| Not enough approvals, or the PR is not mergeable | Get the approvals `apply.require_approvals` asks for; resolve conflicts. |
| No code-owner approval on the head SHA | With `require_codeowner_review`, an approval from the owning team must be on the current head commit. Re-request review after a push. |
| The requester is the PR author | With `four_eyes`, someone else must comment `stackorder apply`. |
| A stack has no plan for the head SHA | Wait for the plans of the latest push, or fix the stack whose plan failed. |
| A named check failed | Fix what the check reported. `warn` does not block; `fail` does. |
| A stack is locked by another PR | Wait for that PR to merge, or see [Locks after a closed PR](#locks-after-close). |

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
2. Release the locks. With write permission, comment on the PR, use the UI, or run the CLI:

   ```sh
   stackorder unlock stacks/prod/vpc stacks/prod/apps --reason "PR 41 closed; reverting in PR 43"
   ```

3. Apply the reconciling PR. Until then, a drift check reports the stacks as drifted.

## Runner died mid-apply {#force-state}

**Symptom.** A stack is `unknown` in the run. Its orchestration lock is held, and the S3 state lock may be too.

**Why.** The job vanished before reporting a result, so nobody knows how far `apply` got. Stackorder leaves the S3 lock as is; it never touches state locks.

**Fix.**

1. Read what the job log shows of the apply.
2. Check the state lock. With `use_lockfile`, a `<key>.tflock` object sits next to the state in the bucket; with DynamoDB, there is a lock item. If the lock is stale, release it from the stack directory with credentials that can write it:

   ```sh
   tofu force-unlock <LOCK_ID>
   ```

   The lock id is in the error any later `plan` prints while the lock is held.

3. Release the orchestration lock. A stack in `unknown` needs `--force-state`:

   ```sh
   stackorder unlock stacks/prod/vpc --force-state --reason "runner lost during apply"
   ```

4. Re-plan the PR, push or comment `stackorder plan`, and apply again to converge.

## Lost webhooks {#lost-webhooks}

**Symptom.** A wave's jobs finished but the next wave did not start straight away, or a check stayed pending.

**Why.** GitHub delayed or dropped a webhook. The server does not depend on it: it polls `workflow_run` state for in-flight runs every 60 s, and the CLI's result call is the source of truth. A run moves on within about a minute even with no webhooks at all.

**Fix.** Usually none. If a comment command got no reaction, open the App's delivery log on GitHub. A delivery that failed can be redelivered; the server deduplicates by delivery id, so an event it already processed is not processed twice. Deliveries that fail the signature check point at a `GITHUB_WEBHOOK_SECRET` that does not match the App's.

## Command ignored {#command-ignored}

The App adds an eyes reaction when it receives a command, and a rocket when it dispatches.

| What you see | Cause |
| --- | --- |
| No reaction | The webhook did not arrive (see above); the commenter has no write access; more than 10 commands in a minute on this PR; or the line does not start with `stackorder` |
| Eyes, no rocket | The gate refused; look for the refusal comment |
| Rocket, nothing in Actions | Check that `.github/workflows/stackorder-run.yml` exists on the default branch with the inputs `run_id`, `mode`, `wave`, `sha` and `stacks` |

The first token must be exactly `stackorder`, case insensitive, at the start of a line.

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

| Cause | Fix |
| --- | --- |
| The job has no `id-token: write` | Grant it on the job that calls the reusable workflow. A called workflow cannot widen its caller's permissions. |
| Wrong audience | The CLI requests the server's base URL as audience; it must equal `STACKORDER_OIDC_AUDIENCE`, which defaults to `STACKORDER_BASE_URL`. |
| `job_workflow_ref` does not match | With `STACKORDER_REQUIRED_WORKFLOW_REF` set, only the pinned reusable workflows can post results. |
| Wrong environment | A dispatched job must run under the environment the server assigned to the stack. |
| Token too old, or reused | Tokens older than 10 minutes and tokens already seen are refused. |
| Re-running a finished run | The server refuses tokens whose `run_id` belongs to a run it has seen complete. Start a new run: push, or comment `stackorder plan`. |
| Fork pull request | Forks get no `id-token` permission. Stackorder posts a neutral check and runs nothing. |

## Server start-up {#server-start}

| Symptom | Cause |
| --- | --- |
| The server exits at start | `DATABASE_URL` is missing, or the database cannot be migrated. |
| Only `/setup`, `/healthz` and `/readyz` respond | Setup mode: the GitHub App variables are not set. |
| `/healthz` passes, `/readyz` fails | The database is unreachable. |
| Everyone was signed out | `STACKORDER_SESSION_KEY` changed, or differs between instances. |
