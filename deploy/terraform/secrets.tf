resource "random_password" "db" {
  length  = 40
  special = false
}

resource "random_bytes" "session_key" {
  count = nonsensitive(var.session_key == null) ? 1 : 0

  length = 32
}

locals {
  session_key  = var.session_key != null ? var.session_key : one(random_bytes.session_key[*].hex)
  database_url = "postgres://${local.db_username}:${random_password.db.result}@${local.db_address}:${local.db_port}/${local.db_name}?sslmode=require"

  app_secret = merge(
    { STACKORDER_SESSION_KEY = local.session_key },
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
    ["STACKORDER_SESSION_KEY"],
    local.github_app_configured ? ["GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY", "GITHUB_WEBHOOK_SECRET"] : [],
    local.github_oauth_configured ? ["GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_SECRET"] : [],
  ))
}

resource "aws_secretsmanager_secret" "database" {
  name                    = "${var.name}/database-url"
  description             = "Stackorder DATABASE_URL: PostgreSQL DSN including the master password"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = var.secret_recovery_window_days

  tags = var.tags
}

resource "aws_secretsmanager_secret_version" "database" {
  secret_id     = aws_secretsmanager_secret.database.id
  secret_string = local.database_url
}

resource "aws_secretsmanager_secret" "app" {
  name                    = "${var.name}/app"
  description             = "Stackorder GitHub App credentials and session key, one JSON key per environment variable"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = var.secret_recovery_window_days

  tags = var.tags
}

resource "aws_secretsmanager_secret_version" "app" {
  secret_id     = aws_secretsmanager_secret.app.id
  secret_string = jsonencode(local.app_secret)
}
