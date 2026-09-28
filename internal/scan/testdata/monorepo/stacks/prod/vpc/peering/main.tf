terraform {
  backend "s3" {
    bucket       = "acme-tfstate"
    key          = "prod/vpc-peering.tfstate"
    region       = "eu-west-1"
    use_lockfile = true
  }
}
