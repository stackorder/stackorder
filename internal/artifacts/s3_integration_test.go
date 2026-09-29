//go:build integration

package artifacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/api"
	"github.com/stackorder/stackorder/internal/artifacts"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
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
	os.Exit(pgtest.Main(m, func() {
		if container != nil {
			if err := testcontainers.TerminateContainer(container); err != nil {
				fmt.Fprintf(os.Stderr, "artifacts: terminate localstack: %v\n", err)
			}
		}
	}))
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

type noRuns struct{ api.RunService }

func TestTheAPIServesPlanTextFromTheBucket(t *testing.T) {
	ep, bucket := newBucket(t)
	s := newStore(t, ep, bucket, "stackorder")
	st := pgtest.New(t)
	ctx := t.Context()
	_, err := st.UpsertInstallation(ctx, store.Installation{ID: 1, Account: "acme", AccountType: "Organization"})
	require.NoError(t, err)
	repo, err := st.UpsertRepo(ctx, store.RepoParams{ID: 100, InstallationID: 1, FullName: "acme/infra", DefaultBranch: "main"})
	require.NoError(t, err)
	const sha, vpc, blue = "abcdef0111111111111111111111111111111111", "stacks/prod/vpc", "stacks/prod/apps:blue"
	graphID, ids, err := st.SaveGraph(ctx, repo.ID, &v1.Graph{Repo: repo.FullName, SHA: sha, Stacks: []v1.Stack{
		{Key: vpc, Path: vpc}, {Key: blue, Path: "stacks/prod/apps", Workspace: "blue"},
	}})
	require.NoError(t, err)
	run, err := st.CreateRun(ctx, store.CreateRunParams{RepoID: repo.ID, SHA: sha, PRNumber: 7, Trigger: v1.TriggerPullRequest, Mode: v1.ModePlan, Status: v1.RunPlanned})
	require.NoError(t, err)
	require.NoError(t, st.SetRunGraph(ctx, run.ID, graphID, 1, nil))
	require.NoError(t, st.UpsertRunStacks(ctx, run.ID, []store.RunStack{{StackID: ids[vpc]}, {StackID: ids[blue]}}))

	full := strings.Repeat("  # module.vpc.aws_subnet.private[0] will be updated in-place\n", 20000)
	planURL, err := s.Put(ctx, store.PlanTextArtifactKey(run.ID, vpc), "text/plain; charset=utf-8", []byte(full))
	require.NoError(t, err)
	gone := s.URL(store.PlanTextArtifactKey(run.ID, blue))
	for stack, link := range map[string]string{vpc: planURL, blue: gone} {
		_, err = st.UpdateRunStack(ctx, run.ID, ids[stack], store.RunStackPatch{PlanText: aws.String(full[:8<<10]), PlanTextTruncated: aws.Bool(true), PlanURL: aws.String(link)})
		require.NoError(t, err)
	}
	key, _, err := st.CreateAPIKey(ctx, "ci", "admin")
	require.NoError(t, err)
	h := api.New(api.Config{BaseURL: "https://stackorder.test", SessionKey: bytes.Repeat([]byte{1}, 32)},
		api.Deps{Store: st, Runs: noRuns{}, Artifacts: s, Logger: slog.New(slog.DiscardHandler)})
	get := func(stack string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID.String()+"/stacks/"+url.PathEscape(stack)+"/plan", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := get(vpc)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, full, rec.Body.String(), "the object under the runs service's key, read back through the API")

	rec = get(blue)
	require.Equal(t, http.StatusNotFound, rec.Code, "an object the bucket no longer holds")
	var apiErr v1.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, "not_found", apiErr.Code)
	assert.Contains(t, apiErr.Message, "is no longer in the artifact bucket")
}
