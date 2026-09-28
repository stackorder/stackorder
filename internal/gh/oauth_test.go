package gh_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

func TestExchangeOAuthCode(t *testing.T) {
	fake := ghfake.New(t)
	fake.SetOAuthClient("Iv1.client", "s3cret")
	fake.AddOAuthCode("good-code", "alice")
	cfg := gh.OAuthConfig{ClientID: "Iv1.client", ClientSecret: "s3cret", BaseWebURL: fake.URL() + "/", HTTPClient: fake.HTTPClient(), RedirectURL: "https://stackorder.example.com/auth/callback"}
	ctx := context.Background()

	tok, err := gh.ExchangeOAuthCode(ctx, cfg, "good-code")
	require.NoError(t, err)
	assert.Regexp(t, `^gho_fake_\d+$`, tok)

	c, err := gh.NewTokenClient(gh.Config{BaseURL: fake.URL(), HTTPClient: fake.HTTPClient()}, tok)
	require.NoError(t, err)
	u, err := c.AuthenticatedUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "alice", u.Login)

	_, err = gh.ExchangeOAuthCode(ctx, cfg, "good-code")
	var oerr *gh.OAuthError
	require.ErrorAs(t, err, &oerr)
	assert.Equal(t, "bad_verification_code", oerr.Code)
	assert.Contains(t, oerr.Error(), "incorrect or expired")

	fake.AddOAuthCode("another", "alice")
	bad := cfg
	bad.ClientSecret = "wrong"
	_, err = gh.ExchangeOAuthCode(ctx, bad, "another")
	require.ErrorAs(t, err, &oerr)
	assert.Equal(t, "incorrect_client_credentials", oerr.Code)

	var form url.Values
	for _, r := range fake.Requests() {
		if r.Pattern == "POST /login/oauth/access_token" {
			form, err = url.ParseQuery(string(r.Body))
			require.NoError(t, err)
			assert.Equal(t, "application/json", r.Header.Get("Accept"))
		}
	}
	assert.Equal(t, "another", form.Get("code"))
	assert.Equal(t, "https://stackorder.example.com/auth/callback", form.Get("redirect_uri"))
}

func TestExchangeOAuthCodeValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	_, err := gh.ExchangeOAuthCode(ctx, gh.OAuthConfig{}, "c")
	require.Error(t, err)
	_, err = gh.ExchangeOAuthCode(ctx, gh.OAuthConfig{ClientID: "a", ClientSecret: "b"}, "")
	require.Error(t, err)
	_, err = gh.ExchangeOAuthCode(ctx, gh.OAuthConfig{ClientID: "a", ClientSecret: "b", BaseWebURL: "http://%zz"}, "c")
	require.Error(t, err)

	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"not json", 200, "<html>", "decode response"},
		{"no token", 200, `{}`, "without an access token"},
		{"server error", 500, `{}`, "status 500"},
		{"bare error code", 200, `{"error":"access_denied"}`, "gh: oauth: access_denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			_, err := gh.ExchangeOAuthCode(ctx, gh.OAuthConfig{ClientID: "a", ClientSecret: "b", BaseWebURL: srv.URL}, "c")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer srv.Close()
	_, err = gh.ExchangeOAuthCode(ctx, gh.OAuthConfig{ClientID: "a", ClientSecret: "b", BaseWebURL: srv.URL}, "c")
	require.Error(t, err)
}

func TestAuthorizeURL(t *testing.T) {
	cfg := gh.OAuthConfig{ClientID: "Iv1.client", RedirectURL: "https://stackorder.example.com/auth/callback"}
	u, err := url.Parse(cfg.AuthorizeURL("st4te"))
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/login/oauth/authorize", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, "Iv1.client", u.Query().Get("client_id"))
	assert.Equal(t, "st4te", u.Query().Get("state"))
	assert.Equal(t, "https://stackorder.example.com/auth/callback", u.Query().Get("redirect_uri"))

	ghes := gh.OAuthConfig{ClientID: "c", BaseWebURL: "https://ghe.corp.example/"}
	assert.Equal(t, "https://ghe.corp.example/login/oauth/authorize?client_id=c&state=s", ghes.AuthorizeURL("s"))
}
