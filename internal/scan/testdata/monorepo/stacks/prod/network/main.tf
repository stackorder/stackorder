terraform {
  backend "s3" {
    bucket               = "acme-tfstate"
    key                  = "prod/network.tfstate"
    region               = "eu-west-1"
    workspace_key_prefix = "ws"
    use_lockfile         = true
  }
}
