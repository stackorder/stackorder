package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const testRunID = "5b1e3f0a-8c2d-4c1b-9a51-0d7f3e2a9b11"

type recorded struct {
	Method      string
	Pattern     string
	EscapedPath string
	Values      map[string]string
	Auth        string
	ContentType string
	Body        string
}

type apiServer struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []recorded
}

var routes = []string{
	"POST /v1/runs",
	"POST /v1/runs/{id}/graph",
	"POST /v1/runs/{id}/stacks/{key}/result",
	"POST /v1/runs/{id}/stacks/{key}/checks/{name}",
	"GET /v1/runs/{id}",
	"POST /v1/unlock",
	"GET /v1/me",
	"GET /healthz",
}

func newAPIServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *apiServer {
	t.Helper()
	s := &apiServer{}
	mux := http.NewServeMux()
	for _, pattern := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			values := map[string]string{}
			for _, name := range []string{"id", "key", "name"} {
				if v := r.PathValue(name); v != "" {
					values[name] = v
				}
			}
			s.mu.Lock()
			s.got = append(s.got, recorded{
				Method:      r.Method,
				Pattern:     r.Pattern,
				EscapedPath: r.URL.EscapedPath(),
				Values:      values,
				Auth:        r.Header.Get("Authorization"),
				ContentType: r.Header.Get("Content-Type"),
				Body:        string(body),
			})
			s.mu.Unlock()
			respond(w, r)
		})
	}
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *apiServer) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.got...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fastClient(base string, ts TokenSource, opts ...Option) *Client {
	return New(base, ts, append([]Option{WithBackoff(time.Millisecond)}, opts...)...)
}

