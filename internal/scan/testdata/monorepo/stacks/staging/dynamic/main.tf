variable "env" {
  type    = string
  default = "staging"
}

terraform {
  backend "s3" {
    bucket = "acme-tfstate"
    key    = "${var.env}/dynamic.tfstate"
    region = "eu-west-1"
  }
}
