package oidc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
)

type recordingStore struct {
	calls []time.Time
	jtis  []string
	err   error
}

func (s *recordingStore) SeenJTI(_ context.Context, jti string, exp time.Time) (bool, error) {
	s.jtis = append(s.jtis, jti)
	s.calls = append(s.calls, exp)
	return false, s.err
}

func TestVerifyOnceRejectsReplay(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	store := oidc.NewMemoryJTIStore(iss.Now)
	claims := iss.DispatchClaims("acme/infra", "42", 555, "production", "main", sha)
	token := iss.Token(claims)

	got, err := v.VerifyOnce(t.Context(), token, store)
	require.NoError(t, err)
	assert.Equal(t, "555", got.RunID)

	iss.Advance(time.Minute)
	_, err = v.VerifyOnce(t.Context(), token, store)
	require.ErrorIs(t, err, oidc.ErrReplay)
	assert.ErrorIs(t, err, oidc.ErrInvalidToken)
	assert.ErrorContains(t, err, "already used")

	_, err = v.VerifyOnce(t.Context(), iss.Token(claims), store)
	require.NoError(t, err, "a fresh token for the same job is accepted")

	iss.Advance(oidc.DefaultMaxAge)
	_, err = v.VerifyOnce(t.Context(), token, store)
	require.ErrorIs(t, err, oidc.ErrInvalidToken, "a token outlives its jti record only in a state Verify refuses")
	assert.NotErrorIs(t, err, oidc.ErrReplay)
}

func TestVerifyOnceRetainsJTIUntilTokenIsRefused(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	tests := []struct {
		name string
		iat  time.Duration
		exp  time.Duration
		want time.Duration
	}{
		{name: "exp plus skew comes first", iat: 0, exp: 5 * time.Minute, want: 5*time.Minute + oidc.ClockSkew},
		{name: "max age comes first", iat: -8 * time.Minute, exp: 5 * time.Minute, want: 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := iss.PlanClaims("acme/infra", "42", 7, sha)
			c.ID = "jti-" + tt.name
			c.IssuedAt, c.ExpiresAt = at(iss, tt.iat), at(iss, tt.exp)
			store := &recordingStore{}
			_, err := v.VerifyOnce(t.Context(), iss.Token(c), store)
			require.NoError(t, err)
			require.Len(t, store.calls, 1)
			assert.Equal(t, []string{c.ID}, store.jtis)
			assert.True(t, store.calls[0].Equal(iss.Now().Add(tt.want)), "retained until %s", store.calls[0])
		})
	}
}

func TestVerifyOnceFailures(t *testing.T) {
	iss := oidcfake.New(t)
	v := newVerifier(t, iss)
	storeErr := errors.New("database is down")
	claims := iss.PlanClaims("acme/infra", "42", 7, sha)
	expired := claims
	expired.IssuedAt, expired.ExpiresAt = at(iss, -9*time.Minute), at(iss, -4*time.Minute)
	tests := []struct {
		name      string
		token     string
		storeErr  error
		want      error
		wantCalls int
		wantToken bool
	}{
		{name: "missing jti", token: iss.Token(claims, oidcfake.WithoutClaim("jti")), want: oidc.ErrMalformed, wantToken: true},
		{name: "invalid token", token: iss.Token(expired), want: oidc.ErrExpired, wantToken: true},
		{name: "store failure", token: iss.Token(claims), storeErr: storeErr, want: storeErr, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &recordingStore{err: tt.storeErr}
			got, err := v.VerifyOnce(t.Context(), tt.token, store)
			require.ErrorIs(t, err, tt.want)
			assert.Nil(t, got)
			assert.Equal(t, tt.wantToken, errors.Is(err, oidc.ErrInvalidToken))
			assert.Len(t, store.calls, tt.wantCalls)
		})
	}
}
