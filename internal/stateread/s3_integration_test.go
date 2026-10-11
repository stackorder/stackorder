//go:build integration

package stateread_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/stackorder/stackorder/internal/stateread"
)

const (
	localstackImage = "localstack/localstack:4.0"
	envEndpoint     = "TEST_S3_ENDPOINT"
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
			fmt.Fprintf(os.Stderr, "stateread: terminate localstack: %v\n", err)
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

func newBucket(t *testing.T, region string, objects map[string][]byte) (string, string) {
	t.Helper()
	ep := localstack(t)
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(ep),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	})
	bucket := "acme-state-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	in := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	if region != "us-east-1" {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(region)}
	}
	_, err := client.CreateBucket(t.Context(), in)
	require.NoError(t, err)
	for key, body := range objects {
		_, err := client.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
		require.NoError(t, err)
	}
	return ep, bucket
}

var credentialScope = regexp.MustCompile(`Credential=([^/]+)/\d{8}/([^/]+)/([^/]+)/aws4_request`)

type recorder struct {
	mu       sync.Mutex
	requests []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	signed := "unsigned"
	if m := credentialScope.FindStringSubmatch(req.Header.Get("Authorization")); m != nil {
		signed = m[3] + " " + m[2] + " " + m[1]
	}
	r.mu.Lock()
	r.requests = append(r.requests, req.Method+" "+signed)
	r.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

func newLocalReader(t *testing.T, ep string, grants []stateread.Grant, opts ...stateread.Option) *stateread.Reader {
	t.Helper()
	opts = append([]stateread.Option{
		stateread.WithEndpoint(ep), stateread.WithRegion("us-east-1"), stateread.WithStaticCredentials(accessKey, secretKey),
	}, opts...)
	r, err := stateread.New(t.Context(), grants, opts...)
	require.NoError(t, err)
	return r
}

func TestReadOutputs(t *testing.T) {
	ep, bucket := newBucket(t, "us-east-1", map[string][]byte{
		"stacks/prod/vpc/terraform.tfstate":           fixture(t, "terraform.tfstate"),
		"env:/blue/stacks/prod/apps/tofu.tfstate":     fixture(t, "tofu.tfstate"),
		"stacks/prod/vpc/encrypted/terraform.tfstate": fixture(t, "tofu-encrypted.tfstate"),
	})
	grants, err := stateread.ParseGrants(producer + "=" + bucket + "/stacks/prod/, *=" + bucket + "/env:/blue/")
	require.NoError(t, err)
	r := newLocalReader(t, ep, grants)
	tests := []struct {
		key              string
		terraformVersion string
	}{
		{"stacks/prod/vpc/terraform.tfstate", "1.14.4"},
		{"env:/blue/stacks/prod/apps/tofu.tfstate", "1.12.6"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			st, err := r.Read(t.Context(), producer, stateread.Location{Bucket: bucket, Key: tt.key, Region: "us-east-1"})
			require.NoError(t, err)
			assert.Equal(t, tt.terraformVersion, st.TerraformVersion)
			assert.Equal(t, uint64(1), st.Serial)
			require.Empty(t, cmp.Diff(fixtureOutputs(), st.Outputs))
		})
	}

	_, err = r.Read(t.Context(), producer, stateread.Location{Bucket: bucket, Key: "stacks/prod/vpc/encrypted/terraform.tfstate"})
	require.ErrorIs(t, err, stateread.ErrEncrypted)

	_, err = r.Read(t.Context(), "acme/network", stateread.Location{Bucket: bucket, Key: "stacks/prod/vpc/terraform.tfstate", Region: "us-east-1"})
	require.ErrorIs(t, err, stateread.ErrNotAllowed, "the prefix is granted to %s only", producer)
	st, err := r.Read(t.Context(), "acme/network", stateread.Location{Bucket: bucket, Key: "env:/blue/stacks/prod/apps/tofu.tfstate", Region: "us-east-1"})
	require.NoError(t, err, "the wildcard grant covers every repository")
	assert.Equal(t, "1.12.6", st.TerraformVersion)
}

func TestReadMissingObject(t *testing.T) {
	ep, bucket := newBucket(t, "us-east-1", nil)
	r := newLocalReader(t, ep, []stateread.Grant{{Repository: producer, Bucket: bucket}, {Repository: producer, Bucket: "acme-state-missing"}})

	_, err := r.Read(t.Context(), producer, stateread.Location{Bucket: bucket, Key: "stacks/prod/vpc/terraform.tfstate"})
	require.ErrorIs(t, err, stateread.ErrNotFound)
	assert.Contains(t, err.Error(), "s3://"+bucket+"/stacks/prod/vpc/terraform.tfstate")

	_, err = r.Read(t.Context(), producer, stateread.Location{Bucket: "acme-state-missing", Key: "terraform.tfstate"})
	require.ErrorIs(t, err, stateread.ErrNotFound, "a missing bucket holds no state object")
}

