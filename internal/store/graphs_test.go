//go:build integration

package store_test

import (
	"cmp"
	"slices"
	"testing"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func sortedGraph(g *v1.Graph) *v1.Graph {
	out := *g
	out.Stacks = slices.Clone(g.Stacks)
	slices.SortFunc(out.Stacks, func(a, b v1.Stack) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Key, b.Key))
	})
	out.Modules = slices.Clone(g.Modules)
	slices.SortFunc(out.Modules, func(a, b v1.Module) int { return cmp.Compare(a.Key, b.Key) })
	return &out
}

func TestSaveGraphRoundTrip(t *testing.T) {
	f := newFixture(t)
	in := sampleGraph(f.repo.FullName, "sha1")
	graphID, ids := f.saveGraph(f.repo.ID, in)
	require.NotEqual(t, uuid.Nil, graphID)
	assert.Len(t, ids, 3, "external stacks get no id")
	assert.NotContains(t, ids, "stacks/prod/tgw")

	got, gotID, err := f.s.GetGraph(f.ctx, f.repo.ID, "sha1")
	require.NoError(t, err)
	assert.Equal(t, graphID, gotID)
	if diff := gocmp.Diff(sortedGraph(in), sortedGraph(got), cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("graph round trip (-want +got):\n%s", diff)
	}

	byID, err := f.s.GetGraphByID(f.ctx, graphID)
	require.NoError(t, err)
	assert.Equal(t, got, byID)

	stackIDs, err := f.s.GraphStackIDs(f.ctx, graphID)
	require.NoError(t, err)
	assert.Equal(t, ids, stackIDs)

	vpc, err := f.s.GetStack(f.ctx, ids["stacks/prod/vpc"])
	require.NoError(t, err)
	assert.Equal(t, "acme/infra", vpc.Repo)
	assert.Equal(t, "production", vpc.Environment)
	assert.Equal(t, v1.ToolTofu, vpc.Tool)
	require.NotNil(t, vpc.Backend)
	assert.Equal(t, "prod/vpc.tfstate", vpc.Backend.Key)
	assert.Nil(t, vpc.RemovedAt)

	apps, err := f.s.GetStackByKey(f.ctx, f.repo.ID, "stacks/prod/apps:blue")
	require.NoError(t, err)
	assert.Equal(t, "blue", apps.Workspace)
	assert.Equal(t, "stacks/prod/apps", apps.Path)
	assert.Equal(t, v1.DefaultEnvironment, apps.Environment, "a stack with no environment mapping runs under the default one")
	assert.Equal(t, v1.DefaultEnvironment, apps.Detail().Environment)
}

func TestSaveGraphIdempotentAndStackIdentityStable(t *testing.T) {
	f := newFixture(t)
	first := sampleGraph(f.repo.FullName, "sha1")
	id1, ids1 := f.saveGraph(f.repo.ID, first)

	again, idsAgain := f.saveGraph(f.repo.ID, first)
	assert.Equal(t, id1, again, "same sha is the same graph")
	assert.Equal(t, ids1, idsAgain)
	got, _, err := f.s.GetGraph(f.ctx, f.repo.ID, "sha1")
	require.NoError(t, err)
	assert.Len(t, got.Edges, len(first.Edges), "resaving replaces edges instead of duplicating them")

	second := sampleGraph(f.repo.FullName, "sha2")
	second.Stacks[0].Environment = "prod-eu"
	second.Stacks = append(second.Stacks, v1.Stack{Key: "stacks/prod/dns", Path: "stacks/prod/dns"})
	id2, ids2 := f.saveGraph(f.repo.ID, second)
	assert.NotEqual(t, id1, id2)
	for key, id := range ids1 {
		assert.Equal(t, id, ids2[key], "stack %s keeps its id across graphs", key)
	}
	assert.Contains(t, ids2, "stacks/prod/dns")

	vpc, err := f.s.GetStack(f.ctx, ids1["stacks/prod/vpc"])
	require.NoError(t, err)
	assert.Equal(t, "prod-eu", vpc.Environment, "stack row reflects the latest saved graph")

	old, _, err := f.s.GetGraph(f.ctx, f.repo.ID, "sha1")
	require.NoError(t, err)
	assert.Equal(t, "production", old.Stacks[slices.IndexFunc(old.Stacks, func(s v1.Stack) bool { return s.Key == "stacks/prod/vpc" })].Environment,
		"stored graph keeps the node as uploaded")

	latest, latestID, err := f.s.LatestGraph(f.ctx, f.repo.ID)
	require.NoError(t, err)
	assert.Equal(t, id2, latestID)
	assert.Equal(t, "sha2", latest.SHA)

	cached, cachedID, err := f.s.FindGraphByTreeHash(f.ctx, f.repo.ID, "tree-sha1")
	require.NoError(t, err)
	assert.Equal(t, id1, cachedID)
	assert.Equal(t, "sha1", cached.SHA)
}

