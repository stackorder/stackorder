terraform {
  backend "s3" {
    bucket       = "acme-tfstate"
    key          = "prod/eks.tfstate"
    region       = "eu-west-1"
    use_lockfile = true
  }
}

data "terraform_remote_state" "vpc" {
  backend = "s3"
  config = {
    bucket = "acme-tfstate"
    key    = "prod/vpc.tfstate"
    region = "eu-west-1"
  }
}

module "eks" {
  source = "../../../modules/eks"
  vpc_id = data.terraform_remote_state.vpc.outputs.vpc_id
}
