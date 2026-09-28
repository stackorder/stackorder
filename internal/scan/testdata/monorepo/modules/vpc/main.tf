variable "cidr" {
  type = string
}

resource "aws_vpc" "this" {
  cidr_block = var.cidr
}

output "vpc_id" {
  value = aws_vpc.this.id
}
