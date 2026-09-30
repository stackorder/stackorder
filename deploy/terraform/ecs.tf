locals {
  image_ref       = "${var.image}${startswith(var.image_tag, "sha256:") ? "@" : ":"}${var.image_tag}"
  verify_image    = var.verify_image && startswith(var.image, "ghcr.io/")
  ghcr_repository = trimprefix(var.image, "ghcr.io/")
}

data "http" "image_pull_token" {
  count = local.verify_image ? 1 : 0

  url = "https://ghcr.io/token?scope=repository:${local.ghcr_repository}:pull"
}

data "http" "image_manifest" {
  count = local.verify_image ? 1 : 0

  url = "https://ghcr.io/v2/${local.ghcr_repository}/manifests/${var.image_tag}"
  request_headers = {
    Accept = join(", ", [
      "application/vnd.oci.image.index.v1+json",
      "application/vnd.oci.image.manifest.v1+json",
      "application/vnd.docker.distribution.manifest.list.v2+json",
      "application/vnd.docker.distribution.manifest.v2+json",
    ])
    Authorization = "Bearer ${try(jsondecode(data.http.image_pull_token[0].response_body).token, "")}"
  }

  lifecycle {
    postcondition {
      condition     = self.status_code == 200
      error_message = "${local.image_ref} cannot be pulled anonymously from ghcr.io (token HTTP ${data.http.image_pull_token[0].status_code}, manifest HTTP ${self.status_code}): the package is private or the tag does not exist. ECS would otherwise retry the pull until the deployment times out. Make the package public or fix image_tag, or set verify_image = false to skip this check."
    }
  }
}

locals {
  environment = merge(
    {
      STACKORDER_BASE_URL      = local.url
      STACKORDER_LISTEN        = ":${local.container_port}"
      GITHUB_API_URL           = var.github_api_url
      STACKORDER_OIDC_AUDIENCE = local.oidc_audience
      STACKORDER_LOG_LEVEL     = var.log_level
    },
    var.required_workflow_ref == null ? {} : { STACKORDER_REQUIRED_WORKFLOW_REF = var.required_workflow_ref },
    var.artifact_bucket_enabled ? { STACKORDER_ARTIFACT_BUCKET = aws_s3_bucket.artifacts[0].bucket } : {},
    var.extra_environment,
  )

  container_secrets = concat(
    [{ name = "DATABASE_URL", valueFrom = aws_secretsmanager_secret.database.arn }],
    [for key in local.app_secret_keys : { name = key, valueFrom = "${aws_secretsmanager_secret.app.arn}:${key}::" }],
  )

  container = merge(
    {
      name                   = local.container_name
      image                  = local.image_ref
      essential              = true
      user                   = "65532:65532"
      readonlyRootFilesystem = !var.enable_execute_command
      stopTimeout            = var.stop_timeout_seconds
      portMappings = [{
        name          = "http"
        containerPort = local.container_port
        hostPort      = local.container_port
        protocol      = "tcp"
      }]
      environment = [for key in sort(keys(local.environment)) : { name = key, value = local.environment[key] }]
      secrets     = local.container_secrets
      dockerLabels = {
        "io.stackorder.database-secret-version" = aws_secretsmanager_secret_version.database.version_id
        "io.stackorder.app-secret-version"      = aws_secretsmanager_secret_version.app.version_id
      }
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.this.name
          awslogs-region        = local.region
          awslogs-stream-prefix = local.container_name
        }
      }
      linuxParameters = {
        initProcessEnabled = var.enable_execute_command
      }
    },
    length(var.health_check_command) == 0 ? {} : {
      healthCheck = {
        command     = var.health_check_command
        interval    = 30
        timeout     = 5
        retries     = 3
        startPeriod = 30
      }
    },
  )
}

resource "aws_ecs_cluster" "this" {
  name = var.name

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  tags = var.tags
}

resource "aws_ecs_task_definition" "this" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.cpu)
  memory                   = tostring(var.memory)
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn
  container_definitions    = jsonencode([local.container])

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  tags = var.tags
}

resource "aws_ecs_service" "this" {
  name                   = var.name
  cluster                = aws_ecs_cluster.this.id
  task_definition        = aws_ecs_task_definition.this.arn
  desired_count          = local.github_app_configured ? var.desired_count : 1
  launch_type            = "FARGATE"
  platform_version       = "LATEST"
  enable_execute_command = var.enable_execute_command
  propagate_tags         = "SERVICE"
  wait_for_steady_state  = var.wait_for_steady_state

  enable_ecs_managed_tags            = true
  health_check_grace_period_seconds  = 60
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  deployment_controller {
    type = "ECS"
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = local.task_subnet_ids
    security_groups  = [aws_security_group.service.id]
    assign_public_ip = var.public_tasks
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.this.arn
    container_name   = local.container_name
    container_port   = local.container_port
  }

  tags = var.tags

  timeouts {
    create = var.deployment_timeout
    update = var.deployment_timeout
  }

  depends_on = [
    aws_lb_listener.https,
    aws_iam_role_policy.execution,
    aws_iam_role_policy_attachment.execution,
    aws_vpc_security_group_egress_rule.service_https,
    aws_vpc_security_group_egress_rule.service_to_db,
    aws_route.private_nat,
    aws_route_table_association.private,
    aws_route.public_internet,
    aws_route_table_association.public,
  ]
}