func TestSaveGraphErrors(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name   string
		repoID int64
		graph  *v1.Graph
		want   error
	}{
		{"nil graph", f.repo.ID, nil, store.ErrInvalid},
		{"missing sha", f.repo.ID, &v1.Graph{}, store.ErrInvalid},
		{"unknown repo", 404, &v1.Graph{SHA: "x"}, store.ErrNotFound},
		{"duplicate stack", f.repo.ID, &v1.Graph{SHA: "x", Stacks: []v1.Stack{{Key: "a", Path: "a"}, {Key: "a", Path: "a"}}}, store.ErrInvalid},
		{"duplicate module", f.repo.ID, &v1.Graph{SHA: "x", Modules: []v1.Module{
			{Key: "m", Kind: v1.ModuleLocal}, {Key: "m", Kind: v1.ModuleLocal},
		}}, store.ErrInvalid},
		{"bad module kind", f.repo.ID, &v1.Graph{SHA: "x", Modules: []v1.Module{{Key: "m", Kind: "svn"}}}, store.ErrInvalid},
		{"bad edge type", f.repo.ID, &v1.Graph{SHA: "x", Edges: []v1.Edge{
			{From: v1.StackRef("a"), To: v1.StackRef("b"), Type: "likes"},
		}}, store.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := f.s.SaveGraph(f.ctx, tc.repoID, tc.graph)
			require.ErrorIs(t, err, tc.want)
		})
	}
	_, _, err := f.s.GetGraph(f.ctx, f.repo.ID, "x")
	require.ErrorIs(t, err, store.ErrNotFound, "failed saves leave nothing behind")
}

func TestGraphLookupsNotFound(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.s.LatestGraph(f.ctx, f.repo.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, _, err = f.s.FindGraphByTreeHash(f.ctx, f.repo.ID, "")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, _, err = f.s.FindGraphByTreeHash(f.ctx, f.repo.ID, "nope")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.GetGraphByID(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.GetStack(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestDefaultGraphAccessors(t *testing.T) {
	f := newFixture(t)
	other := f.addRepo(2, 200, "globex", "globex/platform")
	_, _, err := f.s.GetDefaultGraph(f.ctx, f.repo.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "no default graph before a merge")

	mergedID, _ := f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "merged"))
	f.saveGraph(f.repo.ID, sampleGraph(f.repo.FullName, "pr-head"))
	otherID, _ := f.saveGraph(other.ID, &v1.Graph{SHA: "elsewhere"})

	require.NoError(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, mergedID))
	require.NoError(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, mergedID), "setting it again is a no-op")
	g, id, err := f.s.GetDefaultGraph(f.ctx, f.repo.ID)
	require.NoError(t, err)
	assert.Equal(t, mergedID, id)
	assert.Equal(t, "merged", g.SHA)
	assert.Equal(t, f.repo.FullName, g.Repo)
	repo, err := f.s.GetRepo(f.ctx, f.repo.ID)
	require.NoError(t, err)
	require.NotNil(t, repo.DefaultGraphID)
	assert.Equal(t, mergedID, *repo.DefaultGraphID)
	latest, _, err := f.s.LatestGraph(f.ctx, f.repo.ID)
	require.NoError(t, err)
	assert.Equal(t, "pr-head", latest.SHA, "the latest graph is unaffected")

	require.ErrorIs(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, otherID), store.ErrNotFound, "graph of another repository")
	require.ErrorIs(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, uuid.New()), store.ErrNotFound)
	require.ErrorIs(t, f.s.SetDefaultGraph(f.ctx, 404, mergedID), store.ErrNotFound)
	_, _, err = f.s.GetDefaultGraph(f.ctx, other.ID)
	require.ErrorIs(t, err, store.ErrNotFound)

	f.exec(`DELETE FROM graphs WHERE id = $1`, mergedID)
	_, _, err = f.s.GetDefaultGraph(f.ctx, f.repo.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "deleting the graph clears the default")

	require.NoError(t, f.s.SetDefaultGraph(f.ctx, other.ID, otherID))
	require.NoError(t, f.s.DeleteRepo(f.ctx, other.ID), "a repository with a default graph can be deleted")
	_, err = f.s.GetRepo(f.ctx, other.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestMarkStacksRemoved(t *testing.T) {
	f := newFixture(t)
	ids := f.stacks("a", "b", "c")

	n, err := f.s.MarkStacksRemoved(f.ctx, f.repo.ID, []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	n, err = f.s.MarkStacksRemoved(f.ctx, f.repo.ID, []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, 0, n, "already removed stacks are not counted again")

	live, err := f.s.ListStacks(f.ctx, f.repo.ID, false)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "a", live[0].Key)

	all, err := f.s.ListStacks(f.ctx, f.repo.ID, true)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	again := f.stacks("b")
	assert.Equal(t, ids["b"], again["b"])
	b, err := f.s.GetStack(f.ctx, ids["b"])
	require.NoError(t, err)
	assert.Nil(t, b.RemovedAt, "seeing a stack again clears removed_at")

	n, err = f.s.MarkStacksRemoved(f.ctx, f.repo.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "nil keep list removes every live stack")
}
