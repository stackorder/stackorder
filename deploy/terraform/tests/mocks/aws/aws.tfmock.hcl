mock_data "aws_caller_identity" {
  defaults = {
    account_id = "123456789012"
  }
}

mock_data "aws_region" {
  defaults = {
    region = "eu-west-1"
  }
}

mock_data "aws_partition" {
  defaults = {
    partition  = "aws"
    dns_suffix = "amazonaws.com"
  }
}

mock_data "aws_availability_zones" {
  defaults = {
    names = ["eu-west-1c", "eu-west-1a", "eu-west-1b"]
  }
}

mock_data "aws_rds_engine_version" {
  defaults = {
    version_actual = "17.5"
  }
}

mock_resource "aws_db_instance" {
  defaults = {
    address = "stackorder.abcdefghijkl.eu-west-1.rds.amazonaws.com"
  }
}

mock_resource "aws_rds_cluster" {
  defaults = {
    endpoint = "stackorder.cluster-abcdefghijkl.eu-west-1.rds.amazonaws.com"
  }
}

mock_resource "aws_s3_bucket" {
  defaults = {
    arn = "arn:aws:s3:::stackorder-artifacts-123456789012-eu-west-1"
  }
}

mock_resource "aws_secretsmanager_secret_version" {
  defaults = {
    version_id = "00000000-0000-0000-0000-000000000001"
  }
}
