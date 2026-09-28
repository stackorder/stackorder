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