func TestMethods(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	exit := 2
	tests := []struct {
		name        string
		call        func(ctx context.Context, c *Client) (any, error)
		response    any
		wantPattern string
		wantPath    string
		wantValues  map[string]string
		wantBody    any
		want        any
	}{
		{
			name: "CreateRun",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.CreateRun(ctx, v1.CreateRunRequest{Repo: "acme/infra", SHA: "abc123", PRNumber: 7, Mode: v1.ModePlan, Trigger: v1.TriggerPullRequest, WorkflowRunID: 42, Attempt: 1})
			},
			response:    v1.CreateRunResponse{RunID: testRunID, Status: v1.RunPending},
			wantPattern: "POST /v1/runs",
			wantPath:    "/v1/runs",
			wantValues:  map[string]string{},
			wantBody:    v1.CreateRunRequest{Repo: "acme/infra", SHA: "abc123", PRNumber: 7, Mode: v1.ModePlan, Trigger: v1.TriggerPullRequest, WorkflowRunID: 42, Attempt: 1},
			want:        &v1.CreateRunResponse{RunID: testRunID, Status: v1.RunPending},
		},
		{
			name: "UploadGraph",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.UploadGraph(ctx, testRunID, v1.GraphUploadRequest{
					Graph:        v1.Graph{Repo: "acme/infra", SHA: "abc123", Stacks: []v1.Stack{{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc"}}},
					ChangedPaths: []string{"stacks/prod/vpc/main.tf"},
				})
			},
			response: v1.ResolveResponse{
				RunID:    testRunID,
				Affected: []v1.AffectedStack{{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Reasons: []v1.Reason{v1.ReasonChanged}}},
				Waves:    [][]string{{"stacks/prod/vpc"}},
				Matrix:   v1.Matrix{Include: []v1.MatrixEntry{{Stack: "stacks/prod/vpc", Key: "stacks/prod/vpc", Environment: v1.DefaultEnvironment}}},
			},
			wantPattern: "POST /v1/runs/{id}/graph",
			wantPath:    "/v1/runs/" + testRunID + "/graph",
			wantValues:  map[string]string{"id": testRunID},
			wantBody: v1.GraphUploadRequest{
				Graph:        v1.Graph{Repo: "acme/infra", SHA: "abc123", Stacks: []v1.Stack{{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc"}}},
				ChangedPaths: []string{"stacks/prod/vpc/main.tf"},
			},
			want: &v1.ResolveResponse{
				RunID:    testRunID,
				Affected: []v1.AffectedStack{{Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Reasons: []v1.Reason{v1.ReasonChanged}}},
				Waves:    [][]string{{"stacks/prod/vpc"}},
				Matrix:   v1.Matrix{Include: []v1.MatrixEntry{{Stack: "stacks/prod/vpc", Key: "stacks/prod/vpc", Environment: v1.DefaultEnvironment}}},
			},
		},
		{
			name: "PostResult",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.PostResult(ctx, testRunID, "stacks/prod/vpc:blue", v1.StackResult{Mode: v1.ModePlan, Status: v1.ResultSuccess, ExitCode: 2, HasChanges: true, Summary: &v1.PlanSummary{Adds: 1, Added: []string{"aws_vpc.main"}}})
			},
			response:    v1.RunStack{StackID: "s-1", Key: "stacks/prod/vpc:blue", Path: "stacks/prod/vpc", Workspace: "blue", Status: v1.StackPlanned, ExitCode: &exit},
			wantPattern: "POST /v1/runs/{id}/stacks/{key}/result",
			wantPath:    "/v1/runs/" + testRunID + "/stacks/stacks%2Fprod%2Fvpc:blue/result",
			wantValues:  map[string]string{"id": testRunID, "key": "stacks/prod/vpc:blue"},
			wantBody:    v1.StackResult{Mode: v1.ModePlan, Status: v1.ResultSuccess, ExitCode: 2, HasChanges: true, Summary: &v1.PlanSummary{Adds: 1, Added: []string{"aws_vpc.main"}}},
			want:        &v1.RunStack{StackID: "s-1", Key: "stacks/prod/vpc:blue", Path: "stacks/prod/vpc", Workspace: "blue", Status: v1.StackPlanned, ExitCode: &exit},
		},
		{
			name: "PostCheck",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.PostCheck(ctx, testRunID, "stacks/prod/vpc", "cost/infracost", v1.CheckVerdict{Status: v1.CheckWarn, Summary: "+$12/month"})
			},
			response:    v1.Check{Name: "cost/infracost", Status: v1.CheckWarn, Summary: "+$12/month", UpdatedAt: now},
			wantPattern: "POST /v1/runs/{id}/stacks/{key}/checks/{name}",
			wantPath:    "/v1/runs/" + testRunID + "/stacks/stacks%2Fprod%2Fvpc/checks/cost%2Finfracost",
			wantValues:  map[string]string{"id": testRunID, "key": "stacks/prod/vpc", "name": "cost/infracost"},
			wantBody:    v1.CheckVerdict{Status: v1.CheckWarn, Summary: "+$12/month"},
			want:        &v1.Check{Name: "cost/infracost", Status: v1.CheckWarn, Summary: "+$12/month", UpdatedAt: now},
		},
		{
			name: "GetRun",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.GetRun(ctx, testRunID)
			},
			response:    v1.Run{ID: testRunID, Repo: "acme/infra", SHA: "abc123", Trigger: v1.TriggerComment, Mode: v1.ModeApply, Status: v1.RunApplying, CreatedAt: now, Waves: 2, CurrentWave: 1},
			wantPattern: "GET /v1/runs/{id}",
			wantPath:    "/v1/runs/" + testRunID,
			wantValues:  map[string]string{"id": testRunID},
			want:        &v1.Run{ID: testRunID, Repo: "acme/infra", SHA: "abc123", Trigger: v1.TriggerComment, Mode: v1.ModeApply, Status: v1.RunApplying, CreatedAt: now, Waves: 2, CurrentWave: 1},
		},
		{
			name: "Unlock",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.Unlock(ctx, v1.UnlockRequest{Repo: "acme/infra", StackKey: "stacks/prod/vpc", Reason: "abandoned PR", ForceState: true})
			},
			response:    v1.UnlockResponse{Released: []v1.LockInfo{{StackKey: "stacks/prod/vpc", RunID: testRunID, PRNumber: 7, TakenAt: now}}},
			wantPattern: "POST /v1/unlock",
			wantPath:    "/v1/unlock",
			wantValues:  map[string]string{},
			wantBody:    v1.UnlockRequest{Repo: "acme/infra", StackKey: "stacks/prod/vpc", Reason: "abandoned PR", ForceState: true},
			want:        &v1.UnlockResponse{Released: []v1.LockInfo{{StackKey: "stacks/prod/vpc", RunID: testRunID, PRNumber: 7, TakenAt: now}}},
		},
		{
			name: "Whoami",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.Whoami(ctx)
			},
			response:    v1.Whoami{Login: "octocat", Orgs: []string{"acme"}},
			wantPattern: "GET /v1/me",
			wantPath:    "/v1/me",
			wantValues:  map[string]string{},
			want:        &v1.Whoami{Login: "octocat", Orgs: []string{"acme"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, tt.response)
			})
			c := fastClient(s.srv.URL, StaticTokenSource("tok"))
			got, err := tt.call(context.Background(), c)
			require.NoError(t, err)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("response mismatch (-want +got):\n%s", diff)
			}
			reqs := s.requests()
			require.Len(t, reqs, 1)
			r := reqs[0]
			assert.Equal(t, tt.wantPattern, r.Pattern)
			assert.Equal(t, tt.wantPath, r.EscapedPath)
			assert.Equal(t, tt.wantValues, r.Values)
			assert.Equal(t, "Bearer tok", r.Auth)
			if tt.wantBody == nil {
				assert.Empty(t, r.Body)
				assert.Empty(t, r.ContentType)
				return
			}
			assert.Equal(t, "application/json", r.ContentType)
			want, err := json.Marshal(tt.wantBody)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), r.Body)
		})
	}
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "ok", status: http.StatusOK},
		{name: "no content", status: http.StatusNoContent},
		{name: "not ready", status: http.StatusServiceUnavailable, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("ok"))
			})
			ts := &countingSource{}
			err := fastClient(s.srv.URL, ts, WithRetries(0)).Healthz(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, IsUnreachable(err))
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, int32(0), ts.calls.Load(), "healthz must not request a token")
			for _, r := range s.requests() {
				assert.Empty(t, r.Auth)
			}
		})
	}
}

