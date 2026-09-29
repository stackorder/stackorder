output "name" {
  description = "Name of the application."
  value       = terraform_data.app.input.name
}

output "vpc_id" {
  description = "VPC the application runs in, as read from stacks/prod/vpc."
  value       = terraform_data.app.input.vpc_id
}
