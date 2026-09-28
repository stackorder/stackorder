package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeInvalid      = "invalid"
	codeConflict     = "conflict"
	codeRefused      = "refused"
	codeLocked       = "locked"
	codeSuperseded   = "superseded"
	codeInternal     = "internal"
	codeUnavailable  = "unavailable"
)

type apiError struct {
	status int
	body   v1.Error
}

func (e *apiError) Error() string { return e.body.Error() }

func newError(status int, code, message string) *apiError {
	return &apiError{status: status, body: v1.Error{Code: code, Message: message}}
}

func unauthorized(message string) *apiError {
	return newError(http.StatusUnauthorized, codeUnauthorized, message)
}

func forbidden(message string) *apiError {
	return newError(http.StatusForbidden, codeForbidden, message)
}

func notFound(message string) *apiError {
	return newError(http.StatusNotFound, codeNotFound, message)
}

func invalid(message string) *apiError {
	return newError(http.StatusBadRequest, codeInvalid, message)
}

func unavailable(message string) *apiError {
	return newError(http.StatusServiceUnavailable, codeUnavailable, message)
}

type gateFailure struct {
	Layer  int      `json:"layer"`
	Name   string   `json:"name"`
	Reason string   `json:"reason"`
	Stacks []string `json:"stacks,omitempty"`
}

func gateFailures(in []report.GateFailure) []gateFailure {
	out := make([]gateFailure, len(in))
	for i, f := range in {
		name := f.Name
		if name == "" {
			name = report.LayerName(f.Layer)
		}
		out[i] = gateFailure{Layer: f.Layer, Name: name, Reason: f.Reason, Stacks: f.Stacks}
	}
	return out
}

func lockConflicts(in []v1.LockInfo) []v1.LockInfo {
	if in == nil {
		return []v1.LockInfo{}
	}
	return in
}

func toAPIError(err error) (*apiError, bool) {
	var (
		ae      *apiError
		locked  *principal.LockedError
		refused *principal.RefusedError
		bad     *principal.InvalidError
		binding *oidc.ErrBinding
		ghErr   *gh.APIError
		connErr *pgconn.ConnectError
	)
	switch {
	case errors.As(err, &ae):
		return ae, false
	case errors.As(err, &locked):
		e := newError(http.StatusLocked, codeLocked, err.Error())
		e.body.Details = map[string]any{"conflicts": lockConflicts(locked.Conflicts)}
		return e, false
	case errors.Is(err, principal.ErrLocked):
		return newError(http.StatusLocked, codeLocked, err.Error()), false
	case errors.As(err, &refused):
		e := newError(http.StatusConflict, codeRefused, err.Error())
		e.body.Details = map[string]any{"failures": gateFailures(refused.Failures)}
		return e, false
	case errors.Is(err, principal.ErrRefused):
		return newError(http.StatusConflict, codeRefused, err.Error()), false
	case errors.As(err, &bad):
		e := invalid(err.Error())
		e.body.Details = map[string]any{"field": bad.Field, "reason": bad.Reason}
		return e, false
	case errors.Is(err, principal.ErrInvalid):
		return invalid(err.Error()), false
	case errors.Is(err, principal.ErrSuperseded):
		return newError(http.StatusConflict, codeSuperseded, err.Error()), false
	case errors.Is(err, principal.ErrConflict):
		return newError(http.StatusConflict, codeConflict, err.Error()), false
	case errors.Is(err, principal.ErrForbidden):
		return forbidden(err.Error()), false
	case errors.Is(err, principal.ErrNotFound):
		return notFound(err.Error()), false
	case errors.Is(err, oidc.ErrJWKSUnavailable):
		return unavailable("the runner token issuer's keys are unavailable"), false
	case errors.As(err, &binding):
		return forbidden(err.Error()), false
	case errors.Is(err, oidc.ErrInvalidToken):
		return unauthorized(err.Error()), false
	case errors.Is(err, store.ErrNotFound):
		return notFound("not found"), false
	case errors.Is(err, store.ErrInvalid):
		return invalid("invalid request"), false
	case errors.Is(err, store.ErrConflict):
		return newError(http.StatusConflict, codeConflict, "the request conflicts with the current state"), false
	case errors.Is(err, gh.ErrRateLimited), errors.As(err, &ghErr) && ghErr.Status >= http.StatusInternalServerError:
		return unavailable("GitHub is unavailable"), true
	case errors.As(err, &connErr):
		return unavailable("the database is unavailable"), true
	case errors.Is(err, context.DeadlineExceeded):
		return unavailable("the request timed out"), true
	case errors.Is(err, context.Canceled):
		return unavailable("the request was canceled"), false
	}
	return newError(http.StatusInternalServerError, codeInternal, "internal server error"), true
}
