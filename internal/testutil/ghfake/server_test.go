package ghfake_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

const repo = "acme/infra"

type result struct {
	status int
	header http.Header
	body   []byte
}

func (r result) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(r.body, &m), string(r.body))
	return m
}

func call(t *testing.T, fake *ghfake.Server, method, path, auth, body string) result {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, fake.URL()+path, rd)
	require.NoError(t, err)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := fake.HTTPClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return result{status: resp.StatusCode, header: resp.Header, body: data}
}

func signJWT(t *testing.T, fake *ghfake.Server, claims jwt.RegisteredClaims) string {
	t.Helper()
	key, err := gh.ParsePrivateKey(fake.AppKeyPEM())
	require.NoError(t, err)
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	require.NoError(t, err)
	return s
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	fake := ghfake.New(t)
	r := call(t, fake, http.MethodGet, "/nope", "", "")
	assert.Equal(t, http.StatusNotFound, r.status)
	assert.Equal(t, "Not Found", r.json(t)["message"])
	assert.Equal(t, "4999", r.header.Get("X-RateLimit-Remaining"))
}

func TestAuthentication(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	inst := "Bearer " + fake.InstallationToken(1)
	user := "token " + fake.UserToken("alice")
	now := time.Now()
	valid := "Bearer " + signJWT(t, fake, jwt.RegisteredClaims{Issuer: "1", IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute))})

	tests := []struct {
		name, method, path, auth string
		want                     int
		message                  string
	}{
		{"no auth on repo", "GET", "/repos/acme/infra", "", 401, "Requires authentication"},
		{"bad scheme", "GET", "/repos/acme/infra", "Basic abc", 401, "Bad credentials"},
		{"unknown token", "GET", "/repos/acme/infra", "Bearer ghs_forged", 401, "Bad credentials"},
		{"installation token", "GET", "/repos/acme/infra", inst, 200, ""},
		{"user token", "GET", "/repos/acme/infra", user, 200, ""},
		{"jwt on repo", "GET", "/repos/acme/infra", valid, 401, "Requires authentication"},
		{"jwt on app", "GET", "/app", valid, 200, ""},
		{"installation token on app", "GET", "/app", inst, 401, "JSON web token"},
		{"user token on installation repos", "GET", "/installation/repositories", user, 403, "installation access token"},
		{"no auth on installation repos", "GET", "/installation/repositories", "", 401, "Requires authentication"},
		{"installation token on user", "GET", "/user", inst, 403, "not accessible by integration"},
		{"no auth on user", "GET", "/user", "", 401, "Requires authentication"},
		{"garbage jwt", "GET", "/app", "Bearer a.b.c", 401, "could not be decoded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := call(t, fake, tt.method, tt.path, tt.auth, "")
			assert.Equal(t, tt.want, r.status, string(r.body))
			if tt.message != "" {
				assert.Contains(t, r.json(t)["message"], tt.message)
			}
		})
	}
}

func TestJWTClaimChecks(t *testing.T) {
	fake := ghfake.New(t)
	now := time.Now()
	date := jwt.NewNumericDate
	tests := []struct {
		name    string
		claims  jwt.RegisteredClaims
		message string
	}{
		{"wrong issuer", jwt.RegisteredClaims{Issuer: "2", IssuedAt: date(now), ExpiresAt: date(now.Add(time.Minute))}, "Issuer"},
		{"too far", jwt.RegisteredClaims{Issuer: "1", IssuedAt: date(now), ExpiresAt: date(now.Add(time.Hour))}, "too far in the future"},
		{"expired", jwt.RegisteredClaims{Issuer: "1", IssuedAt: date(now.Add(-time.Hour)), ExpiresAt: date(now.Add(-time.Minute))}, "could not be decoded"},
		{"missing expiry", jwt.RegisteredClaims{Issuer: "1", IssuedAt: date(now)}, "could not be decoded"},
		{"issued in the future", jwt.RegisteredClaims{Issuer: "1", IssuedAt: date(now.Add(time.Hour)), ExpiresAt: date(now.Add(2 * time.Minute))}, "could not be decoded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := call(t, fake, "GET", "/app", "Bearer "+signJWT(t, fake, tt.claims), "")
			assert.Equal(t, 401, r.status)
			assert.Contains(t, r.json(t)["message"], tt.message)
		})
	}
}

