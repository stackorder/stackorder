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
  target          = aws_lb.this
  override_during = plan
  values = {
    arn = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:loadbalancer/app/stackorder/50dc6c495c0c9188"
  }
}

override_resource {
  target          = aws_lb_listener.https
  override_during = plan
  values = {
    arn = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener/app/stackorder/50dc6c495c0c9188/f2f7dc8efc522ab2"
  }
}

override_resource {
  target          = aws_lb_listener.http
  override_during = plan
  values = {
    arn = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener/app/stackorder/50dc6c495c0c9188/0467ef3c8400ae65"
  }
}

override_resource {
  target          = aws_lb_target_group.this
  override_during = plan
  values = {
    arn = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/stack-20260930/73e2d6bc24d8a067"
  }
}

variables {
  domain_name     = "stackorder.example.com"
  route53_zone_id = "Z0123456789ABCDEFGHIJ"
}

run "listener_outputs" {
  command = plan

  assert {
    condition = (
      output.alb_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:loadbalancer/app/stackorder/50dc6c495c0c9188" &&
      output.https_listener_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener/app/stackorder/50dc6c495c0c9188/f2f7dc8efc522ab2" &&
      output.http_listener_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener/app/stackorder/50dc6c495c0c9188/0467ef3c8400ae65" &&
      output.target_group_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/stack-20260930/73e2d6bc24d8a067"
    )
    error_message = "The load balancer, both listeners and the target group must be exported, so callers can attach listener rules."
  }

  assert {
    condition = (
      length(aws_lb_listener.https.default_action) == 1 &&
      aws_lb_listener.https.default_action[0].type == "forward"
    )
    error_message = "Without authentication the HTTPS listener must only forward."
  }

  assert {
    condition     = length(aws_lb_listener_rule.bypass) == 0
    error_message = "Without authentication the module must add no listener rules."
  }

  assert {
    condition     = length(aws_vpc_security_group_egress_rule.alb_https) == 0
    error_message = "Without authentication or alb_https_egress_cidrs the load balancer must reach nothing but the service."
  }
}

