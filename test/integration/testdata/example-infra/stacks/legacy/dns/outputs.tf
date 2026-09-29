output "zone_name" {
  description = "Name of the private zone."
  value       = terraform_data.zone.input.name
}
