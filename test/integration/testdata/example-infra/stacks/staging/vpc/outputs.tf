output "vpc_id" {
  description = "VPC id, read by dependent stacks."
  value       = module.vpc.vpc_id
}

output "subnet_ids" {
  description = "Subnet ids, read by dependent stacks."
  value       = module.vpc.subnet_ids
}