run "oidc_authentication" {
  command = plan

  variables {
    oidc_authentication = {
      issuer                 = "https://accounts.google.com"
      authorization_endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
      token_endpoint         = "https://oauth2.googleapis.com/token"
      user_info_endpoint     = "https://openidconnect.googleapis.com/v1/userinfo"
      client_id              = "123456789012-abcdefghijklmnopqrstuvwxyz012345.apps.googleusercontent.com"
      client_secret          = "GOCSPX-mocksecret"
      scope                  = "openid email"
      authentication_request_extra_params = {
        hd = "acme.com"
      }
    }
  }

  assert {
    condition = (
      length(aws_lb_listener.https.default_action) == 2 &&
      aws_lb_listener.https.default_action[0].type == "authenticate-oidc" &&
      aws_lb_listener.https.default_action[0].order == 1 &&
      aws_lb_listener.https.default_action[1].type == "forward" &&
      aws_lb_listener.https.default_action[1].order == 2 &&
      aws_lb_listener.https.default_action[1].target_group_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/stack-20260930/73e2d6bc24d8a067"
    )
    error_message = "The HTTPS listener must authenticate, then forward to the target group."
  }

  assert {
    condition = (
      aws_lb_listener.https.default_action[0].authenticate_oidc[0].issuer == "https://accounts.google.com" &&
      aws_lb_listener.https.default_action[0].authenticate_oidc[0].token_endpoint == "https://oauth2.googleapis.com/token" &&
      aws_lb_listener.https.default_action[0].authenticate_oidc[0].scope == "openid email" &&
      aws_lb_listener.https.default_action[0].authenticate_oidc[0].authentication_request_extra_params == tomap({ hd = "acme.com" }) &&
      nonsensitive(aws_lb_listener.https.default_action[0].authenticate_oidc[0].client_secret) == "GOCSPX-mocksecret"
    )
    error_message = "oidc_authentication must reach the authenticate_oidc action."
  }

  assert {
    condition     = !issensitive(aws_lb_listener.https.default_action[0].authenticate_oidc[0].issuer)
    error_message = "Only the client secret may be hidden from the listener diff."
  }

  assert {
    condition = {
      for k, r in aws_lb_listener_rule.bypass : k => r.priority
      } == {
      webhooks = 1
      health   = 2
      runs     = 3
      cli      = 4
      metrics  = 5
    }
    error_message = "The module must add its five bypass rules at priorities 1 to 5."
  }

  assert {
    condition = alltrue(flatten([
      for r in aws_lb_listener_rule.bypass : [
        length(flatten([for c in r.condition : [c.path_pattern[*].values, c.http_request_method[*].values, c.http_header[*].values]])) <= 5,
        [for c in r.condition : length(flatten([c.path_pattern[*].values, c.http_request_method[*].values, c.http_header[*].values])) <= 3],
      ]
    ]))
    error_message = "Application Load Balancers accept at most three values per condition and five per rule."
  }

  assert {
    condition = alltrue([
      for r in aws_lb_listener_rule.bypass :
      r.listener_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener/app/stackorder/50dc6c495c0c9188/f2f7dc8efc522ab2" &&
      length(r.action) == 1 &&
      r.action[0].type == "forward" &&
      r.action[0].target_group_arn == "arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/stack-20260930/73e2d6bc24d8a067"
    ])
    error_message = "Every bypass rule must forward straight to the target group, without authentication."
  }

  assert {
    condition = (
      toset(one([for c in aws_lb_listener_rule.bypass["webhooks"].condition : c.path_pattern[0].values if length(c.path_pattern) > 0])) == toset(["/webhooks/github"]) &&
      toset(one([for c in aws_lb_listener_rule.bypass["webhooks"].condition : c.http_request_method[0].values if length(c.http_request_method) > 0])) == toset(["POST"]) &&
      length([for c in aws_lb_listener_rule.bypass["webhooks"].condition : c if length(c.http_header) > 0]) == 0
    )
    error_message = "GitHub webhook deliveries, which carry a signature instead of a bearer token, must bypass on POST /webhooks/github."
  }

  assert {
    condition = (
      toset(one([for c in aws_lb_listener_rule.bypass["health"].condition : c.path_pattern[0].values if length(c.path_pattern) > 0])) == toset(["/healthz", "/readyz"]) &&
      toset(one([for c in aws_lb_listener_rule.bypass["health"].condition : c.http_request_method[0].values if length(c.http_request_method) > 0])) == toset(["GET", "HEAD"])
    )
    error_message = "Health checks must bypass on GET and HEAD of /healthz and /readyz."
  }

  assert {
    condition = (
      toset(one([for c in aws_lb_listener_rule.bypass["runs"].condition : c.path_pattern[0].values if length(c.path_pattern) > 0])) == toset(["/v1/runs", "/v1/runs/*"]) &&
      toset(one([for c in aws_lb_listener_rule.bypass["cli"].condition : c.path_pattern[0].values if length(c.path_pattern) > 0])) == toset(["/v1/unlock", "/v1/me"]) &&
      alltrue([
        for k in ["runs", "cli"] :
        length([for c in aws_lb_listener_rule.bypass[k].condition : c if length(c.http_request_method) > 0]) == 0 &&
        one([for c in aws_lb_listener_rule.bypass[k].condition : c.http_header[0] if length(c.http_header) > 0]).http_header_name == "Authorization" &&
        toset(one([for c in aws_lb_listener_rule.bypass[k].condition : c.http_header[0] if length(c.http_header) > 0]).values) == toset(["Bearer *"])
      ])
    )
    error_message = "Runner and CLI calls must bypass only on the endpoints the client uses, and only with a bearer token."
  }

  assert {
    condition = (
      toset(one([for c in aws_lb_listener_rule.bypass["metrics"].condition : c.path_pattern[0].values if length(c.path_pattern) > 0])) == toset(["/metrics"]) &&
      one([for c in aws_lb_listener_rule.bypass["metrics"].condition : c.http_header[0] if length(c.http_header) > 0]).http_header_name == "Authorization"
    )
    error_message = "Prometheus must bypass on /metrics with its bearer token."
  }

  assert {
    condition = alltrue([
      for r in aws_lb_listener_rule.bypass : alltrue([
        for c in r.condition : length(c.path_pattern) == 0 || alltrue([
          for p in c.path_pattern[0].values : !startswith(p, "/auth") && !startswith(p, "/setup") && p != "/" && p != "/*"
        ])
      ])
    ])
    error_message = "The UI, the GitHub sign-in and the setup pages must stay behind the load balancer's authentication."
  }

  assert {
    condition = (
      keys(aws_vpc_security_group_egress_rule.alb_https) == ["0.0.0.0/0"] &&
      aws_vpc_security_group_egress_rule.alb_https["0.0.0.0/0"].cidr_ipv4 == "0.0.0.0/0" &&
      aws_vpc_security_group_egress_rule.alb_https["0.0.0.0/0"].from_port == 443 &&
      aws_vpc_security_group_egress_rule.alb_https["0.0.0.0/0"].to_port == 443 &&
      aws_vpc_security_group_egress_rule.alb_https["0.0.0.0/0"].ip_protocol == "tcp"
    )
    error_message = "With authentication and no alb_https_egress_cidrs the load balancer must reach HTTPS anywhere, to call the identity provider."
  }
}

