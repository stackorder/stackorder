variable "region" {
  type    = string
  default = "eu-west-1"
}

module "not_scanned" {
  source = "../../modules/vpc"
}
