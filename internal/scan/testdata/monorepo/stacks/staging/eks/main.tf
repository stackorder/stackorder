terraform {
  backend "s3" {
    bucket         = "acme-tfstate"
    key            = "staging/eks.tfstate"
    region         = "eu-west-1"
    dynamodb_table = "tf-locks"
  }
}

variable "state_bucket" {
  type    = string
  default = "acme-tfstate"
}

data "terraform_remote_state" "vpc" {
  backend = "s3"
  config = {
    bucket = var.state_bucket
    key    = "staging/vpc.tfstate"
  }
}
