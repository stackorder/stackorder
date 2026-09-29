variables {
  cluster_name = "prod"
  vpc_id       = "vpc-6754af9632a2745e8"
  subnet_ids   = ["subnet-62dfd2129d1b5508a", "subnet-c01aaf2ff6132fd08", "subnet-ca9a32e3583c00b2e"]
}

run "tags_come_from_the_common_module" {
  command = plan

  assert {
    condition = terraform_data.cluster.input.tags == {
      Name        = "prod"
      Environment = "example"
      ManagedBy   = "stackorder-example-infra"
      Component   = "eks"
    }
    error_message = "Cluster tags must be the modules/common defaults plus Component = eks."
  }

  assert {
    condition     = terraform_data.node_group.input.tags == terraform_data.cluster.input.tags
    error_message = "The node group must carry the cluster's tags."
  }
}

run "endpoint_is_derived_from_the_name" {
  command = plan

  assert {
    condition     = output.endpoint == "https://6754AF9632A2745E85C293E5AAC08633.eks.example"
    error_message = "The endpoint must be the upper-cased first 32 hex digits of sha256(cluster_name)."
  }
}

run "rejects_a_vpc_id_without_the_vpc_prefix" {
  command = plan

  variables {
    vpc_id = "prod"
  }

  expect_failures = [var.vpc_id]
}

run "rejects_a_single_subnet" {
  command = plan

  variables {
    subnet_ids = ["subnet-62dfd2129d1b5508a"]
  }

  expect_failures = [var.subnet_ids]
}
