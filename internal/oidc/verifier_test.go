package oidc_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
)

const (
	audience = "https://stackorder.example.com"
	sha      = "0123456789abcdef0123456789abcdef01234567"
)

func newVerifier(t *testing.T, iss *oidcfake.Issuer, mutate ...func(*oidc.Config)) *oidc.Verifier {
	t.Helper()
	cfg := iss.Config(audience)
	for _, m := range mutate {
		m(&cfg)
	}
	v, err := oidc.New(cfg)
	require.NoError(t, err)
	return v
}

func at(iss *oidcfake.Issuer, d time.Duration) *jwt.NumericDate {
	return jwt.NewNumericDate(iss.Now().Add(d))
}

func TestVerifyAcceptsGitHubToken(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	want := iss.PlanClaims("acme/infra", "42", 7, sha)
	want.ID = "jti-1"

	got, err := v.Verify(t.Context(), iss.Token(want))
	require.NoError(t, err)
	assert.Equal(t, iss.URL(), got.Issuer)
	assert.Equal(t, jwt.ClaimStrings{audience}, got.Audience)
	assert.Equal(t, "jti-1", got.ID)
	assert.Equal(t, "repo:acme/infra:pull_request", got.Subject)
	assert.True(t, got.IssuedAt.Equal(iss.Now()))
	got.RegisteredClaims = jwt.RegisteredClaims{}
	want.RegisteredClaims = jwt.RegisteredClaims{}
	assert.Equal(t, want, *got)

	n, ok := got.PullRequestNumber()
	assert.True(t, ok)
	assert.Equal(t, 7, n)
}

func TestVerifyAccepts(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	tests := []struct {
		name   string
		mutate func(*oidc.Claims)
		opts   []oidcfake.TokenOption
	}{
		{name: "audience array containing ours", mutate: func(c *oidc.Claims) { c.Audience = jwt.ClaimStrings{"https://github.com/acme", audience} }},
		{name: "exp passed within skew", mutate: func(c *oidc.Claims) { c.IssuedAt, c.ExpiresAt = at(iss, -5*time.Minute), at(iss, -59*time.Second) }},
		{name: "nbf ahead within skew", mutate: func(c *oidc.Claims) { c.NotBefore = at(iss, oidc.ClockSkew) }},
		{name: "iat ahead within skew", mutate: func(c *oidc.Claims) {
			c.IssuedAt, c.NotBefore, c.ExpiresAt = at(iss, oidc.ClockSkew), at(iss, 0), at(iss, 5*time.Minute)
		}},
		{name: "exactly max age old", mutate: func(c *oidc.Claims) { c.IssuedAt, c.ExpiresAt = at(iss, -oidc.DefaultMaxAge), at(iss, time.Minute) }},
		{name: "no nbf", opts: []oidcfake.TokenOption{oidcfake.WithoutClaim("nbf")}},
		{name: "no jti", opts: []oidcfake.TokenOption{oidcfake.WithoutClaim("jti")}},
		{name: "unknown claims", opts: []oidcfake.TokenOption{oidcfake.WithClaim("ref_protected", "true"), oidcfake.WithClaim("check_run_id", "9")}},
		{name: "typ header", opts: []oidcfake.TokenOption{oidcfake.WithHeader("typ", "JWT")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := iss.PlanClaims("acme/infra", "42", 7, sha)
			if tt.mutate != nil {
				tt.mutate(&c)
			}
			_, err := v.Verify(t.Context(), iss.Token(c, tt.opts...))
			require.NoError(t, err)
		})
	}
}

