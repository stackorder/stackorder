//go:build e2e

// Package e2e runs Stackorder end to end: the real terraform and tofu
// binaries against a LocalStack S3 bucket, the example monorepo
// stackorder/example-infra, the server in-process on its own Postgres
// database, and the stackorder CLI built once per test binary and executed
// as a separate process for every job, with the environment, event payload
// and OIDC token endpoint an Actions runner gives it.
//
// The suite stands GitHub in with internal/testutil/ghfake and the Actions
// token service with internal/testutil/oidcfake, and plays the runner itself: it
// performs the checkouts, uploads and downloads plan artifacts, and
// delivers the webhooks GitHub would send.
//
// TestEndToEnd tells one story per tool, terraform and tofu, selected by
// the repository's tool setting: adopting the repository, bootstrapping
// the stacks under stacks/ with apply --local, reading a stack's outputs
// through the server with the API key and with the token of a plan job
// whose stack the default branch lists as a reader, planning and applying a
// pull request wave by wave, refusing an apply behind another pull
// request's locks and releasing them with unlock, re-planning for an
// expired plan artifact and refusing a plan that no longer matches,
// detecting and resolving drift, a stack in a non-default workspace,
// planning, applying and drift-checking the instances of one directory,
// each with its own var file, per-mode env, environment and state object,
// planning them one after another in a single checkout, affecting them
// through a shared backend config file, and failing a run whose job ended
// without reporting. A tool whose binary is missing is skipped.
//
// The example's stacks/prod/eks reads the outputs of stacks/prod/vpc with
// the stackorder provider, which each tool installs from its own registry.
// The story checks the provider's reads with the API key in the bootstrap,
// with the token of a pull request's plan job, with the token of the
// dispatch of a wave-1 apply job that plans again for an expired artifact,
// and with the token of a drift job: the values each job plans or applies,
// the outputs_read row each read leaves in the audit log, and the
// reads_state edge the scanner infers from the stackorder_outputs block.
//
// Environment:
//
//   - STACKORDER_E2E_EXAMPLE_INFRA_DIR points at a checkout of
//     stackorder/example-infra; STACKORDER_E2E_EXAMPLE_INFRA_URL clones it
//     instead. Without either, an example-infra directory next to this
//     repository or one of its parents is used.
//   - STACKORDER_E2E_LOCALSTACK_URL reuses a running LocalStack with
//     SERVICES=s3,sts instead of starting localstack/localstack:4.0.
//   - TEST_DATABASE_URL reuses a Postgres server, as for every package.
//   - STACKORDER_TERRAFORM_BIN and STACKORDER_TOFU_BIN pick the binaries.
//
// The stacks keep their S3 backend blocks unchanged. Jobs reach LocalStack
// through AWS_ENDPOINT_URL_S3 at s3.localhost.localstack.cloud, which
// resolves to the loopback address, so that the backend and the
// terraform_remote_state reads both use it, and through
// STACKORDER_BACKEND_CONFIG for the settings only the backend accepts.
// The server grants the state bucket to the example repository in
// STACKORDER_STATE_BUCKETS and reads it through LocalStack's own endpoint
// with path-style requests. Every job of stacks/prod/eks downloads the
// stackorder provider, so the suite needs to reach registry.terraform.io
// and registry.opentofu.org.
package e2e
