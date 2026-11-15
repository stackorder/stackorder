//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

func secretFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if f := strings.TrimSpace(line); strings.HasPrefix(f, store.APIKeyPrefix) {
			return f
		}
	}
	t.Fatalf("no secret in output: %q", out)
	return ""
}

func TestAPIKeyCommandIntegration(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.DSN(t)
	st, err := store.Open(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(st.Close)
	require.NoError(t, st.Migrate(ctx))

	env := envOf(map[string]string{"DATABASE_URL": dsn})

	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"api-key", "create", "ci", "--actor", "alice"}, env, &stdout, &stderr), stderr.String())
	secret := secretFrom(t, stdout.String())

	verified, err := st.VerifyAPIKey(ctx, secret)
	require.NoError(t, err, "the minted key authenticates")
	assert.Equal(t, "ci", verified.Name)
	assert.Equal(t, "alice", verified.CreatedBy)

	entries, _, err := st.ListAudit(ctx, store.AuditFilter{Action: "api_key_created"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "alice", entries[0].Actor)
	assert.Equal(t, "ci", entries[0].Target)
	assert.Equal(t, verified.ID.String(), entries[0].Details["key_id"])

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"api-key", "list"}, env, &stdout, &stderr), stderr.String())
	assert.Contains(t, stdout.String(), "ci")
	assert.Contains(t, stdout.String(), verified.ID.String())

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"api-key", "revoke", verified.ID.String(), "--actor", "alice"}, env, &stdout, &stderr), stderr.String())
	assert.Contains(t, stdout.String(), "Revoked")

	_, err = st.VerifyAPIKey(ctx, secret)
	require.ErrorIs(t, err, store.ErrNotFound, "a revoked key stops authenticating")

	entries, _, err = st.ListAudit(ctx, store.AuditFilter{Action: "api_key_revoked"})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"api-key", "revoke", verified.ID.String()}, env, &stdout, &stderr), "revoking twice still succeeds")

	entries, _, err = st.ListAudit(ctx, store.AuditFilter{Action: "api_key_revoked"})
	require.NoError(t, err)
	require.Len(t, entries, 1, "revoking an already-revoked key records no further audit entry")

	stdout.Reset()
	stderr.Reset()
	missing := "00000000-0000-0000-0000-000000000000"
	require.Equal(t, 1, run([]string{"api-key", "revoke", missing}, env, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "not found")
}
