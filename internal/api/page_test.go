package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func TestPageParams(t *testing.T) {
	cases := []struct {
		query  string
		limit  int
		cursor string
		bad    bool
	}{
		{query: "", limit: defaultPageSize},
		{query: "?limit=5&cursor=abc", limit: 5, cursor: "abc"},
		{query: "?limit=100000", limit: maxPageSize},
		{query: "?limit=0", bad: true},
		{query: "?limit=-3", bad: true},
		{query: "?limit=ten", bad: true},
	}
	for _, tc := range cases {
		limit, cursor, err := pageParams(newRequest(t, http.MethodGet, "/v1/repos"+tc.query, nil))
		if tc.bad {
			var bad *principal.InvalidError
			require.ErrorAs(t, err, &bad, tc.query)
			assert.Equal(t, "limit", bad.Field)
			continue
		}
		require.NoError(t, err, tc.query)
		assert.Equal(t, tc.limit, limit, tc.query)
		assert.Equal(t, tc.cursor, cursor, tc.query)
	}
}

func TestPageByKey(t *testing.T) {
	items := []string{"d", "a", "c", "b", "e"}
	key := func(s string) string { return s }

	var (
		all    []string
		cursor string
		pages  int
	)
	for {
		page, next, err := pageByKey(items, key, 2, cursor)
		require.NoError(t, err)
		all = append(all, page...)
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, all)
	assert.Equal(t, 3, pages)
	assert.Equal(t, []string{"d", "a", "c", "b", "e"}, items, "the input is not reordered")

	page, next, err := pageByKey(items, key, 10, encodeKeyCursor("bb"))
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "d", "e"}, page, "a cursor between keys resumes after it")
	assert.Empty(t, next)

	page, next, err = pageByKey([]string{}, key, 10, "")
	require.NoError(t, err)
	assert.Equal(t, []string{}, page)
	assert.Empty(t, next)

	for _, bad := range []string{"!!!", "="} {
		_, _, err = pageByKey(items, key, 2, bad)
		assert.ErrorIs(t, err, principal.ErrInvalid, bad)
	}
	assert.ErrorIs(t, storeCursorError(store.ErrInvalid), principal.ErrInvalid)
}

func TestModuleOwner(t *testing.T) {
	cases := map[string]string{
		"acme/infra//modules/vpc":                      "acme",
		"acme/modules//vpc":                            "acme",
		"acme/modules//vpc@v1.2.0":                     "acme",
		"gitlab.com/acme/modules//vpc":                 "",
		"registry:terraform-aws-modules/vpc/aws":       "",
		"registry:terraform-aws-modules/vpc/aws@5.1.0": "",
		"acme//x":      "",
		"no-separator": "",
	}
	for key, want := range cases {
		assert.Equal(t, want, moduleOwner(key), key)
	}
	assert.Equal(t, "acme", repoOwner("acme/infra"))
}

func TestAuditVisible(t *testing.T) {
	sess := identity{
		Principal: principal.Principal{Kind: principal.Session, Login: "octocat"},
		session:   &store.Session{Login: "octocat", Orgs: []string{"acme"}},
	}
	key := identity{Principal: principal.Principal{Kind: principal.APIKey, Login: "ci"}}
	cases := []struct {
		entry store.AuditEntry
		want  bool
	}{
		{store.AuditEntry{Actor: "octocat", Target: "globex/platform//x"}, true},
		{store.AuditEntry{Actor: "hubot", Target: "acme/infra//stacks/prod/vpc"}, true},
		{store.AuditEntry{Actor: "hubot", Target: "Acme/infra"}, true},
		{store.AuditEntry{Actor: "hubot", Target: "globex/platform//stacks/a"}, false},
		{store.AuditEntry{Actor: "hubot", Target: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Details: map[string]any{"repo": "acme/infra"}}, true},
		{store.AuditEntry{Actor: "hubot", Target: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Details: map[string]any{"repo": "globex/platform"}}, false},
		{store.AuditEntry{Actor: "hubot", Target: "7c9e6679-7425-40de-944b-e07fc1f90ae7"}, false},
		{store.AuditEntry{Actor: "hubot"}, false},
		{store.AuditEntry{Actor: "hubot", Target: "pr:acme/infra#7"}, true},
		{store.AuditEntry{Actor: "", Target: "repo:acme/infra"}, true},
		{store.AuditEntry{Actor: "", Target: "workflow_run:acme/infra:4242"}, true},
		{store.AuditEntry{Actor: "hubot", Target: "pr:globex/platform#7"}, false},
		{store.AuditEntry{Actor: "hubot", Target: "stack:7c9e6679-7425-40de-944b-e07fc1f90ae7", Details: map[string]any{"repo": "acme/infra"}}, true},
		{store.AuditEntry{Actor: "hubot", Target: "stack:7c9e6679-7425-40de-944b-e07fc1f90ae7"}, false},
		{store.AuditEntry{Actor: "", Target: "lock:7c9e6679-7425-40de-944b-e07fc1f90ae7:2026-09-28"}, false},
		{store.AuditEntry{Actor: "hubot", Target: "acme/infra//stacks/prod/apps:blue"}, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, auditVisible(sess, tc.entry), "%+v", tc.entry)
		assert.True(t, auditVisible(key, tc.entry), "%+v", tc.entry)
	}
}

