---
description: 'Every environment variable that configures stackorder-server: base URL, database, GitHub App, OIDC, artifact and state buckets, retention, metrics and logs.'
---

# Server configuration

The server, `stackorder-server`, is configured entirely with environment variables. Load secrets from a secret store at start-up, such as AWS Secrets Manager through ECS `secrets`; never bake them into an image.

Values are trimmed of surrounding white space, and an empty value counts as unset. The server checks every variable at start-up and reports all problems at once, each naming its variable, before it exits with status 1.

## Variables {#variables}

| Variable | Default | Purpose and rules |
| --- | --- | --- |
| `STACKORDER_BASE_URL` | required | Public URL of the server, an absolute `http` or `https` URL without query or fragment; trailing slashes are removed. The App manifest, OAuth and manifest callbacks, the OIDC audience default and every link derive from it; set it before opening `/setup`. |
| `DATABASE_URL` | required | Postgres connection string, as `pgx` parses it, such as `postgres://stackorder:secret@db:5432/stackorder?sslmode=require`. `pool_max_conns` in it sizes the connection pool. |
| `STACKORDER_LISTEN` | `:8080` | Listen address, `host:port` or `:port`. |
| `GITHUB_APP_ID` | unset | The numeric App id. |
| `GITHUB_APP_PRIVATE_KEY` | unset | The App private key: PEM (`RSA PRIVATE KEY` or `PRIVATE KEY`), the same PEM with literal `\n` for line breaks, or base64 of the PEM. |
| `GITHUB_WEBHOOK_SECRET` | unset | The secret webhook signatures are checked against. |
| `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | unset | The App's OAuth credentials, for human sign-in. Set both or neither; without them `/auth/login` explains that sign-in is not configured. |
| `GITHUB_API_URL` | `https://api.github.com` | GitHub API base URL. GitHub Enterprise Server uses `https://<host>/api/v3`. |
| `GITHUB_WEB_URL` | `https://github.com` | GitHub web URL, for the manifest flow, sign-in and links. |
| `GITHUB_OIDC_ISSUER` | `https://token.actions.githubusercontent.com` | Expected `iss` of runner tokens. |
| `GITHUB_OIDC_JWKS_URL` | issuer + `/.well-known/jwks` | Where runner token signing keys are fetched. |
| `STACKORDER_OIDC_AUDIENCE` | `STACKORDER_BASE_URL` | Expected `aud` of runner tokens, used as set with any trailing slash kept. When unset it defaults to `STACKORDER_BASE_URL` with its trailing slash removed; the CLI derives the same default from the server URL, so a server URL with a trailing slash still matches. |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | unset | A glob that every runner token's `job_workflow_ref` must match, validated at start-up. See [below](#required-workflow-ref). |
| `STACKORDER_ARTIFACT_BUCKET` | unset | S3 bucket name, not a URL, for full plan text. See [below](#artifact-bucket). |
| `STACKORDER_ARTIFACT_PREFIX` | empty | Key prefix inside the bucket. Setting it without a bucket is an error. |
| `AWS_ENDPOINT_URL_S3` | unset | Only read with an artifact bucket or state buckets: an S3-compatible endpoint, used with path-style addressing, such as LocalStack. It applies to S3 requests only. The STS requests that assume `STACKORDER_STATE_ROLE_ARN` keep the AWS SDK's own endpoint resolution, which reads `AWS_ENDPOINT_URL_STS`. |
| `STACKORDER_STATE_BUCKETS` | unset | Comma-separated `owner/repo=bucket` or `owner/repo=bucket/prefix` entries, the state objects the server may read to serve the [shared outputs](/configuration/outputs) of each repository's stacks; `*=bucket[/prefix]` grants an entry to every repository. Unset turns shared outputs off. Each entry must name a repository, or `*`, and a valid S3 bucket name. See [below](#shared-outputs). |
| `STACKORDER_STATE_ROLE_ARN` | unset | The ARN of one IAM role, such as `arn:aws:iam::123456789012:role/stackorder-state-reader`, that the server assumes to read those state objects. Setting it without `STACKORDER_STATE_BUCKETS` is an error. |
| `STACKORDER_STATE_EXTERNAL_ID` | unset | The `sts:ExternalId` the server sends when it assumes `STACKORDER_STATE_ROLE_ARN`, for a role whose trust policy requires one, as a role in another account commonly does. 2 to 1224 of the characters AWS allows in an external ID (letters, digits and `+=,.@:/_-`). Setting it without `STACKORDER_STATE_ROLE_ARN` is an error. |
| `STACKORDER_SESSION_KEY` | generated | 32 bytes, hex encoded (64 characters), that sign session, sign-in and setup cookies. When unset the server generates one at each start and logs a warning, so sessions end at every restart and are not shared between instances. |
| `STACKORDER_SESSION_TTL` | `168h` | How long a browser session lasts (7 days), a Go duration between `5m` and `8760h`. It fixes both the session cookie's lifetime and the server-side expiry at sign-in; a change applies only to sessions created afterwards. |
| `STACKORDER_METRICS_TOKEN` | unset | When set, `GET /metrics` requires `Authorization: Bearer <token>`. |
| `STACKORDER_HSTS_MAX_AGE` | unset | When set, the `max-age` of the `Strict-Transport-Security` header the server adds to every response, a positive Go duration of at least `1s` such as `8760h`. The header is sent only when `STACKORDER_BASE_URL` is `https`; setting this with an `http` base URL is an error. Unset sends no header. See [HSTS](#hsts). |
| `STACKORDER_HSTS_INCLUDE_SUBDOMAINS` | `false` | Adds `includeSubDomains` to the header. Only meaningful with `STACKORDER_HSTS_MAX_AGE`. |
| `STACKORDER_HSTS_PRELOAD` | `false` | Adds `preload` to the header. Requires `STACKORDER_HSTS_INCLUDE_SUBDOMAINS` and a `STACKORDER_HSTS_MAX_AGE` of at least `8760h` (one year), what the preload list demands. |
| `STACKORDER_PLAN_TEXT_RETENTION` | `720h` | How long stored plan text is kept (30 days). |
| `STACKORDER_EVENT_RETENTION` | `168h` | How long webhook events and finished jobs are kept (7 days). |
| `STACKORDER_DRIFT_RETENTION` | `2160h` | How long drift history is kept (90 days); the latest result of each stack is always kept. |
| `STACKORDER_SCHEDULER_CATCH_UP` | `1h` | How far a new scheduler leader looks back to catch up schedules missed while no instance was leading, such as during an outage; a positive Go duration, at most `168h`. For each schedule only the latest fire in the window is enqueued, so a window wider than an outage recovers the daily `08:00` and `04:00` UTC maintenance jobs and the latest drift fire without a burst of repeated work. See [below](#scheduler-catch-up). |
| `STACKORDER_WORKERS` | `4` | Worker goroutines claiming events and jobs; a positive integer. The database pool gets this many connections plus 8, for the API, the webhook receiver and the scheduler, unless `DATABASE_URL` sets `pool_max_conns`. |
| `STACKORDER_ALLOW_RESETUP` | `false` | A boolean, such as `true` or `false`; other values are refused. `true` lets a server that already has App credentials create another App through `/setup?force=1`. See [below](#setup-mode). |
| `STACKORDER_SETUP_TOKEN` | generated | The one-time token that opens `/setup`: at least 32 letters, digits, `.`, `_`, `~` or `-`, such as the output of `openssl rand -hex 32`. When unset and `/setup` can create an App, the server generates one from 32 random bytes at each start and logs the setup URL with it. See [below](#setup-token). |
| `STACKORDER_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `STACKORDER_LOG_FORMAT` | `json` | `json`, or `text` for human-readable lines. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP HTTP base URL, such as `http://otel-collector:4318`; spans go to `<endpoint>/v1/traces`. Unset disables tracing. See [Metrics and tracing](/reference/metrics#tracing). |

Retention values and `STACKORDER_SCHEDULER_CATCH_UP` are positive Go durations, such as `720h` or `90m`. URL variables must be absolute `http` or `https` URLs.

## Setup mode {#setup-mode}

`GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY` and `GITHUB_WEBHOOK_SECRET` go together: set all three, or none. A partial set is an error.

With none of them the server starts in **setup mode**. It connects to the database and runs the migrations, then serves only `/setup`, `/setup/callback`, `/setup/installed`, `/setup/logo.png`, `/healthz` and `/readyz`; everything else answers `503` with code `unavailable`. It runs no workers, no scheduler and no GitHub client. `/healthz` and `/readyz` report `"setup_mode": true`.

Open the setup URL from the server log, create the App, set the printed variables and restart. See [Getting started](/guide/getting-started#create-app) and the [setup endpoints](/reference/api#setup).

Once the App variables are set, `/setup` only says so. Creating a replacement App, for instance after moving the server to a new URL, needs `STACKORDER_ALLOW_RESETUP=true`; without it `/setup?force=1` and `/setup/callback` answer 404, so nobody can start a second App on a running server. Unset it again once the new credentials are loaded.

### The setup token {#setup-token}

Whoever creates the App owns it, so `/setup` opens only with a one-time token. At start-up the server logs a `setup_url` line at `warn` level, which shows under the default `STACKORDER_LOG_LEVEL` and at `warn`, but not at `error`:

```json
{"time":"…","level":"WARN","msg":"setup is open to whoever holds the setup token; open setup_url in a browser to create the GitHub App","setup_url":"https://stackorder.example.com/setup?token=x0hYMC7BJPZbzQBz6vZcb4IS1MnVumltqNVS5uiNkgE","hint":"…"}
```

- Open that URL in the browser you create the App with. The server checks the token, sets a signed cookie valid for an hour, and redirects to `/setup` without the token, so the token never reaches GitHub. Add `&org=` or `&name=` to the URL, or open `/setup?org=<organisation>` afterwards in the same browser.
- Without the token or the cookie, `/setup` answers `403` with a page saying where to find the URL.
- The token stops working on the instance that created an App. Until that instance restarts, its `/setup` says an App exists. Other instances that share the token keep accepting it until they restart, which is why one instance during setup is the recommendation.
- A generated token changes at every start, and each instance generates its own. Run one instance until the App exists, or set the same `STACKORDER_SETUP_TOKEN` and `STACKORDER_SESSION_KEY` on every instance: the setup cookies are signed with the session key.
- With `STACKORDER_SETUP_TOKEN` set, the log line shows `<STACKORDER_SETUP_TOKEN>` in place of the token. That token opens `/setup` again after every restart, so unset it once the App exists.
- With `STACKORDER_ALLOW_RESETUP=true` the server logs `/setup?force=1&token=…`. Creating a replacement App needs the token too, so anyone who can read the log can start one while the variable is set.

## Start-up and shutdown {#lifecycle}

1. Load and check the configuration.
2. Connect to Postgres and run pending migrations under a migration lock, so several instances starting together apply each migration once.
3. Outside setup mode: enqueue a sync of the App's installations and their repositories, then start the worker pool and the scheduler. The sync adds and refreshes installations and repositories, and reads `stackorder.yaml` only for repositories it has not seen before; the scheduler repeats it daily at 04:00 UTC. This is how installations made while the server was in setup mode, or whose webhooks were lost, are learned. When GitHub lists every installation and the repositories of every unsuspended one, the sync also forgets the installations it no longer lists and the repositories of unsuspended installations that no listing names, with their history, as their deletion webhooks would have; when any listing fails it forgets nothing.
4. Serve HTTP.

On `SIGTERM` or `SIGINT` the server stops accepting connections, gives in-flight requests 15 s to finish, stops the scheduler, hands unstarted work back to the queue and gives running handlers 30 s to drain. A second signal stops it at once.

## Scheduler catch-up {#scheduler-catch-up}

Only one instance schedules at a time: it is elected leader by a Postgres advisory lock, while every instance runs the jobs the leader enqueues. When a new instance takes the lead, after a restart, a deploy or an outage, it catches up the schedules that fired while no instance was leading. It looks back `STACKORDER_SCHEDULER_CATCH_UP`, one hour by default, and for each schedule enqueues only the latest fire in that window, so catching up never releases a burst of identical jobs. Jobs carry a dedupe key naming their slot, so if several instances or ticks see the same fire it is still enqueued once.

The housekeeping schedules are the per-minute reconcile, the hourly prune, `stale_locks` at `08:00` UTC and `sync_installations` at `04:00` UTC; drift runs fire on each repository's [`drift.schedule`](/configuration/drift). With the one-hour default, an outage longer than an hour drops the daily `08:00` and `04:00` jobs and that window's drift fires, which resume at their next scheduled time. Set a window wider than the outages you want to recover from, such as `26h` to cover a full day, and the leader enqueues the most recent missed fire of each daily job and of each drift schedule once on recovery; because only the latest fire is kept, a wider window never multiplies the work. The maximum is `168h`.

## Commands {#commands}

```text
$ stackorder-server --help
usage: stackorder-server [command]

Commands:
  (none)       run the server; configuration comes from the environment
  migrate      apply the embedded migrations to DATABASE_URL and exit
  healthcheck  GET http://127.0.0.1<STACKORDER_LISTEN>/healthz, exit 0 on 200
  api-key      create, list and revoke API keys; needs DATABASE_URL
  version      print the build information
```

`stackorder-server healthcheck` requests `/healthz` on the listen port, mapping an unspecified host to `127.0.0.1`, with a 3 s timeout, and exits 0 on `200` and 1 otherwise. It is for container health checks in images without a shell: the published image declares it as its `HEALTHCHECK`. An unknown command exits 2.

`stackorder-server api-key` manages [API keys](#api-keys) from a shell with `DATABASE_URL` set, which an operator uses to mint the first key when none exists. It needs no App credentials, and the published image can run it (`docker run … api-key create ci`). An unknown subcommand, a missing or extra argument exits 2.

### Migrating {#migrate}

The server applies pending migrations at [start-up](#lifecycle), so you normally never run migrations by hand. `stackorder-server migrate` applies them without starting a server, for operators who migrate as a separate deploy step or from a one-off job:

```text
$ stackorder-server migrate --help
usage: stackorder-server migrate [--to N | --status]

Apply the embedded migrations to DATABASE_URL and exit, holding the same
migration lock as start-up so it is safe next to starting servers. It needs
only DATABASE_URL, not STACKORDER_BASE_URL or the GitHub App settings.
```

- `stackorder-server migrate` applies every pending migration and exits `0`; it prints `migrated database from version X to version Y`, or `database already at version N` when nothing was pending.
- `stackorder-server migrate --to N` applies migrations only up to version `N`. It refuses with exit `1` when `N` is below the applied version, because this command only migrates up, or when there is no migration numbered `N`.
- `stackorder-server migrate --status` changes nothing and prints the applied and target version for a script to read:

  ```text
  current=5
  target=11
  dirty=false
  ```

  It exits `0` when the database is already at the target version, `2` when migrations are pending, and `1` when the database is `dirty` — left half way by a migration that failed, which a human must repair before it can be migrated or started — or when the database is *ahead* of the binary, its version above the latest this binary embeds (as after a rolling downgrade), which `migrate` cannot bring up because it never reverts a migration. `--status` and plain `migrate` agree: both exit `1` on a database the binary cannot migrate up.

It takes the same migration lock as start-up, so it is safe to run while servers are starting: they wait for each other and apply each migration once. It needs only `DATABASE_URL`, so it runs before `STACKORDER_BASE_URL` and the GitHub App are configured. Server start still migrates automatically; running `migrate` first does not change that.

## Generating the session key {#session-key}

```sh
openssl rand -hex 32
```

Keep the key stable across restarts and instances. Every instance must use the same key, and changing it signs everyone out.

## API keys {#api-keys}

Automation, `stackorder apply --local` and `stackorder unlock` authenticate with an API key, `sk_` followed by random characters, sent as `Authorization: Bearer sk_…`. An API key sees every repository and is treated as an administrator. The server stores only the SHA-256 of a key and its first 10 characters, for display.

### Bootstrap a key from the command line {#api-keys-bootstrap}

When no key exists yet, mint the first one with the `api-key` subcommand, from a shell that can reach the database. It prints the secret once; store it and discard the output.

```sh
stackorder-server api-key create ci --actor alice
```

`--actor` (default `operator`) is recorded as the creator and as the audit actor; it also sets the key's `created_by`. List and revoke keys the same way:

```sh
stackorder-server api-key list
stackorder-server api-key revoke <key-id>
```

### Manage keys over the API {#api-keys-api}

Once you hold a key, manage the rest over HTTP without database access; see [API keys](/reference/api#keys) for the request and response shapes.

```sh
curl -sS -H "Authorization: Bearer $STACKORDER_API_KEY" \
  -H 'Content-Type: application/json' -d '{"name":"deploy"}' \
  https://stackorder.example.com/v1/keys        # create; the secret is in the response once
curl -sS -H "Authorization: Bearer $STACKORDER_API_KEY" https://stackorder.example.com/v1/keys        # list
curl -sS -X DELETE -H "Authorization: Bearer $STACKORDER_API_KEY" https://stackorder.example.com/v1/keys/<key-id>   # revoke
```

Only an API key may manage keys; a signed-in browser user is not an administrator and is refused. Creating and revoking a key, by either route, is written to the [audit log](/reference/api#audit) as `api_key_created` and `api_key_revoked`, each in the same transaction as the change it records, so a key never exists without its creation entry and a revocation is never recorded unless it took effect; revoking an already-revoked key keeps the first revocation time and adds no further entry. A key's `name` is required, is trimmed, and may be at most 100 characters, by either route. The `name` becomes the actor of a key's own later calls, as `apikey:ci`.

### Keys inserted by hand still work {#api-keys-manual}

Rows added directly to `api_keys` before this release keep working, and you can still insert one when you cannot run the subcommand:

```sh
key="sk_$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -v key="$key" <<'SQL'
INSERT INTO api_keys (name, key_hash, prefix, created_by)
VALUES ('ci', encode(sha256(convert_to(:'key', 'UTF8')), 'hex'), left(:'key', 10), 'admin');
SQL
echo "$key"   # shown once; store it in your secret store
```

Revoke such a key by setting its `revoked_at`, or by its id with the subcommand or the API:

```sh
psql "$DATABASE_URL" -c "UPDATE api_keys SET revoked_at = now() WHERE name = 'ci' AND revoked_at IS NULL"
```

## Pinning the reusable workflow {#required-workflow-ref}

`STACKORDER_REQUIRED_WORKFLOW_REF` makes the server accept runner tokens only from jobs whose `job_workflow_ref` matches the glob, so a locally edited copy of the workflow cannot register runs or post results; other tokens are refused with `403 forbidden`. To accept only the canonical reusable workflows at a `v1` tag:

```sh
STACKORDER_REQUIRED_WORKFLOW_REF='stackorder/actions/.github/workflows/*.yml@refs/tags/v1*'
```

Pair it with a `job_workflow_ref` condition in the AWS role trust policy. See [Security hardening](/operations/security-hardening#workflow-ref).

## Artifact bucket {#artifact-bucket}

The CLI sends at most 256 KB of redacted plan text per stack, and without a bucket the server keeps it in Postgres. With `STACKORDER_ARTIFACT_BUCKET` set, the server writes that text to `<prefix>runs/<run id>/<slug>/plan.txt` in the bucket, and the plan summary to `plan.json` next to it (the slug is the key with `/` and `:` replaced by `-`, then `-` and the first 8 hex characters of the key's SHA-256, as in the [plan artifact name](/reference/cli#plan)), keeps only the first 8 KB in Postgres, and links the object from the run's stack row as `plan_url` (`s3://bucket/key`). [`GET /v1/runs/{id}/stacks/{key}/plan`](/reference/api#plan-text) reads the full text back from the bucket, and the run page of the web UI shows it in place of the stored beginning, so the server's role needs `s3:GetObject` and `s3:DeleteObject` as well as `s3:PutObject` on the prefix, and `s3:ListBucket` on the bucket: without `s3:ListBucket` S3 answers a read of an expired object with `403` instead of `404`, and the endpoint answers `500` instead of `404`. The [Terraform module](/operations/deploy-aws) grants all four. When the upload fails it falls back to Postgres and counts a `stackorder_artifact_operations_total{operation="write",result="error"}`, which is otherwise silent; see [Metrics](/reference/metrics#artifacts). Stacks with `plan_output: summary` store no text anywhere.

The server deletes a stack's `plan.txt` and `plan.json` from the bucket when [`STACKORDER_PLAN_TEXT_RETENTION`](#variables) prunes its plan text, and deletes every plan object of a repository when the repository is removed, so the full text stops being served once the retention clears the beginning kept in Postgres, not when a bucket lifecycle rule expires the object. Deletion is always on and needs `s3:DeleteObject`; a role without it keeps working, as a failed delete never fails pruning or repository removal: the full text still stops being served at retention, because the prune clears the `plan_url` and the beginning kept in Postgres whether or not the delete succeeds, but the object is logged, counted in the [`stackorder_pending_object_deletions`](/reference/metrics) gauge, left in the bucket and retried on the next prune, so only the bucket space is not reclaimed until the permission is granted or a lifecycle rule expires the object. A deployment that followed earlier versions of this page may have granted only `s3:GetObject`, `s3:PutObject` and `s3:ListBucket`; add `s3:DeleteObject` to reclaim the space, and keep a bucket lifecycle expiry as a backstop for objects a role could not delete. Plan text an older server pruned before this release left its object behind with the stack's `plan_url`; the prune job reclaims those too, queuing and deleting them once `s3:DeleteObject` is granted rather than waiting for a lifecycle rule to expire them.

The server's AWS credentials come from the standard SDK chain, such as the ECS task role. Unless [state buckets](#shared-outputs) are set, the artifact bucket is the only AWS access the server needs, and it holds Stackorder's own artifacts, never Terraform state. The server reads and deletes under the current bucket and prefix, so after either changes, the plans stored before answer `404` and their objects are no longer deleted by pruning; a bucket lifecycle expiry still reclaims them.

## Shared outputs {#shared-outputs}

`STACKORDER_STATE_BUCKETS` turns [shared outputs](/configuration/outputs) on and bounds them: a stack's job can then read another stack's root-module outputs through the server, and the server reads the producer's state object with its own AWS credentials. Without it the server reads no state, and every read answers `404 outputs_disabled`.

```sh
STACKORDER_STATE_BUCKETS=acme/infra=acme-tfstate/acme/infra/,acme/platform=acme-platform-tfstate
STACKORDER_STATE_ROLE_ARN=arn:aws:iam::123456789012:role/stackorder-state-reader
```

- Each entry is `owner/repo=` followed by a bucket, or a bucket, `/` and a key prefix. Spaces around entries and around `=` are ignored, and repositories compare without regard to case. An entry without a repository, such as `acme-tfstate/stacks/prod/`, stops the server at start, as does an entry whose prefix has a `.` or `..` segment.
- An entry grants its location to the stacks of its repository only. The server records the state object an apply or drift job reports, and reads it, only when an entry of the stack's own repository covers it, so a repository cannot make the server read another repository's state, as long as no entry of its own, or of `*`, covers that state too. Keep the prefixes of different repositories apart. An entry follows the repository's name: change it when the repository is renamed or transferred, and remove it when the repository is deleted, since a repository created later under that name would inherit it.
- `*=bucket` or `*=bucket/prefix` grants the location to every repository the server knows, an explicit opt-in to exposure across repositories: any of them can then point its stacks at an object under it. `*` is accepted only as the whole repository.
- An entry covers an object when it names its bucket and the object's key is the prefix or lies under it at a path segment boundary: the prefix ends with `/`, or the key goes on with `/` after it, so `stacks/prod` covers `stacks/prod/vpc.tfstate` but not `stacks/production/vpc.tfstate`. A key with a `.` or `..` segment is never read.
- The object is the one the stack's last apply or drift job reported, never one a pull request's plan names. A result whose object no entry of its repository covers is accepted, its object is not recorded, and the server logs it at `warn` level and clears the location it had recorded for the stack, unless a newer run recorded it, so reads answer `409 state_location_unknown` until an apply or drift job reports an object an entry covers. A read of a location no entry of the producer's repository covers answers `403 state_bucket_not_allowed` before any request to S3, and is audited and logged at `warn` level with the repository, the stack, the bucket and the key.
- With `STACKORDER_STATE_ROLE_ARN`, the server assumes that role through STS with its own credentials for these reads, and renews it before it expires. Without it, it reads with its own credentials, such as the ECS task role.
- A role in another account commonly requires an external ID in its trust policy to guard against the [confused deputy](https://docs.aws.amazon.com/IAM/latest/UserGuide/confused-deputy.html). Set `STACKORDER_STATE_EXTERNAL_ID` to the value its `sts:ExternalId` condition expects, and the server sends it as the `ExternalId` of the role assumption; without it the assumption answers `403 state_access_denied`. The external ID is an identifier, not a secret, and the server never logs its value. A trust policy that requires it reads:

  ```json
  {
    "Effect": "Allow",
    "Principal": { "AWS": "arn:aws:iam::123456789012:role/stackorder-task" },
    "Action": "sts:AssumeRole",
    "Condition": { "StringEquals": { "sts:ExternalId": "acme-stackorder" } }
  }
  ```
- The credentials that read need `s3:GetObject` on the objects under each entry, `s3:ListBucket` on each bucket limited to the same prefixes, so that a missing object answers `404 state_not_found` rather than `403 state_access_denied`, and `kms:Decrypt` for objects encrypted with a KMS key. IAM cannot tell repositories apart, so these grants cover every entry for the server as a whole, and the server's check of the producer's repository keeps each repository to its own entries. The [Terraform module](/operations/deploy-aws#shared-outputs) grants these from `shared_outputs_state`, or only `sts:AssumeRole` with `shared_outputs_role_arn`.
- The server has read access to whole state objects, secrets included, though it returns only the outputs and stores, caches and logs nothing it reads. List the narrowest prefixes that hold the state of the stacks that share outputs, one per repository.
- At start-up the server logs once, at `info` level, that shared outputs are on, with the number of entries, the entries as `repository=bucket/prefix` and the role. It then logs at `warn` level each pair of entries of different repositories, `*` included, where one's location lies under the other's, such as `acme/infra=acme-tfstate/stacks/` and `acme/network=acme-tfstate/stacks/network/`: the stacks of either repository can be pointed at the objects both cover.

The limits on a read, 64 MiB of state scanned, 4 MiB of outputs, 30 s once it has a slot, 4 reads at a time per instance and 20 s of waiting for a slot, are on the [Shared outputs](/configuration/outputs#server) page.

## GitHub Enterprise Server {#ghes}

Set the API and web URLs, and the OIDC issuer of your instance, before opening `/setup`:

```sh
GITHUB_API_URL=https://github.example.com/api/v3
GITHUB_WEB_URL=https://github.example.com
GITHUB_OIDC_ISSUER=https://github.example.com/_services/token
```

`GITHUB_OIDC_JWKS_URL` defaults to the issuer followed by `/.well-known/jwks`. The App manifest flow is the same, except that the manifest leaves out the `deployment_protection_rule` event and the Deployments permission, which not every Enterprise Server release offers; add them in the App settings if your instance supports custom deployment protection rules. The page shown after the App is created also prints `GITHUB_API_URL` and `GITHUB_WEB_URL`.

## HSTS {#hsts}

`Strict-Transport-Security` tells a browser to reach this origin over HTTPS only, for the `max-age` it names. The server does not send it by default: a self-hoster on plain HTTP, or on a parent domain shared with other services, must not be locked into HTTPS by an upgrade. Turn it on only once HTTPS is in place to stay, with `STACKORDER_HSTS_MAX_AGE`:

```sh
STACKORDER_HSTS_MAX_AGE=8760h            # one year
STACKORDER_HSTS_INCLUDE_SUBDOMAINS=true  # optional, covers every subdomain
```

- The header is added to every response, but only when `STACKORDER_BASE_URL` is `https`, the same signal that marks the session cookie `Secure`. TLS is terminated in front of the server (see [Server secrets and network](/operations/security-hardening#server)), so the base URL scheme, not the request's own transport, decides this. Setting `STACKORDER_HSTS_MAX_AGE` with an `http` base URL is a start-up error.
- `includeSubDomains` applies the policy to every subdomain of the host. Set it only when every subdomain is served over HTTPS, since a browser that has seen it refuses plain HTTP for all of them.
- `preload` (`STACKORDER_HSTS_PRELOAD`) marks the policy for the browser preload list and requires `includeSubDomains` and a `max-age` of at least one year. Submitting a host to the list is a lasting, hard-to-reverse commitment for the whole domain; set it only if you have read [hstspreload.org](https://hstspreload.org) and intend to submit.
- Start low (for example `STACKORDER_HSTS_MAX_AGE=300s`) to confirm HTTPS holds, then raise it.

## Network {#network}

The server listens on plain HTTP on `STACKORDER_LISTEN`; terminate TLS in front of it. It needs:

| Direction | Peer | Why |
| --- | --- | --- |
| Inbound | GitHub | Webhooks to `/webhooks/github` |
| Inbound | Runners | The runner API under `/v1` |
| Inbound | People | The UI, `/auth` and `/setup` |
| Outbound | `GITHUB_API_URL` | Installation tokens, checks, comments, dispatches |
| Outbound | `GITHUB_OIDC_JWKS_URL` | Runner token signing keys, cached for an hour |
| Outbound | Postgres | Everything else |
| Outbound, optional | S3 | The artifact bucket, and the state buckets of [shared outputs](#shared-outputs) |
| Outbound, optional | STS | Assuming `STACKORDER_STATE_ROLE_ARN` |
| Outbound, optional | `OTEL_EXPORTER_OTLP_ENDPOINT` | Traces |

It makes no other outbound calls.
