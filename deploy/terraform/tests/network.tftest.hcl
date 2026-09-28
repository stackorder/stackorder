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

override_resource {
  target          = aws_security_group.alb
  override_during = plan
  values = {
    id = "sg-00000000000000a1b"
  }
}

override_resource {
  target          = aws_security_group.service
  override_during = plan
  values = {
    id = "sg-00000000000000c2d"
  }
}

override_resource {
  target          = aws_security_group.db
  override_during = plan
  values = {
    id = "sg-00000000000000e3f"
  }
}

variables {
  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
}

run "created_vpc" {
  command = plan

  assert {
    condition     = aws_vpc.this[0].cidr_block == "10.0.0.0/16" && aws_vpc.this[0].enable_dns_hostnames
    error_message = "The VPC must use vpc_cidr with DNS host names."
  }

  assert {
    condition     = [for s in aws_subnet.public : s.availability_zone] == ["eu-west-1a", "eu-west-1b"]
    error_message = "The first two available zones must be used."
  }

  assert {
    condition     = [for s in aws_subnet.public : s.cidr_block] == ["10.0.0.0/19", "10.0.32.0/19"]
    error_message = "Public subnets must come from the first half of the VPC."
  }

  assert {
    condition     = [for s in aws_subnet.private : s.cidr_block] == ["10.0.128.0/19", "10.0.160.0/19"]
    error_message = "Private subnets must come from the second half of the VPC."
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 1 && length(aws_route_table.private) == 1 && length(aws_route_table_association.private) == 2
    error_message = "One NAT gateway must serve both private subnets."
  }

  assert {
    condition     = length(aws_default_security_group.this) == 1
    error_message = "The created VPC's default security group must be adopted, which strips its rules."
  }
}

run "nat_per_zone" {
  command = plan

  variables {
    single_nat_gateway = false
    availability_zones = ["eu-west-1a", "eu-west-1b", "eu-west-1c"]
  }

  assert {
    condition     = length(aws_subnet.public) == 3 && length(aws_subnet.private) == 3
    error_message = "One public and one private subnet per zone."
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 3 && length(aws_route_table.private) == 3
    error_message = "single_nat_gateway = false must create a NAT gateway and route table per zone."
  }
}

run "existing_vpc" {
  command = plan

  variables {
    create_vpc         = false
    vpc_id             = "vpc-0123456789abcdef0"
    public_subnet_ids  = ["subnet-0000000000000000a", "subnet-0000000000000000b"]
    private_subnet_ids = ["subnet-0000000000000000c", "subnet-0000000000000000d"]
  }

  assert {
    condition     = length(aws_vpc.this) == 0 && length(aws_subnet.public) == 0 && length(aws_nat_gateway.this) == 0 && length(aws_internet_gateway.this) == 0
    error_message = "No network must be created for an existing VPC."
  }

  assert {
    condition     = alltrue([for sg in [aws_security_group.alb, aws_security_group.service, aws_security_group.db] : sg.vpc_id == "vpc-0123456789abcdef0"])
    error_message = "Security groups must be created in the supplied VPC."
  }

  assert {
    condition     = toset(aws_lb.this.subnets) == toset(["subnet-0000000000000000a", "subnet-0000000000000000b"])
    error_message = "The load balancer must use the public subnets."
  }

  assert {
    condition     = toset(one(aws_ecs_service.this.network_configuration).subnets) == toset(["subnet-0000000000000000c", "subnet-0000000000000000d"])
    error_message = "Tasks must run in the private subnets."
  }

  assert {
    condition     = toset(aws_db_subnet_group.this.subnet_ids) == toset(["subnet-0000000000000000c", "subnet-0000000000000000d"])
    error_message = "The database must live in the private subnets."
  }
}

