locals {
  manage_dns = var.route53_zone_id != null
}

resource "aws_acm_certificate" "this" {
  count = local.manage_dns ? 1 : 0

  domain_name       = var.domain_name
  validation_method = "DNS"

  tags = merge(var.tags, { Name = var.domain_name })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "validation" {
  count = local.manage_dns ? 1 : 0

  zone_id         = var.route53_zone_id
  name            = tolist(aws_acm_certificate.this[0].domain_validation_options)[0].resource_record_name
  type            = tolist(aws_acm_certificate.this[0].domain_validation_options)[0].resource_record_type
  records         = [tolist(aws_acm_certificate.this[0].domain_validation_options)[0].resource_record_value]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "this" {
  count = local.manage_dns ? 1 : 0

  certificate_arn         = aws_acm_certificate.this[0].arn
  validation_record_fqdns = [aws_route53_record.validation[0].fqdn]
}

resource "aws_route53_record" "this" {
  count = local.manage_dns ? 1 : 0

  zone_id = var.route53_zone_id
  name    = var.domain_name
  type    = "A"

  alias {
    name                   = aws_lb.this.dns_name
    zone_id                = aws_lb.this.zone_id
    evaluate_target_health = true
  }
}
