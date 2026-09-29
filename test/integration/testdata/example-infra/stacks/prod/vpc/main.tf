module "vpc" {
  source = "../../../modules/vpc"

  name = "prod"
  cidr = "10.10.0.0/16"
}
