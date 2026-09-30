variable "environment" {
  type = string
}

variable "instance_size" {
  type = string
}

variable "role" {
  type      = string
  ephemeral = true

  validation {
    condition     = contains(["plan", "deploy"], var.role)
    error_message = "role must be plan or deploy."
  }
}

resource "terraform_data" "kyc" {
  input = {
    environment   = var.environment
    instance_size = var.instance_size
  }

  lifecycle {
    precondition {
      condition     = !terraform.applying || var.role == "deploy"
      error_message = "role must be deploy while applying."
    }
  }
}

output "environment" {
  value = terraform_data.kyc.output.environment
}

output "instance_size" {
  value = terraform_data.kyc.output.instance_size
}
