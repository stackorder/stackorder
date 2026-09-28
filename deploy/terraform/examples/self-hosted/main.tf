provider "aws" {
  region = var.region
}

data "aws_secretsmanager_secret_version" "github_app" {
  secret_id = var.github_app_secret_name
}

locals {
  github_app = jsondecode(data.aws_secretsmanager_secret_version.github_app.secret_string)
}

module "stackorder" {
  source = "../.."

  name            = "stackorder"
  domain_name     = var.domain_name
  route53_zone_id = var.route53_zone_id
  image_tag       = var.stackorder_version
  desired_count   = 2

  github_app_id              = lookup(local.github_app, "GITHUB_APP_ID", null)
  github_app_private_key     = lookup(local.github_app, "GITHUB_APP_PRIVATE_KEY", null)
  github_webhook_secret      = lookup(local.github_app, "GITHUB_WEBHOOK_SECRET", null)
  github_oauth_client_id     = lookup(local.github_app, "GITHUB_OAUTH_CLIENT_ID", null)
  github_oauth_client_secret = lookup(local.github_app, "GITHUB_OAUTH_CLIENT_SECRET", null)
  required_workflow_ref      = "stackorder/actions/.github/workflows/*.yml@refs/tags/v1*"
}
