// Package store is the Postgres persistence layer of the stackorder server.
//
// It owns the schema (embedded from /migrations and applied with
// golang-migrate under a Postgres advisory lock, so concurrent servers
// serialise their start-up migrations) and every query the server runs. All
// access goes through pgx v5 and a pgxpool; there is no ORM.
//
// The package covers the data model of the design document: installations
// and repositories, dependency graphs with stable stack identities across
// commits, modules and their released versions, runs with per-stack rows,
// named checks and wave dispatches, orchestration locks, drift history, the
// events and jobs queue claimed with SELECT ... FOR UPDATE SKIP LOCKED,
// sessions, API keys, OIDC jti replay protection, the audit log, data
// retention and the scheduler's advisory-lock leader election.
//
// Row types are plain structs with db tags. Where an api/v1 type exists the
// row type has a ToV1 method that converts it for the JSON API.
//
// Errors are wrapped with the operation that failed. Callers match them with
// errors.Is against ErrNotFound, ErrConflict and ErrInvalid.
//
// A Store is safe for concurrent use. InTx runs a function against a Store
// bound to a single transaction, so callers can compose several methods
// atomically; methods that open their own transaction use a savepoint when
// called inside InTx.
package store
