---
description: 'Deploy the Stackorder server on AWS with the Terraform module in deploy/terraform: ECS Fargate behind an ALB, RDS or Aurora PostgreSQL and Secrets Manager.'
---

# Deploy on AWS

The repository ships a Terraform module in `deploy/terraform` that deploys the server on ECS Fargate behind an Application Load Balancer, with PostgreSQL on RDS or Aurora Serverless v2. It works with Terraform 1.11 or later and OpenTofu 1.11 or later, and Stackorder uses it to deploy itself, so it is also a working example of a stack Stackorder can manage.

```hcl
module "stackorder" {
  source = "github.com/stackorder/stackorder//deploy/terraform?ref=v0.1.0"

  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
  image_tag       = "0.1.0"
}
```

Set exactly one of `route53_zone_id`, to have the module issue a DNS-validated certificate and create the alias record, or `certificate_arn`, to bring a certificate and point your own DNS at `alb_dns_name`. `deploy/terraform/examples/` has three complete calls: `complete` (a new VPC, the artifact bucket and alarms), `existing-vpc` (your VPC and certificate) and `self-hosted` (the stack through which Stackorder deploys and upgrades itself).

## What the module creates {#resources}

| Resource | Purpose |
| --- | --- |
| VPC with public and private subnets and NAT, unless `create_vpc = false` | Two availability zones by default; one NAT gateway unless `single_nat_gateway = false`, and none with `public_tasks = true` |
| Application Load Balancer, HTTPS listener with TLS 1.3, HTTP redirect | Forwards to port 8080; health checks on `/readyz`; 30 s deregistration delay; access logs to an S3 bucket (`alb_access_logs_enabled`), a WAF web ACL (`waf_web_acl_arn`) and single sign-on (`oidc_authentication` or `cognito_authentication`) are optional |
| ACM certificate and Route53 records, with `route53_zone_id` | The certificate and alias for `domain_name` |
| ECS cluster and Fargate service, 1 or 2 tasks | Runs `ghcr.io/stackorder/stackorder` as user `65532`, read-only root file system, with the deployment circuit breaker; the container health check runs `stackorder-server healthcheck` |
| RDS PostgreSQL 17 (`db.t4g.micro`, gp3, encrypted), or Aurora Serverless v2 | The database, with `rds.force_ssl = 1`, 7 days of point-in-time recovery, deletion protection and a final snapshot |
| Three Secrets Manager secrets | `DATABASE_URL`; a JSON secret with the App credentials, `STACKORDER_SESSION_KEY` and `STACKORDER_METRICS_TOKEN`, injected through ECS `secrets`; and a copy of the metrics token for scrapers |
| Execution role and task role | The task role has no permissions unless the artifact bucket or ECS Exec is enabled |
| Security groups | Load balancer: 80 and 443 from `ingress_cidrs`, 8080 to the tasks, and 443 out to `alb_https_egress_cidrs`, or anywhere with single sign-on. Tasks: 8080 from the load balancer, 443 out, Postgres to the database. Database: Postgres from the tasks |
| CloudWatch log group | The server's logs, 30 days by default |
| Artifact bucket, with `artifact_bucket_enabled` | Full plan text, expiring after `artifact_retention_days` |
| CloudWatch alarms, with `alarms_enabled` | Target 5xx, unhealthy targets, CPU, and database free storage or ACU utilization |

The server needs nothing else. It holds no AWS credentials for your infrastructure.

Security groups filter by address, not by host name, so the tasks' egress is HTTPS to anywhere, through NAT: the tasks pull the image from ghcr.io and reach the GitHub API, GitHub's OIDC keys, Secrets Manager and CloudWatch Logs over their public endpoints. For stricter egress, put a proxy or firewall on the NAT path.

For a single-user deployment the NAT gateway is the largest fixed cost after the database, so `public_tasks = true` runs the tasks in the public subnets with public IP addresses instead, and a created VPC gets no NAT gateway. The service security group still admits only the load balancer, so nothing can connect to the tasks' public addresses, and the database stays in the private subnets. With an existing VPC the tasks then use `public_subnet_ids`, and `private_subnet_ids` stays required for the database.

## Inputs {#inputs}

The tables follow the descriptions in `deploy/terraform/variables.tf` and `deploy/terraform/outputs.tf`, and `docs/test` fails when they drift apart. Every input also has validation rules, which the descriptions summarise.