run "security_groups" {
  command = plan

  assert {
    condition = (
      aws_vpc_security_group_ingress_rule.service_from_alb.referenced_security_group_id == "sg-00000000000000a1b" &&
      aws_vpc_security_group_ingress_rule.service_from_alb.from_port == 8080 &&
      aws_vpc_security_group_ingress_rule.service_from_alb.to_port == 8080
    )
    error_message = "The service must accept 8080 from the load balancer only."
  }

  assert {
    condition = (
      aws_vpc_security_group_egress_rule.service_https.cidr_ipv4 == "0.0.0.0/0" &&
      aws_vpc_security_group_egress_rule.service_https.from_port == 443 &&
      aws_vpc_security_group_egress_rule.service_https.to_port == 443
    )
    error_message = "The service may reach HTTPS anywhere, which covers api.github.com."
  }

  assert {
    condition = (
      aws_vpc_security_group_egress_rule.service_to_db.referenced_security_group_id == "sg-00000000000000e3f" &&
      aws_vpc_security_group_egress_rule.service_to_db.from_port == 5432
    )
    error_message = "The service may reach PostgreSQL on the database group only."
  }

  assert {
    condition = (
      aws_vpc_security_group_ingress_rule.db_from_service.security_group_id == "sg-00000000000000e3f" &&
      aws_vpc_security_group_ingress_rule.db_from_service.referenced_security_group_id == "sg-00000000000000c2d" &&
      aws_vpc_security_group_ingress_rule.db_from_service.from_port == 5432
    )
    error_message = "The database must accept 5432 from the service only."
  }

  assert {
    condition = (
      aws_vpc_security_group_egress_rule.alb_to_service.referenced_security_group_id == "sg-00000000000000c2d" &&
      aws_vpc_security_group_egress_rule.alb_to_service.from_port == 8080
    )
    error_message = "The load balancer may only reach the service on 8080."
  }

  assert {
    condition     = output.security_group_ids == { alb = "sg-00000000000000a1b", service = "sg-00000000000000c2d", db = "sg-00000000000000e3f" }
    error_message = "security_group_ids must expose the three groups."
  }
}

run "webhook_ranges_only" {
  command = plan

  variables {
    github_webhook_ip_ranges_only = true
    admin_cidrs                   = ["198.51.100.0/24"]
  }

  assert {
    condition     = data.http.github_meta[0].url == "https://api.github.com/meta"
    error_message = "The hook ranges must come from the GitHub meta API."
  }

  assert {
    condition = toset(compact([for r in aws_vpc_security_group_ingress_rule.alb : r.cidr_ipv4])) == toset([
      "192.30.252.0/22", "185.199.108.0/22", "140.82.112.0/20", "143.55.64.0/20", "198.51.100.0/24",
    ])
    error_message = "IPv4 ingress must be the hook ranges plus admin_cidrs."
  }

  assert {
    condition     = toset(compact([for r in aws_vpc_security_group_ingress_rule.alb : r.cidr_ipv6])) == toset(["2a0a:a440::/29", "2606:50c0::/32"])
    error_message = "IPv6 hook ranges must be allowed."
  }

  assert {
    condition     = !contains([for r in aws_vpc_security_group_ingress_rule.alb : r.cidr_ipv4], "0.0.0.0/0")
    error_message = "ingress_cidrs must be ignored in hook ranges mode."
  }
}

run "enterprise_server_meta" {
  command = plan

  variables {
    github_api_url                = "https://github.example.com/api/v3"
    github_webhook_ip_ranges_only = true
  }

  assert {
    condition     = data.http.github_meta[0].url == "https://github.example.com/api/v3/meta"
    error_message = "The meta endpoint must follow github_api_url."
  }

  assert {
    condition     = local.environment["GITHUB_API_URL"] == "https://github.example.com/api/v3"
    error_message = "GITHUB_API_URL must follow github_api_url."
  }
}

run "metrics_allowed" {
  command = plan

  variables {
    metrics_allowed_cidrs = ["10.0.0.0/8"]
  }

  assert {
    condition = (
      toset(flatten([for c in aws_lb_listener_rule.metrics_allow[0].condition : [for s in c.source_ip : s.values]])) == toset(["10.0.0.0/8"]) &&
      aws_lb_listener_rule.metrics_allow[0].priority < aws_lb_listener_rule.metrics_deny.priority
    )
    error_message = "metrics_allowed_cidrs must be forwarded before the /metrics block."
  }
}
