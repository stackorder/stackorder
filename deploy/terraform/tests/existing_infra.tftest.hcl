mock_provider "aws" {
  override_during = plan
  source          = "./tests/mocks/aws"
}

mock_provider "http" {
  override_during = plan
  source          = "./tests/mocks/http"
}

variables {
  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
}

run "defaults_create_their_own_infrastructure" {
  command = plan

  assert {
    condition     = length(aws_ecs_cluster.this) == 1 && length(aws_ecs_cluster_capacity_providers.this) == 0
    error_message = "By default the module creates its own cluster and associates no capacity providers."
  }

  assert {
    condition     = aws_ecs_service.this.launch_type == "FARGATE" && length(aws_ecs_service.this.capacity_provider_strategy) == 0
    error_message = "By default the service must run on FARGATE through launch_type, with no capacity provider strategy."
  }

  assert {
    condition     = length(aws_secretsmanager_secret.database) == 1 && length(aws_secretsmanager_secret.app) == 1 && length(aws_secretsmanager_secret.metrics_token) == 1
    error_message = "By default the module creates all three secrets."
  }
}

run "existing_cluster" {
  command = plan

  variables {
    create_cluster = false
    cluster_arn    = "arn:aws:ecs:eu-west-1:123456789012:cluster/prod-shared"
  }

  assert {
    condition     = length(aws_ecs_cluster.this) == 0 && length(aws_ecs_cluster_capacity_providers.this) == 0
    error_message = "With an existing cluster the module must create neither a cluster nor a capacity provider association."
  }

  assert {
    condition     = aws_ecs_service.this.cluster == "arn:aws:ecs:eu-west-1:123456789012:cluster/prod-shared"
    error_message = "The service must run in the existing cluster."
  }

  assert {
    condition     = output.ecs_cluster_name == "prod-shared"
    error_message = "ecs_cluster_name must be the name parsed from the existing cluster ARN."
  }
}

run "capacity_provider_strategy_on_a_created_cluster" {
  command = plan

  variables {
    capacity_provider_strategy = [
      { capacity_provider = "FARGATE", weight = 1, base = 1 },
      { capacity_provider = "FARGATE_SPOT", weight = 4 },
    ]
  }

  assert {
    condition = {
      for c in aws_ecs_service.this.capacity_provider_strategy : c.capacity_provider => c.weight
    } == { FARGATE = 1, FARGATE_SPOT = 4 }
    error_message = "The service must weight each capacity provider as the input says."
  }

  assert {
    condition     = one([for c in aws_ecs_service.this.capacity_provider_strategy : c.base if c.capacity_provider == "FARGATE"]) == 1
    error_message = "The base must reach the provider it is set on."
  }

  assert {
    condition     = length(aws_ecs_cluster_capacity_providers.this) == 1 && toset(aws_ecs_cluster_capacity_providers.this[0].capacity_providers) == toset(["FARGATE", "FARGATE_SPOT"])
    error_message = "A created cluster must have exactly the strategy's providers associated with it."
  }
}

run "capacity_provider_strategy_on_an_existing_cluster" {
  command = plan

  variables {
    create_cluster             = false
    cluster_arn                = "arn:aws:ecs:eu-west-1:123456789012:cluster/prod-shared"
    capacity_provider_strategy = [{ capacity_provider = "FARGATE_SPOT" }]
  }

  assert {
    condition     = length(aws_ecs_cluster_capacity_providers.this) == 0
    error_message = "The module must not manage the capacity providers of a cluster it did not create."
  }

  assert {
    condition     = [for c in aws_ecs_service.this.capacity_provider_strategy : c.capacity_provider] == ["FARGATE_SPOT"]
    error_message = "The service must run on FARGATE_SPOT in the existing cluster."
  }
}

