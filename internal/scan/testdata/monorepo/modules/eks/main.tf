variable "vpc_id" {
  type = string
}

module "common" {
  source = "../common"
  name   = "eks"
}

resource "aws_eks_cluster" "this" {
  name     = module.common.name
  role_arn = "arn:aws:iam::123456789012:role/eks"

  vpc_config {
    subnet_ids = []
  }
}