### Naming

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | `string` | `"stackorder"` | Name of the deployment, used as the name or prefix of every resource. Lowercase letters, digits and single hyphens, 2 to 24 characters. |
| `tags` | `map(string)` | `{}` | Tags added to every resource that supports them. |

### Network

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `create_vpc` | `bool` | `true` | Create a VPC with public and private subnets and, unless public_tasks is true, NAT. When false, vpc_id, public_subnet_ids and private_subnet_ids are required. |
| `vpc_cidr` | `string` | `"10.0.0.0/16"` | IPv4 CIDR of the VPC created when create_vpc is true. Subnets are carved as eight equal blocks: public from the first four, private from the last four. |
| `vpc_id` | `string` | `null` | ID of an existing VPC. Required when create_vpc is false, must be null otherwise. |
| `public_subnet_ids` | `list(string)` | `[]` | IDs of existing public subnets in at least two availability zones, for the load balancer and, when public_tasks is true, the tasks. Required when create_vpc is false. |
| `private_subnet_ids` | `list(string)` | `[]` | IDs of existing private subnets in at least two availability zones, for the database and, unless public_tasks is true, the tasks, which then need a route to the internet through NAT. Required when create_vpc is false. |
| `availability_zones` | `list(string)` | `[]` | Availability zones for the created VPC. Empty picks the first two available zones of the region. |
| `single_nat_gateway` | `bool` | `true` | Use one NAT gateway and route table for all private subnets instead of one per availability zone. Without NAT, when public_tasks is true, it only sets the number of private route tables. |
| `public_tasks` | `bool` | `false` | Run the tasks in the public subnets with public IP addresses instead of in the private subnets behind NAT, so a created VPC needs no NAT gateway. The service security group still admits only the load balancer; the database stays in the private subnets. |

### Name, TLS and ingress

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `domain_name` | `string` | required | Fully qualified host name the server is reached at, such as stackorder.example.com. |
| `route53_zone_id` | `string` | `null` | Route53 hosted zone in which to create the ACM validation records and the alias record for domain_name. Exactly one of route53_zone_id and certificate_arn must be set. |
| `certificate_arn` | `string` | `null` | ARN of an existing ACM certificate covering domain_name. DNS for domain_name is then left to the caller. Exactly one of route53_zone_id and certificate_arn must be set. |
| `base_url` | `string` | `null` | Public URL of the server when it differs from `https://<domain_name>`, for example behind another proxy. No trailing slash. |
| `ssl_policy` | `string` | `"ELBSecurityPolicy-TLS13-1-2-2021-06"` | Security policy of the HTTPS listener. Must be a TLS 1.3 policy. |
| `ingress_cidrs` | `list(string)` | `["0.0.0.0/0"]` | IPv4 or IPv6 CIDRs allowed to reach the load balancer on ports 80 and 443. GitHub webhooks and GitHub-hosted runners need the default. Ignored when github_webhook_ip_ranges_only is true. |
| `github_webhook_ip_ranges_only` | `bool` | `false` | Restrict the load balancer to GitHub's webhook source ranges (the hooks list of the GitHub meta API, read at plan time) plus admin_cidrs, instead of ingress_cidrs. |
| `admin_cidrs` | `list(string)` | `[]` | CIDRs of people and self-hosted runners that need the UI and API when github_webhook_ip_ranges_only is true. |
| `alb_access_logs_enabled` | `bool` | `false` | Write load balancer access logs to an S3 bucket the module creates, encrypted with SSE-S3 as ELB log delivery requires. |
| `alb_access_logs_retention_days` | `number` | `90` | Days after which objects in the access log bucket expire. |
| `waf_web_acl_arn` | `string` | `null` | ARN of a regional AWS WAFv2 web ACL in the module's region to associate with the load balancer. Null associates none. |
| `oidc_authentication` | `object` (sensitive) | `null` | OpenID Connect provider with which the load balancer authenticates people before forwarding, as the authenticate_oidc action of the HTTPS listener. Webhooks, health checks, and runner, CLI and metrics requests that carry a bearer token bypass it. The listener stores client_secret in Terraform state. Null authenticates nobody at the load balancer. |
| `cognito_authentication` | `object` | `null` | Amazon Cognito user pool with which the load balancer authenticates people before forwarding, as the authenticate_cognito action of the HTTPS listener, with the same bypass as oidc_authentication. At most one of oidc_authentication and cognito_authentication may be set. Null authenticates nobody at the load balancer. |
| `alb_https_egress_cidrs` | `list(string)` | `[]` | IPv4 or IPv6 CIDRs the load balancer may reach on port 443, as it must to reach the identity provider of oidc_authentication or cognito_authentication. Empty allows none, or 0.0.0.0/0 when either authentication is set, because identity providers publish no fixed address ranges. |

