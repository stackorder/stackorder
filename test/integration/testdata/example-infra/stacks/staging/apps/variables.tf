variable "name" {
  description = "Name of the application."
  type        = string
  default     = "storefront"
}

variable "replicas" {
  description = "Number of replicas."
  type        = number
  default     = 1
}

variable "vpc_id" {
  description = "VPC of the application; the default is the vpc_id output of stacks/staging/vpc."
  type        = string
  default     = "vpc-e919a75364398a449"
}
