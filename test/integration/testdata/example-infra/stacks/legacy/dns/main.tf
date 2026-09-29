data "terraform_remote_state" "vpc" {
  backend = "s3"

  config = {
    bucket = "stackorder-example-state"
    key    = "prod/vpc.tfstate"
    region = "us-east-1"
  }
}

resource "terraform_data" "zone" {
  input = {
    name   = var.zone_name
    vpc_id = data.terraform_remote_state.vpc.outputs.vpc_id
  }
}
