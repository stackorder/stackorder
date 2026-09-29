variable "zone_name" {
  description = "Private DNS zone attached to the production VPC."
  type        = string
  default     = "internal.example"
}
