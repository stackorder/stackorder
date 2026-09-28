package oidcfake_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
)

const sha = "89abcdef0123456789abcdef0123456789abcdef"

func getJSON(t *testing.T, client *http.Client, url string, v any) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	if v != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(v))
	}
	return resp.StatusCode
}

func decodeSegment(t *testing.T, raw string, i int, v any) {
	t.Helper()
	parts := strings.Split(raw, ".")
	require.Len(t, parts, 3)
	data, err := base64.RawURLEncoding.DecodeString(parts[i])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, v))
}

func TestDiscoveryDocument(t *testing.T) {
	iss := oidcfake.New(t)
	var doc map[string]any
	status := getJSON(t, http.DefaultClient, iss.URL()+oidcfake.DiscoveryPath, &doc)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, iss.URL(), doc["issuer"])
	assert.Equal(t, iss.JWKSURL(), doc["jwks_uri"])
	assert.Equal(t, []any{"RS256"}, doc["id_token_signing_alg_values_supported"])
	assert.Contains(t, doc["claims_supported"], "job_workflow_ref")
}

func TestJWKSPublishesRotatedAndRetiredKeys(t *testing.T) {
	iss := oidcfake.New(t)
	kids := func() []string {
		var set struct {
			Keys []struct {
				Kty, Alg, Use, Kid, N, E string
			} `json:"keys"`
		}
		require.Equal(t, http.StatusOK, getJSON(t, http.DefaultClient, iss.JWKSURL(), &set))
		out := make([]string, 0, len(set.Keys))
		for _, k := range set.Keys {
			assert.Equal(t, "RSA", k.Kty)
			assert.Equal(t, "RS256", k.Alg)
			assert.Equal(t, "sig", k.Use)
			assert.NotEmpty(t, k.N)
			assert.Equal(t, "AQAB", k.E)
			out = append(out, k.Kid)
		}
		return out
	}

	assert.Equal(t, "kid-1", iss.KID())
	assert.Equal(t, []string{"kid-1"}, kids())

	assert.Equal(t, "kid-2", iss.Rotate())
	assert.Equal(t, "kid-2", iss.KID())
	assert.Equal(t, []string{"kid-1", "kid-2"}, kids())

	iss.Retire("kid-1")
	assert.Equal(t, []string{"kid-2"}, kids())
	assert.Equal(t, 3, iss.JWKSRequests())

	iss.FailJWKS(http.StatusServiceUnavailable)
	assert.Equal(t, http.StatusServiceUnavailable, getJSON(t, http.DefaultClient, iss.JWKSURL(), nil))
	iss.FailJWKS(0)
	assert.Equal(t, []string{"kid-2"}, kids())
	assert.Equal(t, 5, iss.JWKSRequests())
}

func TestClock(t *testing.T) {
	iss := oidcfake.New(t)
	assert.Equal(t, oidcfake.Epoch, iss.Now())
	iss.Advance(90 * time.Second)
	assert.Equal(t, oidcfake.Epoch.Add(90*time.Second), iss.Now())
	assert.Equal(t, oidcfake.Epoch.Add(90*time.Second), iss.Config("aud").Clock())
}

func TestTokenDefaults(t *testing.T) {
	iss := oidcfake.New(t)
	raw := iss.Token(iss.PlanClaims("acme/infra", "42", 7, sha))

	var header map[string]any
	decodeSegment(t, raw, 0, &header)
	assert.Equal(t, "RS256", header["alg"])
	assert.Equal(t, "kid-1", header["kid"])

	var payload map[string]any
	decodeSegment(t, raw, 1, &payload)
	assert.Equal(t, iss.URL(), payload["iss"])
	assert.Equal(t, oidcfake.DefaultAudience, payload["aud"], "a single audience is encoded as a string")
	assert.InDelta(t, float64(oidcfake.Epoch.Unix()), payload["iat"], 0)
	assert.InDelta(t, float64(oidcfake.Epoch.Unix()), payload["nbf"], 0)
	assert.InDelta(t, float64(oidcfake.Epoch.Add(oidcfake.TokenLifetime).Unix()), payload["exp"], 0)
	assert.NotEmpty(t, payload["jti"])
	assert.Equal(t, "repo:acme/infra:pull_request", payload["sub"])
	assert.Equal(t, "refs/pull/7/merge", payload["ref"])
	assert.Equal(t, "pull_request", payload["event_name"])
	assert.Equal(t, sha, payload["sha"])
	assert.Equal(t, "42", payload["repository_id"])
	assert.Equal(t, "acme", payload["repository_owner"])
	assert.Equal(t, oidcfake.PlanWorkflowRef, payload["job_workflow_ref"])

	other := iss.Token(iss.PlanClaims("acme/infra", "42", 7, sha))
	var otherPayload map[string]any
	decodeSegment(t, other, 1, &otherPayload)
	assert.NotEqual(t, payload["jti"], otherPayload["jti"])
	assert.NotEqual(t, payload["run_id"], otherPayload["run_id"])
}

