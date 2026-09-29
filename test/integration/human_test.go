//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
)

type browser struct {
	t      *testing.T
	e      *Env
	client *http.Client
}

func newBrowser(t *testing.T, e *Env) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &browser{t: t, e: e, client: &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

type reply struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
}

func (b *browser) do(method, target string, body any, header http.Header) (reply, string) {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(b.t, err)
		r = strings.NewReader(string(data))
	}
	if strings.HasPrefix(target, "/") {
		target = b.e.URL(target)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, r)
	require.NoError(b.t, err)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := b.client.Do(req)
	require.NoError(b.t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(b.t, err)
	return reply{status: resp.StatusCode, header: resp.Header, cookies: resp.Cookies()}, string(data)
}

func (b *browser) get(path string, out any) {
	b.t.Helper()
	resp, body := b.do(http.MethodGet, path, nil, nil)
	require.Equal(b.t, http.StatusOK, resp.status, "GET %s: %s", path, body)
	require.NoError(b.t, json.Unmarshal([]byte(body), out), "GET %s", path)
}

func (b *browser) signIn(login string) (reply, string) {
	b.t.Helper()
	resp, body := b.do(http.MethodGet, "/auth/login?next=/repos", nil, nil)
	require.Equal(b.t, http.StatusFound, resp.status, body)
	authorize, err := url.Parse(resp.header.Get("Location"))
	require.NoError(b.t, err)
	require.True(b.t, strings.HasPrefix(authorize.String(), b.e.GH.URL()+"/login/oauth/authorize?"), "login redirects to GitHub: %s", authorize)
	assert.Equal(b.t, OAuthClientID, authorize.Query().Get("client_id"))
	assert.Equal(b.t, b.e.BaseURL+"/auth/callback", authorize.Query().Get("redirect_uri"))
	assert.Equal(b.t, "read:org", authorize.Query().Get("scope"))
	state := authorize.Query().Get("state")
	require.NotEmpty(b.t, state)
	code := "code-" + login
	b.e.GH.AddOAuthCode(code, login)
	return b.do(http.MethodGet, "/auth/callback?code="+code+"&state="+url.QueryEscape(state), nil, nil)
}

func addRepo(t *testing.T, e *Env, name string, inst int64, account string) int64 {
	t.Helper()
	id := repoIDs.Add(1)
	e.GH.SetRepo(name, gh.Repository{ID: id, DefaultBranch: "main"})
	e.GH.SetRef(name, "heads/main", strings.Repeat("ab", 20))
	e.GH.SetContents(name, "main", "stackorder.yaml", []byte("version: 1\n"))
	e.GH.AddInstallation(inst, account, name)
	f := &fixture{t: t, e: e, name: name, account: account, id: id, inst: inst}
	f.deliver(gh.EventInstallationRepositories, f.reposEvent("added", true))
	return id
}

