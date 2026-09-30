---
layout: home
title: Stackorder docs
titleTemplate: Terraform orchestration on GitHub Actions

hero:
  name: Stackorder
  text: Which stacks, in what order.
  tagline: Lightweight Terraform and OpenTofu orchestration on GitHub Actions
  image:
    light: /mark-light.svg
    dark: /mark-dark.svg
    alt: The Stackorder mark, three flat plates with the middle one offset to the right
  actions:
    - theme: brand
      text: Getting started
      link: /guide/getting-started
    - theme: alt
      text: GitHub
      link: https://github.com/stackorder/stackorder

features:
  - title: A coordinator, not an executor
    details: The server holds no cloud credentials, no state and no plan files with secrets. A compromised server can trigger workflows and post comments. It cannot touch infrastructure.
    link: /reference/security-model
    linkText: Security model
  - title: GitHub is the control plane
    details: OIDC, repo permissions, CODEOWNERS, environments with required reviewers, check runs, secrets and compute all come from GitHub. Stackorder adds the dependency graph and a memory of what ran.
    link: /configuration/environments-and-authorization
    linkText: Apply authorization
  - title: Heavy work on the runner, one binary each side
    details: HCL parsing, git diffing, terraform plan and redaction run in your Actions job, in one Go CLI that behaves the same on a laptop, and the plan action uploads the plan file. The server is one Go binary with an embedded UI, plus Postgres. No Docker on the runner.
    link: /guide/how-it-works
    linkText: How it works
  - title: Degrade gracefully, never silently
    details: If the server is down, pull request plans still run and are marked unconfirmed. Applies fail closed, so nobody mistakes a fallback for a green light.
    link: /operations/troubleshooting
    linkText: Failure handling
---

## The smallest setup

A repository needs a root `stackorder.yaml`, two thin workflow files, and the Stackorder GitHub App installed. Everything has a default, so the smallest valid configuration is one line:

```yaml
version: 1
```

Stacks are discovered under `stacks/**` wherever a directory holds a `terraform` block with a `backend "s3"`. A change to a local module, a directory a `module` block points at with a relative `source`, reaches every stack that uses it. Explicit dependencies between stacks go in a `.stackorder.yaml` next to the stack:

```yaml
depends_on:
  - stacks/prod/vpc
  - acme/network-infra//stacks/prod/tgw
```

Continue with [Getting started](/guide/getting-started), or read [How it works](/guide/how-it-works) first.
