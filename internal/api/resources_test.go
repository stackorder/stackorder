package api

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

type resourcesEnv struct {
	*runnerEnv
	acmeStack, globexStack uuid.UUID
	reportRun, changeRun   uuid.UUID
	reportedAt, changedAt  time.Time
	finishedAt             time.Time
}

func newResourcesEnv(t *testing.T) *resourcesEnv {
	t.Helper()
	e := &resourcesEnv{
		runnerEnv: newRunnerEnv(t), acmeStack: uuid.New(), globexStack: uuid.New(), reportRun: uuid.New(), changeRun: uuid.New(),
		reportedAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC),
		changedAt:  time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC),
		finishedAt: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC),
	}
	globex := e.db.addRepo(200, 2, "globex", "globex/platform")
	e.db.stacks[e.acmeStack] = store.Stack{ID: e.acmeStack, RepoID: 100, Repo: "acme/infra", Key: "stacks/prod/vpc"}
	e.db.stacks[e.globexStack] = store.Stack{ID: e.globexStack, RepoID: globex.ID, Repo: globex.FullName, Key: "stacks/app"}
	e.db.resources[e.acmeStack] = store.StackResources{
		StackID: e.acmeStack, RunID: &e.reportRun, RunMode: v1.ModeApply, SHA: testSHA,
		ObservedAt: e.reportedAt, ReportedAt: e.reportedAt, ChangedAt: e.changedAt, Total: 4, Truncated: true,
		Resources: []v1.StackResource{
			{Address: "aws_subnet.a", Type: "aws_subnet", Provider: "registry.terraform.io/hashicorp/aws", Tainted: true},
			{Address: "aws_vpc.main", Type: "aws_vpc", Provider: "registry.terraform.io/hashicorp/aws"},
			{Address: `module.eks.aws_eks_cluster.this["<b>"]`, Type: "aws_eks_cluster", Module: "module.eks", Deposed: true},
		},
	}
	e.db.changes[e.acmeStack] = []store.ResourceChange{
		{Address: "aws_vpc.main", Action: v1.ResourceUpdate, RunID: e.changeRun, SHA: "abc123", PRNumber: 41, FinishedAt: &e.finishedAt},
		{Address: `module.eks.aws_eks_cluster.this["<b>"]`, Action: v1.ResourceReplace, RunID: e.changeRun, SHA: "abc123"},
		{Address: "aws_eip.gone", Action: v1.ResourceCreate, RunID: e.changeRun, SHA: "abc123"},
	}
	return e
}

func resourcesTarget(id uuid.UUID) string {
	return "/v1/stacks/" + id.String() + "/resources"
}

