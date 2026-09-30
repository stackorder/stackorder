---
title: 'Terraform on GitHub Actions: plan on PRs, apply in order'
titleTemplate: false
description: 'Run Terraform and OpenTofu on GitHub Actions: plan on pull requests, apply on merge, and what Stackorder adds across many stacks: order, locks and drift.'
---

# Running Terraform and OpenTofu on GitHub Actions

Most teams start with one workflow file that plans on every pull request and applies on merge. It works well for one stack. This page shows that workflow, where it stops once a repository holds many stacks that depend on each other, and the same repository with Stackorder. Terraform keeps running on your Actions runners either way.

## The usual workflow: plan on pull_request, apply on merge {#usual-workflow}

```yaml
name: terraform
on:
  pull_request:
  push:
    branches: [main]
permissions:
  id-token: write
  contents: read
jobs:
  terraform:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: stacks/prod/vpc
    steps:
      - uses: actions/checkout@v4
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::123456789012:role/terraform
          aws-region: us-east-1
      - uses: hashicorp/setup-terraform@v3
      - run: terraform init -input=false
      - run: terraform plan -input=false
        if: github.event_name == 'pull_request'
      - run: terraform apply -input=false -auto-approve
        if: github.event_name == 'push'
```

The job assumes an AWS role with its GitHub OIDC token, so there are no long-lived keys. For OpenTofu, use `opentofu/setup-opentofu@v1` and run `tofu` instead of `terraform`. The plan output lands in the job log, and the apply runs after the merge.

## What a single workflow cannot do {#limits}

The workflow above names one stack directory. With several stacks, and modules and remote state shared between them, these jobs are left to you:

- **Find the affected stacks.** A change to a local module affects every stack that uses it, and a stack that reads another stack's state through `terraform_remote_state` is affected when that stack changes. A path filter sees the directories that changed, not the stacks that depend on them.
- **Order applies across stacks.** A VPC has to apply before the cluster that uses it. Hard-coded `needs:` between jobs fix one order in the file, whatever the change touches.
- **Lock stacks across pull requests.** Terraform's state lock covers one command, not the time between a plan and its apply, so two open pull requests can both plan the same stack.
- **Check fresh plans and approvals before apply.** An apply that re-plans at merge time can apply something other than the plan the reviewer read.
- **Schedule drift checks with issues.** Each stack needs a scheduled plan, and something has to open an issue when it finds drift and close it once the drift is gone.

## The same workflow with Stackorder {#with-stackorder}

Stackorder keeps plans and applies in your Actions jobs and adds a server that holds the dependency graph and decides which stacks run and in what order. A repository gets a root `stackorder.yaml`, whose smallest form is `version: 1`, the Stackorder GitHub App, and two workflow files. The plan workflow runs on every pull request:

```yaml
name: stackorder plan
on:
  pull_request:
    types: [opened, synchronize, reopened]
concurrency:
  group: stackorder-plan-${{ github.event.pull_request.number }}
  cancel-in-progress: true
jobs:
  plan:
    permissions:
      id-token: write
      contents: read
      actions: read
      checks: write
      pull-requests: read
    uses: stackorder/actions/.github/workflows/plan.yml@v1
    with:
      server-url: ${{ vars.STACKORDER_SERVER_URL }}
      aws-role-arn: arn:aws:iam::123456789012:role/stackorder-plan
      tool: terraform
    secrets: inherit
```

The run workflow, `stackorder-run.yml`, runs only when the server dispatches it; [Workflows](/configuration/workflows#run) has the file. Each gap above maps to one thing Stackorder does:

- **Affected stacks.** The `resolve` job scans the repository, uploads the graph and gets back the affected stacks as a job matrix: stacks changed directly, stacks that use a changed local module, stacks that read a changed stack's state, and their dependents. One plan job runs per stack, under the plan role, with one `stackorder/plan: <key>` check each and one sticky pull request comment. See [The affected set](./concepts#affected-set).
- **Order.** A `stackorder apply` comment, or the merge in `on_merge` mode, applies the affected stacks in [waves](./concepts#waves). The server dispatches `stackorder-run.yml` one wave at a time, and a failed stack blocks its dependents.
- **Locks.** Before the first wave, the server locks every affected stack, all or nothing, and releases the locks on merge or when the run completes. See [Locks](./concepts#locks).
- **Fresh plans and approvals.** The [apply gate](./how-it-works#apply-gate) checks who asked, the pull request's approvals, fresh plans on the head commit, named policy checks and locks, and reports every failure in one comment. Each apply job runs under the stack's GitHub environment and assumes that environment's apply role through OIDC, so environment reviewers and the role's trust policy remain the hard gates.
- **Drift.** On a `drift.schedule`, every stack runs `plan -detailed-exitcode`, and `open_issue` keeps one GitHub issue per drifted stack. It never applies to fix drift. See [Drift detection](/configuration/drift).

The server never runs Terraform and holds no cloud credentials or state. State stays in your S3 bucket, and Stackorder adds under 10 s to what Terraform itself needs in each job. [Getting started](./getting-started) sets up one repository, and the [local demo](./local-demo) runs everything on one machine with no GitHub App and no AWS account.

## FAQ {#faq}

### How do I run terraform plan on every pull request with GitHub Actions?

Trigger a workflow on `pull_request`, assume a read-only AWS role with `aws-actions/configure-aws-credentials` and the job's OIDC token, install Terraform, and run `terraform init` and `terraform plan` in each stack directory. With Stackorder, `stackorder-plan.yml` does this for every stack the pull request affects, and the server posts one check per stack and one sticky comment.

### How do I apply Terraform in dependency order across multiple stacks?

Declare the dependencies and let a tool order them. In Stackorder, `depends_on` in a stack's `.stackorder.yaml` orders the run, and a `terraform_remote_state` read of another stack's state is inferred as an ordering edge too. Module sources decide which stacks are affected, not their order. Affected stacks are layered into waves by the longest path through the graph, and a cycle fails `stackorder/resolve` with the cycle spelled out. Edges to stacks in other repositories appear in the graph and can trigger plan-only runs there, but each run applies one repository.

### Does it work with OpenTofu (setup-opentofu)?

Yes. Set `tool: tofu` in `stackorder.yaml`, or per stack. The reusable workflows install each stack's tool with `hashicorp/setup-terraform@v3` or `opentofu/setup-opentofu@v1`, and Stackorder's end-to-end tests run OpenTofu 1.12.
