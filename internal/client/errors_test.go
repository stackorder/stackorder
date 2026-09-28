package client

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var allSentinels = []error{ErrUnreachable, ErrUnauthorized, ErrForbidden, ErrNotFound, ErrConflict, ErrLocked, ErrInvalid, ErrRefused}

func TestDecodeError(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantCode    string
		wantMessage string
		wantDetails any
		wantIs      []error
	}{
		{
			name: "unauthorized", status: 401, body: `{"code":"unauthorized","message":"token expired"}`,
			wantCode: CodeUnauthorized, wantMessage: "token expired", wantIs: []error{ErrUnauthorized},
		},
		{
			name: "forbidden", status: 403, body: `{"code":"forbidden","message":"environment claim does not match"}`,
			wantCode: CodeForbidden, wantMessage: "environment claim does not match", wantIs: []error{ErrForbidden, ErrRefused},
		},
		{
			name: "not found", status: 404, body: `{"code":"not_found","message":"run not found"}`,
			wantCode: CodeNotFound, wantMessage: "run not found", wantIs: []error{ErrNotFound},
		},
		{
			name: "conflict with details", status: 409, body: `{"code":"conflict","message":"head moved","details":{"sha":"abc"}}`,
			wantCode: CodeConflict, wantMessage: "head moved", wantDetails: map[string]any{"sha": "abc"}, wantIs: []error{ErrConflict, ErrRefused},
		},
		{
			name: "locked as 423", status: 423, body: `{"code":"locked","message":"stacks/prod/vpc is locked by #12"}`,
			wantCode: CodeLocked, wantMessage: "stacks/prod/vpc is locked by #12", wantIs: []error{ErrLocked, ErrRefused},
		},
		{
			name: "locked code on 409", status: 409, body: `{"code":"locked","message":"locked"}`,
			wantCode: CodeLocked, wantMessage: "locked", wantIs: []error{ErrLocked, ErrConflict, ErrRefused},
		},
		{
			name: "unconfirmed", status: 409, body: `{"code":"unconfirmed","message":"run is unconfirmed"}`,
			wantCode: CodeUnconfirmed, wantMessage: "run is unconfirmed", wantIs: []error{ErrConflict, ErrRefused},
		},
		{
			name: "unconfirmed on 412", status: 412, body: `{"code":"unconfirmed","message":"gate refused"}`,
			wantCode: CodeUnconfirmed, wantMessage: "gate refused", wantIs: []error{ErrRefused},
		},
		{
			name: "invalid", status: 400, body: `{"code":"invalid","message":"sha is required"}`,
			wantCode: CodeInvalid, wantMessage: "sha is required", wantIs: []error{ErrInvalid},
		},
		{
			name: "plain text 422", status: 422, body: "bad body\n",
			wantCode: CodeInvalid, wantMessage: "bad body", wantIs: []error{ErrInvalid},
		},
		{
			name: "html 404 from a proxy", status: 404, body: "<html>\n  <body>Not Found</body>\n</html>",
			wantCode: CodeNotFound, wantMessage: "<html> <body>Not Found</body> </html>", wantIs: []error{ErrNotFound},
		},
		{
			name: "empty 401", status: 401, body: "",
			wantCode: CodeUnauthorized, wantMessage: "Unauthorized", wantIs: []error{ErrUnauthorized},
		},
		{
			name: "internal", status: 500, body: `{"code":"internal","message":"database unavailable"}`,
			wantCode: CodeInternal, wantMessage: "database unavailable",
		},
		{
			name: "bad gateway without body", status: 502, body: "",
			wantCode: CodeInternal, wantMessage: "Bad Gateway",
		},
		{
			name: "teapot", status: 418, body: "short and stout",
			wantCode: "http_418", wantMessage: "short and stout",
		},
		{
			name: "json without code", status: 403, body: `{"message":"nope"}`,
			wantCode: CodeForbidden, wantMessage: `{"message":"nope"}`, wantIs: []error{ErrForbidden, ErrRefused},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rec.WriteHeader(tt.status)
			_, _ = rec.WriteString(tt.body)
			e := decodeError(rec.Result())
			assert.Equal(t, tt.status, e.Status)
			assert.Equal(t, tt.wantCode, e.Code)
			assert.Equal(t, tt.wantMessage, e.Message)
			assert.Equal(t, tt.wantDetails, e.Details)
			for _, s := range allSentinels {
				want := false
				for _, w := range tt.wantIs {
					want = want || w == s
				}
				assert.Equal(t, want, errors.Is(e, s), "errors.Is(%s, %v)", tt.name, s)
			}
			wrapped := fmt.Errorf("client: POST /v1/runs: %w", e)
			var got *Error
			assert.ErrorAs(t, wrapped, &got)
		})
	}
}

func TestErrorString(t *testing.T) {
	tests := []struct {
		err  *Error
		want string
	}{
		{err: &Error{Status: 409, Code: "locked", Message: "stacks/prod/vpc is locked by #12"}, want: "server returned 409 locked: stacks/prod/vpc is locked by #12"},
		{err: &Error{Status: 404, Code: "not_found"}, want: "server returned 404 not_found"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.err.Error())
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("é", 150)
	got := snippet([]byte(long))
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.LessOrEqual(t, len(got), 200+len("…"))
	assert.Equal(t, "a b c", snippet([]byte("  a\n\tb   c \n")))
}

func TestIsUnreachable(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{err: nil, want: false},
		{err: errors.New("other"), want: false},
		{err: ErrUnreachable, want: true},
		{err: fmt.Errorf("client: GET /v1/me: 4 attempts: %w", fmt.Errorf("%w: %w", ErrUnreachable, &Error{Status: http.StatusBadGateway, Code: CodeInternal})), want: true},
		{err: &Error{Status: http.StatusInternalServerError, Code: CodeInternal}, want: false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, IsUnreachable(tt.err), "%v", tt.err)
	}
}
