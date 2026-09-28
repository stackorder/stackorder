variable "name" {
  type = string
}

output "name" {
  value = "acme-${var.name}"
}
