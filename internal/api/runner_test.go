package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/testutil/oidcfake"
)

const testSHA = "9b2f7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c"

type runnerEnv struct {
	*testEnv
	issuer *oidcfake.Issuer
}

func newRunnerEnv(t *testing.T, mutate ...func(*Config, *Deps)) *runnerEnv {
	t.Helper()
	issuer := oidcfake.New(t)
	verifier, err := oidc.New(issuer.Config(testBaseURL))
	require.NoError(t, err)
	e := newEnv(t, append([]func(*Config, *Deps){func(_ *Config, d *Deps) { d.Verifier = verifier }}, mutate...)...)
	e.db.addRepo(100, 1, "acme", "acme/infra")
	return &runnerEnv{testEnv: e, issuer: issuer}
}

func (e *runnerEnv) planToken(opts ...oidcfake.TokenOption) string {
	return e.issuer.Token(e.issuer.PlanClaims("acme/infra", "100", 7, testSHA), opts...)
}

func createRunBody() v1.CreateRunRequest {
	return v1.CreateRunRequest{Repo: "acme/infra", SHA: testSHA, PRNumber: 7, Mode: v1.ModePlan, Trigger: v1.TriggerPullRequest}
}

func TestRunnerAuthentication(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config, *Deps)
		prepare func(e *runnerEnv) string
		status  int
		message string
	}{
		{
			name:    "valid plan token",
			prepare: func(e *runnerEnv) string { return e.planToken() },
			status:  http.StatusCreated,
		},
		{
			name: "unknown repository",
			prepare: func(e *runnerEnv) string {
				return e.issuer.Token(e.issuer.PlanClaims("acme/unknown", "101", 7, testSHA))
			},
			status: http.StatusForbidden, message: `repository "acme/unknown" is not installed on this server`,
		},
		{
			name: "repository id mismatch",
			prepare: func(e *runnerEnv) string {
				return e.issuer.Token(e.issuer.PlanClaims("acme/infra", "999", 7, testSHA))
			},
			status: http.StatusForbidden, message: `repository_id "999" is not the id of acme/infra`,
		},
		{
			name: "suspended installation",
			prepare: func(e *runnerEnv) string {
				e.db.mu.Lock()
				e.db.repos[0].Suspended = true
				e.db.mu.Unlock()
				return e.planToken()
			},
			status: http.StatusForbidden, message: "the App installation of acme/infra is suspended",
		},
		{
			name: "expired",
			prepare: func(e *runnerEnv) string {
				tok := e.planToken()
				e.issuer.Advance(oidcfake.TokenLifetime + oidc.ClockSkew + time.Second)
				return tok
			},
			status: http.StatusUnauthorized, message: "token expired",
		},
		{
			name: "older than ten minutes",
			prepare: func(e *runnerEnv) string {
				tok := e.planToken(oidcfake.WithClaim("exp", e.issuer.Now().Add(time.Hour).Unix()))
				e.issuer.Advance(10*time.Minute + time.Second)
				return tok
			},
			status: http.StatusUnauthorized, message: "token too old",
		},
		{
			name:    "wrong audience",
			prepare: func(e *runnerEnv) string { return e.planToken(oidcfake.WithClaim("aud", "https://elsewhere.test")) },
			status:  http.StatusUnauthorized, message: "unexpected audience",
		},
		{
			name:    "bad signature",
			prepare: func(e *runnerEnv) string { return e.planToken(oidcfake.WithSigningKey(oidcfake.GenerateKey(t))) },
			status:  http.StatusUnauthorized, message: "invalid token signature",
		},
		{
			name:    "missing jti",
			prepare: func(e *runnerEnv) string { return e.planToken(oidcfake.WithoutClaim("jti")) },
			status:  http.StatusUnauthorized, message: "missing jti",
		},
		{
			name: "jwks unavailable",
			prepare: func(e *runnerEnv) string {
				e.issuer.FailJWKS(http.StatusBadGateway)
				return e.planToken()
			},
			status: http.StatusServiceUnavailable, message: "keys are unavailable",
		},
		{
			name:    "required workflow ref matches",
			mutate:  func(c *Config, _ *Deps) { c.RequiredWorkflowRef = oidc.DefaultWorkflowRefPattern },
			prepare: func(e *runnerEnv) string { return e.planToken() },
			status:  http.StatusCreated,
		},
		{
			name:   "required workflow ref mismatch",
			mutate: func(c *Config, _ *Deps) { c.RequiredWorkflowRef = oidc.DefaultWorkflowRefPattern },
			prepare: func(e *runnerEnv) string {
				return e.planToken(oidcfake.WithClaim("job_workflow_ref", "acme/infra/.github/workflows/plan.yml@refs/heads/main"))
			},
			status: http.StatusForbidden, message: "claim job_workflow_ref",
		},
		{
			name:    "no verifier",
			mutate:  func(_ *Config, d *Deps) { d.Verifier = nil },
			prepare: func(e *runnerEnv) string { return e.planToken() },
			status:  http.StatusServiceUnavailable, message: "no OIDC verifier is configured",
		},
		{
			name:    "api key",
			prepare: func(e *runnerEnv) string { return e.apiKey("laptop") },
			status:  http.StatusCreated,
		},
		{
			name:    "unknown api key",
			prepare: func(*runnerEnv) string { return "sk_unknown" },
			status:  http.StatusUnauthorized, message: "the API key is unknown or revoked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mutate []func(*Config, *Deps)
			if tc.mutate != nil {
				mutate = append(mutate, tc.mutate)
			}
			e := newRunnerEnv(t, mutate...)
			token := tc.prepare(e)
			rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), token))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.message != "" {
				assert.Contains(t, errorOf(t, rec).Message, tc.message)
				assert.Zero(t, e.runs.count(), "the run service is not called")
				return
			}
			call := e.runs.last()
			assert.Equal(t, "CreateRun", call.method)
			assert.Equal(t, createRunBody(), call.body)
		})
	}
}

