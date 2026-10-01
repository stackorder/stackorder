//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/report"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/faketf"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

const (
	applyCommand = "stackorder apply"

	author   = "alice"
	reviewer = "bob"
	applier  = "carol"
	reader   = "mallory"

	prodVPC     = "stacks/prod/vpc"
	stagingVPC  = "stacks/staging/vpc"
	prodEKS     = "stacks/prod/eks"
	prodApps    = "stacks/prod/apps"
	stagingApps = "stacks/staging/apps"
	legacyDNS   = "stacks/legacy/dns"
)

var exampleStackKeys = append([]string{prodVPC, stagingVPC, prodEKS, prodApps, stagingApps, legacyDNS}, infraKeys...)

var repoIDs atomic.Int64

func init() { repoIDs.Store(810000) }

type fixture struct {
	t       *testing.T
	e       *Env
	name    string
	account string
	id      int64
	inst    int64
	co      *checkout
	main    string
	planDir string
	tf      faketf.Config
}

type fixtureSetup struct {
	account      string
	installation int64
	config       func(string) string
	edit         func(root string)
	contents     map[string]string
	skipInstall  bool
}

type fixtureOption func(*fixtureSetup)

func withConfig(change func(string) string) fixtureOption {
	return func(s *fixtureSetup) { s.config = change }
}

func withEdit(edit func(root string)) fixtureOption {
	return func(s *fixtureSetup) { s.edit = edit }
}

func withContents(path, content string) fixtureOption {
	return func(s *fixtureSetup) { s.contents[path] = content }
}

func withInstallation(id int64, account string) fixtureOption {
	return func(s *fixtureSetup) { s.installation, s.account = id, account }
}

func notInstalled() fixtureOption {
	return func(s *fixtureSetup) { s.skipInstall = true }
}

func newFixture(t *testing.T, e *Env, repo string, opts ...fixtureOption) *fixture {
	t.Helper()
	setup := fixtureSetup{account: acmeOrg, installation: acmeInstallation, contents: map[string]string{}}
	for _, o := range opts {
		o(&setup)
	}
	name := setup.account + "/" + repo
	co := newCheckout(t, name, func(root string) {
		if setup.config != nil {
			editFile(t, root, "stackorder.yaml", setup.config)
		}
		if setup.edit != nil {
			setup.edit(root)
		}
	})
	f := &fixture{
		t: t, e: e, name: name, account: setup.account, id: repoIDs.Add(1), inst: setup.installation,
		co: co, main: co.base, planDir: t.TempDir(), tf: defaultPlans(t),
	}
	e.GH.SetRepo(name, gh.Repository{ID: f.id, DefaultBranch: "main", Private: true})
	e.GH.SetRef(name, "heads/main", co.base)
	for path, data := range co.configFiles(co.base) {
		e.GH.SetContents(name, "main", path, data)
	}
	for path, content := range setup.contents {
		e.GH.SetContents(name, "main", path, []byte(content))
	}
	for login, perm := range map[string]string{author: "write", reviewer: "write", applier: "maintain", reader: "read"} {
		e.GH.SetCollaboratorPermission(name, login, perm)
	}
	if !setup.skipInstall {
		e.GH.AddInstallation(f.inst, f.account, name)
		f.deliver(gh.EventInstallationRepositories, f.reposEvent("added", true))
		repo, err := e.Store.GetRepo(t.Context(), f.id)
		require.NoError(t, err, "installation_repositories recorded %s", name)
		require.NotNil(t, repo.Config, "stackorder.yaml of %s was loaded", name)
	}
	return f
}

func defaultPlans(t *testing.T) faketf.Config {
	t.Helper()
	vpc := faketf.Behavior{PlanExit: 2, ShowJSON: fixturePath(t, "vpc.json")}
	return faketf.Config{
		Log:     t.TempDir() + "/faketf.jsonl",
		Default: faketf.Behavior{ShowJSON: fixturePath(t, "noop.json")},
		Stacks:  map[string]faketf.Behavior{prodVPC: vpc, stagingVPC: vpc},
	}
}

func (f *fixture) setStack(key string, change func(*faketf.Behavior)) {
	b, ok := f.tf.Stacks[key]
	if !ok {
		b = f.tf.Default
	}
	change(&b)
	f.tf.Stacks[key] = b
}

func (f *fixture) repoID() string { return strconv.FormatInt(f.id, 10) }

