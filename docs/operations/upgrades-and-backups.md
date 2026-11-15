---
description: 'How the Stackorder server image, CLI and actions are versioned and upgraded, how to back up and restore Postgres, its only state, and how to rotate secrets.'
---

# Upgrades and backups

Three things carry versions: the server image, the CLI, and the actions. Postgres is the only state to back up.

## What is versioned {#versions}

| Component | Where | Versions |
| --- | --- | --- |
| Server | `ghcr.io/stackorder/stackorder` | `X.Y.Z` and `latest`, from tags `vX.Y.Z` on `stackorder/stackorder` |
| CLI | Release assets on `stackorder/stackorder` | `stackorder_X.Y.Z_<os>_<arch>.tar.gz` with a SHA-256 checksum file |
| Actions and reusable workflows | `stackorder/actions` | `v1` and `v1.x.y`; the `v1` tag moves independently of server releases |

Workflows reference the reusable workflows at `@v1`. The `stackorder-version` input pins the CLI release that `setup` installs; it defaults to `latest`.

The API between the CLI and the server is `v1`. Fields are only ever added and both sides ignore fields they do not know, so the server and the CLI can be upgraded independently within `v1`.

## Upgrading the server {#server}

1. Read the release notes for the new version.
2. Take a database snapshot if the release includes migrations.
3. Deploy the new image tag.
4. The new instance runs pending migrations at start-up, under a migration lock, so only one instance applies them.
5. The previous instance keeps serving until the new one passes `/readyz`.

