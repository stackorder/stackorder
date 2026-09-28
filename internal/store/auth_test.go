//go:build integration

package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestSeenJTI(t *testing.T) {
	f := newFixture(t)
	exp := time.Now().Add(10 * time.Minute)
	cases := []struct {
		name    string
		jti     string
		exp     time.Time
		want    bool
		wantErr error
	}{
		{"first use", "jti-1", exp, false, nil},
		{"replay", "jti-1", exp, true, nil},
		{"other token", "jti-2", time.Now().Add(-time.Minute), false, nil},
		{"empty", "", exp, false, store.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen, err := f.s.SeenJTI(f.ctx, tc.jti, tc.exp)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, seen)
		})
	}
	n, err := f.s.PruneJTIs(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	seen, err := f.s.SeenJTI(f.ctx, "jti-1", exp)
	require.NoError(t, err)
	assert.True(t, seen, "unexpired ids survive pruning")
}

func TestSessions(t *testing.T) {
	f := newFixture(t)
	token, sess, err := f.s.CreateSession(f.ctx, store.NewSession{
		Login: "octocat", UserID: 1, AvatarURL: "https://avatars/1", Orgs: []string{"acme"}, TTL: time.Hour,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.NotEqual(t, token, sess.ID, "the token itself is not stored")
	assert.WithinDuration(t, time.Now().Add(time.Hour), sess.ExpiresAt, time.Minute)

	got, err := f.s.GetSession(f.ctx, token)
	require.NoError(t, err)
	assert.Equal(t, sess, got)
	assert.Equal(t, v1.Whoami{Login: "octocat", AvatarURL: "https://avatars/1", Orgs: []string{"acme"}}, got.ToV1())

	expired, _, err := f.s.CreateSession(f.ctx, store.NewSession{Login: "old", TTL: time.Microsecond})
	require.NoError(t, err)
	f.exec(`UPDATE sessions SET expires_at = now() - interval '1 second' WHERE login = 'old'`)
	_, err = f.s.GetSession(f.ctx, expired)
	require.ErrorIs(t, err, store.ErrNotFound)
	n, err := f.s.PruneSessions(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	require.NoError(t, f.s.DeleteSession(f.ctx, token))
	require.NoError(t, f.s.DeleteSession(f.ctx, token))
	_, err = f.s.GetSession(f.ctx, token)
	require.ErrorIs(t, err, store.ErrNotFound)

	_, _, err = f.s.CreateSession(f.ctx, store.NewSession{Login: "x"})
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestAPIKeys(t *testing.T) {
	f := newFixture(t)
	plaintext, key, err := f.s.CreateAPIKey(f.ctx, "ci", "octocat")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plaintext, store.APIKeyPrefix))
	assert.True(t, strings.HasPrefix(plaintext, key.Prefix))
	assert.Len(t, key.Prefix, 10)
	assert.Nil(t, key.LastUsedAt)

	var stored string
	require.NoError(t, f.s.Pool().QueryRow(f.ctx, `SELECT key_hash FROM api_keys WHERE id = $1`, key.ID).Scan(&stored))
	assert.NotContains(t, stored, plaintext[len(store.APIKeyPrefix):], "only the hash is stored")

	verified, err := f.s.VerifyAPIKey(f.ctx, plaintext)
	require.NoError(t, err)
	assert.Equal(t, key.ID, verified.ID)
	assert.NotNil(t, verified.LastUsedAt)

	cases := []struct {
		name      string
		plaintext string
	}{
		{"unknown", store.APIKeyPrefix + "nope"},
		{"malformed", "not-a-key"},
		{"prefix only", store.APIKeyPrefix},
		{"tampered", plaintext + "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.s.VerifyAPIKey(f.ctx, tc.plaintext)
			require.ErrorIs(t, err, store.ErrNotFound)
		})
	}

	second, _, err := f.s.CreateAPIKey(f.ctx, "deploy", "octocat")
	require.NoError(t, err)
	require.NotEqual(t, plaintext, second)
	require.NoError(t, f.s.RevokeAPIKey(f.ctx, key.ID))
	require.NoError(t, f.s.RevokeAPIKey(f.ctx, key.ID))
	_, err = f.s.VerifyAPIKey(f.ctx, plaintext)
	require.ErrorIs(t, err, store.ErrNotFound, "revoked keys no longer verify")
	_, err = f.s.VerifyAPIKey(f.ctx, second)
	require.NoError(t, err)
	require.ErrorIs(t, f.s.RevokeAPIKey(f.ctx, uuid.New()), store.ErrNotFound)

	keys, err := f.s.ListAPIKeys(f.ctx)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	names := map[string]bool{}
	for _, k := range keys {
		names[k.Name] = k.RevokedAt != nil
	}
	assert.Equal(t, map[string]bool{"ci": true, "deploy": false}, names)

	_, _, err = f.s.CreateAPIKey(f.ctx, "", "x")
	require.ErrorIs(t, err, store.ErrInvalid)
}

func TestAudit(t *testing.T) {
	f := newFixture(t)
	entries := []store.AuditEntry{
		{Actor: "octocat", Action: "unlock", Target: "acme/infra//stacks/a", Details: map[string]any{"reason": "stuck"}},
		{Actor: "octocat", Action: "rerun", Target: "run-1"},
		{Actor: "hubot", Action: "unlock", Target: "acme/infra//stacks/b"},
		{Actor: "octocat", Action: "unlock", Target: "acme/infra//stacks/c"},
	}
	for _, e := range entries {
		got, err := f.s.RecordAudit(f.ctx, e)
		require.NoError(t, err)
		assert.NotZero(t, got.ID)
		assert.Equal(t, e.Details, got.Details)
	}
	_, err := f.s.RecordAudit(f.ctx, store.AuditEntry{Action: "x"})
	require.ErrorIs(t, err, store.ErrInvalid)

	cases := []struct {
		name   string
		filter store.AuditFilter
		want   []string
	}{
		{"all newest first", store.AuditFilter{}, []string{"acme/infra//stacks/c", "acme/infra//stacks/b", "run-1", "acme/infra//stacks/a"}},
		{"by actor and action", store.AuditFilter{Actor: "octocat", Action: "unlock"}, []string{"acme/infra//stacks/c", "acme/infra//stacks/a"}},
		{"by target", store.AuditFilter{Target: "run-1"}, []string{"run-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, next, err := f.s.ListAudit(f.ctx, tc.filter)
			require.NoError(t, err)
			assert.Empty(t, next)
			var targets []string
			for _, e := range got {
				targets = append(targets, e.Target)
			}
			assert.Equal(t, tc.want, targets)
		})
	}

	page1, next, err := f.s.ListAudit(f.ctx, store.AuditFilter{Limit: 3})
	require.NoError(t, err)
	require.Len(t, page1, 3)
	require.NotEmpty(t, next)
	page2, next, err := f.s.ListAudit(f.ctx, store.AuditFilter{Limit: 3, Cursor: next})
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Empty(t, next)
	assert.Equal(t, "acme/infra//stacks/a", page2[0].Target)
	_, _, err = f.s.ListAudit(f.ctx, store.AuditFilter{Cursor: "zz"})
	require.ErrorIs(t, err, store.ErrInvalid)
}
