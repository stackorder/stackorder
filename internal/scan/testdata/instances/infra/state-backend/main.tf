terraform {
  backend "s3" {
    bucket = "acme-state"
    key    = "bootstrap.tfstate"
  }
}