func TestVerifyRejects(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	plan := func(mutate func(*oidc.Claims), opts ...oidcfake.TokenOption) func() string {
		return func() string {
			c := iss.PlanClaims("acme/infra", "42", 7, sha)
			if mutate != nil {
				mutate(&c)
			}
			return iss.Token(c, opts...)
		}
	}
	splice := func(header, payload, signature string) string {
		return strings.Join([]string{
			strings.Split(header, ".")[0], strings.Split(payload, ".")[1], strings.Split(signature, ".")[2],
		}, ".")
	}
	tests := []struct {
		name  string
		token func() string
		want  error
	}{
		{name: "empty", token: func() string { return "" }, want: oidc.ErrMalformed},
		{name: "one segment", token: func() string { return "eyJhbGciOiJSUzI1NiJ9" }, want: oidc.ErrMalformed},
		{name: "undecodable segments", token: func() string { return "a.b.c" }, want: oidc.ErrMalformed},
		{name: "oversized", token: func() string { return strings.Repeat("a", 70<<10) }, want: oidc.ErrMalformed},
		{name: "numeric run_id", token: plan(nil, oidcfake.WithClaim("run_id", 1001)), want: oidc.ErrMalformed},
		{name: "numeric aud", token: plan(nil, oidcfake.WithClaim("aud", 7)), want: oidc.ErrMalformed},
		{name: "string exp", token: plan(nil, oidcfake.WithClaim("exp", "soon")), want: oidc.ErrMalformed},
		{name: "missing exp", token: plan(nil, oidcfake.WithoutClaim("exp")), want: oidc.ErrMalformed},
		{name: "missing iat", token: plan(nil, oidcfake.WithoutClaim("iat")), want: oidc.ErrMalformed},
		{name: "alg none", token: plan(nil, oidcfake.WithAlg("none")), want: oidc.ErrInvalidSignature},
		{name: "HS256 keyed with the public key", token: plan(nil, oidcfake.WithAlg("HS256")), want: oidc.ErrInvalidSignature},
		{name: "HS512", token: plan(nil, oidcfake.WithAlg("HS512")), want: oidc.ErrInvalidSignature},
		{name: "RS384", token: plan(nil, oidcfake.WithAlg("RS384")), want: oidc.ErrInvalidSignature},
		{name: "PS256", token: plan(nil, oidcfake.WithAlg("PS256")), want: oidc.ErrInvalidSignature},
		{name: "ES256", token: plan(nil, oidcfake.WithAlg("ES256")), want: oidc.ErrInvalidSignature},
		{name: "unregistered alg", token: plan(nil, oidcfake.WithHeader("alg", "XY512")), want: oidc.ErrInvalidSignature},
		{name: "missing alg", token: plan(nil, oidcfake.WithHeader("alg", nil)), want: oidc.ErrInvalidSignature},
		{name: "foreign signing key", token: plan(nil, oidcfake.WithSigningKey(oidcfake.GenerateKey(t))), want: oidc.ErrInvalidSignature},
		{name: "tampered payload", token: func() string {
			good := plan(nil)()
			forged := plan(func(c *oidc.Claims) { c.SHA = strings.Repeat("f", 40) })()
			return splice(good, forged, good)
		}, want: oidc.ErrInvalidSignature},
		{name: "tampered signature", token: func() string { return splice(plan(nil)(), plan(nil)(), plan(nil)()) }, want: oidc.ErrInvalidSignature},
		{name: "unknown kid", token: plan(nil, oidcfake.WithKID("kid-404")), want: oidc.ErrUnknownKey},
		{name: "no kid", token: plan(nil, oidcfake.WithKID("")), want: oidc.ErrUnknownKey},
		{name: "numeric kid", token: plan(nil, oidcfake.WithHeader("kid", 1)), want: oidc.ErrUnknownKey},
		{name: "github.com issuer", token: plan(func(c *oidc.Claims) { c.Issuer = oidc.DefaultIssuer }), want: oidc.ErrIssuer},
		{name: "issuer with trailing slash", token: plan(func(c *oidc.Claims) { c.Issuer = iss.URL() + "/" }), want: oidc.ErrIssuer},
		{name: "other audience", token: plan(func(c *oidc.Claims) { c.Audience = jwt.ClaimStrings{"https://github.com/acme"} }), want: oidc.ErrAudience},
		{name: "audience array without ours", token: plan(func(c *oidc.Claims) { c.Audience = jwt.ClaimStrings{"a", "b"} }), want: oidc.ErrAudience},
		{name: "audience prefix", token: plan(func(c *oidc.Claims) { c.Audience = jwt.ClaimStrings{audience + ".evil"} }), want: oidc.ErrAudience},
		{name: "no audience", token: plan(nil, oidcfake.WithoutClaim("aud")), want: oidc.ErrAudience},
		{name: "expired", token: plan(func(c *oidc.Claims) { c.IssuedAt, c.ExpiresAt = at(iss, -6*time.Minute), at(iss, -61*time.Second) }), want: oidc.ErrExpired},
		{name: "expired at the skew boundary", token: plan(func(c *oidc.Claims) { c.IssuedAt, c.ExpiresAt = at(iss, -5*time.Minute), at(iss, -oidc.ClockSkew) }), want: oidc.ErrExpired},
		{name: "nbf in the future", token: plan(func(c *oidc.Claims) { c.NotBefore = at(iss, oidc.ClockSkew+time.Second) }), want: oidc.ErrNotYetValid},
		{name: "iat in the future", token: plan(func(c *oidc.Claims) {
			c.IssuedAt, c.NotBefore, c.ExpiresAt = at(iss, oidc.ClockSkew+time.Second), at(iss, 0), at(iss, 6*time.Minute)
		}), want: oidc.ErrNotYetValid},
		{name: "older than max age", token: plan(func(c *oidc.Claims) {
			c.IssuedAt, c.ExpiresAt = at(iss, -oidc.DefaultMaxAge-time.Second), at(iss, time.Minute)
		}), want: oidc.ErrTooOld},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Verify(t.Context(), tt.token())
			require.ErrorIs(t, err, tt.want)
			assert.ErrorIs(t, err, oidc.ErrInvalidToken)
			assert.NotErrorIs(t, err, oidc.ErrJWKSUnavailable)
			assert.Nil(t, got)
		})
	}
}

