//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/store"
)

func TestInstallationLifecycle(t *testing.T) {
	e := shared(t)
	const inst, account = 21, "globex"
	infra := newFixture(t, e, "infra", withInstallation(inst, account), notInstalled())
	network := newFixture(t, e, "network", withInstallation(inst, account), notInstalled(), withConfig(func(s string) string {
		return strings.Replace(s, "tool: terraform", "tool: tofu", 1)
	}))

	e.GH.AddInstallation(inst, account, infra.name)
	infra.deliver(gh.EventInstallation, e.GH.InstallationEvent("created", inst))
	in, err := e.Store.GetInstallation(t.Context(), inst)
	require.NoError(t, err, "installation created records the installation")
	assert.Equal(t, account, in.Account)
	assert.Nil(t, in.SuspendedAt)
	repo, err := e.Store.GetRepo(t.Context(), infra.id)
	require.NoError(t, err, "and its repositories")
	assert.Equal(t, infra.name, repo.FullName)
	assert.Equal(t, int64(inst), repo.InstallationID)
	assert.Equal(t, "main", repo.DefaultBranch)
	require.NotNil(t, repo.Config, "stackorder.yaml is read from the default branch through the Contents API")
	assert.Equal(t, v1.ToolTerraform, repo.Config.Tool)
	assert.Equal(t, map[string]string{"stacks/prod/": "production", "stacks/staging/": "staging"}, repo.Config.Environments)
	assert.Equal(t, v1.ApplyBeforeMerge, repo.Config.Apply.Mode)
	assert.Equal(t, infra.co.base, repo.ConfigSHA)
	_, err = e.Store.GetRepo(t.Context(), network.id)
	require.ErrorIs(t, err, store.ErrNotFound, "a repository outside the installation is unknown")

	e.GH.AddInstallation(inst, account, network.name)
	network.deliver(gh.EventInstallationRepositories, network.reposEvent("added", true))
	repo, err = e.Store.GetRepo(t.Context(), network.id)
	require.NoError(t, err, "a repository added to the installation is recorded")
	require.NotNil(t, repo.Config)
	assert.Equal(t, v1.ToolTofu, repo.Config.Tool, "with its own stackorder.yaml")

	infra.deliver(gh.EventInstallationRepositories, infra.reposEvent("removed", false))
	_, err = e.Store.GetRepo(t.Context(), infra.id)
	require.ErrorIs(t, err, store.ErrNotFound, "a repository removed from the installation is forgotten")
	var page v1.Page[v1.RepoSummary]
	e.getJSON("/v1/repos?limit=200", &page)
	var names []string
	for _, r := range page.Items {
		if strings.HasPrefix(r.FullName, account+"/") {
			names = append(names, r.FullName)
		}
	}
	assert.Equal(t, []string{network.name}, names)

	ev := network.openPR(90, network.co.head, "feature/vpc-subnet")
	e.GH.SuspendInstallation(inst)
	network.deliver(gh.EventInstallation, e.GH.InstallationEvent("suspend", inst))
	in, err = e.Store.GetInstallation(t.Context(), inst)
	require.NoError(t, err)
	assert.NotNil(t, in.SuspendedAt, "installation suspend is recorded")
	repo, err = e.Store.GetRepo(t.Context(), network.id)
	require.NoError(t, err)
	assert.True(t, repo.Suspended)

	res := network.planWorkflow(ev).run("resolve")
	requireExit(t, 3, res)
	assert.Contains(t, res.stderr, "the App installation of "+network.name+" is suspended")
	network.deliver(gh.EventPullRequest, e.GH.PullRequestEvent("synchronize", network.name, ev.PullRequest))
	assert.Empty(t, e.GH.CheckRuns(network.name), "a suspended installation's webhooks are ignored")
	var n int
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE repo_id = $1`, network.id).Scan(&n))
	assert.Zero(t, n, "no run is created for a suspended installation")
}
