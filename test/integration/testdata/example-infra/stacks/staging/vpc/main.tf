module "vpc" {
  source = "../../../modules/vpc"

  name = "staging"
  cidr = "10.20.0.0/16"
}
