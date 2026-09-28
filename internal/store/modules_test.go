//go:build integration

package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func TestUpsertModulesAndFamilies(t *testing.T) {
	f := newFixture(t)
	ids, err := f.s.UpsertModules(f.ctx, []v1.Module{
		{Key: "acme/modules//vpc@v1.0.0", Kind: v1.ModuleGit, Source: "git::x?ref=v1.0.0", Ref: "v1.0.0"},
		{Key: "registry:terraform-aws-modules/vpc/aws@5.1.0", Kind: v1.ModuleRegistry, Source: "terraform-aws-modules/vpc/aws"},
		{Key: "acme/infra//modules/net", Kind: v1.ModuleLocal, Path: "modules/net", Source: "../net"},
	})
	require.NoError(t, err)
	assert.Len(t, ids, 3)

	again, err := f.s.UpsertModules(f.ctx, []v1.Module{{Key: "acme/modules//vpc@v1.0.0", Kind: v1.ModuleGit}})
	require.NoError(t, err)
	assert.Equal(t, ids["acme/modules//vpc@v1.0.0"], again["acme/modules//vpc@v1.0.0"])

	m, err := f.s.GetModuleByKey(f.ctx, "acme/modules//vpc@v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "acme/modules//vpc", m.BaseKey)
	assert.Equal(t, "v1.0.0", m.Ref)
	assert.Equal(t, "git::x?ref=v1.0.0", m.Source, "an empty source keeps the stored one")
	assert.False(t, m.Family())

	reg, err := f.s.GetModuleByKey(f.ctx, "registry:terraform-aws-modules/vpc/aws@5.1.0")
	require.NoError(t, err)
	assert.Equal(t, "5.1.0", reg.Ref, "ref is taken from the key when not given")

	cases := []struct {
		name   string
		filter store.ModuleFilter
		want   []string
	}{
		{"all", store.ModuleFilter{}, []string{
			"acme/infra//modules/net", "acme/modules//vpc", "acme/modules//vpc@v1.0.0",
			"registry:terraform-aws-modules/vpc/aws", "registry:terraform-aws-modules/vpc/aws@5.1.0",
		}},
		{"git", store.ModuleFilter{Kind: v1.ModuleGit}, []string{"acme/modules//vpc", "acme/modules//vpc@v1.0.0"}},
		{"prefix families", store.ModuleFilter{Prefix: "acme/", FamiliesOnly: true}, []string{"acme/infra//modules/net", "acme/modules//vpc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mods, err := f.s.ListModules(f.ctx, tc.filter)
			require.NoError(t, err)
			var keys []string
			for _, m := range mods {
				keys = append(keys, m.Key)
			}
			assert.Equal(t, tc.want, keys)
		})
	}

	got, err := f.s.GetModule(f.ctx, ids["acme/infra//modules/net"])
	require.NoError(t, err)
	assert.Equal(t, v1.Module{Key: "acme/infra//modules/net", Kind: v1.ModuleLocal, Path: "modules/net", Source: "../net"}, got.ToV1())
	_, err = f.s.GetModule(f.ctx, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestModuleVersionsAndConsumers(t *testing.T) {
	f := newFixture(t)
	other := f.addRepo(1, 101, "acme", "acme/apps")
	mod := func(ref string) v1.Module {
		return v1.Module{Key: "acme/modules//vpc@" + ref, Kind: v1.ModuleGit, Ref: ref, Source: "git::vpc?ref=" + ref}
	}
	wrapper := v1.Module{Key: "acme/infra//modules/wrapper", Kind: v1.ModuleLocal, Path: "modules/wrapper"}
	uses := func(from v1.NodeRef, to v1.Module) v1.Edge {
		e := v1.Edge{From: from, To: v1.ModuleRef(to.Key), Type: v1.EdgeUsesModule}
		if to.Ref != "" {
			e.Meta = map[string]string{"ref": to.Ref}
		}
		return e
	}

	f.saveGraph(f.repo.ID, &v1.Graph{
		SHA:     "old",
		Stacks:  []v1.Stack{{Key: "stacks/legacy", Path: "stacks/legacy"}},
		Modules: []v1.Module{mod("v1.0.0")},
		Edges:   []v1.Edge{uses(v1.StackRef("stacks/legacy"), mod("v1.0.0"))},
	})
	f.saveGraph(f.repo.ID, &v1.Graph{
		SHA:     "new",
		Stacks:  []v1.Stack{{Key: "stacks/net", Path: "stacks/net"}, {Key: "stacks/edge", Path: "stacks/edge"}},
		Modules: []v1.Module{mod("v1.1.0"), wrapper},
		Edges: []v1.Edge{
			uses(v1.StackRef("stacks/net"), mod("v1.1.0")),
			uses(v1.StackRef("stacks/edge"), wrapper),
			uses(v1.ModuleRef(wrapper.Key), mod("v1.1.0")),
		},
	})
	f.saveGraph(other.ID, &v1.Graph{
		SHA:     "apps",
		Stacks:  []v1.Stack{{Key: "stacks/web", Path: "stacks/web"}},
		Modules: []v1.Module{mod("v1.0.0")},
		Edges:   []v1.Edge{uses(v1.StackRef("stacks/web"), mod("v1.0.0"))},
	})

	pinned, err := f.s.GetModuleByKey(f.ctx, "acme/modules//vpc@v1.0.0")
	require.NoError(t, err)
	family, err := f.s.GetModuleByKey(f.ctx, "acme/modules//vpc")
	require.NoError(t, err)
	require.True(t, family.Family())

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, v := range []string{"v1.0.0", "v1.1.0", "v1.2.0", "v2.0.0"} {
		require.NoError(t, f.s.RecordModuleVersion(f.ctx, pinned.ID, v, "sha-"+v, base.Add(time.Duration(i)*24*time.Hour)))
	}
	require.NoError(t, f.s.RecordModuleVersion(f.ctx, family.ID, "v2.0.0", "sha-v2-retag", base.Add(10*24*time.Hour)))
	require.ErrorIs(t, f.s.RecordModuleVersion(f.ctx, uuid.New(), "v1", "x", time.Time{}), store.ErrNotFound)
	require.ErrorIs(t, f.s.RecordModuleVersion(f.ctx, pinned.ID, "", "x", time.Time{}), store.ErrInvalid)

	versions, err := f.s.ListModuleVersions(f.ctx, pinned.ID)
	require.NoError(t, err)
	require.Len(t, versions, 4)
	assert.Equal(t, "v2.0.0", versions[0].Version)
	assert.Equal(t, "sha-v2-retag", versions[0].SHA, "recording a version again updates it")
	assert.Equal(t, family.ID, versions[0].ModuleID, "versions live on the family row")
	fromFamily, err := f.s.ListModuleVersions(f.ctx, family.ID)
	require.NoError(t, err)
	assert.Equal(t, versions, fromFamily)

	cases := []struct {
		name   string
		module uuid.UUID
		want   []store.ModuleConsumer
	}{
		{
			name:   "family lists every ref in latest graphs only",
			module: family.ID,
			want: []store.ModuleConsumer{
				{Repo: "acme/apps", StackKey: "stacks/web", ModuleKey: "acme/modules//vpc@v1.0.0", Ref: "v1.0.0", Latest: "v2.0.0", Behind: 3},
				{Repo: "acme/infra", StackKey: "stacks/edge", ModuleKey: "acme/modules//vpc@v1.1.0", Ref: "v1.1.0", Latest: "v2.0.0", Behind: 2},
				{Repo: "acme/infra", StackKey: "stacks/net", ModuleKey: "acme/modules//vpc@v1.1.0", Ref: "v1.1.0", Latest: "v2.0.0", Behind: 2},
			},
		},
		{
			name:   "pinned ref",
			module: pinned.ID,
			want: []store.ModuleConsumer{
				{Repo: "acme/apps", StackKey: "stacks/web", ModuleKey: "acme/modules//vpc@v1.0.0", Ref: "v1.0.0", Latest: "v2.0.0", Behind: 3},
			},
		},
		{name: "unknown module", module: uuid.New()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.s.ModuleConsumers(f.ctx, tc.module)
			require.NoError(t, err)
			require.Len(t, got, len(tc.want))
			if len(tc.want) == 0 {
				return
			}
			for i := range got {
				assert.NotEqual(t, uuid.Nil, got[i].StackID)
				got[i].StackID, got[i].RepoID, got[i].ModuleID = uuid.Nil, 0, uuid.Nil
			}
			assert.Equal(t, tc.want, got)
		})
	}

	consumers, err := f.s.ModuleConsumers(f.ctx, family.ID)
	require.NoError(t, err)
	assert.Equal(t, v1.ModuleConsumer{StackID: consumers[0].StackID.String(), Repo: "acme/apps", StackKey: "stacks/web", Ref: "v1.0.0", Behind: 3}, consumers[0].ToV1())

	edge, err := f.s.GetStackByKey(f.ctx, f.repo.ID, "stacks/edge")
	require.NoError(t, err)
	used, err := f.s.StackModules(f.ctx, edge.ID)
	require.NoError(t, err)
	consumed := make([]v1.ModuleConsume, 0, len(used))
	for _, u := range used {
		consumed = append(consumed, u.Consume())
	}
	assert.Equal(t, []v1.ModuleConsume{
		{ModuleKey: "acme/infra//modules/wrapper"},
		{ModuleKey: "acme/modules//vpc@v1.1.0", Ref: "v1.1.0", Latest: "v2.0.0", Behind: 2},
	}, consumed)

	_, oldGraphID, err := f.s.GetGraph(f.ctx, f.repo.ID, "old")
	require.NoError(t, err)
	require.NoError(t, f.s.SetDefaultGraph(f.ctx, f.repo.ID, oldGraphID))
	consumers, err = f.s.ModuleConsumers(f.ctx, family.ID)
	require.NoError(t, err)
	keys := make([]string, len(consumers))
	for i, c := range consumers {
		keys[i] = c.Repo + "//" + c.StackKey + "@" + c.Ref
	}
	assert.Equal(t, []string{"acme/apps//stacks/web@v1.0.0", "acme/infra//stacks/legacy@v1.0.0"}, keys,
		"the default-branch graph wins over newer graphs")
	used, err = f.s.StackModules(f.ctx, edge.ID)
	require.NoError(t, err)
	assert.Empty(t, used, "the stack is not in the default-branch graph")
}
