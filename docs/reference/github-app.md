---
description: 'The Stackorder GitHub App: the permissions and events its manifest requests and why, what it cannot do, the install flow, comment commands and checks.'
---

# GitHub App

The App is the server's only identity towards GitHub and the only way GitHub reaches the server. The server creates it from a manifest at [`/setup`](#install-flow), so the permissions and events below are set for you.

## Permissions {#permissions}

The manifest the server generates, in `internal/gh/manifest.go`, requests these permissions:

| Permission | Level | Why | Needed when |
| --- | --- | --- | --- |
| Metadata | Read | Required for any App | Always |
| Contents | Read | Read `stackorder.yaml`, `.stackorder.yaml` files and `CODEOWNERS` at the default branch, and branch heads | Always |
| Pull requests | Write | The sticky comment, refusal comments, reactions to commands, reviews, mergeability and head commits | Always |
| Checks | Write | One check run per stack per phase, the roll-ups, named checks | Always |
| Actions | Write | `workflow_dispatch` of `stackorder-run.yml`; reading workflow runs and jobs to reconcile | Always |
| Issues | Write | Drift issues | `drift.open_issue: true` |
| Members | Read | Team membership | `apply.allowed_teams`, or `CODEOWNERS` teams with `apply.require_codeowner_review` |
| Deployments | Read and write | Answering `deployment_protection_rule` requests | The App is a [custom deployment protection rule](/configuration/environments-and-authorization#layer-4) |

The last three are needed only for the features in the right column, but the manifest requests them so that turning a feature on needs no change to the App. Remove them in the App's settings if you will never use them. On GitHub Enterprise Server the manifest leaves out Deployments, since not every release offers custom deployment protection rules.

### What the App cannot do {#not-requested}

The App has no `Secrets`, `Administration`, `Environments` or `Workflows` permission. It cannot change workflow files, secrets, environments or protection rules, so a compromised server cannot widen its own access.

An App also cannot be a required reviewer on an environment. Even the Deployments permission never lets the server approve a human gate; it only lets the server answer for its own protection rule.

## Events {#events}

The manifest subscribes to these events. GitHub delivers `installation` and `installation_repositories` to every App without a subscription.

| Event | Used for |
| --- | --- |
| `installation`, `installation_repositories` | Tracking installations, suspensions and the repositories they cover |
| `pull_request` | Superseding plans on a new head; on merge, recording the default-branch graph, releasing locks (`before_merge`) or starting the apply (`on_merge`); on close without merge, warning about held locks |
| `pull_request_review` | Subscribed so approvals reach the server; the gate reads reviews from GitHub when it runs |
| `issue_comment` | [Comment commands](#commands) |
| `push` | Refreshing `stackorder.yaml` and the default branch name on default-branch pushes; semver tags on module repositories |
| `check_run` | Re-runs requested from the checks UI (`rerequested`) |
| `check_suite` | Check suite re-requests |
| `workflow_run` | Binding dispatches to workflow runs and closing each wave |
| `workflow_job` | Recording a stack's job and moving the stack to `planning` or `applying` when the job starts. The server finds the stack in the job's name: `plan <key>` and `<mode> wave <n> <key>`, as the reusable workflows name their jobs, or GitHub's default matrix form, `<job> (<stack>, …)` |
| `deployment_protection_rule` | Approving or rejecting deployments as a custom protection rule; not on Enterprise Server |

Every minute the server also reconciles dispatches whose webhooks were lost: it resends dispatches GitHub never accepted, binds dispatches to workflow runs by their title, marks the stacks of a dispatch unbound for 30 minutes `unknown`, and closes dispatches whose workflow run completed without every stack reporting. The CLI's result call stays the source of truth.

## Install flow {#install-flow}

1. Start the server with `STACKORDER_BASE_URL` set and no App variables. It comes up in [setup mode](/reference/server-configuration#setup-mode) and logs a `setup_url` line: `https://<server>/setup?token=<token>`, with a one-time [setup token](/reference/server-configuration#setup-token).
2. Open that URL. The server trades the token for a cookie and redirects to `/setup`. Add `&org=<organisation>` to the URL to create the App in an organisation. `&name=` chooses its name, up to 34 characters; the default is `stackorder-<host>`.
3. The page posts the manifest to GitHub. Confirm the App there.
4. GitHub redirects to `/setup/callback`, which exchanges the one-time code for the App id, private key, webhook secret and OAuth client id and secret, and prints them **once**, as environment variables, with the next steps and a link to install the App.
5. Store the values in your secret store, restart the server with them, and install the App on your repositories. GitHub then sends the browser to `/setup/installed`.

Installations made while the server was in setup mode are learned at start-up: the server syncs the App's installations and repositories when it starts and daily at 04:00 UTC. The sync also forgets installations and repositories GitHub no longer lists, as a lost uninstall or removal webhook would have, but only after every listing succeeded.

The manifest sets the webhook URL to `<base URL>/webhooks/github`, the setup redirect to `/setup/callback`, the sign-in callback to `/auth/callback` and the post-installation page to `/setup/installed`. The App is private. On GitHub Enterprise Server set `GITHUB_API_URL` and `GITHUB_WEB_URL` before opening `/setup`; the flow is the same.

Without the token, `/setup` answers `403`, so nobody else who reaches the server between the deploy and the setup can create the App under their own account. The token stops working on the instance that created an App, and a generated one changes at every start.

Opening `/setup` on a server that already has App credentials shows a page saying so. Only with [`STACKORDER_ALLOW_RESETUP=true`](/reference/server-configuration#setup-mode) does it link to creating another App anyway (`/setup?force=1`), for instance after moving the server to a new URL, which needs the setup token too; without it `/setup?force=1` and `/setup/callback` answer `404`.

## Token handling {#tokens}

- The App's private key signs a 10-minute JWT.
- The server exchanges the JWT for installation tokens, valid for one hour, cached per installation until 5 minutes before expiry.
- Every call to GitHub uses the token of the installation it concerns, so a repository in one organisation can never be touched with another organisation's token.
- Failed calls are retried, and `Retry-After` and rate-limit resets are respected.

Human sign-in uses the App's OAuth client with the `read:org` scope. A session is issued only to a user whose own account or one of whose organisations has the App installed, and the UI shows only those accounts' repositories.

Rotating anything means rotating the App's private key, which runners never see. Generate a new key in the App settings, update `GITHUB_APP_PRIVATE_KEY`, redeploy, then delete the old key.

## Comment commands {#commands}

Commands are pull request comments. The first line whose first two words are `stackorder` and a known verb, both case insensitive, is the command; lines in code blocks, indented by four spaces or quoted with `>` are ignored. Stack keys follow the verb, separated by spaces or commas, with or without backticks. `stackorder` followed by an unknown word gets the help text.

| Command | Effect |
| --- | --- |
| `stackorder plan [key…]` | Re-plans the affected stacks of the head commit, or only the named ones, in a new run dispatched to `stackorder-run.yml` with `mode: plan`. |
| `stackorder apply [key…]` | Applies the planned stacks of the head commit through the [apply gate](/guide/how-it-works#apply-gate), or only the named subset; dependency waves are still honoured within the subset. Refused in `on_merge` repositories. |
| `stackorder unlock [key…]` | Releases the orchestration locks this pull request holds, all of them or the named ones. |
| `stackorder help` | Replies with the list of commands. |

```text
stackorder apply stacks/prod/vpc stacks/prod/apps
```

- Commands are accepted only from users with push permission on the repository; others are ignored, and audited as `command_ignored`.
- Each pull request may send 10 commands a minute, counted from the audit log. The first command over the limit gets a comment asking to wait.
- The App adds an eyes reaction when it accepts a command and a rocket when it dispatches, so a dropped command is visible. Every refusal is a comment with the reason.
- A comment runs at most once, however often GitHub delivers it. A command that fails part way is answered with a comment asking for it to be posted again.

## Checks and the sticky comment {#checks}

| Check run | Meaning |
| --- | --- |
| `stackorder/resolve` | The graph was resolved; fails on a dependency cycle, with the cycle spelled out |
| `stackorder/plan: <key>` | The plan for one stack |
| `stackorder/plan` | Roll-up of every stack's plan for the head commit |
| `stackorder/apply: <key>` | The apply for one stack |
| `stackorder/apply` | Roll-up of the apply; green when the last wave is green, and green for a `before_merge` pull request that affects nothing |
| `stackorder/<check-name>: <key>` | A named policy or cost check reported with `stackorder check`: `pass` succeeds, `warn` is neutral, `fail` fails |

Require `stackorder/plan` in branch protection, and `stackorder/apply` too in `before_merge` mode. In `on_merge` mode the apply runs after the merge, so a required `stackorder/apply` check would block every merge. Stackorder reports; GitHub enforces the merge.

Each pull request has one sticky comment, found by the hidden marker `<!-- stackorder:sticky -->` on its first line and by the App's own login. It has a collapsible section per stack, and links to pending environment approvals.

## Fork pull requests {#forks}

The `pull_request` trigger gives forks a read-only `GITHUB_TOKEN` and no `id-token` permission, so neither the AWS role nor the Stackorder API can be reached from a fork. The reusable `plan.yml` runs nothing for a fork but a job that explains this in its summary, and the server posts a neutral check. See [Workflows](/configuration/workflows#forks).
