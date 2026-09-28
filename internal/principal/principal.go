// Package principal identifies who is calling the server and defines the
// error vocabulary the API layer maps to HTTP status codes. It is shared by
// internal/runs, which produces the errors, and internal/api, which maps
// them, so neither has to import the other.
package principal

import (
	"errors"
	"fmt"
	"strings"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/report"
)

// Kind says how the caller authenticated.
type Kind string

const (
	// OIDC is a GitHub Actions job presenting a runner token.
	OIDC Kind = "oidc"
	// APIKey is automation presenting an sk_ key.
	APIKey Kind = "apikey"
	// Session is a human signed in through GitHub OAuth.
	Session Kind = "session"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	Kind Kind
	// Login is the GitHub login for OIDC and Session principals and the key
	// name for APIKey principals.
	Login string
	// Claims is set for OIDC principals.
	Claims *oidc.Claims
	// APIKeyID is set for APIKey principals.
	APIKeyID string
}

// Actor renders the principal for audit rows and comments.
func (p Principal) Actor() string {
	switch p.Kind {
	case APIKey:
		return "apikey:" + p.Login
	default:
		return p.Login
	}
}

// Sentinel errors returned by services and mapped by the API layer.
var (
	ErrNotFound   = errors.New("not found")
	ErrForbidden  = errors.New("forbidden")
	ErrConflict   = errors.New("conflict")
	ErrInvalid    = errors.New("invalid")
	ErrLocked     = errors.New("locked")
	ErrRefused    = errors.New("refused")
	ErrSuperseded = errors.New("superseded")
)

// LockedError reports the orchestration locks that prevented an action.
type LockedError struct {
	Conflicts []v1.LockInfo
}

// Error implements error.
func (e *LockedError) Error() string {
	keys := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		if c.PRNumber > 0 {
			keys = append(keys, fmt.Sprintf("%s (PR #%d)", c.StackKey, c.PRNumber))
		} else {
			keys = append(keys, c.StackKey)
		}
	}
	return "locked: " + strings.Join(keys, ", ")
}

// Is makes errors.Is(err, ErrLocked) true.
func (e *LockedError) Is(target error) bool { return target == ErrLocked }

// RefusedError reports why the apply gate refused a request.
type RefusedError struct {
	Failures []report.GateFailure
}

// Error implements error.
func (e *RefusedError) Error() string {
	reasons := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		reasons = append(reasons, fmt.Sprintf("layer %d %s: %s", f.Layer, f.Name, f.Reason))
	}
	return "refused: " + strings.Join(reasons, "; ")
}

// Is makes errors.Is(err, ErrRefused) true.
func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// InvalidError reports a malformed request field.
type InvalidError struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *InvalidError) Error() string {
	if e.Field == "" {
		return "invalid: " + e.Reason
	}
	return "invalid " + e.Field + ": " + e.Reason
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

// Wrap adds context to a sentinel while keeping errors.Is working.
func Wrap(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}