func TestRunnerPrincipals(t *testing.T) {
	e := newRunnerEnv(t)
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), e.planToken()))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	p := e.runs.last().p
	assert.Equal(t, principal.OIDC, p.Kind)
	assert.Equal(t, oidcfake.Actor, p.Login)
	require.NotNil(t, p.Claims)
	assert.Equal(t, "acme/infra", p.Claims.Repository)
	assert.Equal(t, "refs/pull/7/merge", p.Claims.Ref)
	assert.Contains(t, e.logs.String(), `"principal":"oidc"`)

	key := e.apiKey("laptop")
	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), key))
	require.Equal(t, http.StatusCreated, rec.Code)
	p = e.runs.last().p
	assert.Equal(t, principal.APIKey, p.Kind)
	assert.Equal(t, "laptop", p.Login)
	assert.NotEmpty(t, p.APIKeyID)
	assert.Nil(t, p.Claims)
}

func TestRunnerTokensAreAcceptedOnce(t *testing.T) {
	e := newRunnerEnv(t)
	token := e.planToken()
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), token))
	require.Equal(t, http.StatusCreated, rec.Code)

	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), token))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, errorOf(t, rec).Message, "token replayed")
	assert.Equal(t, 1, e.runs.count())
}

func TestRunnerEndpointsRefuseSessions(t *testing.T) {
	e := newRunnerEnv(t)
	cookie := e.session("octocat", "acme")
	runID := uuid.NewString()
	for _, target := range []string{
		"/v1/runs",
		"/v1/runs/" + runID + "/graph",
		"/v1/runs/" + runID + "/stacks/a/result",
		"/v1/runs/" + runID + "/stacks/a/checks/policy",
	} {
		rec := e.do(withCookie(newRequest(t, http.MethodPost, target, map[string]any{}), cookie))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, target)
		assert.Equal(t, "a runner OIDC token or an API key is required", errorOf(t, rec).Message, target)
	}
	assert.Zero(t, e.runs.count())
}