func TestUnverifiedJWTWithoutPublicKey(t *testing.T) {
	fake := ghfake.New(t)
	other := ghfake.New(t)
	now := time.Now()
	good := signJWT(t, other, jwt.RegisteredClaims{Issuer: "1", IssuedAt: date(now), ExpiresAt: date(now.Add(time.Minute))})
	assert.Equal(t, 200, call(t, fake, "GET", "/app", "Bearer "+good, "").status)
	noIat := signJWT(t, other, jwt.RegisteredClaims{Issuer: "1", ExpiresAt: date(now.Add(time.Minute))})
	assert.Equal(t, 401, call(t, fake, "GET", "/app", "Bearer "+noIat, "").status)
	expired := signJWT(t, other, jwt.RegisteredClaims{Issuer: "1", IssuedAt: date(now.Add(-time.Hour)), ExpiresAt: date(now.Add(-time.Minute))})
	assert.Equal(t, 401, call(t, fake, "GET", "/app", "Bearer "+expired, "").status)

	fake.SetApp(gh.AppInfo{ID: 99, Slug: "custom"})
	r := call(t, fake, "GET", "/app", "Bearer "+good, "")
	assert.Equal(t, 401, r.status)
	custom := signJWT(t, other, jwt.RegisteredClaims{Issuer: "99", IssuedAt: date(now), ExpiresAt: date(now.Add(time.Minute))})
	r = call(t, fake, "GET", "/app", "Bearer "+custom, "")
	require.Equal(t, 200, r.status)
	assert.Equal(t, "custom", r.json(t)["slug"])
}

func date(t time.Time) *jwt.NumericDate { return jwt.NewNumericDate(t) }

func TestInstallationTokensExpireAndRevoke(t *testing.T) {
	fake := ghfake.New(t)
	var mu sync.Mutex
	now := time.Now()
	fake.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	fake.AddInstallation(1, "acme", repo)
	tok := fake.InstallationToken(1)
	assert.Equal(t, "ghs_fake_1_1", tok)
	assert.Equal(t, 200, call(t, fake, "GET", "/repos/acme/infra", "Bearer "+tok, "").status)
	mu.Lock()
	now = now.Add(time.Hour)
	mu.Unlock()
	assert.Equal(t, 401, call(t, fake, "GET", "/repos/acme/infra", "Bearer "+tok, "").status)

	tok = fake.InstallationToken(1)
	assert.Equal(t, "ghs_fake_1_2", tok)
	user := fake.UserToken("alice")
	fake.RevokeTokens()
	assert.Equal(t, 401, call(t, fake, "GET", "/repos/acme/infra", "Bearer "+tok, "").status)
	assert.Equal(t, 401, call(t, fake, "GET", "/user", "Bearer "+user, "").status)
}

func TestRepositoriesOutsideAnyInstallationAreReachable(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	fake.SetRepo("public/lib", gh.Repository{Private: false})
	fake.AddInstallation(1, "acme", repo)
	r := call(t, fake, "GET", "/repos/public/lib", "Bearer "+fake.InstallationToken(1), "")
	require.Equal(t, 200, r.status)
	assert.Equal(t, "public/lib", r.json(t)["full_name"])
}

func TestPaginationLinks(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	fake.SetTags(repo, []gh.Tag{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}})
	tok := "Bearer " + fake.InstallationToken(1)

	r := call(t, fake, "GET", "/repos/acme/infra/tags?per_page=2&page=2", tok, "")
	require.Equal(t, 200, r.status)
	var tags []gh.Tag
	require.NoError(t, json.Unmarshal(r.body, &tags))
	assert.Equal(t, []gh.Tag{{Name: "c"}, {Name: "d"}}, tags)
	link := r.header.Get("Link")
	for _, want := range []string{`page=3&per_page=2>; rel="next"`, `page=3&per_page=2>; rel="last"`, `page=1&per_page=2>; rel="prev"`, `page=1&per_page=2>; rel="first"`} {
		assert.Contains(t, link, want)
	}
	assert.True(t, strings.HasPrefix(link, "<"+fake.URL()+"/repos/acme/infra/tags?"))

	r = call(t, fake, "GET", "/repos/acme/infra/tags?per_page=500&page=9", tok, "")
	require.NoError(t, json.Unmarshal(r.body, &tags))
	assert.Empty(t, tags)

	r = call(t, fake, "GET", "/repos/acme/infra/tags", tok, "")
	assert.Empty(t, r.header.Get("Link"))
	fake.SetPageSize(4)
	r = call(t, fake, "GET", "/repos/acme/infra/tags?per_page=100", tok, "")
	require.NoError(t, json.Unmarshal(r.body, &tags))
	assert.Len(t, tags, 4)
	assert.Contains(t, r.header.Get("Link"), `rel="next"`)
}

