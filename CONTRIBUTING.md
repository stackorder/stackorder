# Contributing

## Prerequisites

- Go (the version in `go.mod`; `GOTOOLCHAIN=auto` fetches it)
- Node 24 with npm, for the UI and the docs site
- Docker, for Postgres and LocalStack in integration and end-to-end tests
- Terraform or OpenTofu on `PATH`, for end-to-end tests and the deployment module

## Everyday commands

```sh
make build            # bin/stackorder and bin/stackorder-server
make test             # unit tests, no Docker needed
make test-integration # server + Postgres + fake GitHub, needs Docker
make test-e2e         # real terraform + LocalStack + example-infra, needs Docker
make lint             # gofmt, go vet, golangci-lint
make ui               # build the UI into internal/ui/dist
make docs             # check and build the docs site
make dev              # Postgres + LocalStack via docker compose, then the server
```

Set `TEST_DATABASE_URL` to reuse an existing Postgres instead of starting a
container.

`test/integration/testdata/example-infra` is a copy of the
[example monorepo](https://github.com/stackorder/example-infra) without its
workflow files; the integration suite always uses it and the e2e suite falls
back to it when no checkout is available. Refresh it with `make sync-example`
(`EXAMPLE_INFRA=<path>` points at another checkout) whenever the example
changes.

## End-to-end tests

`make test-e2e` runs `test/e2e` with the `e2e` build tag: the real
`terraform` and `tofu` binaries against a LocalStack S3 bucket, the
[example monorepo](https://github.com/stackorder/example-infra), the server
in-process on its own Postgres database and the `stackorder` CLI as a
separate process for every job. GitHub is played by the in-memory fake of
`internal/testutil/ghfake` and the Actions token service by
`internal/testutil/oidcfake`, because a real organisation only talks to a
server it can reach from the internet. The suite runs once with Terraform
and once with OpenTofu, and skips a tool whose binary is not on `PATH`;
each takes one to two minutes. Each run is one story: bootstrap the stacks
under `stacks/` with `apply --local`, plan a `modules/vpc` change, apply it
wave by wave from a `stackorder apply` comment, refuse an apply behind
another pull request's locks and release them with `stackorder unlock`,
re-plan for an expired plan artifact and refuse one that no longer matches,
open and close a drift issue, apply a stack in a non-default workspace,
plan, apply and drift-check the `infra/` stack instances, each with its own
var file, per-mode env, GitHub environment and state object, plan them one
after another in a single checkout, affect them all through their shared
backend config file, and fail a run whose apply job ends without reporting.

It needs Docker for `localstack/localstack:4.0` and Postgres, git 2.38 or
later, and DNS that resolves `s3.localhost.localstack.cloud` to the loopback
address (public DNS does). It looks for an `example-infra` checkout next to
this repository; point it elsewhere with one of:

```sh
STACKORDER_E2E_EXAMPLE_INFRA_DIR=../example-infra make test-e2e
STACKORDER_E2E_EXAMPLE_INFRA_URL=https://github.com/stackorder/example-infra.git make test-e2e
```

`STACKORDER_E2E_LOCALSTACK_URL=http://localhost:4566` reuses the LocalStack
of `docker compose up localstack` instead of starting a container, and
`STACKORDER_TERRAFORM_BIN` or `STACKORDER_TOFU_BIN` pick other binaries.
`.github/workflows/e2e.yml` runs the suite on every push to `main` and
nightly.

### Against a real GitHub organisation

Teams with a Stackorder server GitHub can reach, and its App installed on
all repositories of an organisation, can run `TestLiveGitHub` instead. It
creates a throwaway private repository from the example, sets the
`STACKORDER_SERVER_URL` repository variable, pushes `main` and a branch
that changes `modules/vpc`, opens a pull request, waits for the server's
`stackorder/resolve` and `stackorder/plan` check runs and the plan checks
of both VPC stacks, and deletes the repository afterwards. It installs
nothing and needs neither Docker nor Terraform locally.

| Variable | Meaning |
| --- | --- |
| `STACKORDER_E2E_GITHUB_TOKEN` | Token that may create and delete repositories in the organisation, push workflow files (the `workflow` scope, or Workflows: write) and set their Actions variables |
| `STACKORDER_E2E_ORG` | The organisation |
| `STACKORDER_E2E_SERVER_URL` | The server's public `https` base URL |
| `STACKORDER_E2E_PLAN_ROLE_ARN` | Optional plan role, set as `STACKORDER_PLAN_ROLE_ARN`; with it every plan check must succeed |
| `STACKORDER_E2E_GITHUB_API_URL`, `STACKORDER_E2E_GITHUB_URL` | Optional GitHub Enterprise Server URLs |
| `STACKORDER_E2E_KEEP_REPO` | `true` keeps the repository for inspection |

The first three are required together. When they are set the fake story is
skipped; without them `TestLiveGitHub` is skipped. In CI the `live` job of
`e2e.yml` runs when the repository variable `STACKORDER_E2E_ORG` is set,
reading the token from the secret `STACKORDER_E2E_GITHUB_TOKEN`.

## Layout and contracts

Read [ARCHITECTURE.md](ARCHITECTURE.md) before adding a package. It fixes
package boundaries, library choices, identifiers, endpoints and status
values. Behavioural questions are answered by the design document linked
from the README.

## Commits and pull requests

- Conventional Commits with a scope: `feat(graph): assign waves by longest path`.
- One logical change per commit; keep refactors separate from behaviour changes.
- Every behaviour change comes with tests at the lowest level that can
  observe it. The `graph` package is tested exhaustively.
- No code comments explaining rationale; put it in the commit body.
- CI must be green: `make lint test` locally reproduces it.

## Documentation site

`make docs` runs the tests in `docs/test` and builds the VitePress site, and
CI runs both on every pull request. `.github/workflows/docs.yml` publishes the
site to GitHub Pages at https://docs.stackorder.io on every push to `main` that
touches it, gated on the repository variable `DOCS_DEPLOY` being `true`. See
[Contributing to the docs](docs/contributing.md) for the Pages and DNS setup a
fork needs.

## Releasing

Tags `vX.Y.Z` on this repository release the CLI binaries through
GoReleaser, the server image on `ghcr.io/stackorder/stackorder` and the
Terraform module. Work through the list in order for every release.

1. **Changelog and docs.** In one local commit on `main`, move the
   `Unreleased` entries of `CHANGELOG.md` under a dated `X.Y.Z` heading and
   update every version the docs pin:
   - `README.md`: the status line, the CLI download, the `setup` action's
     `version`, the server image tags, and the module's `ref=vX.Y.Z` and
     `image_tag`.
   - `deploy/terraform/README.md`: `ref=vX.Y.Z` and `image_tag` in both
     examples.
   - `deploy/terraform/examples/self-hosted/README.md`: `ref=vX.Y.Z`.
   - `deploy/terraform/examples/self-hosted/variables.tf`: the default of
     `stackorder_version`.
   - `docs/operations/deploy-aws.md`: `ref=vX.Y.Z` and `image_tag`.
   - `docs/index.md`: the current release.

   When the release needs a newer `stackorder/actions`, the changelog names
   the minimum version.
2. **Tag.** Tag that commit `vX.Y.Z` and push it together with `main`, so
   the docs site, which deploys on the push to `main`, never pins a tag that
   does not exist:

   ```sh
   git tag vX.Y.Z
   git push --atomic origin main vX.Y.Z
   ```
3. **Release workflow.** Watch the `release` workflow until every job
   passes. `cli` publishes the binaries, `image` pushes the tags `X.Y.Z`,
   `X.Y` and `latest`, and `visibility` checks from a runner without a
   registry login that each of those tags can be pulled anonymously.
4. **Package visibility.** GitHub publishes a new container package as
   private, and its REST API cannot change the visibility, so the first
   release of a new package fails the `visibility` job. Someone with admin
   access to the package opens its
   [settings](https://github.com/orgs/stackorder/packages/container/stackorder/settings),
   chooses **Change visibility**, makes it public and re-runs the failed
   job. A public package cannot be made private again, so later releases
   pass. The docs site already shows the new pins, so fix a failed
   `visibility` job right away, and do not announce the release until it
   passes: the Terraform module's `verify_image` check refuses an image
   that cannot be pulled anonymously, so a self-hosted apply against a
   private image fails at plan time instead of hanging in ECS until the
   steady-state timeout.
5. **`stackorder/actions`.** When its actions or reusable workflows
   changed, tag `vX.Y.Z` in that repository, publish the GitHub release
   and move its `v1` tag to the new version. Otherwise it keeps its own
   versions and moves `v1` independently.