func TestRunnerEndpointsDelegate(t *testing.T) {
	e := newRunnerEnv(t)
	runID := uuid.NewString()
	key := "stacks/prod/apps:blue"
	escaped := url.PathEscape(key)
	require.Equal(t, "stacks%2Fprod%2Fapps:blue", escaped)

	graphReq := v1.GraphUploadRequest{
		Graph:        v1.Graph{Repo: "acme/infra", SHA: testSHA, Stacks: []v1.Stack{{Key: key, Path: "stacks/prod/apps", Workspace: "blue"}}},
		ChangedPaths: []string{"stacks/prod/apps/main.tf"},
	}
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs/"+runID+"/graph", graphReq), e.planToken()))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, runID, decodeBody[v1.ResolveResponse](t, rec).RunID)
	call := e.runs.last()
	assert.Equal(t, "UploadGraph", call.method)
	assert.Equal(t, runID, call.runID)
	assert.Equal(t, graphReq, call.body)
	assert.Equal(t, principal.OIDC, call.p.Kind)

	result := v1.StackResult{Mode: v1.ModePlan, Status: v1.ResultSuccess, HasChanges: true, Summary: &v1.PlanSummary{Adds: 1}}
	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs/"+runID+"/stacks/"+escaped+"/result", result), e.planToken()))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, key, decodeBody[v1.RunStack](t, rec).Key)
	call = e.runs.last()
	assert.Equal(t, "RecordResult", call.method)
	assert.Equal(t, runID, call.runID)
	assert.Equal(t, key, call.stackKey, "the %2F-escaped key is decoded")
	assert.Equal(t, result, call.body)

	verdict := v1.CheckVerdict{Status: v1.CheckFail, Summary: "2 violations"}
	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs/"+runID+"/stacks/stacks%2Fprod%2Fvpc/checks/cost-estimate", verdict), e.planToken()))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, v1.Check{Name: "cost-estimate", Status: v1.CheckFail}, decodeBody[v1.Check](t, rec))
	call = e.runs.last()
	assert.Equal(t, "RecordCheck", call.method)
	assert.Equal(t, "stacks/prod/vpc", call.stackKey)
	assert.Equal(t, "cost-estimate", call.name)
	assert.Equal(t, verdict, call.body)

	upper := strings.ToUpper(runID)
	rec = e.do(bearer(newRequest(t, http.MethodGet, "/v1/runs/"+upper, nil), e.planToken()))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	run := decodeBody[v1.Run](t, rec)
	assert.Equal(t, runID, run.ID, "run ids are passed on in canonical form")
	assert.Equal(t, testBaseURL+"/runs/"+runID, run.HTMLURL)
	call = e.runs.last()
	assert.Equal(t, "GetRunForPrincipal", call.method)
	assert.Equal(t, principal.OIDC, call.p.Kind)

	e.runs.run = &v1.Run{ID: runID, Repo: "globex/platform"}
	rec = e.do(bearer(newRequest(t, http.MethodGet, "/v1/runs/"+runID, nil), e.planToken()))
	assert.Equal(t, http.StatusNotFound, rec.Code, "a runner token only reads runs of its own repository")
}

func TestCreateRunStatus(t *testing.T) {
	e := newRunnerEnv(t)
	key := e.apiKey("ci")
	e.runs.createResp = &v1.CreateRunResponse{RunID: "r1", Status: v1.RunPlanning, Existing: true, Run: &v1.Run{ID: "r1"}}
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), key))
	require.Equal(t, http.StatusOK, rec.Code, "an existing run is 200")
	resp := decodeBody[v1.CreateRunResponse](t, rec)
	assert.True(t, resp.Existing)
	assert.Equal(t, testBaseURL+"/runs/r1", resp.Run.HTMLURL)
}

