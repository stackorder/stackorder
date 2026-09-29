variable "name" {
  description = "Name of the application."
  type        = string
  default     = "storefront"
}

variable "replicas" {
  description = "Number of replicas."
  type        = number
  default     = 3
}
