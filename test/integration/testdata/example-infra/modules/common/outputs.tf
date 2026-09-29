output "name" {
  description = "Name of the component."
  value       = var.name
}

output "tags" {
  description = "Tags to put on every resource of the component."
  value       = local.tags
}
