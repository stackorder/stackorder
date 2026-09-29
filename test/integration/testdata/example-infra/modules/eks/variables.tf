variable "cluster_name" {
  description = "Name of the cluster."
  type        = string
}

variable "vpc_id" {
  description = "VPC the cluster runs in."
  type        = string

  validation {
    condition     = startswith(var.vpc_id, "vpc-")
    error_message = "vpc_id must look like a VPC id (vpc-...)."
  }
}

variable "subnet_ids" {
  description = "Subnets for the control plane; at least two."
  type        = list(string)

  validation {
    condition     = length(var.subnet_ids) >= 2
    error_message = "subnet_ids must list at least two subnets."
  }
}
