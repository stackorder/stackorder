// Package oidc verifies the GitHub Actions OIDC tokens that runner jobs
// present to the server and binds their claims to the run the server
// expects.
//
// A Verifier checks the RS256 signature against GitHub's JWKS, which it
// caches, and validates iss, aud, exp, nbf and iat. VerifyOnce additionally
// rejects a token whose jti was already used. BindPlan, BindDispatch and
// BindWorkflowRef then compare the verified claims with what the server
// recorded for the run.
//
// Errors that reject the token itself all match ErrInvalidToken with
// errors.Is, and each also matches its specific sentinel. A claim that does
// not match the run is reported as an *ErrBinding naming the claim.
package oidc

import "time"

const (
	// DefaultIssuer is the issuer of GitHub.com Actions OIDC tokens.
	DefaultIssuer = "https://token.actions.githubusercontent.com"
	// JWKSPath is appended to the issuer to form the default JWKS URL.
	JWKSPath = "/.well-known/jwks"
	// DefaultMaxAge is how long after iat a token is still accepted.
	DefaultMaxAge = 10 * time.Minute
	// DefaultMinRefresh is the minimum interval between two JWKS fetches.
	DefaultMinRefresh = time.Minute
	// DefaultCacheTTL is how long a fetched JWKS is used before it is
	// refreshed.
	DefaultCacheTTL = time.Hour
	// ClockSkew is the tolerance applied to exp, nbf and a future iat.
	ClockSkew = 60 * time.Second
)

type tokenError string

func (e tokenError) Error() string { return string(e) }

func (e tokenError) Is(target error) bool { return target == ErrInvalidToken }

var (
	// ErrInvalidToken is matched by every error that rejects the token
	// itself: ErrMalformed, ErrInvalidSignature, ErrUnknownKey, ErrIssuer,
	// ErrAudience, ErrExpired, ErrNotYetValid, ErrTooOld and ErrReplay.
	ErrInvalidToken error = tokenError("oidc: invalid token")
	// ErrMalformed reports a token that cannot be decoded or lacks a
	// required claim.
	ErrMalformed error = tokenError("oidc: malformed token")
	// ErrInvalidSignature reports a signature that does not verify or a
	// signing algorithm other than RS256.
	ErrInvalidSignature error = tokenError("oidc: invalid token signature")
	// ErrUnknownKey reports a kid that is absent from the issuer's JWKS.
	ErrUnknownKey error = tokenError("oidc: unknown signing key")
	// ErrIssuer reports an iss claim other than the configured issuer.
	ErrIssuer error = tokenError("oidc: unexpected issuer")
	// ErrAudience reports an aud claim that does not contain the configured
	// audience.
	ErrAudience error = tokenError("oidc: unexpected audience")
	// ErrExpired reports a token whose exp has passed.
	ErrExpired error = tokenError("oidc: token expired")
	// ErrNotYetValid reports a token whose nbf or iat lies in the future.
	ErrNotYetValid error = tokenError("oidc: token not yet valid")
	// ErrTooOld reports a token issued more than MaxAge ago.
	ErrTooOld error = tokenError("oidc: token too old")
	// ErrReplay reports a token whose jti was already used.
	ErrReplay error = tokenError("oidc: token replayed")
)
