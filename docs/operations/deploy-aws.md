# Deploy on AWS

The repository ships a Terraform module in `deploy/terraform` that deploys the server on ECS Fargate with RDS Postgres. Stackorder uses the same module to deploy itself, so it is also a working example of a stack Stackorder can manage.

## What the module creates {#resources}

| Resource | Purpose |
| --- | --- |
| ECS service on Fargate, 1 to 2 tasks | Runs the `ghcr.io/stackorder/stackorder` image. A 0.25 vCPU / 512 MB task is enough for an org with a few hundred stacks. |
| Application Load Balancer with an ACM certificate | Terminates TLS and forwards to port 8080; health checks use `/readyz`. |
| RDS Postgres, or Aurora Serverless v2 | The database. `db.t4g.micro` is enough to start. |
| Secrets Manager secrets | The GitHub App credentials and the server's other secrets, injected into the task through ECS `secrets` and never baked into the image. |
| Task IAM role | No permissions, unless the artifact bucket is enabled. |
| Security groups | Ingress to the tasks only from the ALB; egress only to GitHub over HTTPS and to the database. |
| Artifact bucket (optional) | An S3 bucket owned by the task role for full plan text, with lifecycle expiry. |

The server needs nothing else. It holds no AWS credentials for your infrastructure, and the task role stays empty unless you turn on the artifact bucket.

Security groups filter by address, not by host name. The task's HTTPS egress has to reach `api.github.com` and GitHub's OIDC key endpoint, so in practice it is HTTPS to the internet, or through an egress proxy if you run one.

## Inputs {#inputs}

The module's inputs fall into these groups. Their exact names and defaults are in `deploy/terraform/variables.tf`, and `deploy/terraform/examples/` has complete calls.

| Group | What you provide |
| --- | --- |
| Network | The VPC, public subnets for the ALB, private subnets for the tasks and the database |
| Name and TLS | The public host name and its ACM certificate |
| Image | The server image tag to run, such as `X.Y.Z` |
| Size | Task CPU and memory, desired task count, database class or Aurora capacity |
| GitHub App | The Secrets Manager secrets holding the App id, private key, webhook secret and OAuth client id and secret |
| Options | The artifact bucket and its expiry, `STACKORDER_REQUIRED_WORKFLOW_REF`, and tracing |

## First deployment {#first-deploy}

The server starts in setup mode while the GitHub App variables are unset, serving only `/setup`, `/healthz` and `/readyz`. The first deployment uses that:

1. Apply the module. The service comes up in setup mode.
2. Open `https://<host>/setup` and create the App. Store the printed values in the Secrets Manager secrets the task reads.
3. Redeploy the service so new tasks start with the App variables.
4. Install the App on your repositories and continue with [Getting started](/guide/getting-started#install).

## Upgrades {#upgrades}

1. Change the image tag to the new `X.Y.Z` and apply.
2. ECS starts a new task. The server runs any database migrations at start-up, under a migration lock.
3. The previous task keeps serving until the new one passes `/readyz`, then drains.

Pin a specific version rather than `latest`, so an apply is the only thing that changes the running version. Take a database snapshot before an upgrade that includes migrations. See [Upgrades and backups](./upgrades-and-backups).

## Scaling out {#scale-out}

Set the desired count to 2 for availability. No other change is needed:

- webhook handling, workers and the API are stateless;
- all coordination goes through Postgres, and workers claim work with `SKIP LOCKED`;
- the scheduler is single-leader through a Postgres advisory lock, so one task schedules and any task executes;
- sessions live in Postgres, and nothing on local disk matters.

Every task must share the same `STACKORDER_SESSION_KEY`.

## Managing the module with Stackorder {#dogfooding}

Put the module call in its own stack, with its own `backend "s3"` block, in a repository that has Stackorder installed. Plans for the server's own changes then run like any other stack. Applies depend on the server being up, so for an upgrade that might not come back healthy, keep a way to apply the stack by hand.