func TestFailureInjection(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	tok := "Bearer " + fake.InstallationToken(1)

	fake.FailNext("GET /repos/{owner}/{repo}", 503, 2)
	fake.FailNext("GET /repos/acme/infra/tags", 418, 1)
	assert.Equal(t, 418, call(t, fake, "GET", "/repos/acme/infra/tags", tok, "").status)
	assert.Equal(t, 200, call(t, fake, "GET", "/repos/acme/infra/tags", tok, "").status)
	assert.Equal(t, 503, call(t, fake, "GET", "/repos/acme/infra", tok, "").status)
	assert.Equal(t, 503, call(t, fake, "GET", "/repos/acme/infra", tok, "").status)
	assert.Equal(t, 200, call(t, fake, "GET", "/repos/acme/infra", tok, "").status)

	fake.FailNext("", 500, 1)
	assert.Equal(t, 500, call(t, fake, "GET", "/user", "", "").status)

	reset := time.Now().Add(time.Minute).Truncate(time.Second)
	fake.RateLimitNext(reset)
	r := call(t, fake, "GET", "/repos/acme/infra", tok, "")
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "0", r.header.Get("X-RateLimit-Remaining"))
	assert.Equal(t, fmt.Sprint(reset.Unix()), r.header.Get("X-RateLimit-Reset"))
	assert.Contains(t, r.json(t)["message"], "rate limit")

	fake.SecondaryRateLimitNext(3 * time.Second)
	r = call(t, fake, "GET", "/repos/acme/infra", tok, "")
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "3", r.header.Get("Retry-After"))

	fake.FailNext("GET /repos/{owner}/{repo}", 0, 1)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fake.URL()+"/repos/acme/infra", nil)
	require.NoError(t, err)
	fresh := &http.Client{Transport: &http.Transport{}}
	resp, err := fresh.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)

	reqs := fake.Requests()
	last := reqs[len(reqs)-1]
	assert.Equal(t, "GET /repos/{owner}/{repo}", last.Pattern)
	assert.Equal(t, "none", last.Auth)
	assert.Zero(t, last.Status)
}

func TestLatency(t *testing.T) {
	fake := ghfake.New(t)
	fake.SetLatency(60 * time.Millisecond)
	start := time.Now()
	call(t, fake, "GET", "/nope", "", "")
	assert.GreaterOrEqual(t, time.Since(start), 60*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fake.URL()+"/nope", nil)
	require.NoError(t, err)
	resp, err := fake.HTTPClient().Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
}

func TestRequestLog(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(7, "acme", repo)
	tok := fake.InstallationToken(7)
	call(t, fake, "POST", "/repos/acme/infra/issues/3/comments?x=1", "Bearer "+tok, `{"body":"hi"}`)
	reqs := fake.Requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, "POST", r.Method)
	assert.Equal(t, "/repos/acme/infra/issues/3/comments", r.Path)
	assert.Equal(t, "x=1", r.RawQuery)
	assert.Equal(t, "POST /repos/{owner}/{repo}/issues/{issue_number}/comments", r.Pattern)
	assert.Equal(t, "installation", r.Auth)
	assert.Equal(t, int64(7), r.InstallationID)
	assert.Equal(t, "stackorder-test[bot]", r.Login)
	assert.JSONEq(t, `{"body":"hi"}`, string(r.Body))
	assert.Equal(t, 201, r.Status)
}

