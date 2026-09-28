# Upgrades and backups

Three things carry versions: the server image, the CLI, and the actions. Postgres is the only state to back up.

## What is versioned {#versions}

| Component | Where | Versions |
| --- | --- | --- |
| Server | `ghcr.io/stackorder/stackorder` | `X.Y.Z` and `latest`, from tags `vX.Y.Z` on `stackorder/stackorder` |
| CLI | Release assets on `stackorder/stackorder` | `stackorder_X.Y.Z_<os>_<arch>.tar.gz` with a SHA-256 checksum file |
| Actions and reusable workflows | `stackorder/actions` | `v1` and `v1.x.y`; the `v1` tag moves independently of server releases |

Workflows reference the reusable workflows at `@v1`. The `stackorder-version` input pins the CLI release that `setup` installs.

The API between the CLI and the server is `v1`. Fields are only ever added and both sides ignore fields they do not know, so the server and the CLI can be upgraded independently within `v1`.

## Upgrading the server {#server}

1. Read the release notes for the new version.
2. Take a database snapshot if the release includes migrations.
3. Deploy the new image tag.
4. The new instance runs pending migrations at start-up, under a migration lock, so only one instance applies them.
5. The previous instance keeps serving until the new one passes `/readyz`.

On AWS with the Terraform module, this is a change of the image tag and an apply. See [Deploy on AWS](./deploy-aws#upgrades).

In-flight runs survive an upgrade. Work is queued in Postgres and every handler is idempotent, so anything the old instance had claimed is claimed again by the new one. The 60-second `workflow_run` reconciliation catches any wave that finished during the switch. GitHub does not retry a failed webhook delivery by itself; redeliver it from the App's delivery log if needed.

To go back, deploy the previous image. If the new version ran migrations the previous one does not understand, restore the snapshot from step 2.

## Upgrading the CLI and actions {#cli}

- Bump `stackorder-version` in the calling workflows to move the CLI.
- The `v1` tag of `stackorder/actions` moves on its own; pin `@v1.x.y` instead of `@v1` to control it.
- If the server sets `STACKORDER_REQUIRED_WORKFLOW_REF` or the AWS roles pin `job_workflow_ref`, keep the pinned pattern in step with the tags you use. `refs/tags/v1*` covers every `v1` release.

## Backups {#backups}

Postgres holds the graphs, the run history, the locks, drift results, sessions, API keys and the work queue. Terraform state is never there.

- **RDS or Aurora:** turn on automated backups with point-in-time recovery.
- **Elsewhere:** use your platform's snapshots or `pg_dump` on a schedule.

What a lost database costs you: history, drift results and locks. Plans and applies keep working against your S3 state once a new database is in place, because nothing in the server is needed to operate Terraform.

## Restoring {#restore}

1. Stop the server, or scale it to zero.
2. Restore the database to the chosen point.
3. Start the server. It runs any migrations the restored schema is missing.
4. Check `GET /v1/overview` for locks held, and compare them with open pull requests. A lock restored from before an apply finished, or a lock lost in the restore, needs a human decision: re-apply from the pull request, or release it with `stackorder unlock`.
5. Push to open pull requests, or comment `stackorder plan`, to re-plan anything whose results were lost.

## Rotating secrets {#rotation}

| Secret | How to rotate |
| --- | --- |
| App private key | Generate a new key in the App settings, update `GITHUB_APP_PRIVATE_KEY`, redeploy, then delete the old key. Runners never see it. |
| Webhook secret | Change it in the App settings and in `GITHUB_WEBHOOK_SECRET` together; deliveries in between fail signature checks and can be redelivered from the App's delivery log. |
| OAuth client secret | Generate a new one in the App settings, update `GITHUB_OAUTH_CLIENT_SECRET`, redeploy. |
| Session key | Change `STACKORDER_SESSION_KEY` on every instance at once. Everyone is signed out. |
| API keys | API keys are stored hashed; replace them and revoke the old ones. |

There are no runner-side secrets to rotate: runners authenticate with short-lived OIDC tokens.
