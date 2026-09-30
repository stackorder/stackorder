output "alb_dns_name" {
  description = "Point a CNAME for domain_name at this name."
  value       = module.stackorder.alb_dns_name
}

output "alb_zone_id" {
  description = "Zone id for an alias record instead of a CNAME."
  value       = module.stackorder.alb_zone_id
}

output "setup_url" {
  description = "Page that creates the GitHub App from a manifest on the first deploy. It opens only with the one-time token the server logs at start-up: take the full URL from the setup_url line in the log_group_name log group."
  value       = module.stackorder.setup_url
}

output "log_group_name" {
  description = "CloudWatch log group of the server, where it logs the setup URL with its one-time token."
  value       = module.stackorder.log_group_name
}
