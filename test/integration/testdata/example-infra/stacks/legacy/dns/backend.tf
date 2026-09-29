terraform {
  backend "s3" {
    bucket       = "stackorder-example-state"
    key          = "legacy/dns.tfstate"
    region       = "us-east-1"
    use_lockfile = true
  }
}
