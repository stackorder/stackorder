package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDefaults(t *testing.T) {
	v, err := New(Config{Audience: "https://stackorder.example.com"})
	require.NoError(t, err)
	assert.Equal(t, DefaultIssuer, v.cfg.Issuer)
	assert.Equal(t, "https://token.actions.githubusercontent.com/.well-known/jwks", v.cfg.JWKSURL)
	assert.Equal(t, DefaultMaxAge, v.cfg.MaxAge)
	assert.Equal(t, DefaultMinRefresh, v.cfg.MinRefresh)
	assert.Equal(t, DefaultCacheTTL, v.cfg.CacheTTL)
	require.NotNil(t, v.cfg.HTTPClient)
	assert.Equal(t, defaultHTTPTimeout, v.cfg.HTTPClient.Timeout)
	require.NotNil(t, v.cfg.Clock)
	assert.WithinDuration(t, time.Now(), v.cfg.Clock(), time.Minute)
}

func TestNewKeepsExplicitValues(t *testing.T) {
	client := &http.Client{}
	clock := func() time.Time { return time.Unix(0, 0) }
	v, err := New(Config{
		Issuer:     "https://ghes.example.com/_services/token/",
		Audience:   "aud",
		HTTPClient: client,
		Clock:      clock,
		MaxAge:     time.Minute,
		MinRefresh: time.Second,
		CacheTTL:   time.Minute,
	})
	require.NoError(t, err)
	assert.Equal(t, "https://ghes.example.com/_services/token/", v.cfg.Issuer)
	assert.Equal(t, "https://ghes.example.com/_services/token/.well-known/jwks", v.cfg.JWKSURL)
	assert.Same(t, client, v.cfg.HTTPClient)
	assert.Equal(t, time.Unix(0, 0), v.cfg.Clock())
	assert.Equal(t, time.Minute, v.cfg.MaxAge)
	assert.Equal(t, time.Second, v.cfg.MinRefresh)
	assert.Equal(t, time.Minute, v.cfg.CacheTTL)
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "no audience", cfg: Config{}, want: "audience is required"},
		{name: "unparsable JWKS URL", cfg: Config{Audience: "a", JWKSURL: "http://[::1"}, want: "JWKS URL"},
		{name: "relative JWKS URL", cfg: Config{Audience: "a", JWKSURL: "/.well-known/jwks"}, want: "not an absolute http(s) URL"},
		{name: "non http JWKS URL", cfg: Config{Audience: "a", JWKSURL: "file:///etc/jwks"}, want: "not an absolute http(s) URL"},
		{name: "negative max age", cfg: Config{Audience: "a", MaxAge: -time.Second}, want: "MaxAge must not be negative"},
		{name: "negative min refresh", cfg: Config{Audience: "a", MinRefresh: -time.Second}, want: "MinRefresh must not be negative"},
		{name: "negative cache ttl", cfg: Config{Audience: "a", CacheTTL: -time.Second}, want: "CacheTTL must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := New(tt.cfg)
			require.ErrorContains(t, err, tt.want)
			assert.Nil(t, v)
		})
	}
}

func TestParseJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	good := map[string]string{"kty": "RSA", "kid": "good", "use": "sig", "alg": "RS256", "n": n, "e": e}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == "" {
				delete(m, kv[i])
			} else {
				m[kv[i]] = kv[i+1]
			}
		}
		return m
	}
	encode := func(keys ...map[string]string) []byte {
		data, err := json.Marshal(map[string]any{"keys": keys})
		require.NoError(t, err)
		return data
	}

	tests := []struct {
		name    string
		data    []byte
		want    []string
		wantErr string
	}{
		{name: "one key", data: encode(good), want: []string{"good"}},
		{name: "use and alg optional", data: encode(with("use", "", "alg", "")), want: []string{"good"}},
		{name: "padded base64", data: encode(with("e", e+"=")), want: []string{"good"}},
		{name: "skips unusable keys", data: encode(
			with("kid", "ec", "kty", "EC"),
			with("kid", "enc", "use", "enc"),
			with("kid", "rs512", "alg", "RS512"),
			with("kid", ""),
			with("kid", "bad-n", "n", "!!"),
			with("kid", "bad-e", "e", "!!"),
			with("kid", "even-e", "e", base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x00})),
			with("kid", "tiny-e", "e", base64.RawURLEncoding.EncodeToString([]byte{0x01})),
			with("kid", "empty-e", "e", ""),
			with("kid", "long-e", "e", base64.RawURLEncoding.EncodeToString([]byte{1, 0, 0, 0, 1})),
			with("kid", "small", "n", base64.RawURLEncoding.EncodeToString(small.N.Bytes())),
			good,
		), want: []string{"good"}},
		{name: "first duplicate wins", data: encode(good, with("n", base64.RawURLEncoding.EncodeToString(small.N.Bytes()))), want: []string{"good"}},
		{name: "no usable key", data: encode(with("kty", "oct")), wantErr: "no usable RS256 signing key"},
		{name: "empty set", data: []byte(`{"keys":[]}`), wantErr: "no usable RS256 signing key"},
		{name: "not JSON", data: []byte(`<html>`), wantErr: "decoding key set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := parseJWKS(tt.data)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			got := make([]string, 0, len(keys))
			for kid, k := range keys {
				got = append(got, kid)
				assert.Equal(t, 0, k.N.Cmp(key.N))
				assert.Equal(t, key.E, k.E)
			}
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}
