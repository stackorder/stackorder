provider "aws" {
  region = var.region
}

ephemeral "aws_secretsmanager_secret_version" "github_app" {
  secret_id = var.github_app_secret_name
}

locals {
  github_app = jsondecode(ephemeral.aws_secretsmanager_secret_version.github_app.secret_string)
}

module "stackorder" {
  source = "../.."

  name            = "stackorder"
  domain_name     = var.domain_name
  route53_zone_id = var.route53_zone_id
  image_tag       = var.stackorder_version
  desired_count   = 2

  github_app_id              = var.github_app_id
  github_app_private_key     = var.github_app_id == null ? null : lookup(local.github_app, "GITHUB_APP_PRIVATE_KEY", null)
  github_webhook_secret      = var.github_app_id == null ? null : lookup(local.github_app, "GITHUB_WEBHOOK_SECRET", null)
  github_oauth_client_id     = var.github_oauth_client_id
  github_oauth_client_secret = var.github_oauth_client_id == null ? null : lookup(local.github_app, "GITHUB_OAUTH_CLIENT_SECRET", null)
  secrets_version            = 1
  required_workflow_ref      = "stackorder/actions/.github/workflows/*.yml@refs/tags/v1*"
}
