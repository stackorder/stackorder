terraform {
  backend "s3" {
    bucket       = "acme-tfstate"
    key          = "legacy/dns.tfstate"
    region       = "eu-west-1"
    use_lockfile = true
  }
}

module "outside" {
  source = "../../../../outside"
}

module "archive" {
  source = "s3::https://s3-eu-west-1.amazonaws.com/acme-modules/dns.zip"
}
