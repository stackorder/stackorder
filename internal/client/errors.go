package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	// CodeUnauthorized is the v1.Error code for a missing or rejected token.
	CodeUnauthorized = "unauthorized"
	// CodeForbidden is the v1.Error code for an authenticated caller that may
	// not perform the request.
	CodeForbidden = "forbidden"
	// CodeNotFound is the v1.Error code for an unknown run, stack or route.
	CodeNotFound = "not_found"
	// CodeConflict is the v1.Error code for a request that conflicts with the
	// run's state.
	CodeConflict = "conflict"
	// CodeInvalid is the v1.Error code for a malformed request.
	CodeInvalid = "invalid"
	// CodeLocked is the v1.Error code for a stack held by another lock.
	CodeLocked = "locked"
	// CodeUnconfirmed is the v1.Error code for a request the server refuses
	// because the run could not be confirmed.
	CodeUnconfirmed = "unconfirmed"
	// CodeInternal is the v1.Error code for a server failure.
	CodeInternal = "internal"
)

const maxErrorBody = 1 << 20

var (
	// ErrUnreachable matches network errors, timeouts and 5xx answers that
	// persisted through every retry, from the server or from the runner's
	// OIDC token endpoint. The CLI degrades on it for plans and fails closed
	// for applies.
	ErrUnreachable = errors.New("server unreachable")
	// ErrUnauthorized matches a 401 or an unauthorized code.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden matches a 403 or a forbidden code.
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound matches a 404 or a not_found code.
	ErrNotFound = errors.New("not found")
	// ErrConflict matches a 409 or a conflict code.
	ErrConflict = errors.New("conflict")
	// ErrLocked matches a 423 or a locked code.
	ErrLocked = errors.New("locked")
	// ErrInvalid matches a 400, a 422 or an invalid code.
	ErrInvalid = errors.New("invalid request")
	// ErrRefused matches every deliberate refusal the CLI reports with exit
	// code 3: the forbidden, conflict, locked and unconfirmed codes and their
	// statuses 403, 409 and 423.
	ErrRefused = errors.New("refused by the server")
)

// Error is a non-2xx answer from the server, decoded from its v1.Error body.
// When the body is not a v1.Error, Code is derived from Status and Message
// holds the start of the body.
type Error struct {
	// Status is the HTTP status code.
	Status int
	// Code is the v1.Error code, such as CodeLocked.
	Code string
	// Message is the server's human readable reason.
	Message string
	// Details is the decoded details value, when the server sent one.
	Details any
}

// Error implements the error interface.
func (e *Error) Error() string {
	msg := "server returned " + strconv.Itoa(e.Status) + " " + e.Code
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Is lets errors.Is match the sentinel errors of this package by code or
// status.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Code == CodeUnauthorized || e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Code == CodeForbidden || e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Code == CodeNotFound || e.Status == http.StatusNotFound
	case ErrConflict:
		return e.Code == CodeConflict || e.Status == http.StatusConflict
	case ErrLocked:
		return e.Code == CodeLocked || e.Status == http.StatusLocked
	case ErrInvalid:
		return e.Code == CodeInvalid || e.Status == http.StatusBadRequest || e.Status == http.StatusUnprocessableEntity
	case ErrRefused:
		switch e.Code {
		case CodeForbidden, CodeConflict, CodeLocked, CodeUnconfirmed:
			return true
		}
		switch e.Status {
		case http.StatusForbidden, http.StatusConflict, http.StatusLocked:
			return true
		}
	}
	return false
}

// IsUnreachable reports whether err means the server could not be reached.
func IsUnreachable(err error) bool { return errors.Is(err, ErrUnreachable) }

func decodeError(resp *http.Response) *Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &Error{Status: resp.StatusCode}
	var wire v1.Error
	if err := json.Unmarshal(body, &wire); err == nil && wire.Code != "" {
		e.Code, e.Message, e.Details = wire.Code, wire.Message, wire.Details
		return e
	}
	e.Code = codeForStatus(resp.StatusCode)
	e.Message = snippet(body)
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	return e
}

func codeForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return CodeUnauthorized
	case status == http.StatusForbidden:
		return CodeForbidden
	case status == http.StatusNotFound:
		return CodeNotFound
	case status == http.StatusConflict:
		return CodeConflict
	case status == http.StatusLocked:
		return CodeLocked
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		return CodeInvalid
	case status >= http.StatusInternalServerError:
		return CodeInternal
	}
	return fmt.Sprintf("http_%d", status)
}

func snippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) <= 200 {
		return s
	}
	n := 200
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
