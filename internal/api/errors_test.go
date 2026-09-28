package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

func TestToAPIError(t *testing.T) {
	taken := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	conflicts := []v1.LockInfo{{StackKey: "stacks/prod/vpc", RunID: "r1", PRNumber: 41, TakenAt: taken}}
	failures := []report.GateFailure{
		{Layer: report.LayerAuthorization, Reason: "not a member", Stacks: []string{"stacks/prod/vpc"}},
		{Layer: report.LayerApprovals, Name: "four eyes", Reason: "author"},
	}
	cases := []struct {
		name       string
		err        error
		status     int
		code       string
		message    string
		details    any
		unexpected bool
	}{
		{name: "not found", err: principal.Wrap(principal.ErrNotFound, "run %s", "abc"), status: 404, code: "not_found", message: "not found: run abc"},
		{name: "forbidden", err: fmt.Errorf("apply: %w", principal.ErrForbidden), status: 403, code: "forbidden", message: "apply: forbidden"},
		{name: "conflict", err: principal.Wrap(principal.ErrConflict, "run finished"), status: 409, code: "conflict", message: "conflict: run finished"},
		{name: "superseded", err: principal.Wrap(principal.ErrSuperseded, "new head"), status: 409, code: "superseded", message: "superseded: new head"},
		{name: "invalid sentinel", err: principal.Wrap(principal.ErrInvalid, "bad mode"), status: 400, code: "invalid", message: "invalid: bad mode"},
		{
			name: "invalid typed", err: fmt.Errorf("create: %w", &principal.InvalidError{Field: "sha", Reason: "must be 40 hex characters"}),
			status: 400, code: "invalid", message: "create: invalid sha: must be 40 hex characters",
			details: map[string]any{"field": "sha", "reason": "must be 40 hex characters"},
		},
		{name: "locked sentinel", err: principal.ErrLocked, status: 423, code: "locked", message: "locked"},
		{
			name: "locked typed", err: fmt.Errorf("apply: %w", &principal.LockedError{Conflicts: conflicts}),
			status: 423, code: "locked", message: "apply: locked: stacks/prod/vpc (PR #41)",
			details: map[string]any{"conflicts": conflicts},
		},
		{
			name: "locked without conflicts", err: &principal.LockedError{},
			status: 423, code: "locked", message: "locked: ", details: map[string]any{"conflicts": []v1.LockInfo{}},
		},
		{name: "refused sentinel", err: principal.ErrRefused, status: 409, code: "refused", message: "refused"},
		{
			name: "refused typed", err: &principal.RefusedError{Failures: failures},
			status: 409, code: "refused", message: "refused: layer 1 : not a member; layer 2 four eyes: author",
			details: map[string]any{"failures": []gateFailure{
				{Layer: 1, Name: report.LayerName(1), Reason: "not a member", Stacks: []string{"stacks/prod/vpc"}},
				{Layer: 2, Name: "four eyes", Reason: "author"},
			}},
		},
		{name: "oidc expired", err: fmt.Errorf("%w: exp passed", oidc.ErrExpired), status: 401, code: "unauthorized", message: "oidc: token expired: exp passed"},
		{name: "oidc replay", err: fmt.Errorf("%w: jti used", oidc.ErrReplay), status: 401, code: "unauthorized", message: "oidc: token replayed: jti used"},
		{name: "oidc jwks", err: fmt.Errorf("fetch: %w", oidc.ErrJWKSUnavailable), status: 503, code: "unavailable", message: "the runner token issuer's keys are unavailable"},
		{
			name: "oidc binding", err: fmt.Errorf("bind: %w", &oidc.ErrBinding{Reason: "environment", Want: "production", Got: "staging"}),
			status: 403, code: "forbidden", message: `bind: oidc: claim environment is "staging", want "production"`,
		},
		{name: "store not found", err: fmt.Errorf("store: get run: %w", store.ErrNotFound), status: 404, code: "not_found", message: "not found"},
		{name: "store invalid", err: fmt.Errorf("store: list runs: malformed cursor: %w", store.ErrInvalid), status: 400, code: "invalid", message: "invalid request"},
		{name: "store conflict", err: fmt.Errorf("store: x: %w", store.ErrConflict), status: 409, code: "conflict", message: "the request conflicts with the current state"},
		{name: "api error", err: fmt.Errorf("wrapped: %w", forbidden("nope")), status: 403, code: "forbidden", message: "nope"},
		{name: "github rate limited", err: fmt.Errorf("perm: %w", gh.ErrRateLimited), status: 503, code: "unavailable", message: "GitHub is unavailable", unexpected: true},
		{name: "github 502", err: &gh.APIError{Status: 502, Method: "GET", Route: "/x"}, status: 503, code: "unavailable", message: "GitHub is unavailable", unexpected: true},
		{name: "github 404", err: &gh.APIError{Status: 404, Method: "GET", Route: "/x"}, status: 500, code: "internal", message: "internal server error", unexpected: true},
		{name: "database down", err: fmt.Errorf("store: ping: %w", &pgconn.ConnectError{}), status: 503, code: "unavailable", message: "the database is unavailable", unexpected: true},
		{name: "deadline", err: fmt.Errorf("x: %w", context.DeadlineExceeded), status: 503, code: "unavailable", message: "the request timed out", unexpected: true},
		{name: "canceled", err: context.Canceled, status: 503, code: "unavailable", message: "the request was canceled"},
		{name: "anything else", err: errors.New("pq: secret table exploded"), status: 500, code: "internal", message: "internal server error", unexpected: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unexpected := toAPIError(tc.err)
			assert.Equal(t, tc.status, got.status)
			assert.Equal(t, tc.code, got.body.Code)
			assert.Equal(t, tc.message, got.body.Message)
			assert.Equal(t, tc.details, got.body.Details)
			assert.Equal(t, tc.unexpected, unexpected)
		})
	}
}

func TestWriteErrorLogsOnlyUnexpectedErrors(t *testing.T) {
	e := newEnv(t)
	rec := newRecorderFor(t, e, errors.New("disk on fire"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "disk on fire")
	assert.Contains(t, e.logs.String(), "disk on fire")

	e = newEnv(t)
	rec = newRecorderFor(t, e, principal.Wrap(principal.ErrNotFound, "run x"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, e.logs.String(), "request failed")

	rec = newRecorderFor(t, e, unauthorized("no"))
	assert.Equal(t, `Bearer realm="stackorder"`, rec.Header().Get("WWW-Authenticate"))
}
