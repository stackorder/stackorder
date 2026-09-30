output "url" {
  description = "Public URL of the server."
  value       = module.stackorder.url
}

output "setup_url" {
  description = "Page that creates the GitHub App from a manifest on the first deploy. It opens only with the one-time token the server logs at start-up: take the full URL from the setup_url line in the log_group_name log group."
  value       = module.stackorder.setup_url
}

output "log_group_name" {
  description = "CloudWatch log group of the server, where it logs the setup URL with its one-time token."
  value       = module.stackorder.log_group_name
}

output "webhook_url" {
  description = "Webhook URL of the GitHub App."
  value       = module.stackorder.webhook_url
}

output "app_secret_arn" {
  description = "Secret holding the App credentials, session key and metrics token."
  value       = module.stackorder.app_secret_arn
}

output "metrics_token_secret_arn" {
  description = "Secret holding only the /metrics bearer token, for Prometheus."
  value       = module.stackorder.metrics_token_secret_arn
}
