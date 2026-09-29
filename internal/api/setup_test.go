package api

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

type untouchedStore struct{ dataStore }

type setupEnv struct {
	*testEnv
	gh *ghfake.Server
}

func newSetupEnv(t *testing.T, mutate ...func(*Config, *Deps)) *setupEnv {
	t.Helper()
	fake := ghfake.New(t)
	base := func(c *Config, d *Deps) {
		c.SetupMode = true
		c.GitHubWebURL, c.GitHubAPIURL = gh.DefaultWebURL, fake.URL()
		d.HTTPClient = fake.HTTPClient()
		d.Runs = nil
	}
	e := newEnv(t, append([]func(*Config, *Deps){base}, mutate...)...)
	e.srv = newServer(e.cfg, e.deps, untouchedStore{})
	e.h = e.srv.routes()
	return &setupEnv{testEnv: e, gh: fake}
}

var (
	formAction    = regexp.MustCompile(`<form id="manifest-form" method="post" action="([^"]+)">`)
	manifestInput = regexp.MustCompile(`<input type="hidden" name="manifest" value="([^"]+)">`)
)

type setupForm struct {
	action   *url.URL
	manifest map[string]any
	cookie   *http.Cookie
	csp      string
	body     string
}

func (e *setupEnv) open(t *testing.T, target string) setupForm {
	t.Helper()
	rec := e.do(newRequest(t, http.MethodGet, target, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	m := formAction.FindStringSubmatch(body)
	require.NotNil(t, m, body)
	action, err := url.Parse(html.UnescapeString(m[1]))
	require.NoError(t, err)
	mi := manifestInput.FindStringSubmatch(body)
	require.NotNil(t, mi, body)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(html.UnescapeString(mi[1])), &manifest))
	return setupForm{action: action, manifest: manifest, cookie: cookieNamed(rec.Result(), setupCookie), csp: rec.Header().Get("Content-Security-Policy"), body: body}
}

func TestSetupPagePostsTheManifest(t *testing.T) {
	e := newSetupEnv(t)
	f := e.open(t, "/setup")

	assert.Equal(t, "https://github.com/settings/apps/new", f.action.Scheme+"://"+f.action.Host+f.action.Path)
	state := f.action.Query().Get("state")
	require.NotEmpty(t, state)
	require.NotNil(t, f.cookie)
	assert.Equal(t, "/setup", f.cookie.Path)
	assert.True(t, f.cookie.HttpOnly)
	st, ok := e.srv.unseal(setupPurpose, f.cookie.Value)
	require.True(t, ok)
	assert.Equal(t, state, st.Value, "the nonce in the URL is the one in the signed cookie")

	perms := map[string]any{}
	for k, v := range gh.DefaultPermissions {
		perms[k] = v
	}
	assert.Equal(t, perms, f.manifest["default_permissions"], "the design's permissions, deployments included on github.com")
	events := make([]any, len(gh.DefaultEvents))
	for i, ev := range gh.DefaultEvents {
		events[i] = ev
	}
	assert.Equal(t, events, f.manifest["default_events"])
	assert.Equal(t, map[string]any{"url": testBaseURL + "/webhooks/github", "active": true}, f.manifest["hook_attributes"])
	assert.Equal(t, testBaseURL+"/setup/callback", f.manifest["redirect_url"])
	assert.Equal(t, []any{testBaseURL + "/auth/callback"}, f.manifest["callback_urls"])
	assert.Equal(t, testBaseURL+"/setup/installed", f.manifest["setup_url"])
	assert.Equal(t, "stackorder-stackorder-test", f.manifest["name"])
	assert.Equal(t, false, f.manifest["public"])

	assert.Contains(t, f.csp, "form-action https://github.com")
	nonce := regexp.MustCompile(`script-src 'nonce-([^']+)'`).FindStringSubmatch(f.csp)
	require.NotNil(t, nonce, f.csp)
	assert.Contains(t, f.body, `<script nonce="`+nonce[1]+`">document.getElementById("manifest-form").submit();</script>`, "the form submits itself")
	assert.Contains(t, f.body, `<button type="submit">`, "and has a button for browsers without scripts")
	assert.Contains(t, f.body, "<code>actions</code>: write")

	again := e.open(t, "/setup")
	assert.NotEqual(t, state, again.action.Query().Get("state"))
}

