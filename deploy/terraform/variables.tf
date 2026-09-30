variable "name" {
  description = "Name of the deployment, used as the name or prefix of every resource. Lowercase letters, digits and single hyphens, 2 to 24 characters."
  type        = string
  default     = "stackorder"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,22}[a-z0-9]$", var.name)) && !strcontains(var.name, "--")
    error_message = "name must be 2 to 24 characters of lowercase letters, digits and single hyphens, start with a letter and end with a letter or digit."
  }
}

variable "tags" {
  description = "Tags added to every resource that supports them."
  type        = map(string)
  default     = {}
}

variable "create_vpc" {
  description = "Create a VPC with public and private subnets and NAT. When false, vpc_id, public_subnet_ids and private_subnet_ids are required."
  type        = bool
  default     = true
}

variable "vpc_cidr" {
  description = "IPv4 CIDR of the VPC created when create_vpc is true. Subnets are carved as eight equal blocks: public from the first four, private from the last four."
  type        = string
  default     = "10.0.0.0/16"

  validation {
    condition = (
      can(cidrnetmask(var.vpc_cidr)) &&
      try(tonumber(split("/", var.vpc_cidr)[1]), 0) >= 16 &&
      try(tonumber(split("/", var.vpc_cidr)[1]), 99) <= 24
    )
    error_message = "vpc_cidr must be an IPv4 CIDR with a prefix length between /16 and /24."
  }
}

variable "vpc_id" {
  description = "ID of an existing VPC. Required when create_vpc is false, must be null otherwise."
  type        = string
  default     = null

  validation {
    condition     = var.create_vpc == (var.vpc_id == null)
    error_message = "Set vpc_id when create_vpc is false, and leave it null when create_vpc is true."
  }
}

variable "public_subnet_ids" {
  description = "IDs of existing public subnets in at least two availability zones, for the load balancer. Required when create_vpc is false."
  type        = list(string)
  default     = []

  validation {
    condition     = var.create_vpc ? length(var.public_subnet_ids) == 0 : length(var.public_subnet_ids) >= 2
    error_message = "Set at least two public_subnet_ids when create_vpc is false, and none when create_vpc is true."
  }
}

variable "private_subnet_ids" {
  description = "IDs of existing private subnets in at least two availability zones, with a route to the internet through NAT, for the tasks and the database. Required when create_vpc is false."
  type        = list(string)
  default     = []

  validation {
    condition     = var.create_vpc ? length(var.private_subnet_ids) == 0 : length(var.private_subnet_ids) >= 2
    error_message = "Set at least two private_subnet_ids when create_vpc is false, and none when create_vpc is true."
  }
}

variable "availability_zones" {
  description = "Availability zones for the created VPC. Empty picks the first two available zones of the region."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.availability_zones) == 0 || (length(var.availability_zones) >= 2 && length(var.availability_zones) <= 4)
    error_message = "availability_zones must be empty or list two to four zones."
  }
}

variable "single_nat_gateway" {
  description = "Use one NAT gateway for all private subnets instead of one per availability zone."
  type        = bool
  default     = true
}

variable "domain_name" {
  description = "Fully qualified host name the server is reached at, such as stackorder.example.com."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z]{2,63}$", var.domain_name))
    error_message = "domain_name must be a lowercase fully qualified host name."
  }
}

variable "route53_zone_id" {
  description = "Route53 hosted zone in which to create the ACM validation records and the alias record for domain_name. Exactly one of route53_zone_id and certificate_arn must be set."
  type        = string
  default     = null
}

variable "certificate_arn" {
  description = "ARN of an existing ACM certificate covering domain_name. DNS for domain_name is then left to the caller. Exactly one of route53_zone_id and certificate_arn must be set."
  type        = string
  default     = null

  validation {
    condition     = var.certificate_arn == null || can(regex("^arn:[a-z-]+:acm:[a-z0-9-]+:[0-9]{12}:certificate/.+$", var.certificate_arn))
    error_message = "certificate_arn must be an ACM certificate ARN."
  }
}