func TestErrorResponses(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantIs       []error
		wantNot      []error
		wantRequests int
	}{
		{name: "unauthorized", status: 401, body: `{"code":"unauthorized","message":"bad token"}`, wantIs: []error{ErrUnauthorized}, wantNot: []error{ErrRefused, ErrUnreachable}, wantRequests: 1},
		{name: "forbidden", status: 403, body: `{"code":"forbidden","message":"wrong environment"}`, wantIs: []error{ErrForbidden, ErrRefused}, wantNot: []error{ErrUnreachable}, wantRequests: 1},
		{name: "not found", status: 404, body: `{"code":"not_found","message":"no such run"}`, wantIs: []error{ErrNotFound}, wantNot: []error{ErrRefused, ErrUnreachable}, wantRequests: 1},
		{name: "conflict", status: 409, body: `{"code":"conflict","message":"head moved"}`, wantIs: []error{ErrConflict, ErrRefused}, wantNot: []error{ErrUnreachable}, wantRequests: 1},
		{name: "locked", status: 423, body: `{"code":"locked","message":"held by #3"}`, wantIs: []error{ErrLocked, ErrRefused}, wantNot: []error{ErrUnreachable}, wantRequests: 1},
		{name: "unconfirmed", status: 409, body: `{"code":"unconfirmed","message":"plan unconfirmed"}`, wantIs: []error{ErrRefused}, wantNot: []error{ErrLocked, ErrUnreachable}, wantRequests: 1},
		{name: "invalid", status: 400, body: `{"code":"invalid","message":"sha is required"}`, wantIs: []error{ErrInvalid}, wantNot: []error{ErrRefused, ErrUnreachable}, wantRequests: 1},
		{name: "internal after retries", status: 500, body: `{"code":"internal","message":"db down"}`, wantIs: []error{ErrUnreachable}, wantNot: []error{ErrRefused}, wantRequests: 3},
		{name: "gateway after retries", status: 504, body: "", wantIs: []error{ErrUnreachable}, wantNot: []error{ErrRefused}, wantRequests: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, err := fastClient(s.srv.URL, StaticTokenSource("tok"), WithRetries(2)).GetRun(context.Background(), testRunID)
			require.Error(t, err)
			for _, want := range tt.wantIs {
				assert.ErrorIs(t, err, want)
			}
			for _, not := range tt.wantNot {
				assert.NotErrorIs(t, err, not)
			}
			var apiErr *Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.status, apiErr.Status)
			assert.Len(t, s.requests(), tt.wantRequests)
			assert.True(t, strings.HasPrefix(err.Error(), "client: GET /v1/runs/"+testRunID+": "), err.Error())
		})
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		switch n.Add(1) {
		case 1:
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		case 3:
			writeJSON(w, http.StatusServiceUnavailable, v1.Error{Code: CodeInternal, Message: "starting"})
		default:
			writeJSON(w, http.StatusOK, v1.CreateRunResponse{RunID: testRunID, Existing: true})
		}
	})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ts := &countingSource{}
	got, err := fastClient(s.srv.URL, ts, WithLogger(logger)).CreateRun(context.Background(), v1.CreateRunRequest{Repo: "acme/infra", SHA: "abc"})
	require.NoError(t, err)
	assert.Equal(t, testRunID, got.RunID)
	assert.True(t, got.Existing)
	assert.Equal(t, int32(4), n.Load(), "three retries after the first attempt")
	assert.Equal(t, int32(4), ts.calls.Load(), "each attempt asks for a token")
	for _, r := range s.requests() {
		assert.JSONEq(t, `{"repo":"acme/infra","sha":"abc","mode":""}`, r.Body, "every attempt resends the body")
	}
	assert.Equal(t, 3, strings.Count(logs.String(), "retrying server request"))
}

