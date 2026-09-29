resource "terraform_data" "app" {
  input = {
    name     = var.name
    replicas = var.replicas
    vpc_id   = var.vpc_id
  }
}
