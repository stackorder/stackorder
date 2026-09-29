run "prod_ids_match_the_stack_defaults" {
  command = plan

  variables {
    name = "prod"
    cidr = "10.10.0.0/16"
  }

  assert {
    condition     = output.vpc_id == "vpc-6754af9632a2745e8"
    error_message = "The prod vpc_id changed; update the vpc_id default of stacks/prod/eks."
  }

  assert {
    condition     = output.subnet_ids == ["subnet-62dfd2129d1b5508a", "subnet-c01aaf2ff6132fd08", "subnet-ca9a32e3583c00b2e"]
    error_message = "The prod subnet_ids changed; update the subnet_ids default of stacks/prod/eks."
  }
}

run "staging_id_matches_the_stack_default" {
  command = plan

  variables {
    name = "staging"
    cidr = "10.20.0.0/16"
  }

  assert {
    condition     = output.vpc_id == "vpc-e919a75364398a449"
    error_message = "The staging vpc_id changed; update the vpc_id default of stacks/staging/apps."
  }
}

run "subnets_split_the_cidr_by_zone" {
  command = plan

  variables {
    name = "prod"
    cidr = "10.10.0.0/16"
  }

  assert {
    condition     = [for zone in ["a", "b", "c"] : terraform_data.subnet[zone].input.cidr] == ["10.10.0.0/20", "10.10.16.0/20", "10.10.32.0/20"]
    error_message = "Subnets must be the first three /20 blocks of the VPC, in zone order."
  }

  assert {
    condition     = alltrue([for subnet in terraform_data.subnet : subnet.input.vpc_id == output.vpc_id])
    error_message = "Every subnet must belong to the VPC."
  }
}

run "rejects_a_cidr_too_small_for_three_subnets" {
  command = plan

  variables {
    name = "tiny"
    cidr = "10.30.0.0/31"
  }

  expect_failures = [var.cidr]
}
