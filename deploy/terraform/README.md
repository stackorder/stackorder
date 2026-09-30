# Stackorder on AWS

Terraform module that runs the Stackorder server on ECS Fargate behind an
Application Load Balancer, with PostgreSQL on RDS or Aurora Serverless v2.
It works with Terraform 1.11 or later and OpenTofu 1.11 or later, and
needs the AWS provider 6.50 or later.

```text
                 GitHub webhooks, runners, people
                              |
                         443 (80 -> 443)
                              v
  public subnets    [ ALB, TLS 1.3, ACM ]  -- /readyz health check
                              |
                            8080
                              v
  private subnets   [ ECS service, 1-2 Fargate tasks ] --443--> api.github.com
  (public subnets with        |                                 (through NAT, or
   public_tasks = true)     5432                                from public IPs)
                              v
  private subnets   [ RDS PostgreSQL or Aurora Serverless v2 ]
```

The server holds no cloud credentials: its task role has no permissions
unless the optional artifact bucket is enabled, and its security group
allows only traffic from the load balancer, HTTPS out and PostgreSQL to the
database. That is the "server to nothing else" property of the design, in
AWS terms.

## Usage

```hcl
module "stackorder" {
  source = "github.com/stackorder/stackorder//deploy/terraform?ref=v0.1.0"

  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
  image_tag       = "0.1.0"
}
```

Set exactly one of `route53_zone_id` (the module issues a DNS validated
certificate and creates an alias record) or `certificate_arn` (an existing
certificate; point your own DNS at `alb_dns_name`). A precondition on the
HTTPS listener enforces this.

The [examples](examples) cover a full deployment with a new VPC
([complete](examples/complete)), an existing VPC and certificate
([existing-vpc](examples/existing-vpc)), and a stack through which
Stackorder deploys and upgrades itself ([self-hosted](examples/self-hosted)).

## First deploy and the GitHub App

1. Apply with the `github_*` inputs unset. The server starts in setup mode
   and serves only `/setup`, `/healthz` and `/readyz`. The module runs one
   task while the App inputs are unset, whatever `desired_count` says, so
   there is a single setup token.
2. Take the setup URL from the task logs. `/setup` opens only with a
   one-time token, which the server logs with the URL at start-up:

   ```sh
   aws logs tail "$(terraform output -raw log_group_name)" --since 1d | grep setup_url
   ```

   The `setup_url` output is the same page without the token, which
   answers 403. The task generates a new token at every start, so take the
   latest line. The server logs the line once, when the task starts; to
   get a new one, restart the task:

   ```sh
   aws ecs update-service --cluster "$(terraform output -raw ecs_cluster_name)" \
     --service "$(terraform output -raw ecs_service_name)" --force-new-deployment
   ```

   With `log_level = "error"` the line is not logged; keep `info` or
   `warn` until the App exists, or set `STACKORDER_SETUP_TOKEN` through
   `extra_environment` and open `/setup?token=` followed by its value. A
   token set that way is plain text in the task definition and in state,
   so remove it once the App exists.
3. Open that URL. The page creates the GitHub App from a manifest with the
   right webhook URL (`webhook_url`), permissions and events, and prints
   the App id, private key, webhook secret and OAuth client id and secret
   once.
4. Apply again with those five values. The App id, private key and webhook
   secret must be set together, as must the two OAuth values.
   Setting them rewrites the app secret without a change to
   `secrets_version`.

