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
      aws_db_instance.this[0].kms_key_id == "arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000"
    )
    error_message = "The KMS key must encrypt both secrets and the database."
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
    error_message = "Without a KMS key the execution role may only read the two secrets."
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

run "health_check_disabled" {
  command = plan

  variables {
    health_check_command = []
  }

  assert {
    condition     = !contains(keys(jsondecode(aws_ecs_task_definition.this.container_definitions)[0]), "healthCheck")
    error_message = "An empty health_check_command must drop the container health check."
  }
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
