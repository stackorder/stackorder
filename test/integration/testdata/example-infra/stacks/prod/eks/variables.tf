variable "cluster_name" {
  description = "Name of the cluster."
  type        = string
  default     = "prod"
}

variable "vpc_id" {
  description = "VPC of the cluster; the default is the vpc_id output of stacks/prod/vpc."
  type        = string
  default     = "vpc-6754af9632a2745e8"
}

variable "subnet_ids" {
  description = "Subnets of the cluster; the default is the subnet_ids output of stacks/prod/vpc."
  type        = list(string)
  default     = ["subnet-62dfd2129d1b5508a", "subnet-c01aaf2ff6132fd08", "subnet-ca9a32e3583c00b2e"]
}
