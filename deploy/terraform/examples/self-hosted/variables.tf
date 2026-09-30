variable "region" {
  description = "AWS region the server runs in."
  type        = string
  default     = "eu-west-1"
}

variable "domain_name" {
  description = "Host name of the server."
  type        = string
  default     = "stackorder.acme.example"
}

variable "route53_zone_id" {
  description = "Hosted zone of domain_name."
  type        = string
  default     = "Z0123456789ABCDEFGHIJ"
}

variable "stackorder_version" {
  description = "Server release to run. Bumping it in a pull request is how Stackorder upgrades itself."
  type        = string
  default     = "0.1.0"
}

variable "github_app_id" {
  description = "GitHub App id printed by /setup. Null until the App exists, which keeps the server in setup mode."
  type        = string
  default     = null
}

variable "github_oauth_client_id" {
  description = "OAuth client id of the GitHub App printed by /setup."
  type        = string
  default     = null
}

variable "github_app_secret_name" {
  description = "Secrets Manager secret holding the secret values printed by /setup, as JSON keyed by environment variable name. Create it with {} before the first apply; it is read as an ephemeral value on every plan and apply, so it never reaches state."
  type        = string
  default     = "stackorder/github-app"
}
