package artifacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/api"
	"github.com/stackorder/stackorder/internal/artifacts"
	"github.com/stackorder/stackorder/internal/runs"
)

var (
	_ runs.ArtifactStore = (*artifacts.S3Store)(nil)
	_ api.ArtifactReader = (*artifacts.S3Store)(nil)
)

func offline(t *testing.T, bucket, prefix string) (*artifacts.S3Store, error) {
	t.Helper()
	return artifacts.NewS3Store(t.Context(), bucket, prefix,
		artifacts.WithRegion("eu-west-1"),
		artifacts.WithStaticCredentials("AKIDEXAMPLE", "secret"),
		artifacts.WithEndpoint("http://127.0.0.1:1/"))
}

func TestPrefixNormalisation(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
	}{
		{"", ""},
		{"/", ""},
		{"plans", "plans/"},
		{"/plans", "plans/"},
		{"plans/", "plans/"},
		{"//stackorder/plans//", "stackorder/plans/"},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			s, err := offline(t, "bucket", tt.prefix)
			require.NoError(t, err)
			assert.Equal(t, tt.want, s.Prefix())
			assert.Equal(t, "bucket", s.Bucket())
			assert.Equal(t, tt.want+"runs/1/plan.txt", s.ObjectKey("/runs/1/plan.txt"))
			assert.Equal(t, "s3://bucket/"+tt.want+"runs/1/plan.txt", s.URL("runs/1/plan.txt"))
		})
	}
}

func TestInvalidArguments(t *testing.T) {
	_, err := offline(t, " ", "plans")
	require.ErrorContains(t, err, "a bucket is required")

	s, err := offline(t, "bucket", "plans")
	require.NoError(t, err)
	_, err = s.Put(t.Context(), "/", "text/plain", []byte("x"))
	require.ErrorContains(t, err, "a key is required")
	_, _, err = s.Get(t.Context(), "")
	require.ErrorContains(t, err, "a key is required")
}

func TestPutReportsUnreachableEndpoint(t *testing.T) {
	s, err := offline(t, "bucket", "plans")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = s.Put(ctx, "runs/1/plan.txt", "text/plain", []byte("x"))
	require.ErrorContains(t, err, "artifacts: put s3://bucket/plans/runs/1/plan.txt")
}
