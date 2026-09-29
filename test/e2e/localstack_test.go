//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

const (
	localstackImage  = "localstack/localstack:4.0"
	localstackS3Host = "s3.localhost.localstack.cloud"
	envLocalStackURL = "STACKORDER_E2E_LOCALSTACK_URL"

	stateBucket    = "stackorder-example-state"
	artifactBucket = "stackorder-e2e-artifacts"

	awsRegion    = "us-east-1"
	awsAccessKey = "test"
	awsSecretKey = "test"
)

type localStack struct {
	endpoint   string
	s3Endpoint string
	client     *s3.Client
}

var (
	localstackOnce      sync.Once
	localstackShared    *localStack
	localstackErr       error
	localstackContainer testcontainers.Container
)

func requireDocker(t *testing.T) {
	t.Helper()
	if os.Getenv(envLocalStackURL) != "" && os.Getenv(pgtest.EnvDSN) != "" {
		return
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
}

func sharedLocalStack(t *testing.T) *localStack {
	t.Helper()
	localstackOnce.Do(func() {
		localstackShared, localstackErr = startLocalStack()
	})
	require.NoError(t, localstackErr, "start %s", localstackImage)
	return localstackShared
}

func startLocalStack() (*localStack, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	endpoint := strings.TrimRight(os.Getenv(envLocalStackURL), "/")
	if endpoint == "" {
		c, err := testcontainers.Run(ctx, localstackImage,
			testcontainers.WithExposedPorts("4566/tcp"),
			testcontainers.WithEnv(map[string]string{"SERVICES": "s3,sts", "DEBUG": "0"}),
			testcontainers.WithWaitStrategy(wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").
				WithResponseMatcher(s3Ready).WithStartupTimeout(3*time.Minute)),
		)
		if c != nil {
			localstackContainer = c
		}
		if err != nil {
			return nil, err
		}
		if endpoint, err = c.PortEndpoint(ctx, "4566/tcp", "http"); err != nil {
			return nil, err
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envLocalStackURL, err)
	}
	if _, err := net.DefaultResolver.LookupHost(ctx, localstackS3Host); err != nil {
		return nil, fmt.Errorf("%s must resolve to the loopback address for virtual-hosted S3 requests: %w", localstackS3Host, err)
	}
	ls := &localStack{
		endpoint:   endpoint,
		s3Endpoint: u.Scheme + "://" + localstackS3Host + ":" + u.Port(),
		client: s3.New(s3.Options{
			Region:       awsRegion,
			BaseEndpoint: aws.String(endpoint),
			UsePathStyle: true,
			Credentials:  credentials.NewStaticCredentialsProvider(awsAccessKey, awsSecretKey, ""),
		}),
	}
	return ls, nil
}

func s3Ready(body io.Reader) bool {
	var health struct {
		Services map[string]string `json:"services"`
	}
	if err := json.NewDecoder(body).Decode(&health); err != nil {
		return false
	}
	for _, name := range []string{"s3", "sts"} {
		if s := health.Services[name]; s != "available" && s != "running" {
			return false
		}
	}
	return true
}

func stopLocalStack() {
	if localstackContainer == nil {
		return
	}
	if err := testcontainers.TerminateContainer(localstackContainer); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: terminate localstack: %v\n", err)
	}
}

func (ls *localStack) resetBucket(t *testing.T, bucket string) {
	t.Helper()
	ctx := t.Context()
	_, err := ls.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	var owned *types.BucketAlreadyOwnedByYou
	if err != nil && !errors.As(err, &owned) {
		require.NoError(t, err, "create bucket %s", bucket)
	}
	keys := ls.keys(t, bucket, "")
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		objects := make([]types.ObjectIdentifier, n)
		for i, k := range keys[:n] {
			objects[i] = types.ObjectIdentifier{Key: aws.String(k)}
		}
		_, err := ls.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)}})
		require.NoError(t, err, "empty bucket %s", bucket)
		keys = keys[n:]
	}
}

func (ls *localStack) keys(t *testing.T, bucket, prefix string) []string {
	t.Helper()
	var out []string
	p := s3.NewListObjectsV2Paginator(ls.client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(t.Context())
		require.NoError(t, err, "list s3://%s/%s", bucket, prefix)
		for _, o := range page.Contents {
			out = append(out, aws.ToString(o.Key))
		}
	}
	sort.Strings(out)
	return out
}

func (ls *localStack) get(t *testing.T, bucket, key string) ([]byte, bool) {
	t.Helper()
	out, err := ls.client.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	var missing *types.NoSuchKey
	if errors.As(err, &missing) {
		return nil, false
	}
	require.NoError(t, err, "get s3://%s/%s", bucket, key)
	defer func() { _ = out.Body.Close() }()
	body, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	return body, true
}

type tfState struct {
	Serial  int    `json:"serial"`
	Lineage string `json:"lineage"`
	Outputs map[string]struct {
		Value any `json:"value"`
	} `json:"outputs"`
	Resources []struct {
		Module    string `json:"module"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Instances []struct {
			IndexKey   any            `json:"index_key"`
			Attributes map[string]any `json:"attributes"`
		} `json:"instances"`
	} `json:"resources"`
}

func (s tfState) addresses() []string {
	var out []string
	for _, r := range s.Resources {
		addr := r.Type + "." + r.Name
		if r.Module != "" {
			addr = r.Module + "." + addr
		}
		for _, in := range r.Instances {
			switch k := in.IndexKey.(type) {
			case nil:
				out = append(out, addr)
			case string:
				out = append(out, fmt.Sprintf("%s[%q]", addr, k))
			default:
				out = append(out, fmt.Sprintf("%s[%v]", addr, k))
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s tfState) input(address string) map[string]any {
	for _, r := range s.Resources {
		addr := r.Type + "." + r.Name
		if r.Module != "" {
			addr = r.Module + "." + addr
		}
		if addr != address || len(r.Instances) == 0 {
			continue
		}
		in, _ := r.Instances[0].Attributes["input"].(map[string]any)
		if dynamic, ok := in["value"].(map[string]any); ok {
			return dynamic
		}
		return in
	}
	return nil
}

func (ls *localStack) state(t *testing.T, key string) (tfState, bool) {
	t.Helper()
	body, ok := ls.get(t, stateBucket, key)
	if !ok {
		return tfState{}, false
	}
	var st tfState
	require.NoError(t, json.Unmarshal(body, &st), "decode state s3://%s/%s", stateBucket, key)
	return st, true
}

func (ls *localStack) requireState(t *testing.T, key string) tfState {
	t.Helper()
	st, ok := ls.state(t, key)
	require.True(t, ok, "state object s3://%s/%s exists", stateBucket, key)
	return st
}

func (ls *localStack) lockObjects(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, k := range ls.keys(t, stateBucket, "") {
		if strings.HasSuffix(k, ".tflock") {
			out = append(out, k)
		}
	}
	return out
}
