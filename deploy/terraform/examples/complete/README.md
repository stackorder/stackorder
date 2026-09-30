# Complete example

Deploys Stackorder with everything the module can create: a VPC across two
availability zones with public and private subnets and one NAT gateway, a
DNS validated ACM certificate and an alias record in an existing Route53
hosted zone, two tasks, the optional artifact bucket, load balancer access
logs and CloudWatch alarms sent to a new SNS topic.

```sh
terraform init
terraform apply -var domain_name=stackorder.example.com -var route53_zone_id=Z0123456789ABCDEFGHIJ
```

The first apply leaves the GitHub App inputs unset, so the server starts in
setup mode, with one task. `/setup` opens only with the one-time token the
server logs at start-up, so take the URL with its token from the task logs
rather than from the `setup_url` output:

```sh
aws logs tail "$(terraform output -raw log_group_name)" --since 1d | grep setup_url
```

The server logs the line once, when the task starts. To get a new one,
restart the task:

```sh
aws ecs update-service --cluster stackorder --service stackorder --force-new-deployment
```

Open the URL, create the App, then apply again with the five values the
page printed, through `TF_VAR_*` environment variables so they stay out of
shell history:

```sh
export TF_VAR_github_app_id="$(jq -r .GITHUB_APP_ID github-app.json)"
export TF_VAR_github_app_private_key="$(jq -r .GITHUB_APP_PRIVATE_KEY github-app.json)"
export TF_VAR_github_webhook_secret="$(jq -r .GITHUB_WEBHOOK_SECRET github-app.json)"
export TF_VAR_github_oauth_client_id="$(jq -r .GITHUB_OAUTH_CLIENT_ID github-app.json)"
export TF_VAR_github_oauth_client_secret="$(jq -r .GITHUB_OAUTH_CLIENT_SECRET github-app.json)"
terraform apply -var domain_name=stackorder.example.com -var route53_zone_id=Z0123456789ABCDEFGHIJ
```

where `github-app.json` holds the printed values keyed by variable name.

The private key and the two secrets are ephemeral variables, so they stay
out of Terraform state and out of saved plans. Export them again for every
plan and apply, including the apply of a saved plan. After changing any of
them later, also increase `secrets_version`, for example with
`-var secrets_version=2`, or the secret keeps the old value.

`/metrics` requires a bearer token the module generates; the
`metrics_token_secret_arn` output names the secret Prometheus reads it
from, as described in the module README.
