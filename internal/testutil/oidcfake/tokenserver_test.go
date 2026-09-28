package oidcfake_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
)

type tokenResponse struct {
	Value   string `json:"value"`
	Message string `json:"message"`
}

func requestToken(t *testing.T, ts *oidcfake.TokenServer, method, rawURL, auth string) (int, tokenResponse) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, http.NoBody)
	require.NoError(t, err)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	var body tokenResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return resp.StatusCode, body
}

func TestTokenServerIssuesVerifiableTokens(t *testing.T) {
	iss := oidcfake.New(t)
	ts := iss.TokenRequestServer(t)
	const aud = "https://stackorder.example.com"
	v, err := oidc.New(iss.Config(aud))
	require.NoError(t, err)

	status, body := requestToken(t, ts, http.MethodGet, ts.URL()+"&audience="+url.QueryEscape(aud), "Bearer "+ts.RequestToken())
	require.Equal(t, http.StatusOK, status, body.Message)
	c, err := v.Verify(t.Context(), body.Value)
	require.NoError(t, err)
	assert.Equal(t, oidcfake.DefaultRepository, c.Repository)
	assert.Equal(t, oidcfake.DefaultRepositoryID, c.RepositoryID)
	assert.Equal(t, oidcfake.DefaultSHA, c.SHA)
	assert.Equal(t, "refs/pull/1/merge", c.Ref)
	require.NoError(t, oidc.BindPlan(c, oidc.PlanBinding{
		Repository: oidcfake.DefaultRepository, RepositoryID: oidcfake.DefaultRepositoryID, SHA: oidcfake.DefaultSHA, PRNumber: oidcfake.DefaultPR,
	}))

	_, again := requestToken(t, ts, http.MethodGet, ts.URL()+"&audience="+url.QueryEscape(aud), "bearer "+ts.RequestToken())
	c2, err := v.Verify(t.Context(), again.Value)
	require.NoError(t, err)
	assert.NotEqual(t, c.ID, c2.ID, "every request mints a new jti")

	status, body = requestToken(t, ts, http.MethodGet, ts.URL(), "Bearer "+ts.RequestToken())
	require.Equal(t, http.StatusOK, status)
	var payload map[string]any
	decodeSegment(t, body.Value, 1, &payload)
	assert.Equal(t, "https://github.com/acme", payload["aud"], "GitHub's default audience is the owner URL")

	assert.Equal(t, []string{aud, aud, "https://github.com/acme"}, ts.Audiences())
}

func TestTokenServerSetClaims(t *testing.T) {
	iss := oidcfake.New(t)
	ts := iss.TokenRequestServer(t)
	c := iss.DispatchClaims("acme/infra", "42", 4242, "production", "main", sha)
	ts.SetClaims(c, oidcfake.WithKID("kid-custom"))
	assert.Equal(t, c, ts.Claims())

	status, body := requestToken(t, ts, http.MethodGet, ts.URL()+"&audience=x", "Bearer "+ts.RequestToken())
	require.Equal(t, http.StatusOK, status)
	var header, payload map[string]any
	decodeSegment(t, body.Value, 0, &header)
	decodeSegment(t, body.Value, 1, &payload)
	assert.Equal(t, "kid-custom", header["kid"])
	assert.Equal(t, "4242", payload["run_id"])
	assert.Equal(t, "production", payload["environment"])
	assert.Equal(t, "x", payload["aud"])
}

