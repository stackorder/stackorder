package oidc

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultHTTPTimeout = 10 * time.Second
	maxTokenBytes      = 64 << 10
)

// Config configures a Verifier. Only Audience is required.
type Config struct {
	// Issuer is the expected iss claim. Default DefaultIssuer.
	Issuer string
	// JWKSURL is where the signing keys are fetched. Default Issuer +
	// JWKSPath.
	JWKSURL string
	// Audience is the value the aud claim must contain, normally
	// STACKORDER_OIDC_AUDIENCE.
	Audience string
	// HTTPClient fetches the JWKS. Default a client with a 10 s timeout.
	HTTPClient *http.Client
	// Clock returns the current time. Default time.Now.
	Clock func() time.Time
	// MaxAge is how long after iat a token is accepted. Default
	// DefaultMaxAge.
	MaxAge time.Duration
	// MinRefresh is the minimum interval between two JWKS fetches, which
	// bounds the fetches that tokens with unknown kids can cause. Default
	// DefaultMinRefresh.
	MinRefresh time.Duration
	// CacheTTL is how long a fetched JWKS is used before it is refreshed.
	// Default DefaultCacheTTL.
	CacheTTL time.Duration
}

// Verifier checks GitHub Actions OIDC tokens. It is safe for concurrent use.
type Verifier struct {
	cfg    Config
	parser *jwt.Parser

	refreshMu sync.Mutex

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time
	attemptedAt time.Time
	lastErr     error
}

// New validates cfg, fills its defaults and returns a Verifier. It does not
// contact the issuer; the JWKS is fetched on first use.
func New(cfg Config) (*Verifier, error) {
	if cfg.Audience == "" {
		return nil, errors.New("oidc: audience is required")
	}
	if cfg.Issuer == "" {
		cfg.Issuer = DefaultIssuer
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = strings.TrimSuffix(cfg.Issuer, "/") + JWKSPath
	}
	u, err := url.Parse(cfg.JWKSURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: JWKS URL: %w", err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("oidc: JWKS URL %q is not an absolute http(s) URL", cfg.JWKSURL)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	for _, d := range []struct {
		name string
		v    *time.Duration
		def  time.Duration
	}{
		{"MaxAge", &cfg.MaxAge, DefaultMaxAge},
		{"MinRefresh", &cfg.MinRefresh, DefaultMinRefresh},
		{"CacheTTL", &cfg.CacheTTL, DefaultCacheTTL},
	} {
		if *d.v < 0 {
			return nil, fmt.Errorf("oidc: %s must not be negative", d.name)
		}
		if *d.v == 0 {
			*d.v = d.def
		}
	}
	return &Verifier{
		cfg: cfg,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
			jwt.WithoutClaimsValidation(),
			jwt.WithStrictDecoding(),
		),
	}, nil
}

// Verify checks the token's RS256 signature against the issuer's JWKS and
// validates its registered claims: iss equals the configured issuer, aud
// (a string or an array) contains the configured audience, exp has not
// passed, nbf has been reached, iat is not in the future and not older than
// MaxAge. exp, nbf and a future iat are allowed ClockSkew of tolerance.
//
// The JWKS is fetched on first use and cached for CacheTTL. A kid missing
// from the cache triggers a refresh, at most once per MinRefresh; a stale
// cache is kept when a refresh fails. When no usable key set can be fetched
// the error wraps ErrJWKSUnavailable instead of ErrInvalidToken. Verify
// does not check claims that depend on the run; see BindPlan, BindDispatch
// and BindWorkflowRef.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	if len(rawToken) > maxTokenBytes {
		return nil, fmt.Errorf("%w: token is %d bytes", ErrMalformed, len(rawToken))
	}
	now := v.cfg.Clock()
	claims := &Claims{}
	var keyErr error
	_, err := v.parser.ParseWithClaims(rawToken, claims, func(t *jwt.Token) (any, error) {
		key, err := v.key(ctx, t.Header["kid"], now)
		keyErr = err
		return key, err
	})
	switch {
	case keyErr != nil:
		return nil, keyErr
	case errors.Is(err, jwt.ErrTokenMalformed):
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	if err := v.validate(claims, now); err != nil {
		return nil, err
	}
	return claims, nil
}

func (v *Verifier) validate(c *Claims, now time.Time) error {
	if c.Issuer != v.cfg.Issuer {
		return fmt.Errorf("%w: iss is %q, want %q", ErrIssuer, c.Issuer, v.cfg.Issuer)
	}
	if !slices.Contains(c.Audience, v.cfg.Audience) {
		return fmt.Errorf("%w: aud %q does not contain %q", ErrAudience, []string(c.Audience), v.cfg.Audience)
	}
	if c.ExpiresAt == nil {
		return fmt.Errorf("%w: missing exp", ErrMalformed)
	}
	if c.IssuedAt == nil {
		return fmt.Errorf("%w: missing iat", ErrMalformed)
	}
	if !now.Before(c.ExpiresAt.Add(ClockSkew)) {
		return fmt.Errorf("%w: exp %s is before %s", ErrExpired, c.ExpiresAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	if c.NotBefore != nil && now.Add(ClockSkew).Before(c.NotBefore.Time) {
		return fmt.Errorf("%w: nbf %s is after %s", ErrNotYetValid, c.NotBefore.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	if now.Add(ClockSkew).Before(c.IssuedAt.Time) {
		return fmt.Errorf("%w: iat %s is after %s", ErrNotYetValid, c.IssuedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	if age := now.Sub(c.IssuedAt.Time); age > v.cfg.MaxAge {
		return fmt.Errorf("%w: issued %s ago, limit %s", ErrTooOld, age.Round(time.Second), v.cfg.MaxAge)
	}
	return nil
}

func (v *Verifier) key(ctx context.Context, kidHeader any, now time.Time) (*rsa.PublicKey, error) {
	kid, _ := kidHeader.(string)
	if kid == "" {
		return nil, fmt.Errorf("%w: token has no kid header", ErrUnknownKey)
	}
	v.mu.Lock()
	key, found := v.keys[kid]
	fresh := !v.fetchedAt.IsZero() && now.Sub(v.fetchedAt) < v.cfg.CacheTTL
	v.mu.Unlock()
	if found && fresh {
		return key, nil
	}
	refreshErr := v.refresh(ctx, now)
	v.mu.Lock()
	key, found = v.keys[kid]
	v.mu.Unlock()
	switch {
	case found:
		return key, nil
	case refreshErr != nil:
		return nil, refreshErr
	default:
		return nil, fmt.Errorf("%w: kid %q is not in the issuer's JWKS", ErrUnknownKey, kid)
	}
}

func (v *Verifier) refresh(ctx context.Context, now time.Time) error {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()

	v.mu.Lock()
	throttled := !v.attemptedAt.IsZero() && now.Sub(v.attemptedAt) < v.cfg.MinRefresh
	everFetched, lastErr := !v.fetchedAt.IsZero(), v.lastErr
	if !throttled {
		v.attemptedAt = now
	}
	v.mu.Unlock()
	if throttled {
		if everFetched {
			return nil
		}
		return lastErr
	}

	keys, err := v.fetch(ctx)
	v.mu.Lock()
	defer v.mu.Unlock()
	if err != nil {
		v.lastErr = err
		return err
	}
	v.keys, v.fetchedAt, v.lastErr = keys, now, nil
	return nil
}
