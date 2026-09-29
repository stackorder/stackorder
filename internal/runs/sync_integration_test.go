//go:build integration

package runs_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/store"
)

func TestSyncInstallations(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRepo("beta/infra", gh.Repository{ID: 700, DefaultBranch: "trunk", Private: true})
	e.gh.SetRepo("beta/apps", gh.Repository{ID: 701, DefaultBranch: "main"})
	e.gh.AddInstallation(2, "beta", "beta/infra", "beta/apps")
	e.gh.SetContents("beta/infra", "trunk", "stackorder.yaml", []byte("version: 1\ntool: tofu\n"))
	e.gh.SetRef("beta/infra", "heads/trunk", headSHA)
	e.gh.SetRepo("gamma/infra", gh.Repository{ID: 800})
	e.gh.AddInstallation(3, "gamma", "gamma/infra")
	e.gh.SuspendInstallation(3)

	require.NoError(t, e.svc.SyncInstallations(e.ctx))

	infra, err := e.st.GetRepo(e.ctx, 700)
	require.NoError(t, err)
	assert.Equal(t, int64(2), infra.InstallationID)
	assert.Equal(t, "beta/infra", infra.FullName)
	assert.Equal(t, "trunk", infra.DefaultBranch)
	assert.True(t, infra.Private)
	require.NotNil(t, infra.Config, "stackorder.yaml is loaded for a repository not seen before")
	assert.Equal(t, v1.ToolTofu, infra.Config.Tool)
	assert.Equal(t, headSHA, infra.ConfigSHA)
	apps, err := e.st.GetRepo(e.ctx, 701)
	require.NoError(t, err)
	assert.Nil(t, apps.Config, "a repository without stackorder.yaml gets defaults")

	gamma, err := e.st.GetInstallation(e.ctx, 3)
	require.NoError(t, err)
	assert.Equal(t, "gamma", gamma.Account)
	assert.NotNil(t, gamma.SuspendedAt, "a suspended installation is recorded as suspended")
	_, err = e.st.GetRepo(e.ctx, 800)
	require.ErrorIs(t, err, store.ErrNotFound, "a suspended installation's repositories cannot be listed")

	known, err := e.st.GetRepo(e.ctx, repoID)
	require.NoError(t, err)
	assert.Equal(t, e.repo.Config, known.Config, "a known repository keeps its configuration")
	assert.Equal(t, mainSHA, known.ConfigSHA)

	e.gh.SetContents("beta/infra", "trunk", "stackorder.yaml", []byte("version: 1\ntool: terraform\n"))
	e.gh.SetRepo("beta/infra", gh.Repository{ID: 700, DefaultBranch: "release", Private: true})
	require.NoError(t, e.svc.HandleJob(e.ctx, runs.JobSyncInstallations, nil))
	infra, err = e.st.GetRepo(e.ctx, 700)
	require.NoError(t, err)
	assert.Equal(t, "release", infra.DefaultBranch, "GitHub's fields of a known repository are refreshed")
	assert.Equal(t, v1.ToolTofu, infra.Config.Tool, "a known repository's configuration is left to push events")

	insts, err := e.st.ListInstallations(e.ctx)
	require.NoError(t, err)
	assert.Len(t, insts, 3)
}

func TestSyncInstallationsReadsConfigurationOnce(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRepo("beta/apps", gh.Repository{ID: 701, DefaultBranch: "main"})
	e.gh.SetRef("beta/apps", "heads/main", headSHA)
	e.gh.SetRepo("beta/empty", gh.Repository{ID: 702, DefaultBranch: "main"})
	e.gh.AddInstallation(2, "beta", "beta/apps", "beta/empty")
	require.NoError(t, e.svc.SyncInstallations(e.ctx))
	apps, err := e.st.GetRepo(e.ctx, 701)
	require.NoError(t, err)
	assert.Nil(t, apps.Config)
	assert.Equal(t, headSHA, apps.ConfigSHA, "the commit without stackorder.yaml is recorded")

	configReads := func(since int) map[string]int {
		reads := map[string]int{}
		for _, r := range e.gh.Requests()[since:] {
			if strings.Contains(r.Pattern, "/contents/") || strings.Contains(r.Pattern, "/git/ref/") {
				reads[r.Path]++
			}
		}
		return reads
	}
	before := len(e.gh.Requests())
	require.NoError(t, e.svc.SyncInstallations(e.ctx))
	reads := configReads(before)
	for path := range reads {
		assert.NotContains(t, path, "/beta/apps/", "a repository read at a commit without stackorder.yaml is left to push events")
		assert.NotContains(t, path, "/acme/infra/", "a configured repository is left to push events")
	}
	assert.NotEmpty(t, reads, "a repository whose default branch had no commit is read again")
}

