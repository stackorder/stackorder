output "cluster_name" {
  description = "Name of the cluster."
  value       = var.cluster_name
}

output "endpoint" {
  description = "Fake API server endpoint, derived from the cluster name."
  value       = local.endpoint
}