func TestCheckRunValidation(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	tok := "Bearer " + fake.InstallationToken(1)
	tests := []struct {
		name, body, message string
	}{
		{"bad json", `{`, "Problems parsing JSON"},
		{"bad status", `{"name":"n","head_sha":"s","status":"done"}`, "status is not included"},
		{"bad conclusion", `{"name":"n","head_sha":"s","conclusion":"great"}`, "conclusion is not included"},
		{"completed without conclusion", `{"name":"n","head_sha":"s","status":"completed"}`, "conclusion is required"},
		{"output without title", `{"name":"n","head_sha":"s","output":{"summary":"x"}}`, "output.title is required"},
		{"output without summary", `{"name":"n","head_sha":"s","output":{"title":"x"}}`, "output.summary is required"},
		{"too many actions", `{"name":"n","head_sha":"s","actions":[{"label":"a","description":"a","identifier":"a"},{"label":"b","description":"b","identifier":"b"},{"label":"c","description":"c","identifier":"c"},{"label":"d","description":"d","identifier":"d"}]}`, "at most 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := call(t, fake, "POST", "/repos/acme/infra/check-runs", tok, tt.body)
			assert.Contains(t, string(r.body), tt.message)
			assert.GreaterOrEqual(t, r.status, 400)
		})
	}

	r := call(t, fake, "POST", "/repos/acme/infra/check-runs", tok, `{"name":"n","head_sha":"s","conclusion":"neutral"}`)
	require.Equal(t, 201, r.status)
	created := r.json(t)
	assert.Equal(t, "completed", created["status"])
	assert.NotEmpty(t, created["completed_at"])
	id := int64(created["id"].(float64))

	path := fmt.Sprintf("/repos/acme/infra/check-runs/%d", id)
	assert.Contains(t, string(call(t, fake, "PATCH", path, tok, `{"head_sha":"x"}`).body), "not a permitted key")
	assert.Equal(t, 400, call(t, fake, "PATCH", path, tok, `{`).status)
	assert.Equal(t, 400, call(t, fake, "PATCH", path, tok, `{"status":5}`).status)
	assert.Equal(t, 422, call(t, fake, "PATCH", path, tok, `{"status":"bogus"}`).status)
	r = call(t, fake, "PATCH", path, tok, `{"name":"renamed","details_url":"https://d","external_id":"e"}`)
	require.Equal(t, 200, r.status)
	assert.Equal(t, "renamed", r.json(t)["name"])
	assert.Equal(t, "neutral", r.json(t)["conclusion"])

	call(t, fake, "POST", "/repos/acme/infra/check-runs", tok, `{"name":"renamed","head_sha":"s"}`)
	var list struct {
		Total int           `json:"total_count"`
		Runs  []gh.CheckRun `json:"check_runs"`
	}
	require.NoError(t, json.Unmarshal(call(t, fake, "GET", "/repos/acme/infra/commits/s/check-runs", tok, "").body, &list))
	assert.Equal(t, 1, list.Total)
	require.NoError(t, json.Unmarshal(call(t, fake, "GET", "/repos/acme/infra/commits/s/check-runs?filter=all", tok, "").body, &list))
	assert.Equal(t, 2, list.Total)
	require.NoError(t, json.Unmarshal(call(t, fake, "GET", "/repos/acme/infra/commits/s/check-runs?filter=all&status=queued", tok, "").body, &list))
	assert.Equal(t, 1, list.Total)

	fake.SetRef(repo, "heads/feature/x", "s")
	require.NoError(t, json.Unmarshal(call(t, fake, "GET", "/repos/acme/infra/commits/feature%2Fx/check-runs?filter=all", tok, "").body, &list))
	assert.Equal(t, 2, list.Total)
	assert.Len(t, fake.CheckRuns("acme/missing"), 0)
}

func TestCommentAuthorAssociation(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	fake.SetOrgMembership("acme", "member", gh.MembershipActive, "member")
	fake.SetCollaboratorPermission(repo, "outside", "write")
	fake.SetCollaboratorPermission(repo, "none", "none")
	tests := []struct{ login, want string }{
		{"acme", "OWNER"},
		{"member", "MEMBER"},
		{"outside", "COLLABORATOR"},
		{"none", "NONE"},
		{"stranger", "NONE"},
	}
	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			assert.Equal(t, tt.want, fake.AddComment(repo, 1, tt.login, "hi").AuthorAssociation)
		})
	}

	r := call(t, fake, "POST", "/repos/acme/infra/issues/1/comments", "Bearer "+fake.UserToken("member"), `{"body":"from a user"}`)
	require.Equal(t, 201, r.status)
	assert.Equal(t, "MEMBER", r.json(t)["author_association"])
	tok := "Bearer " + fake.InstallationToken(1)
	assert.Equal(t, 422, call(t, fake, "POST", "/repos/acme/infra/issues/1/comments", tok, `{"body":""}`).status)
	assert.Equal(t, 400, call(t, fake, "POST", "/repos/acme/infra/issues/1/comments", tok, `{`).status)
	id := fake.Comments(repo, 1)[0].ID
	assert.Equal(t, 422, call(t, fake, "PATCH", fmt.Sprintf("/repos/acme/infra/issues/comments/%d", id), tok, `{"body":""}`).status)
	assert.Empty(t, fake.Comments("acme/missing", 1))
	fake.SetOrgMembership("acme", "member", "", "")
	assert.Equal(t, "NONE", fake.AddComment(repo, 1, "member", "x").AuthorAssociation)
}