variable "base_url" {
  description = "Public URL of the server when it differs from https://<domain_name>, for example behind another proxy. No trailing slash."
  type        = string
  default     = null

  validation {
    condition     = var.base_url == null || can(regex("^https?://[^/]+$", var.base_url))
    error_message = "base_url must be a scheme and host, such as https://stackorder.example.com, without a path or trailing slash."
  }
}

variable "ssl_policy" {
  description = "Security policy of the HTTPS listener. Must be a TLS 1.3 policy."
  type        = string
  default     = "ELBSecurityPolicy-TLS13-1-2-2021-06"

  validation {
    condition     = startswith(var.ssl_policy, "ELBSecurityPolicy-TLS13-")
    error_message = "ssl_policy must be one of the ELBSecurityPolicy-TLS13-* policies."
  }
}

variable "ingress_cidrs" {
  description = "IPv4 or IPv6 CIDRs allowed to reach the load balancer on ports 80 and 443. GitHub webhooks and GitHub-hosted runners need the default. Ignored when github_webhook_ip_ranges_only is true."
  type        = list(string)
  default     = ["0.0.0.0/0"]

  validation {
    condition     = length(var.ingress_cidrs) > 0 && alltrue([for c in var.ingress_cidrs : can(cidrhost(c, 0))])
    error_message = "ingress_cidrs must list at least one valid CIDR."
  }
}

variable "github_webhook_ip_ranges_only" {
  description = "Restrict the load balancer to GitHub's webhook source ranges (the hooks list of the GitHub meta API, read at plan time) plus admin_cidrs, instead of ingress_cidrs."
  type        = bool
  default     = false
}

variable "admin_cidrs" {
  description = "CIDRs of people and self-hosted runners that need the UI and API when github_webhook_ip_ranges_only is true."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.admin_cidrs) <= 20 && alltrue([for c in var.admin_cidrs : can(cidrhost(c, 0))])
    error_message = "admin_cidrs must list at most 20 valid CIDRs."
  }
}

variable "alb_access_logs_enabled" {
  description = "Write load balancer access logs to an S3 bucket the module creates, encrypted with SSE-S3 as ELB log delivery requires."
  type        = bool
  default     = false
}

variable "alb_access_logs_retention_days" {
  description = "Days after which objects in the access log bucket expire."
  type        = number
  default     = 90

  validation {
    condition     = var.alb_access_logs_retention_days >= 1 && var.alb_access_logs_retention_days <= 3650 && floor(var.alb_access_logs_retention_days) == var.alb_access_logs_retention_days
    error_message = "alb_access_logs_retention_days must be a whole number between 1 and 3650."
  }
}

variable "waf_web_acl_arn" {
  description = "ARN of a regional AWS WAFv2 web ACL in the module's region to associate with the load balancer. Null associates none."
  type        = string
  default     = null

  validation {
    condition     = var.waf_web_acl_arn == null || can(regex("^arn:[a-z-]+:wafv2:[a-z0-9-]+:[0-9]{12}:regional/webacl/[^/]+/[^/]+$", var.waf_web_acl_arn))
    error_message = "waf_web_acl_arn must be the ARN of a regional WAFv2 web ACL, arn:<partition>:wafv2:<region>:<account>:regional/webacl/<name>/<id>."
  }
}

