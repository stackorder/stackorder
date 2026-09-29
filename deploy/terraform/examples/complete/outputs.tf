output "url" {
  description = "Public URL of the server."
  value       = module.stackorder.url
}

output "setup_url" {
  description = "Open this after the first apply to create the GitHub App."
  value       = module.stackorder.setup_url
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