func TestVerifyUsesConfiguredMaxAge(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss, func(c *oidc.Config) { c.MaxAge = time.Minute })
	c := iss.PlanClaims("acme/infra", "42", 7, sha)
	c.IssuedAt = at(iss, -2*time.Minute)
	_, err := v.Verify(t.Context(), iss.Token(c))
	require.ErrorIs(t, err, oidc.ErrTooOld)
	assert.ErrorContains(t, err, "issued 2m0s ago, limit 1m0s")
}

func TestVerifyDerivesJWKSURLFromIssuer(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss, func(c *oidc.Config) { c.JWKSURL = "" })
	_, err := v.Verify(t.Context(), iss.Token(iss.PlanClaims("acme/infra", "42", 7, sha)))
	require.NoError(t, err)
	assert.Equal(t, 1, iss.JWKSRequests())
}

func TestVerifyPicksUpRotatedKey(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	claims := iss.PlanClaims("acme/infra", "42", 7, sha)
	oldToken := iss.Token(claims)
	_, err := v.Verify(t.Context(), oldToken)
	require.NoError(t, err)
	require.Equal(t, 1, iss.JWKSRequests())

	iss.Advance(oidc.DefaultMinRefresh)
	kid := iss.Rotate()
	got, err := v.Verify(t.Context(), iss.Token(claims))
	require.NoError(t, err, "an unknown kid triggers a refresh")
	assert.Equal(t, "acme/infra", got.Repository)
	assert.Equal(t, 2, iss.JWKSRequests())
	assert.Equal(t, "kid-2", kid)

	_, err = v.Verify(t.Context(), oldToken)
	require.NoError(t, err, "the previous key stays valid while published")
	_, err = v.Verify(t.Context(), iss.Token(claims))
	require.NoError(t, err)
	assert.Equal(t, 2, iss.JWKSRequests(), "known kids are served from the cache")
}