func TestIssueValidation(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	tok := "Bearer " + fake.InstallationToken(1)
	r := call(t, fake, "POST", "/repos/acme/infra/issues", tok, `{"title":"t"}`)
	require.Equal(t, 201, r.status)
	assert.Equal(t, 422, call(t, fake, "PATCH", "/repos/acme/infra/issues/1", tok, `{"state":"merged"}`).status)
	assert.Empty(t, fake.Issues("acme/missing"))
}

func TestDispatchBehaviour(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	fake.SetTags(repo, []gh.Tag{{Name: "v1", SHA: "tagsha"}})
	tok := "Bearer " + fake.InstallationToken(1)

	r := call(t, fake, "POST", "/repos/acme/infra/actions/workflows/stackorder-run.yml/dispatches", tok, `{"ref":"v1","inputs":{"wave":2,"dry":true,"mode":"drift"}}`)
	require.Equal(t, 204, r.status, string(r.body))
	d := fake.Dispatches()[0]
	assert.Equal(t, map[string]string{"wave": "2", "dry": "true", "mode": "drift"}, d.Inputs)
	runs := fake.WorkflowRuns(repo)
	require.Len(t, runs, 1)
	assert.Equal(t, "tagsha", runs[0].HeadSHA)
	assert.Equal(t, "stackorder-run", runs[0].Name)
	assert.Equal(t, 1, runs[0].RunNumber)
	assert.Equal(t, d.RunID, runs[0].ID)

	assert.Equal(t, 422, call(t, fake, "POST", "/repos/acme/infra/actions/workflows/stackorder-run.yml/dispatches", tok, `{}`).status)
	assert.Equal(t, 400, call(t, fake, "POST", "/repos/acme/infra/actions/workflows/stackorder-run.yml/dispatches", tok, `nope`).status)
	assert.Equal(t, 404, call(t, fake, "POST", "/repos/acme/other/actions/workflows/x.yml/dispatches", tok, `{"ref":"main"}`).status)
	assert.Nil(t, fake.WorkflowRuns("acme/missing"))
}

func TestWorkflowRunHelpers(t *testing.T) {
	fake := ghfake.New(t)
	run := fake.AddWorkflowRun(repo, gh.WorkflowRun{Path: ".github/workflows/stackorder-plan.yaml", Event: "pull_request"})
	assert.Equal(t, "stackorder-plan", run.Name)
	assert.Equal(t, run.Name, run.DisplayTitle)
	assert.Equal(t, gh.RunStatusQueued, run.Status)

	started := fake.SetWorkflowRunStatus(repo, run.ID, gh.RunStatusInProgress)
	assert.False(t, started.RunStartedAt.IsZero())
	done := fake.CompleteWorkflowRun(repo, run.ID, gh.ConclusionCancelled)
	assert.Equal(t, gh.RunStatusCompleted, done.Status)
	assert.Equal(t, gh.ConclusionCancelled, done.Conclusion)

	replaced := fake.AddWorkflowRun(repo, gh.WorkflowRun{ID: run.ID, Path: run.Path, Status: gh.RunStatusQueued, RunAttempt: 2})
	assert.Equal(t, 2, replaced.RunAttempt)
	assert.Len(t, fake.WorkflowRuns(repo), 1)

	rec := &recorder{TB: t}
	strict := ghfake.New(rec)
	strict.CompleteWorkflowRun(repo, 1, "success")
	strict.SetWorkflowRunStatus("acme/unknown", 1, "queued")
	assert.Len(t, rec.errors, 2)
}

type recorder struct {
	testing.TB
	mu     sync.Mutex
	errors []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestContentsDirectoryAndRefs(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	fake.SetContents(repo, "", "stacks/a/main.tf", []byte("a"))
	fake.SetContents(repo, "", "stacks/b/main.tf", []byte("b"))
	tok := "Bearer " + fake.InstallationToken(1)
	r := call(t, fake, "GET", "/repos/acme/infra/contents/stacks", tok, "")
	require.Equal(t, 200, r.status)
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(r.body, &entries))
	require.Len(t, entries, 2)
	assert.Equal(t, "stacks/a/main.tf", entries[0]["path"])
	assert.Equal(t, "stacks/b/main.tf", entries[1]["path"])
	assert.Equal(t, 404, call(t, fake, "GET", "/repos/acme/infra/contents/", tok, "").status)
	assert.Equal(t, 404, call(t, fake, "GET", "/repos/acme/infra/contents/stacks/a/main.tf?ref=unknown", tok, "").status)

	fake.PushEvent(repo, "refs/heads/release", "relsha")
	fake.SetContents(repo, "release", "x.txt", []byte("x"))
	r = call(t, fake, "GET", "/repos/acme/infra/contents/x.txt?ref=relsha", tok, "")
	require.Equal(t, 200, r.status)
	assert.Equal(t, "file", r.json(t)["type"])
}

