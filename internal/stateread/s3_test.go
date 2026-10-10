package stateread_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/stateread"
)

var credentialRegion = regexp.MustCompile(`Credential=[^/]+/\d{8}/([^/]+)/s3/aws4_request`)

type fakeS3 struct {
	*httptest.Server
	mu      sync.Mutex
	regions []string
	paths   []string
}

func (f *fakeS3) requests() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.regions...), append([]string(nil), f.paths...)
}

func newFakeS3(t *testing.T, serve func(w http.ResponseWriter, r *http.Request, region string)) *fakeS3 {
	t.Helper()
	f := &fakeS3{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		region := ""
		if m := credentialRegion.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
			region = m[1]
		}
		f.mu.Lock()
		f.regions = append(f.regions, region)
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		serve(w, r, region)
	}))
	t.Cleanup(f.Close)
	return f
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>m</Message><RequestId>r</RequestId></Error>`, code)
}

func serveState(state []byte) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(state)
	}
}

var acme = stateread.Location{Bucket: "acme-state", Key: "stacks/prod/vpc/terraform.tfstate", Region: "eu-west-1"}

func newReader(t *testing.T, endpoint string, grants ...stateread.Grant) *stateread.Reader {
	t.Helper()
	if len(grants) == 0 {
		grants = []stateread.Grant{{Bucket: "acme-state"}}
	}
	r, err := stateread.New(t.Context(), grants,
		stateread.WithEndpoint(endpoint), stateread.WithRegion("us-east-1"),
		stateread.WithStaticCredentials("AKIDEXAMPLE", "secret"))
	require.NoError(t, err)
	return r
}

func TestNewRequiresValidGrants(t *testing.T) {
	tests := []struct {
		name   string
		grants []stateread.Grant
		want   string
	}{
		{"no grants", nil, "at least one grant"},
		{"no bucket", []stateread.Grant{{Prefix: "stacks/prod/"}}, `grant bucket "" is not a valid S3 bucket name`},
		{"invalid bucket", []stateread.Grant{{Bucket: "acme-state"}, {Bucket: "Acme_State"}}, `grant bucket "Acme_State" is not a valid S3 bucket name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := stateread.New(t.Context(), tt.grants, stateread.WithRegion("us-east-1"))
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestReadDecodesTheObject(t *testing.T) {
	s3 := newFakeS3(t, serveState(fixture(t, "terraform.tfstate")))
	st, err := newReader(t, s3.URL).Read(t.Context(), acme)
	require.NoError(t, err)
	assert.Equal(t, "1.14.4", st.TerraformVersion)
	assert.Len(t, st.Outputs, len(fixtureOutputs()))
	regions, paths := s3.requests()
	assert.Equal(t, []string{"eu-west-1"}, regions, "signed for the object's region")
	assert.Equal(t, []string{"/acme-state/stacks/prod/vpc/terraform.tfstate"}, paths)
}

func TestReadRefusesBeforeCallingS3(t *testing.T) {
	s3 := newFakeS3(t, serveState(fixture(t, "terraform.tfstate")))
	r := newReader(t, s3.URL, stateread.Grant{Bucket: "acme-state", Prefix: "stacks/prod/"})
	tests := []struct {
		name string
		loc  stateread.Location
		want error
	}{
		{"another bucket", stateread.Location{Bucket: "acme-other", Key: acme.Key}, stateread.ErrNotAllowed},
		{"outside the prefix", stateread.Location{Bucket: "acme-state", Key: "stacks/dev/vpc/terraform.tfstate"}, stateread.ErrNotAllowed},
		{"dot segment", stateread.Location{Bucket: "acme-state", Key: "stacks/prod/../dev/terraform.tfstate"}, stateread.ErrNotAllowed},
		{"no key", stateread.Location{Bucket: "acme-state"}, stateread.ErrNotAllowed},
		{"no bucket", stateread.Location{Key: acme.Key}, stateread.ErrNotAllowed},
		{"invalid region", stateread.Location{Bucket: "acme-state", Key: acme.Key, Region: "eu-west-1.evil.example"}, stateread.ErrUnreadable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.Read(t.Context(), tt.loc)
			require.ErrorIs(t, err, tt.want)
		})
	}
	regions, _ := s3.requests()
	assert.Empty(t, regions, "no request reached S3")
}