variable "oidc_authentication" {
  description = "OpenID Connect provider with which the load balancer authenticates people before forwarding, as the authenticate_oidc action of the HTTPS listener. Webhooks, health checks, and runner, CLI and metrics requests that carry a bearer token bypass it. The listener stores client_secret in Terraform state. Null authenticates nobody at the load balancer."
  type = object({
    issuer                              = string
    authorization_endpoint              = string
    token_endpoint                      = string
    user_info_endpoint                  = string
    client_id                           = string
    client_secret                       = string
    scope                               = optional(string)
    session_cookie_name                 = optional(string)
    session_timeout                     = optional(number)
    on_unauthenticated_request          = optional(string)
    authentication_request_extra_params = optional(map(string))
  })
  default   = null
  sensitive = true

  validation {
    condition = nonsensitive(var.oidc_authentication == null || alltrue([
      for url in [
        try(var.oidc_authentication.issuer, ""), try(var.oidc_authentication.authorization_endpoint, ""),
        try(var.oidc_authentication.token_endpoint, ""), try(var.oidc_authentication.user_info_endpoint, ""),
      ] : startswith(url, "https://")
    ]))
    error_message = "oidc_authentication needs https URLs for issuer, authorization_endpoint, token_endpoint and user_info_endpoint."
  }

  validation {
    condition     = nonsensitive(try(contains(["authenticate", "allow", "deny"], coalesce(var.oidc_authentication.on_unauthenticated_request, "authenticate")), true))
    error_message = "oidc_authentication.on_unauthenticated_request must be authenticate, allow or deny."
  }
}

variable "cognito_authentication" {
  description = "Amazon Cognito user pool with which the load balancer authenticates people before forwarding, as the authenticate_cognito action of the HTTPS listener, with the same bypass as oidc_authentication. At most one of oidc_authentication and cognito_authentication may be set. Null authenticates nobody at the load balancer."
  type = object({
    user_pool_arn                       = string
    user_pool_client_id                 = string
    user_pool_domain                    = string
    scope                               = optional(string)
    session_cookie_name                 = optional(string)
    session_timeout                     = optional(number)
    on_unauthenticated_request          = optional(string)
    authentication_request_extra_params = optional(map(string))
  })
  default = null

  validation {
    condition     = nonsensitive(var.oidc_authentication == null) || var.cognito_authentication == null
    error_message = "Set at most one of oidc_authentication and cognito_authentication."
  }

  validation {
    condition     = var.cognito_authentication == null || can(regex("^arn:[a-z-]+:cognito-idp:[a-z0-9-]+:[0-9]{12}:userpool/.+$", try(var.cognito_authentication.user_pool_arn, "")))
    error_message = "cognito_authentication.user_pool_arn must be a Cognito user pool ARN."
  }

  validation {
    condition     = try(contains(["authenticate", "allow", "deny"], coalesce(var.cognito_authentication.on_unauthenticated_request, "authenticate")), true)
    error_message = "cognito_authentication.on_unauthenticated_request must be authenticate, allow or deny."
  }
}

variable "alb_https_egress_cidrs" {
  description = "IPv4 or IPv6 CIDRs the load balancer may reach on port 443, as it must to reach the identity provider of oidc_authentication or cognito_authentication. Empty allows none, or 0.0.0.0/0 when either authentication is set, because identity providers publish no fixed address ranges."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for c in var.alb_https_egress_cidrs : can(cidrhost(c, 0))])
    error_message = "alb_https_egress_cidrs must list valid CIDRs."
  }
}

variable "image" {
  description = "Container image repository of the server."
  type        = string
  default     = "ghcr.io/stackorder/stackorder"

  validation {
    condition     = length(var.image) > 0 && !strcontains(var.image, "@") && !strcontains(reverse(split("/", var.image))[0], ":")
    error_message = "image must be a repository without a tag or digest; set the tag with image_tag."
  }
}

variable "image_tag" {
  description = "Tag or digest (sha256:...) of the server image. Pin a release such as 1.2.3 so upgrades are explicit plans."
  type        = string
  default     = "latest"

  validation {
    condition     = can(regex("^([A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|sha256:[a-f0-9]{64})$", var.image_tag))
    error_message = "image_tag must be a valid image tag or a sha256 digest."
  }
}

variable "desired_count" {
  description = "Number of server tasks. All coordination goes through Postgres, so a second task adds availability without any other change."
  type        = number
  default     = 1

  validation {
    condition     = var.desired_count >= 1 && var.desired_count <= 2 && floor(var.desired_count) == var.desired_count
    error_message = "desired_count must be 1 or 2."
  }
}

