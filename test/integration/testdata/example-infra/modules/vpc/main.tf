locals {
  vpc_id = "vpc-${substr(sha256(var.name), 0, 17)}"

  subnets = {
    for index, zone in ["a", "b", "c"] : zone => {
      id   = "subnet-${substr(sha256("${var.name}/${zone}"), 0, 17)}"
      cidr = cidrsubnet(var.cidr, 4, index)
    }
  }
}

resource "terraform_data" "vpc" {
  input = {
    id   = local.vpc_id
    name = var.name
    cidr = var.cidr
  }
}

resource "terraform_data" "subnet" {
  for_each = local.subnets

  input = {
    id     = each.value.id
    vpc_id = terraform_data.vpc.input.id
    cidr   = each.value.cidr
    zone   = each.key
  }
}