func TestTokenKeepsExplicitClaims(t *testing.T) {
	iss := oidcfake.New(t)
	c := iss.DispatchClaims("acme/infra", "42", 99, "production", "main", sha)
	c.Issuer = "https://elsewhere"
	c.Audience = jwt.ClaimStrings{"a", "b"}
	c.Subject = "custom"
	c.ID = "jti-1"
	c.IssuedAt = jwt.NewNumericDate(oidcfake.Epoch.Add(-time.Minute))
	c.NotBefore = jwt.NewNumericDate(oidcfake.Epoch.Add(-2 * time.Minute))
	c.ExpiresAt = jwt.NewNumericDate(oidcfake.Epoch.Add(time.Minute))
	raw := iss.Token(c, oidcfake.WithClaim("run_id", 99), oidcfake.WithoutClaim("sha"), oidcfake.WithHeader("typ", "JWT"))

	var header map[string]any
	decodeSegment(t, raw, 0, &header)
	assert.Equal(t, "JWT", header["typ"])

	var payload map[string]any
	decodeSegment(t, raw, 1, &payload)
	assert.Equal(t, "https://elsewhere", payload["iss"])
	assert.Equal(t, []any{"a", "b"}, payload["aud"])
	assert.Equal(t, "custom", payload["sub"])
	assert.Equal(t, "jti-1", payload["jti"])
	assert.InDelta(t, float64(oidcfake.Epoch.Add(-time.Minute).Unix()), payload["iat"], 0)
	assert.InDelta(t, float64(oidcfake.Epoch.Add(-2*time.Minute).Unix()), payload["nbf"], 0)
	assert.InDelta(t, float64(oidcfake.Epoch.Add(time.Minute).Unix()), payload["exp"], 0)
	assert.InDelta(t, 99, payload["run_id"], 0)
	assert.NotContains(t, payload, "sha")
	assert.Equal(t, "production", payload["environment"])
}

func TestTokenAudienceFollowsConfig(t *testing.T) {
	iss := oidcfake.New(t)
	iss.Config("https://stackorder.example.com")
	var payload map[string]any
	decodeSegment(t, iss.Token(oidc.Claims{}), 1, &payload)
	assert.Equal(t, "https://stackorder.example.com", payload["aud"])
}

