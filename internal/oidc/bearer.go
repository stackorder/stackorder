package oidc

import (
	"net/http"
	"strings"
)

// BearerToken extracts the credentials of an "Authorization: Bearer <token>"
// header. The scheme is case insensitive and surrounding blanks are
// ignored. It reports false when the header is missing, repeated, uses
// another scheme, or carries an empty token or one containing blanks.
func BearerToken(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(values[0]), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}