On AWS with the Terraform module, this is a change of the image tag and an apply. See [Deploy on AWS](./deploy-aws#upgrades).

To apply the migrations as a separate step before the server rolls — so a schema change lands under your control, not as a side effect of the first new instance starting — run the new image's `migrate` command against the same database first:

```sh
docker run --rm -e DATABASE_URL="$DATABASE_URL" \
  ghcr.io/stackorder/stackorder:X.Y.Z migrate
```

It takes the same migration lock as start-up, so it is safe while the old instances keep serving, and `stackorder-server migrate --status` reports whether a database is already at the image's version (exit `0`), has migrations pending (exit `2`) or is `dirty` or ahead of the image's version (exit `1`). This is optional; without it the new instance migrates at start-up as before. See [Migrating](/reference/server-configuration#migrate).

If a migration fails — whether at start-up or from `migrate` — it leaves the schema marked `dirty`, and every later start and `migrate` refuses the database until the flag is cleared, so the backup from step 2 is what you roll back to. Each migration runs in one transaction, so a failed one rolls back and leaves the schema at the previous version. To clear the flag after checking or repairing the schema, use `stackorder-server migrate --force N` with the version the schema is at; see [Recover from a dirty migration](/operations/troubleshooting#dirty-migration) for the full procedure.

In-flight runs survive an upgrade. Work is queued in Postgres and every handler is idempotent: on `SIGTERM` an instance hands unstarted work back to the queue and gives running handlers 30 s, and anything a killed instance had claimed is claimed again after 10 minutes. The per-minute reconciliation catches any wave that finished during the switch. GitHub does not retry a failed webhook delivery by itself; redeliver it from the App's delivery log if needed.

Upgrading from v0.5.0 crosses a window where old and new replicas share the database. A v0.5.0 replica clears a stack's `removed_at` on every plan save; a replica on this release clears it only when a merge moves the default-branch graph. So while a v0.5.0 replica is still serving, an open pull request it replans can revive a stack that a merged pull request already deleted from the default branch, and because that stack is no longer on the default branch no later merge re-marks it — it stays in the stack lists and the overview count until its directory returns to the default branch. Complete the rollout promptly to keep the window short; the effect is cosmetic (a deleted stack counted as live) and touches no run or state.

The server applies an installation's `suspend` and `unsuspend` webhooks in receive order (see the [data model](/reference/data-model#queue)). This guarantee holds once every replica runs the version that introduced it. While a v0.5.0 replica and a new one run together during the rollover, a `suspend` or `unsuspend` the old replica processes still takes effect in the order it is processed, as v0.5.0 always did, not by receive time; the daily installation sync and the finished upgrade settle it, so no action is required. To avoid even that transient, drain the old replicas before the new ones serve.

[API key scopes](/reference/api#api-key-scopes) are enforced only by a replica on this release. Migration `0016_api_key_scopes` stores a scoped or expiring key in the same table, and under the same SHA-256 hash, that v0.5.0 accepts, and a v0.5.0 replica reads neither the scope columns nor the expiry: it authenticates every unrevoked key as a full, installation-wide, never-expiring administrator. So during the rolling upgrade a read-only, installation-scoped or expired key minted by a new replica is honoured as an unrestricted administrator, including past its expiry, by any v0.5.0 replica still serving. Existing keys are unaffected — they are unscoped administrators already — so mint and hand out scoped or expiring keys only once every replica runs this release. Before rolling back to a version without scope enforcement, revoke every scoped or expiring key you issued, because that version ignores all three restrictions.

To go back, deploy the previous image. The server never runs down migrations, so if the new version ran migrations the previous one does not understand, restore the snapshot from step 2. For the same reason a replica of the previous version cannot start once the new one has migrated the database: its startup migration refuses a schema newer than it knows. During a rolling deploy already-running old replicas keep serving and draining normally, but do not count on restarting or rescheduling one after the migration has run without restoring the snapshot.

## Upgrading the CLI and actions {#cli}

- Set `stackorder-version` in the calling workflows to pin the CLI, and bump it to move. Left at `latest`, every run installs the newest release.
- The `v1` tag of `stackorder/actions` moves on its own. Calling `plan.yml@v1.x.y` instead of `@v1` pins the workflow file, but the reusable workflows still call the actions at `@v1`.
- If the server sets `STACKORDER_REQUIRED_WORKFLOW_REF` or the AWS roles pin `job_workflow_ref`, keep the pinned pattern in step with the tags you use. `refs/tags/v1*` covers every `v1` release.
- CLI v0.5.0 and later report each stack's [resource list](/guide/concepts#resources) from apply and drift jobs. An older server ignores it, and a stack's page shows no list until a job on such a release has reported one.

## Backups {#backups}

Postgres holds the graphs, the run history, the locks, drift results, sessions, API keys, the work queue, where each stack's state object lives and each stack's resource list, with no attribute values. Terraform state itself is never there.

- **RDS or Aurora:** turn on automated backups with point-in-time recovery.
- **Elsewhere:** use your platform's snapshots or `pg_dump` on a schedule.

### Data-only dumps {#data-only}

A snapshot or a plain `pg_dump` restores the whole database, schema included. If instead you keep a data-only dump (`pg_dump --data-only`) to load into a database the server has already migrated, dump everything but the migration bookkeeping and restore it in a single transaction:

```sh
pg_dump --data-only --no-owner --no-privileges \
  --exclude-table-data=schema_migrations "$SOURCE_URL" -f stackorder-data.sql
psql --single-transaction -v ON_ERROR_STOP=1 "$TARGET_URL" -f stackorder-data.sql
```

`repos` and `graphs` reference each other, so no load order satisfies both foreign keys row by row. `repos.default_graph_id` is `DEFERRABLE INITIALLY DEFERRED`, so the cycle is checked once at commit instead: the whole dump must load in one transaction, which `--single-transaction` (or `pg_restore --single-transaction` for a custom-format dump) does. This needs no superuser, so it works on a managed Postgres where `pg_restore --disable-triggers` is not available.

Restore into a database migrated to the same version the dump was taken at. `--exclude-table-data=schema_migrations` leaves out the migration bookkeeping row the target already has; without it the load fails on a duplicate key in `schema_migrations`.

What a lost database costs you: history, drift results, locks, and the state locations [shared outputs](/configuration/outputs#state-location) read from and the resource lists, which each stack's next apply or drift job records again. Plans and applies keep working against your S3 state once a new database is in place, because nothing in the server is needed to operate Terraform, except by stacks that read shared outputs: their plans fail until each producer's next apply or drift job records its state location again.

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
| API keys | Only their SHA-256 is stored. Create a new key, switch the automation to it, then revoke the old one; see [API keys](/reference/server-configuration#api-keys). |

With the [Terraform module](./deploy-aws#secrets), a new value in one of its ephemeral inputs reaches the App secret only in an apply that also increases `secrets_version`, and `db_password_version` rotates the database password. Each increase of `secrets_version` also generates a new session key and metrics token unless `session_key` and `metrics_token` are set: everyone signs in again, and scrapers must read the new token.

There are no runner-side secrets to rotate: runners authenticate with short-lived OIDC tokens.