func TestAuditVisibleSystemActorLogin(t *testing.T) {
	sess := identity{
		Principal: principal.Principal{Kind: principal.Session, Login: "StackOrder"},
		session:   &store.Session{Login: "StackOrder", Orgs: []string{"acme"}},
	}
	cases := []struct {
		entry store.AuditEntry
		want  bool
	}{
		{store.AuditEntry{Actor: store.SystemActor, Action: "cross_repo_plan", Target: "pr:globex/platform#7"}, false},
		{store.AuditEntry{Actor: store.SystemActor, Action: "lock_warning", Target: "repo:globex/platform"}, false},
		{store.AuditEntry{Actor: store.SystemActor, Action: "deployment_rejected", Target: "workflow_run:globex/platform:4242"}, false},
		{store.AuditEntry{Actor: store.SystemActor, Action: "unlock", Target: "globex/platform//stacks/prod/eks"}, false},
		{store.AuditEntry{Actor: store.SystemActor, Action: "unlock", Target: "acme/infra//stacks/prod/vpc"}, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, auditVisible(sess, tc.entry), "%+v", tc.entry)
	}
}

func TestReplay(t *testing.T) {
	g := &v1.Graph{Stacks: []v1.Stack{{Key: "a", Tool: v1.ToolTofu, ToolVersion: "1.9.0", PlanOutput: "summary"}}}
	rows := []store.RunStack{
		{Key: "a", Path: "a", Wave: 0, Reasons: []v1.Reason{v1.ReasonChanged}, Environment: "production"},
		{Key: "c", Path: "c", Wave: 2, Environment: "default"},
		{Key: "d", Path: "d", Wave: 1, Reasons: []v1.Reason{v1.ReasonDependent}, Environment: "default", Via: []string{"a"}},
		{Key: "infra/kyc:production", Path: "infra/kyc", Workspace: "prod", Wave: 3, Environment: "production"},
	}
	affected, waves := replay(store.Run{Waves: 4}, rows, g)
	assert.Equal(t, []v1.AffectedStack{
		{Key: "a", Path: "a", Wave: 0, Reasons: []v1.Reason{v1.ReasonChanged}, Environment: "production", Tool: v1.ToolTofu, ToolVersion: "1.9.0", PlanOutput: "summary"},
		{Key: "c", Path: "c", Wave: 2, Reasons: []v1.Reason{}, Environment: "default"},
		{Key: "d", Path: "d", Wave: 1, Reasons: []v1.Reason{v1.ReasonDependent}, Environment: "default", Via: []string{"a"}},
		{Key: "infra/kyc:production", Path: "infra/kyc", Instance: "production", Workspace: "prod", Wave: 3, Reasons: []v1.Reason{}, Environment: "production"},
	}, affected)
	assert.Equal(t, [][]string{{"a"}, {"d"}, {"c"}, {"infra/kyc:production"}}, waves)

	affected, waves = replay(store.Run{}, nil, g)
	assert.Equal(t, []v1.AffectedStack{}, affected)
	assert.Equal(t, [][]string{}, waves)
}