func (f *fixture) reposEvent(action string, added bool) *gh.InstallationRepositoriesEvent {
	r := gh.Repository{ID: f.id, Name: strings.TrimPrefix(f.name, f.account+"/"), FullName: f.name, Private: true}
	ev := &gh.InstallationRepositoriesEvent{
		EventCommon: gh.EventCommon{
			Action:       action,
			Installation: &gh.Installation{ID: f.inst, Account: gh.User{Login: f.account, Type: "Organization"}},
			Sender:       gh.User{Login: "octocat", Type: "User"},
		},
		RepositorySelection: "selected",
	}
	if added {
		ev.RepositoriesAdded = []gh.Repository{r}
	} else {
		ev.RepositoriesRemoved = []gh.Repository{r}
	}
	return ev
}

func (f *fixture) deliver(event string, payload any) Delivery {
	f.t.Helper()
	d := f.e.Deliver(event, payload)
	require.Equal(f.t, http.StatusAccepted, d.Status, "%s delivery: %s", event, d.Body)
	f.e.waitEvent(d.ID)
	return d
}

func (e *Env) waitEvent(id string) {
	e.t.Helper()
	var ev store.Event
	e.WaitFor(func() bool {
		got, err := e.Store.GetEvent(context.Background(), id)
		ev = got
		return err == nil && (got.DoneAt != nil || got.LastError != "")
	}, waitFor, "the worker pool processes delivery "+id)
	require.Empty(e.t, ev.LastError, "delivery %s (%s) failed", id, ev.Kind)
}

func (e *Env) runJob(kind string, payload any) {
	e.t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(e.t, err)
	job, inserted, err := e.Store.EnqueueJob(context.Background(), kind, body, time.Time{}, "integration:"+kind+":"+uuid.NewString())
	require.NoError(e.t, err)
	require.True(e.t, inserted)
	e.waitJob(job.ID)
}

func (e *Env) waitJob(id uuid.UUID) {
	e.t.Helper()
	var job store.Job
	e.WaitFor(func() bool {
		got, err := e.Store.GetJob(context.Background(), id)
		job = got
		return err == nil && (got.DoneAt != nil || got.LastError != "")
	}, waitFor, "the worker pool runs job "+id.String())
	require.Empty(e.t, job.LastError, "job %s (%s) failed", id, job.Kind)
}

func (f *fixture) openPR(number int, head, branch string) *gh.PullRequestEvent {
	f.t.Helper()
	mergeable := true
	ev := f.e.GH.PullRequestEvent("opened", f.name, gh.PullRequest{
		Number: number, Title: "Add a fourth subnet to the VPC module", User: gh.User{Login: author},
		HeadSHA: head, HeadRef: branch, BaseSHA: f.co.base, BaseRef: "main",
		Mergeable: &mergeable, MergeableState: "clean",
	})
	f.deliver(gh.EventPullRequest, ev)
	return ev
}

func (f *fixture) push(ev *gh.PullRequestEvent, head string) *gh.PullRequestEvent {
	f.t.Helper()
	pr := ev.PullRequest
	before := pr.HeadSHA
	pr.HeadSHA = head
	next := f.e.GH.PullRequestEvent("synchronize", f.name, pr)
	next.Before = before
	f.deliver(gh.EventPullRequest, next)
	return next
}

func (f *fixture) merge(ev *gh.PullRequestEvent, mergeCommit, merger string) *gh.PullRequestEvent {
	f.t.Helper()
	pr := ev.PullRequest
	now := time.Now().UTC()
	pr.State, pr.Merged, pr.MergedAt, pr.MergeCommitSHA = gh.IssueClosed, true, &now, mergeCommit
	closed := f.e.GH.PullRequestEvent("closed", f.name, pr)
	closed.Sender = gh.User{Login: merger, Type: "User"}
	f.e.GH.SetRef(f.name, "heads/main", mergeCommit)
	f.main = mergeCommit
	f.deliver(gh.EventPullRequest, closed)
	return closed
}

func (f *fixture) approve(pr int, login, sha string) {
	f.t.Helper()
	ev := f.e.GH.PullRequestReviewEvent("submitted", f.name, pr, gh.Review{
		User: gh.User{Login: login, Type: "User"}, State: gh.ReviewApproved, CommitID: sha, Body: "LGTM",
	})
	f.deliver(gh.EventPullRequestReview, ev)
}

