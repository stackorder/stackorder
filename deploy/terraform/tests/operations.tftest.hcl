mock_provider "aws" {
  override_during = plan
  source          = "./tests/mocks/aws"
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

override_resource {
  target          = aws_secretsmanager_secret.metrics_token
  override_during = plan
  values = {
    arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-MnOpQr"
  }
}

variables {
  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
}

run "alarms" {
  command = plan

  variables {
    alarms_enabled      = true
    alarm_sns_topic_arn = "arn:aws:sns:eu-west-1:123456789012:alerts"
  }

  assert {
    condition = (
      aws_cloudwatch_metric_alarm.target_5xx[0].metric_name == "HTTPCode_Target_5XX_Count" &&
      aws_cloudwatch_metric_alarm.unhealthy_hosts[0].metric_name == "UnHealthyHostCount" &&
      aws_cloudwatch_metric_alarm.cpu[0].metric_name == "CPUUtilization" &&
      aws_cloudwatch_metric_alarm.db_free_storage[0].metric_name == "FreeStorageSpace"
    )
    error_message = "The four alarms must watch 5xx, unhealthy hosts, CPU and database free storage."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.cpu[0].threshold == 80 && aws_cloudwatch_metric_alarm.cpu[0].comparison_operator == "GreaterThanThreshold"
    error_message = "The CPU alarm must fire above 80 percent by default."
  }

  assert {
    condition = alltrue([
      for a in concat(aws_cloudwatch_metric_alarm.target_5xx, aws_cloudwatch_metric_alarm.unhealthy_hosts, aws_cloudwatch_metric_alarm.cpu, aws_cloudwatch_metric_alarm.db_free_storage) :
      a.alarm_actions == toset(["arn:aws:sns:eu-west-1:123456789012:alerts"]) && a.ok_actions == toset(["arn:aws:sns:eu-west-1:123456789012:alerts"])
    ])
    error_message = "Every alarm must notify the SNS topic."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.db_free_storage[0].dimensions.DBInstanceIdentifier == "stackorder"
    error_message = "The storage alarm must watch the RDS instance."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.db_free_storage[0].threshold == 1073741824 && length(aws_cloudwatch_metric_alarm.db_capacity) == 0
    error_message = "RDS must get a 1 GiB free storage alarm and no ACU alarm."
  }
}

run "alarm_thresholds" {
  command = plan

  variables {
    alarms_enabled      = true
    alarm_sns_topic_arn = "arn:aws:sns:eu-west-1:123456789012:alerts"
    alarm_thresholds = {
      cpu_percent = 90
    }
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.cpu[0].threshold == 90 && aws_cloudwatch_metric_alarm.target_5xx[0].threshold == 10
    error_message = "Unset thresholds must keep their defaults."
  }
}

run "execute_command" {
  command = plan

  variables {
    enable_execute_command = true
  }

  assert {
    condition     = aws_ecs_service.this.enable_execute_command && length(aws_iam_role_policy.task_execute_command) == 1
    error_message = "ECS Exec must be enabled on the service with the ssmmessages permissions."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].readonlyRootFilesystem == false
    error_message = "ECS Exec needs a writable root file system for the SSM agent."
  }

  assert {
    condition     = length(aws_iam_role_policy.task_artifacts) == 0
    error_message = "ECS Exec must not grant artifact access."
  }
}

run "kms_key" {
  command = plan

  variables {
    kms_key_arn          = "arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000"
    performance_insights = true
  }

  assert {
    condition = (
      aws_secretsmanager_secret.database.kms_key_id == "arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000" &&
      aws_secretsmanager_secret.app.kms_key_id == "arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000" &&
      aws_secretsmanager_secret.metrics_token.kms_key_id == "arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000" &&
      aws_db_instance.this[0].kms_key_id == "arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000"
    )
    error_message = "The KMS key must encrypt the three secrets and the database."
  }

  assert {
    condition = anytrue([
      for s in jsondecode(aws_iam_role_policy.execution.policy).Statement :
      s.Action == ["kms:Decrypt"] && s.Resource == ["arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000"]
    ])
    error_message = "The execution role must be able to decrypt the secrets with the KMS key."
  }

  assert {
    condition     = aws_db_instance.this[0].performance_insights_enabled && aws_db_instance.this[0].performance_insights_retention_period == 7
    error_message = "Performance Insights must use the free retention."
  }
}

