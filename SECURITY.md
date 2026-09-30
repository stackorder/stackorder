# Security policy

## Supported versions

Only the latest 0.1.x release receives security fixes. The CLI, the server image and the Terraform module are released together from one tag, so a fix ships in the next release of all three. Upgrade the components the advisory names; the server and the CLI can be upgraded independently within API v1, as [Upgrades and backups](https://docs.stackorder.io/operations/upgrades-and-backups) explains.

For [`stackorder/actions`](https://github.com/stackorder/actions), fixes go into the latest v1 release, and the `v1` tag moves to it.

## Reporting a vulnerability

Please do not open a public issue, pull request or discussion for a security problem. Report it privately through GitHub's private vulnerability reporting instead:

- The server, the CLI, the web UI or the Terraform module: [report a vulnerability in stackorder/stackorder](https://github.com/stackorder/stackorder/security/advisories/new).
- The reusable workflows or the actions: [report a vulnerability in stackorder/actions](https://github.com/stackorder/actions/security/advisories/new).

If you are not sure which repository is affected, use stackorder/stackorder. The same form is under **Security > Report a vulnerability** on each repository.

Include as much of this as you can:

- The version: the release tag or image tag of the server and the CLI, and the `stackorder/actions` ref your workflows call.
- The deployment: the Terraform module, the container on another platform, or Docker Compose, and any settings that matter, such as `STACKORDER_REQUIRED_WORKFLOW_REF` or the artifact bucket.
- Steps to reproduce, with the smallest `stackorder.yaml`, workflow files or requests that show the problem.
- The impact: what an attacker can read or change, and what access they need to start with.

## Scope

In scope:

- The server (`stackorder-server`), including its API, webhooks, GitHub App integration and OIDC verification.
- The `stackorder` CLI, including redaction of plan output.
- The web UI.
- The Terraform deployment module in `deploy/terraform`.
- The workflows and actions in [`stackorder/actions`](https://github.com/stackorder/actions).

A good report shows one of these doing something the [security model](https://docs.stackorder.io/reference/security-model) says it cannot, for example a compromised server reaching Terraform state or an AWS role, a runner token accepted for a run it does not belong to, or a secret that the CLI should have redacted reaching the server.

Out of scope:

- Vulnerabilities in Terraform, OpenTofu, providers, GitHub, GitHub Actions or AWS themselves. Report those to their maintainers.
- Findings that require an attacker who already controls a GitHub organisation owner account or the AWS account.
- What the security model already lists as possible for a compromised party, such as a compromised server dispatching workflows and posting comments, or a PR author with write access adding a stack that nothing maps to a protected environment. A way to go beyond those limits is in scope.
- Deployments that leave out the controls the [security hardening](https://docs.stackorder.io/operations/security-hardening) guide describes, such as trust policies pinned to the environment, unless Stackorder's own documentation led to the gap.

## What happens next

We acknowledge the report and discuss it with you in the private advisory, where we confirm the problem, agree on its severity and work on a fix. Disclosure is coordinated with you: the details stay private until a fixed release is out. We then publish a GitHub security advisory and, where warranted, request a CVE for it. If you would like credit, the advisory names you.
