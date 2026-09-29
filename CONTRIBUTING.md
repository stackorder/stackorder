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
make docs             # build the docs site
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
each takes one to two minutes. Each run is one story: bootstrap every stack
with `apply --local`, plan a `modules/vpc` change, apply it wave by wave
from a `stackorder apply` comment, refuse an apply behind another pull
request's locks and release them with `stackorder unlock`, re-plan for an
expired plan artifact and refuse one that no longer matches, open and
close a drift issue, apply a stack in a non-default workspace, and fail a
run whose apply job ends without reporting.

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

`make docs` builds the VitePress site and CI checks the build on every pull
request. Publishing to GitHub Pages runs from `.github/workflows/docs.yml`
only when the repository variable `DOCS_DEPLOY` is `true`; set it once Pages
is enabled for the repository (GitHub offers Pages on private repositories
only on paid plans, so this stays off until the repository is public).

## Releasing

Tags `vX.Y.Z` on this repository release the CLI binaries through
GoReleaser and push the server image to `ghcr.io/stackorder/stackorder`.
The `stackorder/actions` repository moves its `v1` tag independently.
