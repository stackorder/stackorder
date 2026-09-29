output "cluster_name" {
  description = "Name of the cluster."
  value       = module.eks.cluster_name
}

output "endpoint" {
  description = "API server endpoint of the cluster."
  value       = module.eks.endpoint
}
