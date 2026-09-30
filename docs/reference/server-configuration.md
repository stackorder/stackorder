---
description: 'Every environment variable that configures stackorder-server: base URL, database, GitHub App, OIDC, artifact bucket, retention, metrics token and logs.'
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
| `STACKORDER_OIDC_AUDIENCE` | `STACKORDER_BASE_URL` | Expected `aud` of runner tokens. The CLI requests the server URL it was given as audience, so the two must match exactly. |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | unset | A glob that every runner token's `job_workflow_ref` must match, validated at start-up. See [below](#required-workflow-ref). |
| `STACKORDER_ARTIFACT_BUCKET` | unset | S3 bucket name, not a URL, for full plan text. See [below](#artifact-bucket). |
| `STACKORDER_ARTIFACT_PREFIX` | empty | Key prefix inside the bucket. Setting it without a bucket is an error. |
| `AWS_ENDPOINT_URL_S3` | unset | Only read with a bucket: an S3-compatible endpoint, used with path-style addressing, such as LocalStack. |
| `STACKORDER_SESSION_KEY` | generated | 32 bytes, hex encoded (64 characters), that sign session and sign-in cookies. When unset the server generates one at each start and logs a warning, so sessions end at every restart and are not shared between instances. |
| `STACKORDER_METRICS_TOKEN` | unset | When set, `GET /metrics` requires `Authorization: Bearer <token>`. |
| `STACKORDER_PLAN_TEXT_RETENTION` | `720h` | How long stored plan text is kept (30 days). |
| `STACKORDER_EVENT_RETENTION` | `168h` | How long webhook events and finished jobs are kept (7 days). |
| `STACKORDER_DRIFT_RETENTION` | `2160h` | How long drift history is kept (90 days); the latest result of each stack is always kept. |
| `STACKORDER_WORKERS` | `4` | Worker goroutines claiming events and jobs; a positive integer. The database pool gets this many connections plus 8, for the API, the webhook receiver and the scheduler, unless `DATABASE_URL` sets `pool_max_conns`. |
| `STACKORDER_ALLOW_RESETUP` | `false` | A boolean, such as `true` or `false`; other values are refused. `true` lets a server that already has App credentials create another App through `/setup?force=1`. See [below](#setup-mode). |
| `STACKORDER_SETUP_TOKEN` | generated | The one-time token that opens `/setup`: at least 32 letters, digits, `.`, `_`, `~` or `-`, such as the output of `openssl rand -hex 32`. When unset and `/setup` can create an App, the server generates one from 32 random bytes at each start and logs the setup URL with it. See [below](#setup-token). |
| `STACKORDER_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `STACKORDER_LOG_FORMAT` | `json` | `json`, or `text` for human-readable lines. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP HTTP base URL, such as `http://otel-collector:4318`; spans go to `<endpoint>/v1/traces`. Unset disables tracing. See [Metrics and tracing](/reference/metrics#tracing). |

Retention values are positive Go durations, such as `720h` or `90m`. URL variables must be absolute `http` or `https` URLs.

## Setup mode {#setup-mode}

`GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY` and `GITHUB_WEBHOOK_SECRET` go together: set all three, or none. A partial set is an error.

With none of them the server starts in **setup mode**. It connects to the database and runs the migrations, then serves only `/setup`, `/setup/callback`, `/setup/installed`, `/healthz` and `/readyz`; everything else answers `503` with code `unavailable`. It runs no workers, no scheduler and no GitHub client. `/healthz` and `/readyz` report `"setup_mode": true`.

Open the setup URL from the server log, create the App, set the printed variables and restart. See [Getting started](/guide/getting-started#create-app) and the [setup endpoints](/reference/api#setup).

Once the App variables are set, `/setup` only says so. Creating a replacement App, for instance after moving the server to a new URL, needs `STACKORDER_ALLOW_RESETUP=true`; without it `/setup?force=1` and `/setup/callback` answer 404, so nobody can start a second App on a running server. Unset it again once the new credentials are loaded.

### The setup token {#setup-token}

Whoever creates the App owns it, so `/setup` opens only with a one-time token. At start-up the server logs a `setup_url` line at `warn` level, which shows under the default `STACKORDER_LOG_LEVEL`:

```json
{"time":"…","level":"WARN","msg":"setup is open to whoever holds the setup token; open setup_url in a browser to create the GitHub App","setup_url":"https://stackorder.example.com/setup?token=x0hYMC7BJPZbzQBz6vZcb4IS1MnVumltqNVS5uiNkgE","hint":"…"}
```

- Open that URL in the browser you create the App with. The server checks the token, sets a signed cookie valid for an hour, and redirects to `/setup` without the token, so the token never reaches GitHub. Add `&org=` or `&name=` to the URL, or open `/setup?org=<organisation>` afterwards in the same browser.
- Without the token or the cookie, `/setup` answers `403` with a page saying where to find the URL.
- The token stops working once an App is created. Until the server restarts, `/setup` then says an App exists.
- A generated token changes at every start, and each instance generates its own. Run one instance until the App exists, or set the same `STACKORDER_SETUP_TOKEN` on every instance.
- With `STACKORDER_SETUP_TOKEN` set, the log line shows `<STACKORDER_SETUP_TOKEN>` in place of the token. That token opens `/setup` again after every restart, so unset it once the App exists.
- With `STACKORDER_ALLOW_RESETUP=true` the server logs `/setup?force=1&token=…`. Creating a replacement App needs the token too.

## Start-up and shutdown {#lifecycle}

1. Load and check the configuration.
2. Connect to Postgres and run pending migrations under a migration lock, so several instances starting together apply each migration once.
3. Outside setup mode: enqueue a sync of the App's installations and their repositories, then start the worker pool and the scheduler. The sync adds and refreshes installations and repositories, and reads `stackorder.yaml` only for repositories it has not seen before; the scheduler repeats it daily at 04:00 UTC. This is how installations made while the server was in setup mode, or whose webhooks were lost, are learned. When GitHub lists every installation and the repositories of every unsuspended one, the sync also forgets the installations it no longer lists and the repositories of unsuspended installations that no listing names, with their history, as their deletion webhooks would have; when any listing fails it forgets nothing.
4. Serve HTTP.

On `SIGTERM` or `SIGINT` the server stops accepting connections, gives in-flight requests 15 s to finish, stops the scheduler, hands unstarted work back to the queue and gives running handlers 30 s to drain. A second signal stops it at once.

## Commands {#commands}

```text
$ stackorder-server --help
usage: stackorder-server [command]

Commands:
  (none)       run the server; configuration comes from the environment
  healthcheck  GET http://127.0.0.1<STACKORDER_LISTEN>/healthz, exit 0 on 200
  version      print the build information
```

`stackorder-server healthcheck` requests `/healthz` on the listen port, mapping an unspecified host to `127.0.0.1`, with a 3 s timeout, and exits 0 on `200` and 1 otherwise. It is for container health checks in images without a shell: the published image declares it as its `HEALTHCHECK`. An unknown command exits 2.

## Generating the session key {#session-key}

```sh
openssl rand -hex 32
```

Keep the key stable across restarts and instances. Every instance must use the same key, and changing it signs everyone out.

## API keys {#api-keys}

Automation, `stackorder apply --local` and `stackorder unlock` authenticate with an API key, `sk_` followed by random characters, sent as `Authorization: Bearer sk_…`. An API key sees every repository and is treated as an administrator. The server stores only the SHA-256 of a key and its first 10 characters, for display.

This release has no endpoint or command that creates API keys. Create one with a row in the `api_keys` table, from a shell that can reach the database:

```sh
key="sk_$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -v key="$key" <<'SQL'
INSERT INTO api_keys (name, key_hash, prefix, created_by)
VALUES ('ci', encode(sha256(convert_to(:'key', 'UTF8')), 'hex'), left(:'key', 10), 'admin');
SQL
echo "$key"   # shown once; store it in your secret store
```

The `name` becomes the actor in audit entries, as `apikey:ci`. Revoke a key by setting its `revoked_at`:

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

The CLI sends at most 256 KB of redacted plan text per stack, and without a bucket the server keeps it in Postgres. With `STACKORDER_ARTIFACT_BUCKET` set, the server writes that text to `<prefix>runs/<run id>/<slug>/plan.txt` in the bucket, and the plan summary to `plan.json` next to it (the slug is the key with `/` and `:` replaced by `-`, then `-` and the first 8 hex characters of the key's SHA-256, as in the [plan artifact name](/reference/cli#plan)), keeps only the first 8 KB in Postgres, and links the object from the run's stack row as `plan_url` (`s3://bucket/key`). [`GET /v1/runs/{id}/stacks/{key}/plan`](/reference/api#plan-text) reads the full text back from the bucket, and the run page of the web UI shows it in place of the stored beginning, so the server's role needs `s3:GetObject` as well as `s3:PutObject` on the prefix, and `s3:ListBucket` on the bucket: without it S3 answers a read of an expired object with `403` instead of `404`, and the endpoint answers `500` instead of `404`. The [Terraform module](/operations/deploy-aws) grants all three. When the upload fails it falls back to Postgres. Stacks with `plan_output: summary` store no text anywhere.

The server's AWS credentials come from the standard SDK chain, such as the ECS task role. It is the only AWS access the server ever has, and it is for Stackorder's own artifacts, never for Terraform state. Give the bucket a lifecycle expiry; the [Terraform module](/operations/deploy-aws) does. That expiry, not `STACKORDER_PLAN_TEXT_RETENTION`, bounds how long the full text is served: the endpoint keeps answering after the retention cleared the beginning kept in Postgres, until the object expires. It reads under the current bucket and prefix, so after either changes, the plans stored before answer `404`.

## GitHub Enterprise Server {#ghes}

Set the API and web URLs, and the OIDC issuer of your instance, before opening `/setup`:

```sh
GITHUB_API_URL=https://github.example.com/api/v3
GITHUB_WEB_URL=https://github.example.com
GITHUB_OIDC_ISSUER=https://github.example.com/_services/token
```

`GITHUB_OIDC_JWKS_URL` defaults to the issuer followed by `/.well-known/jwks`. The App manifest flow is the same, except that the manifest leaves out the `deployment_protection_rule` event and the Deployments permission, which not every Enterprise Server release offers; add them in the App settings if your instance supports custom deployment protection rules. The page shown after the App is created also prints `GITHUB_API_URL` and `GITHUB_WEB_URL`.

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
| Outbound, optional | S3 | The artifact bucket |
| Outbound, optional | `OTEL_EXPORTER_OTLP_ENDPOINT` | Traces |

It makes no other outbound calls.
