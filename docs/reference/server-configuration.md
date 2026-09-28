# Server configuration

The server is configured entirely with environment variables. Load secrets from a secret store at start-up, such as AWS Secrets Manager through ECS `secrets`; never bake them into an image.

## Variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `STACKORDER_BASE_URL` | none | Public URL of the server. The App manifest, OAuth callbacks, the OIDC audience default and links derive from it; set it before opening `/setup`. |
| `STACKORDER_LISTEN` | `:8080` | Listen address. |
| `DATABASE_URL` | required | Postgres DSN. The server refuses to start without it. |
| `GITHUB_APP_ID` | none | The App id. |
| `GITHUB_APP_PRIVATE_KEY` | none | The App private key, PEM. |
| `GITHUB_WEBHOOK_SECRET` | none | The secret webhook signatures are checked against. |
| `GITHUB_OAUTH_CLIENT_ID` | none | The App's OAuth client id, for human sign-in. |
| `GITHUB_OAUTH_CLIENT_SECRET` | none | The App's OAuth client secret. |
| `GITHUB_API_URL` | `https://api.github.com` | GitHub API base URL. GitHub Enterprise Server sets its own. |
| `GITHUB_OIDC_ISSUER` | `https://token.actions.githubusercontent.com` | Expected `iss` of runner tokens. Override for GitHub Enterprise Server and tests. |
| `GITHUB_OIDC_JWKS_URL` | GitHub's JWKS | Where runner token signing keys are fetched. Override for GitHub Enterprise Server and tests. |
| `STACKORDER_OIDC_AUDIENCE` | `STACKORDER_BASE_URL` | Expected `aud` of runner tokens. |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | unset | Optional glob that runner tokens' `job_workflow_ref` must match. |
| `STACKORDER_ARTIFACT_BUCKET` | unset | Optional S3 bucket for full plan text and JSON. |
| `STACKORDER_SESSION_KEY` | none | 32-byte key, hex encoded, that signs session cookies. |
| `STACKORDER_PLAN_TEXT_RETENTION` | `720h` | How long plan text is kept (30 days). |
| `STACKORDER_EVENT_RETENTION` | `168h` | How long queue rows are kept (7 days). |
| `STACKORDER_DRIFT_RETENTION` | `2160h` | How long drift history is kept (90 days). |
| `STACKORDER_WORKERS` | `4` | Worker goroutines claiming events and jobs. |
| `STACKORDER_LOG_LEVEL` | `info` | Log level. |
| `STACKORDER_LOG_FORMAT` | `json` | `json`, or `text` for human-readable logs. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | Enables OpenTelemetry tracing over OTLP HTTP. |

Retention values are Go durations, such as `720h`.

## Setup mode {#setup-mode}

Without the GitHub App variables, the server starts in setup mode. It serves only `/setup`, `/healthz` and `/readyz`, so you can create the App from the browser. Once `/setup` prints the App credentials, set `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET`, `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET` and restart. See [Getting started](/guide/getting-started#create-app).

## Generating the session key

```sh
openssl rand -hex 32
```

Keep the key stable across restarts and instances. Every instance must use the same key, and changing it signs everyone out.

## Pinning the reusable workflow {#required-workflow-ref}

`STACKORDER_REQUIRED_WORKFLOW_REF` makes the server accept results only from jobs whose `job_workflow_ref` matches the glob, so a locally edited copy of the workflow cannot post results. To accept only the canonical reusable workflows at a `v1` tag:

```sh
STACKORDER_REQUIRED_WORKFLOW_REF='stackorder/actions/.github/workflows/*.yml@refs/tags/v1*'
```

Pair it with a `job_workflow_ref` condition in the AWS role trust policy. See [the AWS trust policy layer](/configuration/environments-and-authorization#layer-5).

## Artifact bucket {#artifact-bucket}

Plan text beyond 256 KB is truncated with a pointer to the Actions job log. `STACKORDER_ARTIFACT_BUCKET` moves full plan text and JSON into an S3 bucket owned by the server's task role, with lifecycle expiry. It is the only optional AWS dependency, and it holds Stackorder's own artifacts, never Terraform state.

## GitHub Enterprise Server {#ghes}

Set the API URL and the OIDC issuer of your instance before opening `/setup`:

```sh
GITHUB_API_URL=https://github.example.com/api/v3
GITHUB_OIDC_ISSUER=https://github.example.com/_services/token
GITHUB_OIDC_JWKS_URL=https://github.example.com/_services/token/.well-known/jwks
```

The App manifest flow is the same.

## Network

The server listens on plain HTTP on `STACKORDER_LISTEN`; terminate TLS in front of it. It needs inbound HTTPS from GitHub for webhooks and from people for the UI, and outbound access to the GitHub API, GitHub's OIDC keys, and Postgres. It makes no other outbound calls, apart from the optional artifact bucket and, when tracing is enabled, the OTLP endpoint.
