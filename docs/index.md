---
layout: home
title: Stackorder docs
description: 'Documentation for Stackorder: set up Terraform and OpenTofu pull request plans, dependency-ordered applies and scheduled drift checks on GitHub Actions.'
titleTemplate: Terraform orchestration on GitHub Actions

hero:
  name: Stackorder
  text: '<span class="visually-hidden">: </span>Terraform and OpenTofu orchestration on GitHub Actions'
  tagline: Which stacks, in what order. Stackorder plans every stack a pull request affects and applies them in dependency waves on GitHub Actions. Open source, self-hosted, and the server holds no cloud credentials.
  image:
    light: /mark-light.svg
    dark: /mark-dark.svg
    alt: The Stackorder mark, three flat plates with the middle one offset to the right
  actions:
    - theme: brand
      text: Getting started
      link: /guide/getting-started
    - theme: alt
      text: Local demo
      link: /guide/local-demo
    - theme: alt
      text: GitHub
      link: https://github.com/stackorder/stackorder

features:
  - title: Plans on every pull request
    details: One check per affected stack and one sticky comment. A change to a stack, to a local module it uses or to a stack whose state it reads plans it, and the stacks that depend on it.
    link: /guide/concepts#affected-set
    linkText: The affected set
  - title: Applies in dependency waves
    details: A <code>stackorder apply</code> comment or the merge applies the affected stacks wave by wave, in the order of the dependency graph. A failed stack blocks its dependents.
    link: /guide/how-it-works#waves
    linkText: Waves
  - title: A coordinator, not an executor
    details: The server holds no cloud credentials, no state and no plan files with secrets. A compromised server can trigger workflows and post comments. It cannot touch infrastructure.
    link: /reference/security-model
    linkText: Security model
  - title: Degrade gracefully, never silently
    details: If the server is down, pull request plans still run and are marked unconfirmed. Applies fail closed, so nobody mistakes a fallback for a green light.
    link: /operations/troubleshooting
    linkText: Failure handling
---

## The smallest setup

Stackorder works with GitHub.com, S3 state and AWS roles through GitHub OIDC. Current release: v0.1.0.

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