variable "cpu" {
  description = "Fargate task CPU units."
  type        = number
  default     = 256

  validation {
    condition     = contains([256, 512, 1024, 2048, 4096], var.cpu)
    error_message = "cpu must be one of 256, 512, 1024, 2048 or 4096."
  }
}

variable "memory" {
  description = "Fargate task memory in MiB; must be a valid combination with cpu."
  type        = number
  default     = 512

  validation {
    condition = (
      (var.cpu == 256 && contains([512, 1024, 2048], var.memory)) ||
      (var.cpu == 512 && var.memory >= 1024 && var.memory <= 4096 && var.memory % 1024 == 0) ||
      (var.cpu == 1024 && var.memory >= 2048 && var.memory <= 8192 && var.memory % 1024 == 0) ||
      (var.cpu == 2048 && var.memory >= 4096 && var.memory <= 16384 && var.memory % 1024 == 0) ||
      (var.cpu == 4096 && var.memory >= 8192 && var.memory <= 30720 && var.memory % 1024 == 0)
    )
    error_message = "memory is not a valid Fargate combination for the chosen cpu."
  }
}

variable "cpu_architecture" {
  description = "CPU architecture of the task, X86_64 or ARM64."
  type        = string
  default     = "X86_64"

  validation {
    condition     = contains(["X86_64", "ARM64"], var.cpu_architecture)
    error_message = "cpu_architecture must be X86_64 or ARM64."
  }
}

variable "enable_execute_command" {
  description = "Enable ECS Exec. The SSM agent needs a writable root file system, so this also turns readonlyRootFilesystem off and grants the task role the ssmmessages permissions."
  type        = bool
  default     = false
}

variable "health_check_command" {
  description = "Container health check command, starting with CMD or CMD-SHELL. The default runs the server's healthcheck subcommand, which GETs /healthz on the listen port. The distroless image has no shell or curl, so a replacement must be a command the image itself provides. Empty turns the container health check off and leaves task health to the load balancer check on /readyz."
  type        = list(string)
  default     = ["CMD", "/stackorder-server", "healthcheck"]

  validation {
    condition     = length(var.health_check_command) == 0 || (length(var.health_check_command) >= 2 && contains(["CMD", "CMD-SHELL"], try(var.health_check_command[0], "")))
    error_message = "health_check_command must be empty, or CMD or CMD-SHELL followed by the command."
  }
}

variable "stop_timeout_seconds" {
  description = "Seconds ECS waits after SIGTERM before it kills the container (stopTimeout), 2 to 120 on Fargate. The server drains HTTP for up to 15 s, then its workers for up to 30 s plus 5 s for cancelled handlers, so a value under 50 can cut the drain short."
  type        = number
  default     = 60

  validation {
    condition     = var.stop_timeout_seconds >= 2 && var.stop_timeout_seconds <= 120 && floor(var.stop_timeout_seconds) == var.stop_timeout_seconds
    error_message = "stop_timeout_seconds must be a whole number between 2 and 120, the range Fargate accepts."
  }
}

variable "wait_for_steady_state" {
  description = "Make terraform apply wait until the new tasks pass /readyz, so an apply of an upgrade fails when the deployment rolls back."
  type        = bool
  default     = true
}

variable "log_retention_days" {
  description = "Retention of the server log group in days."
  type        = number
  default     = 30

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653], var.log_retention_days)
    error_message = "log_retention_days must be a retention value CloudWatch Logs accepts, such as 7, 30, 90 or 365."
  }
}

variable "log_level" {
  description = "Server log level (STACKORDER_LOG_LEVEL)."
  type        = string
  default     = "info"

  validation {
    condition     = contains(["debug", "info", "warn", "error"], var.log_level)
    error_message = "log_level must be one of debug, info, warn or error."
  }
}

