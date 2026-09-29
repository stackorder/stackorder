data "terraform_remote_state" "vpc" {
  backend = "s3"

  config = {
    bucket = "stackorder-example-state"
    key    = "prod/vpc.tfstate"
    region = "us-east-1"
  }
}

resource "terraform_data" "app" {
  input = {
    name       = var.name
    replicas   = var.replicas
    vpc_id     = data.terraform_remote_state.vpc.outputs.vpc_id
    subnet_ids = data.terraform_remote_state.vpc.outputs.subnet_ids
  }
}
