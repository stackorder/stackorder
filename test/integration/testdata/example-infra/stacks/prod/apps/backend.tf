terraform {
  backend "s3" {
    bucket       = "stackorder-example-state"
    key          = "prod/apps.tfstate"
    region       = "us-east-1"
    use_lockfile = true
  }
}