func TestRetriesExhausted(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		writeJSON(w, http.StatusServiceUnavailable, v1.Error{Code: CodeInternal, Message: "overloaded"})
	})
	_, err := fastClient(s.srv.URL, nil).Whoami(context.Background())
	require.Error(t, err)
	assert.True(t, IsUnreachable(err))
	assert.Equal(t, int32(DefaultRetries+1), n.Load())
	assert.Contains(t, err.Error(), "4 attempts")
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "overloaded", apiErr.Message)
}

func TestUnreachableConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	_, err := fastClient(base, StaticTokenSource("tok"), WithRetries(1)).PostResult(context.Background(), testRunID, "stacks/a", v1.StackResult{})
	require.Error(t, err)
	assert.True(t, IsUnreachable(err))
	assert.Contains(t, err.Error(), "2 attempts")
}

func TestUnreachableTimeout(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(_ http.ResponseWriter, r *http.Request) {
		n.Add(1)
		<-r.Context().Done()
	})
	start := time.Now()
	_, err := fastClient(s.srv.URL, StaticTokenSource("tok"), WithRetries(1), WithTimeout(100*time.Millisecond)).GetRun(context.Background(), testRunID)
	require.Error(t, err)
	assert.True(t, IsUnreachable(err))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, int32(2), n.Load())
}

func TestCallerCancellationIsNotUnreachable(t *testing.T) {
	s := newAPIServer(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := fastClient(s.srv.URL, StaticTokenSource("tok")).GetRun(ctx, testRunID)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, IsUnreachable(err))
	assert.Len(t, s.requests(), 1)
}