variable "extra_environment" {
  description = "Additional environment variables for the server, such as STACKORDER_WORKERS or OTEL_EXPORTER_OTLP_ENDPOINT. Variables the module sets itself are rejected."
  type        = map(string)
  default     = {}

  validation {
    condition = alltrue([
      for k in keys(var.extra_environment) : can(regex("^[A-Za-z_][A-Za-z0-9_]*$", k)) && !contains([
        "STACKORDER_BASE_URL", "STACKORDER_LISTEN", "GITHUB_API_URL", "STACKORDER_OIDC_AUDIENCE",
        "STACKORDER_REQUIRED_WORKFLOW_REF", "STACKORDER_ARTIFACT_BUCKET", "STACKORDER_LOG_LEVEL",
        "DATABASE_URL", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY", "GITHUB_WEBHOOK_SECRET",
        "GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_SECRET", "STACKORDER_SESSION_KEY", "STACKORDER_METRICS_TOKEN",
      ], k)
    ])
    error_message = "extra_environment keys must be valid variable names and must not be a variable the module manages; use the dedicated input instead."
  }
}

variable "engine_version" {
  description = "PostgreSQL major version, or major.minor. For Aurora a major version resolves to the AWS default minor of that major at plan time."
  type        = string
  default     = "17"

  validation {
    condition     = can(regex("^[0-9]+(\\.[0-9]+)?$", var.engine_version)) && try(tonumber(split(".", var.engine_version)[0]), 0) >= 14
    error_message = "engine_version must be a PostgreSQL version of 14 or later, such as 17 or 17.5."
  }
}

variable "allow_major_version_upgrade" {
  description = "Allow a new major version in engine_version to upgrade the RDS instance or Aurora cluster in place. A major upgrade cannot be rolled back; take a snapshot first and set apply_immediately for the same apply."
  type        = bool
  default     = false
}

variable "apply_immediately" {
  description = "Apply database changes, such as engine_version, instance_class or the parameter group, at once instead of in the next maintenance window. Changes that need a restart then cause a short outage."
  type        = bool
  default     = false
}

variable "instance_class" {
  description = "RDS instance class. Ignored when use_aurora_serverless is true."
  type        = string
  default     = "db.t4g.micro"

  validation {
    condition     = startswith(var.instance_class, "db.")
    error_message = "instance_class must be an RDS instance class such as db.t4g.micro."
  }
}

variable "allocated_storage" {
  description = "Initial RDS storage in GiB. Ignored when use_aurora_serverless is true."
  type        = number
  default     = 20

  validation {
    condition     = var.allocated_storage >= 20 && var.allocated_storage <= 65536
    error_message = "allocated_storage must be between 20 and 65536 GiB."
  }
}

variable "max_allocated_storage" {
  description = "Upper bound for RDS storage autoscaling in GiB; 0 disables autoscaling. Ignored when use_aurora_serverless is true."
  type        = number
  default     = 100

  validation {
    condition     = var.max_allocated_storage == 0 || (var.max_allocated_storage >= var.allocated_storage && var.max_allocated_storage <= 65536)
    error_message = "max_allocated_storage must be 0 or between allocated_storage and 65536 GiB."
  }
}

variable "multi_az" {
  description = "Run the RDS instance Multi-AZ, or add an Aurora reader in another zone."
  type        = bool
  default     = false
}

variable "deletion_protection" {
  description = "Protect the database from deletion."
  type        = bool
  default     = true
}

variable "skip_final_snapshot" {
  description = "Skip the final database snapshot on destroy. Keep false outside of throwaway environments."
  type        = bool
  default     = false
}

variable "backup_retention_days" {
  description = "Automated backup retention in days; point-in-time recovery covers this window."
  type        = number
  default     = 7

  validation {
    condition     = var.backup_retention_days >= 1 && var.backup_retention_days <= 35
    error_message = "backup_retention_days must be between 1 and 35."
  }
}

variable "performance_insights" {
  description = "Enable Performance Insights with the free 7 day retention. Not every instance class supports it."
  type        = bool
  default     = false
}

variable "use_aurora_serverless" {
  description = "Use an Aurora PostgreSQL Serverless v2 cluster instead of an RDS instance. Switching an existing deployment replaces the database."
  type        = bool
  default     = false
}