func TestReviewProtectionRuleRequiresApp(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	body := `{"environment_name":"production","state":"approved"}`
	r := call(t, fake, "POST", "/repos/acme/infra/actions/runs/5/deployment_protection_rule", "Bearer "+fake.UserToken("alice"), body)
	assert.Equal(t, 403, r.status)
	tok := "Bearer " + fake.InstallationToken(1)
	assert.Equal(t, 422, call(t, fake, "POST", "/repos/acme/infra/actions/runs/5/deployment_protection_rule", tok, `{"state":"approved"}`).status)
	assert.Equal(t, 204, call(t, fake, "POST", "/repos/acme/infra/actions/runs/5/deployment_protection_rule", tok, body).status)
}

func TestOAuthTokenJSONBody(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddOAuthCode("c1", "alice")
	r := call(t, fake, "POST", "/login/oauth/access_token", "", `{"client_id":"x","client_secret":"y","code":"c1"}`)
	require.Equal(t, 200, r.status)
	assert.Equal(t, "bearer", r.json(t)["token_type"])
	assert.Equal(t, 400, call(t, fake, "POST", "/login/oauth/access_token", "", `{`).status)
}

func TestAppHelpers(t *testing.T) {
	fake := ghfake.New(t)
	key, err := gh.ParsePrivateKey(fake.AppKeyPEM())
	require.NoError(t, err)
	cfg := fake.AppConfig()
	assert.True(t, key.Equal(cfg.PrivateKey))
	assert.Equal(t, int64(1), cfg.AppID)
	assert.Equal(t, fake.URL(), cfg.BaseURL)

	fake.AddInstallation(5, "acme", repo, "ACME/Infra")
	app := fake.NewApp()
	repos, err := app.InstallationRepos(context.Background(), 5)
	require.NoError(t, err)
	assert.Len(t, repos, 1)

	fake.SuspendInstallation(5)
	inst, err := app.Installation(context.Background(), 5)
	require.NoError(t, err)
	assert.NotNil(t, inst.SuspendedAt)

	r := call(t, fake, "GET", "/installation/repositories", "Bearer "+fake.InstallationToken(77), "")
	assert.Equal(t, 404, r.status)
}

func TestMatchCreatedFilters(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", repo)
	t0 := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	for i := range 3 {
		fake.AddWorkflowRun(repo, gh.WorkflowRun{Path: ".github/workflows/w.yml", CreatedAt: t0.Add(time.Duration(i) * time.Hour)})
	}
	fake.AddWorkflowRun(repo, gh.WorkflowRun{Path: ".github/workflows/w.yml", CreatedAt: t0.Add(24 * time.Hour)})
	tok := "Bearer " + fake.InstallationToken(1)
	tests := []struct {
		filter string
		want   int
	}{
		{">=2026-09-28T09:00:00Z", 3},
		{">2026-09-28T09:00:00Z", 2},
		{"<=2026-09-28T09:00:00Z", 2},
		{"<2026-09-28T09:00:00Z", 1},
		{"2026-09-28", 3},
		{">=2026-09-29", 1},
		{"garbage", 0},
		{">=garbage", 0},
	}
	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			var out struct {
				Total int `json:"total_count"`
			}
			q := "created=" + strings.NewReplacer(">", "%3E", "<", "%3C", "=", "%3D", ":", "%3A").Replace(tt.filter)
			require.NoError(t, json.Unmarshal(call(t, fake, "GET", "/repos/acme/infra/actions/runs?"+q, tok, "").body, &out))
			assert.Equal(t, tt.want, out.Total)
		})
	}
}

