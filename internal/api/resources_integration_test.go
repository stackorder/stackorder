//go:build integration

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestStackResources(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	vpcID := w.stacks["stacks/prod/vpc"]
	_, err := w.st.UpdateRunStack(ctx, w.applyRun.ID, vpcID, store.RunStackPatch{
		Summary: &v1.PlanSummary{Adds: 1, Changes: 1, Added: []string{"aws_subnet.a"}, Changed: []string{"aws_vpc.main"}},
	})
	require.NoError(t, err)
	list := v1.ResourceList{Total: 3, Items: []v1.StackResource{
		{Address: "aws_vpc.main", Type: "aws_vpc", Provider: "registry.opentofu.org/hashicorp/aws"},
		{Address: "aws_subnet.a", Type: "aws_subnet", Provider: "registry.opentofu.org/hashicorp/aws", Tainted: true},
		{Address: "aws_route_table.private", Type: "aws_route_table", Provider: "registry.opentofu.org/hashicorp/aws"},
	}}
	require.NoError(t, w.st.SetStackResources(ctx, vpcID, w.applyRun.ID, list, store.ObservedNow))
	require.NoError(t, w.st.SetStackResources(ctx, w.globexStack, w.globexRun.ID, v1.ResourceList{Total: 1, Items: []v1.StackResource{
		{Address: "aws_s3_bucket.globex", Type: "aws_s3_bucket"},
	}}, store.ObservedNow))

	target := "/v1/stacks/" + vpcID.String() + "/resources"
	got := decodeBody[v1.StackResources](t, w.ok(target, true))
	assert.Equal(t, w.applyRun.ID.String(), got.RunID)
	assert.Equal(t, v1.ModeApply, got.Mode)
	assert.Equal(t, mergedSHA, got.SHA)
	require.NotNil(t, got.ReportedAt)
	require.NotNil(t, got.ChangedAt)
	assert.Equal(t, 3, got.Total)
	assert.False(t, got.Truncated)
	require.Len(t, got.Items, 3)
	assert.Equal(t, []string{"aws_route_table.private", "aws_subnet.a", "aws_vpc.main"},
		[]string{got.Items[0].Address, got.Items[1].Address, got.Items[2].Address}, "sorted by address")
	assert.Nil(t, got.Items[0].LastChange, "no applied run changed it")
	require.NotNil(t, got.Items[1].LastChange)
	assert.Equal(t, v1.ResourceCreate, got.Items[1].LastChange.Action)
	assert.True(t, got.Items[1].Tainted)
	require.NotNil(t, got.Items[2].LastChange)
	change := *got.Items[2].LastChange
	assert.Equal(t, v1.ResourceUpdate, change.Action)
	assert.Equal(t, w.applyRun.ID.String(), change.RunID)
	assert.Equal(t, mergedSHA, change.SHA)
	assert.Equal(t, 5, change.PRNumber)
	assert.NotNil(t, change.FinishedAt)
	assert.Equal(t, got, decodeBody[v1.StackResources](t, w.ok(target, false)), "an API key sees every stack")

	globexTarget := "/v1/stacks/" + w.globexStack.String() + "/resources"
	assert.Equal(t, http.StatusNotFound, w.get(globexTarget, true).Code, "another organization's stack is not found")
	globex := decodeBody[v1.StackResources](t, w.ok(globexTarget, false))
	assert.Equal(t, v1.ModeDrift, globex.Mode)
	require.Len(t, globex.Items, 1)

	rec := w.ok("/v1/stacks/"+w.stacks["stacks/prod/eks"].String()+"/resources", true)
	assert.JSONEq(t, `{"total": 0, "items": []}`, rec.Body.String(), "a stack no job has reported on")

	r := newRequest(t, http.MethodGet, target, nil)
	r.Header.Set("Authorization", "Bearer "+w.issuer.Token(w.issuer.PlanClaims("acme/infra", "100", 7, prHeadSHA)))
	runner := httptest.NewRecorder()
	w.h.ServeHTTP(runner, r)
	assert.Equal(t, http.StatusUnauthorized, runner.Code, "runner tokens do not read resource lists")

	marked, err := w.st.MarkStacksRemoved(ctx, w.infra.ID, []string{"stacks/prod/eks", "stacks/prod/apps:blue", "stacks/prod/new"})
	require.NoError(t, err)
	require.Positive(t, marked)
	removed := decodeBody[v1.StackResources](t, w.ok(target, true))
	assert.Equal(t, got, removed, "a removed stack keeps its last list")
}
