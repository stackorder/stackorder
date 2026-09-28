mock_provider "aws" {
  override_during = plan
  source          = "./tests/mocks/aws"
}

mock_provider "random" {
  override_during = plan
  source          = "./tests/mocks/random"
}

mock_provider "http" {
  override_during = plan
  source          = "./tests/mocks/http"
}

variables {
  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
}

run "name_uppercase" {
  command = plan

  variables {
    name = "Stackorder"
  }

  expect_failures = [var.name]
}

run "name_double_hyphen" {
  command = plan

  variables {
    name = "stack--order"
  }

  expect_failures = [var.name]
}

run "name_too_long" {
  command = plan

  variables {
    name = "stackorder-production-eu1"
  }

  expect_failures = [var.name]
}

run "existing_vpc_without_ids" {
  command = plan

  variables {
    create_vpc = false
  }

  expect_failures = [var.vpc_id, var.public_subnet_ids, var.private_subnet_ids]
}

run "created_vpc_with_vpc_id" {
  command = plan

  variables {
    vpc_id = "vpc-0123456789abcdef0"
  }

  expect_failures = [var.vpc_id]
}

run "existing_vpc_single_subnet" {
  command = plan

  variables {
    create_vpc         = false
    vpc_id             = "vpc-0123456789abcdef0"
    public_subnet_ids  = ["subnet-0000000000000000a"]
    private_subnet_ids = ["subnet-0000000000000000c", "subnet-0000000000000000d"]
  }

  expect_failures = [var.public_subnet_ids]
}

run "vpc_cidr_too_large" {
  command = plan

  variables {
    vpc_cidr = "10.0.0.0/8"
  }

  expect_failures = [var.vpc_cidr]
}

run "vpc_cidr_not_a_cidr" {
  command = plan

  variables {
    vpc_cidr = "10.0.0.0"
  }

  expect_failures = [var.vpc_cidr]
}

run "domain_name_with_scheme" {
  command = plan

  variables {
    domain_name = "https://stackorder.example.com"
  }

  expect_failures = [var.domain_name]
}

run "base_url_trailing_slash" {
  command = plan

  variables {
    base_url = "https://stackorder.example.com/"
  }

  expect_failures = [var.base_url]
}

run "ssl_policy_without_tls13" {
  command = plan

  variables {
    ssl_policy = "ELBSecurityPolicy-2016-08"
  }

  expect_failures = [var.ssl_policy]
}

run "ingress_cidrs_empty" {
  command = plan

  variables {
    ingress_cidrs = []
  }

  expect_failures = [var.ingress_cidrs]
}

run "memory_invalid_for_cpu" {
  command = plan

  variables {
    cpu    = 1024
    memory = 512
  }

  expect_failures = [var.memory]
}

run "cpu_not_fargate" {
  command = plan

  variables {
    cpu = 300
  }

  expect_failures = [var.cpu]
}

run "extra_environment_reserved" {
  command = plan

  variables {
    extra_environment = {
      DATABASE_URL = "postgres://elsewhere"
    }
  }

  expect_failures = [var.extra_environment]
}

run "github_app_partial" {
  command = plan

  variables {
    github_app_id = "123456"
  }

  expect_failures = [var.github_app_id]
}

run "github_oauth_partial" {
  command = plan

  variables {
    github_oauth_client_secret = "secret"
  }

  expect_failures = [var.github_oauth_client_id]
}

run "github_private_key_not_pem" {
  command = plan

  variables {
    github_app_id          = "123456"
    github_app_private_key = "not a key"
    github_webhook_secret  = "secret"
  }

  expect_failures = [var.github_app_private_key]
}

run "session_key_not_hex" {
  command = plan

  variables {
    session_key = "not-a-hex-key"
  }

  expect_failures = [var.session_key]
}

run "required_workflow_ref_empty" {
  command = plan

  variables {
    required_workflow_ref = ""
  }

  expect_failures = [var.required_workflow_ref]
}

run "github_api_url_trailing_slash" {
  command = plan

  variables {
    github_api_url = "https://api.github.com/"
  }

  expect_failures = [var.github_api_url]
}

run "engine_version_too_old" {
  command = plan

  variables {
    engine_version = "13"
  }

  expect_failures = [var.engine_version]
}

run "max_allocated_below_allocated" {
  command = plan

  variables {
    allocated_storage     = 50
    max_allocated_storage = 30
  }

  expect_failures = [var.max_allocated_storage]
}

run "log_retention_not_supported" {
  command = plan

  variables {
    log_retention_days = 45
  }

  expect_failures = [var.log_retention_days]
}

run "alarms_without_topic" {
  command = plan

  variables {
    alarms_enabled = true
  }

  expect_failures = [var.alarm_sns_topic_arn]
}

run "secret_recovery_window_out_of_range" {
  command = plan

  variables {
    secret_recovery_window_days = 3
  }

  expect_failures = [var.secret_recovery_window_days]
}

run "image_with_digest" {
  command = plan

  variables {
    image = "ghcr.io/stackorder/stackorder@sha256:0000000000000000000000000000000000000000000000000000000000000000"
  }

  expect_failures = [var.image]
}
