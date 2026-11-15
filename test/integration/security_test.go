//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/server"
)

func TestSessionTTLSetting(t *testing.T) {
	e := NewEnv(t, WithVar(server.EnvSessionTTL, "1h"))
	const inst, org, user = 88, "hooli", "richard"
	newFixture(t, e, "platform", withInstallation(inst, org))
	e.GH.SetOrgMembership(org, user, gh.MembershipActive, "member")

	b := newBrowser(t, e)
	resp, body := b.signIn(user)
	require.Equal(t, http.StatusFound, resp.status, body)

	var session *http.Cookie
	for _, c := range resp.cookies {
		if c.Name == "stackorder_session" {
			session = c
		}
	}
	require.NotNil(t, session, "the callback sets the session cookie")
	assert.Equal(t, int(time.Hour/time.Second), session.MaxAge, "the cookie lifetime is the configured session TTL")

	var expires time.Time
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(),
		`SELECT expires_at FROM sessions WHERE login = $1`, user).Scan(&expires))
	assert.WithinDuration(t, time.Now().Add(time.Hour), expires, time.Minute,
		"the stored session expires one hour after it is created")
}

func TestHSTSHeaderSetting(t *testing.T) {
	on := NewEnv(t,
		WithVar(server.EnvBaseURL, "https://stackorder.example.test"),
		WithVar(server.EnvHSTSMaxAge, "8760h"),
		WithVar(server.EnvHSTSIncludeSubdomains, "true"),
	)
	assert.Equal(t, "max-age=31536000; includeSubDomains", hstsHeaderOf(t, on),
		"the configured header is sent when the base URL is https")

	off := NewEnv(t)
	assert.Empty(t, hstsHeaderOf(t, off), "no header without the setting")
}

func hstsHeaderOf(t *testing.T, e *Env) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.URL("/healthz"), nil)
	require.NoError(t, err)
	resp, err := e.Client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp.Header.Get("Strict-Transport-Security")
}