run "cognito_authentication" {
  command = plan

  variables {
    cognito_authentication = {
      user_pool_arn       = "arn:aws:cognito-idp:eu-west-1:123456789012:userpool/eu-west-1_AbCdEfGhI"
      user_pool_client_id = "1example23456789"
      user_pool_domain    = "acme-stackorder"
      session_timeout     = 43200
    }
  }

  assert {
    condition = (
      aws_lb_listener.https.default_action[0].type == "authenticate-cognito" &&
      aws_lb_listener.https.default_action[0].authenticate_cognito[0].user_pool_arn == "arn:aws:cognito-idp:eu-west-1:123456789012:userpool/eu-west-1_AbCdEfGhI" &&
      aws_lb_listener.https.default_action[0].authenticate_cognito[0].user_pool_domain == "acme-stackorder" &&
      aws_lb_listener.https.default_action[0].authenticate_cognito[0].session_timeout == 43200 &&
      aws_lb_listener.https.default_action[1].type == "forward"
    )
    error_message = "cognito_authentication must put an authenticate_cognito action before the forward."
  }

  assert {
    condition     = length(aws_lb_listener_rule.bypass) == 5
    error_message = "Cognito authentication must get the same bypass rules."
  }
}

run "oidc_and_cognito" {
  command = plan

  variables {
    oidc_authentication = {
      issuer                 = "https://accounts.google.com"
      authorization_endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
      token_endpoint         = "https://oauth2.googleapis.com/token"
      user_info_endpoint     = "https://openidconnect.googleapis.com/v1/userinfo"
      client_id              = "client"
      client_secret          = "secret"
    }
    cognito_authentication = {
      user_pool_arn       = "arn:aws:cognito-idp:eu-west-1:123456789012:userpool/eu-west-1_AbCdEfGhI"
      user_pool_client_id = "1example23456789"
      user_pool_domain    = "acme-stackorder"
    }
  }

  expect_failures = [var.cognito_authentication]
}

run "oidc_plain_http_endpoint" {
  command = plan

  variables {
    oidc_authentication = {
      issuer                 = "https://idp.example.com"
      authorization_endpoint = "https://idp.example.com/authorize"
      token_endpoint         = "http://idp.example.com/token"
      user_info_endpoint     = "https://idp.example.com/userinfo"
      client_id              = "client"
      client_secret          = "secret"
    }
  }

  expect_failures = [var.oidc_authentication]
}

run "oidc_unknown_unauthenticated_behaviour" {
  command = plan

  variables {
    oidc_authentication = {
      issuer                     = "https://idp.example.com"
      authorization_endpoint     = "https://idp.example.com/authorize"
      token_endpoint             = "https://idp.example.com/token"
      user_info_endpoint         = "https://idp.example.com/userinfo"
      client_id                  = "client"
      client_secret              = "secret"
      on_unauthenticated_request = "redirect"
    }
  }

  expect_failures = [var.oidc_authentication]
}

run "cognito_bad_user_pool_arn" {
  command = plan

  variables {
    cognito_authentication = {
      user_pool_arn       = "eu-west-1_AbCdEfGhI"
      user_pool_client_id = "1example23456789"
      user_pool_domain    = "acme-stackorder"
    }
  }

  expect_failures = [var.cognito_authentication]
}

run "cognito_with_egress_cidrs" {
  command = plan

  variables {
    cognito_authentication = {
      user_pool_arn       = "arn:aws:cognito-idp:eu-west-1:123456789012:userpool/eu-west-1_AbCdEfGhI"
      user_pool_client_id = "1example23456789"
      user_pool_domain    = "acme-stackorder"
    }
    alb_https_egress_cidrs = ["198.51.100.0/24", "2001:db8::/32"]
  }

  assert {
    condition = (
      toset(compact([for r in aws_vpc_security_group_egress_rule.alb_https : r.cidr_ipv4])) == toset(["198.51.100.0/24"]) &&
      toset(compact([for r in aws_vpc_security_group_egress_rule.alb_https : r.cidr_ipv6])) == toset(["2001:db8::/32"]) &&
      alltrue([for r in aws_vpc_security_group_egress_rule.alb_https : r.from_port == 443 && r.to_port == 443])
    )
    error_message = "alb_https_egress_cidrs must replace the default, with IPv4 and IPv6 CIDRs on port 443."
  }
}

run "egress_cidrs_without_authentication" {
  command = plan

  variables {
    alb_https_egress_cidrs = ["203.0.113.10/32"]
  }

  assert {
    condition     = keys(aws_vpc_security_group_egress_rule.alb_https) == ["203.0.113.10/32"]
    error_message = "alb_https_egress_cidrs must apply without authentication too, for listener rules of the caller's own."
  }
}

run "egress_cidrs_invalid" {
  command = plan

  variables {
    alb_https_egress_cidrs = ["accounts.google.com"]
  }

  expect_failures = [var.alb_https_egress_cidrs]
}