func (f *fixture) comment(pr int, login, body string) gh.Comment {
	f.t.Helper()
	ev := f.e.GH.IssueCommentEvent(f.name, pr, body, login)
	f.deliver(gh.EventIssueComment, ev)
	return ev.Comment
}

func (f *fixture) botComments(pr int, after int64) []gh.Comment {
	var out []gh.Comment
	for _, c := range f.e.GH.Comments(f.name, pr) {
		if c.ID > after && strings.HasSuffix(c.User.Login, "[bot]") && !strings.HasPrefix(c.Body, report.Marker) &&
			!strings.HasPrefix(c.Body, "<!-- stackorder:run:") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fixture) sticky(pr int) gh.Comment {
	f.t.Helper()
	var found []gh.Comment
	for _, c := range f.e.GH.Comments(f.name, pr) {
		if strings.HasPrefix(c.Body, report.Marker+"\n") {
			found = append(found, c)
		}
	}
	require.Len(f.t, found, 1, "one sticky comment on #%d", pr)
	return found[0]
}

func (f *fixture) runComment(pr int, runID string) gh.Comment {
	f.t.Helper()
	var found []gh.Comment
	for _, c := range f.e.GH.Comments(f.name, pr) {
		if strings.HasPrefix(c.Body, report.RunMarker(runID)+"\n") {
			found = append(found, c)
		}
	}
	require.Len(f.t, found, 1, "one comment of run %s on #%d", runID, pr)
	return found[0]
}

func (f *fixture) checks(sha string) map[string]ghfake.CheckRun {
	out := map[string]ghfake.CheckRun{}
	for _, c := range f.e.GH.CheckRuns(f.name) {
		if c.HeadSHA == sha {
			out[c.Name] = c
		}
	}
	return out
}

func (f *fixture) check(sha, name string) ghfake.CheckRun {
	f.t.Helper()
	c, ok := f.checks(sha)[name]
	require.True(f.t, ok, "check run %q on %s; have %v", name, sha[:7], checkNames(f.checks(sha)))
	return c
}

func checkNames(m map[string]ghfake.CheckRun) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (f *fixture) dispatches(runID string) []ghfake.Dispatch {
	var out []ghfake.Dispatch
	for _, d := range f.e.GH.Dispatches() {
		if strings.EqualFold(d.Repo, f.name) && (runID == "" || d.Inputs["run_id"] == runID) {
			out = append(out, d)
		}
	}
	return out
}

func (f *fixture) waitDispatches(runID string, wave, n int) []ghfake.Dispatch {
	f.t.Helper()
	var got []ghfake.Dispatch
	f.e.WaitFor(func() bool {
		got = nil
		for _, d := range f.dispatches(runID) {
			if d.Inputs["wave"] == strconv.Itoa(wave) {
				got = append(got, d)
			}
		}
		return len(got) >= n
	}, waitFor, fmt.Sprintf("%d dispatches of wave %d of run %s", n, wave, runID))
	require.Len(f.t, got, n, "dispatches of wave %d", wave)
	slices.SortFunc(got, func(a, b ghfake.Dispatch) int { return strings.Compare(dispatchEnv(f.t, a), dispatchEnv(f.t, b)) })
	return got
}

func dispatchEntries(t *testing.T, d ghfake.Dispatch) []v1.MatrixEntry {
	t.Helper()
	var entries []v1.MatrixEntry
	require.NoError(t, json.Unmarshal([]byte(d.Inputs["stacks"]), &entries))
	require.NotEmpty(t, entries)
	return entries
}

func dispatchEnv(t *testing.T, d ghfake.Dispatch) string {
	t.Helper()
	return dispatchEntries(t, d)[0].Environment
}

func (f *fixture) complete(d ghfake.Dispatch, conclusion string) {
	f.t.Helper()
	run := f.e.GH.CompleteWorkflowRun(f.name, d.RunID, conclusion)
	f.deliver(gh.EventWorkflowRun, f.e.GH.WorkflowRunEvent("completed", f.name, run))
}

type planned struct {
	w       *workflow
	resolve jobResult
	runID   string
	matrix  v1.Matrix
	plans   map[string]jobResult
}

func (f *fixture) plan(ev *gh.PullRequestEvent) *planned {
	f.t.Helper()
	return f.planEach(ev, nil)
}

func (f *fixture) planEach(ev *gh.PullRequestEvent, before func(p *planned, entry v1.MatrixEntry)) *planned {
	f.t.Helper()
	w := f.planWorkflow(ev)
	res := w.run("resolve")
	requireExit(f.t, 0, res)
	p := &planned{w: w, resolve: res, runID: res.outputs["run-id"], plans: map[string]jobResult{}}
	require.NotEmpty(f.t, p.runID, "resolve wrote the run-id output")
	require.NoError(f.t, json.Unmarshal([]byte(res.outputs["matrix"]), &p.matrix))
	for _, entry := range p.matrix.Include {
		if before != nil {
			before(p, entry)
		}
		r := w.run("plan", "--stack", entry.Key, "--run-id", p.runID)
		requireExit(f.t, 0, r)
		p.plans[entry.Key] = r
	}
	return p
}

func (f *fixture) apply(d ghfake.Dispatch, before func(entry v1.MatrixEntry)) map[string]jobResult {
	f.t.Helper()
	entries := dispatchEntries(f.t, d)
	w := f.dispatchWorkflow(d, entries[0].Environment)
	out := map[string]jobResult{}
	for _, entry := range entries {
		if before != nil {
			before(entry)
		}
		out[entry.Key] = w.run("apply", "--stack", entry.Key, "--run-id", d.Inputs["run_id"], "--plan-file", f.planFile(entry.Artifact))
	}
	return out
}

func (f *fixture) planFile(artifact string) string {
	return filepath.Join(f.planDir, artifact+".tfplan")
}

func (e *Env) call(method, path string, body any, auth func(*http.Request)) (int, string) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(e.t, err)
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.URL(path), r)
	require.NoError(e.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		auth(req)
	}
	return e.Do(req)
}

