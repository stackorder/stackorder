output "url" {
  description = "Public URL of the server (STACKORDER_BASE_URL)."
  value       = local.url
}

output "alb_dns_name" {
  description = "DNS name of the load balancer; point a CNAME or alias here when DNS is not managed by the module."
  value       = aws_lb.this.dns_name
}

output "alb_zone_id" {
  description = "Route53 zone id of the load balancer, for alias records."
  value       = aws_lb.this.zone_id
}

output "setup_url" {
  description = "Page that creates the GitHub App from a manifest on the first deploy."
  value       = "${local.url}/setup"
}

output "webhook_url" {
  description = "Webhook URL of the GitHub App."
  value       = "${local.url}/webhooks/github"
}

output "ecs_cluster_name" {
  description = "Name of the ECS cluster."
  value       = aws_ecs_cluster.this.name
}

output "ecs_service_name" {
  description = "Name of the ECS service."
  value       = aws_ecs_service.this.name
}

output "task_definition_arn" {
  description = "ARN of the current task definition revision."
  value       = aws_ecs_task_definition.this.arn
}

output "db_endpoint" {
  description = "Host name of the database writer endpoint."
  value       = local.db_address
}

output "db_secret_arn" {
  description = "ARN of the Secrets Manager secret holding DATABASE_URL."
  value       = aws_secretsmanager_secret.database.arn
}

output "app_secret_arn" {
  description = "ARN of the Secrets Manager secret holding the GitHub App credentials, the session key and the metrics token as JSON."
  value       = aws_secretsmanager_secret.app.arn
}

output "metrics_token_secret_arn" {
  description = "ARN of the Secrets Manager secret holding only the /metrics bearer token, as plain text; grant Prometheus read access to this one rather than to the app secret."
  value       = aws_secretsmanager_secret.metrics_token.arn
}

output "artifact_bucket" {
  description = "Name of the artifact bucket, or null when artifact_bucket_enabled is false."
  value       = one(aws_s3_bucket.artifacts[*].bucket)
}

output "alb_access_logs_bucket" {
  description = "Name of the load balancer access log bucket, or null when alb_access_logs_enabled is false."
  value       = one(aws_s3_bucket.alb_logs[*].bucket)
}

output "security_group_ids" {
  description = "Security group ids of the load balancer, the service and the database."
  value = {
    alb     = aws_security_group.alb.id
    service = aws_security_group.service.id
    db      = aws_security_group.db.id
  }
}

output "log_group_name" {
  description = "CloudWatch log group of the server."
  value       = aws_cloudwatch_log_group.this.name
}