run "execution_role_scope" {
  command = plan

  assert {
    condition = jsondecode(aws_iam_role_policy.execution.policy).Statement == [{
      Sid    = "ReadSecrets"
      Effect = "Allow"
      Action = ["secretsmanager:GetSecretValue"]
      Resource = [
        "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf",
        "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl",
      ]
    }]
    error_message = "Without a KMS key the execution role may only read the database and app secrets, not the scraper's copy of the metrics token."
  }

  assert {
    condition     = aws_iam_role_policy_attachment.execution.policy_arn == "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
    error_message = "The execution role must carry the AWS managed ECS execution policy."
  }

  assert {
    condition = (
      jsondecode(aws_iam_role.task.assume_role_policy).Statement[0].Principal.Service == "ecs-tasks.amazonaws.com" &&
      jsondecode(aws_iam_role.task.assume_role_policy).Statement[0].Condition.StringEquals["aws:SourceAccount"] == "123456789012"
    )
    error_message = "Only ECS tasks of this account may assume the task role."
  }
}

run "health_check_command" {
  command = plan

  variables {
    health_check_command = ["CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:8080/healthz"]
  }

  assert {
    condition = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].healthCheck == {
      command     = ["CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:8080/healthz"]
      interval    = 30
      timeout     = 5
      retries     = 3
      startPeriod = 30
    }
    error_message = "A health_check_command must replace the default container health check command."
  }
}

run "health_check_disabled" {
  command = plan

  variables {
    health_check_command = []
  }

  assert {
    condition     = !contains(keys(jsondecode(aws_ecs_task_definition.this.container_definitions)[0]), "healthCheck")
    error_message = "An empty health_check_command must leave the container without a health check, so the load balancer's /readyz check alone decides task health."
  }
}

run "health_check_command_not_exec_form" {
  command = plan

  variables {
    health_check_command = ["/stackorder-server", "healthcheck"]
  }

  expect_failures = [var.health_check_command]
}

run "health_check_command_without_command" {
  command = plan

  variables {
    health_check_command = ["CMD"]
  }

  expect_failures = [var.health_check_command]
}

run "stop_timeout" {
  command = plan

  variables {
    stop_timeout_seconds = 120
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].stopTimeout == 120
    error_message = "stop_timeout_seconds must become the container's stopTimeout."
  }
}

run "stop_timeout_above_fargate_maximum" {
  command = plan

  variables {
    stop_timeout_seconds = 121
  }

  expect_failures = [var.stop_timeout_seconds]
}

run "stop_timeout_below_fargate_minimum" {
  command = plan

  variables {
    stop_timeout_seconds = 1
  }

  expect_failures = [var.stop_timeout_seconds]
}

run "image_digest_and_arm64" {
  command = plan

  variables {
    image_tag        = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    cpu_architecture = "ARM64"
    cpu              = 512
    memory           = 1024
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].image == "ghcr.io/stackorder/stackorder@sha256:0000000000000000000000000000000000000000000000000000000000000000"
    error_message = "A digest must be referenced with @."
  }

  assert {
    condition = (
      one(aws_ecs_task_definition.this.runtime_platform).cpu_architecture == "ARM64" &&
      aws_ecs_task_definition.this.cpu == "512" &&
      aws_ecs_task_definition.this.memory == "1024"
    )
    error_message = "Architecture and size must follow the inputs."
  }
}

run "image_on_registry_with_port" {
  command = plan

  variables {
    image     = "registry.example.com:5000/stackorder/stackorder"
    image_tag = "1.2.3"
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].image == "registry.example.com:5000/stackorder/stackorder:1.2.3"
    error_message = "A registry port must not be mistaken for a tag."
  }
}

run "secret_rotation_redeploys" {
  command = plan

  assert {
    condition = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].dockerLabels == {
      "io.stackorder.database-secret-version" = "00000000-0000-0000-0000-000000000001"
      "io.stackorder.app-secret-version"      = "00000000-0000-0000-0000-000000000001"
    }
    error_message = "The task definition must carry the secret versions so a new secret value rolls the service."
  }
}

run "secret_versions_follow_their_inputs" {
  command = plan

  assert {
    condition     = aws_db_instance.this[0].password_wo_version == parseint(substr(sha256(jsonencode([1, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf"])), 0, 12), 16)
    error_message = "The database password version must follow db_password_version and the database secret, so a new secret also sets a new password."
  }

  assert {
    condition     = aws_secretsmanager_secret_version.database.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, "db-ABCDEFGHIJKLMNOPQRSTUVWXYZ"])), 0, 12), 16)
    error_message = "The database secret version must follow db_password_version and the database resource id, so a new database also gets a new secret."
  }

  assert {
    condition = (
      aws_secretsmanager_secret_version.app.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, null, null, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-MnOpQr"])), 0, 12), 16) &&
      aws_secretsmanager_secret_version.metrics_token.secret_string_wo_version == aws_secretsmanager_secret_version.app.secret_string_wo_version
    )
    error_message = "The app and metrics token secret versions must follow secrets_version, the App ids and both secrets."
  }
}