func TestVerifyThrottlesRefresh(t *testing.T) {
	tests := []struct {
		name       string
		minRefresh time.Duration
	}{
		{name: "default", minRefresh: 0},
		{name: "configured", minRefresh: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := oidcfake.New(t)
			v := newVerifier(t, iss, func(c *oidc.Config) { c.MinRefresh = tt.minRefresh })
			interval := tt.minRefresh
			if interval == 0 {
				interval = oidc.DefaultMinRefresh
			}
			claims := iss.PlanClaims("acme/infra", "42", 7, sha)
			unknown := func() error {
				_, err := v.Verify(t.Context(), iss.Token(claims, oidcfake.WithKID("kid-"+rand.Text())))
				return err
			}

			_, err := v.Verify(t.Context(), iss.Token(claims))
			require.NoError(t, err)
			require.Equal(t, 1, iss.JWKSRequests())

			for range 20 {
				require.ErrorIs(t, unknown(), oidc.ErrUnknownKey)
			}
			assert.Equal(t, 1, iss.JWKSRequests(), "no refresh within MinRefresh of the last fetch")

			iss.Advance(interval - time.Second)
			require.ErrorIs(t, unknown(), oidc.ErrUnknownKey)
			assert.Equal(t, 1, iss.JWKSRequests())

			iss.Advance(time.Second)
			require.ErrorIs(t, unknown(), oidc.ErrUnknownKey)
			assert.Equal(t, 2, iss.JWKSRequests(), "one refresh once MinRefresh has elapsed")
			for range 20 {
				require.ErrorIs(t, unknown(), oidc.ErrUnknownKey)
			}
			assert.Equal(t, 2, iss.JWKSRequests())

			iss.Rotate()
			_, err = v.Verify(t.Context(), iss.Token(claims))
			require.ErrorIs(t, err, oidc.ErrUnknownKey, "a rotation inside the window waits for it to end")
			iss.Advance(interval)
			_, err = v.Verify(t.Context(), iss.Token(claims))
			require.NoError(t, err)
			assert.Equal(t, 3, iss.JWKSRequests())
		})
	}
}

func TestVerifyRefreshesAfterCacheTTL(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss, func(c *oidc.Config) { c.CacheTTL = 30 * time.Minute })
	claims := iss.PlanClaims("acme/infra", "42", 7, sha)
	verify := func(opts ...oidcfake.TokenOption) error {
		_, err := v.Verify(t.Context(), iss.Token(claims, opts...))
		return err
	}

	require.NoError(t, verify())
	iss.Advance(30*time.Minute - time.Second)
	require.NoError(t, verify())
	assert.Equal(t, 1, iss.JWKSRequests())

	iss.Advance(time.Second)
	require.NoError(t, verify())
	assert.Equal(t, 2, iss.JWKSRequests(), "a cache older than CacheTTL is refreshed")

	iss.Rotate()
	iss.Retire("kid-1")
	iss.Advance(30 * time.Minute)
	require.ErrorIs(t, verify(oidcfake.WithKID("kid-1")), oidc.ErrUnknownKey, "a retired key is dropped on refresh")
	assert.Equal(t, 3, iss.JWKSRequests())
	require.NoError(t, verify())
	assert.Equal(t, 3, iss.JWKSRequests())
}

func TestVerifyKeepsStaleKeysWhenRefreshFails(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	claims := iss.PlanClaims("acme/infra", "42", 7, sha)
	verify := func(opts ...oidcfake.TokenOption) error {
		_, err := v.Verify(t.Context(), iss.Token(claims, opts...))
		return err
	}

	require.NoError(t, verify())
	iss.Advance(oidc.DefaultCacheTTL)
	iss.FailJWKS(http.StatusInternalServerError)
	require.NoError(t, verify(), "a stale key is used when the refresh fails")
	assert.Equal(t, 2, iss.JWKSRequests())

	err := verify(oidcfake.WithKID("kid-9"))
	require.ErrorIs(t, err, oidc.ErrJWKSUnavailable, "throttled right after the failed attempt, an unknown kid still cannot be judged")
	assert.NotErrorIs(t, err, oidc.ErrInvalidToken)
	assert.ErrorContains(t, err, "status 500")
	assert.Equal(t, 2, iss.JWKSRequests())

	iss.Advance(oidc.DefaultMinRefresh)
	err = verify(oidcfake.WithKID("kid-9"))
	require.ErrorIs(t, err, oidc.ErrJWKSUnavailable, "an unknown kid cannot be judged while the JWKS is down")
	assert.NotErrorIs(t, err, oidc.ErrInvalidToken)
	assert.ErrorContains(t, err, "status 500")
	assert.Equal(t, 3, iss.JWKSRequests())
	require.NoError(t, verify(), "the stale key still verifies while throttled")
	assert.Equal(t, 3, iss.JWKSRequests())

	iss.FailJWKS(0)
	iss.Advance(oidc.DefaultMinRefresh)
	require.NoError(t, verify())
	assert.Equal(t, 4, iss.JWKSRequests())
	require.NoError(t, verify())
	assert.Equal(t, 4, iss.JWKSRequests(), "the cache is fresh again")
}

