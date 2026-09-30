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

  alb_authentication = nonsensitive(var.oidc_authentication != null) || var.cognito_authentication != null
  alb_bypass_rules = {
    webhooks = { priority = 1, paths = ["/webhooks/github"], methods = ["POST"], bearer = false }
    health   = { priority = 2, paths = ["/healthz", "/readyz"], methods = ["GET", "HEAD"], bearer = false }
    api      = { priority = 3, paths = ["/v1/runs", "/v1/runs/*", "/v1/unlock", "/v1/me"], methods = [], bearer = true }
    metrics  = { priority = 4, paths = ["/metrics"], methods = ["GET"], bearer = true }
  }
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

  access_logs {
    bucket  = var.alb_access_logs_enabled ? aws_s3_bucket.alb_logs[0].bucket : ""
    enabled = var.alb_access_logs_enabled
  }

  tags = var.tags

  depends_on = [
    aws_s3_bucket_policy.alb_logs,
    aws_s3_bucket_server_side_encryption_configuration.alb_logs,
  ]
}

resource "aws_wafv2_web_acl_association" "this" {
  count = var.waf_web_acl_arn == null ? 0 : 1

  resource_arn = aws_lb.this.arn
  web_acl_arn  = var.waf_web_acl_arn

  lifecycle {
    precondition {
      condition     = split(":", var.waf_web_acl_arn)[3] == local.region
      error_message = "waf_web_acl_arn must name a web ACL in the load balancer's region."
    }
  }
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

  dynamic "default_action" {
    for_each = nonsensitive(var.oidc_authentication != null) ? [1] : []

    content {
      type  = "authenticate-oidc"
      order = 1

      authenticate_oidc {
        issuer                              = nonsensitive(var.oidc_authentication.issuer)
        authorization_endpoint              = nonsensitive(var.oidc_authentication.authorization_endpoint)
        token_endpoint                      = nonsensitive(var.oidc_authentication.token_endpoint)
        user_info_endpoint                  = nonsensitive(var.oidc_authentication.user_info_endpoint)
        client_id                           = nonsensitive(var.oidc_authentication.client_id)
        client_secret                       = var.oidc_authentication.client_secret
        scope                               = nonsensitive(var.oidc_authentication.scope)
        session_cookie_name                 = nonsensitive(var.oidc_authentication.session_cookie_name)
        session_timeout                     = nonsensitive(var.oidc_authentication.session_timeout)
        on_unauthenticated_request          = nonsensitive(var.oidc_authentication.on_unauthenticated_request)
        authentication_request_extra_params = nonsensitive(var.oidc_authentication.authentication_request_extra_params)
      }
    }
  }

  dynamic "default_action" {
    for_each = var.cognito_authentication == null ? [] : [var.cognito_authentication]

    content {
      type  = "authenticate-cognito"
      order = 1

      authenticate_cognito {
        user_pool_arn                       = default_action.value.user_pool_arn
        user_pool_client_id                 = default_action.value.user_pool_client_id
        user_pool_domain                    = default_action.value.user_pool_domain
        scope                               = default_action.value.scope
        session_cookie_name                 = default_action.value.session_cookie_name
        session_timeout                     = default_action.value.session_timeout
        on_unauthenticated_request          = default_action.value.on_unauthenticated_request
        authentication_request_extra_params = default_action.value.authentication_request_extra_params
      }
    }
  }

  default_action {
    type             = "forward"
    order            = local.alb_authentication ? 2 : null
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

resource "aws_lb_listener_rule" "bypass" {
  for_each = { for k, v in local.alb_bypass_rules : k => v if local.alb_authentication }

  listener_arn = aws_lb_listener.https.arn
  priority     = each.value.priority

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }

  condition {
    path_pattern {
      values = each.value.paths
    }
  }

  dynamic "condition" {
    for_each = length(each.value.methods) > 0 ? [each.value.methods] : []

    content {
      http_request_method {
        values = condition.value
      }
    }
  }

  dynamic "condition" {
    for_each = each.value.bearer ? [1] : []

    content {
      http_header {
        http_header_name = "Authorization"
        values           = ["Bearer *"]
      }
    }
  }

  tags = merge(var.tags, { Name = "${var.name}-${each.key}" })
}
