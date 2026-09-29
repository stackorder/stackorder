locals {
  tags = merge(
    {
      Name        = var.name
      Environment = var.environment
      ManagedBy   = "stackorder-example-infra"
    },
    var.extra_tags,
  )
}
