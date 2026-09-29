package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/artifacts"
	"github.com/stackorder/stackorder/internal/store"
)

type memArtifacts struct {
	mu    sync.Mutex
	items map[string]string
	err   error
	reads []string
}

func (m *memArtifacts) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, key)
	if m.err != nil {
		return nil, "", m.err
	}
	body, ok := m.items[key]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", artifacts.ErrNotFound, key)
	}
	return []byte(body), "text/plain; charset=utf-8", nil
}

func (m *memArtifacts) readCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reads)
}

func planTarget(runID uuid.UUID, key string) string {
	return "/v1/runs/" + runID.String() + "/stacks/" + url.PathEscape(key) + "/plan"
}

func TestPlanTextServesTheArtifactBucketCopy(t *testing.T) {
	mem := &memArtifacts{items: map[string]string{}}
	e := newRunnerEnv(t, func(_ *Config, d *Deps) { d.Artifacts = mem })
	e.db.addRepo(200, 2, "globex", "globex/platform")
	run, globexRun := uuid.New(), uuid.New()
	e.db.runs[run] = store.Run{ID: run, RepoID: 100, Repo: "acme/infra"}
	e.db.runs[globexRun] = store.Run{ID: globexRun, RepoID: 200, Repo: "globex/platform"}
	const blue, vpc = "stacks/prod/apps:blue", "stacks/prod/vpc"
	full := strings.Repeat("  ~ resource \"aws_instance\" \"app\" {\n", 12000)
	e.db.runStacks[run] = []store.RunStack{
		{Key: vpc, PlanText: "Plan: 1 to add, 0 to change, 0 to destroy.\n"},
		{Key: blue, PlanText: full[:8<<10], PlanTextTruncated: true, PlanURL: "s3://bucket/" + store.PlanTextArtifactKey(run, blue)},
	}
	e.db.runStacks[globexRun] = []store.RunStack{
		{Key: blue, PlanText: "globex", PlanTextTruncated: true, PlanURL: "s3://bucket/" + store.PlanTextArtifactKey(globexRun, blue)},
	}
	mem.items[store.PlanTextArtifactKey(run, blue)] = full
	mem.items[store.PlanTextArtifactKey(globexRun, blue)] = "globex plan"
	session := e.session("octocat", "acme")

	rec := e.do(withCookie(newRequest(t, http.MethodGet, planTarget(run, blue), nil), session))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, full, rec.Body.String(), "the whole plan, past the beginning Postgres keeps")
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(len(full)), rec.Header().Get("Content-Length"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, []string{"runs/" + run.String() + "/stacks-prod-apps-blue-0f29de5f/plan.txt"}, mem.reads,
		"the key the runs service writes the plan text under")

	rec = e.do(bearer(newRequest(t, http.MethodGet, planTarget(globexRun, blue), nil), e.apiKey("ci")))
	require.Equal(t, http.StatusOK, rec.Code, "API keys see every repository")
	assert.Equal(t, "globex plan", rec.Body.String())

	reads := mem.readCount()
	for name, tc := range map[string]struct {
		req     *http.Request
		status  int
		message string
	}{
		"a run in another organisation": {
			withCookie(newRequest(t, http.MethodGet, planTarget(globexRun, blue), nil), session),
			http.StatusNotFound, `run "` + globexRun.String() + `" not found`,
		},
		"an unknown run": {
			withCookie(newRequest(t, http.MethodGet, planTarget(uuid.New(), blue), nil), session),
			http.StatusNotFound, "not found",
		},
		"a stack the run does not have": {
			withCookie(newRequest(t, http.MethodGet, planTarget(run, "stacks/prod/eks"), nil), session),
			http.StatusNotFound, `stack "stacks/prod/eks" is not part of run`,
		},
		"a stack whose plan text Postgres holds whole": {
			withCookie(newRequest(t, http.MethodGet, planTarget(run, vpc), nil), session),
			http.StatusNotFound, "keeps no full plan text of stacks/prod/vpc",
		},
		"no credentials": {
			newRequest(t, http.MethodGet, planTarget(run, blue), nil),
			http.StatusUnauthorized, "sign in or use an API key",
		},
		"a runner token": {
			bearer(newRequest(t, http.MethodGet, planTarget(run, blue), nil), e.planToken()),
			http.StatusUnauthorized, "runner tokens are not accepted on this endpoint",
		},
	} {
		rec := e.do(tc.req)
		require.Equal(t, tc.status, rec.Code, name)
		assert.Contains(t, errorOf(t, rec).Message, tc.message, name)
	}
	assert.Equal(t, reads, mem.readCount(), "the bucket is read only for a visible run stack with plan_url")

	delete(mem.items, store.PlanTextArtifactKey(run, blue))
	rec = e.do(withCookie(newRequest(t, http.MethodGet, planTarget(run, blue), nil), session))
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, errorOf(t, rec).Message, "the full plan text of stacks/prod/apps:blue in run "+run.String()+" is no longer in the artifact bucket")

	mem.err = errors.New("s3: connection reset")
	rec = e.do(withCookie(newRequest(t, http.MethodGet, planTarget(run, blue), nil), session))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "connection reset", "the reader's error stays in the log")
}

func TestPlanTextWithoutAnArtifactBucket(t *testing.T) {
	e := newEnv(t)
	e.db.addRepo(100, 1, "acme", "acme/infra")
	run := uuid.New()
	e.db.runs[run] = store.Run{ID: run, RepoID: 100, Repo: "acme/infra"}
	e.db.runStacks[run] = []store.RunStack{
		{Key: "stacks/prod/vpc", PlanText: "Plan:", PlanTextTruncated: true, PlanURL: "s3://bucket/" + store.PlanTextArtifactKey(run, "stacks/prod/vpc")},
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, bearer(newRequest(t, http.MethodGet, planTarget(run, "stacks/prod/vpc"), nil), e.apiKey("ci")))
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not_found", errorOf(t, rec).Code)
	assert.Contains(t, errorOf(t, rec).Message, "this server has no artifact bucket")
}