run "db_password_rotation" {
  command = plan

  variables {
    db_password_version = 2
  }

  assert {
    condition = (
      aws_db_instance.this[0].password_wo_version == parseint(substr(sha256(jsonencode([2, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/database-url-AbCdEf"])), 0, 12), 16) &&
      aws_secretsmanager_secret_version.database.secret_string_wo_version == parseint(substr(sha256(jsonencode([2, "db-ABCDEFGHIJKLMNOPQRSTUVWXYZ"])), 0, 12), 16)
    )
    error_message = "A new db_password_version must write the database password and the DATABASE_URL secret in the same apply."
  }

  assert {
    condition     = aws_secretsmanager_secret_version.app.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, null, null, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-MnOpQr"])), 0, 12), 16)
    error_message = "Rotating the database password must leave the app secret alone."
  }
}

run "secrets_version_rotation" {
  command = plan

  variables {
    secrets_version = 2
  }

  assert {
    condition = (
      aws_secretsmanager_secret_version.app.secret_string_wo_version == parseint(substr(sha256(jsonencode([2, null, null, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-MnOpQr"])), 0, 12), 16) &&
      aws_secretsmanager_secret_version.metrics_token.secret_string_wo_version == aws_secretsmanager_secret_version.app.secret_string_wo_version
    )
    error_message = "A new secrets_version must rewrite the app and metrics token secrets together."
  }

  assert {
    condition     = aws_secretsmanager_secret_version.database.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, "db-ABCDEFGHIJKLMNOPQRSTUVWXYZ"])), 0, 12), 16)
    error_message = "A new secrets_version must leave the database password alone."
  }
}

run "configuring_the_app_rewrites_the_app_secret" {
  command = plan

  variables {
    github_app_id          = "123456"
    github_app_private_key = "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----\n"
    github_webhook_secret  = "webhook-secret"
  }

  assert {
    condition     = aws_secretsmanager_secret_version.app.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, "123456", null, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-MnOpQr"])), 0, 12), 16)
    error_message = "Setting the App inputs must rewrite the app secret without a new secrets_version, since the task definition starts reading the new keys."
  }
}

run "changing_the_app_id_rewrites_the_app_secret" {
  command = plan

  variables {
    github_app_id          = "654321"
    github_app_private_key = "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----\n"
    github_webhook_secret  = "webhook-secret"
  }

  assert {
    condition     = aws_secretsmanager_secret_version.app.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, "654321", null, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-MnOpQr"])), 0, 12), 16)
    error_message = "A new App id must rewrite the app secret, which holds it as GITHUB_APP_ID."
  }
}

run "a_new_metrics_token_secret_rewrites_both_copies" {
  command = plan

  override_resource {
    target          = aws_secretsmanager_secret.metrics_token
    override_during = plan
    values = {
      arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-StUvWx"
    }
  }

  assert {
    condition = (
      aws_secretsmanager_secret_version.app.secret_string_wo_version == parseint(substr(sha256(jsonencode([1, null, null, "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/app-GhIjKl", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:stackorder/metrics-token-StUvWx"])), 0, 12), 16) &&
      aws_secretsmanager_secret_version.metrics_token.secret_string_wo_version == aws_secretsmanager_secret_version.app.secret_string_wo_version
    )
    error_message = "A recreated metrics token secret must also rewrite the app secret, so both hold the same generated token."
  }
}

run "sensitive_app_inputs_keep_task_definition_readable" {
  command = plan

  variables {
    github_app_id              = sensitive("123456")
    github_app_private_key     = "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----\n"
    github_webhook_secret      = "webhook-secret"
    github_oauth_client_id     = sensitive("Iv23liABCDEF")
    github_oauth_client_secret = "oauth-secret"
  }

  assert {
    condition     = !issensitive(aws_ecs_task_definition.this.container_definitions)
    error_message = "Values read from a secret must not hide the task definition diff."
  }

  assert {
    condition     = length(jsondecode(aws_ecs_task_definition.this.container_definitions)[0].secrets) == 8
    error_message = "All App secrets must be mapped."
  }
}

run "verify_image_pullable" {
  command = plan

  variables {
    image_tag = "1.2.3"
  }

  override_data {
    target = data.http.image_pull_token[0]
    values = {
      status_code   = 200
      response_body = "{\"token\":\"anonymous-pull-token\"}"
    }
  }

  override_data {
    target = data.http.image_manifest[0]
    values = {
      status_code   = 200
      response_body = "{\"schemaVersion\":2}"
    }
  }

  assert {
    condition = (
      data.http.image_pull_token[0].url == "https://ghcr.io/token?scope=repository:stackorder/stackorder:pull" &&
      data.http.image_manifest[0].url == "https://ghcr.io/v2/stackorder/stackorder/manifests/1.2.3"
    )
    error_message = "The check must ask ghcr.io for an anonymous pull token and then the manifest of image_tag."
  }

  assert {
    condition     = data.http.image_manifest[0].request_headers.Authorization == "Bearer anonymous-pull-token"
    error_message = "The manifest request must carry the anonymous pull token."
  }

  assert {
    condition = alltrue([
      for t in [
        "application/vnd.oci.image.index.v1+json",
        "application/vnd.oci.image.manifest.v1+json",
        "application/vnd.docker.distribution.manifest.list.v2+json",
        "application/vnd.docker.distribution.manifest.v2+json",
      ] : strcontains(data.http.image_manifest[0].request_headers.Accept, t)
    ])
    error_message = "The manifest request must accept OCI and Docker indexes and manifests, or ghcr.io answers 404 for some images."
  }

  assert {
    condition     = aws_ecs_service.this.timeouts.create == "20m" && aws_ecs_service.this.timeouts.update == "20m"
    error_message = "The service must wait for a steady state for 20 minutes by default, the provider's own default."
  }
}

run "verify_image_digest" {
  command = plan

  variables {
    image_tag = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  }

  assert {
    condition     = data.http.image_manifest[0].url == "https://ghcr.io/v2/stackorder/stackorder/manifests/sha256:0000000000000000000000000000000000000000000000000000000000000000"
    error_message = "A digest must be looked up as the manifest reference."
  }
}

run "verify_image_private_or_missing" {
  command = plan

  variables {
    image     = "ghcr.io/acme/stackorder"
    image_tag = "0.1.0"
  }

  override_data {
    target = data.http.image_pull_token[0]
    values = {
      status_code   = 403
      response_body = "{\"errors\":[{\"code\":\"DENIED\",\"message\":\"requested access to the resource is denied\"}]}"
    }
  }

  override_data {
    target = data.http.image_manifest[0]
    values = {
      status_code   = 403
      response_body = "{\"errors\":[{\"code\":\"DENIED\",\"message\":\"requested access to the resource is denied\"}]}"
    }
  }

  expect_failures = [data.http.image_manifest]
}

run "verify_image_unknown_tag" {
  command = plan

  variables {
    image_tag = "9.9.9"
  }

  override_data {
    target = data.http.image_manifest[0]
    values = {
      status_code   = 404
      response_body = "{\"errors\":[{\"code\":\"MANIFEST_UNKNOWN\",\"message\":\"manifest unknown\"}]}"
    }
  }

  expect_failures = [data.http.image_manifest]
}

run "verify_image_disabled" {
  command = plan

  variables {
    verify_image = false
  }

  assert {
    condition     = length(data.http.image_pull_token) == 0 && length(data.http.image_manifest) == 0
    error_message = "verify_image = false must skip the ghcr.io requests."
  }
}

run "verify_image_other_registry" {
  command = plan

  variables {
    image     = "registry.example.com:5000/stackorder/stackorder"
    image_tag = "1.2.3"
  }

  assert {
    condition     = length(data.http.image_pull_token) == 0 && length(data.http.image_manifest) == 0
    error_message = "Only images on ghcr.io are checked."
  }
}

run "deployment_timeout" {
  command = plan

  variables {
    deployment_timeout = "45m"
  }

  assert {
    condition     = aws_ecs_service.this.timeouts.create == "45m" && aws_ecs_service.this.timeouts.update == "45m"
    error_message = "deployment_timeout must bound creating and updating the service."
  }
}

run "deployment_timeout_invalid" {
  command = plan

  variables {
    deployment_timeout = "20 minutes"
  }

  expect_failures = [var.deployment_timeout]
}

run "deployment_timeout_zero" {
  command = plan

  variables {
    deployment_timeout = "0m"
  }

  expect_failures = [var.deployment_timeout]
}