func TestStackResourcesRoute(t *testing.T) {
	e := newResourcesEnv(t)
	rec := e.do(withCookie(newRequest(t, http.MethodGet, resourcesTarget(e.acmeStack), nil), e.session("octocat", "acme")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	want := v1.StackResources{
		RunID: e.reportRun.String(), Mode: v1.ModeApply, SHA: testSHA, ReportedAt: &e.reportedAt, ChangedAt: &e.changedAt,
		Total: 4, Truncated: true,
		Items: []v1.StackResource{
			{Address: "aws_subnet.a", Type: "aws_subnet", Provider: "registry.terraform.io/hashicorp/aws", Tainted: true},
			{
				Address: "aws_vpc.main", Type: "aws_vpc", Provider: "registry.terraform.io/hashicorp/aws",
				LastChange: &v1.ResourceChange{Action: v1.ResourceUpdate, RunID: e.changeRun.String(), SHA: "abc123", PRNumber: 41, FinishedAt: &e.finishedAt},
			},
			{
				Address: `module.eks.aws_eks_cluster.this["<b>"]`, Type: "aws_eks_cluster", Module: "module.eks", Deposed: true,
				LastChange: &v1.ResourceChange{Action: v1.ResourceReplace, RunID: e.changeRun.String(), SHA: "abc123"},
			},
		},
	}
	if diff := cmp.Diff(want, decodeBody[v1.StackResources](t, rec)); diff != "" {
		t.Errorf("response (-want +got):\n%s", diff)
	}
	assert.NotContains(t, rec.Body.String(), "<b>", "addresses are escaped like every JSON string")
	e.db.mu.Lock()
	asked := e.db.changesAsked
	e.db.mu.Unlock()
	assert.Equal(t, [][]string{{"aws_subnet.a", "aws_vpc.main", `module.eks.aws_eks_cluster.this["<b>"]`}}, asked,
		"one attribution query for the listed addresses")
	assert.Nil(t, e.db.resources[e.acmeStack].Resources[1].LastChange, "the stored list is left alone")
}

func TestStackResourcesWithoutAList(t *testing.T) {
	e := newResourcesEnv(t)
	rec := e.do(bearer(newRequest(t, http.MethodGet, resourcesTarget(e.globexStack), nil), e.apiKey("ci")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"total": 0, "items": []}`, rec.Body.String(), "no run fields and never a null list")
	e.db.mu.Lock()
	defer e.db.mu.Unlock()
	assert.Empty(t, e.db.changesAsked, "nothing to attribute")
}

func TestStackResourcesOfARemovedStack(t *testing.T) {
	e := newResourcesEnv(t)
	removed := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	st := e.db.stacks[e.acmeStack]
	st.RemovedAt = &removed
	e.db.stacks[e.acmeStack] = st
	rec := e.do(bearer(newRequest(t, http.MethodGet, resourcesTarget(e.acmeStack), nil), e.apiKey("ci")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeBody[v1.StackResources](t, rec)
	assert.Equal(t, e.reportRun.String(), got.RunID)
	assert.Len(t, got.Items, 3, "a removed stack keeps its last list")
}

func TestStackResourcesRefusals(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *resourcesEnv) *http.Request
		status  int
		message string
	}{
		{
			name: "another organization's stack",
			prepare: func(t *testing.T, e *resourcesEnv) *http.Request {
				return withCookie(newRequest(t, http.MethodGet, resourcesTarget(e.globexStack), nil), e.session("octocat", "acme"))
			},
			status: http.StatusNotFound, message: "not found",
		},
		{
			name: "unknown stack",
			prepare: func(t *testing.T, e *resourcesEnv) *http.Request {
				return bearer(newRequest(t, http.MethodGet, resourcesTarget(uuid.New()), nil), e.apiKey("ci"))
			},
			status: http.StatusNotFound, message: "not found",
		},
		{
			name: "malformed stack id",
			prepare: func(t *testing.T, e *resourcesEnv) *http.Request {
				return bearer(newRequest(t, http.MethodGet, "/v1/stacks/vpc/resources", nil), e.apiKey("ci"))
			},
			status: http.StatusNotFound,
		},
		{
			name: "runner token",
			prepare: func(t *testing.T, e *resourcesEnv) *http.Request {
				return bearer(newRequest(t, http.MethodGet, resourcesTarget(e.acmeStack), nil), e.planToken())
			},
			status: http.StatusUnauthorized, message: "runner tokens are not accepted on this endpoint",
		},
		{
			name: "signed out",
			prepare: func(t *testing.T, e *resourcesEnv) *http.Request {
				return newRequest(t, http.MethodGet, resourcesTarget(e.acmeStack), nil)
			},
			status: http.StatusUnauthorized, message: "sign in or use an API key",
		},
		{
			name: "store failure",
			prepare: func(t *testing.T, e *resourcesEnv) *http.Request {
				e.db.resourcesErr = errors.New("connection reset")
				return bearer(newRequest(t, http.MethodGet, resourcesTarget(e.acmeStack), nil), e.apiKey("ci"))
			},
			status: http.StatusInternalServerError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResourcesEnv(t)
			rec := e.do(tc.prepare(t, e))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			body := errorOf(t, rec)
			assert.Contains(t, body.Message, tc.message)
			assert.NotContains(t, rec.Body.String(), "aws_vpc.main", "no list is returned")
			assert.NotContains(t, rec.Body.String(), "connection reset")
		})
	}
}

func TestResultWithAMalformedResourceListNamesTheField(t *testing.T) {
	e := newRunnerEnv(t)
	body := `{"mode":"apply","status":"success","resources":{"total":1,"items":[{"address":"aws_vpc.main","type":"aws_vpc","tainted":"yes"}]}}`
	rec := e.do(bearer(newRequest(t, http.MethodPost, "/v1/runs/"+uuid.NewString()+"/stacks/a/result", body), e.apiKey("ci")))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	got := errorOf(t, rec)
	assert.Equal(t, codeInvalid, got.Code)
	details, ok := got.Details.(map[string]any)
	require.True(t, ok, rec.Body.String())
	assert.Equal(t, "resources.items.tainted", details["field"], "the CLI resends the result without its list when the field is under resources")
	assert.Zero(t, e.runs.count())
}
