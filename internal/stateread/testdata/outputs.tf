output "name" {
  value = "acme-network"
}

output "subnet_count" {
  value = 3
}

output "nat_enabled" {
  value = true
}

output "zones" {
  value = tolist(["eu-west-1a", "eu-west-1b"])
}

output "tags" {
  value = tomap({ team = "platform", env = "prod" })
}

output "network" {
  value = {
    cidr    = "10.0.0.0/16"
    private = ["10.0.1.0/24", "10.0.2.0/24"]
  }
}

output "db_password" {
  value     = "acme-canary-password"
  sensitive = true
}

output "unset" {
  value = null
}
