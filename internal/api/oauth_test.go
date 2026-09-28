package api

import (
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type oauthEnv struct {
	*testEnv
	gh    *ghfake.Server
	clock *clock
}

func newOAuthEnv(t *testing.T, mutate ...func(*Config, *Deps)) *oauthEnv {
	t.Helper()
	fake := ghfake.New(t)
	fake.SetOAuthClient("Iv1.client", "client-secret")
	c := &clock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	base := func(cfg *Config, d *Deps) {
		cfg.OAuthClientID, cfg.OAuthClientSecret = "Iv1.client", "client-secret"
		cfg.GitHubWebURL, cfg.GitHubAPIURL = fake.URL(), fake.URL()
		cfg.SessionTTL = 48 * time.Hour
		d.HTTPClient = fake.HTTPClient()
		d.Clock = c.Now
	}
	e := newEnv(t, append([]func(*Config, *Deps){base}, mutate...)...)
	return &oauthEnv{testEnv: e, gh: fake, clock: c}
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (e *oauthEnv) startLogin(next string) (state string, cookie *http.Cookie) {
	e.t.Helper()
	target := "/auth/login"
	if next != "" {
		target += "?next=" + url.QueryEscape(next)
	}
	rec := e.do(newRequest(e.t, http.MethodGet, target, nil))
	require.Equal(e.t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(e.t, err)
	cookie = cookieNamed(rec.Result(), oauthCookie)
	require.NotNil(e.t, cookie)
	return loc.Query().Get("state"), cookie
}

func (e *oauthEnv) finish(state, code string, cookie *http.Cookie) *http.Response {
	e.t.Helper()
	q := url.Values{}
	q.Set("state", state)
	q.Set("code", code)
	r := newRequest(e.t, http.MethodGet, "/auth/callback?"+q.Encode(), nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return e.do(r).Result()
}

func TestLoginRedirectsToGitHub(t *testing.T) {
	e := newOAuthEnv(t)
	rec := e.do(newRequest(t, http.MethodGet, "/auth/login?next=%2Frepos%2Facme%2Finfra%3Ftab%3Druns", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, e.gh.URL()+"/login/oauth/authorize", loc.Scheme+"://"+loc.Host+loc.Path)
	q := loc.Query()
	assert.Equal(t, "Iv1.client", q.Get("client_id"))
	assert.Equal(t, testBaseURL+"/auth/callback", q.Get("redirect_uri"))
	assert.Equal(t, "read:org", q.Get("scope"))
	require.Len(t, q.Get("state"), 43)

	c := cookieNamed(rec.Result(), oauthCookie)
	require.NotNil(t, c)
	assert.Equal(t, "/auth", c.Path)
	assert.True(t, c.HttpOnly)
	assert.True(t, c.Secure, "cookies are Secure behind an https base URL")
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, int(oauthStateTTL/time.Second), c.MaxAge)
	st, ok := e.srv.unseal(oauthPurpose, c.Value)
	require.True(t, ok)
	assert.Equal(t, q.Get("state"), st.Value)
	assert.Equal(t, "/repos/acme/infra?tab=runs", st.Next)

	state2, _ := e.startLogin("")
	assert.NotEqual(t, q.Get("state"), state2, "every attempt gets a fresh state")

	plain := newOAuthEnv(t, func(c *Config, _ *Deps) { c.BaseURL = "http://localhost:8080" })
	rec = plain.do(newRequest(t, http.MethodGet, "/auth/login", nil))
	assert.False(t, cookieNamed(rec.Result(), oauthCookie).Secure)

	off := newOAuthEnv(t, func(c *Config, _ *Deps) { c.OAuthClientSecret = "" })
	rec = off.do(newRequest(t, http.MethodGet, "/auth/login", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "GITHUB_OAUTH_CLIENT_ID")
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                         "/",
		"/":                        "/",
		"/repos/acme/infra":        "/repos/acme/infra",
		"/runs/1?x=1&y=2":          "/runs/1?x=1&y=2",
		"/stacks/a%2Fb":            "/stacks/a%2Fb",
		"//evil.test/x":            "/",
		"/\\evil.test":             "/",
		"https://evil.test/":       "/",
		"javascript:alert(1)":      "/",
		"repos":                    "/",
		"/a\nb":                    "/",
		"/auth/login":              "/",
		"/auth/logout?x":           "/",
		"/authors":                 "/authors",
		"/%2F%2Fevil.test":         "/%2F%2Fevil.test",
		"/ok#frag":                 "/ok",
		"/x/../../y":               "/x/../../y",
		"/@evil.test":              "/@evil.test",
		"/path with spaces":        "/path%20with%20spaces",
		"/‮evil":                   "/%E2%80%AEevil",
		"/a?next=//evil.test":      "/a?next=//evil.test",
		"/\x7f":                    "/",
		"/foo\\bar":                "/",
		"///evil.test":             "/",
		"/.well-known/../..//x":    "/.well-known/../..//x",
		"http:/evil.test":          "/",
		"/%0d%0aSet-Cookie:%20a=b": "/%0d%0aSet-Cookie:%20a=b",
	}
	for in, want := range cases {
		assert.Equal(t, want, safeNext(in), "%q", in)
	}
}

func TestCallbackIssuesSession(t *testing.T) {
	e := newOAuthEnv(t)
	e.db.addRepo(100, 1, "acme", "acme/infra")
	e.gh.AddUser(gh.User{Login: "octocat", ID: 583231, AvatarURL: "https://avatars.githubusercontent.com/u/583231"})
	e.gh.SetOrgMembership("acme", "octocat", gh.MembershipActive, "member")
	e.gh.SetOrgMembership("zeta", "octocat", gh.MembershipActive, "member")
	e.gh.AddOAuthCode("code-1", "octocat")

	state, cookie := e.startLogin("/repos/acme/infra")
	resp := e.finish(state, "code-1", cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/repos/acme/infra", resp.Header.Get("Location"))

	session := cookieNamed(resp, sessionCookie)
	require.NotNil(t, session)
	assert.Equal(t, "/", session.Path)
	assert.True(t, session.HttpOnly)
	assert.True(t, session.Secure)
	assert.Equal(t, http.SameSiteLaxMode, session.SameSite)
	assert.Equal(t, int((48 * time.Hour).Seconds()), session.MaxAge)
	token, ok := e.srv.openSessionCookie(session.Value)
	require.True(t, ok, "the cookie is <token>.<hmac>")
	assert.True(t, clearedCookie(t, resp, oauthCookie), "the state cookie is single use")

	require.Len(t, e.db.created, 1)
	assert.Equal(t, store.NewSession{
		Login: "octocat", UserID: 583231, AvatarURL: "https://avatars.githubusercontent.com/u/583231",
		Orgs: []string{"acme", "zeta"}, TTL: 48 * time.Hour,
	}, e.db.created[0])
	_, known := e.db.sessions[token]
	assert.True(t, known)

	rec := e.do(withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), session))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, v1.Whoami{Login: "octocat", AvatarURL: "https://avatars.githubusercontent.com/u/583231", Orgs: []string{"acme", "zeta"}},
		decodeBody[v1.Whoami](t, rec))

	resp = e.finish(state, "code-1", cookie)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a used code is refused by GitHub")
}

func TestCallbackAcceptsAnInstallationOnTheUserAccount(t *testing.T) {
	e := newOAuthEnv(t)
	e.db.addRepo(300, 3, "SoloDev", "SoloDev/infra")
	e.gh.AddOAuthCode("code-2", "solodev")
	state, cookie := e.startLogin("")
	resp := e.finish(state, "code-2", cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/", resp.Header.Get("Location"))
	require.Len(t, e.db.created, 1)
	assert.Equal(t, []string{}, append([]string{}, e.db.created[0].Orgs...))
}

func TestCallbackRefusesUsersWithoutAnInstallation(t *testing.T) {
	e := newOAuthEnv(t, func(c *Config, _ *Deps) { c.AppSlug = "stackorder-acme" })
	e.db.addRepo(100, 1, "acme", "acme/infra")
	e.db.addRepo(200, 2, "globex", "globex/platform")
	now := time.Now()
	e.db.installations[1].SuspendedAt = &now
	e.gh.SetOrgMembership("globex", "mallory", gh.MembershipActive, "member")
	e.gh.SetOrgMembership("initech", "mallory", gh.MembershipActive, "member")
	e.gh.SetOrgMembership("acme", "mallory", gh.MembershipPending, "member")
	e.gh.AddOAuthCode("code-3", "mallory")

	state, cookie := e.startLogin("")
	resp := e.finish(state, "code-3", cookie)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Nil(t, cookieNamed(resp, sessionCookie))
	assert.Empty(t, e.db.created, "no session is stored")
	body := readBody(t, resp)
	assert.Contains(t, body, "Stackorder is not installed for your organisations")
	assert.Contains(t, body, "mallory")
	assert.Contains(t, body, "<code>globex</code>", "a suspended installation does not count")
	assert.Contains(t, body, "<code>initech</code>")
	assert.NotContains(t, body, "<code>acme</code>", "pending memberships are not reported by GitHub")
	assert.Contains(t, body, e.gh.URL()+"/apps/stackorder-acme/installations/new")
	assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "default-src 'none'")
}

func TestCallbackReadsTheSlugFromTheApp(t *testing.T) {
	e := newOAuthEnv(t)
	e.srv.app = e.gh.NewApp()
	e.gh.AddOAuthCode("code-4", "nobody")
	state, cookie := e.startLogin("")
	resp := e.finish(state, "code-4", cookie)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	body := readBody(t, resp)
	assert.Contains(t, body, "/apps/stackorder-test/installations/new")
	assert.Contains(t, body, "GitHub reported no organisations for you")
}

func TestCallbackFailures(t *testing.T) {
	e := newOAuthEnv(t)
	e.db.addRepo(100, 1, "acme", "acme/infra")
	e.gh.SetOrgMembership("acme", "octocat", gh.MembershipActive, "member")

	state, cookie := e.startLogin("")
	resp := e.finish("other-state", "code", cookie)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "state mismatch")
	assert.Contains(t, readBody(t, resp), "cannot be completed")

	resp = e.finish(state, "code", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "no state cookie")

	forged := *cookie
	forged.Value = e.srv.seal("setup", sealed{Value: state, Expires: e.clock.Now().Add(time.Hour).Unix()})
	resp = e.finish(state, "code", &forged)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a cookie sealed for another purpose")

	state, cookie = e.startLogin("")
	e.clock.Advance(oauthStateTTL + time.Second)
	resp = e.finish(state, "code", cookie)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "expired state")

	state, cookie = e.startLogin("")
	resp = e.finish(state, "unknown-code", cookie)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "GitHub refused the sign-in code")

	state, cookie = e.startLogin("")
	resp = e.finish(state, "", cookie)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "missing code")

	state, cookie = e.startLogin("")
	q := url.Values{"state": {state}, "error": {"access_denied"}, "error_description": {"The user has denied your application access."}}
	r := withCookie(newRequest(t, http.MethodGet, "/auth/callback?"+q.Encode(), nil), cookie)
	rec := e.do(r)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "The user has denied your application access.")

	e.gh.AddOAuthCode("code-5", "octocat")
	e.gh.FailNext("GET /user", http.StatusInternalServerError, 10)
	state, cookie = e.startLogin("")
	resp = e.finish(state, "code-5", cookie)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "GitHub could not be reached")
	assert.Contains(t, e.logs.String(), "get authenticated user")
	assert.Empty(t, e.db.created)
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	cookie := e.session("octocat", "acme")
	token, _ := e.srv.openSessionCookie(cookie.Value)

	r := withCookie(newRequest(t, http.MethodPost, "/auth/logout", nil), cookie)
	r.Header.Set("Origin", "https://evil.test")
	rec := e.do(r)
	assert.Equal(t, http.StatusForbidden, rec.Code, "a cross-site form cannot sign people out")
	assert.Empty(t, e.db.deleted)

	rec = e.do(withCookie(sameOriginPost(t, "/auth/logout", nil), cookie))
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
	assert.Equal(t, []string{token}, e.db.deleted)
	assert.True(t, clearedCookie(t, rec.Result(), sessionCookie))
	assert.Contains(t, e.logs.String(), `"route":"POST /auth/logout"`)

	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/v1/me", nil), cookie))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the session is gone")

	rec = e.do(sameOriginPost(t, "/auth/logout", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code, "signing out twice is harmless")
	rec = e.do(withCookie(sameOriginPost(t, "/auth/logout", nil), &http.Cookie{Name: sessionCookie, Value: "forged.00"}))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, e.db.deleted, 1, "forged cookies are not looked up")

	rec = e.do(newRequest(t, http.MethodGet, "/auth/logout", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, codeNotFound, errorOf(t, rec).Code)
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(data)
}
