//go:build e2e

// Package e2e runs Stackorder end to end: the real terraform and tofu
// binaries against a LocalStack S3 bucket, the example monorepo
// stackorder/example-infra, the server in-process on its own Postgres
// database, and the stackorder CLI built once per test binary and executed
// as a separate process for every job, with the environment, event payload
// and OIDC token endpoint an Actions runner gives it.
//
// The design runs these tests against a throwaway GitHub organisation. A
// real organisation delivers webhooks only to a publicly reachable server
// and mints runner tokens only inside its own Actions jobs, so a suite that
// runs on a laptop or a pull request build stands GitHub in with
// internal/testutil/ghfake and the Actions token service with
// internal/testutil/oidcfake, and plays the runner itself: it performs the
// checkouts, uploads and downloads plan artifacts, and delivers the
// webhooks GitHub would send.
//
// TestEndToEnd tells one story per tool, terraform and tofu, selected by
// the repository's tool setting: adopting the repository, bootstrapping
// every stack with apply --local, planning and applying a pull request
// wave by wave, refusing an apply behind another pull request's locks and
// releasing them with unlock, re-planning for an expired plan artifact and
// refusing a plan that no longer matches, and detecting and resolving
// drift. A tool whose binary is missing is skipped.
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
package e2e
