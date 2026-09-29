output "url" {
  description = "Public URL of the server."
  value       = module.stackorder.url
}

output "setup_url" {
  description = "Page that creates the GitHub App on the first deploy."
  value       = module.stackorder.setup_url
}

output "metrics_token_secret_arn" {
  description = "Secret holding only the /metrics bearer token, for Prometheus."
  value       = module.stackorder.metrics_token_secret_arn
}