func TestTokenOptionsSelectAlgorithmAndKey(t *testing.T) {
	iss := oidcfake.New(t)
	claims := iss.PlanClaims("acme/infra", "42", 7, sha)
	tests := []struct {
		name    string
		opts    []oidcfake.TokenOption
		wantAlg string
		wantKid any
	}{
		{name: "default", wantAlg: "RS256", wantKid: "kid-1"},
		{name: "none", opts: []oidcfake.TokenOption{oidcfake.WithAlg("none")}, wantAlg: "none", wantKid: "kid-1"},
		{name: "hs256", opts: []oidcfake.TokenOption{oidcfake.WithAlg("HS256")}, wantAlg: "HS256", wantKid: "kid-1"},
		{name: "rs384", opts: []oidcfake.TokenOption{oidcfake.WithAlg("RS384")}, wantAlg: "RS384", wantKid: "kid-1"},
		{name: "ps256", opts: []oidcfake.TokenOption{oidcfake.WithAlg("PS256")}, wantAlg: "PS256", wantKid: "kid-1"},
		{name: "es256", opts: []oidcfake.TokenOption{oidcfake.WithAlg("ES256")}, wantAlg: "ES256", wantKid: "kid-1"},
		{name: "es512", opts: []oidcfake.TokenOption{oidcfake.WithAlg("ES512")}, wantAlg: "ES512", wantKid: "kid-1"},
		{name: "custom kid", opts: []oidcfake.TokenOption{oidcfake.WithKID("other")}, wantAlg: "RS256", wantKid: "other"},
		{name: "no kid", opts: []oidcfake.TokenOption{oidcfake.WithKID("")}, wantAlg: "RS256", wantKid: nil},
		{name: "other key", opts: []oidcfake.TokenOption{oidcfake.WithSigningKey(oidcfake.GenerateKey(t))}, wantAlg: "RS256", wantKid: "kid-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := iss.Token(claims, tt.opts...)
			var header map[string]any
			decodeSegment(t, raw, 0, &header)
			assert.Equal(t, tt.wantAlg, header["alg"])
			assert.Equal(t, tt.wantKid, header["kid"])
		})
	}
}

func TestTokenPanicsOnUnsupportedAlg(t *testing.T) {
	iss := oidcfake.New(t)
	assert.PanicsWithError(t, `oidcfake: unsupported alg "XX1"`, func() {
		iss.Token(oidc.Claims{}, oidcfake.WithAlg("XX1"))
	})
}

func TestTokensVerify(t *testing.T) {
	iss := oidcfake.New(t)
	v, err := oidc.New(iss.Config("https://stackorder.test"))
	require.NoError(t, err)

	plan := iss.PlanClaims("acme/infra", "42", 7, sha)
	got, err := v.Verify(t.Context(), iss.Token(plan))
	require.NoError(t, err)
	require.NoError(t, oidc.BindPlan(got, oidc.PlanBinding{Repository: "acme/infra", RepositoryID: "42", SHA: sha, PRNumber: 7}))
	require.NoError(t, oidc.BindWorkflowRef(got, oidc.DefaultWorkflowRefPattern))

	dispatch := iss.DispatchClaims("acme/infra", "42", 555, "production", "main", sha)
	got, err = v.Verify(t.Context(), iss.Token(dispatch))
	require.NoError(t, err)
	require.NoError(t, oidc.BindDispatch(got, oidc.DispatchBinding{
		Repository: "acme/infra", RepositoryID: "42", RunID: 555, Environment: "production", DefaultBranch: "main", SHA: sha,
	}))
	require.NoError(t, oidc.BindWorkflowRef(got, oidc.DefaultWorkflowRefPattern))
	assert.Equal(t, "repo:acme/infra:environment:production", got.Subject)

	iss.Rotate()
	_, err = v.Verify(t.Context(), iss.Token(plan))
	require.ErrorIs(t, err, oidc.ErrUnknownKey, "rotation is picked up only after MinRefresh")
	iss.Advance(oidc.DefaultMinRefresh)
	_, err = v.Verify(t.Context(), iss.Token(plan))
	require.NoError(t, err)
}

func TestSubject(t *testing.T) {
	tests := []struct {
		name string
		c    oidc.Claims
		want string
	}{
		{name: "environment wins", c: oidc.Claims{Repository: "a/b", Environment: "prod", EventName: "pull_request"}, want: "repo:a/b:environment:prod"},
		{name: "pull request", c: oidc.Claims{Repository: "a/b", EventName: "pull_request", Ref: "refs/pull/1/merge"}, want: "repo:a/b:pull_request"},
		{name: "pull request target", c: oidc.Claims{Repository: "a/b", EventName: "pull_request_target"}, want: "repo:a/b:pull_request"},
		{name: "branch", c: oidc.Claims{Repository: "a/b", EventName: "push", Ref: "refs/heads/main"}, want: "repo:a/b:ref:refs/heads/main"},
		{name: "tag", c: oidc.Claims{Repository: "a/b", EventName: "push", Ref: "refs/tags/v1"}, want: "repo:a/b:ref:refs/tags/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, oidcfake.Subject(tt.c))
		})
	}
}
