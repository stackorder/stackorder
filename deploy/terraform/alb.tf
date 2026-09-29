data "http" "github_meta" {
  count = var.github_webhook_ip_ranges_only ? 1 : 0

  url = "${var.github_api_url}/meta"
  request_headers = {
    Accept                 = "application/vnd.github+json"
    "X-GitHub-Api-Version" = "2022-11-28"
  }

  lifecycle {
    postcondition {
      condition     = self.status_code == 200 && length(try(jsondecode(self.response_body).hooks, [])) > 0
      error_message = "The GitHub meta API did not return a hooks list; check github_api_url."
    }
  }
}

locals {
  github_hook_cidrs = var.github_webhook_ip_ranges_only ? tolist(jsondecode(data.http.github_meta[0].response_body).hooks) : []
  alb_ingress_cidrs = var.github_webhook_ip_ranges_only ? distinct(concat(local.github_hook_cidrs, var.admin_cidrs)) : distinct(var.ingress_cidrs)
  alb_ingress_rules = merge([
    for port in [80, 443] : {
      for cidr in local.alb_ingress_cidrs : "${port}-${cidr}" => { port = port, cidr = cidr }
    }
  ]...)

  certificate_arn = var.certificate_arn != null ? var.certificate_arn : one(aws_acm_certificate_validation.this[*].certificate_arn)
}

resource "aws_lb" "this" {
  name                       = var.name
  load_balancer_type         = "application"
  internal                   = false
  security_groups            = [aws_security_group.alb.id]
  subnets                    = local.public_subnet_ids
  drop_invalid_header_fields = true
  enable_http2               = true
  idle_timeout               = 60

  dynamic "access_logs" {
    for_each = aws_s3_bucket.alb_logs[*].bucket

    content {
      bucket  = access_logs.value
      enabled = true
    }
  }

  tags = var.tags

  depends_on = [
    aws_s3_bucket_policy.alb_logs,
    aws_s3_bucket_server_side_encryption_configuration.alb_logs,
  ]
}

resource "aws_lb_target_group" "this" {
  name_prefix          = "${substr(var.name, 0, 5)}-"
  vpc_id               = local.vpc_id
  target_type          = "ip"
  protocol             = "HTTP"
  protocol_version     = "HTTP1"
  port                 = local.container_port
  deregistration_delay = 30

  health_check {
    enabled             = true
    path                = "/readyz"
    port                = "traffic-port"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  tags = var.tags

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"

    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }

  tags = var.tags
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = var.ssl_policy
  certificate_arn   = local.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }

  tags = var.tags

  lifecycle {
    precondition {
      condition     = (var.route53_zone_id == null) != (var.certificate_arn == null)
      error_message = "Set exactly one of route53_zone_id (the module issues and validates a certificate and creates the DNS record) or certificate_arn (an existing certificate; DNS is left to you)."
    }
  }
}
