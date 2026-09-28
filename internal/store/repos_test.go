//go:build integration

package store_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/store"
)

func TestInstallations(t *testing.T) {
	f := newFixture(t)

	in, err := f.s.UpsertInstallation(f.ctx, store.Installation{ID: 1, Account: "acme-renamed", AccountType: "Organization"})
	require.NoError(t, err)
	assert.Equal(t, "acme-renamed", in.Account)

	require.NoError(t, f.s.SuspendInstallation(f.ctx, 1, true))
	got, err := f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, got.SuspendedAt)
	first := *got.SuspendedAt

	require.NoError(t, f.s.SuspendInstallation(f.ctx, 1, true))
	got, err = f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, first, *got.SuspendedAt, "suspending twice keeps the first time")

	repo, err := f.s.GetRepo(f.ctx, f.repo.ID)
	require.NoError(t, err)
	assert.True(t, repo.Suspended)

	_, err = f.s.UpsertInstallation(f.ctx, store.Installation{ID: 1, Account: "acme"})
	require.NoError(t, err)
	got, err = f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	assert.NotNil(t, got.SuspendedAt, "upsert leaves suspension alone")

	require.NoError(t, f.s.SuspendInstallation(f.ctx, 1, false))
	got, err = f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	assert.Nil(t, got.SuspendedAt)

	require.ErrorIs(t, f.s.SuspendInstallation(f.ctx, 404, true), store.ErrNotFound)
	_, err = f.s.GetInstallation(f.ctx, 404)
	require.ErrorIs(t, err, store.ErrNotFound)

	f.stacks("stacks/a")
	require.NoError(t, f.s.DeleteInstallation(f.ctx, 1))
	require.NoError(t, f.s.DeleteInstallation(f.ctx, 1), "deleting twice is not an error")
	_, err = f.s.GetRepo(f.ctx, f.repo.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "repos go with their installation")
	stacks, err := f.s.ListStacks(f.ctx, f.repo.ID, true)
	require.NoError(t, err)
	assert.Empty(t, stacks)
}

func TestListInstallations(t *testing.T) {
	f := newFixture(t)
	f.addRepo(3, 300, "Globex", "Globex/platform")
	f.addRepo(2, 200, "acme-labs", "acme-labs/sandbox")
	require.NoError(t, f.s.SuspendInstallation(f.ctx, 2, true))

	got, err := f.s.ListInstallations(f.ctx)
	require.NoError(t, err)
	accounts := make([]string, len(got))
	for i, in := range got {
		accounts[i] = in.Account
	}
	assert.Equal(t, []string{"acme", "acme-labs", "Globex"}, accounts)
	assert.Nil(t, got[0].SuspendedAt)
	assert.NotNil(t, got[1].SuspendedAt, "suspended installations are listed")

	require.NoError(t, f.s.DeleteInstallation(f.ctx, 1))
	require.NoError(t, f.s.DeleteInstallation(f.ctx, 2))
	require.NoError(t, f.s.DeleteInstallation(f.ctx, 3))
	got, err = f.s.ListInstallations(f.ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRepos(t *testing.T) {
	f := newFixture(t)
	other := f.addRepo(2, 200, "globex", "globex/platform")

	cases := []struct {
		name     string
		accounts []string
		want     []string
	}{
		{"all", nil, []string{"acme/infra", "globex/platform"}},
		{"one account", []string{"ACME"}, []string{"acme/infra"}},
		{"two accounts", []string{"acme", "globex"}, []string{"acme/infra", "globex/platform"}},
		{"unknown account", []string{"initech"}, nil},
		{"empty account name matches nothing", []string{""}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos, err := f.s.ListRepos(f.ctx, tc.accounts...)
			require.NoError(t, err)
			var names []string
			for _, r := range repos {
				names = append(names, r.FullName)
			}
			assert.Equal(t, tc.want, names)
		})
	}

	byName, err := f.s.GetRepoByName(f.ctx, "ACME/Infra")
	require.NoError(t, err)
	assert.Equal(t, f.repo.ID, byName.ID)
	assert.Equal(t, "acme", byName.Account)
	assert.Equal(t, "main", byName.DefaultBranch)

	cfg := config.Default()
	cfg.Tool = v1.ToolTofu
	require.NoError(t, f.s.UpdateRepoConfig(f.ctx, f.repo.ID, cfg, "cfgsha"))
	got, err := f.s.GetRepo(f.ctx, f.repo.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Config)
	assert.Equal(t, v1.ToolTofu, got.Config.Tool)
	assert.Equal(t, "cfgsha", got.ConfigSHA)
	require.ErrorIs(t, f.s.UpdateRepoConfig(f.ctx, 404, cfg, "x"), store.ErrNotFound)

	renamed, err := f.s.UpsertRepo(f.ctx, store.RepoParams{ID: f.repo.ID, InstallationID: 1, FullName: "acme/infrastructure", Private: true})
	require.NoError(t, err)
	assert.Equal(t, "acme/infrastructure", renamed.FullName)
	assert.Equal(t, "main", renamed.DefaultBranch, "empty default branch keeps the stored one")
	assert.True(t, renamed.Private)
	require.NotNil(t, renamed.Config, "upsert keeps the stored config")

	reused, err := f.s.UpsertRepo(f.ctx, store.RepoParams{ID: 300, InstallationID: 2, FullName: "globex/platform", DefaultBranch: "trunk"})
	require.NoError(t, err)
	assert.Equal(t, "trunk", reused.DefaultBranch)
	stale, err := f.s.GetRepo(f.ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, "globex/platform~200", stale.FullName, "a stale row holding the name is moved aside")

	_, err = f.s.UpsertRepo(f.ctx, store.RepoParams{ID: 400, InstallationID: 404, FullName: "nobody/x"})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.s.UpsertRepo(f.ctx, store.RepoParams{})
	require.ErrorIs(t, err, store.ErrInvalid)

	require.NoError(t, f.s.DeleteRepo(f.ctx, 300))
	_, err = f.s.GetRepo(f.ctx, 300)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestUpsertRepoConcurrently(t *testing.T) {
	f := newFixture(t)
	const writers = 8
	for round := range 3 {
		id := int64(500 + round)
		name := fmt.Sprintf("acme/concurrent-%d", round)
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for i := range writers {
			wg.Go(func() {
				_, errs[i] = f.s.UpsertRepo(f.ctx, store.RepoParams{ID: id, InstallationID: 1, FullName: name, DefaultBranch: "main"})
			})
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err, "concurrent upserts of a new repository all succeed")
		}
		got, err := f.s.GetRepo(f.ctx, id)
		require.NoError(t, err)
		assert.Equal(t, name, got.FullName)
	}
}
