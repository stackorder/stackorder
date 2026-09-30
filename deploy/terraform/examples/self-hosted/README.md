# Self-hosted example

Stackorder deploying Stackorder. This directory is an ordinary stack: it
has a `backend "s3"` block, so it is discovered like any other stack when it
lives under `stacks/` in your infrastructure repository (for example at
`stacks/prod/stackorder`), and upgrades of the server are pull requests that
Stackorder itself plans and applies.

In your repository, change the module `source` in `main.tf` from the
relative path to a tag, so Renovate or Dependabot can bump it together with
`stackorder_version`:

```hcl
source = "github.com/stackorder/stackorder//deploy/terraform?ref=v0.1.0"
```

`.stackorder.yaml` maps the stack to the `production` GitHub environment and
keeps plan text out of the server and the PR comment (`plan_output:
summary`), because this stack's resources hold the server's own secrets.

## Bootstrap, once, from a workstation

1. Create the secret the stack reads the App credentials from, empty:

   ```sh
   aws secretsmanager create-secret --name stackorder/github-app --secret-string '{}'
   ```

2. `terraform init && terraform apply`. With no App credentials the server
   starts in setup mode, with a single task.
3. Take the setup URL with its one-time token from the task logs, open it
   and create the GitHub App. The `setup_url` output lacks the token and
   answers 403:

   ```sh
   aws logs tail "$(terraform output -raw log_group_name)" --since 1d | grep setup_url
   ```

   The server logs the line once, when the task starts. To get a new one,
   restart the task:

   ```sh
   aws ecs update-service --cluster stackorder --service stackorder --force-new-deployment
   ```

   Store the secret values the page prints as JSON keyed by variable name
   (`GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET`,
   `GITHUB_OAUTH_CLIENT_SECRET`):

   ```sh
   aws secretsmanager put-secret-value --secret-id stackorder/github-app --secret-string file://github-app.json
   ```

   The App id and OAuth client id are not secret: set the `github_app_id`
   and `github_oauth_client_id` variables, for example in a committed
   `terraform.tfvars`.
4. `terraform apply` again. The new secret version rolls the service onto
   the App credentials.
5. Install the App on the repository, add the two workflow files and commit
   this directory.

## Upgrades

When moving this stack from module v0.1.0, which read the App id and
OAuth client id from `stackorder/github-app`, set the `github_app_id` and
`github_oauth_client_id` variables in the same pull request. Left null,
they put the server back into setup mode.

Open a pull request that changes `stackorder_version` (and the module
`ref`). The plan shows a new task definition revision. On `stackorder
apply`, ECS starts the new task next to the old one; the old task keeps
serving, including the result this very apply job posts, until the new
task has run its migrations and passes `/readyz`. `wait_for_steady_state`
makes the apply job fail if the deployment circuit breaker rolls back, so a
bad release shows up as a red apply on the pull request.

If a release leaves no healthy task, applies through Stackorder fail closed.
Roll back from a workstation with `terraform apply -var
stackorder_version=<previous>` and release the orchestration lock with
`stackorder unlock stacks/prod/stackorder` once the server is back.

## Secrets

`stackorder/github-app` is read with an ephemeral resource on every plan
and apply, so its values reach neither the state nor the saved plan that
Stackorder applies, and the module writes them to its own secrets through
write-only attributes. After changing a value in `stackorder/github-app`,
for example to rotate the App private key, increase `secrets_version` in
`main.tf` in the same pull request; without that the module does not
rewrite its secret.

## Permissions

The plan and apply roles of this stack need `secretsmanager:GetSecretValue`
on `stackorder/github-app`. Refreshing the module's secret versions reads
their version ids, which takes `secretsmanager:ListSecretVersionIds`.
When upgrading from module v0.1.0, keep `secretsmanager:GetSecretValue`
on the module's three secrets until the first apply with the new module
has completed: the first refresh still reads the values of the versions
v0.1.0 wrote. The state object no longer holds the database password or
the App private key, but it describes the whole deployment: restrict it
to the plan and apply roles and keep bucket encryption on.