func TestHumanAPI(t *testing.T) {
	e := shared(t)
	const inst, org, user = 31, "initech", "peter"
	modules := org + "/tf-modules"
	f := newFixture(t, e, "platform", withInstallation(inst, org), withEdit(func(root string) {
		editFile(t, root, "stacks/prod/apps/main.tf", func(s string) string {
			return s + "\nmodule \"alb\" {\n  source = \"git::https://github.com/" + modules + ".git//alb?ref=v1.0.0\"\n\n  name = var.name\n}\n"
		})
	}))
	addRepo(t, e, modules, inst, org)
	e.GH.SetCollaboratorPermission(f.name, user, "write")
	e.GH.SetOrgMembership(org, user, gh.MembershipActive, "member")
	hidden := newFixture(t, e, "hidden")

	ev := f.openPR(100, f.co.head, "feature/vpc-subnet")
	p := f.plan(ev)
	f.approve(100, reviewer, f.co.head)
	cmd := f.comment(100, applier, applyCommand)
	require.Equal(t, []string{gh.ReactionEyes, gh.ReactionRocket}, e.GH.Reactions(cmd.ID))
	require.Len(t, f.locks(), 5)

	b := newBrowser(t, e)
	resp, body := b.signIn(user)
	require.Equal(t, http.StatusFound, resp.status, body)
	assert.Equal(t, "/repos", resp.header.Get("Location"), "the callback returns to the page that asked for sign-in")
	var session *http.Cookie
	for _, c := range resp.cookies {
		if c.Name == "stackorder_session" {
			session = c
		}
	}
	require.NotNil(t, session, "the callback sets the session cookie")
	assert.True(t, session.HttpOnly)
	var sessions int
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE login = $1`, user).Scan(&sessions))
	assert.Equal(t, 1, sessions, "the session is stored")

	var me v1.Whoami
	b.get("/v1/me", &me)
	assert.Equal(t, user, me.Login)
	assert.Equal(t, []string{org}, me.Orgs)
	assert.False(t, me.Admin)

	var repos v1.Page[v1.RepoSummary]
	b.get("/v1/repos", &repos)
	names := make([]string, 0, len(repos.Items))
	for _, r := range repos.Items {
		names = append(names, r.FullName)
	}
	assert.Equal(t, []string{f.name, modules}, names, "a person sees only the repositories of their organisations")
	for _, r := range repos.Items {
		if r.FullName == f.name {
			assert.Equal(t, 6, r.Stacks)
			assert.Equal(t, 5, r.LocksHeld)
		}
	}
	var overview v1.Overview
	b.get("/v1/overview", &overview)
	assert.Equal(t, 2, overview.Repos)
	assert.Equal(t, 6, overview.Stacks)
	assert.Equal(t, 5, overview.LocksHeld)
	assert.Equal(t, 1, overview.RunsByStatus[v1.RunPlanned])
	assert.Equal(t, 1, overview.RunsByStatus[v1.RunApplying])
	resp, _ = b.do(http.MethodGet, "/v1/repos/"+hidden.name+"/runs", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.status, "another organisation's repository does not exist for this person")

	var replay v1.GraphView
	b.get("/v1/repos/"+f.name+"/graph?run="+p.runID, &replay)
	assert.Equal(t, f.co.head, replay.SHA)
	var planWaves [][]string
	requireJSON(t, p.resolve.outputs["waves"], &planWaves)
	assert.Equal(t, planWaves, replay.Waves, "the graph replays the run's affected set")
	assert.Len(t, replay.Affected, 5)
	assert.Len(t, replay.StackIDs, 6)

	var run v1.Run
	b.get("/v1/runs/"+p.runID, &run)
	assert.Equal(t, v1.RunPlanned, run.Status)
	assert.Len(t, run.Stacks, 5)

	vpcID := f.stackID(prodVPC)
	resp, body = b.do(http.MethodPost, "/v1/stacks/"+vpcID+"/unlock", v1.UnlockRequest{Reason: "reverted by hand"}, nil)
	assert.Equal(t, http.StatusForbidden, resp.status, "a session needs a same-origin request to unlock: %s", body)
	assert.Contains(t, f.locks(), prodVPC)
	resp, body = b.do(http.MethodPost, "/v1/stacks/"+vpcID+"/unlock", v1.UnlockRequest{Reason: "reverted by hand"},
		http.Header{"Origin": {e.BaseURL}})
	require.Equal(t, http.StatusOK, resp.status, body)
	var unlocked v1.UnlockResponse
	requireJSON(t, body, &unlocked)
	require.Len(t, unlocked.Released, 1)
	assert.Equal(t, prodVPC, unlocked.Released[0].StackKey)
	assert.NotContains(t, f.locks(), prodVPC)
	var audit v1.Page[v1.AuditEntry]
	b.get("/v1/audit", &audit)
	found := false
	for _, a := range audit.Items {
		if a.Action == "unlock" && a.Actor == user {
			found = true
			assert.Equal(t, "reverted by hand", a.Details["reason"])
			assert.Equal(t, f.name, a.Details["repo"])
		}
		assert.NotEqual(t, hidden.name, a.Details["repo"], "a person's audit log leaves out other organisations")
	}
	assert.True(t, found, "the unlock is audited under the person's login")

	merged := f.co.merge(f.co.head, "Merge pull request #100 from initech/feature/vpc-subnet")
	f.merge(ev, merged, applier)
	for i, tag := range []string{"v1.0.0", "v1.1.0"} {
		f.deliver(gh.EventPush, e.GH.PushEvent(modules, "refs/tags/"+tag, strings.Repeat(string(rune('1'+i)), 40)))
	}

	var apps v1.StackDetail
	b.get("/v1/stacks/"+f.stackID(prodApps), &apps)
	assert.Equal(t, []string{prodVPC}, apps.DependsOn)
	require.Len(t, apps.Modules, 1)
	assert.Equal(t, v1.ModuleConsume{ModuleKey: modules + "//alb", Ref: "v1.0.0", Latest: "v1.1.0", Behind: 1}, apps.Modules[0],
		"the stack pins v1.0.0 and is one release behind")
	require.NotNil(t, apps.LastPlan)
	assert.Equal(t, p.runID, apps.LastPlan.RunID)
	var vpc v1.StackDetail
	b.get("/v1/stacks/"+vpcID, &vpc)
	assert.Equal(t, []string{prodApps, prodEKS}, vpc.Dependents)
	assert.Nil(t, vpc.Lock)

	var mods v1.Page[v1.ModuleDetail]
	b.get("/v1/modules?q=alb", &mods)
	require.Len(t, mods.Items, 1)
	alb := mods.Items[0]
	assert.Equal(t, modules+"//alb", alb.Key)
	assert.Equal(t, v1.ModuleGit, alb.Kind)
	require.Len(t, alb.Versions, 2)
	assert.Equal(t, "v1.1.0", alb.Versions[0].Version, "versions are newest first")
	require.Len(t, alb.Consumers, 1)
	assert.Equal(t, v1.ModuleConsumer{StackID: apps.ID, Repo: f.name, StackKey: prodApps, Ref: "v1.0.0", Behind: 1}, alb.Consumers[0])
	var one v1.ModuleDetail
	b.get("/v1/modules/"+alb.ID, &one)
	assert.Equal(t, alb, one)
	b.get("/v1/modules?q="+url.QueryEscape(f.name+"//modules/vpc"), &mods)
	require.Len(t, mods.Items, 1)
	consumers := make([]string, 0, len(mods.Items[0].Consumers))
	for _, c := range mods.Items[0].Consumers {
		consumers = append(consumers, c.StackKey)
	}
	assert.Equal(t, []string{prodVPC, stagingVPC}, consumers)

	resp, _ = b.do(http.MethodPost, "/auth/logout", nil, http.Header{"Origin": {e.BaseURL}})
	assert.Equal(t, http.StatusNoContent, resp.status)
	resp, _ = b.do(http.MethodGet, "/v1/me", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.status, "the session ends at logout")

	outsider := newBrowser(t, e)
	e.GH.SetOrgMembership("hooli", "gavin", gh.MembershipActive, "admin")
	resp, body = outsider.signIn("gavin")
	assert.Equal(t, http.StatusForbidden, resp.status, "a user outside every installed organisation gets no session")
	assert.Contains(t, body, "Stackorder is not installed for your organisations")
	for _, c := range resp.cookies {
		assert.NotEqual(t, "stackorder_session", c.Name)
	}
	resp, _ = outsider.do(http.MethodGet, "/v1/me", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.status)
}
