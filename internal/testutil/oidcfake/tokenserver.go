package oidcfake

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stackorder/stackorder/internal/oidc"
)

const (
	// TokenRequestPath is the path of the emulated token request endpoint.
	TokenRequestPath = "/_apis/distributedtask/hubs/Actions/plans/fake/jobs/fake/idtoken"
	// DefaultRepository is the repository of the token server's default
	// claims.
	DefaultRepository = "acme/infra"
	// DefaultRepositoryID is the repository id of the default claims.
	DefaultRepositoryID = "700123"
	// DefaultPR is the pull request of the default claims.
	DefaultPR = 1
	// DefaultSHA is the commit of the default claims.
	DefaultSHA = "0123456789abcdef0123456789abcdef01234567"
)

// TokenServer emulates the endpoint a runner exposes as
// ACTIONS_ID_TOKEN_REQUEST_URL. Like the real one, its URL already carries
// an api-version query parameter, so clients must append
// "&audience=<aud>" rather than replace the query; a request without
// api-version is answered with 400. A GET with
// "Authorization: Bearer <ACTIONS_ID_TOKEN_REQUEST_TOKEN>" is answered with
// {"value": "<token>"}, a fresh token minted by the Issuer from Claims with
// aud set to the audience parameter, or to https://github.com/<owner> when
// it is absent, as GitHub does.
type TokenServer struct {
	issuer       *Issuer
	srv          *httptest.Server
	requestToken string

	mu        sync.Mutex
	claims    oidc.Claims
	opts      []TokenOption
	status    int
	audiences []string
}

// TokenRequestServer starts a token request endpoint that mints tokens
// from PlanClaims(DefaultRepository, DefaultRepositoryID, DefaultPR,
// DefaultSHA) until SetClaims is called, and registers its shutdown with
// t.Cleanup.
func (i *Issuer) TokenRequestServer(t testing.TB) *TokenServer {
	t.Helper()
	ts := &TokenServer{
		issuer:       i,
		requestToken: "fake-request-token-" + rand.Text(),
		claims:       i.PlanClaims(DefaultRepository, DefaultRepositoryID, DefaultPR, DefaultSHA),
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(ts.serve))
	t.Cleanup(ts.srv.Close)
	return ts
}

// URL is the value of ACTIONS_ID_TOKEN_REQUEST_URL.
func (ts *TokenServer) URL() string { return ts.srv.URL + TokenRequestPath + "?api-version=2.0" }

// RequestToken is the value of ACTIONS_ID_TOKEN_REQUEST_TOKEN.
func (ts *TokenServer) RequestToken() string { return ts.requestToken }

// Client returns an HTTP client for the server.
func (ts *TokenServer) Client() *http.Client { return ts.srv.Client() }

// SetClaims replaces the claims of subsequently minted tokens and the
// GITHUB_* values returned by Env. Registered claims left zero are filled
// in by Issuer.Token; aud is always taken from the request.
func (ts *TokenServer) SetClaims(c oidc.Claims, opts ...TokenOption) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.claims = c
	ts.opts = opts
}

// Claims returns the claims tokens are minted from.
func (ts *TokenServer) Claims() oidc.Claims {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.claims
}

// Fail makes every subsequent request fail with status, or restores normal
// operation when status is 0.
func (ts *TokenServer) Fail(status int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.status = status
}

// Audiences returns the aud of every token issued so far, in order.
func (ts *TokenServer) Audiences() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.audiences...)
}

// Env returns the variables a runner sets for a job with these claims:
// ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN pointing
// at this server, GITHUB_ACTIONS=true, and the GITHUB_* and RUNNER_*
// variables that mirror the claims.
func (ts *TokenServer) Env() map[string]string {
	c := ts.Claims()
	refName := c.Ref
	for _, prefix := range []string{"refs/heads/", "refs/tags/", "refs/pull/"} {
		refName = strings.TrimPrefix(refName, prefix)
	}
	return map[string]string{
		"ACTIONS_ID_TOKEN_REQUEST_URL":   ts.URL(),
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": ts.requestToken,
		"GITHUB_ACTIONS":                 "true",
		"GITHUB_SERVER_URL":              "https://github.com",
		"GITHUB_REPOSITORY":              c.Repository,
		"GITHUB_REPOSITORY_ID":           c.RepositoryID,
		"GITHUB_REPOSITORY_OWNER":        c.RepositoryOwner,
		"GITHUB_REPOSITORY_OWNER_ID":     c.RepositoryOwnerID,
		"GITHUB_RUN_ID":                  c.RunID,
		"GITHUB_RUN_NUMBER":              c.RunNumber,
		"GITHUB_RUN_ATTEMPT":             c.RunAttempt,
		"GITHUB_SHA":                     c.SHA,
		"GITHUB_REF":                     c.Ref,
		"GITHUB_REF_NAME":                refName,
		"GITHUB_REF_TYPE":                c.RefType,
		"GITHUB_BASE_REF":                c.BaseRef,
		"GITHUB_HEAD_REF":                c.HeadRef,
		"GITHUB_EVENT_NAME":              c.EventName,
		"GITHUB_WORKFLOW":                c.Workflow,
		"GITHUB_WORKFLOW_REF":            c.WorkflowRef,
		"GITHUB_WORKFLOW_SHA":            c.WorkflowSHA,
		"GITHUB_ACTOR":                   c.Actor,
		"GITHUB_ACTOR_ID":                c.ActorID,
		"RUNNER_ENVIRONMENT":             c.RunnerEnvironment,
	}
}

// Setenv sets every variable of Env with t.Setenv, which restores them
// when the test ends and cannot be used in parallel tests.
func (ts *TokenServer) Setenv(t testing.TB) {
	t.Helper()
	for k, v := range ts.Env() {
		t.Setenv(k, v)
	}
}

func (ts *TokenServer) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path != TokenRequestPath:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	case r.Method != http.MethodGet:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		return
	case r.URL.Query().Get("api-version") == "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "api-version is required"})
		return
	}
	if token, ok := oidc.BearerToken(r); !ok || token != ts.requestToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "bad request token"})
		return
	}
	ts.mu.Lock()
	claims, opts, status := ts.claims, ts.opts, ts.status
	ts.mu.Unlock()
	if status != 0 {
		writeJSON(w, status, map[string]string{"message": http.StatusText(status)})
		return
	}
	aud := r.URL.Query().Get("audience")
	if aud == "" {
		aud = "https://github.com/" + claims.RepositoryOwner
	}
	claims.Audience = jwt.ClaimStrings{aud}
	raw, err := ts.issuer.mint(claims, opts...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	ts.mu.Lock()
	ts.audiences = append(ts.audiences, aud)
	ts.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"value": raw})
}
