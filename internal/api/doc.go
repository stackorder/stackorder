// Package api serves every HTTP endpoint of the stackorder server except the
// webhook receiver, which it only mounts: the runner, human and automation
// JSON API under /v1, GitHub sign-in under /auth, the GitHub App manifest
// flow under /setup, health and metrics endpoints, and the embedded UI for
// every other path.
//
// # Authentication
//
// A request carries one of three credentials.
//
//   - A runner presents its GitHub Actions OIDC token as
//     "Authorization: Bearer <jwt>". The token must verify against the
//     issuer's JWKS, carry the configured audience, be at most ten minutes
//     old and never have been presented before: its jti is recorded in the
//     store, so a replayed token is refused with 401. Its repository must be
//     one the server knows, with the same repository_id and an installation
//     that is not suspended, and when Config.RequiredWorkflowRef is set its
//     job_workflow_ref must match it; either failure is 403. The principal's
//     login is the token's actor. Binding the claims to a particular run is
//     the run service's job.
//   - Automation presents an API key as "Authorization: Bearer sk_...". The
//     key must exist and not be revoked. API keys see every repository.
//   - A person signs in with GitHub and presents the stackorder_session
//     cookie, "<token>.<hex HMAC-SHA256 of the token under the session
//     key>". The signature is checked before the store is consulted, so a
//     forged cookie never reaches the database. A session is issued only to
//     a user who belongs to an organisation with the App installed, or who
//     has it installed on their own account, and it records the user's
//     organisations. A person sees only the repositories whose installation
//     account is one of those organisations or their own login; any other
//     repository, and everything in it, answers 404 as if it did not exist.
//
// The runner endpoints POST /v1/runs, POST /v1/runs/{id}/graph and the
// result and check uploads accept OIDC tokens and API keys. GET
// /v1/runs/{id} accepts all three credentials, and POST /v1/unlock accepts
// API keys and sessions. Every other /v1 endpoint accepts API keys and
// sessions.
//
// State-changing requests made with a session cookie (unlock, re-run and
// sign-out) must also come from the server's own origin: the Origin header
// must equal the origin of Config.BaseURL or, without an Origin header,
// Sec-Fetch-Site must be same-origin. The cookie itself is HttpOnly and
// SameSite=Lax. API keys are exempt, since a browser never attaches them to
// a request on its own. Unlocking a stack or re-running a run as a person
// also requires push permission on the repository.
//
// # Errors
//
// Every error response is a v1.Error. The run service's errors from
// internal/principal, the OIDC verification errors and the store's
// not-found errors are mapped to the codes ARCHITECTURE.md lists in one
// function; any other error is logged and answered with 500 internal
// without its message.
//
// # Setup mode
//
// A server started without App credentials serves only /setup*, /healthz
// and /readyz; everything else answers 503 unavailable.
package api
