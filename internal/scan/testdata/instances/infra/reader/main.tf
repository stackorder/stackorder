terraform {
  backend "s3" {
    key = "reader.tfstate"
  }
}

data "terraform_remote_state" "kyc" {
  backend = "s3"
  config = {
    bucket = "acme-state"
    key    = "kyc/production.tfstate"
  }
}
