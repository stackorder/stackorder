mock_provider "aws" {
  override_during = plan
  source          = "./tests/mocks/aws"
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

  assert {
    condition     = length(aws_route.private_nat) == 3 && length(aws_route_table_association.private) == 3
    error_message = "Each private route table must get a default route through its zone's NAT gateway."
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
    condition     = one(aws_ecs_service.this.network_configuration).assign_public_ip == false
    error_message = "Tasks in private subnets must not get public IPs."
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

run "webhook_ranges_only_without_hooks" {
  command = plan

  variables {
    github_webhook_ip_ranges_only = true
    admin_cidrs                   = ["198.51.100.0/24"]
  }

  override_data {
    target = data.http.github_meta[0]
    values = {
      status_code   = 200
      response_body = "{\"web\":[\"192.30.252.0/22\"]}"
    }
  }

  expect_failures = [data.http.github_meta]
}

run "webhook_ranges_only_meta_error" {
  command = plan

  variables {
    github_webhook_ip_ranges_only = true
    admin_cidrs                   = ["198.51.100.0/24"]
  }

  override_data {
    target = data.http.github_meta[0]
    values = {
      status_code   = 503
      response_body = "{\"hooks\":[\"192.30.252.0/22\"]}"
    }
  }

  expect_failures = [data.http.github_meta]
}

run "alb_access_logs" {
  command = plan

  variables {
    alb_access_logs_enabled = true
  }

  override_resource {
    target          = aws_s3_bucket.alb_logs[0]
    override_during = plan
    values = {
      arn = "arn:aws:s3:::stackorder-alb-logs-123456789012-eu-west-1"
    }
  }

  assert {
    condition     = aws_s3_bucket.alb_logs[0].bucket == "stackorder-alb-logs-123456789012-eu-west-1" && output.alb_access_logs_bucket == "stackorder-alb-logs-123456789012-eu-west-1"
    error_message = "The access log bucket must be named after the deployment, account and region."
  }

  assert {
    condition = (
      one(aws_lb.this.access_logs).enabled &&
      one(aws_lb.this.access_logs).bucket == "stackorder-alb-logs-123456789012-eu-west-1" &&
      one(aws_lb.this.access_logs).prefix == null
    )
    error_message = "The load balancer must write access logs to the bucket, without a prefix."
  }

  assert {
    condition = jsondecode(aws_s3_bucket_policy.alb_logs[0].policy).Statement[0] == {
      Sid       = "AllowLoadBalancerLogDelivery"
      Effect    = "Allow"
      Principal = { Service = "logdelivery.elasticloadbalancing.amazonaws.com" }
      Action    = "s3:PutObject"
      Resource  = "arn:aws:s3:::stackorder-alb-logs-123456789012-eu-west-1/AWSLogs/123456789012/*"
      Condition = {
        ArnLike = { "aws:SourceArn" = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:loadbalancer/app/stackorder/*" }
      }
    }
    error_message = "Only ELB log delivery for this account's load balancer may write, and only under AWSLogs/<account>."
  }

  assert {
    condition = (
      jsondecode(aws_s3_bucket_policy.alb_logs[0].policy).Statement[1].Effect == "Deny" &&
      jsondecode(aws_s3_bucket_policy.alb_logs[0].policy).Statement[1].Condition.Bool["aws:SecureTransport"] == "false"
    )
    error_message = "The access log bucket must deny insecure transport."
  }

  assert {
    condition = (
      one(one(aws_s3_bucket_server_side_encryption_configuration.alb_logs[0].rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256" &&
      one(one(aws_s3_bucket_lifecycle_configuration.alb_logs[0].rule).expiration).days == 90 &&
      one(aws_s3_bucket_ownership_controls.alb_logs[0].rule).object_ownership == "BucketOwnerEnforced"
    )
    error_message = "The access log bucket must use SSE-S3, which ELB log delivery requires, expire logs after 90 days and disable ACLs."
  }

  assert {
    condition = (
      aws_s3_bucket_public_access_block.alb_logs[0].block_public_acls &&
      aws_s3_bucket_public_access_block.alb_logs[0].block_public_policy &&
      aws_s3_bucket_public_access_block.alb_logs[0].ignore_public_acls &&
      aws_s3_bucket_public_access_block.alb_logs[0].restrict_public_buckets
    )
    error_message = "Public access to the access log bucket must be blocked."
  }
}

run "alb_access_logs_retention" {
  command = plan

  variables {
    alb_access_logs_enabled        = true
    alb_access_logs_retention_days = 400
  }

  assert {
    condition     = one(one(aws_s3_bucket_lifecycle_configuration.alb_logs[0].rule).expiration).days == 400
    error_message = "alb_access_logs_retention_days must set the expiry."
  }
}

run "waf_web_acl" {
  command = plan

  variables {
    waf_web_acl_arn = "arn:aws:wafv2:eu-west-1:123456789012:regional/webacl/stackorder/a1b2c3d4-5678-90ab-cdef-EXAMPLE11111"
  }

  override_resource {
    target          = aws_lb.this
    override_during = plan
    values = {
      arn = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:loadbalancer/app/stackorder/50dc6c495c0c9188"
    }
  }

  assert {
    condition = (
      aws_wafv2_web_acl_association.this[0].resource_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:loadbalancer/app/stackorder/50dc6c495c0c9188" &&
      aws_wafv2_web_acl_association.this[0].web_acl_arn == "arn:aws:wafv2:eu-west-1:123456789012:regional/webacl/stackorder/a1b2c3d4-5678-90ab-cdef-EXAMPLE11111"
    )
    error_message = "The web ACL must be associated with the load balancer."
  }
}

run "waf_web_acl_other_region" {
  command = plan

  variables {
    waf_web_acl_arn = "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/stackorder/a1b2c3d4-5678-90ab-cdef-EXAMPLE11111"
  }

  expect_failures = [aws_wafv2_web_acl_association.this]
}

run "public_tasks" {
  command = plan

  variables {
    public_tasks = true
  }

  override_resource {
    target          = aws_subnet.public[0]
    override_during = plan
    values = {
      id = "subnet-00000000000000p0a"
    }
  }

  override_resource {
    target          = aws_subnet.public[1]
    override_during = plan
    values = {
      id = "subnet-00000000000000p1b"
    }
  }

  override_resource {
    target          = aws_subnet.private[0]
    override_during = plan
    values = {
      id = "subnet-00000000000000q0a"
    }
  }

  override_resource {
    target          = aws_subnet.private[1]
    override_during = plan
    values = {
      id = "subnet-00000000000000q1b"
    }
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 0 && length(aws_eip.nat) == 0 && length(aws_route.private_nat) == 0
    error_message = "public_tasks must create no NAT gateway, Elastic IP or NAT route."
  }

  assert {
    condition     = length(aws_subnet.public) == 2 && length(aws_route.public_internet) == 1 && length(aws_route_table_association.public) == 2
    error_message = "The public subnets and their internet route must remain."
  }

  assert {
    condition     = length(aws_subnet.private) == 2 && length(aws_route_table.private) == 1 && length(aws_route_table_association.private) == 2
    error_message = "The private subnets must remain, for the database, with a route table of their own."
  }

  assert {
    condition     = one(aws_ecs_service.this.network_configuration).assign_public_ip == true
    error_message = "Tasks in the public subnets need public IPs to reach ghcr.io, GitHub and the AWS endpoints."
  }

  assert {
    condition     = toset(one(aws_ecs_service.this.network_configuration).subnets) == toset(["subnet-00000000000000p0a", "subnet-00000000000000p1b"])
    error_message = "Tasks must run in the public subnets."
  }

  assert {
    condition     = toset(aws_db_subnet_group.this.subnet_ids) == toset(["subnet-00000000000000q0a", "subnet-00000000000000q1b"])
    error_message = "The database must stay in the private subnets."
  }

  assert {
    condition = (
      one(aws_ecs_service.this.network_configuration).security_groups == toset(["sg-00000000000000c2d"]) &&
      aws_vpc_security_group_ingress_rule.service_from_alb.security_group_id == "sg-00000000000000c2d" &&
      aws_vpc_security_group_ingress_rule.service_from_alb.referenced_security_group_id == "sg-00000000000000a1b"
    )
    error_message = "With public IPs the service must still admit traffic from the load balancer only."
  }
}

run "public_tasks_nat_per_zone" {
  command = plan

  variables {
    public_tasks       = true
    single_nat_gateway = false
    availability_zones = ["eu-west-1a", "eu-west-1b", "eu-west-1c"]
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 0 && length(aws_eip.nat) == 0 && length(aws_route.private_nat) == 0
    error_message = "public_tasks must create no NAT gateway whatever single_nat_gateway says."
  }

  assert {
    condition     = length(aws_route_table.private) == 3 && length(aws_route_table_association.private) == 3
    error_message = "Private route tables must follow single_nat_gateway, not the NAT gateway count, so every private subnet keeps an association."
  }
}

run "public_tasks_existing_vpc" {
  command = plan

  variables {
    public_tasks       = true
    create_vpc         = false
    vpc_id             = "vpc-0123456789abcdef0"
    public_subnet_ids  = ["subnet-0000000000000000a", "subnet-0000000000000000b"]
    private_subnet_ids = ["subnet-0000000000000000c", "subnet-0000000000000000d"]
  }

  assert {
    condition     = toset(one(aws_ecs_service.this.network_configuration).subnets) == toset(["subnet-0000000000000000a", "subnet-0000000000000000b"])
    error_message = "With an existing VPC, public_tasks must run the tasks in public_subnet_ids."
  }

  assert {
    condition     = one(aws_ecs_service.this.network_configuration).assign_public_ip
    error_message = "Tasks in public subnets need public IPs."
  }

  assert {
    condition     = toset(aws_db_subnet_group.this.subnet_ids) == toset(["subnet-0000000000000000c", "subnet-0000000000000000d"])
    error_message = "The database must stay in private_subnet_ids."
  }
}

run "public_tasks_existing_vpc_needs_private_subnets" {
  command = plan

  variables {
    public_tasks      = true
    create_vpc        = false
    vpc_id            = "vpc-0123456789abcdef0"
    public_subnet_ids = ["subnet-0000000000000000a", "subnet-0000000000000000b"]
  }

  expect_failures = [var.private_subnet_ids]
}