func TestPageNoncesSurviveAttributeEscaping(t *testing.T) {
	e := newEnv(t)
	nonceOf := regexp.MustCompile(`style-src 'nonce-([^']+)'`)
	for range 64 {
		rec := e.do(newRequest(t, http.MethodGet, "/setup/installed", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		m := nonceOf.FindStringSubmatch(rec.Header().Get("Content-Security-Policy"))
		require.NotNil(t, m, rec.Header().Get("Content-Security-Policy"))
		require.Contains(t, rec.Body.String(), `<style nonce="`+m[1]+`">`, "the markup carries the header's nonce byte for byte")
	}
}

func TestSetupPageOptions(t *testing.T) {
	e := newSetupEnv(t)
	f := e.open(t, "/setup?org=acme-corp&name=Stackorder+Acme")
	assert.Equal(t, "/organizations/acme-corp/settings/apps/new", f.action.Path)
	assert.Equal(t, "Stackorder Acme", f.manifest["name"])
	assert.Contains(t, f.body, "the <strong>acme-corp</strong> organisation")

	for _, target := range []string{"/setup?org=acme/../evil", "/setup?org=-acme", "/setup?name=" + strings.Repeat("x", 35), "/setup?name=%3Cscript%3E"} {
		rec := e.do(newRequest(t, http.MethodGet, target, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, target)
		assert.Nil(t, cookieNamed(rec.Result(), setupCookie), target)
	}

	ghes := newSetupEnv(t, func(c *Config, _ *Deps) { c.GitHubWebURL = "https://ghe.example.com" })
	f = ghes.open(t, "/setup")
	assert.Equal(t, "https://ghe.example.com/settings/apps/new", f.action.Scheme+"://"+f.action.Host+f.action.Path)
	assert.NotContains(t, f.manifest["default_permissions"], "deployments")
	assert.NotContains(t, f.manifest["default_events"], gh.EventDeploymentProtectionRule)
	assert.Contains(t, f.csp, "form-action https://ghe.example.com")
}

func TestSetupWhenAlreadyConfigured(t *testing.T) {
	e := newSetupEnv(t, func(c *Config, _ *Deps) { c.SetupMode = false })
	rec := e.do(newRequest(t, http.MethodGet, "/setup", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "This server already has a GitHub App")
	assert.NotContains(t, rec.Body.String(), "manifest-form")
	assert.NotContains(t, rec.Body.String(), "force=1", "no link to a re-setup that is disabled")
	assert.Nil(t, cookieNamed(rec.Result(), setupCookie))

	rec = e.do(newRequest(t, http.MethodGet, "/setup?force=1", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "creating another App needs STACKORDER_ALLOW_RESETUP")
	assert.NotContains(t, rec.Body.String(), "manifest-form")
	assert.Nil(t, cookieNamed(rec.Result(), setupCookie))
	e.gh.SetManifestConversion("code-3", gh.AppCredentials{ID: 3, Slug: "s", PEM: "pem", WebhookSecret: "w"})
	forged := &http.Cookie{Name: setupCookie, Value: e.srv.seal(setupPurpose, sealed{Value: "st", Expires: time.Now().Add(time.Hour).Unix()})}
	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-3&state=st", nil), forged))
	assert.Equal(t, http.StatusNotFound, rec.Code, "nor does the callback convert a code")
	assert.NotContains(t, rec.Body.String(), "GITHUB_APP_ID=")

	allowed := newSetupEnv(t, func(c *Config, _ *Deps) { c.SetupMode, c.AllowResetup = false, true })
	rec = allowed.do(newRequest(t, http.MethodGet, "/setup", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `href="/setup?force=1"`)
	f := allowed.open(t, "/setup?force=1")
	state := f.action.Query().Get("state")
	assert.NotEmpty(t, state)
	allowed.gh.SetManifestConversion("code-4", gh.AppCredentials{ID: 4, Slug: "s", PEM: "pem", WebhookSecret: "w"})
	rec = allowed.do(withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-4&state="+url.QueryEscape(state), nil), f.cookie))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "GITHUB_APP_ID=4")
}

func TestSetupCallbackPrintsTheCredentialsOnce(t *testing.T) {
	e := newSetupEnv(t)
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAsecret+key/material==\n-----END RSA PRIVATE KEY-----\n"
	e.gh.SetManifestConversion("code-9", gh.AppCredentials{
		ID: 424242, Slug: "stackorder-acme", Name: "Stackorder Acme", PEM: pem,
		WebhookSecret: "whsec-123", ClientID: "Iv1.abc", ClientSecret: "oauth-secret-456",
	})
	f := e.open(t, "/setup")
	state := f.action.Query().Get("state")

	r := withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-9&state="+url.QueryEscape(state), nil), f.cookie)
	rec := e.do(r)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := html.UnescapeString(rec.Body.String())
	for _, want := range []string{
		"GITHUB_APP_ID=424242",
		"GITHUB_WEBHOOK_SECRET=whsec-123",
		"GITHUB_OAUTH_CLIENT_ID=Iv1.abc",
		"GITHUB_OAUTH_CLIENT_SECRET=oauth-secret-456",
		"GITHUB_APP_PRIVATE_KEY=\n" + strings.TrimSpace(pem),
		"Secrets Manager",
		"https://github.com/apps/stackorder-acme/installations/new",
		"Next steps",
		"audience " + testBaseURL,
	} {
		assert.Contains(t, body, want)
	}
	assert.NotContains(t, body, "GITHUB_API_URL", "github.com needs no API URL")
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.True(t, clearedCookie(t, rec.Result(), setupCookie))
	logs := e.logs.String()
	assert.Contains(t, logs, `"app_id":424242`)
	for _, secret := range []string{"whsec-123", "oauth-secret-456", "secret+key"} {
		assert.NotContains(t, logs, secret, "credentials are never logged")
	}

	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-9&state="+url.QueryEscape(state), nil), f.cookie))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "GitHub converts a code once")
	assert.Contains(t, rec.Body.String(), "no longer accepts this code")
	assert.NotContains(t, rec.Body.String(), "whsec-123")
}

func TestSetupCallbackRefusals(t *testing.T) {
	e := newSetupEnv(t)
	e.gh.SetManifestConversion("code-1", gh.AppCredentials{ID: 1, Slug: "s", PEM: "pem", WebhookSecret: "w"})
	f := e.open(t, "/setup")
	state := f.action.Query().Get("state")

	cases := map[string]*http.Request{
		"wrong state":  withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-1&state=nope", nil), f.cookie),
		"no cookie":    newRequest(t, http.MethodGet, "/setup/callback?code=code-1&state="+url.QueryEscape(state), nil),
		"oauth cookie": withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-1&state="+url.QueryEscape(state), nil), &http.Cookie{Name: setupCookie, Value: e.srv.seal(oauthPurpose, sealed{Value: state, Expires: time.Now().Add(time.Hour).Unix()})}),
		"no code":      withCookie(newRequest(t, http.MethodGet, "/setup/callback?state="+url.QueryEscape(state), nil), f.cookie),
	}
	for name, r := range cases {
		rec := e.do(r)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), "GITHUB_APP_ID=", name)
	}

	e.gh.FailNext("POST /app-manifests/{code}/conversions", http.StatusBadGateway, 1)
	rec := e.do(withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-1&state="+url.QueryEscape(state), nil), f.cookie))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "Reload this page")
	assert.Nil(t, cookieNamed(rec.Result(), setupCookie), "the state survives a GitHub outage")

	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-1&state="+url.QueryEscape(state), nil), f.cookie))
	require.Equal(t, http.StatusOK, rec.Code, "reloading converts the same code")
	assert.Contains(t, rec.Body.String(), "GITHUB_APP_ID=1")
	assert.True(t, clearedCookie(t, rec.Result(), setupCookie))
}

func TestSetupCallbackOnGHES(t *testing.T) {
	e := newSetupEnv(t, func(c *Config, _ *Deps) {
		c.GitHubWebURL = "https://ghe.example.com"
		c.OIDCAudience = "https://stackorder.internal"
	})
	e.gh.SetManifestConversion("code-2", gh.AppCredentials{ID: 7, Slug: "so", PEM: "pem", WebhookSecret: "w", ClientID: "c", ClientSecret: "s"})
	f := e.open(t, "/setup")
	rec := e.do(withCookie(newRequest(t, http.MethodGet, "/setup/callback?code=code-2&state="+url.QueryEscape(f.action.Query().Get("state")), nil), f.cookie))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := html.UnescapeString(rec.Body.String())
	assert.Contains(t, body, "GITHUB_API_URL="+e.gh.URL())
	assert.Contains(t, body, "GITHUB_WEB_URL=https://ghe.example.com")
	assert.Contains(t, body, "https://ghe.example.com/apps/so/installations/new")
	assert.Contains(t, body, "audience https://stackorder.internal")
}

func TestSetupInstalled(t *testing.T) {
	e := newSetupEnv(t)
	rec := e.do(newRequest(t, http.MethodGet, "/setup/installed?installation_id=4242&setup_action=install", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "installation 4242")
	assert.Contains(t, rec.Body.String(), "still in setup mode")

	configured := newSetupEnv(t, func(c *Config, _ *Deps) { c.SetupMode = false })
	rec = configured.do(newRequest(t, http.MethodGet, "/setup/installed?installation_id=<b>", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "<b>")
	assert.Contains(t, rec.Body.String(), `href="/auth/login"`)
}

func TestSetupModeRoutes(t *testing.T) {
	e := newSetupEnv(t)
	for _, target := range []string{"/setup", "/setup/installed"} {
		assert.Equal(t, http.StatusOK, e.do(newRequest(t, http.MethodGet, target, nil)).Code, target)
	}
	assert.Equal(t, http.StatusBadRequest, e.do(newRequest(t, http.MethodGet, "/setup/callback", nil)).Code)
	rec := e.do(newRequest(t, http.MethodGet, "/v1/overview", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, http.StatusInternalServerError, e.do(newRequest(t, http.MethodGet, "/readyz", nil)).Code,
		"any call on the untouched store fails, so the setup pages above provably never touch the store")
}