func withAPIKey(req *http.Request) { req.Header.Set("Authorization", "Bearer "+suite.apiKey) }

func (e *Env) getJSON(path string, out any) {
	e.t.Helper()
	status, body := e.call(http.MethodGet, path, nil, withAPIKey)
	require.Equal(e.t, http.StatusOK, status, "GET %s: %s", path, body)
	require.NoError(e.t, json.Unmarshal([]byte(body), out), "GET %s", path)
}

func (e *Env) run(id string) v1.Run {
	e.t.Helper()
	var run v1.Run
	e.getJSON("/v1/runs/"+id, &run)
	return run
}

func (e *Env) waitRun(id string, status v1.RunStatus) v1.Run {
	e.t.Helper()
	var run v1.Run
	e.WaitFor(func() bool {
		run = e.run(id)
		return run.Status == status
	}, waitFor, fmt.Sprintf("run %s is %s", id, status))
	return run
}

func runStacks(run v1.Run) map[string]v1.RunStack {
	out := make(map[string]v1.RunStack, len(run.Stacks))
	for _, rs := range run.Stacks {
		out[rs.Key] = rs
	}
	return out
}

func (f *fixture) locks() map[string]store.Lock {
	f.t.Helper()
	locks, err := f.e.Store.ListLocks(f.t.Context(), f.id)
	require.NoError(f.t, err)
	out := make(map[string]store.Lock, len(locks))
	for _, l := range locks {
		out[l.StackKey] = l
	}
	return out
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func requireJSON(t *testing.T, data string, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(data), out), data)
}

func secondBranch(f *fixture) string {
	f.co.branch("feature/vpc-outputs", f.co.base)
	return f.co.edit("modules/vpc/outputs.tf", "feat(vpc): export the CIDR", func(s string) string {
		return s + "\noutput \"cidr\" {\n  description = \"CIDR block of the VPC.\"\n  value       = var.cidr\n}\n"
	})
}

func (f *fixture) audit(action string) []v1.AuditEntry {
	f.t.Helper()
	var page v1.Page[v1.AuditEntry]
	f.e.getJSON("/v1/audit?limit=200", &page)
	var out []v1.AuditEntry
	for _, a := range page.Items {
		if a.Action == action && (strings.Contains(a.Target, f.name) || a.Details["repo"] == f.name) {
			out = append(out, a)
		}
	}
	return out
}

func (f *fixture) stackID(key string) string {
	f.t.Helper()
	st, err := f.e.Store.GetStackByKey(f.t.Context(), f.id, key)
	require.NoError(f.t, err, key)
	return st.ID.String()
}

func (f *fixture) stackDetail(key string) v1.StackDetail {
	f.t.Helper()
	var d v1.StackDetail
	f.e.getJSON("/v1/stacks/"+f.stackID(key), &d)
	return d
}