run "existing_secrets" {
  command = plan

  variables {
    database_secret_arn      = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/database-url-AAAAAA"
    app_secret_arn           = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/app-BBBBBB"
    metrics_token_secret_arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/metrics-token-CCCCCC"
    github_app_id            = "123456"
    github_app_private_key   = "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\n"
    github_webhook_secret    = "webhook-secret"
  }

  assert {
    condition     = length(aws_secretsmanager_secret.database) == 0 && length(aws_secretsmanager_secret.app) == 0 && length(aws_secretsmanager_secret.metrics_token) == 0
    error_message = "With existing secrets the module must create none of the three secret resources."
  }

  assert {
    condition = (
      aws_secretsmanager_secret_version.database.secret_id == "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/database-url-AAAAAA" &&
      aws_secretsmanager_secret_version.app.secret_id == "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/app-BBBBBB" &&
      aws_secretsmanager_secret_version.metrics_token.secret_id == "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/metrics-token-CCCCCC"
    )
    error_message = "The module must still write each secret value into the existing secret the operator supplied."
  }

  assert {
    condition = (
      aws_secretsmanager_secret_version.database.secret_string == null &&
      aws_secretsmanager_secret_version.app.secret_string == null &&
      aws_secretsmanager_secret_version.metrics_token.secret_string == null
    )
    error_message = "No secret value may reach the plan even when the secret is supplied: the writes must stay write-only."
  }

  assert {
    condition = {
      for s in jsondecode(aws_ecs_task_definition.this.container_definitions)[0].secrets : s.name => s.valueFrom
      if contains(["DATABASE_URL", "STACKORDER_SESSION_KEY", "GITHUB_APP_ID"], s.name)
      } == {
      DATABASE_URL           = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/database-url-AAAAAA"
      STACKORDER_SESSION_KEY = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/app-BBBBBB:STACKORDER_SESSION_KEY::"
      GITHUB_APP_ID          = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/app-BBBBBB:GITHUB_APP_ID::"
    }
    error_message = "The container must read DATABASE_URL and each app value from the supplied secrets."
  }

  assert {
    condition = toset(jsondecode(aws_iam_role_policy.execution.policy).Statement[0].Resource) == toset([
      "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/database-url-AAAAAA",
      "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/app-BBBBBB",
    ])
    error_message = "The execution role must read the supplied database and app secrets."
  }

  assert {
    condition = (
      output.db_secret_arn == "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/database-url-AAAAAA" &&
      output.app_secret_arn == "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/app-BBBBBB" &&
      output.metrics_token_secret_arn == "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ops/stackorder/metrics-token-CCCCCC"
    )
    error_message = "The secret ARN outputs must expose the supplied secrets."
  }
}

run "existing_cluster_without_arn" {
  command = plan

  variables {
    create_cluster = false
  }

  expect_failures = [var.cluster_arn]
}

run "created_cluster_with_arn" {
  command = plan

  variables {
    cluster_arn = "arn:aws:ecs:eu-west-1:123456789012:cluster/shared"
  }

  expect_failures = [var.cluster_arn]
}

run "cluster_arn_not_an_arn" {
  command = plan

  variables {
    create_cluster = false
    cluster_arn    = "shared"
  }

  expect_failures = [var.cluster_arn]
}

run "capacity_provider_unknown" {
  command = plan

  variables {
    capacity_provider_strategy = [{ capacity_provider = "EC2" }]
  }

  expect_failures = [var.capacity_provider_strategy]
}

run "capacity_provider_duplicated" {
  command = plan

  variables {
    capacity_provider_strategy = [
      { capacity_provider = "FARGATE" },
      { capacity_provider = "FARGATE" },
    ]
  }

  expect_failures = [var.capacity_provider_strategy]
}

run "capacity_provider_weight_too_high" {
  command = plan

  variables {
    capacity_provider_strategy = [{ capacity_provider = "FARGATE", weight = 2000 }]
  }

  expect_failures = [var.capacity_provider_strategy]
}

run "capacity_provider_two_bases" {
  command = plan

  variables {
    capacity_provider_strategy = [
      { capacity_provider = "FARGATE", base = 1 },
      { capacity_provider = "FARGATE_SPOT", base = 1 },
    ]
  }

  expect_failures = [var.capacity_provider_strategy]
}

run "app_secret_arn_not_an_arn" {
  command = plan

  variables {
    app_secret_arn = "my-app-secret"
  }

  expect_failures = [var.app_secret_arn]
}

run "database_secret_arn_not_an_arn" {
  command = plan

  variables {
    database_secret_arn = "my-db-secret"
  }

  expect_failures = [var.database_secret_arn]
}

run "metrics_token_secret_arn_not_an_arn" {
  command = plan

  variables {
    metrics_token_secret_arn = "my-metrics-secret"
  }

  expect_failures = [var.metrics_token_secret_arn]
}