### Service

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `image` | `string` | `"ghcr.io/stackorder/stackorder"` | Container image repository of the server. |
| `image_tag` | `string` | `"latest"` | Tag or digest (sha256:...) of the server image. Pin a release such as 1.2.3 so upgrades are explicit plans. |
| `verify_image` | `bool` | `true` | When image is on ghcr.io, check at plan time that image_tag can be pulled anonymously, as ECS pulls it, and fail the plan if not, instead of letting ECS retry the pull until the deployment times out. Needs HTTPS access to ghcr.io from where Terraform runs. |
| `desired_count` | `number` | `1` | Number of server tasks, but one task while the GitHub App inputs are unset, so there is a single setup token. All coordination goes through Postgres, so a second task adds availability without any other change. |
| `cpu` | `number` | `256` | Fargate task CPU units. |
| `memory` | `number` | `512` | Fargate task memory in MiB; must be a valid combination with cpu. |
| `cpu_architecture` | `string` | `"X86_64"` | CPU architecture of the task, X86_64 or ARM64. |
| `enable_execute_command` | `bool` | `false` | Enable ECS Exec. The SSM agent needs a writable root file system, so this also turns readonlyRootFilesystem off and grants the task role the ssmmessages permissions. |
| `health_check_command` | `list(string)` | `["CMD", "/stackorder-server", "healthcheck"]` | Container health check command, starting with CMD or CMD-SHELL. The default runs the server's healthcheck subcommand, which GETs /healthz on the listen port. The distroless image has no shell or curl, so a replacement must be a command the image itself provides. Empty turns the container health check off and leaves task health to the load balancer check on /readyz. |
| `stop_timeout_seconds` | `number` | `60` | Seconds ECS waits after SIGTERM before it kills the container (stopTimeout), 2 to 120 on Fargate. The server drains HTTP for up to 15 s, then its workers for up to 30 s plus 5 s for cancelled handlers, so a value under 50 can cut the drain short. |
| `wait_for_steady_state` | `bool` | `true` | Make terraform apply wait until the new tasks pass /readyz, so an apply of an upgrade fails when the deployment rolls back. |
| `deployment_timeout` | `string` | `"20m"` | How long terraform apply waits for the service to reach a steady state when wait_for_steady_state is true, as the create and update timeout of the ECS service, such as 20m or 1h. |
| `log_retention_days` | `number` | `30` | Retention of the server log group in days. |
| `log_level` | `string` | `"info"` | Server log level (STACKORDER_LOG_LEVEL). |
| `extra_environment` | `map(string)` | `{}` | Additional environment variables for the server, such as STACKORDER_WORKERS or OTEL_EXPORTER_OTLP_ENDPOINT. Variables the module sets itself are rejected. |

### Database

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `engine_version` | `string` | `"17"` | PostgreSQL major version, or major.minor. For Aurora a major version resolves to the AWS default minor of that major at plan time. |
| `allow_major_version_upgrade` | `bool` | `false` | Allow a new major version in engine_version to upgrade the RDS instance or Aurora cluster in place. A major upgrade cannot be rolled back; take a snapshot first and set apply_immediately for the same apply. |
| `apply_immediately` | `bool` | `false` | Apply database changes, such as engine_version, instance_class or the parameter group, at once instead of in the next maintenance window. Changes that need a restart then cause a short outage. |
| `instance_class` | `string` | `"db.t4g.micro"` | RDS instance class. Ignored when use_aurora_serverless is true. |
| `allocated_storage` | `number` | `20` | Initial RDS storage in GiB. Ignored when use_aurora_serverless is true. |
| `max_allocated_storage` | `number` | `100` | Upper bound for RDS storage autoscaling in GiB; 0 disables autoscaling. Ignored when use_aurora_serverless is true. |
| `multi_az` | `bool` | `false` | Run the RDS instance Multi-AZ, or add an Aurora reader in another zone. |
| `deletion_protection` | `bool` | `true` | Protect the database from deletion. |
| `skip_final_snapshot` | `bool` | `false` | Skip the final database snapshot on destroy. Keep false outside of throwaway environments. |
| `backup_retention_days` | `number` | `7` | Automated backup retention in days; point-in-time recovery covers this window. |
| `performance_insights` | `bool` | `false` | Enable Performance Insights with the free 7 day retention. Not every instance class supports it. |
| `use_aurora_serverless` | `bool` | `false` | Use an Aurora PostgreSQL Serverless v2 cluster instead of an RDS instance. Switching an existing deployment replaces the database. |
| `aurora_min_acu` | `number` | `0.5` | Minimum Aurora Serverless v2 capacity in ACUs. |
| `aurora_max_acu` | `number` | `2` | Maximum Aurora Serverless v2 capacity in ACUs. |
| `kms_key_arn` | `string` | `null` | Customer managed KMS key for the Secrets Manager secrets and database storage. Null uses the AWS managed keys. |

