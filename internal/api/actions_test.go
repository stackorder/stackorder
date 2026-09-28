package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

type actionEnv struct {
	*testEnv
	acmeStack, globexStack uuid.UUID
	acmeRun, globexRun     uuid.UUID
}

func newActionEnv(t *testing.T) *actionEnv {
	t.Helper()
	e := &actionEnv{testEnv: newEnv(t), acmeStack: uuid.New(), globexStack: uuid.New(), acmeRun: uuid.New(), globexRun: uuid.New()}
	acme := e.db.addRepo(100, 1, "acme", "acme/infra")
	globex := e.db.addRepo(200, 2, "globex", "globex/platform")
	e.db.stacks[e.acmeStack] = store.Stack{ID: e.acmeStack, RepoID: acme.ID, Repo: acme.FullName, Key: "stacks/prod/vpc"}
	e.db.stacks[e.globexStack] = store.Stack{ID: e.globexStack, RepoID: globex.ID, Repo: globex.FullName, Key: "stacks/app"}
	e.db.runs[e.acmeRun] = store.Run{ID: e.acmeRun, RepoID: acme.ID, Repo: acme.FullName}
	e.db.runs[e.globexRun] = store.Run{ID: e.globexRun, RepoID: globex.ID, Repo: globex.FullName}
	return e
}

func sameOriginPost(t *testing.T, target string, body any) *http.Request {
	r := newRequest(t, http.MethodPost, target, body)
	r.Header.Set("Origin", testBaseURL)
	return r
}

