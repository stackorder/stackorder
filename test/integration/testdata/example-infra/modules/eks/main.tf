module "tags" {
  source = "../common"

  name       = var.cluster_name
  extra_tags = { Component = "eks" }
}

locals {
  endpoint = "https://${upper(substr(sha256(var.cluster_name), 0, 32))}.eks.example"
}

resource "terraform_data" "cluster" {
  input = {
    name       = var.cluster_name
    vpc_id     = var.vpc_id
    subnet_ids = var.subnet_ids
    endpoint   = local.endpoint
    tags       = module.tags.tags
  }
}

resource "terraform_data" "node_group" {
  input = {
    cluster    = terraform_data.cluster.input.name
    subnet_ids = var.subnet_ids
    tags       = module.tags.tags
  }
}