### GitHub App and server settings

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `github_app_id` | `string` | `null` | GitHub App id (GITHUB_APP_ID). Leave the App inputs null on the first deploy: the server starts in setup mode and /setup creates the App. |
| `github_app_private_key` | `string` (ephemeral, sensitive) | `null` | PEM private key of the GitHub App (GITHUB_APP_PRIVATE_KEY). |
| `github_webhook_secret` | `string` (ephemeral, sensitive) | `null` | Webhook secret of the GitHub App (GITHUB_WEBHOOK_SECRET). |
| `github_oauth_client_id` | `string` | `null` | OAuth client id of the GitHub App, for human sign-in (GITHUB_OAUTH_CLIENT_ID). |
| `github_oauth_client_secret` | `string` (ephemeral, sensitive) | `null` | OAuth client secret of the GitHub App (GITHUB_OAUTH_CLIENT_SECRET). |
| `session_key` | `string` (ephemeral, sensitive) | `null` | 32 byte hex key for cookie signing (STACKORDER_SESSION_KEY). Null generates a new one whenever the app secret is written, which ends every session. |
| `metrics_token` | `string` (ephemeral, sensitive) | `null` | Bearer token that GET /metrics requires (STACKORDER_METRICS_TOKEN), at least 16 printable ASCII characters without white space. Null generates 32 hexadecimal characters whenever the app secret is written. |
| `db_password_version` | `number` | `1` | Version of the generated database password. Increase it to rotate the password: the database and the DATABASE_URL secret get the new one in the same apply, and the service rolls. |
| `secrets_version` | `number` | `1` | Version of the app and metrics token secrets. Increase it after changing an ephemeral input, or to generate a new session key and metrics token; both secrets are rewritten and the service rolls. |
| `secret_recovery_window_days` | `number` | `30` | Days Secrets Manager keeps a deleted secret recoverable; 0 deletes immediately. |
| `github_api_url` | `string` | `"https://api.github.com"` | GitHub API base URL (GITHUB_API_URL); GitHub Enterprise Server uses `https://<host>/api/v3`. |
| `required_workflow_ref` | `string` | `null` | Glob that runner tokens' job_workflow_ref must match (STACKORDER_REQUIRED_WORKFLOW_REF), such as stackorder/actions/.github/workflows/*.yml@refs/tags/v1*. Null accepts any workflow. |
| `oidc_audience` | `string` | `null` | Audience runner OIDC tokens must carry (STACKORDER_OIDC_AUDIENCE). Null uses the public URL. |

### Artifact bucket and alarms

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `artifact_bucket_enabled` | `bool` | `false` | Create an S3 bucket for full plan text (STACKORDER_ARTIFACT_BUCKET) and grant the task role access to it. This is the only AWS permission the task role ever gets. |
| `artifact_retention_days` | `number` | `90` | Days after which objects in the artifact bucket expire. |
| `alarms_enabled` | `bool` | `false` | Create CloudWatch alarms for target 5xx responses, unhealthy targets and service CPU, plus database free storage (RDS) or ACU utilization (Aurora, whose storage grows on its own). |
| `alarm_sns_topic_arn` | `string` | `null` | SNS topic notified when an alarm changes state. Required when alarms_enabled is true. |
| `alarm_thresholds` | `object` | `{}` | Alarm thresholds: target 5xx responses per 5 minutes, average service CPU percent, RDS free storage in bytes, and Aurora ACU utilization percent. |

### What the module passes to the server {#server-environment}