func (e *actionEnv) calls(method string) []runsCall {
	e.runs.mu.Lock()
	defer e.runs.mu.Unlock()
	var out []runsCall
	for _, c := range e.runs.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func TestUnlockStackAsPerson(t *testing.T) {
	e := newActionEnv(t)
	e.runs.canAct = true
	target := "/v1/stacks/" + e.acmeStack.String() + "/unlock"
	req := v1.UnlockRequest{Reason: "PR 41 closed", ForceState: true}

	rec := e.do(withCookie(sameOriginPost(t, target, req), e.session("octocat", "acme")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, v1.UnlockResponse{Released: []v1.LockInfo{{StackID: e.acmeStack.String(), RunID: "r1"}}}, decodeBody[v1.UnlockResponse](t, rec))
	unlock := e.calls("Unlock")
	require.Len(t, unlock, 1)
	assert.Equal(t, "octocat", unlock[0].actor)
	assert.Equal(t, e.acmeStack.String(), unlock[0].stackID)
	assert.Equal(t, req, unlock[0].body)
	check := e.calls("CanActOnRepo")
	require.Len(t, check, 1)
	assert.Equal(t, runsCall{method: "CanActOnRepo", login: "octocat", repoID: 100}, check[0])

	r := sameOriginPost(t, target, nil)
	r.Header.Del("Origin")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	rec = e.do(withCookie(r, e.session("octocat", "acme")))
	assert.Equal(t, http.StatusOK, rec.Code, "the body is optional and Sec-Fetch-Site suffices")
}

func TestUnlockStackRefusals(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(e *actionEnv) *http.Request
		status  int
		checked bool
	}{
		{
			name: "no push permission",
			prepare: func(e *actionEnv) *http.Request {
				return withCookie(sameOriginPost(t, "/v1/stacks/"+e.acmeStack.String()+"/unlock", v1.UnlockRequest{}), e.session("octocat", "acme"))
			},
			status: http.StatusForbidden, checked: true,
		},
		{
			name: "stack of another organisation",
			prepare: func(e *actionEnv) *http.Request {
				e.runs.canAct = true
				return withCookie(sameOriginPost(t, "/v1/stacks/"+e.globexStack.String()+"/unlock", v1.UnlockRequest{}), e.session("octocat", "acme"))
			},
			status: http.StatusNotFound,
		},
		{
			name: "unknown stack",
			prepare: func(e *actionEnv) *http.Request {
				e.runs.canAct = true
				return withCookie(sameOriginPost(t, "/v1/stacks/"+uuid.NewString()+"/unlock", v1.UnlockRequest{}), e.session("octocat", "acme"))
			},
			status: http.StatusNotFound,
		},
		{
			name: "cross-origin",
			prepare: func(e *actionEnv) *http.Request {
				e.runs.canAct = true
				r := withCookie(sameOriginPost(t, "/v1/stacks/"+e.acmeStack.String()+"/unlock", v1.UnlockRequest{}), e.session("octocat", "acme"))
				r.Header.Set("Origin", "https://evil.test")
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "no origin headers",
			prepare: func(e *actionEnv) *http.Request {
				e.runs.canAct = true
				r := withCookie(sameOriginPost(t, "/v1/stacks/"+e.acmeStack.String()+"/unlock", v1.UnlockRequest{}), e.session("octocat", "acme"))
				r.Header.Del("Origin")
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "form post",
			prepare: func(e *actionEnv) *http.Request {
				e.runs.canAct = true
				r := withCookie(sameOriginPost(t, "/v1/stacks/"+e.acmeStack.String()+"/unlock", "reason=x"), e.session("octocat", "acme"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return r
			},
			status: http.StatusBadRequest,
		},
		{
			name: "permission check fails",
			prepare: func(e *actionEnv) *http.Request {
				e.runs.canActErr = errors.New("github down")
				return withCookie(sameOriginPost(t, "/v1/stacks/"+e.acmeStack.String()+"/unlock", v1.UnlockRequest{}), e.session("octocat", "acme"))
			},
			status: http.StatusInternalServerError, checked: true,
		},
		{
			name: "runner token",
			prepare: func(e *actionEnv) *http.Request {
				return bearer(sameOriginPost(t, "/v1/stacks/"+e.acmeStack.String()+"/unlock", v1.UnlockRequest{}), "eyJhbGciOiJSUzI1NiJ9.e30.x")
			},
			status: http.StatusUnauthorized,
		},
		{
			name: "signed out",
			prepare: func(e *actionEnv) *http.Request {
				return sameOriginPost(t, "/v1/stacks/"+e.acmeStack.String()+"/unlock", v1.UnlockRequest{})
			},
			status: http.StatusUnauthorized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newActionEnv(t)
			rec := e.do(tc.prepare(e))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Empty(t, e.calls("Unlock"), "the lock is not released")
			assert.Equal(t, tc.checked, len(e.calls("CanActOnRepo")) > 0)
		})
	}
}

func TestUnlockWithAPIKey(t *testing.T) {
	e := newActionEnv(t)
	key := e.apiKey("ci")
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/stacks/"+e.globexStack.String()+"/unlock", v1.UnlockRequest{Reason: "cleanup"}), key))
	require.Equal(t, http.StatusOK, rec.Code, "API keys need neither an origin nor push permission")
	assert.Equal(t, "apikey:ci", e.calls("Unlock")[0].actor)
	assert.Empty(t, e.calls("CanActOnRepo"))

	e.runs.err = principal.Wrap(principal.ErrNotFound, "stack is not locked")
	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/stacks/"+e.acmeStack.String()+"/unlock", nil), key))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not found: stack is not locked", errorOf(t, rec).Message)
}

func TestUnlockByKey(t *testing.T) {
	e := newActionEnv(t)
	key := e.apiKey("laptop")
	req := v1.UnlockRequest{Repo: "globex/platform", StackKey: "stacks/app", Reason: "stuck"}
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/unlock", req), key))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "stacks/app", decodeBody[v1.UnlockResponse](t, rec).Released[0].StackKey)
	calls := e.calls("UnlockByKey")
	require.Len(t, calls, 1)
	assert.Equal(t, "apikey:laptop", calls[0].actor)
	assert.Equal(t, req, calls[0].body)

	for name, body := range map[string]v1.UnlockRequest{
		"missing repo":      {StackKey: "stacks/app"},
		"missing stack key": {Repo: "globex/platform"},
	} {
		rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/unlock", body), key))
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
	}
	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/unlock", nil), key))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "the body is required")

	cookie := e.session("octocat", "acme")
	rec = e.do(withCookie(sameOriginPost(t, "/v1/unlock", req), cookie))
	assert.Equal(t, http.StatusNotFound, rec.Code, "a person cannot unlock another organisation's stack")
	rec = e.do(withCookie(sameOriginPost(t, "/v1/unlock", v1.UnlockRequest{Repo: "acme/infra", StackKey: "stacks/prod/vpc"}), cookie))
	assert.Equal(t, http.StatusForbidden, rec.Code, "push permission is required")
	e.runs.canAct = true
	rec = e.do(withCookie(sameOriginPost(t, "/v1/unlock", v1.UnlockRequest{Repo: "acme/infra", StackKey: "stacks/prod/vpc"}), cookie))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "octocat", e.calls("UnlockByKey")[1].actor)
	assert.Len(t, e.calls("UnlockByKey"), 2)
}

func TestRerun(t *testing.T) {
	e := newActionEnv(t)
	target := "/v1/runs/" + e.acmeRun.String() + "/rerun"
	cookie := e.session("octocat", "acme")

	rec := e.do(withCookie(sameOriginPost(t, target, map[string]any{}), cookie))
	require.Equal(t, http.StatusForbidden, rec.Code, "push permission is required")
	assert.Empty(t, e.calls("Rerun"))

	e.runs.canAct = true
	rec = e.do(withCookie(sameOriginPost(t, target, map[string]any{}), cookie))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	resp := decodeBody[v1.CreateRunResponse](t, rec)
	assert.Equal(t, "5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b", resp.RunID)
	assert.Equal(t, v1.RunPending, resp.Status)
	assert.False(t, resp.Existing)
	require.NotNil(t, resp.Run)
	assert.Equal(t, testBaseURL+"/runs/5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b", resp.Run.HTMLURL)
	calls := e.calls("Rerun")
	require.Len(t, calls, 1)
	assert.Equal(t, "octocat", calls[0].actor)
	assert.Equal(t, e.acmeRun.String(), calls[0].runID)

	e.runs.run = &v1.Run{ID: e.acmeRun.String(), Status: v1.RunPlanning}
	rec = e.do(withCookie(sameOriginPost(t, target, nil), cookie))
	require.Equal(t, http.StatusOK, rec.Code, "re-running into the same run reports it as existing")
	assert.True(t, decodeBody[v1.CreateRunResponse](t, rec).Existing)

	rec = e.do(withCookie(sameOriginPost(t, "/v1/runs/"+e.globexRun.String()+"/rerun", nil), cookie))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = e.do(withCookie(sameOriginPost(t, "/v1/runs/"+uuid.NewString()+"/rerun", nil), cookie))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = e.do(withCookie(sameOriginPost(t, "/v1/runs/nope/rerun", nil), cookie))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	r := withCookie(newRequest(t, http.MethodPost, target, nil), cookie)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	assert.Equal(t, http.StatusForbidden, e.do(r).Code)

	e.runs.canAct = false
	rec = e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs/"+e.globexRun.String()+"/rerun", nil), e.apiKey("ci")))
	require.Equal(t, http.StatusCreated, rec.Code, "API keys re-run anything")
	assert.Equal(t, "apikey:ci", e.calls("Rerun")[len(e.calls("Rerun"))-1].actor)

	e.runs.err = &principal.RefusedError{}
	rec = e.do(bearer(newRequest(t, http.MethodPost, target, nil), e.apiKey("ci")))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, codeRefused, errorOf(t, rec).Code)
}
