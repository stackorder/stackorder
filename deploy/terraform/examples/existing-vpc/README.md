# Existing VPC example

Deploys Stackorder into a VPC you already run, with a certificate you
already have. The module creates no network and no DNS records: point a
CNAME (or an alias, using `alb_zone_id`) for `domain_name` at the
`alb_dns_name` output.

The private subnets need a route to the internet through NAT, because the
tasks pull the image from ghcr.io, call api.github.com and reach Secrets
Manager and CloudWatch Logs over their public endpoints.

```sh
terraform init
terraform apply \
  -var vpc_id=vpc-0123456789abcdef0 \
  -var 'public_subnet_ids=["subnet-0a1b2c3d4e5f60001","subnet-0a1b2c3d4e5f60002"]' \
  -var 'private_subnet_ids=["subnet-0a1b2c3d4e5f60003","subnet-0a1b2c3d4e5f60004"]' \
  -var domain_name=stackorder.example.com \
  -var certificate_arn=arn:aws:acm:eu-west-1:123456789012:certificate/8f2e6c1a-3b4d-4e5f-9a0b-1c2d3e4f5a6b
```
