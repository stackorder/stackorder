output "vpc_id" {
  description = "Fake VPC id, derived from the name so it is stable across applies."
  value       = local.vpc_id
}

output "cidr" {
  description = "CIDR block of the VPC."
  value       = var.cidr
}

output "subnet_ids" {
  description = "Fake subnet ids, one per zone, ordered a to c."
  value       = [for subnet in values(local.subnets) : subnet.id]
}
