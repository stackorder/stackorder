provider "aws" {
  region = var.region
}

module "stackorder" {
  source = "../.."

  name               = "stackorder"
  create_vpc         = false
  vpc_id             = var.vpc_id
  public_subnet_ids  = var.public_subnet_ids
  private_subnet_ids = var.private_subnet_ids

  domain_name     = var.domain_name
  certificate_arn = var.certificate_arn
}