func TestReadRefusesUngrantedBucketWithoutCallingS3(t *testing.T) {
	const unreachable = "http://127.0.0.1:1"
	rec := &recorder{}
	r, err := stateread.New(t.Context(), []stateread.Grant{
		{Repository: producer, Bucket: "acme-state", Prefix: "stacks/prod"},
		{Repository: "acme/network", Bucket: "acme-state", Prefix: "stacks/network/"},
	},
		stateread.WithEndpoint(unreachable), stateread.WithRegion("us-east-1"),
		stateread.WithStaticCredentials(accessKey, secretKey), stateread.WithHTTPClient(&http.Client{Transport: rec}))
	require.NoError(t, err)

	for _, read := range []struct {
		repo string
		loc  stateread.Location
	}{
		{producer, stateread.Location{Bucket: "acme-other", Key: "stacks/prod/vpc/terraform.tfstate"}},
		{producer, stateread.Location{Bucket: "acme-state", Key: "stacks/dev/vpc/terraform.tfstate"}},
		{producer, stateread.Location{Bucket: "acme-state", Key: "stacks/production/vpc/terraform.tfstate"}},
		{producer, stateread.Location{Bucket: "acme-state", Key: "stacks/prod/../dev/terraform.tfstate"}},
		{producer, stateread.Location{Bucket: "acme-state", Key: "stacks/network/vpc/terraform.tfstate"}},
		{"acme/network", stateread.Location{Bucket: "acme-state", Key: "stacks/prod/vpc/terraform.tfstate"}},
	} {
		_, err := r.Read(t.Context(), read.repo, read.loc)
		require.ErrorIs(t, err, stateread.ErrNotAllowed, "s3://%s/%s for %s", read.loc.Bucket, read.loc.Key, read.repo)
	}
	assert.Empty(t, rec.seen(), "no request was sent")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = r.Read(ctx, producer, stateread.Location{Bucket: "acme-state", Key: "stacks/prod/vpc/terraform.tfstate"})
	require.Error(t, err, "a granted location does reach the unreachable endpoint")
	assert.NotErrorIs(t, err, stateread.ErrNotAllowed)
	assert.NotEmpty(t, rec.seen())
}

func TestReadUsesTheObjectRegion(t *testing.T) {
	ep, bucket := newBucket(t, "eu-central-1", map[string][]byte{"terraform.tfstate": fixture(t, "terraform.tfstate")})
	rec := &recorder{}
	r := newLocalReader(t, ep, []stateread.Grant{{Repository: producer, Bucket: bucket}}, stateread.WithHTTPClient(&http.Client{Transport: rec}))

	st, err := r.Read(t.Context(), producer, stateread.Location{Bucket: bucket, Key: "terraform.tfstate", Region: "eu-central-1"})
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(fixtureOutputs(), st.Outputs))
	assert.Equal(t, []string{"GET s3 eu-central-1 " + accessKey}, rec.seen(), "signed for the object's region, not the reader's us-east-1")
}

func TestReadAssumesTheRole(t *testing.T) {
	ep, bucket := newBucket(t, "us-east-1", map[string][]byte{"terraform.tfstate": fixture(t, "tofu.tfstate")})
	rec := &recorder{}
	r := newLocalReader(t, ep, []stateread.Grant{{Repository: producer, Bucket: bucket}},
		stateread.WithRoleARN("arn:aws:iam::000000000000:role/acme-stackorder-state"),
		stateread.WithHTTPClient(&http.Client{Transport: rec}))

	for range 2 {
		st, err := r.Read(t.Context(), producer, stateread.Location{Bucket: bucket, Key: "terraform.tfstate", Region: "us-east-1"})
		require.NoError(t, err)
		assert.Equal(t, "1.12.6", st.TerraformVersion)
	}
	seen := rec.seen()
	require.Len(t, seen, 3, "one role assumption, then two reads with its cached credentials")
	assert.Equal(t, "POST sts us-east-1 "+accessKey, seen[0], "the role is assumed with the base credentials")
	for _, get := range seen[1:] {
		assert.True(t, strings.HasPrefix(get, "GET s3 us-east-1 "), get)
		assert.NotEqual(t, "GET s3 us-east-1 "+accessKey, get, "S3 is read with the assumed role's credentials")
	}
}
