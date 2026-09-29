module "eks" {
  source = "../../../modules/eks"

  cluster_name = var.cluster_name
  vpc_id       = var.vpc_id
  subnet_ids   = var.subnet_ids
}
