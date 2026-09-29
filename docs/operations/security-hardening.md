# Security hardening

Stackorder's design keeps the server away from your cloud: it holds no AWS credentials and never runs Terraform. What stops an unauthorised apply is GitHub's environment gate and the AWS trust policy, not the server. This page is the checklist that makes those controls tight. The reasoning behind each layer is on [Environments and authorization](/configuration/environments-and-authorization) and [Security model](/reference/security-model).

| Control | Where | Protects against |
| --- | --- | --- |
| [Trust policies pinned to the OIDC subject](#trust-policies) | AWS IAM | A workflow that is not the gated job obtaining credentials |
| [Environment gates](#environments) | GitHub repository settings | An apply starting without a human or rule approving it |
| [Required workflow ref](#workflow-ref) | Server and AWS | A locally edited workflow posting results or assuming a role |
| [Metrics token](#metrics) | Server | Anyone on the network reading operational metrics |
| [Branch protection](#branch-protection) | GitHub | Merging without plans, applies or reviews; editing the workflow files unreviewed |
| [Server secrets and network](#server) | Your platform | Session forgery, webhook forgery, database exposure |

## Trust policies {#trust-policies}

Every Stackorder job assumes its AWS role with its own GitHub OIDC token, through `aws-actions/configure-aws-credentials`. The role's trust policy decides which jobs may do that, by the token's `sub` claim. With GitHub's default subject format, the jobs Stackorder runs carry these subjects:

| Job | Workflow | `sub` |
| --- | --- | --- |
| Pull request resolve and plan jobs | `stackorder-plan.yml` → `plan.yml` | `repo:acme/infra:pull_request` |
| Server-dispatched plans (`stackorder plan` comments, re-runs, cross-repository plans) and drift checks | `stackorder-run.yml` → `run.yml` | `repo:acme/infra:environment:default` |
| Applies of stacks mapped to `production` | `stackorder-run.yml` → `run.yml` | `repo:acme/infra:environment:production` |
| Applies of stacks mapped to `staging` | `stackorder-run.yml` → `run.yml` | `repo:acme/infra:environment:staging` |

Server-dispatched plan and drift jobs always run under the environment `default`, whatever the stack's mapping, and `run.yml` gives them `aws-plan-role-arn`, falling back to `aws-role-arn`; only apply jobs use `aws-role-arn-map` and run under the stack's own environment.

### The plan role

It must trust both plan subjects: pull request jobs, and server-dispatched plan and drift jobs.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": [
            "repo:acme/infra:pull_request",
            "repo:acme/infra:environment:default"
          ]
        }
      }
    }
  ]
}
```

Give it read access to state, write access to the state lock (`plan` takes it: the `<key>.tflock` object with `use_lockfile`, or the DynamoDB table), and the read-only permissions your providers need to plan. Nothing else: anyone who can push a branch to the repository can run a plan job with it.

Pass it to both wrappers: `aws-role-arn` in `stackorder-plan.yml` and `aws-plan-role-arn` in `stackorder-run.yml`. Without `aws-plan-role-arn`, dispatched plans and drift checks use `aws-role-arn` and, when that is empty too, run without AWS credentials.

### One apply role per environment

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": "repo:acme/infra:environment:production"
        }
      }
    }
  ]
}
```

`environment:production` appears in the subject only when the job ran under that environment, and so only after its protection rules passed. Repeat for `staging`, and list each role in `aws-role-arn-map` under the stack path prefix the environment covers.

::: warning Map every stack to a named environment
A stack that matches no prefix in `environments` applies under `default`, the same environment the dispatched plan and drift jobs use. An apply role trusting `repo:acme/infra:environment:default` can therefore be assumed by any job the server dispatches for a plan or drift check, and by any other workflow run under `default`. Give every stack that changes real infrastructure an environment of its own, and keep `environment:default` for the read-only plan role.
:::

### Pinning the reusable workflow {#subject-workflow-ref}

To accept only the canonical reusable workflows, add `job_workflow_ref` to the subject with the repository's OIDC subject customization, and match it in every role:

```sh
gh api --method PUT repos/acme/infra/actions/oidc/customization/sub \
  --input - <<'EOF'
{"use_default": false, "include_claim_keys": ["repo", "context", "job_workflow_ref"]}
EOF
```

The subjects then become:

| Job | `sub` |
| --- | --- |
| Pull request plan jobs | `repo:acme/infra:pull_request:job_workflow_ref:stackorder/actions/.github/workflows/plan.yml@refs/tags/v1` |
| Dispatched plans and drift checks | `repo:acme/infra:environment:default:job_workflow_ref:stackorder/actions/.github/workflows/run.yml@refs/tags/v1` |
| Production applies | `repo:acme/infra:environment:production:job_workflow_ref:stackorder/actions/.github/workflows/run.yml@refs/tags/v1` |

with the ref your wrappers call, here `@v1`. Match them with `StringLike` so a later `v1.x.y` pin still works:

```json
"Condition": {
  "StringEquals": {
    "token.actions.githubusercontent.com:aud": "sts.amazonaws.com"
  },
  "StringLike": {
    "token.actions.githubusercontent.com:sub": "repo:acme/infra:environment:production:job_workflow_ref:stackorder/actions/.github/workflows/run.yml@refs/tags/v1*"
  }
}
```

The customization applies to every workflow of the repository, so update every role that trusts the repository at the same time, the plan role included.

## Environment gates {#environments}

In the repository settings, under **Environments**:

| Environment | Required reviewers | Prevent self-review | Deployment branches |
| --- | --- | --- | --- |
| `production` | A team, such as `acme/platform-prod` | On | The default branch only |
| `staging` | Optional | On | The default branch only |
| `default` | None | | The default branch only |

- The server dispatches `stackorder-run.yml` on the default branch, and the job checks out the commit it is given, so a default-branch deployment rule never blocks a legitimate apply and stops a workflow on another branch from using the environment.
- Do not put required reviewers on `default`: every `stackorder plan` comment and every scheduled drift check would wait for an approval.
- Required reviewers on private repositories need GitHub Enterprise. Without them, the trust policy remains the hard stop, and the [deployment protection rule](/configuration/environments-and-authorization#layer-4) can replace the human.
- The App cannot approve a deployment: it has no Environments permission, and an App cannot be a required reviewer.

## Required workflow ref {#workflow-ref}

`STACKORDER_REQUIRED_WORKFLOW_REF` makes the server refuse every runner token whose `job_workflow_ref` does not match, with `403 forbidden`. A copy of the workflow edited in a pull request can then neither register runs nor post results.

```sh
STACKORDER_REQUIRED_WORKFLOW_REF='stackorder/actions/.github/workflows/*.yml@refs/tags/v1*'
```

- The part before `@` is matched segment by segment, so `*.yml` matches `plan.yml` and `run.yml` of `stackorder/actions` but no workflow in another repository.
- In the part after `@`, `*` also matches `/`: `refs/tags/v1*` matches `refs/tags/v1` and `refs/tags/v1.4.2`, but also `refs/tags/v10.0.0`. Use `refs/tags/v1` when every wrapper calls `@v1`.
- The server checks the pattern at start-up and refuses to start with a malformed one.
- Call the reusable workflows by a tag that matches, never by a branch or a commit, and keep the IAM `job_workflow_ref` condition above in step with the same pattern.
- Hand-built workflows that use the actions directly, instead of `plan.yml` and `run.yml`, stop working.

With the [Terraform module](/operations/deploy-aws), set `required_workflow_ref`.

## Metrics token {#metrics}

`GET /metrics` is open unless `STACKORDER_METRICS_TOKEN` is set. The metrics carry no secrets, but they reveal activity, queue depth, GitHub rate limits and the server version. Set a token when the server is reachable from the internet:

```sh
STACKORDER_METRICS_TOKEN="$(openssl rand -hex 32)"
```

The server then answers `401` to any scrape without `Authorization: Bearer <token>`. In Prometheus:

```yaml
scrape_configs:
  - job_name: stackorder
    scheme: https
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/stackorder-metrics-token
    static_configs:
      - targets: ["stackorder.example.com"]
```

Do not rely on a load balancer path rule to hide `/metrics`: the server's router decodes percent-escapes before matching, so an escaped spelling of the path reaches the handler without matching a rule on the literal path.

The Terraform module has no dedicated input for the token. Pass it through `extra_environment`, which puts it in plain text in the task definition, or add it to the task's secrets yourself.

## Branch protection {#branch-protection}

On the default branch, with a branch protection rule or a ruleset:

- Require the status checks `stackorder/plan` and, in `before_merge` mode, `stackorder/apply`. In `on_merge` mode require only `stackorder/plan`, since the apply runs after the merge.
- Require the approvals `apply.require_approvals` asks for, and review from code owners.
- Put `.github/workflows/`, `stackorder.yaml` and `.stackorder/hooks/` under `CODEOWNERS` of a team you trust with production. The server reads its policy from `stackorder.yaml` on the default branch, the dispatched workflow is the default branch's `stackorder-run.yml`, and the CLI runs the hooks inside apply jobs, so all three decide what an apply does.
- Restrict who can push to the default branch and who can bypass the rules.

## Server secrets and network {#server}

- **`STACKORDER_SESSION_KEY`**: set it (`openssl rand -hex 32`), share it between instances, and keep it in a secret store. Without it the server generates one per start.
- **`GITHUB_WEBHOOK_SECRET`**: deliveries without a valid `X-Hub-Signature-256` are refused with `401`. Rotate it in the App settings and the server together.
- **`GITHUB_APP_PRIVATE_KEY`**: the server's only credential towards GitHub. Rotate it in the App settings; runners never see it.
- **API keys** see every repository and act as administrators. Create them only for automation that needs `apply --local` or `unlock`, one per use, and revoke them when unused. See [API keys](/reference/server-configuration#api-keys).
- **HTTPS**: terminate TLS in front of the server and use an `https` base URL, which also marks the session cookie `Secure`.
- **Ingress**: GitHub webhooks and GitHub-hosted runners come from large, changing ranges. If your runners are self-hosted with fixed egress, the Terraform module's `github_webhook_ip_ranges_only` limits the load balancer to GitHub's webhook ranges plus `admin_cidrs`.
- **Egress**: the server needs only the GitHub API, GitHub's OIDC keys, Postgres and, when enabled, the artifact bucket and the OTLP endpoint. See [Network](/reference/server-configuration#network).
- **Database**: use TLS (`sslmode=require` or stricter) and a role that owns only the Stackorder database.

## Plan output {#plan-output}

- Set `plan_output: summary` on stacks whose plans show secrets that are not marked `sensitive`: only counts and addresses then reach the server and the pull request comment.
- The binary plan file in the workflow artifact contains every value, sensitive ones included. It is governed by the repository's access rules; the `plan` action keeps it 5 days (`retention-days`). Keep that short.
