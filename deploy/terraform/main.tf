data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

data "aws_partition" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  region     = data.aws_region.current.region
  partition  = data.aws_partition.current.partition
  dns_suffix = data.aws_partition.current.dns_suffix

  url           = var.base_url != null ? var.base_url : "https://${var.domain_name}"
  oidc_audience = var.oidc_audience != null ? var.oidc_audience : local.url

  container_name = "stackorder"
  container_port = 8080
  db_port        = 5432
  db_name        = "stackorder"
  db_username    = "stackorder"
  db_major       = split(".", var.engine_version)[0]

  github_app_configured   = nonsensitive(var.github_app_id != null)
  github_oauth_configured = nonsensitive(var.github_oauth_client_id != null)
}