var sentinels = []error{
	stateread.ErrNotFound, stateread.ErrAccessDenied, stateread.ErrEncrypted, stateread.ErrUnsupported,
	stateread.ErrTooLarge, stateread.ErrNotAllowed, stateread.ErrUnreadable, stateread.ErrMalformed,
}

func TestReadMapsS3Errors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{"missing key", http.StatusNotFound, "NoSuchKey", stateread.ErrNotFound},
		{"missing bucket", http.StatusNotFound, "NoSuchBucket", stateread.ErrNotFound},
		{"denied", http.StatusForbidden, "AccessDenied", stateread.ErrAccessDenied},
		{"KMS denied", http.StatusForbidden, "KMS.AccessDeniedException", stateread.ErrAccessDenied},
		{"invalid request", http.StatusBadRequest, "InvalidRequest", stateread.ErrUnreadable},
		{"conflict", http.StatusConflict, "OperationAborted", stateread.ErrUnreadable},
		{"moved without a region", http.StatusMovedPermanently, "PermanentRedirect", stateread.ErrUnreadable},
		{"server error that is not retried", http.StatusNotImplemented, "NotImplemented", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s3 := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ string) { s3Error(w, tt.status, tt.code) })
			_, err := newReader(t, s3.URL).Read(t.Context(), acme)
			require.Error(t, err)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
				return
			}
			for _, sentinel := range sentinels {
				assert.NotErrorIs(t, err, sentinel, "callers answer 503 for errors that may pass")
			}
		})
	}
}

func TestReadMapsStateErrors(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  error
	}{
		{"encrypted", string(fixture(t, "tofu-encrypted.tfstate")), stateread.ErrEncrypted},
		{"version 3", `{"version": 3, "modules": []}`, stateread.ErrUnsupported},
		{"malformed", `{"version": 4, "outputs": {`, stateread.ErrMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s3 := newFakeS3(t, serveState([]byte(tt.state)))
			_, err := newReader(t, s3.URL).Read(t.Context(), acme)
			require.ErrorIs(t, err, tt.want)
			assert.Contains(t, err.Error(), "s3://acme-state/stacks/prod/vpc/terraform.tfstate")
		})
	}
}

func TestReadFollowsTheBucketRegionOnce(t *testing.T) {
	state := fixture(t, "tofu.tfstate")
	moved := func(to string) func(http.ResponseWriter, *http.Request, string) {
		return func(w http.ResponseWriter, r *http.Request, region string) {
			if region == "ap-southeast-2" {
				serveState(state)(w, r, region)
				return
			}
			w.Header().Set("X-Amz-Bucket-Region", to)
			s3Error(w, http.StatusMovedPermanently, "PermanentRedirect")
		}
	}
	tests := []struct {
		name    string
		serve   func(http.ResponseWriter, *http.Request, string)
		region  string
		want    error
		regions []string
	}{
		{"moved", moved("ap-southeast-2"), "eu-west-1", nil, []string{"eu-west-1", "ap-southeast-2"}},
		{"no region in the location", moved("ap-southeast-2"), "", nil, []string{"us-east-1", "ap-southeast-2"}},
		{"moved again", moved("ca-central-1"), "eu-west-1", stateread.ErrUnreadable, []string{"eu-west-1", "ca-central-1"}},
		{"moved to the same region", moved("eu-west-1"), "eu-west-1", stateread.ErrUnreadable, []string{"eu-west-1"}},
		{"moved to an invalid region", moved("evil.example/"), "eu-west-1", stateread.ErrUnreadable, []string{"eu-west-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s3 := newFakeS3(t, tt.serve)
			loc := acme
			loc.Region = tt.region
			st, err := newReader(t, s3.URL).Read(t.Context(), loc)
			if tt.want == nil {
				require.NoError(t, err)
				assert.Equal(t, "1.12.6", st.TerraformVersion)
			} else {
				require.ErrorIs(t, err, tt.want)
			}
			regions, _ := s3.requests()
			assert.Equal(t, tt.regions, regions)
		})
	}
}

