variable "region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "eu-west-1"
}

variable "vpc_id" {
  description = "Existing VPC."
  type        = string
}

variable "public_subnet_ids" {
  description = "Public subnets, in at least two availability zones, for the load balancer."
  type        = list(string)
}

variable "private_subnet_ids" {
  description = "Private subnets with a NAT route, in at least two availability zones, for the tasks and the database."
  type        = list(string)
}

variable "domain_name" {
  description = "Host name of the server; its DNS record is managed outside this configuration."
  type        = string
}

variable "certificate_arn" {
  description = "Existing ACM certificate covering domain_name, in the same region."
  type        = string
}