func TestVerifyWithoutAnyKeySet(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	token := iss.Token(iss.PlanClaims("acme/infra", "42", 7, sha))
	iss.FailJWKS(http.StatusServiceUnavailable)

	for range 3 {
		_, err := v.Verify(t.Context(), token)
		require.ErrorIs(t, err, oidc.ErrJWKSUnavailable)
		assert.NotErrorIs(t, err, oidc.ErrInvalidToken)
		assert.ErrorContains(t, err, "status 503")
	}
	assert.Equal(t, 1, iss.JWKSRequests(), "failed fetches are throttled too")

	iss.FailJWKS(0)
	iss.Advance(oidc.DefaultMinRefresh)
	_, err := v.Verify(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, 2, iss.JWKSRequests())
}

func TestVerifyJWKSFetchFailures(t *testing.T) {
	serve := func(status int, body string) func(t *testing.T) string {
		return func(t *testing.T) string {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)
			return srv.URL + "/jwks"
		}
	}
	tests := []struct {
		name string
		url  func(t *testing.T) string
		want string
	}{
		{name: "not found", url: serve(http.StatusNotFound, "{}"), want: "status 404"},
		{name: "not JSON", url: serve(http.StatusOK, "<html></html>"), want: "decoding key set"},
		{name: "no usable key", url: serve(http.StatusOK, `{"keys":[{"kty":"EC","kid":"a"}]}`), want: "no usable RS256 signing key"},
		{name: "oversized", url: serve(http.StatusOK, `{"keys":[]}`+strings.Repeat(" ", 1<<20)), want: "exceeds 1048576 bytes"},
		{name: "connection refused", url: func(t *testing.T) string {
			srv := httptest.NewServer(http.NotFoundHandler())
			srv.Close()
			return srv.URL + "/jwks"
		}, want: "connect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := oidcfake.New(t)
			v := newVerifier(t, iss, func(c *oidc.Config) {
				c.JWKSURL = tt.url(t)
				c.HTTPClient = http.DefaultClient
			})
			_, err := v.Verify(t.Context(), iss.Token(iss.PlanClaims("acme/infra", "42", 7, sha)))
			require.ErrorIs(t, err, oidc.ErrJWKSUnavailable)
			assert.NotErrorIs(t, err, oidc.ErrInvalidToken)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestVerifyFetchIgnoresCallerCancellation(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := v.Verify(ctx, iss.Token(iss.PlanClaims("acme/infra", "42", 7, sha)))
	require.NoError(t, err)
	assert.Equal(t, 1, iss.JWKSRequests())
}

func TestVerifyConcurrentFirstUse(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	tokens := make([]string, 32)
	for i := range tokens {
		tokens[i] = iss.Token(iss.PlanClaims("acme/infra", "42", i+1, sha))
	}
	var wg sync.WaitGroup
	for i, tok := range tokens {
		wg.Go(func() {
			c, err := v.Verify(t.Context(), tok)
			if assert.NoError(t, err) {
				n, _ := c.PullRequestNumber()
				assert.Equal(t, i+1, n)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, iss.JWKSRequests(), "concurrent callers share one fetch")
}
