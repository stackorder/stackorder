ephemeral "random_password" "db" {
  length  = 40
  special = false
}

ephemeral "random_password" "session_key" {
  length           = 64
  upper            = false
  lower            = false
  numeric          = true
  special          = true
  override_special = "abcdef"
}

ephemeral "random_password" "metrics_token" {
  length           = 32
  upper            = false
  lower            = false
  numeric          = true
  special          = true
  override_special = "abcdef"
}

locals {
  session_key   = var.session_key != null ? var.session_key : ephemeral.random_password.session_key.result
  metrics_token = var.metrics_token != null ? var.metrics_token : ephemeral.random_password.metrics_token.result
  database_url  = "postgres://${local.db_username}:${ephemeral.random_password.db.result}@${local.db_address}:${local.db_port}/${local.db_name}?sslmode=require"

  app_secret = merge(
    {
      STACKORDER_SESSION_KEY   = local.session_key
      STACKORDER_METRICS_TOKEN = local.metrics_token
    },
    local.github_app_configured ? {
      GITHUB_APP_ID          = var.github_app_id
      GITHUB_APP_PRIVATE_KEY = var.github_app_private_key
      GITHUB_WEBHOOK_SECRET  = var.github_webhook_secret
    } : {},
    local.github_oauth_configured ? {
      GITHUB_OAUTH_CLIENT_ID     = var.github_oauth_client_id
      GITHUB_OAUTH_CLIENT_SECRET = var.github_oauth_client_secret
    } : {},
  )
  app_secret_keys = sort(concat(
    ["STACKORDER_SESSION_KEY", "STACKORDER_METRICS_TOKEN"],
    local.github_app_configured ? ["GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY", "GITHUB_WEBHOOK_SECRET"] : [],
    local.github_oauth_configured ? ["GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_SECRET"] : [],
  ))

  db_resource_id = var.use_aurora_serverless ? one(aws_rds_cluster.this[*].cluster_resource_id) : one(aws_db_instance.this[*].resource_id)

  # The versions also follow the database, the secrets and the App ids, so both ends of a write-only value are written in one apply.
  db_password_version     = parseint(substr(sha256(jsonencode([var.db_password_version, aws_secretsmanager_secret.database.arn])), 0, 12), 16)
  database_secret_version = parseint(substr(sha256(jsonencode([var.db_password_version, local.db_resource_id])), 0, 12), 16)
  app_secret_inputs       = [var.secrets_version, var.github_app_id, var.github_oauth_client_id, aws_secretsmanager_secret.app.arn, aws_secretsmanager_secret.metrics_token.arn]
  app_secret_version      = parseint(substr(nonsensitive(sha256(jsonencode(local.app_secret_inputs))), 0, 12), 16)
}

resource "aws_secretsmanager_secret" "database" {
  name                    = "${var.name}/database-url"
  description             = "Stackorder DATABASE_URL: PostgreSQL DSN including the master password"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = var.secret_recovery_window_days

  tags = var.tags
}

resource "aws_secretsmanager_secret_version" "database" {
  secret_id                = aws_secretsmanager_secret.database.id
  secret_string_wo         = local.database_url
  secret_string_wo_version = local.database_secret_version
}

resource "aws_secretsmanager_secret" "app" {
  name                    = "${var.name}/app"
  description             = "Stackorder GitHub App credentials, session key and metrics token, one JSON key per environment variable"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = var.secret_recovery_window_days

  tags = var.tags
}

resource "aws_secretsmanager_secret_version" "app" {
  secret_id                = aws_secretsmanager_secret.app.id
  secret_string_wo         = jsonencode(local.app_secret)
  secret_string_wo_version = local.app_secret_version
}

resource "aws_secretsmanager_secret" "metrics_token" {
  name                    = "${var.name}/metrics-token"
  description             = "Stackorder /metrics bearer token as plain text, for Prometheus; holds nothing else, unlike the app secret"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = var.secret_recovery_window_days

  tags = var.tags
}

resource "aws_secretsmanager_secret_version" "metrics_token" {
  secret_id                = aws_secretsmanager_secret.metrics_token.id
  secret_string_wo         = local.metrics_token
  secret_string_wo_version = local.app_secret_version
}