func TestRunnerRequestValidation(t *testing.T) {
	e := newRunnerEnv(t)
	key := e.apiKey("ci")
	cases := []struct {
		name   string
		target string
		body   any
		ctype  string
		status int
	}{
		{name: "malformed run id", target: "/v1/runs/not-a-uuid/graph", body: v1.GraphUploadRequest{}, status: 404},
		{name: "malformed run id on result", target: "/v1/runs/42/stacks/a/result", body: v1.StackResult{}, status: 404},
		{name: "empty body", target: "/v1/runs", status: 400},
		{name: "not json", target: "/v1/runs", body: "mode=plan", ctype: "application/x-www-form-urlencoded", status: 400},
		{name: "result over 1 MB", target: "/v1/runs/" + uuid.NewString() + "/stacks/a/result", body: v1.StackResult{PlanText: strings.Repeat("x", maxBodyBytes)}, status: 413},
		{name: "graph over 8 MB", target: "/v1/runs/" + uuid.NewString() + "/graph", body: v1.GraphUploadRequest{ChangedPaths: []string{strings.Repeat("x", maxGraphBytes)}}, status: 413},
		{name: "graph over 1 MB is fine", target: "/v1/runs/" + uuid.NewString() + "/graph", body: v1.GraphUploadRequest{ChangedPaths: []string{strings.Repeat("x", 2*maxBodyBytes)}}, status: 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := e.runs.count()
			r := bearer(newRequest(t, http.MethodPost, tc.target, tc.body), key)
			if tc.ctype != "" {
				r.Header.Set("Content-Type", tc.ctype)
			}
			rec := e.do(r)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status >= 400 {
				assert.Equal(t, before, e.runs.count(), "the run service is not called")
			}
		})
	}
}

func TestRunnerErrorsAreMapped(t *testing.T) {
	e := newRunnerEnv(t)
	key := e.apiKey("ci")
	e.runs.err = &principal.LockedError{Conflicts: []v1.LockInfo{{StackKey: "stacks/prod/vpc", PRNumber: 41, RunID: "r0"}}}
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs", createRunBody()), key))
	require.Equal(t, http.StatusLocked, rec.Code)
	body := errorOf(t, rec)
	assert.Equal(t, codeLocked, body.Code)
	assert.Equal(t, map[string]any{"conflicts": []any{map[string]any{
		"stack_key": "stacks/prod/vpc", "pr_number": float64(41), "run_id": "r0", "taken_at": "0001-01-01T00:00:00Z",
	}}}, body.Details)

	e.runs.err = &oidc.ErrBinding{Reason: "environment", Want: "production", Got: "default"}
	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs/"+uuid.NewString()+"/stacks/a/result", v1.StackResult{}), key))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGetRunVisibility(t *testing.T) {
	e := newRunnerEnv(t)
	e.db.addRepo(200, 2, "globex", "globex/platform")
	runID := uuid.NewString()

	rec := e.do(withCookie(newRequest(t, http.MethodGet, "/v1/runs/"+runID, nil), e.session("octocat", "acme")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, principal.Session, e.runs.last().p.Kind)
	assert.Equal(t, "octocat", e.runs.last().p.Login)

	e.runs.run = &v1.Run{ID: runID, Repo: "globex/platform"}
	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/v1/runs/"+runID, nil), e.session("octocat", "acme")))
	assert.Equal(t, http.StatusNotFound, rec.Code, "a run in another organisation is invisible")

	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/v1/runs/"+runID, nil), e.session("globex")))
	assert.Equal(t, http.StatusOK, rec.Code, "an account installed on the user's own login is visible")

	rec = e.do(bearer(newRequest(t, http.MethodGet, "/v1/runs/"+runID, nil), e.apiKey("ci")))
	assert.Equal(t, http.StatusOK, rec.Code, "API keys see everything")

	e.runs.run = &v1.Run{ID: runID, Repo: "gone/repo"}
	rec = e.do(withCookie(newRequest(t, http.MethodGet, "/v1/runs/"+runID, nil), e.session("octocat", "acme", "gone")))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = e.do(newRequest(t, http.MethodGet, "/v1/runs/"+runID, nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "a runner OIDC token, an API key or a session is required", errorOf(t, rec).Message)
}