If the image cannot be pulled anonymously from ghcr.io, the first plan
fails rather than the apply hanging on the ECS deployment; see
[Upgrades](#upgrades) for `verify_image`.

The module writes one Secrets Manager secret with the full `DATABASE_URL`
and one JSON secret with a key per App variable plus
`STACKORDER_SESSION_KEY` (generated when `session_key` is null) and
`STACKORDER_METRICS_TOKEN` (generated when `metrics_token` is null). ECS
injects them through `secrets` with `valueFrom = "<arn>:<KEY>::"`; nothing
secret is in the image or in the task definition. The task definition
carries the version ids of both secrets as docker labels, so changing a
secret value rolls the service onto it. A third secret holds a copy of the
metrics token alone, for scrapers (see [Metrics](#metrics)); the task does
not read it.

## Secrets and Terraform state

This section applies from the module release after v0.1.0. v0.1.0 keeps
these values in state and has no `secrets_version` or
`db_password_version`.

The module keeps the App private key, webhook secret and OAuth client
secret, the session key, the metrics token and the database password out
of Terraform state and saved plans, as long as the caller passes the
secret inputs as ephemeral values too. The three App secrets,
`session_key` and `metrics_token` are ephemeral inputs. The database
password, and the session key and metrics token when those inputs are
null, come from ephemeral `random_password` resources. They reach AWS
only through write-only attributes: `secret_string_wo` on the secret
versions and `password_wo` (RDS) or `master_password_wo` (Aurora) on the
database. State still describes the deployment, so keep it encrypted and
access controlled, but it holds none of these values.

Two inputs are exceptions and stay in state and in saved plans: the
`client_secret` of `oidc_authentication`, which the load balancer
listener keeps (see [Single sign-on](#single-sign-on-in-front-of-the-ui)),
and every value passed through `extra_environment`.

A write-only value is sent only when its version changes, so a changed
input does nothing until a version moves:

- `secrets_version` rewrites the app and metrics token secrets. Increase it
  after changing any ephemeral input. Each rewrite generates a new session
  key and metrics token unless `session_key` and `metrics_token` are set:
  everyone signs in again and scrapers must read the new token. Set both
  inputs to keep them stable.
- `db_password_version` generates a new database password and writes it to
  the database and to the `DATABASE_URL` secret in the same apply. Until
  the new tasks replace the old ones, the old tasks keep their open
  database connections but cannot open new ones.
- The module rewrites the app and metrics token secrets on its own when
  the App id or OAuth client id changes, including when the App inputs are
  first set, and when either secret is recreated. It writes a new database
  password and `DATABASE_URL` when the database or its secret is created or
  replaced.

The generated values are new on every run; only the secrets keep them. If
an apply that writes the database password or `DATABASE_URL` fails, do
not simply run it again: the retry would write the side that failed with
a different password than the side that succeeded. Increase
`db_password_version` and apply. Likewise, increase `secrets_version`
after a failed apply that writes the app or metrics token secret while
the metrics token is generated.

A saved plan does not carry ephemeral values. Supply the ephemeral inputs
when applying it as well as when planning, with `TF_VAR_*` variables or
`-var`, or read them in the calling configuration with an ephemeral
resource such as `ephemeral "aws_secretsmanager_secret_version"`, which is
read again at apply time, as the [self-hosted](examples/self-hosted)
example does. Terraform refuses to apply a saved plan when an ephemeral
input set at plan is missing. OpenTofu instead uses the input's default,
so a `session_key` or `metrics_token` omitted at apply makes the module
write new generated values in place of the chosen ones.

**Warning: under Stackorder, the App inputs reach plan jobs.** Stackorder
applies saved plans, so the ephemeral inputs must reach its plan jobs as
well as its applies, and the module requires `github_app_id`, the private
key and the webhook secret together at plan time. Plan jobs for a pull
request run that pull request's code, so anyone who can push a branch can
read the values, whether they come in as `TF_VAR_*` variables or through
an ephemeral read with the plan role. Passing them as `TF_VAR_*` in the
`env` secret of the reusable workflows also puts the App private key in a
repository or organization secret, and has two more problems: an `ENV`
environment secret replaces the whole `env` secret, so a `production`
`ENV` secret without them drops them from applies, and the `env` secret
needs a `stackorder/actions` release after v1.0.0. Read the App secrets
with an ephemeral resource instead, as the
[self-hosted](examples/self-hosted) example does, keep the stack in a
repository where only its operators can push branches, and see
[Provider credentials](https://docs.stackorder.io/operations/security-hardening#provider-credentials).

## Configuration passed to the server

| Variable | Source |
| --- | --- |
| `STACKORDER_BASE_URL` | `base_url`, or `https://<domain_name>` |
| `STACKORDER_LISTEN` | `:8080` |
| `GITHUB_API_URL` | `github_api_url` |
| `STACKORDER_OIDC_AUDIENCE` | `oidc_audience`, or the base URL |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | `required_workflow_ref`, when set |
| `STACKORDER_ARTIFACT_BUCKET` | the artifact bucket, when `artifact_bucket_enabled` |
| `STACKORDER_LOG_LEVEL` | `log_level` |
| `DATABASE_URL` | database secret, `postgres://stackorder:<password>@<endpoint>:5432/stackorder?sslmode=require` |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET` | app secret, when set |
| `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | app secret, when set |
| `STACKORDER_SESSION_KEY` | app secret |
| `STACKORDER_METRICS_TOKEN` | app secret, `metrics_token` or generated |

Anything else the server reads (`STACKORDER_WORKERS`, the retention
durations, `STACKORDER_LOG_FORMAT`, `OTEL_EXPORTER_OTLP_ENDPOINT`,
`GITHUB_OIDC_ISSUER`, `GITHUB_OIDC_JWKS_URL`) goes through
`extra_environment`, which rejects the names above.

`sslmode=require` encrypts the connection without verifying the server
certificate, because the distroless image carries only public CAs. The
parameter group sets `rds.force_ssl = 1`, so plaintext connections are
refused.

## Network and access

- Tasks run in private subnets without public IPs. With `create_vpc = true`
  the module builds a VPC across two zones (or `availability_zones`) with
  one NAT gateway, or one per zone with `single_nat_gateway = false`. With
  an existing VPC the private subnets need a NAT route: the tasks pull the
  image from ghcr.io and reach api.github.com, Secrets Manager and
  CloudWatch Logs over their public endpoints.
- `public_tasks = true` runs the tasks in the public subnets with public
  IPs instead, and a created VPC gets no NAT gateway or Elastic IP. For a
  single-user deployment the NAT gateway is the largest fixed cost after
  the database, while each task's public IPv4 address costs a fraction of
  it. The service security group still admits only the load balancer, so
  the public IPs serve outbound traffic only. The database stays in the
  private subnets, whose route table then has no default route; with an
  existing VPC, `private_subnet_ids` stays required for it.
- Security groups cannot filter by host name, so the service egress rule is
  443 to `0.0.0.0/0`; everything else is closed. For stricter egress, keep
  the tasks private and put a proxy or firewall on the NAT path.
- The load balancer's only egress is port 8080 to the tasks, plus 443 to
  `alb_https_egress_cidrs`, or to anywhere when single sign-on is set (see
  [Single sign-on in front of the UI](#single-sign-on-in-front-of-the-ui)).
- The load balancer accepts 80 and 443 from `ingress_cidrs`, by default
  anywhere, because GitHub delivers webhooks and GitHub-hosted runners call
  the API from large, changing ranges.
- `github_webhook_ip_ranges_only = true` replaces `ingress_cidrs` with the
  `hooks` ranges of the GitHub meta API (read at plan time from
  `github_api_url`, so GitHub Enterprise Server works) plus `admin_cidrs`.
  GitHub's Actions ranges are too many for a security group, so in this
  mode runners that post results must egress from `admin_cidrs`, which in
  practice means self-hosted runners or larger runners with static IPs.
  New hook ranges appear as a plan diff.
- Every path, `/metrics` included, is reachable through the load balancer.
  A load balancer path rule is not a reliable way to hide `/metrics`: the
  server's router decodes percent-escapes before matching, so an escaped
  spelling of the path can reach the handler without matching a rule on
  the literal path. The server itself requires the metrics bearer token
  instead; see [Metrics](#metrics).

## Single sign-on in front of the UI

`oidc_authentication` (any OpenID Connect provider) or
`cognito_authentication` (a Cognito user pool) makes the HTTPS listener
authenticate people before the server sees them: its default action
becomes `authenticate-oidc` or `authenticate-cognito`, then `forward`.
The UI, `/setup` and the server's own GitHub sign-in (`/auth/login` and
`/auth/callback`) all stay behind it, so a person signs in to the identity
provider first and to GitHub second. The server does not read the
load balancer's `x-amzn-oidc-*` headers; people still get their Stackorder
identity and permissions from GitHub.

Machines cannot sign in interactively, so the module adds listener rules
that forward without authentication:

| Priority | Path | Other conditions | Callers |
| --- | --- | --- | --- |
| 1 | `/webhooks/github` | `POST` | GitHub webhook deliveries, which the server verifies by their signature |
| 2 | `/healthz`, `/readyz` | `GET` or `HEAD` | Uptime checks and external monitoring |
| 3 | `/v1/runs`, `/v1/runs/*` | `Authorization: Bearer *` | Actions jobs with their OIDC token, and the CLI with an OIDC token or an API key |
| 4 | `/v1/unlock`, `/v1/me` | `Authorization: Bearer *` | The CLI with an OIDC token or an API key |
| 5 | `/metrics` | `GET`, `Authorization: Bearer *` | Prometheus with the metrics token |

These are the endpoints the CLI and runners call, and the server still
authenticates every request that reaches them: webhooks by their
signature, the others by their bearer token. The load balancer matches
rules after it normalises the path, and the server's router redirects a
path with `..` segments to its clean form, which then meets the rules
again. The UI's own calls to
`/v1` carry a session cookie rather than a bearer token, so they stay
behind the load balancer; scripts that call other `/v1` endpoints with an
API key need a rule of their own. The module keeps priorities 1 to 99 for
its rules: attach yours to the `https_listener_arn` output at 100 or
above, forwarding to `target_group_arn`.

The load balancer calls the identity provider's token and user info
endpoints itself, so it needs HTTPS egress, which its security group
otherwise lacks. Identity providers publish no fixed address ranges, so
with authentication set and `alb_https_egress_cidrs` empty the module
allows 443 to `0.0.0.0/0`; set `alb_https_egress_cidrs` to narrow that,
for example to a proxy or to a provider that does publish its ranges.

The AWS provider has no write-only argument for the OIDC client secret, so
the listener stores `client_secret` in Terraform state; keep state
encrypted and readable only by the roles that plan and apply this stack.

With Google Workspace, create an OAuth client of type "Web application"
whose authorised redirect URI is `https://<domain_name>/oauth2/idpresponse`,
the load balancer's fixed callback path, and set the OAuth consent
screen's user type to Internal. Only the Internal user type limits sign-in
to your Workspace: the `hd` parameter below only preselects the account.

```hcl
module "stackorder" {
  source = "github.com/stackorder/stackorder//deploy/terraform?ref=v0.1.0"

  domain_name     = "stackorder.acme.com"
  certificate_arn = "arn:aws:acm:eu-north-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
  image_tag       = "0.1.0"

  oidc_authentication = {
    issuer                 = "https://accounts.google.com"
    authorization_endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
    token_endpoint         = "https://oauth2.googleapis.com/token"
    user_info_endpoint     = "https://openidconnect.googleapis.com/v1/userinfo"
    client_id              = var.google_client_id
    client_secret          = var.google_client_secret
    scope                  = "openid email"

    authentication_request_extra_params = {
      hd = "acme.com"
    }
  }
}
```

## Load balancer access logs

`alb_access_logs_enabled = true` creates a bucket named
`<name>-alb-logs-<account>-<region>` and turns the load balancer's access
logs on. ELB log delivery accepts only SSE-S3, so this bucket ignores
`kms_key_arn`. Its policy lets only log delivery for this account's
`<name>` load balancer write, under `AWSLogs/<account>/`, and denies plain
HTTP; objects expire after `alb_access_logs_retention_days` (90). The logs
hold full request URLs, but no headers or bodies. The URLs include the
short-lived, single-use `code` parameters of the OAuth and App setup
callbacks and the setup token of `/setup?token=`, which opens setup until
an App is created. Treat read access to this bucket like read access to
the server's log while setup is open, or turn the logs on after the App
exists. Bearer tokens and webhook payloads stay out of them.

The module never deletes logs. Setting `alb_access_logs_enabled = false`
turns the load balancer's access logs off before the bucket goes, but the
bucket's deletion fails while it holds objects, and so does a destroy of
the module; empty the bucket and apply or destroy again.

## AWS WAF

`waf_web_acl_arn` associates an existing regional web ACL, in the module's
region, with the load balancer; the module does not create one. Most of
Stackorder's traffic is machine to machine, which rule groups written for
browsers handle badly:

- `SizeRestrictions_BODY` in `AWSManagedRulesCommonRuleSet` blocks bodies
  over 8 KB. GitHub webhook deliveries, graph uploads (up to 8 MB) and plan
  results (plan text up to 256 KB) routinely exceed that, so override the
  rule to Count or scope it down to exclude `/webhooks/github` and
  `/v1/runs/`.
- On a load balancer a web ACL inspects only the first 8 KB of a body, and
  plan text can look like cross-site scripting or path traversal to the
  `_BODY` rules. Run new rules in Count mode and read the sampled requests
  before blocking.
- Rate-based rules must leave room for bursts: a wave of plan jobs posts
  its results within seconds, and GitHub sends several webhooks for every
  push and pull request event.

## Metrics

`GET /metrics` always requires `Authorization: Bearer <token>`. The token
is `metrics_token`, or 32 random hexadecimal characters generated whenever
the app secret is written when it is null, and reaches the server as `STACKORDER_METRICS_TOKEN` through the app
secret. Because the app secret also holds the App private key, the module
writes a copy of the token alone, as plain text, to a second secret whose
ARN is the `metrics_token_secret_arn` output. Grant the scraper
`secretsmanager:GetSecretValue` on that secret only (and `kms:Decrypt` when
`kms_key_arn` is set), write the value to a file, and point Prometheus at
it:

```sh
aws secretsmanager get-secret-value --secret-id <metrics_token_secret_arn> \
  --query SecretString --output text > /etc/prometheus/stackorder-metrics-token
```

```yaml
scrape_configs:
  - job_name: stackorder
    scheme: https
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/stackorder-metrics-token
    static_configs:
      - targets: ["stackorder.example.com"]
```

A Prometheus Operator `ScrapeConfig` takes the token through
`authorization.credentials`, a key of a Kubernetes secret that an external
secrets controller can fill from `metrics_token_secret_arn`.

Through the load balancer each scrape reaches one task, so with
`desired_count = 2` successive samples of one series come from either
task. To keep the tasks apart, scrape them directly from inside the VPC;
that needs an ingress rule for port 8080 from the scraper on the service
security group (`security_group_ids.service`).

To rotate a generated token, increase `secrets_version`. The new app
secret version rolls the service, and the scraper must read the new value
from the metrics token secret. A generated session key changes too, unless
`session_key` is set.

## IAM

- The execution role has `AmazonECSTaskExecutionRolePolicy` plus
  `secretsmanager:GetSecretValue` on the database and app secrets, and
  `kms:Decrypt` through Secrets Manager when `kms_key_arn` is set. It
  cannot read the metrics token secret, which only scrapers need.
- The task role has no policy. `artifact_bucket_enabled` adds list, get,
  put and delete on that bucket only; `enable_execute_command` adds the
  `ssmmessages` actions ECS Exec needs.
- Both roles trust `ecs-tasks.amazonaws.com` for this account and region
  only (`aws:SourceAccount`, `aws:SourceArn`).

## Scaling out

Webhook handling, workers and the API are stateless, the scheduler is
single leader through a Postgres advisory lock, and sessions live in
Postgres, so `desired_count = 2` adds availability with no other change.
Nothing on local disk matters, which is why the root file system is
read-only. One 0.25 vCPU / 512 MiB task is sized for an organisation with a
few hundred stacks; raise `cpu` and `memory` before adding tasks, and
consider `db.t4g.small` or Aurora once the database is the bottleneck.

## Upgrades

Pin `image_tag` to a release (or a `sha256:` digest) so an upgrade is a
reviewed plan. The service keeps 100 % of its tasks healthy and may run
200 % during a deployment: the new task runs the migrations under a lock
at start-up and receives traffic only once it passes `/readyz`, while the
old task keeps serving. The deployment circuit breaker rolls back a task
that never becomes healthy, and `wait_for_steady_state` makes the apply
fail in that case.

ECS retries a failed image pull until the deployment times out, so a
private package or a tag that does not exist yet would hold an apply for
the whole timeout. `verify_image` (on by default) prevents that for images
on ghcr.io: the plan fetches an anonymous pull token and the manifest of
`image_tag`, as ECS would, and fails with an explanation unless ghcr.io
answers 200. The check needs HTTPS access to ghcr.io from wherever
Terraform runs; set `verify_image = false` to skip it, for example for a
mirror or a package you make public during the apply.
`deployment_timeout` (20m, the provider's default) bounds how long an
apply waits for a steady state before it fails.

An old task is stopped gracefully. ECS first deregisters it from the
target group and waits the 30 second deregistration delay, then sends
SIGTERM. The server stops accepting connections and gives in-flight
requests up to 15 seconds, then lets its workers finish for up to 30
seconds and cancelled handlers 5 more; claimed but unstarted jobs go back
to the queue. `stop_timeout_seconds` (60, at most 120 on Fargate) is how
long ECS waits after SIGTERM before it kills the container, which also
leaves room for the trace exporter's final flush.

Upgrading the module from v0.1.0, which kept the secrets in state, needs
Terraform or OpenTofu 1.11. In the [self-hosted](examples/self-hosted)
setup, first set the new `github_app_id` and `github_oauth_client_id`
variables of the stack, which it no longer reads from the App secret;
otherwise the server returns to setup mode. The first apply removes `random_password.db`,
`random_bytes.session_key` and `random_password.metrics_token` from state,
which touches nothing in AWS, writes a new database password to the
database and to `DATABASE_URL`, and rewrites the app and metrics token
secrets, so the service rolls. Until the new tasks replace the old ones,
the old tasks keep their open database connections but cannot open new
ones. Unless `session_key` and `metrics_token` are set, both are new:
everyone signs in again, and scrapers must read the new token. Older state
versions, for example in a versioned S3 bucket, still hold the values of
v0.1.0: the database password, session key and generated token in them no
longer work, but the App private key, webhook secret and OAuth client
secret stay valid until you rotate them in the App settings.

## Container health check

By default the container runs `/stackorder-server healthcheck` every 30
seconds, with a 5 second timeout, 3 retries and a 30 second start period.
The subcommand GETs `/healthz` on the listen port and exits 0 on 200, so it
needs neither a shell nor curl in the distroless image. ECS does not use the
image's own `HEALTHCHECK`; only the task definition's check counts, and a
container that fails it is replaced.

`/healthz` answers as long as the process serves HTTP and does not touch
the database, so the container check fails only when the process itself
stops answering. Readiness, database included, stays with the load
balancer's `/readyz` check, which gates deployments and drives the circuit
breaker. ECS also marks a task that fails the load balancer check
unhealthy and replaces it, so a database outage still makes ECS replace
tasks, whatever `health_check_command` is.

Set `health_check_command` to another command the image provides, or to
`[]` to turn the container health check off and leave task health to the
load balancer alone.

## ECS Exec

`enable_execute_command = true` turns on ECS Exec. Its SSM agent needs a
writable root file system, so this also sets `readonlyRootFilesystem` to
false; the distroless image has no shell, so it is mostly useful with a
debug build of the image.

## Database

The default is an encrypted `db.t4g.micro` PostgreSQL 17 instance with gp3
storage autoscaling from 20 to 100 GiB, 7 days of point-in-time recovery,
deletion protection and a final snapshot. `use_aurora_serverless = true`
switches to an Aurora PostgreSQL Serverless v2 cluster (0.5 to 2 ACU by
default, a reader in another zone with `multi_az`). A major-only
`engine_version` resolves to the AWS default minor of that major at plan
time, since Aurora does not document accepting a bare major; when AWS
moves the default, the plan shows the minor upgrade and RDS applies it in
the maintenance window. Set `major.minor` to pin it instead. Switching
between RDS and Aurora replaces the database.

Changes to the database wait for its maintenance window unless
`apply_immediately = true`, in which case changes that need a restart,
such as a new `instance_class`, cause a short outage during the apply.

A new major version in `engine_version` fails to apply until
`allow_major_version_upgrade = true`. A major upgrade cannot be rolled
back, so take a snapshot first, and set `apply_immediately = true` for the
same apply: the module creates a parameter group of the new family and
deletes the old one, which it can only do once the database has moved to
the new one. The database is unavailable while RDS upgrades it, so the
server fails `/readyz` and cannot record runs; in the
[self-hosted](examples/self-hosted) setup, run a major upgrade from a
workstation rather than through a Stackorder apply. Set both flags back to
false afterwards.

## Alarms

With `alarms_enabled` and `alarm_sns_topic_arn`, the module alarms on target
5xx responses, unhealthy targets, service CPU above 80 %, and RDS free
storage below 1 GiB (storage autoscaling should have prevented that) or,
for Aurora, whose storage grows on its own, ACU utilization above 90 %.
Thresholds are in `alarm_thresholds`.

## Testing

```sh
terraform init -backend=false
terraform test
```

The tests plan against mock AWS and HTTP providers and need no AWS account.
Mock providers cannot serve ephemeral resources, so the tests use the real
random provider and check the values written to the secrets through the
module's locals. They need Terraform 1.11.4 or later: 1.11.0 to 1.11.3
return values for write-only attributes from mock providers, which fails
the plans. CI validates the module and its examples, and runs the tests,
on the latest 1.11 and 1.14 releases. OpenTofu rejects the tests' mock
provider syntax at `init`, so run them with Terraform; to validate the
module with OpenTofu, copy it without the `tests` directory.

## Requirements

Terraform 1.11 or later, or OpenTofu 1.11 or later. The AWS provider must
be 6.50 or later: earlier 6.x releases fail the apply with "Provider
produced inconsistent final plan" when the database is replaced, because
the version of the `DATABASE_URL` secret is then unknown at plan time.

| Name | Version |
|------|---------|
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.11 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | >= 6.50, < 7.0 |
| <a name="requirement_http"></a> [http](#requirement\_http) | ~> 3.4 |
| <a name="requirement_random"></a> [random](#requirement\_random) | ~> 3.7 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | >= 6.50, < 7.0 |
| <a name="provider_http"></a> [http](#provider\_http) | ~> 3.4 |
| <a name="provider_random"></a> [random](#provider\_random) | ~> 3.7 |

## Modules

No modules.

## Resources

| Name | Type |
|------|------|
| [aws\_acm\_certificate.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/acm_certificate) | resource |
| [aws\_acm\_certificate\_validation.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/acm_certificate_validation) | resource |
| [aws\_cloudwatch\_log\_group.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_log_group) | resource |
| [aws\_cloudwatch\_metric\_alarm.cpu](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_metric_alarm) | resource |
| [aws\_cloudwatch\_metric\_alarm.db\_capacity](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_metric_alarm) | resource |
| [aws\_cloudwatch\_metric\_alarm.db\_free\_storage](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_metric_alarm) | resource |
| [aws\_cloudwatch\_metric\_alarm.target\_5xx](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_metric_alarm) | resource |
| [aws\_cloudwatch\_metric\_alarm.unhealthy\_hosts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_metric_alarm) | resource |
| [aws\_db\_instance.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/db_instance) | resource |
| [aws\_db\_parameter\_group.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/db_parameter_group) | resource |
| [aws\_db\_subnet\_group.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/db_subnet_group) | resource |
| [aws\_default\_security\_group.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/default_security_group) | resource |
| [aws\_ecs\_cluster.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/ecs_cluster) | resource |
| [aws\_ecs\_service.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/ecs_service) | resource |
| [aws\_ecs\_task\_definition.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/ecs_task_definition) | resource |
| [aws\_eip.nat](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/eip) | resource |
| [aws\_iam\_role.execution](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws\_iam\_role.task](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws\_iam\_role\_policy.execution](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws\_iam\_role\_policy.task\_artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws\_iam\_role\_policy.task\_execute\_command](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws\_iam\_role\_policy\_attachment.execution](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy_attachment) | resource |
| [aws\_internet\_gateway.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/internet_gateway) | resource |
| [aws\_lb.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb) | resource |
| [aws\_lb\_listener.http](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_listener) | resource |
| [aws\_lb\_listener.https](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_listener) | resource |
| [aws\_lb\_listener\_rule.bypass](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_listener_rule) | resource |
| [aws\_lb\_target\_group.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_target_group) | resource |
| [aws\_nat\_gateway.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/nat_gateway) | resource |
| [aws\_rds\_cluster.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/rds_cluster) | resource |
| [aws\_rds\_cluster\_instance.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/rds_cluster_instance) | resource |
| [aws\_rds\_cluster\_parameter\_group.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/rds_cluster_parameter_group) | resource |
| [aws\_route.private\_nat](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route) | resource |
| [aws\_route.public\_internet](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route) | resource |
| [aws\_route53\_record.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |
| [aws\_route53\_record.validation](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |
| [aws\_route\_table.private](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route_table) | resource |
| [aws\_route\_table.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route_table) | resource |
| [aws\_route\_table\_association.private](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route_table_association) | resource |
| [aws\_route\_table\_association.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route_table_association) | resource |
| [aws\_s3\_bucket.alb\_logs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws\_s3\_bucket.artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws\_s3\_bucket\_lifecycle\_configuration.alb\_logs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws\_s3\_bucket\_lifecycle\_configuration.artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws\_s3\_bucket\_ownership\_controls.alb\_logs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_ownership_controls) | resource |
| [aws\_s3\_bucket\_ownership\_controls.artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_ownership_controls) | resource |
| [aws\_s3\_bucket\_policy.alb\_logs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_policy) | resource |
| [aws\_s3\_bucket\_policy.artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_policy) | resource |
| [aws\_s3\_bucket\_public\_access\_block.alb\_logs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws\_s3\_bucket\_public\_access\_block.artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws\_s3\_bucket\_server\_side\_encryption\_configuration.alb\_logs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws\_s3\_bucket\_server\_side\_encryption\_configuration.artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws\_s3\_bucket\_versioning.artifacts](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_versioning) | resource |
| [aws\_secretsmanager\_secret.app](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/secretsmanager_secret) | resource |
| [aws\_secretsmanager\_secret.database](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/secretsmanager_secret) | resource |
| [aws\_secretsmanager\_secret.metrics\_token](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/secretsmanager_secret) | resource |
| [aws\_secretsmanager\_secret\_version.app](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/secretsmanager_secret_version) | resource |
| [aws\_secretsmanager\_secret\_version.database](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/secretsmanager_secret_version) | resource |
| [aws\_secretsmanager\_secret\_version.metrics\_token](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/secretsmanager_secret_version) | resource |
| [aws\_security\_group.alb](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws\_security\_group.db](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws\_security\_group.service](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws\_subnet.private](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/subnet) | resource |
| [aws\_subnet.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/subnet) | resource |
| [aws\_vpc.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc) | resource |
| [aws\_vpc\_security\_group\_egress\_rule.alb\_https](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc_security_group_egress_rule) | resource |
| [aws\_vpc\_security\_group\_egress\_rule.alb\_to\_service](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc_security_group_egress_rule) | resource |
| [aws\_vpc\_security\_group\_egress\_rule.service\_https](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc_security_group_egress_rule) | resource |
| [aws\_vpc\_security\_group\_egress\_rule.service\_to\_db](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc_security_group_egress_rule) | resource |
| [aws\_vpc\_security\_group\_ingress\_rule.alb](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc_security_group_ingress_rule) | resource |
| [aws\_vpc\_security\_group\_ingress\_rule.db\_from\_service](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc_security_group_ingress_rule) | resource |
| [aws\_vpc\_security\_group\_ingress\_rule.service\_from\_alb](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/vpc_security_group_ingress_rule) | resource |
| [aws\_wafv2\_web\_acl\_association.this](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/wafv2_web_acl_association) | resource |
| [random\_password.db](https://registry.terraform.io/providers/hashicorp/random/latest/docs/ephemeral-resources/password) | ephemeral resource |
| [random\_password.metrics\_token](https://registry.terraform.io/providers/hashicorp/random/latest/docs/ephemeral-resources/password) | ephemeral resource |
| [random\_password.session\_key](https://registry.terraform.io/providers/hashicorp/random/latest/docs/ephemeral-resources/password) | ephemeral resource |
| [data.aws\_availability\_zones.available](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/availability_zones) | data source |
| [data.aws\_caller\_identity.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/caller_identity) | data source |
| [data.aws\_partition.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/partition) | data source |
| [data.aws\_rds\_engine\_version.aurora](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/rds_engine_version) | data source |
| [data.aws\_region.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/region) | data source |
| [data.http.github\_meta](https://registry.terraform.io/providers/hashicorp/http/latest/docs/data-sources/http) | data source |
| [data.http.image\_manifest](https://registry.terraform.io/providers/hashicorp/http/latest/docs/data-sources/http) | data source |
| [data.http.image\_pull\_token](https://registry.terraform.io/providers/hashicorp/http/latest/docs/data-sources/http) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_name"></a> [name](#input\_name) | Name of the deployment, used as the name or prefix of every resource. Lowercase letters, digits and single hyphens, 2 to 24 characters. | `string` | `"stackorder"` | no |
| <a name="input_tags"></a> [tags](#input\_tags) | Tags added to every resource that supports them. | `map(string)` | `{}` | no |
| <a name="input_create_vpc"></a> [create\_vpc](#input\_create\_vpc) | Create a VPC with public and private subnets and, unless public\_tasks is true, NAT. When false, vpc\_id, public\_subnet\_ids and private\_subnet\_ids are required. | `bool` | `true` | no |
| <a name="input_vpc_cidr"></a> [vpc\_cidr](#input\_vpc\_cidr) | IPv4 CIDR of the VPC created when create\_vpc is true. Subnets are carved as eight equal blocks: public from the first four, private from the last four. | `string` | `"10.0.0.0/16"` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | ID of an existing VPC. Required when create\_vpc is false, must be null otherwise. | `string` | `null` | no |
| <a name="input_public_subnet_ids"></a> [public\_subnet\_ids](#input\_public\_subnet\_ids) | IDs of existing public subnets in at least two availability zones, for the load balancer and, when public\_tasks is true, the tasks. Required when create\_vpc is false. | `list(string)` | `[]` | no |
| <a name="input_private_subnet_ids"></a> [private\_subnet\_ids](#input\_private\_subnet\_ids) | IDs of existing private subnets in at least two availability zones, for the database and, unless public\_tasks is true, the tasks, which then need a route to the internet through NAT. Required when create\_vpc is false. | `list(string)` | `[]` | no |
| <a name="input_availability_zones"></a> [availability\_zones](#input\_availability\_zones) | Availability zones for the created VPC. Empty picks the first two available zones of the region. | `list(string)` | `[]` | no |
| <a name="input_single_nat_gateway"></a> [single\_nat\_gateway](#input\_single\_nat\_gateway) | Use one NAT gateway and route table for all private subnets instead of one per availability zone. Without NAT, when public\_tasks is true, it only sets the number of private route tables. | `bool` | `true` | no |
| <a name="input_public_tasks"></a> [public\_tasks](#input\_public\_tasks) | Run the tasks in the public subnets with public IP addresses instead of in the private subnets behind NAT, so a created VPC needs no NAT gateway. The service security group still admits only the load balancer; the database stays in the private subnets. | `bool` | `false` | no |
| <a name="input_domain_name"></a> [domain\_name](#input\_domain\_name) | Fully qualified host name the server is reached at, such as stackorder.example.com. | `string` | n/a | yes |
| <a name="input_route53_zone_id"></a> [route53\_zone\_id](#input\_route53\_zone\_id) | Route53 hosted zone in which to create the ACM validation records and the alias record for domain\_name. Exactly one of route53\_zone\_id and certificate\_arn must be set. | `string` | `null` | no |
| <a name="input_certificate_arn"></a> [certificate\_arn](#input\_certificate\_arn) | ARN of an existing ACM certificate covering domain\_name. DNS for domain\_name is then left to the caller. Exactly one of route53\_zone\_id and certificate\_arn must be set. | `string` | `null` | no |
| <a name="input_base_url"></a> [base\_url](#input\_base\_url) | Public URL of the server when it differs from https://&lt;domain\_name&gt;, for example behind another proxy. No trailing slash. | `string` | `null` | no |
| <a name="input_ssl_policy"></a> [ssl\_policy](#input\_ssl\_policy) | Security policy of the HTTPS listener. Must be a TLS 1.3 policy. | `string` | `"ELBSecurityPolicy-TLS13-1-2-2021-06"` | no |
| <a name="input_ingress_cidrs"></a> [ingress\_cidrs](#input\_ingress\_cidrs) | IPv4 or IPv6 CIDRs allowed to reach the load balancer on ports 80 and 443. GitHub webhooks and GitHub-hosted runners need the default. Ignored when github\_webhook\_ip\_ranges\_only is true. | `list(string)` | `["0.0.0.0/0"]` | no |
| <a name="input_github_webhook_ip_ranges_only"></a> [github\_webhook\_ip\_ranges\_only](#input\_github\_webhook\_ip\_ranges\_only) | Restrict the load balancer to GitHub's webhook source ranges (the hooks list of the GitHub meta API, read at plan time) plus admin\_cidrs, instead of ingress\_cidrs. | `bool` | `false` | no |
| <a name="input_admin_cidrs"></a> [admin\_cidrs](#input\_admin\_cidrs) | CIDRs of people and self-hosted runners that need the UI and API when github\_webhook\_ip\_ranges\_only is true. | `list(string)` | `[]` | no |
| <a name="input_alb_access_logs_enabled"></a> [alb\_access\_logs\_enabled](#input\_alb\_access\_logs\_enabled) | Write load balancer access logs to an S3 bucket the module creates, encrypted with SSE-S3 as ELB log delivery requires. | `bool` | `false` | no |
| <a name="input_alb_access_logs_retention_days"></a> [alb\_access\_logs\_retention\_days](#input\_alb\_access\_logs\_retention\_days) | Days after which objects in the access log bucket expire. | `number` | `90` | no |
| <a name="input_waf_web_acl_arn"></a> [waf\_web\_acl\_arn](#input\_waf\_web\_acl\_arn) | ARN of a regional AWS WAFv2 web ACL in the module's region to associate with the load balancer. Null associates none. | `string` | `null` | no |
| <a name="input_oidc_authentication"></a> [oidc\_authentication](#input\_oidc\_authentication) | OpenID Connect provider with which the load balancer authenticates people before forwarding, as the authenticate\_oidc action of the HTTPS listener. Webhooks, health checks, and runner, CLI and metrics requests that carry a bearer token bypass it. The listener stores client\_secret in Terraform state. Null authenticates nobody at the load balancer. Sensitive. | `object({ issuer = string authorization_endpoint = string token_endpoint = string user_info_endpoint = string client_id = string client_secret = string scope = optional(string) session_cookie_name = optional(string) session_timeout = optional(number) on_unauthenticated_request = optional(string) authentication_request_extra_params = optional(map(string)) })` | `null` | no |
| <a name="input_cognito_authentication"></a> [cognito\_authentication](#input\_cognito\_authentication) | Amazon Cognito user pool with which the load balancer authenticates people before forwarding, as the authenticate\_cognito action of the HTTPS listener, with the same bypass as oidc\_authentication. At most one of oidc\_authentication and cognito\_authentication may be set. Null authenticates nobody at the load balancer. | `object({ user_pool_arn = string user_pool_client_id = string user_pool_domain = string scope = optional(string) session_cookie_name = optional(string) session_timeout = optional(number) on_unauthenticated_request = optional(string) authentication_request_extra_params = optional(map(string)) })` | `null` | no |
| <a name="input_alb_https_egress_cidrs"></a> [alb\_https\_egress\_cidrs](#input\_alb\_https\_egress\_cidrs) | IPv4 CIDRs the load balancer may reach on port 443, as it must to reach the identity provider of oidc\_authentication or cognito\_authentication. Empty allows none, or 0.0.0.0/0 when either authentication is set, because identity providers publish no fixed address ranges. | `list(string)` | `[]` | no |
| <a name="input_image"></a> [image](#input\_image) | Container image repository of the server. | `string` | `"ghcr.io/stackorder/stackorder"` | no |
| <a name="input_image_tag"></a> [image\_tag](#input\_image\_tag) | Tag or digest (sha256:...) of the server image. Pin a release such as 1.2.3 so upgrades are explicit plans. | `string` | `"latest"` | no |
| <a name="input_verify_image"></a> [verify\_image](#input\_verify\_image) | When image is on ghcr.io, check at plan time that image\_tag can be pulled anonymously, as ECS pulls it, and fail the plan if not, instead of letting ECS retry the pull until the deployment times out. Needs HTTPS access to ghcr.io from where Terraform runs. | `bool` | `true` | no |
| <a name="input_desired_count"></a> [desired\_count](#input\_desired\_count) | Number of server tasks, but one task while the GitHub App inputs are unset, so there is a single setup token. All coordination goes through Postgres, so a second task adds availability without any other change. | `number` | `1` | no |
| <a name="input_cpu"></a> [cpu](#input\_cpu) | Fargate task CPU units. | `number` | `256` | no |
| <a name="input_memory"></a> [memory](#input\_memory) | Fargate task memory in MiB; must be a valid combination with cpu. | `number` | `512` | no |
| <a name="input_cpu_architecture"></a> [cpu\_architecture](#input\_cpu\_architecture) | CPU architecture of the task, X86\_64 or ARM64. | `string` | `"X86_64"` | no |
| <a name="input_enable_execute_command"></a> [enable\_execute\_command](#input\_enable\_execute\_command) | Enable ECS Exec. The SSM agent needs a writable root file system, so this also turns readonlyRootFilesystem off and grants the task role the ssmmessages permissions. | `bool` | `false` | no |
| <a name="input_health_check_command"></a> [health\_check\_command](#input\_health\_check\_command) | Container health check command, starting with CMD or CMD-SHELL. The default runs the server's healthcheck subcommand, which GETs /healthz on the listen port. The distroless image has no shell or curl, so a replacement must be a command the image itself provides. Empty turns the container health check off and leaves task health to the load balancer check on /readyz. | `list(string)` | `["CMD", "/stackorder-server", "healthcheck"]` | no |
| <a name="input_stop_timeout_seconds"></a> [stop\_timeout\_seconds](#input\_stop\_timeout\_seconds) | Seconds ECS waits after SIGTERM before it kills the container (stopTimeout), 2 to 120 on Fargate. The server drains HTTP for up to 15 s, then its workers for up to 30 s plus 5 s for cancelled handlers, so a value under 50 can cut the drain short. | `number` | `60` | no |
| <a name="input_wait_for_steady_state"></a> [wait\_for\_steady\_state](#input\_wait\_for\_steady\_state) | Make terraform apply wait until the new tasks pass /readyz, so an apply of an upgrade fails when the deployment rolls back. | `bool` | `true` | no |
| <a name="input_deployment_timeout"></a> [deployment\_timeout](#input\_deployment\_timeout) | How long terraform apply waits for the service to reach a steady state when wait\_for\_steady\_state is true, as the create and update timeout of the ECS service, such as 20m or 1h. | `string` | `"20m"` | no |
| <a name="input_log_retention_days"></a> [log\_retention\_days](#input\_log\_retention\_days) | Retention of the server log group in days. | `number` | `30` | no |
| <a name="input_log_level"></a> [log\_level](#input\_log\_level) | Server log level (STACKORDER\_LOG\_LEVEL). | `string` | `"info"` | no |
| <a name="input_extra_environment"></a> [extra\_environment](#input\_extra\_environment) | Additional environment variables for the server, such as STACKORDER\_WORKERS or OTEL\_EXPORTER\_OTLP\_ENDPOINT. Variables the module sets itself are rejected. | `map(string)` | `{}` | no |
| <a name="input_engine_version"></a> [engine\_version](#input\_engine\_version) | PostgreSQL major version, or major.minor. For Aurora a major version resolves to the AWS default minor of that major at plan time. | `string` | `"17"` | no |
| <a name="input_allow_major_version_upgrade"></a> [allow\_major\_version\_upgrade](#input\_allow\_major\_version\_upgrade) | Allow a new major version in engine\_version to upgrade the RDS instance or Aurora cluster in place. A major upgrade cannot be rolled back; take a snapshot first and set apply\_immediately for the same apply. | `bool` | `false` | no |
| <a name="input_apply_immediately"></a> [apply\_immediately](#input\_apply\_immediately) | Apply database changes, such as engine\_version, instance\_class or the parameter group, at once instead of in the next maintenance window. Changes that need a restart then cause a short outage. | `bool` | `false` | no |
| <a name="input_instance_class"></a> [instance\_class](#input\_instance\_class) | RDS instance class. Ignored when use\_aurora\_serverless is true. | `string` | `"db.t4g.micro"` | no |
| <a name="input_allocated_storage"></a> [allocated\_storage](#input\_allocated\_storage) | Initial RDS storage in GiB. Ignored when use\_aurora\_serverless is true. | `number` | `20` | no |
| <a name="input_max_allocated_storage"></a> [max\_allocated\_storage](#input\_max\_allocated\_storage) | Upper bound for RDS storage autoscaling in GiB; 0 disables autoscaling. Ignored when use\_aurora\_serverless is true. | `number` | `100` | no |
| <a name="input_multi_az"></a> [multi\_az](#input\_multi\_az) | Run the RDS instance Multi-AZ, or add an Aurora reader in another zone. | `bool` | `false` | no |
| <a name="input_deletion_protection"></a> [deletion\_protection](#input\_deletion\_protection) | Protect the database from deletion. | `bool` | `true` | no |
| <a name="input_skip_final_snapshot"></a> [skip\_final\_snapshot](#input\_skip\_final\_snapshot) | Skip the final database snapshot on destroy. Keep false outside of throwaway environments. | `bool` | `false` | no |
| <a name="input_backup_retention_days"></a> [backup\_retention\_days](#input\_backup\_retention\_days) | Automated backup retention in days; point-in-time recovery covers this window. | `number` | `7` | no |
| <a name="input_performance_insights"></a> [performance\_insights](#input\_performance\_insights) | Enable Performance Insights with the free 7 day retention. Not every instance class supports it. | `bool` | `false` | no |
| <a name="input_use_aurora_serverless"></a> [use\_aurora\_serverless](#input\_use\_aurora\_serverless) | Use an Aurora PostgreSQL Serverless v2 cluster instead of an RDS instance. Switching an existing deployment replaces the database. | `bool` | `false` | no |
| <a name="input_aurora_min_acu"></a> [aurora\_min\_acu](#input\_aurora\_min\_acu) | Minimum Aurora Serverless v2 capacity in ACUs. | `number` | `0.5` | no |
| <a name="input_aurora_max_acu"></a> [aurora\_max\_acu](#input\_aurora\_max\_acu) | Maximum Aurora Serverless v2 capacity in ACUs. | `number` | `2` | no |
| <a name="input_kms_key_arn"></a> [kms\_key\_arn](#input\_kms\_key\_arn) | Customer managed KMS key for the Secrets Manager secrets and database storage. Null uses the AWS managed keys. | `string` | `null` | no |
| <a name="input_github_app_id"></a> [github\_app\_id](#input\_github\_app\_id) | GitHub App id (GITHUB\_APP\_ID). Leave the App inputs null on the first deploy: the server starts in setup mode and /setup creates the App. | `string` | `null` | no |
| <a name="input_github_app_private_key"></a> [github\_app\_private\_key](#input\_github\_app\_private\_key) | PEM private key of the GitHub App (GITHUB\_APP\_PRIVATE\_KEY). Ephemeral and sensitive. | `string` | `null` | no |
| <a name="input_github_webhook_secret"></a> [github\_webhook\_secret](#input\_github\_webhook\_secret) | Webhook secret of the GitHub App (GITHUB\_WEBHOOK\_SECRET). Ephemeral and sensitive. | `string` | `null` | no |
| <a name="input_github_oauth_client_id"></a> [github\_oauth\_client\_id](#input\_github\_oauth\_client\_id) | OAuth client id of the GitHub App, for human sign-in (GITHUB\_OAUTH\_CLIENT\_ID). | `string` | `null` | no |
| <a name="input_github_oauth_client_secret"></a> [github\_oauth\_client\_secret](#input\_github\_oauth\_client\_secret) | OAuth client secret of the GitHub App (GITHUB\_OAUTH\_CLIENT\_SECRET). Ephemeral and sensitive. | `string` | `null` | no |
| <a name="input_session_key"></a> [session\_key](#input\_session\_key) | 32 byte hex key for cookie signing (STACKORDER\_SESSION\_KEY). Null generates a new one whenever the app secret is written, which ends every session. Ephemeral and sensitive. | `string` | `null` | no |
| <a name="input_metrics_token"></a> [metrics\_token](#input\_metrics\_token) | Bearer token that GET /metrics requires (STACKORDER\_METRICS\_TOKEN), at least 16 printable ASCII characters without white space. Null generates 32 hexadecimal characters whenever the app secret is written. Ephemeral and sensitive. | `string` | `null` | no |
| <a name="input_db_password_version"></a> [db\_password\_version](#input\_db\_password\_version) | Version of the generated database password. Increase it to rotate the password: the database and the DATABASE\_URL secret get the new one in the same apply, and the service rolls. | `number` | `1` | no |
| <a name="input_secrets_version"></a> [secrets\_version](#input\_secrets\_version) | Version of the app and metrics token secrets. Increase it after changing an ephemeral input, or to generate a new session key and metrics token; both secrets are rewritten and the service rolls. | `number` | `1` | no |
| <a name="input_secret_recovery_window_days"></a> [secret\_recovery\_window\_days](#input\_secret\_recovery\_window\_days) | Days Secrets Manager keeps a deleted secret recoverable; 0 deletes immediately. | `number` | `30` | no |
| <a name="input_github_api_url"></a> [github\_api\_url](#input\_github\_api\_url) | GitHub API base URL (GITHUB\_API\_URL); GitHub Enterprise Server uses https://&lt;host&gt;/api/v3. | `string` | `"https://api.github.com"` | no |
| <a name="input_required_workflow_ref"></a> [required\_workflow\_ref](#input\_required\_workflow\_ref) | Glob that runner tokens' job\_workflow\_ref must match (STACKORDER\_REQUIRED\_WORKFLOW\_REF), such as stackorder/actions/.github/workflows/\*.yml@refs/tags/v1\*. Null accepts any workflow. | `string` | `null` | no |
| <a name="input_oidc_audience"></a> [oidc\_audience](#input\_oidc\_audience) | Audience runner OIDC tokens must carry (STACKORDER\_OIDC\_AUDIENCE). Null uses the public URL. | `string` | `null` | no |
| <a name="input_artifact_bucket_enabled"></a> [artifact\_bucket\_enabled](#input\_artifact\_bucket\_enabled) | Create an S3 bucket for full plan text (STACKORDER\_ARTIFACT\_BUCKET) and grant the task role access to it. This is the only AWS permission the task role ever gets. | `bool` | `false` | no |
| <a name="input_artifact_retention_days"></a> [artifact\_retention\_days](#input\_artifact\_retention\_days) | Days after which objects in the artifact bucket expire. | `number` | `90` | no |
| <a name="input_alarms_enabled"></a> [alarms\_enabled](#input\_alarms\_enabled) | Create CloudWatch alarms for target 5xx responses, unhealthy targets and service CPU, plus database free storage (RDS) or ACU utilization (Aurora, whose storage grows on its own). | `bool` | `false` | no |
| <a name="input_alarm_sns_topic_arn"></a> [alarm\_sns\_topic\_arn](#input\_alarm\_sns\_topic\_arn) | SNS topic notified when an alarm changes state. Required when alarms\_enabled is true. | `string` | `null` | no |
| <a name="input_alarm_thresholds"></a> [alarm\_thresholds](#input\_alarm\_thresholds) | Alarm thresholds: target 5xx responses per 5 minutes, average service CPU percent, RDS free storage in bytes, and Aurora ACU utilization percent. | `object({ target_5xx_count = optional(number, 10) cpu_percent = optional(number, 80) db_free_storage_bytes = optional(number, 1073741824) db_acu_utilization_percent = optional(number, 90) })` | `{}` | no |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_url"></a> [url](#output\_url) | Public URL of the server (STACKORDER\_BASE\_URL). |
| <a name="output_alb_dns_name"></a> [alb\_dns\_name](#output\_alb\_dns\_name) | DNS name of the load balancer; point a CNAME or alias here when DNS is not managed by the module. |
| <a name="output_alb_zone_id"></a> [alb\_zone\_id](#output\_alb\_zone\_id) | Route53 zone id of the load balancer, for alias records. |
| <a name="output_setup_url"></a> [setup\_url](#output\_setup\_url) | Page that creates the GitHub App from a manifest on the first deploy. It opens only with the one-time token the server logs at start-up: take the full URL from the setup\_url line in the log\_group\_name log group. |
| <a name="output_webhook_url"></a> [webhook\_url](#output\_webhook\_url) | Webhook URL of the GitHub App. |
| <a name="output_ecs_cluster_name"></a> [ecs\_cluster\_name](#output\_ecs\_cluster\_name) | Name of the ECS cluster. |
| <a name="output_ecs_service_name"></a> [ecs\_service\_name](#output\_ecs\_service\_name) | Name of the ECS service. |
| <a name="output_task_definition_arn"></a> [task\_definition\_arn](#output\_task\_definition\_arn) | ARN of the current task definition revision. |
| <a name="output_db_endpoint"></a> [db\_endpoint](#output\_db\_endpoint) | Host name of the database writer endpoint. |
| <a name="output_db_secret_arn"></a> [db\_secret\_arn](#output\_db\_secret\_arn) | ARN of the Secrets Manager secret holding DATABASE\_URL. |
| <a name="output_app_secret_arn"></a> [app\_secret\_arn](#output\_app\_secret\_arn) | ARN of the Secrets Manager secret holding the GitHub App credentials, the session key and the metrics token as JSON. |
| <a name="output_metrics_token_secret_arn"></a> [metrics\_token\_secret\_arn](#output\_metrics\_token\_secret\_arn) | ARN of the Secrets Manager secret holding only the /metrics bearer token, as plain text; grant Prometheus read access to this one rather than to the app secret. |
| <a name="output_artifact_bucket"></a> [artifact\_bucket](#output\_artifact\_bucket) | Name of the artifact bucket, or null when artifact\_bucket\_enabled is false. |
| <a name="output_alb_access_logs_bucket"></a> [alb\_access\_logs\_bucket](#output\_alb\_access\_logs\_bucket) | Name of the load balancer access log bucket, or null when alb\_access\_logs\_enabled is false. |
| <a name="output_security_group_ids"></a> [security\_group\_ids](#output\_security\_group\_ids) | Security group ids of the load balancer, the service and the database. |
| <a name="output_alb_arn"></a> [alb\_arn](#output\_alb\_arn) | ARN of the load balancer. |
| <a name="output_https_listener_arn"></a> [https\_listener\_arn](#output\_https\_listener\_arn) | ARN of the HTTPS listener, for listener rules of your own at priority 100 or above; the module keeps priorities 1 to 99 for its rules. |
| <a name="output_http_listener_arn"></a> [http\_listener\_arn](#output\_http\_listener\_arn) | ARN of the HTTP listener, which redirects to HTTPS. |
| <a name="output_target_group_arn"></a> [target\_group\_arn](#output\_target\_group\_arn) | ARN of the target group of the server's tasks, for listener rules that forward to the server. |
| <a name="output_log_group_name"></a> [log\_group\_name](#output\_log\_group\_name) | CloudWatch log group of the server. |
