provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project = "stackorder"
    }
  }
}

resource "aws_sns_topic" "alarms" {
  name = "stackorder-alarms"
}

module "stackorder" {
  source = "../.."

  name               = "stackorder"
  create_vpc         = true
  vpc_cidr           = "10.40.0.0/16"
  single_nat_gateway = true

  domain_name     = var.domain_name
  route53_zone_id = var.route53_zone_id

  image_tag     = var.stackorder_version
  desired_count = 2

  github_app_id              = var.github_app_id
  github_app_private_key     = var.github_app_private_key
  github_webhook_secret      = var.github_webhook_secret
  github_oauth_client_id     = var.github_oauth_client_id
  github_oauth_client_secret = var.github_oauth_client_secret
  secrets_version            = var.secrets_version
  required_workflow_ref      = "stackorder/actions/.github/workflows/*.yml@refs/tags/v1*"

  artifact_bucket_enabled = true
  alb_access_logs_enabled = true
  alarms_enabled          = true
  alarm_sns_topic_arn     = aws_sns_topic.alarms.arn
}
