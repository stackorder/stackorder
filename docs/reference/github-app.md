# GitHub App

The App is the server's only identity towards GitHub and the only way GitHub reaches the server. It asks for the smallest permission set that still lets it dispatch workflows, write checks and comment.

## Permissions {#permissions}

| Permission | Level | Why |
| --- | --- | --- |
| Metadata | Read | Required for any App |
| Contents | Read | Read `stackorder.yaml` at the default branch to decide policy without dispatching a job; read tags on module repos |
| Pull requests | Write | Sticky comment, reaction on `stackorder apply`, reading approvals and mergeability |
| Checks | Write | One check run per stack per phase, plus roll-ups |
| Actions | Write | `workflow_dispatch` for `stackorder-run.yml`; reading `workflow_run` and `workflow_job` state; reading artifact metadata |
| Issues | Write (optional) | Only if drift issues are enabled |
| Members | Read (optional) | Only if `apply.allowed_teams` or code-owner team checks are used |
| Deployments | Read and write (optional) | Only if the App is registered as a custom deployment protection rule |

### What the App cannot do {#not-requested}

The App has no `Secrets`, `Administration`, `Environments` or `Workflows` permission. It cannot change workflow files, secrets, environments or protection rules, so a compromised server cannot widen its own access.

An App also cannot be a required reviewer on an environment. Even the optional Deployments permission never lets the server approve a human gate; it only lets the server answer for its own protection rule.

## Events {#events}

| Event | Used for |
| --- | --- |
| `installation`, `installation_repositories` | Tracking installations and the repositories they cover |
| `pull_request` | Opened, synchronized, closed and merged PRs; releasing locks on merge; applying on merge in `on_merge` mode |
| `pull_request_review` | Approvals for the apply gate |
| `issue_comment` | Comment commands |
| `push` | Refreshing `stackorder.yaml` from the default branch; semver tags on module repos |
| `check_run` | Re-runs requested from the checks UI (`rerequested`) |
| `check_suite` | Check suite re-requests |
| `workflow_run` | Closing the loop on each wave |
| `workflow_job` | Live per-job progress and runner queue time in the UI |
| `deployment_protection_rule` (optional) | Approving or rejecting deployments as a custom protection rule |

If a webhook is lost, the server polls `workflow_run` state for in-flight runs every 60 s, and the CLI's result call is the source of truth.

## Install flow {#install-flow}

The server serves `GET /setup`. It renders a GitHub App manifest with the right webhook URL, permissions and events, and posts it to GitHub's manifest-creation endpoint.

1. Open `/setup` on the server and confirm the App on GitHub.
2. GitHub returns the App id, private key, webhook secret and OAuth client id and secret in one exchange, through `/setup/callback`.
3. The page prints them once, as the environment variables to load into your secret store.
4. Restart the server with them, then install the App on your repositories.

The whole setup is one browser visit, with no permission checkboxes to copy by hand. GitHub Enterprise Server uses the same flow with `GITHUB_API_URL` set.

## Token handling {#tokens}

- The App's private key signs a 10-minute JWT.
- The server exchanges the JWT for installation tokens, valid for one hour, cached per installation until 5 minutes before expiry.
- Every call to GitHub uses the token of the installation it concerns, so a repository in one org can never be touched with another org's token.
- Calls are retried, and the `Retry-After` header is respected.

Human sign-in uses the App's user-authorization flow with `login` and `read:org` scope only. A session is issued only to a member of an org where the App is installed, and the UI shows only that org's repositories.

Rotating anything means rotating the App's private key, which runners never see. Generate a new key in the App settings, update `GITHUB_APP_PRIVATE_KEY`, redeploy, then delete the old key.

## Comment commands {#commands}

Commands are PR comments. The first token must be exactly `stackorder`, case insensitive, at the start of a line.

| Command | Effect |
| --- | --- |
| `stackorder plan [key…]` | Re-plans the affected stacks, or only the named ones. |
| `stackorder apply [key…]` | Applies the affected stacks through the apply gate, or only the named subset. Dependency waves are still honoured within the subset. |
| `stackorder unlock [key…]` | Releases orchestration locks. Needs write permission. |
| `stackorder help` | Prints the list of commands. |

```text
stackorder apply stacks/prod/vpc stacks/prod/apps
```

The App reacts with an eyes emoji when it receives a command and a rocket when it dispatches, so a dropped command is visible.

Commands come from anyone the apply policy allows. They are ignored from users without write access and rate-limited to 10 per minute per PR.

## Checks and the sticky comment {#checks}

| Check run | Meaning |
| --- | --- |
| `stackorder/resolve` | The graph was resolved; fails on a dependency cycle, with the cycle spelled out |
| `stackorder/plan: <key>` | The plan for one stack |
| `stackorder/plan` | Roll-up of every stack's plan |
| `stackorder/apply: <key>` | The apply for one stack |
| `stackorder/apply` | Roll-up of the apply; green when the last wave is green |
| `stackorder/<check-name>: <key>` | A named policy or cost check reported with `stackorder check` |

Require `stackorder/plan` in branch protection, and `stackorder/apply` too in `before_merge` mode. In `on_merge` mode the apply runs after the merge, so a required `stackorder/apply` check would block every merge. Stackorder reports; GitHub enforces the merge.

Each PR has one sticky comment, found by the hidden marker `<!-- stackorder:sticky -->` on its first line. It has a collapsible section per stack, and links to pending environment approvals.

## Fork pull requests {#forks}

The `pull_request` trigger gives forks a read-only `GITHUB_TOKEN` and no `id-token` permission, so neither the AWS role nor the Stackorder API can be reached from a fork. By default Stackorder posts a single neutral check explaining this and runs nothing. See [Workflows](/configuration/workflows#forks).
