mock_provider "aws" {
  override_during = plan
  source          = "./tests/mocks/aws"
}

mock_provider "random" {
  override_during = plan
  source          = "./tests/mocks/random"
}

mock_provider "http" {
  override_during = plan
  source          = "./tests/mocks/http"
}

override_resource {
  target          = aws_secretsmanager_secret.database
  override_during = plan
  values = {
    arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf"
  }
}

override_resource {
  target          = aws_secretsmanager_secret.app
  override_during = plan
  values = {
    arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl"
  }
}

variables {
  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
}

run "defaults" {
  command = plan

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].readonlyRootFilesystem == true
    error_message = "The container must run with a read-only root file system."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].user == "65532:65532"
    error_message = "The container must run as the distroless nonroot user."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].image == "ghcr.io/stackorder/stackorder:latest"
    error_message = "The image must default to ghcr.io/stackorder/stackorder:latest."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].portMappings[0].containerPort == 8080
    error_message = "The container must listen on port 8080."
  }

  assert {
    condition     = !contains(keys(jsondecode(aws_ecs_task_definition.this.container_definitions)[0]), "healthCheck")
    error_message = "The distroless image has no health check command, so by default the load balancer's /readyz check alone decides task health."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].logConfiguration.logDriver == "awslogs"
    error_message = "The container must log with the awslogs driver."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].logConfiguration.options["awslogs-group"] == "/ecs/stackorder"
    error_message = "The container must log to the module's log group."
  }

  assert {
    condition = {
      for s in jsondecode(aws_ecs_task_definition.this.container_definitions)[0].secrets : s.name => s.valueFrom
      } == {
      DATABASE_URL           = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf"
      STACKORDER_SESSION_KEY = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl:STACKORDER_SESSION_KEY::"
    }
    error_message = "Without App credentials only DATABASE_URL and STACKORDER_SESSION_KEY are injected, so the server starts in setup mode."
  }

  assert {
    condition = {
      for e in jsondecode(aws_ecs_task_definition.this.container_definitions)[0].environment : e.name => e.value
      } == {
      GITHUB_API_URL           = "https://api.github.com"
      STACKORDER_BASE_URL      = "https://stackorder.example.com"
      STACKORDER_LISTEN        = ":8080"
      STACKORDER_LOG_LEVEL     = "info"
      STACKORDER_OIDC_AUDIENCE = "https://stackorder.example.com"
    }
    error_message = "The default environment must carry the base URL, listen address, API URL, audience and log level."
  }

  assert {
    condition     = nonsensitive(aws_secretsmanager_secret_version.database.secret_string) == "postgres://stackorder:mockdatabasepassword@stackorder.abcdefghijkl.eu-west-1.rds.amazonaws.com:5432/stackorder?sslmode=require"
    error_message = "DATABASE_URL must be the full DSN with the generated password and TLS required."
  }

  assert {
    condition     = keys(jsondecode(nonsensitive(aws_secretsmanager_secret_version.app.secret_string))) == ["STACKORDER_SESSION_KEY"]
    error_message = "The app secret must hold only the session key until the App is configured."
  }

  assert {
    condition     = length(random_bytes.session_key) == 1 && length(nonsensitive(random_bytes.session_key[0].hex)) == 64
    error_message = "A 32 byte session key must be generated when session_key is null."
  }

  assert {
    condition     = length(aws_iam_role_policy.task_artifacts) == 0 && length(aws_iam_role_policy.task_execute_command) == 0
    error_message = "The task role must have no policies by default."
  }

  assert {
    condition     = aws_iam_role_policy_attachment.execution.role == aws_iam_role.execution.name
    error_message = "The only managed policy attachment must be on the execution role."
  }

  assert {
    condition     = length(aws_s3_bucket.artifacts) == 0 && length(aws_s3_bucket_policy.artifacts) == 0
    error_message = "No artifact bucket must exist by default."
  }

  assert {
    condition     = toset([for r in aws_vpc_security_group_ingress_rule.alb : r.cidr_ipv4]) == toset(["0.0.0.0/0"])
    error_message = "The load balancer must default to ingress from anywhere, as GitHub webhooks need."
  }

  assert {
    condition     = aws_db_instance.this[0].deletion_protection == true
    error_message = "deletion_protection must default to true."
  }

  assert {
    condition = (
      aws_db_instance.this[0].storage_encrypted &&
      !aws_db_instance.this[0].publicly_accessible &&
      aws_db_instance.this[0].instance_class == "db.t4g.micro" &&
      aws_db_instance.this[0].engine_version == "17"
    )
    error_message = "The database must be an encrypted, private db.t4g.micro running PostgreSQL 17."
  }

  assert {
    condition     = one([for p in aws_db_parameter_group.this[0].parameter : p.value if p.name == "rds.force_ssl"]) == "1"
    error_message = "The parameter group must force TLS."
  }

  assert {
    condition     = length(aws_rds_cluster.this) == 0 && length(aws_rds_cluster_instance.this) == 0
    error_message = "No Aurora resources by default."
  }

  assert {
    condition = (
      aws_lb_target_group.this.health_check[0].path == "/readyz" &&
      aws_lb_target_group.this.deregistration_delay == "30" &&
      aws_lb_target_group.this.port == 8080
    )
    error_message = "The target group must check /readyz on port 8080 with a 30 s deregistration delay."
  }

  assert {
    condition     = startswith(aws_lb_listener.https.ssl_policy, "ELBSecurityPolicy-TLS13-")
    error_message = "The HTTPS listener must use a TLS 1.3 policy."
  }

  assert {
    condition     = aws_lb_listener.http.default_action[0].redirect[0].protocol == "HTTPS" && aws_lb_listener.http.default_action[0].redirect[0].status_code == "HTTP_301"
    error_message = "HTTP must redirect to HTTPS."
  }

  assert {
    condition = (
      aws_ecs_service.this.desired_count == 1 &&
      aws_ecs_service.this.deployment_minimum_healthy_percent == 100 &&
      aws_ecs_service.this.deployment_maximum_percent == 200 &&
      aws_ecs_service.this.deployment_circuit_breaker[0].enable &&
      aws_ecs_service.this.deployment_circuit_breaker[0].rollback
    )
    error_message = "The service must roll one task at 100/200 with the circuit breaker and rollback."
  }

  assert {
    condition     = one(aws_ecs_service.this.network_configuration).assign_public_ip == false
    error_message = "Tasks must not get public IPs."
  }

  assert {
    condition     = one(aws_ecs_cluster.this.setting).name == "containerInsights" && one(aws_ecs_cluster.this.setting).value == "enabled"
    error_message = "Container insights must be enabled."
  }

  assert {
    condition     = length(aws_acm_certificate.this) == 1 && aws_acm_certificate.this[0].validation_method == "DNS"
    error_message = "A DNS validated certificate must be issued when route53_zone_id is set."
  }

  assert {
    condition     = aws_route53_record.this[0].name == "stackorder.example.com" && aws_route53_record.this[0].type == "A"
    error_message = "An alias A record must point domain_name at the load balancer."
  }

  assert {
    condition     = length(aws_cloudwatch_metric_alarm.target_5xx) == 0 && length(aws_cloudwatch_metric_alarm.db_free_storage) == 0
    error_message = "Alarms must be off by default."
  }

  assert {
    condition     = output.setup_url == "https://stackorder.example.com/setup" && output.webhook_url == "https://stackorder.example.com/webhooks/github"
    error_message = "setup_url and webhook_url must derive from the public URL."
  }

  assert {
    condition     = output.log_group_name == "/ecs/stackorder" && output.artifact_bucket == null
    error_message = "Unexpected log group or artifact bucket output."
  }
}

