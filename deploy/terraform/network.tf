data "aws_availability_zones" "available" {
  count = var.create_vpc && length(var.availability_zones) == 0 ? 1 : 0

  state = "available"

  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}

locals {
  azs = var.create_vpc ? (
    length(var.availability_zones) > 0 ? var.availability_zones : slice(sort(data.aws_availability_zones.available[0].names), 0, 2)
  ) : []
  nat_gateway_count = var.create_vpc ? (var.single_nat_gateway ? 1 : length(local.azs)) : 0

  vpc_id             = var.create_vpc ? aws_vpc.this[0].id : var.vpc_id
  public_subnet_ids  = var.create_vpc ? aws_subnet.public[*].id : var.public_subnet_ids
  private_subnet_ids = var.create_vpc ? aws_subnet.private[*].id : var.private_subnet_ids
}

resource "aws_vpc" "this" {
  count = var.create_vpc ? 1 : 0

  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(var.tags, { Name = var.name })
}

resource "aws_default_security_group" "this" {
  count = var.create_vpc ? 1 : 0

  vpc_id = aws_vpc.this[0].id

  tags = merge(var.tags, { Name = "${var.name}-default" })
}

resource "aws_internet_gateway" "this" {
  count = var.create_vpc ? 1 : 0

  vpc_id = aws_vpc.this[0].id

  tags = merge(var.tags, { Name = var.name })
}

resource "aws_subnet" "public" {
  count = length(local.azs)

  vpc_id            = aws_vpc.this[0].id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 3, count.index)

  tags = merge(var.tags, { Name = "${var.name}-public-${local.azs[count.index]}" })
}

resource "aws_subnet" "private" {
  count = length(local.azs)

  vpc_id            = aws_vpc.this[0].id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 3, count.index + 4)

  tags = merge(var.tags, { Name = "${var.name}-private-${local.azs[count.index]}" })
}

resource "aws_route_table" "public" {
  count = var.create_vpc ? 1 : 0

  vpc_id = aws_vpc.this[0].id

  tags = merge(var.tags, { Name = "${var.name}-public" })
}

resource "aws_route" "public_internet" {
  count = var.create_vpc ? 1 : 0

  route_table_id         = aws_route_table.public[0].id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this[0].id
}

resource "aws_route_table_association" "public" {
  count = length(local.azs)

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

resource "aws_eip" "nat" {
  count = local.nat_gateway_count

  domain = "vpc"

  tags = merge(var.tags, { Name = "${var.name}-nat-${local.azs[count.index]}" })

  depends_on = [aws_internet_gateway.this]
}

resource "aws_nat_gateway" "this" {
  count = local.nat_gateway_count

  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id

  tags = merge(var.tags, { Name = "${var.name}-${local.azs[count.index]}" })

  depends_on = [aws_internet_gateway.this]
}

resource "aws_route_table" "private" {
  count = local.nat_gateway_count

  vpc_id = aws_vpc.this[0].id

  tags = merge(var.tags, { Name = var.single_nat_gateway ? "${var.name}-private" : "${var.name}-private-${local.azs[count.index]}" })
}

resource "aws_route" "private_nat" {
  count = local.nat_gateway_count

  route_table_id         = aws_route_table.private[count.index].id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.this[count.index].id
}

resource "aws_route_table_association" "private" {
  count = length(local.azs)

  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[var.single_nat_gateway ? 0 : count.index].id
}

resource "aws_security_group" "alb" {
  name        = "${var.name}-alb"
  description = "Stackorder load balancer: HTTPS and the HTTP redirect from the allowed CIDRs"
  vpc_id      = local.vpc_id

  tags = merge(var.tags, { Name = "${var.name}-alb" })
}

resource "aws_security_group" "service" {
  name        = "${var.name}-service"
  description = "Stackorder tasks: traffic from the load balancer, egress to GitHub over HTTPS and to the database"
  vpc_id      = local.vpc_id

  tags = merge(var.tags, { Name = "${var.name}-service" })
}

resource "aws_security_group" "db" {
  name        = "${var.name}-db"
  description = "Stackorder database: PostgreSQL from the tasks only"
  vpc_id      = local.vpc_id

  tags = merge(var.tags, { Name = "${var.name}-db" })
}

resource "aws_vpc_security_group_ingress_rule" "alb" {
  for_each = local.alb_ingress_rules

  security_group_id = aws_security_group.alb.id
  description       = each.value.port == 443 ? "HTTPS" : "HTTP redirect to HTTPS"
  ip_protocol       = "tcp"
  from_port         = each.value.port
  to_port           = each.value.port
  cidr_ipv4         = strcontains(each.value.cidr, ":") ? null : each.value.cidr
  cidr_ipv6         = strcontains(each.value.cidr, ":") ? each.value.cidr : null

  tags = var.tags
}

resource "aws_vpc_security_group_egress_rule" "alb_to_service" {
  security_group_id            = aws_security_group.alb.id
  description                  = "Stackorder tasks"
  ip_protocol                  = "tcp"
  from_port                    = local.container_port
  to_port                      = local.container_port
  referenced_security_group_id = aws_security_group.service.id

  tags = var.tags
}

resource "aws_vpc_security_group_ingress_rule" "service_from_alb" {
  security_group_id            = aws_security_group.service.id
  description                  = "Load balancer"
  ip_protocol                  = "tcp"
  from_port                    = local.container_port
  to_port                      = local.container_port
  referenced_security_group_id = aws_security_group.alb.id

  tags = var.tags
}

resource "aws_vpc_security_group_egress_rule" "service_https" {
  security_group_id = aws_security_group.service.id
  description       = "HTTPS to the GitHub API and the AWS endpoints Fargate needs"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"

  tags = var.tags
}

resource "aws_vpc_security_group_egress_rule" "service_to_db" {
  security_group_id            = aws_security_group.service.id
  description                  = "PostgreSQL"
  ip_protocol                  = "tcp"
  from_port                    = local.db_port
  to_port                      = local.db_port
  referenced_security_group_id = aws_security_group.db.id

  tags = var.tags
}

resource "aws_vpc_security_group_ingress_rule" "db_from_service" {
  security_group_id            = aws_security_group.db.id
  description                  = "Stackorder tasks"
  ip_protocol                  = "tcp"
  from_port                    = local.db_port
  to_port                      = local.db_port
  referenced_security_group_id = aws_security_group.service.id

  tags = var.tags
}
