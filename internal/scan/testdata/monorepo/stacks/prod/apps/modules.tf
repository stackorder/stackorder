module "sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "5.1.0"
}

module "alb" {
  source = "git::https://github.com/acme/tf-modules.git//alb?ref=v1.2.0"
}

module "dns" {
  source = "git@github.com:acme/tf-modules.git//dns?ref=v1.3.0"
}

module "cdn" {
  source = "git::ssh://git@gitlab.com/acme/shared.git//cdn?ref=v0.4.0"
}