run "full_configuration" {
  command = plan

  variables {
    github_app_id              = "123456"
    github_app_private_key     = "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\n"
    github_webhook_secret      = "webhook-secret"
    github_oauth_client_id     = "Iv23liABCDEF"
    github_oauth_client_secret = "oauth-secret"
    session_key                = "abababababababababababababababababababababababababababababababab"
    required_workflow_ref      = "stackorder/actions/.github/workflows/*.yml@refs/tags/v1*"
    oidc_audience              = "stackorder"
    base_url                   = "https://ci.example.com"
    log_level                  = "debug"
    image_tag                  = "1.2.3"
    artifact_bucket_enabled    = true
    extra_environment = {
      STACKORDER_WORKERS = "8"
    }
  }

  assert {
    condition = alltrue([
      for name in [
        "STACKORDER_BASE_URL", "STACKORDER_LISTEN", "GITHUB_API_URL", "STACKORDER_OIDC_AUDIENCE",
        "STACKORDER_REQUIRED_WORKFLOW_REF", "STACKORDER_ARTIFACT_BUCKET", "STACKORDER_LOG_LEVEL", "STACKORDER_WORKERS",
      ] : contains([for e in jsondecode(aws_ecs_task_definition.this.container_definitions)[0].environment : e.name], name)
    ])
    error_message = "Every server environment variable must be in the container definition."
  }

  assert {
    condition = {
      for e in jsondecode(aws_ecs_task_definition.this.container_definitions)[0].environment : e.name => e.value
      } == {
      GITHUB_API_URL                   = "https://api.github.com"
      STACKORDER_ARTIFACT_BUCKET       = "stackorder-artifacts-123456789012-eu-west-1"
      STACKORDER_BASE_URL              = "https://ci.example.com"
      STACKORDER_LISTEN                = ":8080"
      STACKORDER_LOG_LEVEL             = "debug"
      STACKORDER_OIDC_AUDIENCE         = "stackorder"
      STACKORDER_REQUIRED_WORKFLOW_REF = "stackorder/actions/.github/workflows/*.yml@refs/tags/v1*"
      STACKORDER_WORKERS               = "8"
    }
    error_message = "Environment values must follow the inputs."
  }

  assert {
    condition = {
      for s in jsondecode(aws_ecs_task_definition.this.container_definitions)[0].secrets : s.name => s.valueFrom
      } == {
      DATABASE_URL               = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf"
      GITHUB_APP_ID              = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl:GITHUB_APP_ID::"
      GITHUB_APP_PRIVATE_KEY     = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl:GITHUB_APP_PRIVATE_KEY::"
      GITHUB_OAUTH_CLIENT_ID     = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl:GITHUB_OAUTH_CLIENT_ID::"
      GITHUB_OAUTH_CLIENT_SECRET = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl:GITHUB_OAUTH_CLIENT_SECRET::"
      GITHUB_WEBHOOK_SECRET      = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl:GITHUB_WEBHOOK_SECRET::"
      STACKORDER_SESSION_KEY     = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl:STACKORDER_SESSION_KEY::"
    }
    error_message = "Each App secret must map to its JSON key of the app secret, and DATABASE_URL to the database secret."
  }

  assert {
    condition = jsondecode(nonsensitive(aws_secretsmanager_secret_version.app.secret_string)) == {
      GITHUB_APP_ID              = "123456"
      GITHUB_APP_PRIVATE_KEY     = "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\n"
      GITHUB_OAUTH_CLIENT_ID     = "Iv23liABCDEF"
      GITHUB_OAUTH_CLIENT_SECRET = "oauth-secret"
      GITHUB_WEBHOOK_SECRET      = "webhook-secret"
      STACKORDER_SESSION_KEY     = "abababababababababababababababababababababababababababababababab"
    }
    error_message = "The app secret must hold every App value and the supplied session key."
  }

  assert {
    condition     = length(random_bytes.session_key) == 0
    error_message = "No session key is generated when one is supplied."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].image == "ghcr.io/stackorder/stackorder:1.2.3"
    error_message = "image_tag must select the image tag."
  }

  assert {
    condition     = length(aws_s3_bucket.artifacts) == 1 && length(aws_s3_bucket_policy.artifacts) == 1 && length(aws_iam_role_policy.task_artifacts) == 1
    error_message = "Enabling the artifact bucket must create the bucket, its policy and the task role policy."
  }

  assert {
    condition = toset(flatten([
      for s in jsondecode(aws_iam_role_policy.task_artifacts[0].policy).Statement : s.Action
    ])) == toset(["s3:ListBucket", "s3:GetObject", "s3:PutObject", "s3:DeleteObject"])
    error_message = "The task role may only list, get, put and delete in the artifact bucket."
  }

  assert {
    condition = (
      one(jsondecode(aws_s3_bucket_policy.artifacts[0].policy).Statement).Effect == "Deny" &&
      one(jsondecode(aws_s3_bucket_policy.artifacts[0].policy).Statement).Condition.Bool["aws:SecureTransport"] == "false"
    )
    error_message = "The bucket policy must deny insecure transport."
  }

  assert {
    condition = (
      aws_s3_bucket_public_access_block.artifacts[0].block_public_acls &&
      aws_s3_bucket_public_access_block.artifacts[0].block_public_policy &&
      aws_s3_bucket_public_access_block.artifacts[0].ignore_public_acls &&
      aws_s3_bucket_public_access_block.artifacts[0].restrict_public_buckets
    )
    error_message = "Public access to the artifact bucket must be blocked."
  }

  assert {
    condition = (
      one(one(aws_s3_bucket_server_side_encryption_configuration.artifacts[0].rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256" &&
      one(aws_s3_bucket_versioning.artifacts[0].versioning_configuration).status == "Disabled" &&
      one(one(aws_s3_bucket_lifecycle_configuration.artifacts[0].rule).expiration).days == 90
    )
    error_message = "The artifact bucket must use SSE-S3, no versioning and a 90 day expiry."
  }

  assert {
    condition     = output.url == "https://ci.example.com" && output.artifact_bucket == "stackorder-artifacts-123456789012-eu-west-1"
    error_message = "base_url must override the public URL."
  }
}

run "ingress_from_variable" {
  command = plan

  variables {
    ingress_cidrs = ["203.0.113.0/24", "2001:db8::/32"]
  }

  assert {
    condition     = toset(compact([for r in aws_vpc_security_group_ingress_rule.alb : r.cidr_ipv4])) == toset(["203.0.113.0/24"])
    error_message = "IPv4 ingress must come from ingress_cidrs."
  }

  assert {
    condition     = toset(compact([for r in aws_vpc_security_group_ingress_rule.alb : r.cidr_ipv6])) == toset(["2001:db8::/32"])
    error_message = "IPv6 ingress must come from ingress_cidrs."
  }

  assert {
    condition     = toset([for r in aws_vpc_security_group_ingress_rule.alb : r.from_port]) == toset([80, 443]) && length(aws_vpc_security_group_ingress_rule.alb) == 4
    error_message = "Each CIDR must be allowed on ports 80 and 443 only."
  }
}

run "deletion_protection_follows_variable" {
  command = plan

  variables {
    deletion_protection = false
    skip_final_snapshot = true
  }

  assert {
    condition     = aws_db_instance.this[0].deletion_protection == false
    error_message = "deletion_protection must follow the variable."
  }

  assert {
    condition     = aws_db_instance.this[0].skip_final_snapshot && aws_db_instance.this[0].final_snapshot_identifier == null
    error_message = "skip_final_snapshot must drop the final snapshot identifier."
  }
}

run "existing_certificate" {
  command = plan

  variables {
    route53_zone_id = null
    certificate_arn = "arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
  }

  assert {
    condition     = aws_lb_listener.https.certificate_arn == "arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
    error_message = "The HTTPS listener must use the supplied certificate."
  }

  assert {
    condition     = length(aws_acm_certificate.this) == 0 && length(aws_route53_record.this) == 0 && length(aws_route53_record.validation) == 0
    error_message = "No certificate or DNS records are managed with an existing certificate."
  }
}

run "requires_certificate_or_zone" {
  command = plan

  variables {
    route53_zone_id = null
  }

  expect_failures = [aws_lb_listener.https]
}

run "rejects_certificate_and_zone" {
  command = plan

  variables {
    certificate_arn = "arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
  }

  expect_failures = [aws_lb_listener.https]
}

run "desired_count_above_range" {
  command = plan

  variables {
    desired_count = 3
  }

  expect_failures = [var.desired_count]
}

run "desired_count_below_range" {
  command = plan

  variables {
    desired_count = 0
  }

  expect_failures = [var.desired_count]
}

run "desired_count_two" {
  command = plan

  variables {
    desired_count = 2
  }

  assert {
    condition     = aws_ecs_service.this.desired_count == 2
    error_message = "desired_count must reach the service."
  }
}
