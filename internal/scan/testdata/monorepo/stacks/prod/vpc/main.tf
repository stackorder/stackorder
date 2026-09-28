terraform {
  backend "s3" {
    bucket       = "acme-tfstate"
    key          = "prod/vpc.tfstate"
    region       = "eu-west-1"
    use_lockfile = true
  }
}

module "vpc" {
  source = "../../../modules/vpc"
  cidr   = "10.0.0.0/16"
}
