variable "name" {
  description = "Name of the VPC; also seeds its fake ids."
  type        = string
}

variable "cidr" {
  description = "CIDR block of the VPC; split into three subnets four bits longer."
  type        = string

  validation {
    condition     = can(cidrsubnet(var.cidr, 4, 2))
    error_message = "cidr must be a CIDR block with room for three /+4 subnets, such as 10.10.0.0/16."
  }
}