func singleUseServer(t *testing.T, failFirst int) *apiServer {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	accepted := 0
	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.MapClaims
		_, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &claims)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, v1.Error{Code: CodeUnauthorized, Message: "malformed token"})
			return
		}
		id, _ := claims["jti"].(string)
		mu.Lock()
		replay := seen[id]
		seen[id] = true
		accepted++
		n := accepted
		mu.Unlock()
		switch {
		case replay:
			writeJSON(w, http.StatusUnauthorized, v1.Error{Code: CodeUnauthorized, Message: "jti already used"})
		case n <= failFirst:
			writeJSON(w, http.StatusServiceUnavailable, v1.Error{Code: CodeInternal, Message: "database unavailable"})
		default:
			writeJSON(w, http.StatusOK, v1.Whoami{Login: "runner"})
		}
	})
}

func TestSingleUseOIDCTokens(t *testing.T) {
	t.Run("every call carries a fresh token", func(t *testing.T) {
		tokens := newFakeTokenServer(t)
		s := singleUseServer(t, 0)
		c := fastClient(s.srv.URL, OIDCTokenSource(testAudience))
		for range 3 {
			got, err := c.Whoami(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "runner", got.Login)
		}
		assert.Equal(t, int32(3), tokens.fetches.Load())
		assert.Len(t, s.requests(), 3)
	})
	t.Run("retries after a 5xx carry a fresh token", func(t *testing.T) {
		tokens := newFakeTokenServer(t)
		s := singleUseServer(t, 2)
		got, err := fastClient(s.srv.URL, OIDCTokenSource(testAudience)).Whoami(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "runner", got.Login)
		assert.Equal(t, int32(3), tokens.fetches.Load())
		assert.Len(t, s.requests(), 3)
	})
	t.Run("a 5xx on every attempt stays unreachable", func(t *testing.T) {
		newFakeTokenServer(t)
		s := singleUseServer(t, 100)
		_, err := fastClient(s.srv.URL, OIDCTokenSource(testAudience)).Whoami(context.Background())
		require.Error(t, err)
		assert.True(t, IsUnreachable(err), "%v", err)
		assert.NotErrorIs(t, err, ErrUnauthorized)
		assert.Len(t, s.requests(), DefaultRetries+1)
	})
}

func TestUnauthorizedIsNotRetried(t *testing.T) {
	tests := []struct {
		name string
		ts   func(t *testing.T) TokenSource
	}{
		{name: "oidc", ts: func(t *testing.T) TokenSource { newFakeTokenServer(t); return OIDCTokenSource(testAudience) }},
		{name: "api key", ts: func(*testing.T) TokenSource { return APIKeyTokenSource("sk_test") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusUnauthorized, v1.Error{Code: CodeUnauthorized, Message: "no"})
			})
			_, err := fastClient(s.srv.URL, tt.ts(t)).Whoami(context.Background())
			require.ErrorIs(t, err, ErrUnauthorized)
			assert.False(t, IsUnreachable(err))
			assert.Len(t, s.requests(), 1)
		})
	}
}

func TestTokenSourceErrors(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantIs          error
		wantUnreachable bool
		wantCalls       int32
	}{
		{name: "no oidc is not retried", err: ErrNoOIDC, wantIs: ErrNoOIDC, wantCalls: 1},
		{name: "token endpoint down is retried", err: ErrUnreachable, wantIs: ErrUnreachable, wantUnreachable: true, wantCalls: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, v1.Whoami{}) })
			ts := &countingSource{err: tt.err}
			_, err := fastClient(s.srv.URL, ts, WithRetries(2)).Whoami(context.Background())
			require.ErrorIs(t, err, tt.wantIs)
			assert.Equal(t, tt.wantUnreachable, IsUnreachable(err))
			assert.Equal(t, tt.wantCalls, ts.calls.Load())
			assert.Empty(t, s.requests())
		})
	}
}

