terraform {
  backend "s3" {
    bucket         = "acme-tfstate"
    key            = "staging/vpc.tfstate"
    region         = "eu-west-1"
    dynamodb_table = "tf-locks"
  }
}

module "vpc" {
  source = "../../../modules/vpc"
  cidr   = "10.1.0.0/16"
}

module "flowlogs" {
  source = "github.com/acme/tf-modules//flowlogs?ref=v1.2.0"
}