func TestTokenServerRejects(t *testing.T) {
	iss := oidcfake.New(t)
	ts := iss.TokenRequestServer(t)
	base, _, _ := strings.Cut(ts.URL(), "?")
	good := "Bearer " + ts.RequestToken()
	tests := []struct {
		name       string
		method     string
		url        string
		auth       string
		fail       int
		claimsOpts []oidcfake.TokenOption
		want       int
	}{
		{name: "query replaced instead of appended", method: http.MethodGet, url: base + "?audience=x", auth: good, want: http.StatusBadRequest},
		{name: "wrong path", method: http.MethodGet, url: strings.TrimSuffix(base, "/idtoken") + "/other?api-version=2.0", auth: good, want: http.StatusNotFound},
		{name: "post", method: http.MethodPost, url: ts.URL(), auth: good, want: http.StatusMethodNotAllowed},
		{name: "no request token", method: http.MethodGet, url: ts.URL(), want: http.StatusUnauthorized},
		{name: "wrong request token", method: http.MethodGet, url: ts.URL(), auth: "Bearer nope", want: http.StatusUnauthorized},
		{name: "forced failure", method: http.MethodGet, url: ts.URL(), auth: good, fail: http.StatusServiceUnavailable, want: http.StatusServiceUnavailable},
		{name: "unsignable claims", method: http.MethodGet, url: ts.URL(), auth: good, claimsOpts: []oidcfake.TokenOption{oidcfake.WithAlg("bogus")}, want: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts.Fail(tt.fail)
			ts.SetClaims(ts.Claims(), tt.claimsOpts...)
			t.Cleanup(func() {
				ts.Fail(0)
				ts.SetClaims(ts.Claims())
			})
			status, body := requestToken(t, ts, tt.method, tt.url, tt.auth)
			assert.Equal(t, tt.want, status)
			assert.Empty(t, body.Value)
			assert.NotEmpty(t, body.Message)
		})
	}
	assert.Empty(t, ts.Audiences())

	status, _ := requestToken(t, ts, http.MethodGet, ts.URL(), good)
	assert.Equal(t, http.StatusOK, status, "Fail(0) restores normal operation")
}

func TestTokenServerEnv(t *testing.T) {
	iss := oidcfake.New(t)
	ts := iss.TokenRequestServer(t)
	env := ts.Env()
	assert.Equal(t, ts.URL(), env["ACTIONS_ID_TOKEN_REQUEST_URL"])
	assert.Equal(t, ts.RequestToken(), env["ACTIONS_ID_TOKEN_REQUEST_TOKEN"])
	assert.Equal(t, "true", env["GITHUB_ACTIONS"])
	assert.Equal(t, oidcfake.DefaultRepository, env["GITHUB_REPOSITORY"])
	assert.Equal(t, oidcfake.DefaultRepositoryID, env["GITHUB_REPOSITORY_ID"])
	assert.Equal(t, "acme", env["GITHUB_REPOSITORY_OWNER"])
	assert.Equal(t, oidcfake.DefaultSHA, env["GITHUB_SHA"])
	assert.Equal(t, "refs/pull/1/merge", env["GITHUB_REF"])
	assert.Equal(t, "1/merge", env["GITHUB_REF_NAME"])
	assert.Equal(t, "pull_request", env["GITHUB_EVENT_NAME"])
	assert.Equal(t, "main", env["GITHUB_BASE_REF"])
	assert.Equal(t, "feature/pr-1", env["GITHUB_HEAD_REF"])
	assert.Equal(t, ts.Claims().RunID, env["GITHUB_RUN_ID"])
	assert.Equal(t, "1", env["GITHUB_RUN_ATTEMPT"])
	assert.Equal(t, oidcfake.Actor, env["GITHUB_ACTOR"])
	assert.Equal(t, "github-hosted", env["RUNNER_ENVIRONMENT"])

	ts.SetClaims(iss.DispatchClaims("acme/infra", "42", 99, "production", "main", sha))
	env = ts.Env()
	assert.Equal(t, "99", env["GITHUB_RUN_ID"])
	assert.Equal(t, "main", env["GITHUB_REF_NAME"])
	assert.Equal(t, "workflow_dispatch", env["GITHUB_EVENT_NAME"])
	assert.Empty(t, env["GITHUB_HEAD_REF"])
}

func TestTokenServerSetenv(t *testing.T) {
	iss := oidcfake.New(t)
	ts := iss.TokenRequestServer(t)
	ts.Setenv(t)
	for k, v := range ts.Env() {
		assert.Equal(t, v, os.Getenv(k), k)
	}
}