| Variable | Source |
| --- | --- |
| `STACKORDER_BASE_URL` | `base_url`, or `https://<domain_name>` |
| `STACKORDER_LISTEN` | `:8080` |
| `GITHUB_API_URL` | `github_api_url` |
| `STACKORDER_OIDC_AUDIENCE` | `oidc_audience`, or the base URL |
| `STACKORDER_REQUIRED_WORKFLOW_REF` | `required_workflow_ref`, when set |
| `STACKORDER_ARTIFACT_BUCKET` | The artifact bucket, when `artifact_bucket_enabled` |
| `STACKORDER_LOG_LEVEL` | `log_level` |
| `DATABASE_URL` | The database secret: `postgres://stackorder:<password>@<endpoint>:5432/stackorder?sslmode=require` |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET`, `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | The App secret, when set |
| `STACKORDER_SESSION_KEY` | The App secret; `session_key`, or generated |
| `STACKORDER_METRICS_TOKEN` | The App secret; `metrics_token`, or generated. Scrapers read the same value from the `<name>/metrics-token` secret |

Everything else the server reads, such as `STACKORDER_WORKERS`, the retention durations, `STACKORDER_LOG_FORMAT`, `GITHUB_WEB_URL`, `GITHUB_OIDC_ISSUER` or `OTEL_EXPORTER_OTLP_ENDPOINT`, goes through `extra_environment`, which rejects the variables above. Values in `extra_environment` are plain text in the task definition, so keep secrets out of it; the metrics token has its own input, `metrics_token`, and scrapers read it from the secret named by the `metrics_token_secret_arn` output, see [Security hardening](/operations/security-hardening#metrics). On GitHub Enterprise Server set `github_api_url` and add `GITHUB_WEB_URL` and `GITHUB_OIDC_ISSUER` to `extra_environment`.

### Container health check {#health-check}

By default the container runs `stackorder-server healthcheck` every 30 s with a 5 s timeout, 3 retries and a 30 s start period. It asks `/healthz`, which does not touch the database, so the container check fails only when the process itself stops answering. The load balancer's `/readyz` check, database included, gates deployments and drives the circuit breaker, and ECS also replaces a task that fails it, so a database outage still makes ECS replace tasks, whatever `health_check_command` is. A replacement task then exits at start-up until the database answers again. Set `health_check_command = []` to turn the container check off. `stop_timeout_seconds` (60 by default) gives the server time to drain HTTP and its workers before ECS sends SIGKILL.

## Outputs {#outputs}

| Output | Description |
| --- | --- |
| `url` | Public URL of the server (STACKORDER_BASE_URL). |
| `alb_dns_name` | DNS name of the load balancer; point a CNAME or alias here when DNS is not managed by the module. |
| `alb_zone_id` | Route53 zone id of the load balancer, for alias records. |
| `setup_url` | Page that creates the GitHub App from a manifest on the first deploy. It opens only with the one-time token the server logs at start-up: take the full URL from the `setup_url` line in the `log_group_name` log group. |
| `webhook_url` | Webhook URL of the GitHub App. |
| `ecs_cluster_name` | Name of the ECS cluster. |
| `ecs_service_name` | Name of the ECS service. |
| `task_definition_arn` | ARN of the current task definition revision. |
| `db_endpoint` | Host name of the database writer endpoint. |
| `db_secret_arn` | ARN of the Secrets Manager secret holding DATABASE_URL. |
| `app_secret_arn` | ARN of the Secrets Manager secret holding the GitHub App credentials, the session key and the metrics token as JSON. |
| `metrics_token_secret_arn` | ARN of the Secrets Manager secret holding only the `/metrics` bearer token, as plain text; grant Prometheus read access to this one rather than to the app secret. |
| `artifact_bucket` | Name of the artifact bucket, or null when artifact_bucket_enabled is false. |
| `alb_access_logs_bucket` | Name of the load balancer access log bucket, or null when alb_access_logs_enabled is false. |
| `security_group_ids` | Security group ids of the load balancer, the service and the database. |
| `alb_arn` | ARN of the load balancer. |
| `https_listener_arn` | ARN of the HTTPS listener, for listener rules of your own at priority 100 or above; the module keeps priorities 1 to 99 for its rules. |
| `http_listener_arn` | ARN of the HTTP listener, which redirects to HTTPS. |
| `target_group_arn` | ARN of the target group of the server's tasks, for listener rules that forward to the server. |
| `log_group_name` | CloudWatch log group of the server. |

## Single sign-on in front of the UI {#sso}

Set `oidc_authentication` for any OpenID Connect provider, such as Google Workspace, Microsoft Entra ID or Okta, or `cognito_authentication` for a Cognito user pool, and the HTTPS listener authenticates people before the server sees them: its default action becomes authenticate, then forward. The UI, `/setup` and the server's own GitHub sign-in (`/auth/login` and `/auth/callback`) stay behind it. People still get their Stackorder identity and permissions from GitHub, because the server does not read the load balancer's `x-amzn-oidc-*` headers.

Machines cannot sign in interactively, so the module adds listener rules that forward without authentication:

| Priority | Path | Other conditions | Callers |
| --- | --- | --- | --- |
| 1 | `/webhooks/github` | `POST` | GitHub webhook deliveries, verified by their signature |
| 2 | `/healthz`, `/readyz` | `GET` or `HEAD` | Uptime checks and external monitoring |
| 3 | `/v1/runs`, `/v1/runs/*` | `Authorization: Bearer *` | Actions jobs with their OIDC token, and the CLI with an OIDC token or an API key |
| 4 | `/v1/unlock`, `/v1/me` | `Authorization: Bearer *` | The CLI with an OIDC token or an API key |
| 5 | `/metrics` | `GET`, `Authorization: Bearer *` | Prometheus with the metrics token |

The server still authenticates every request these rules let through. The UI's own `/v1` calls carry a session cookie rather than a bearer token, so they stay behind the load balancer, and scripts that call other `/v1` endpoints with an API key need a rule of their own. The module keeps priorities 1 to 99; attach your own rules to the `https_listener_arn` output at 100 or above.

The load balancer calls the identity provider itself, so it needs HTTPS egress. Identity providers publish no fixed address ranges, so with authentication set and `alb_https_egress_cidrs` empty the module allows 443 to `0.0.0.0/0`; set `alb_https_egress_cidrs` to narrow it. The AWS provider has no write-only argument for the OIDC client secret, so the listener stores `client_secret` in Terraform state. With Google Workspace, create a "Web application" OAuth client with the redirect URI `https://<domain_name>/oauth2/idpresponse` and set the consent screen's user type to Internal, which is what limits sign-in to your Workspace. `deploy/terraform/README.md` has a complete example.

## First deployment {#first-deploy}

The server starts in setup mode while the GitHub App inputs are unset, serving only `/setup`, `/healthz` and `/readyz`. The first deployment uses that:

1. Apply the module with the `github_*` inputs unset. The service comes up in setup mode, with one task whatever `desired_count` says, so there is a single setup token.
2. Take the setup URL, with its one-time [setup token](/reference/server-configuration#setup-token), from the task logs:

   ```sh
   aws logs tail "$(terraform output -raw log_group_name)" --since 15m | grep setup_url
   ```

   The `setup_url` output is the same page without the token, which answers `403`. Every task start generates a new token, so take the latest line. The module keeps one task until the App inputs are set because each task has its own token, and with two the load balancer could send the browser to the other one.
3. Open that URL and create the App. The page prints the App id, private key, webhook secret and OAuth client id and secret once.
4. Apply again with those five values. The App id, private key and webhook secret must be set together, as must the two OAuth values. Setting them rewrites the App secret without a change to `secrets_version`.
5. Install the App on your repositories and continue with [Getting started](/guide/getting-started#install).

The task definition carries the version ids of both secrets as Docker labels, so a new secret version rolls the service onto it.

### Secrets and Terraform state {#secrets}

The module puts no secret value in Terraform state or in a saved plan, as long as the caller passes the secret inputs as ephemeral values too. The App private key, webhook secret and OAuth client secret, `session_key` and `metrics_token` are ephemeral inputs. The database password, and the session key and metrics token when those inputs are null, come from ephemeral `random_password` resources. They reach AWS only through write-only attributes: `secret_string_wo` on the secret versions and `password_wo` or `master_password_wo` on the database. State still describes the deployment, so keep it encrypted and access controlled, but it holds none of these values.

A write-only value is sent only when its version changes, so a changed input does nothing until a version moves:

- `secrets_version` rewrites the App and metrics token secrets. Increase it after changing any ephemeral input. Each rewrite generates a new session key and metrics token unless `session_key` and `metrics_token` are set, which signs everyone out and means scrapers must read the new token.
- `db_password_version` generates a new database password and writes it to the database and to the `DATABASE_URL` secret in the same apply. Until the new tasks replace the old ones, the old tasks keep their open database connections but cannot open new ones.
- The module rewrites the App and metrics token secrets on its own when the App id or OAuth client id changes, including when the App inputs are first set, and when either secret is recreated. It writes a new database password and `DATABASE_URL` when the database or its secret is created or replaced.

The generated values are new on every run; only the secrets keep them. If an apply that writes the database password or `DATABASE_URL` fails, do not simply run it again: the retry would write the side that failed with a different password than the side that succeeded. Increase `db_password_version` and apply. Likewise, increase `secrets_version` after a failed apply that writes the App or metrics token secret while the metrics token is generated.

A saved plan does not carry ephemeral values. Supply the ephemeral inputs when applying it as well as when planning, with `TF_VAR_*` variables or `-var`, or read them in the calling configuration with an ephemeral resource such as `ephemeral "aws_secretsmanager_secret_version"`, which is read again at apply time, as `examples/self-hosted` does. Stackorder applies saved plans; to pass the values as variables there, set `TF_VAR_github_app_private_key` and the others in the `env` secret of the reusable workflows.

## Upgrades {#upgrades}

1. Change `image_tag` to the new `X.Y.Z`, or a `sha256:` digest, and apply.
2. ECS starts a new task while the old one keeps serving (100 % minimum healthy, up to 200 % during the deployment). The new task runs any database migrations at start-up, under a migration lock.
3. The new task receives traffic once it passes `/readyz`; the old one drains for 30 s and stops. A task that never becomes healthy is rolled back by the circuit breaker, and `wait_for_steady_state` makes the apply fail.

ECS retries a failed image pull until the deployment times out, so a private package or a tag that does not exist yet would otherwise hold the apply for the whole of `deployment_timeout` (20m by default). For images on ghcr.io, `verify_image` makes the plan fetch an anonymous pull token and the manifest of `image_tag`, as ECS would, and fail with an explanation unless ghcr.io answers 200. The check needs HTTPS access to ghcr.io from wherever Terraform runs; `verify_image = false` skips it.

Pin a specific version rather than `latest`, so an apply is the only thing that changes the running version. Take a database snapshot before an upgrade that includes migrations. See [Upgrades and backups](./upgrades-and-backups).

Upgrading the module from v0.1.0, which kept the secrets in state, needs Terraform or OpenTofu 1.11. In the `examples/self-hosted` setup, first set the new `github_app_id` and `github_oauth_client_id` variables of the stack, which it no longer reads from the App secret; otherwise the server returns to setup mode. The first apply removes the module's `random_password` and `random_bytes` resources from state, which touches nothing in AWS, writes a new database password to the database and to `DATABASE_URL`, and rewrites the App and metrics token secrets, so the service rolls. Until the new tasks replace the old ones, the old tasks keep their open database connections but cannot open new ones. Unless `session_key` and `metrics_token` are set, both are new: everyone signs in again, and scrapers must read the new token. Older state versions, for example in a versioned S3 bucket, still hold the values of v0.1.0. The database password, session key and generated token in them no longer work, but the App private key, webhook secret and OAuth client secret stay valid until you rotate them in the App settings.

## Scaling out {#scale-out}

Set `desired_count = 2` for availability. No other change is needed:

- webhook handling, workers and the API are stateless;
- all coordination goes through Postgres, and workers claim work with `SKIP LOCKED`;
- the scheduler is single-leader through a Postgres advisory lock, so one task schedules and any task executes;
- sessions live in Postgres, and nothing on local disk matters.

Both tasks read the same `STACKORDER_SESSION_KEY` from the App secret. Raise `cpu` and `memory` before adding tasks, and consider `db.t4g.small` or Aurora once the database is the bottleneck.

## Testing the module {#testing}

```sh
cd deploy/terraform
terraform init -backend=false
terraform test
```

The tests plan against mock AWS and HTTP providers and need no AWS account. Mock providers cannot serve ephemeral resources, so the tests use the real random provider and check the values written to the secrets through the module's locals. They need Terraform 1.11 or later, the same floor as the module.

## Managing the module with Stackorder {#dogfooding}

Put the module call in its own stack, with its own `backend "s3"` block, in a repository that has Stackorder installed, as `examples/self-hosted` does. Plans for the server's own changes then run like any other stack. Applies depend on the server being up, so for an upgrade that might not come back healthy, keep a way to apply the stack by hand.
