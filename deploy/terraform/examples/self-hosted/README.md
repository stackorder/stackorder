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
   aws logs tail "$(terraform output -raw log_group_name)" --since 15m | grep setup_url
   ```

   Store the values the page prints as JSON keyed by variable name
   (`GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET`,
   `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`):

   ```sh
   aws secretsmanager put-secret-value --secret-id stackorder/github-app --secret-string file://github-app.json
   ```

4. `terraform apply` again. The new secret version rolls the service onto
   the App credentials.
5. Install the App on the repository, add the two workflow files and commit
   this directory.

## Upgrades

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

## Permissions

The plan role of this stack needs `secretsmanager:GetSecretValue` on
`stackorder/github-app` and on the three secrets the module creates, because
refreshing a secret version reads its value. The state object holds the
database password and the App private key: restrict it to the plan and
apply roles and keep bucket encryption on.
