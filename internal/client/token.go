package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// EnvOIDCRequestURL is the runner variable holding the ID token endpoint.
	EnvOIDCRequestURL = "ACTIONS_ID_TOKEN_REQUEST_URL"
	// EnvOIDCRequestToken is the runner variable holding the bearer token for
	// the ID token endpoint.
	EnvOIDCRequestToken = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"
	// OIDCRefreshMargin is how long before its exp claim a cached OIDC token
	// is replaced.
	OIDCRefreshMargin = 2 * time.Minute

	tokenRequestTimeout = 30 * time.Second
)

var (
	// ErrNoOIDC reports that the job cannot request an ID token: the runner
	// variables are unset, as on fork pull requests or without the
	// id-token: write permission.
	ErrNoOIDC = errors.New("client: no GitHub Actions OIDC token available; the job needs `permissions: id-token: write`")
	// ErrNoAPIKey reports an empty API key.
	ErrNoAPIKey = errors.New("client: empty API key")
)

// TokenSource supplies the bearer token for each request.
type TokenSource interface {
	// Token returns the token to send; an empty token sends no
	// Authorization header.
	Token(ctx context.Context) (string, error)
}

type invalidator interface {
	Invalidate()
}

type oidcSource struct {
	audience string
	http     *http.Client
	now      func() time.Time

	mu        sync.Mutex
	token     string
	refreshAt time.Time
}

// OIDCTokenSource returns a TokenSource that requests a GitHub Actions ID
// token for audience from ACTIONS_ID_TOKEN_REQUEST_URL, authenticated with
// ACTIONS_ID_TOKEN_REQUEST_TOKEN, both read on each fetch. A token is cached
// until OIDCRefreshMargin before its exp claim, read without verifying the
// signature; a token without a readable exp is not cached. The client drops
// the cached token and fetches a new one once when the server answers 401,
// which covers a server that accepts each token only once. Endpoint network
// failures and 5xx answers match ErrUnreachable; missing variables return
// ErrNoOIDC.
func OIDCTokenSource(audience string) TokenSource {
	return &oidcSource{
		audience: audience,
		http:     &http.Client{Timeout: tokenRequestTimeout},
		now:      time.Now,
	}
}

func (s *oidcSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Before(s.refreshAt) {
		return s.token, nil
	}
	s.token = ""
	tok, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	if exp := expiry(tok); !exp.IsZero() {
		if refreshAt := exp.Add(-OIDCRefreshMargin); s.now().Before(refreshAt) {
			s.token, s.refreshAt = tok, refreshAt
		}
	}
	return tok, nil
}

func (s *oidcSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}

func (s *oidcSource) fetch(ctx context.Context) (string, error) {
	endpoint, bearer := os.Getenv(EnvOIDCRequestURL), os.Getenv(EnvOIDCRequestToken)
	if endpoint == "" || bearer == "" {
		return "", ErrNoOIDC
	}
	if s.audience != "" {
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		endpoint += sep + "audience=" + url.QueryEscape(s.audience)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("client: building OIDC token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("%w: requesting OIDC token: %w", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return "", fmt.Errorf("%w: reading OIDC token response: %w", ErrUnreachable, err)
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return "", fmt.Errorf("%w: OIDC token endpoint returned %d", ErrUnreachable, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("client: OIDC token endpoint returned %d: %s", resp.StatusCode, snippet(body))
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("client: decoding OIDC token response: %w", err)
	}
	if out.Value == "" {
		return "", errors.New("client: OIDC token endpoint returned no token")
	}
	return out.Value, nil
}

func expiry(tok string) time.Time {
	var claims jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(tok, &claims); err != nil || claims.ExpiresAt == nil {
		return time.Time{}
	}
	return claims.ExpiresAt.Time
}

type apiKeySource string

// APIKeyTokenSource returns a TokenSource that sends an automation API key
// (sk_…) as the bearer token. An empty key fails with ErrNoAPIKey.
func APIKeyTokenSource(key string) TokenSource { return apiKeySource(key) }

func (k apiKeySource) Token(context.Context) (string, error) {
	if k == "" {
		return "", ErrNoAPIKey
	}
	return string(k), nil
}

type staticSource string

// StaticTokenSource returns a TokenSource that always returns tok; an empty
// tok sends requests without an Authorization header.
func StaticTokenSource(tok string) TokenSource { return staticSource(tok) }

func (s staticSource) Token(context.Context) (string, error) { return string(s), nil }
