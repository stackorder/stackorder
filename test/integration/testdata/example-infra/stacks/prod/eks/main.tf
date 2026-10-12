provider "stackorder" {}

data "stackorder_outputs" "vpc" {
  stack = "stacks/prod/vpc"
}

module "eks" {
  source = "../../../modules/eks"

  cluster_name = var.cluster_name
  vpc_id       = data.stackorder_outputs.vpc.nonsensitive_values.vpc_id
  subnet_ids   = data.stackorder_outputs.vpc.nonsensitive_values.subnet_ids
}
