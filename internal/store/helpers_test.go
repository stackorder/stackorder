//go:build integration

package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	s    *store.Store
	repo store.Repo
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: t.Context(), s: pgtest.New(t)}
	f.repo = f.addRepo(1, 100, "acme", "acme/infra")
	return f
}

func (f *fixture) addRepo(installationID, repoID int64, account, fullName string) store.Repo {
	f.t.Helper()
	_, err := f.s.UpsertInstallation(f.ctx, store.Installation{ID: installationID, Account: account, AccountType: "Organization"})
	require.NoError(f.t, err)
	repo, err := f.s.UpsertRepo(f.ctx, store.RepoParams{ID: repoID, InstallationID: installationID, FullName: fullName, DefaultBranch: "main"})
	require.NoError(f.t, err)
	return repo
}

func (f *fixture) saveGraph(repoID int64, g *v1.Graph) (uuid.UUID, map[string]uuid.UUID) {
	f.t.Helper()
	id, ids, err := f.s.SaveGraph(f.ctx, repoID, g)
	require.NoError(f.t, err)
	return id, ids
}

func (f *fixture) stacks(keys ...string) map[string]uuid.UUID {
	f.t.Helper()
	g := &v1.Graph{Repo: f.repo.FullName, SHA: "sha-stacks-" + uuid.NewString()}
	for _, k := range keys {
		g.Stacks = append(g.Stacks, v1.Stack{Key: k, Path: k})
	}
	_, ids := f.saveGraph(f.repo.ID, g)
	return ids
}

func (f *fixture) run(p store.CreateRunParams) store.Run {
	f.t.Helper()
	if p.RepoID == 0 {
		p.RepoID = f.repo.ID
	}
	if p.SHA == "" {
		p.SHA = "abc123"
	}
	if p.Trigger == "" {
		p.Trigger = v1.TriggerPullRequest
	}
	if p.Mode == "" {
		p.Mode = v1.ModePlan
	}
	r, err := f.s.CreateRun(f.ctx, p)
	require.NoError(f.t, err)
	return r
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.s.Pool().Exec(f.ctx, sql, args...)
	require.NoError(f.t, err)
}

func ptr[T any](v T) *T { return &v }

func sampleGraph(repo, sha string) *v1.Graph {
	return &v1.Graph{
		Repo:     repo,
		SHA:      sha,
		TreeHash: "tree-" + sha,
		Stacks: []v1.Stack{
			{
				Key: "stacks/prod/vpc", Path: "stacks/prod/vpc", Environment: "production", Tool: v1.ToolTofu,
				Backend: &v1.Backend{Type: "s3", Bucket: "state", Key: "prod/vpc.tfstate", Region: "eu-west-1"},
			},
			{
				Key: "stacks/prod/eks", Path: "stacks/prod/eks", Environment: "production",
				Config: &v1.StackConfig{DependsOn: []string{"stacks/prod/vpc"}},
			},
			{Key: "stacks/prod/apps:blue", Path: "stacks/prod/apps", Instance: "blue", Workspace: "blue"},
			{Key: "stacks/prod/tgw", Path: "stacks/prod/tgw", Repo: "acme/network", External: true},
		},
		Modules: []v1.Module{
			{Key: "acme/infra//modules/vpc", Kind: v1.ModuleLocal, Path: "modules/vpc", Source: "../../modules/vpc"},
			{Key: "acme/modules//eks@v1.2.0", Kind: v1.ModuleGit, Source: "git::https://github.com/acme/modules.git//eks?ref=v1.2.0", Ref: "v1.2.0"},
		},
		Edges: []v1.Edge{
			{From: v1.StackRef("stacks/prod/vpc"), To: v1.ModuleRef("acme/infra//modules/vpc"), Type: v1.EdgeUsesModule},
			{From: v1.StackRef("stacks/prod/eks"), To: v1.ModuleRef("acme/modules//eks@v1.2.0"), Type: v1.EdgeUsesModule, Meta: map[string]string{"ref": "v1.2.0"}},
			{From: v1.StackRef("stacks/prod/eks"), To: v1.StackRef("stacks/prod/vpc"), Type: v1.EdgeDependsOn},
			{From: v1.StackRef("stacks/prod/apps:blue"), To: v1.StackRef("stacks/prod/eks"), Type: v1.EdgeReadsState, Inferred: true, Meta: map[string]string{"bucket": "state", "key": "prod/eks.tfstate"}},
			{From: v1.StackRef("stacks/prod/vpc"), To: v1.StackRef("acme/network//stacks/prod/tgw"), Type: v1.EdgeDependsOn},
		},
		Warnings: []string{"inferred edge from stacks/prod/apps:blue"},
	}
}
