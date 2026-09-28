variable "region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "eu-west-1"
}

variable "domain_name" {
  description = "Host name of the server, inside the hosted zone."
  type        = string
}

variable "route53_zone_id" {
  description = "Hosted zone that receives the certificate validation and alias records."
  type        = string
}

variable "stackorder_version" {
  description = "Server image tag to run."
  type        = string
  default     = "latest"
}

variable "github_app_id" {
  description = "GitHub App id from /setup; leave null for the first deploy."
  type        = string
  default     = null
}

variable "github_app_private_key" {
  description = "GitHub App private key from /setup."
  type        = string
  default     = null
  sensitive   = true
}

variable "github_webhook_secret" {
  description = "GitHub App webhook secret from /setup."
  type        = string
  default     = null
  sensitive   = true
}

variable "github_oauth_client_id" {
  description = "GitHub App OAuth client id from /setup."
  type        = string
  default     = null
}

variable "github_oauth_client_secret" {
  description = "GitHub App OAuth client secret from /setup."
  type        = string
  default     = null
  sensitive   = true
}