variable "aurora_min_acu" {
  description = "Minimum Aurora Serverless v2 capacity in ACUs."
  type        = number
  default     = 0.5

  validation {
    condition     = var.aurora_min_acu >= 0.5 && var.aurora_min_acu <= 256 && var.aurora_min_acu * 2 == floor(var.aurora_min_acu * 2)
    error_message = "aurora_min_acu must be between 0.5 and 256 in steps of 0.5."
  }
}

variable "aurora_max_acu" {
  description = "Maximum Aurora Serverless v2 capacity in ACUs."
  type        = number
  default     = 2

  validation {
    condition     = var.aurora_max_acu >= 1 && var.aurora_max_acu <= 256 && var.aurora_max_acu * 2 == floor(var.aurora_max_acu * 2) && var.aurora_max_acu >= var.aurora_min_acu
    error_message = "aurora_max_acu must be between 1 and 256 in steps of 0.5 and not below aurora_min_acu."
  }
}

variable "kms_key_arn" {
  description = "Customer managed KMS key for the Secrets Manager secrets and database storage. Null uses the AWS managed keys."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:[a-z-]+:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN."
  }
}

variable "github_app_id" {
  description = "GitHub App id (GITHUB_APP_ID). Leave the App inputs null on the first deploy: the server starts in setup mode and /setup creates the App."
  type        = string
  default     = null

  validation {
    condition = (
      (var.github_app_id == null || can(regex("^[0-9]+$", var.github_app_id))) &&
      (var.github_app_id == null) == (var.github_app_private_key == null) &&
      (var.github_app_id == null) == (var.github_webhook_secret == null)
    )
    error_message = "github_app_id must be numeric, and github_app_id, github_app_private_key and github_webhook_secret must be set together."
  }
}

variable "github_app_private_key" {
  description = "PEM private key of the GitHub App (GITHUB_APP_PRIVATE_KEY)."
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.github_app_private_key == null || can(regex("-----BEGIN [A-Z ]*PRIVATE KEY-----", var.github_app_private_key))
    error_message = "github_app_private_key must be a PEM encoded private key."
  }
}

variable "github_webhook_secret" {
  description = "Webhook secret of the GitHub App (GITHUB_WEBHOOK_SECRET)."
  type        = string
  default     = null
  sensitive   = true
}

variable "github_oauth_client_id" {
  description = "OAuth client id of the GitHub App, for human sign-in (GITHUB_OAUTH_CLIENT_ID)."
  type        = string
  default     = null

  validation {
    condition     = (var.github_oauth_client_id == null) == (var.github_oauth_client_secret == null)
    error_message = "github_oauth_client_id and github_oauth_client_secret must be set together."
  }
}

variable "github_oauth_client_secret" {
  description = "OAuth client secret of the GitHub App (GITHUB_OAUTH_CLIENT_SECRET)."
  type        = string
  default     = null
  sensitive   = true
}

variable "session_key" {
  description = "32 byte hex key for cookie signing (STACKORDER_SESSION_KEY). Null generates one."
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.session_key == null || can(regex("^[0-9a-fA-F]{64}$", var.session_key))
    error_message = "session_key must be 64 hexadecimal characters."
  }
}

variable "metrics_token" {
  description = "Bearer token that GET /metrics requires (STACKORDER_METRICS_TOKEN), at least 16 printable ASCII characters without white space. Null generates 32 hexadecimal characters."
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.metrics_token == null || can(regex("^[!-~]{16,}$", var.metrics_token))
    error_message = "metrics_token must be at least 16 printable ASCII characters without white space."
  }
}

variable "secret_recovery_window_days" {
  description = "Days Secrets Manager keeps a deleted secret recoverable; 0 deletes immediately."
  type        = number
  default     = 30

  validation {
    condition     = var.secret_recovery_window_days == 0 || (var.secret_recovery_window_days >= 7 && var.secret_recovery_window_days <= 30)
    error_message = "secret_recovery_window_days must be 0 or between 7 and 30."
  }
}