func TestRequestValidation(t *testing.T) {
	c := New("http://127.0.0.1:1", StaticTokenSource("tok"), WithRetries(0))
	ctx := context.Background()
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{name: "graph without run", call: func() error { _, err := c.UploadGraph(ctx, "", v1.GraphUploadRequest{}); return err }, want: "run id is required"},
		{name: "result without run", call: func() error { _, err := c.PostResult(ctx, "", "stacks/a", v1.StackResult{}); return err }, want: "run id is required"},
		{name: "result without stack", call: func() error { _, err := c.PostResult(ctx, testRunID, "", v1.StackResult{}); return err }, want: "stack key is required"},
		{name: "check without name", call: func() error { _, err := c.PostCheck(ctx, testRunID, "stacks/a", "", v1.CheckVerdict{}); return err }, want: "check name is required"},
		{name: "get without run", call: func() error { _, err := c.GetRun(ctx, ""); return err }, want: "run id is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.False(t, IsUnreachable(err))
		})
	}
}

func TestOptionsAndBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		base     func(srv string) string
		opts     []Option
		wantUA   string
		wantPath string
	}{
		{name: "defaults", base: func(s string) string { return s }, wantUA: "stackorder/dev", wantPath: "/v1/me"},
		{name: "trailing slash", base: func(s string) string { return s + "/" }, wantUA: "stackorder/dev", wantPath: "/v1/me"},
		{name: "path prefix and user agent", base: func(s string) string { return s + "/stackorder/" }, opts: []Option{WithUserAgent("stackorder/1.2.3 (ci)")}, wantUA: "stackorder/1.2.3 (ci)", wantPath: "/stackorder/v1/me"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotUA, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUA, gotPath = r.UserAgent(), r.URL.Path
				writeJSON(w, http.StatusOK, v1.Whoami{Login: "x"})
			}))
			defer srv.Close()
			var roundTrips atomic.Int32
			hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				roundTrips.Add(1)
				return http.DefaultTransport.RoundTrip(r)
			})}
			c := New(tt.base(srv.URL), nil, append([]Option{WithHTTPClient(hc)}, tt.opts...)...)
			assert.Equal(t, strings.TrimRight(tt.base(srv.URL), "/"), c.BaseURL())
			_, err := c.Whoami(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.wantUA, gotUA)
			assert.Equal(t, tt.wantPath, gotPath)
			assert.Equal(t, int32(1), roundTrips.Load())
		})
	}
}

func TestUndecodableSuccessBody(t *testing.T) {
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>captive portal</html>"))
	})
	_, err := fastClient(s.srv.URL, nil).GetRun(context.Background(), testRunID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding response")
	assert.False(t, IsUnreachable(err))
	assert.Len(t, s.requests(), 1)
}

func TestBackoffSchedule(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want []time.Duration
	}{
		{name: "default", want: []time.Duration{200 * time.Millisecond, 800 * time.Millisecond, 2 * time.Second, 2 * time.Second}},
		{name: "custom repeats last", opts: []Option{WithBackoff(time.Millisecond, 5*time.Millisecond)}, want: []time.Duration{time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}},
		{name: "empty keeps default", opts: []Option{WithBackoff()}, want: []time.Duration{200 * time.Millisecond, 800 * time.Millisecond, 2 * time.Second, 2 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New("http://example.invalid", nil, tt.opts...)
			got := make([]time.Duration, 0, len(tt.want))
			for i := range tt.want {
				got = append(got, c.delay(i))
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNegativeRetriesDisable(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	err := fastClient(s.srv.URL, nil, WithRetries(-5)).Healthz(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(1), n.Load())
}

type countingSource struct {
	calls atomic.Int32
	err   error
}

func (c *countingSource) Token(context.Context) (string, error) {
	c.calls.Add(1)
	if c.err != nil {
		return "", c.err
	}
	return "counted", nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
