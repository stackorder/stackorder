//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/stackorder/stackorder/internal/artifacts"
)

const (
	localstackImage = "localstack/localstack:4.0"
	envEndpoint     = "TEST_S3_ENDPOINT"
	region          = "us-east-1"
	accessKey       = "test"
	secretKey       = "test"
)

var (
	startOnce sync.Once
	endpoint  string
	startErr  error
	container testcontainers.Container
)

func TestMain(m *testing.M) {
	code := m.Run()
	if container != nil {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "artifacts: terminate localstack: %v\n", err)
		}
	}
	os.Exit(code)
}

func localstack(t *testing.T) string {
	t.Helper()
	if e := os.Getenv(envEndpoint); e != "" {
		return e
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	startOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		c, err := testcontainers.Run(ctx, localstackImage,
			testcontainers.WithExposedPorts("4566/tcp"),
			testcontainers.WithEnv(map[string]string{"SERVICES": "s3,sts", "DEBUG": "0"}),
			testcontainers.WithWaitStrategy(wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").
				WithResponseMatcher(func(body io.Reader) bool {
					b, err := io.ReadAll(body)
					return err == nil && (strings.Contains(string(b), `"s3": "available"`) || strings.Contains(string(b), `"s3": "running"`))
				}).WithStartupTimeout(2*time.Minute)),
		)
		if c != nil {
			container = c
		}
		if err != nil {
			startErr = err
			return
		}
		endpoint, startErr = c.PortEndpoint(ctx, "4566/tcp", "http")
	})
	require.NoError(t, startErr, "start %s", localstackImage)
	return endpoint
}

func newBucket(t *testing.T) (string, string) {
	t.Helper()
	ep := localstack(t)
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(ep),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	})
	bucket := "stackorder-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	_, err := client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	return ep, bucket
}

func newStore(t *testing.T, ep, bucket, prefix string) *artifacts.S3Store {
	t.Helper()
	s, err := artifacts.NewS3Store(t.Context(), bucket, prefix,
		artifacts.WithEndpoint(ep), artifacts.WithRegion(region), artifacts.WithStaticCredentials(accessKey, secretKey))
	require.NoError(t, err)
	return s
}

func TestPutAndGet(t *testing.T) {
	ep, bucket := newBucket(t)
	s := newStore(t, ep, bucket, "")

	body := []byte("Terraform will perform the following actions:\n  # aws_vpc.main will be created\n")
	url, err := s.Put(t.Context(), "runs/r1/stacks-prod-vpc/plan.txt", "text/plain; charset=utf-8", body)
	require.NoError(t, err)
	assert.Equal(t, "s3://"+bucket+"/runs/r1/stacks-prod-vpc/plan.txt", url)

	got, contentType, err := s.Get(t.Context(), "runs/r1/stacks-prod-vpc/plan.txt")
	require.NoError(t, err)
	assert.Equal(t, body, got)
	assert.Equal(t, "text/plain; charset=utf-8", contentType)

	_, err = s.Put(t.Context(), "runs/r1/stacks-prod-vpc/plan.txt", "text/plain; charset=utf-8", []byte("replaced"))
	require.NoError(t, err)
	got, _, err = s.Get(t.Context(), "runs/r1/stacks-prod-vpc/plan.txt")
	require.NoError(t, err)
	assert.Equal(t, "replaced", string(got), "a second put replaces the object")

	_, _, err = s.Get(t.Context(), "runs/missing/plan.txt")
	require.ErrorIs(t, err, artifacts.ErrNotFound)
}

func TestContentTypes(t *testing.T) {
	ep, bucket := newBucket(t)
	s := newStore(t, ep, bucket, "plans")
	tests := []struct {
		key         string
		contentType string
		body        string
	}{
		{"a/plan.json", "application/json", `{"adds":1}`},
		{"a/plan.txt", "text/plain; charset=utf-8", "plan"},
		{"a/empty.txt", "text/plain", ""},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, err := s.Put(t.Context(), tt.key, tt.contentType, []byte(tt.body))
			require.NoError(t, err)
			got, contentType, err := s.Get(t.Context(), tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.body, string(got))
			assert.Equal(t, tt.contentType, contentType)
		})
	}
}

func TestPrefixes(t *testing.T) {
	ep, bucket := newBucket(t)
	root := newStore(t, ep, bucket, "")
	for _, prefix := range []string{"plans", "/plans", "plans/", "stackorder/plans"} {
		t.Run(prefix, func(t *testing.T) {
			s := newStore(t, ep, bucket, prefix)
			key := "runs/" + uuid.NewString() + "/plan.txt"
			url, err := s.Put(t.Context(), key, "text/plain", []byte(prefix))
			require.NoError(t, err)
			objectKey := strings.Trim(prefix, "/") + "/" + key
			assert.Equal(t, "s3://"+bucket+"/"+objectKey, url)

			got, _, err := root.Get(t.Context(), objectKey)
			require.NoError(t, err, "the object sits under the prefix")
			assert.Equal(t, prefix, string(got))
			got, _, err = s.Get(t.Context(), key)
			require.NoError(t, err, "the prefixed store reads its own key")
			assert.Equal(t, prefix, string(got))
			_, _, err = root.Get(t.Context(), key)
			require.ErrorIs(t, err, artifacts.ErrNotFound, "nothing is written outside the prefix")
		})
	}
}

func TestMissingBucket(t *testing.T) {
	ep := localstack(t)
	s := newStore(t, ep, "stackorder-does-not-exist", "plans")
	_, err := s.Put(t.Context(), "runs/r1/plan.txt", "text/plain", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifacts: put s3://stackorder-does-not-exist/plans/runs/r1/plan.txt")
	assert.False(t, errors.Is(err, artifacts.ErrNotFound))
}