func TestSyncInstallationsReportsFailures(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.FailNext("GET /app/installations", 404, 1)
	err := e.svc.SyncInstallations(e.ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list installations")

	e.gh.SetRepo("beta/infra", gh.Repository{ID: 700, DefaultBranch: "trunk"})
	e.gh.AddInstallation(2, "beta", "beta/infra")
	e.gh.FailNext("GET /installation/repositories", 404, 1)
	err = e.svc.SyncInstallations(e.ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repositories of installation")
	beta, err := e.st.GetInstallation(e.ctx, 2)
	require.NoError(t, err, "the installation is recorded even when its repositories fail")
	assert.Equal(t, "beta", beta.Account)
}

func TestSyncInstallationsForgetsWhatGitHubNoLongerLists(t *testing.T) {
	e := newEnv(t, baseConfig())
	e.gh.SetRepo("gamma/infra", gh.Repository{ID: 800, DefaultBranch: "main"})
	e.gh.AddInstallation(3, "gamma", "gamma/infra")
	e.gh.SuspendInstallation(3)
	stale := func() {
		t.Helper()
		for _, inst := range []store.Installation{{ID: 9, Account: "gone"}, {ID: 3, Account: "gamma"}} {
			_, err := e.st.UpsertInstallation(e.ctx, inst)
			require.NoError(t, err)
		}
		for _, r := range []store.RepoParams{
			{ID: 900, InstallationID: 9, FullName: "gone/infra"},
			{ID: 101, InstallationID: instID, FullName: "acme/old"},
			{ID: 800, InstallationID: 3, FullName: "gamma/infra"},
		} {
			_, err := e.st.UpsertRepo(e.ctx, r)
			require.NoError(t, err)
		}
	}
	stale()

	e.gh.FailNext("GET /installation/repositories", 404, 1)
	require.Error(t, e.svc.SyncInstallations(e.ctx))
	for _, id := range []int64{900, 101, 800, repoID} {
		_, err := e.st.GetRepo(e.ctx, id)
		require.NoError(t, err, "a failed repository listing forgets nothing, repository %d included", id)
	}
	_, err := e.st.GetInstallation(e.ctx, 9)
	require.NoError(t, err, "nor any installation")

	e.gh.FailNext("GET /app/installations", 404, 1)
	require.Error(t, e.svc.SyncInstallations(e.ctx))
	_, err = e.st.GetInstallation(e.ctx, 9)
	require.NoError(t, err, "a failed installation listing forgets nothing")

	require.NoError(t, e.svc.SyncInstallations(e.ctx))
	_, err = e.st.GetInstallation(e.ctx, 9)
	require.ErrorIs(t, err, store.ErrNotFound, "an installation GitHub no longer lists is forgotten")
	_, err = e.st.GetRepo(e.ctx, 900)
	require.ErrorIs(t, err, store.ErrNotFound, "with its repositories")
	_, err = e.st.GetRepo(e.ctx, 101)
	require.ErrorIs(t, err, store.ErrNotFound, "a repository its installation no longer lists is forgotten")
	known, err := e.st.GetRepo(e.ctx, repoID)
	require.NoError(t, err, "a listed repository is kept")
	assert.Equal(t, e.repo.Config, known.Config)
	_, err = e.st.GetRepo(e.ctx, 800)
	require.NoError(t, err, "a suspended installation's repositories cannot be listed, so they are kept")
	gamma, err := e.st.GetInstallation(e.ctx, 3)
	require.NoError(t, err)
	assert.NotNil(t, gamma.SuspendedAt)
}