variable "github_api_url" {
  description = "GitHub API base URL (GITHUB_API_URL); GitHub Enterprise Server uses https://<host>/api/v3."
  type        = string
  default     = "https://api.github.com"

  validation {
    condition     = can(regex("^https://[^\\s]+[^/]$", var.github_api_url))
    error_message = "github_api_url must be an https URL without a trailing slash."
  }
}

variable "required_workflow_ref" {
  description = "Glob that runner tokens' job_workflow_ref must match (STACKORDER_REQUIRED_WORKFLOW_REF), such as stackorder/actions/.github/workflows/*.yml@refs/tags/v1*. Null accepts any workflow."
  type        = string
  default     = null

  validation {
    condition     = var.required_workflow_ref == null || length(trimspace(coalesce(var.required_workflow_ref, " "))) > 0
    error_message = "required_workflow_ref must be null or a non-empty glob."
  }
}

variable "oidc_audience" {
  description = "Audience runner OIDC tokens must carry (STACKORDER_OIDC_AUDIENCE). Null uses the public URL."
  type        = string
  default     = null

  validation {
    condition     = var.oidc_audience == null || length(trimspace(coalesce(var.oidc_audience, " "))) > 0
    error_message = "oidc_audience must be null or non-empty."
  }
}

variable "artifact_bucket_enabled" {
  description = "Create an S3 bucket for full plan text (STACKORDER_ARTIFACT_BUCKET) and grant the task role access to it. This is the only AWS permission the task role ever gets."
  type        = bool
  default     = false
}

variable "artifact_retention_days" {
  description = "Days after which objects in the artifact bucket expire."
  type        = number
  default     = 90

  validation {
    condition     = var.artifact_retention_days >= 1 && var.artifact_retention_days <= 3650 && floor(var.artifact_retention_days) == var.artifact_retention_days
    error_message = "artifact_retention_days must be a whole number between 1 and 3650."
  }
}

variable "alarms_enabled" {
  description = "Create CloudWatch alarms for target 5xx responses, unhealthy targets and service CPU, plus database free storage (RDS) or ACU utilization (Aurora, whose storage grows on its own)."
  type        = bool
  default     = false
}

variable "alarm_sns_topic_arn" {
  description = "SNS topic notified when an alarm changes state. Required when alarms_enabled is true."
  type        = string
  default     = null

  validation {
    condition     = !var.alarms_enabled || var.alarm_sns_topic_arn != null
    error_message = "alarm_sns_topic_arn is required when alarms_enabled is true."
  }

  validation {
    condition     = var.alarm_sns_topic_arn == null || can(regex("^arn:[a-z-]+:sns:[a-z0-9-]+:[0-9]{12}:.+$", var.alarm_sns_topic_arn))
    error_message = "alarm_sns_topic_arn must be an SNS topic ARN."
  }
}

variable "alarm_thresholds" {
  description = "Alarm thresholds: target 5xx responses per 5 minutes, average service CPU percent, RDS free storage in bytes, and Aurora ACU utilization percent."
  type = object({
    target_5xx_count           = optional(number, 10)
    cpu_percent                = optional(number, 80)
    db_free_storage_bytes      = optional(number, 1073741824)
    db_acu_utilization_percent = optional(number, 90)
  })
  default = {}

  validation {
    condition = (
      var.alarm_thresholds.target_5xx_count >= 1 &&
      var.alarm_thresholds.cpu_percent > 0 && var.alarm_thresholds.cpu_percent <= 100 &&
      var.alarm_thresholds.db_free_storage_bytes > 0 &&
      var.alarm_thresholds.db_acu_utilization_percent > 0 && var.alarm_thresholds.db_acu_utilization_percent <= 100
    )
    error_message = "alarm_thresholds need target_5xx_count >= 1, cpu_percent and db_acu_utilization_percent in (0, 100], and db_free_storage_bytes > 0."
  }
}