func blockUntilReleased(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	release := make(chan struct{})
	return release, sync.OnceFunc(func() { close(release) })
}

func TestReadStopsAtTheContextDeadline(t *testing.T) {
	released, release := blockUntilReleased(t)
	s3 := newFakeS3(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		select {
		case <-released:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := newReader(t, s3.URL).Read(ctx, acme)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	for _, sentinel := range sentinels {
		assert.NotErrorIs(t, err, sentinel)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadAppliesItsTimeout(t *testing.T) {
	s3 := newFakeS3(t, serveState(fixture(t, "terraform.tfstate")))
	deadlines := make(chan time.Time, 1)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if deadline, ok := req.Context().Deadline(); ok {
			select {
			case deadlines <- deadline:
			default:
			}
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	r, err := stateread.New(t.Context(), []stateread.Grant{{Bucket: "acme-state"}},
		stateread.WithEndpoint(s3.URL), stateread.WithRegion("us-east-1"),
		stateread.WithStaticCredentials("AKIDEXAMPLE", "secret"), stateread.WithHTTPClient(client))
	require.NoError(t, err)
	_, callerDeadline := t.Context().Deadline()
	require.False(t, callerDeadline, "the caller's context has no deadline")

	start := time.Now()
	_, err = r.Read(t.Context(), acme)
	end := time.Now()
	require.NoError(t, err)
	require.Len(t, deadlines, 1, "the request carries a deadline")
	assert.WithinRange(t, <-deadlines, start.Add(stateread.Timeout), end.Add(stateread.Timeout))
}

func TestReadRunsAtMostMaxConcurrentReads(t *testing.T) {
	state := fixture(t, "terraform.tfstate")
	released, release := blockUntilReleased(t)
	var inFlight, most atomic.Int32
	s3 := newFakeS3(t, func(w http.ResponseWriter, r *http.Request, region string) {
		n := inFlight.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		<-released
		inFlight.Add(-1)
		serveState(state)(w, r, region)
	})
	t.Cleanup(release)
	r := newReader(t, s3.URL)

	const reads = stateread.MaxConcurrent + 4
	errs := make(chan error, reads)
	for range reads {
		go func() {
			_, err := r.Read(t.Context(), acme)
			errs <- err
		}()
	}
	require.Eventually(t, func() bool { return inFlight.Load() == stateread.MaxConcurrent }, 10*time.Second, 5*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(stateread.MaxConcurrent), inFlight.Load(), "the other reads wait for a slot")
	release()
	for range reads {
		require.NoError(t, <-errs)
	}
	assert.Equal(t, int32(stateread.MaxConcurrent), most.Load())
	regions, _ := s3.requests()
	assert.Len(t, regions, reads)
}

func TestReadWaitsForASlotWithinTheContext(t *testing.T) {
	state := fixture(t, "terraform.tfstate")
	released, release := blockUntilReleased(t)
	var inFlight atomic.Int32
	s3 := newFakeS3(t, func(w http.ResponseWriter, r *http.Request, region string) {
		inFlight.Add(1)
		<-released
		serveState(state)(w, r, region)
	})
	t.Cleanup(release)
	r := newReader(t, s3.URL)
	done := make(chan struct{})
	for range stateread.MaxConcurrent {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = r.Read(t.Context(), acme)
		}()
	}
	require.Eventually(t, func() bool { return inFlight.Load() == stateread.MaxConcurrent }, 10*time.Second, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := r.Read(ctx, acme)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "wait to read")
	release()
	for range stateread.MaxConcurrent {
		<-done
	}
	assert.False(t, errors.Is(err, stateread.ErrUnreadable))
}
