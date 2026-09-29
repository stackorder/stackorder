# Complete example

Deploys Stackorder with everything the module can create: a VPC across two
availability zones with public and private subnets and one NAT gateway, a
DNS validated ACM certificate and an alias record in an existing Route53
hosted zone, two tasks, the optional artifact bucket and CloudWatch alarms
sent to a new SNS topic.

```sh
terraform init
terraform apply -var domain_name=stackorder.example.com -var route53_zone_id=Z0123456789ABCDEFGHIJ
```

The first apply leaves the GitHub App inputs unset, so the server starts in
setup mode. Open the `setup_url` output, create the App, then apply again
with the five values the page printed, through `TF_VAR_*` environment
variables so they stay out of shell history:

```sh
export TF_VAR_github_app_id="$(jq -r .GITHUB_APP_ID github-app.json)"
export TF_VAR_github_app_private_key="$(jq -r .GITHUB_APP_PRIVATE_KEY github-app.json)"
export TF_VAR_github_webhook_secret="$(jq -r .GITHUB_WEBHOOK_SECRET github-app.json)"
export TF_VAR_github_oauth_client_id="$(jq -r .GITHUB_OAUTH_CLIENT_ID github-app.json)"
export TF_VAR_github_oauth_client_secret="$(jq -r .GITHUB_OAUTH_CLIENT_SECRET github-app.json)"
terraform apply -var domain_name=stackorder.example.com -var route53_zone_id=Z0123456789ABCDEFGHIJ
```

where `github-app.json` holds the printed values keyed by variable name.

The values end up in Terraform state, so keep the state encrypted and
access controlled.

`/metrics` requires a bearer token the module generates; the
`metrics_token_secret_arn` output names the secret Prometheus reads it
from, as described in the module README.
