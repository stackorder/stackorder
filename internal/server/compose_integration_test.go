//go:build integration

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestComposeWiresTheStateReaderOnlyWithBuckets(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	const (
		role     = "arn:aws:iam::000000000000:role/stackorder-state-reader"
		endpoint = "http://localstack:4566"
	)
	tests := []struct {
		name    string
		vars    []string
		buckets []any
		role    string
	}{
		{
			name: "no state buckets",
			vars: []string{EnvArtifactEndpoint, endpoint},
		},
		{
			name:    "one bucket",
			vars:    []string{EnvStateBuckets, "acme-state"},
			buckets: []any{"acme-state"},
		},
		{
			name:    "prefixes, a role and an endpoint",
			vars:    []string{EnvStateBuckets, "acme-state/stacks/prod/, acme-shared", EnvStateRoleARN, role, EnvArtifactEndpoint, endpoint},
			buckets: []any{"acme-state/stacks/prod/", "acme-shared"},
			role:    role,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadConfig(envOf(with(appEnv(t), append([]string{EnvDatabaseURL, pgtest.DSN(t)}, tt.vars...)...)))
			require.NoError(t, err)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			var logs bytes.Buffer
			s, err := New(t.Context(), cfg, WithListener(ln), WithLogger(NewLogger(&logs, slog.LevelInfo, LogFormatJSON)))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })

			session := principal.Principal{Kind: principal.Session, Login: "octocat"}
			_, err = s.runs.StackOutputs(t.Context(), session, "acme/infra", "stacks/prod/vpc", "", "")
			require.Error(t, err)
			var coded *principal.CodedError
			disabled := errors.As(err, &coded) && coded.Code == v1.ErrCodeOutputsDisabled
			lines := logLines(t, logs.String(), "shared outputs are on")
			if tt.buckets == nil {
				assert.True(t, disabled, "without state buckets the run service has no state reader, got %v", err)
				assert.Empty(t, lines)
				return
			}
			assert.False(t, disabled, "with state buckets the run service has a state reader")
			require.ErrorIs(t, err, principal.ErrForbidden, "past the state reader check, a session is refused as a reader")
			require.Len(t, lines, 1, "one start-up line says shared outputs are on")
			assert.Equal(t, "INFO", lines[0]["level"])
			assert.InDelta(t, len(tt.buckets), lines[0]["grants"], 0)
			assert.Equal(t, tt.buckets, lines[0]["state_buckets"])
			assert.Equal(t, tt.role, lines[0]["role_arn"])
		})
	}
}

func logLines(t *testing.T, logs, msgPrefix string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if msg, _ := m["msg"].(string); strings.HasPrefix(msg, msgPrefix) {
			out = append(out, m)
		}
	}
	return out
}
