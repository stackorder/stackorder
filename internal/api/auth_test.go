package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func (e *testEnv) apiKey(name string) string {
	e.t.Helper()
	plaintext := "sk_" + name + "_" + uuid.NewString()
	e.db.mu.Lock()
	e.db.keys[plaintext] = store.APIKey{ID: uuid.New(), Name: name}
	e.db.mu.Unlock()
	return plaintext
}

func (e *testEnv) session(login string, orgs ...string) *http.Cookie {
	e.t.Helper()
	token := "session-" + uuid.NewString()
	e.db.mu.Lock()
	e.db.sessions[token] = store.Session{Login: login, AvatarURL: "https://avatars.githubusercontent.com/u/1", Orgs: orgs}
	e.db.mu.Unlock()
	return &http.Cookie{Name: sessionCookie, Value: e.srv.sessionCookieValue(token)}
}

func bearer(r *http.Request, token string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func withCookie(r *http.Request, c *http.Cookie) *http.Request {
	r.AddCookie(c)
	return r
}

func clearedCookie(t *testing.T, resp *http.Response, name string) bool {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c.MaxAge < 0 && c.Value == ""
		}
	}
	return false
}

func TestMeWithAPIKey(t *testing.T) {
	e := newEnv(t)
	key := e.apiKey("ci")
	rec := e.do(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), key))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, v1.Whoami{Login: "apikey:ci", Orgs: []string{}, Admin: true}, decodeBody[v1.Whoami](t, rec))
	assert.Contains(t, e.logs.String(), `"principal":"apikey"`)

	revoked := e.apiKey("old")
	now := time.Now()
	e.db.mu.Lock()
	k := e.db.keys[revoked]
	k.RevokedAt = &now
	e.db.keys[revoked] = k
	e.db.mu.Unlock()
	rec = e.do(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), revoked))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "the API key is unknown or revoked", errorOf(t, rec).Message)

	rec = e.do(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), "sk_never_issued"))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMeWithSession(t *testing.T) {
	e := newEnv(t)
	cookie := e.session("octocat", "acme", "globex")
	rec := e.do(withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), cookie))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, v1.Whoami{Login: "octocat", AvatarURL: "https://avatars.githubusercontent.com/u/1", Orgs: []string{"acme", "globex"}},
		decodeBody[v1.Whoami](t, rec))

	lonely := e.session("solo")
	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), lonely))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{}, decodeBody[v1.Whoami](t, rec).Orgs)
}

func TestForgedSessionCookiesNeverReachTheStore(t *testing.T) {
	e := newEnv(t)
	valid := e.session("octocat", "acme")
	token, ok := e.srv.openSessionCookie(valid.Value)
	require.True(t, ok)
	other := newEnv(t, func(c *Config, _ *Deps) { c.SessionKey = []byte("another key of thirty two bytes!") })

	forged := map[string]string{
		"unsigned":          token,
		"empty signature":   token + ".",
		"wrong signature":   token + "." + other.srv.sign(token),
		"truncated":         valid.Value[:len(valid.Value)-2],
		"not hex":           token + ".zz" + valid.Value[len(token)+3:],
		"signature only":    "." + e.srv.sign(""),
		"other token":       "session-guess." + e.srv.sign("session-guess-x"),
		"trailing garbage":  valid.Value + "00",
		"signature swapped": e.srv.sign(token) + "." + token,
	}
	for name, value := range forged {
		t.Run(name, func(t *testing.T) {
			e.db.mu.Lock()
			e.db.sessionReads = 0
			e.db.mu.Unlock()
			rec := e.do(withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), &http.Cookie{Name: sessionCookie, Value: value}))
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, "the session cookie is invalid; sign in again", errorOf(t, rec).Message)
			assert.True(t, clearedCookie(t, rec.Result(), sessionCookie), "the bad cookie is cleared")
			e.db.mu.Lock()
			defer e.db.mu.Unlock()
			assert.Zero(t, e.db.sessionReads, "the store is not consulted for a forged cookie")
		})
	}

	signedUnknown := &http.Cookie{Name: sessionCookie, Value: e.srv.sessionCookieValue("expired-or-deleted")}
	rec := e.do(withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), signedUnknown))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "the session has expired; sign in again", errorOf(t, rec).Message)
	assert.Equal(t, 1, e.db.sessionReads)
}

func TestAuthenticationFailures(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name    string
		header  []string
		message string
	}{
		{"no credentials", nil, "sign in or use an API key"},
		{"basic scheme", []string{"Basic b2N0bzpjYXQ="}, "the Authorization header must carry a single Bearer token"},
		{"two headers", []string{"Bearer a", "Bearer b"}, "the Authorization header must carry a single Bearer token"},
		{"empty bearer", []string{"Bearer "}, "the Authorization header must carry a single Bearer token"},
		{"runner token", []string{"Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig"}, "runner tokens are not accepted on this endpoint; sign in or use an API key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRequest(t, http.MethodGet, "/v1/me", nil)
			for _, h := range tc.header {
				r.Header.Add("Authorization", h)
			}
			rec := e.do(r)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, tc.message, errorOf(t, rec).Message)
			assert.Equal(t, `Bearer realm="stackorder"`, rec.Header().Get("WWW-Authenticate"))
		})
	}

	key := e.apiKey("ci")
	r := withCookie(bearer(newRequest(t, http.MethodGet, "/v1/me", nil), key), &http.Cookie{Name: sessionCookie, Value: "forged"})
	rec := e.do(r)
	require.Equal(t, http.StatusOK, rec.Code, "the Authorization header wins over a cookie")
	assert.Equal(t, "apikey:ci", decodeBody[v1.Whoami](t, rec).Login)
}

func TestCheckSameOrigin(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		origin string
		site   string
		ok     bool
	}{
		{name: "same origin", origin: testBaseURL, ok: true},
		{name: "same origin, other case", origin: "HTTPS://Stackorder.Test", ok: true},
		{name: "sec-fetch-site only", site: "same-origin", ok: true},
		{name: "origin wins over sec-fetch-site", origin: "https://evil.test", site: "same-origin"},
		{name: "other origin", origin: "https://evil.test"},
		{name: "other scheme", origin: "http://stackorder.test"},
		{name: "other port", origin: "https://stackorder.test:8443"},
		{name: "null origin", origin: "null"},
		{name: "same site", site: "same-site"},
		{name: "cross site", site: "cross-site"},
		{name: "no headers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRequest(t, http.MethodPost, "/v1/x", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			err := e.srv.checkSameOrigin(r)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, http.StatusForbidden, statusOf(err))
		})
	}
}

func TestIdentityVisibility(t *testing.T) {
	key := identity{Principal: principal.Principal{Kind: principal.APIKey, Login: "ci"}}
	assert.True(t, key.sees("anyone"))
	assert.Nil(t, key.accounts())

	sess := identity{
		Principal: principal.Principal{Kind: principal.Session, Login: "octocat"},
		session:   &store.Session{Login: "octocat", Orgs: []string{"acme"}},
	}
	assert.True(t, sess.sees("ACME"))
	assert.True(t, sess.sees("OctoCat"))
	assert.False(t, sess.sees("globex"))
	assert.False(t, sess.sees(""))
	assert.Equal(t, []string{"octocat", "acme"}, sess.accounts())

	runner := identity{Principal: principal.Principal{Kind: principal.OIDC, Login: "octocat"}}
	assert.False(t, runner.sees("octocat"))
}