func TestEventBuildersRoundTrip(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(3, "acme", repo)
	fake.SetCollaboratorPermission(repo, "bob", "write")
	secret := []byte("whsec")

	pr := gh.PullRequest{Number: 12, HeadSHA: "h12", HeadRef: "feature", User: gh.User{Login: "alice"}}
	run := fake.AddWorkflowRun(repo, gh.WorkflowRun{Path: ".github/workflows/stackorder-run.yml", Event: "workflow_dispatch", HeadBranch: "main"})
	tests := []struct {
		event   string
		payload gh.Event
		check   func(t *testing.T, ev any)
	}{
		{gh.EventPullRequest, fake.PullRequestEvent("synchronize", repo, pr), func(t *testing.T, ev any) {
			e := ev.(*gh.PullRequestEvent)
			assert.Equal(t, 12, e.Number)
			assert.Equal(t, "h12", e.PullRequest.HeadSHA)
			assert.Equal(t, "h12", e.After)
			assert.Equal(t, "alice", e.Sender.Login)
			assert.False(t, e.PullRequest.IsFork())
		}},
		{gh.EventPullRequest, fake.PullRequestEvent("opened", repo, gh.PullRequest{Number: 13}), func(t *testing.T, ev any) {
			assert.Equal(t, "octocat", ev.(*gh.PullRequestEvent).Sender.Login)
		}},
		{gh.EventPullRequestReview, fake.PullRequestReviewEvent("submitted", repo, 12, gh.Review{User: gh.User{Login: "bob"}, State: gh.ReviewApproved, CommitID: "h12"}), func(t *testing.T, ev any) {
			e := ev.(*gh.PullRequestReviewEvent)
			assert.Equal(t, "approved", e.Review.State)
			assert.Equal(t, 12, e.PullRequest.Number)
		}},
		{gh.EventPullRequestReview, fake.PullRequestReviewEvent("submitted", repo, 99, gh.Review{User: gh.User{Login: "bob"}, State: gh.ReviewCommented}), func(t *testing.T, ev any) {
			assert.Equal(t, 99, ev.(*gh.PullRequestReviewEvent).PullRequest.Number)
		}},
		{gh.EventIssueComment, fake.IssueCommentEvent(repo, 12, "stackorder apply", "bob"), func(t *testing.T, ev any) {
			e := ev.(*gh.IssueCommentEvent)
			assert.True(t, e.IsPullRequest())
			assert.Equal(t, "COLLABORATOR", e.Comment.AuthorAssociation)
			assert.Equal(t, "alice", e.Issue.User.Login)
			assert.NotZero(t, e.Comment.ID)
		}},
		{gh.EventIssueComment, fake.IssueCommentEvent(repo, 50, "stackorder help", "carol"), func(t *testing.T, ev any) {
			assert.Equal(t, gh.IssueOpen, ev.(*gh.IssueCommentEvent).Issue.State)
		}},
		{gh.EventPush, fake.PushEvent(repo, "refs/heads/main", "m1", "stacks/prod/vpc/main.tf"), func(t *testing.T, ev any) {
			e := ev.(*gh.PushEvent)
			assert.True(t, e.Created)
			assert.Equal(t, "0000000000000000000000000000000000000000", e.Before)
			assert.Equal(t, []string{"stacks/prod/vpc/main.tf"}, e.Paths())
		}},
		{gh.EventPush, fake.PushEvent(repo, "refs/heads/main", "m2"), func(t *testing.T, ev any) {
			e := ev.(*gh.PushEvent)
			assert.False(t, e.Created)
			assert.Equal(t, "m1", e.Before)
		}},
		{gh.EventPush, fake.PushEvent(repo, "refs/tags/v1.0.0", "t1"), func(t *testing.T, ev any) {
			assert.Equal(t, "v1.0.0", ev.(*gh.PushEvent).TagName())
		}},
		{gh.EventPush, fake.PushEvent(repo, "refs/tags/v1.0.0", "t2"), func(t *testing.T, ev any) {
			assert.Equal(t, "t1", ev.(*gh.PushEvent).Before)
		}},
		{gh.EventWorkflowRun, fake.WorkflowRunEvent("completed", repo, gh.WorkflowRun{ID: run.ID}), func(t *testing.T, ev any) {
			e := ev.(*gh.WorkflowRunEvent)
			assert.Equal(t, ".github/workflows/stackorder-run.yml", e.WorkflowRun.Path)
			require.NotNil(t, e.Workflow)
			assert.Equal(t, "stackorder-run", e.Workflow.Name)
		}},
		{gh.EventWorkflowRun, fake.WorkflowRunEvent("requested", repo, gh.WorkflowRun{ID: 1}), func(t *testing.T, ev any) {
			assert.Nil(t, ev.(*gh.WorkflowRunEvent).Workflow)
		}},
		{gh.EventWorkflowJob, fake.WorkflowJobEvent("queued", repo, gh.WorkflowJob{ID: 5, RunID: run.ID, Name: "plan"}), func(t *testing.T, ev any) {
			assert.Equal(t, run.ID, ev.(*gh.WorkflowJobEvent).WorkflowJob.RunID)
		}},
		{gh.EventCheckRun, fake.CheckRunEvent("rerequested", repo, gh.CheckRun{ID: 9, Name: "stackorder/plan"}, ""), func(t *testing.T, ev any) {
			assert.Nil(t, ev.(*gh.CheckRunEvent).RequestedAction)
		}},
		{gh.EventCheckRun, fake.CheckRunEvent("requested_action", repo, gh.CheckRun{ID: 9}, "apply"), func(t *testing.T, ev any) {
			assert.Equal(t, "apply", ev.(*gh.CheckRunEvent).RequestedAction.Identifier)
		}},
		{gh.EventInstallation, fake.InstallationEvent("created", 3), func(t *testing.T, ev any) {
			e := ev.(*gh.InstallationEvent)
			assert.Equal(t, "acme", e.Installation.Account.Login)
			require.Len(t, e.Repositories, 1)
			assert.Equal(t, repo, e.Repositories[0].FullName)
		}},
		{gh.EventInstallation, fake.InstallationEvent("deleted", 404), func(t *testing.T, ev any) {
			assert.Equal(t, int64(404), ev.(*gh.InstallationEvent).InstallationID())
		}},
		{gh.EventDeploymentProtectionRule, fake.DeploymentProtectionRuleEvent(repo, run.ID, "production", "m2"), func(t *testing.T, ev any) {
			e := ev.(*gh.DeploymentProtectionRuleEvent)
			id, err := e.RunID()
			require.NoError(t, err)
			assert.Equal(t, run.ID, id)
			assert.Equal(t, "main", e.Deployment.Ref)
			assert.Equal(t, "workflow_dispatch", e.Event)
			assert.Equal(t, "production", e.Environment)
		}},
		{gh.EventDeploymentProtectionRule, fake.DeploymentProtectionRuleEvent(repo, 1, "staging", "m2"), func(t *testing.T, ev any) {
			assert.Equal(t, "main", ev.(*gh.DeploymentProtectionRuleEvent).Deployment.Ref)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			body, h := ghfake.SignedWebhook(secret, tt.event, tt.payload)
			assert.True(t, gh.VerifySignature(secret, body, h.Get(gh.HeaderSignature)))
			assert.Equal(t, tt.event, h.Get(gh.HeaderEvent))
			assert.Len(t, h.Get(gh.HeaderDelivery), 36)
			ev, err := gh.ParseEvent(h.Get(gh.HeaderEvent), body)
			require.NoError(t, err)
			c := ev.(gh.Event).Common()
			if tt.event != gh.EventInstallation {
				assert.Equal(t, int64(3), c.InstallationID())
				assert.Equal(t, repo, c.RepoFullName())
			}
			tt.check(t, ev)
		})
	}

	assert.Empty(t, fake.CheckRuns(repo))
	cms := fake.Comments(repo, 12)
	require.Len(t, cms, 1)
	assert.Equal(t, "stackorder apply", cms[0].Body)
}

func TestSignedWebhookRawAndDeliver(t *testing.T) {
	secret := []byte("s")
	body, h := ghfake.SignedWebhook(secret, "ping", []byte(`{"zen":"x"}`))
	assert.Equal(t, `{"zen":"x"}`, string(body))
	assert.Equal(t, "application/json", h.Get("Content-Type"))

	var got http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	status, err := ghfake.Deliver(context.Background(), srv.Client(), srv.URL+"/webhooks/github", secret, gh.EventPing, map[string]string{"zen": "y"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, status)
	assert.True(t, gh.VerifySignature(secret, gotBody, got.Get(gh.HeaderSignature)))
	assert.Equal(t, gh.EventPing, got.Get(gh.HeaderEvent))

	_, err = ghfake.Deliver(context.Background(), srv.Client(), "http://127.0.0.1:1/x", secret, "ping", []byte(`{}`))
	require.Error(t, err)
	_, err = ghfake.Deliver(context.Background(), srv.Client(), "://bad", secret, "ping", []byte(`{}`))
	require.Error(t, err)
	assert.Panics(t, func() { ghfake.SignedWebhook(secret, "ping", map[string]any{"c": make(chan int)}) })
}
