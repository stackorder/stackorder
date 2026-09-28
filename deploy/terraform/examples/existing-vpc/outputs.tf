output "alb_dns_name" {
  description = "Point a CNAME for domain_name at this name."
  value       = module.stackorder.alb_dns_name
}

output "alb_zone_id" {
  description = "Zone id for an alias record instead of a CNAME."
  value       = module.stackorder.alb_zone_id
}

output "setup_url" {
  description = "Open this once DNS resolves to create the GitHub App."
  value       = module.stackorder.setup_url
}
