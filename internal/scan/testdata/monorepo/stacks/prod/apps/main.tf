terraform {
  backend "s3" {
    bucket       = "acme-tfstate"
    key          = "prod/apps.tfstate"
    region       = "eu-west-1"
    use_lockfile = true
  }
}

data "terraform_remote_state" "eks" {
  backend = "s3"
  config = {
    bucket = "acme-tfstate"
    key    = "prod/eks.tfstate"
    region = "eu-west-1"
  }
}

data "terraform_remote_state" "dns" {
  backend = "s3"
  config = {
    bucket = "acme-tfstate"
    key    = "legacy/dns.tfstate"
    region = "eu-west-1"
  }
}

data "terraform_remote_state" "network" {
  backend   = "s3"
  workspace = "blue"
  config = {
    bucket               = "acme-tfstate"
    key                  = "prod/network.tfstate"
    workspace_key_prefix = "ws"
    region               = "eu-west-1"
  }
}
