// Package ghfake is an in-memory fake of the GitHub REST API endpoints that
// internal/gh calls, served by an httptest server. Tests configure
// repositories, pull requests, reviews, permissions, contents and workflow
// runs, point a gh.App or gh.Client at URL, and inspect what the code under
// test did: check runs with their update history, comments, reactions,
// dispatches, issues and deployment protection rule decisions. Knobs inject
// failures, rate limits and latency.
//
// Installation tokens are "ghs_fake_<installation>_<n>", expire after one
// hour and are only accepted for repositories of the installation that
// minted them. JWT endpoints require a parseable App JWT, verified against
// the App's public key when one is registered with SetAppPublicKey or
// AppConfig. User tokens come from the OAuth code exchange or UserToken.
package ghfake

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stackorder/stackorder/internal/gh"
)

// CheckRun is a check run as the fake stores it, with every create and
// update payload it received in order.
type CheckRun struct {
	gh.CheckRun
	// Repo is the repository the check run belongs to.
	Repo string
	// History holds the create payload followed by each update payload.
	History []gh.CheckRunParams
}

// Dispatch records one workflow_dispatch call.
type Dispatch struct {
	Repo     string
	Workflow string
	Ref      string
	Inputs   map[string]string
	// RunID is the workflow run the fake created for the dispatch.
	RunID int64
	At    time.Time
}

// ProtectionRuleDecision records one deployment protection rule review.
type ProtectionRuleDecision struct {
	Repo            string
	RunID           int64
	EnvironmentName string
	State           string
	Comment         string
	At              time.Time
}

// Request is one request the fake served.
type Request struct {
	Method   string
	Path     string
	RawQuery string
	// Pattern is the route pattern that matched, such as
	// "GET /repos/{owner}/{repo}/pulls/{pull_number}".
	Pattern string
	// Auth is "jwt", "installation", "user", "none" or "invalid".
	Auth           string
	InstallationID int64
	Login          string
	Header         http.Header
	Body           []byte
	Status         int
}

// Server is the fake GitHub API. It is safe for concurrent use.
type Server struct {
	t   testing.TB
	srv *httptest.Server
	mux *http.ServeMux

	mu             sync.Mutex
	clock          func() time.Time
	latency        time.Duration
	pageSize       int
	appKey         *rsa.PrivateKey
	appPublicKey   *rsa.PublicKey
	app            gh.AppInfo
	installations  map[int64]*installation
	repos          map[string]*repoState
	tokens         map[string]issuedToken
	tokenSeq       map[int64]int
	userTokens     map[string]string
	userTokenSeq   int
	users          map[string]gh.User
	orgMembers     map[string]orgMembership
	oauthCodes     map[string]string
	oauthClientID  string
	oauthSecret    string
	manifests      map[string]gh.AppCredentials
	teams          map[string]string
	reactions      map[int64][]reaction
	dispatches     []Dispatch
	decisions      []ProtectionRuleDecision
	requests       []Request
	failures       []failure
	onDispatch     func(Dispatch)
	nextID         int64
	nextRepoID     int64
	nextAccountID  int64
	suspendedInsts map[int64]bool
}

type installation struct {
	inst  gh.Installation
	repos []string
}

type issuedToken struct {
	installationID int64
	expiresAt      time.Time
}

type orgMembership struct {
	state string
	role  string
}

type reaction struct {
	content string
	login   string
}

type failure struct {
	route   string
	status  int
	left    int
	header  http.Header
	message string
}

type storedComment struct {
	issue   int
	comment gh.Comment
}

type repoState struct {
	repo          gh.Repository
	installation  int64
	pulls         map[int]*gh.PullRequest
	reviews       map[int][]gh.Review
	files         map[int][]string
	comments      []*storedComment
	collaborators map[string]string
	contents      map[string]map[string][]byte
	tags          []gh.Tag
	refs          map[string]string
	checkRuns     []*CheckRun
	runs          []*gh.WorkflowRun
	jobs          map[int64][]gh.WorkflowJob
	artifacts     map[int64][]gh.Artifact
	pending       map[int64][]gh.PendingDeployment
	issues        []*gh.Issue
	runNumber     int
}

// New starts a fake GitHub API that is closed when the test ends. The App is
// id 1, slug "stackorder-test".
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		t:     t,
		clock: time.Now,
		app: gh.AppInfo{
			ID:          1,
			Slug:        "stackorder-test",
			Name:        "Stackorder Test",
			ClientID:    "Iv1.fakeclientid",
			Owner:       gh.User{Login: "stackorder", ID: 1, Type: "Organization"},
			HTMLURL:     "https://github.com/apps/stackorder-test",
			Permissions: maps.Clone(gh.DefaultPermissions),
			Events:      slices.Clone(gh.DefaultEvents),
		},
		installations:  map[int64]*installation{},
		repos:          map[string]*repoState{},
		tokens:         map[string]issuedToken{},
		tokenSeq:       map[int64]int{},
		userTokens:     map[string]string{},
		users:          map[string]gh.User{},
		orgMembers:     map[string]orgMembership{},
		oauthCodes:     map[string]string{},
		manifests:      map[string]gh.AppCredentials{},
		teams:          map[string]string{},
		reactions:      map[int64][]reaction{},
		suspendedInsts: map[int64]bool{},
		nextID:         1000,
		nextRepoID:     5000,
		nextAccountID:  9000,
	}
	s.mux = http.NewServeMux()
	s.routes()
	s.srv = httptest.NewServer(s.mux)
	t.Cleanup(s.srv.Close)
	return s
}

// URL returns the base URL, usable both as gh.Config.BaseURL and as
// gh.OAuthConfig.BaseWebURL.
func (s *Server) URL() string { return s.srv.URL }

// HTTPClient returns a client for the fake.
func (s *Server) HTTPClient() *http.Client { return s.srv.Client() }

func (s *Server) now() time.Time { return s.clock().UTC() }

func (s *Server) id() int64 {
	s.nextID++
	return s.nextID
}

func key(parts ...string) string { return strings.ToLower(strings.Join(parts, "/")) }

// SetClock replaces the clock used for token expiry, JWT validation and
// timestamps.
func (s *Server) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = now
}

// SetApp replaces the App description served by GET /app. The App id is
// also the JWT issuer the fake expects.
func (s *Server) SetApp(info gh.AppInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.app = info
}

// SetAppPublicKey makes JWT endpoints verify the JWT signature.
func (s *Server) SetAppPublicKey(pub *rsa.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appPublicKey = pub
}

var sharedKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
})

func (s *Server) ensureAppKey() *rsa.PrivateKey {
	s.t.Helper()
	k, err := sharedKey()
	if err != nil {
		s.t.Fatalf("ghfake: generate app key: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appKey == nil {
		s.appKey = k
		s.appPublicKey = &k.PublicKey
	}
	return s.appKey
}

// AppConfig returns a gh.Config for the fake's App with a private key,
// generated once per test binary, whose public half the fake verifies, and
// retry delays short enough for tests.
func (s *Server) AppConfig() gh.Config {
	s.t.Helper()
	k := s.ensureAppKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	return gh.Config{
		AppID:          s.app.ID,
		PrivateKey:     k,
		BaseURL:        s.srv.URL,
		HTTPClient:     s.srv.Client(),
		RetryBaseDelay: time.Millisecond,
		MaxRetryWait:   5 * time.Second,
	}
}

// AppKeyPEM returns the PKCS#1 PEM of the key AppConfig uses.
func (s *Server) AppKeyPEM() []byte {
	s.t.Helper()
	k := s.ensureAppKey()
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// NewApp returns a gh.App configured by AppConfig.
func (s *Server) NewApp() *gh.App {
	s.t.Helper()
	app, err := gh.NewApp(s.AppConfig())
	if err != nil {
		s.t.Fatalf("ghfake: new app: %v", err)
	}
	return app
}

// AddInstallation registers an installation on an organisation account and
// grants it the repositories, creating them when needed.
func (s *Server) AddInstallation(id int64, account string, repos ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.installations[id]
	if !ok {
		s.nextAccountID++
		in = &installation{inst: gh.Installation{
			ID:                  id,
			Account:             gh.User{Login: account, ID: s.nextAccountID, Type: "Organization"},
			AppID:               s.app.ID,
			AppSlug:             s.app.Slug,
			TargetType:          "Organization",
			RepositorySelection: "selected",
			HTMLURL:             fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", account, id),
			Permissions:         maps.Clone(gh.DefaultPermissions),
			Events:              slices.Clone(gh.DefaultEvents),
		}}
		s.installations[id] = in
	}
	for _, full := range repos {
		rs := s.ensureRepo(full)
		rs.installation = id
		if !slices.ContainsFunc(in.repos, func(r string) bool { return strings.EqualFold(r, full) }) {
			in.repos = append(in.repos, rs.repo.FullName)
		}
	}
}

// SuspendInstallation makes token exchanges for the installation fail.
func (s *Server) SuspendInstallation(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suspendedInsts[id] = true
	if in, ok := s.installations[id]; ok {
		t := s.now()
		in.inst.SuspendedAt = &t
	}
}

func (s *Server) ensureRepo(full string) *repoState {
	k := strings.ToLower(full)
	if rs, ok := s.repos[k]; ok {
		return rs
	}
	owner, name, _ := strings.Cut(full, "/")
	s.nextRepoID++
	rs := &repoState{
		repo: gh.Repository{
			ID:            s.nextRepoID,
			Name:          name,
			FullName:      full,
			Owner:         gh.User{Login: owner, Type: "Organization"},
			DefaultBranch: "main",
			Private:       true,
			HTMLURL:       "https://github.com/" + full,
		},
		pulls:         map[int]*gh.PullRequest{},
		reviews:       map[int][]gh.Review{},
		files:         map[int][]string{},
		collaborators: map[string]string{},
		contents:      map[string]map[string][]byte{},
		refs:          map[string]string{},
		jobs:          map[int64][]gh.WorkflowJob{},
		artifacts:     map[int64][]gh.Artifact{},
		pending:       map[int64][]gh.PendingDeployment{},
	}
	s.repos[k] = rs
	return rs
}

// SetRepo creates or replaces a repository's metadata. Zero fields get
// defaults: the id, name and owner from fullName and "main" as the default
// branch.
func (s *Server) SetRepo(fullName string, repo gh.Repository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(fullName)
	owner, name, _ := strings.Cut(fullName, "/")
	repo.FullName = fullName
	if repo.ID == 0 {
		repo.ID = rs.repo.ID
	}
	if repo.Name == "" {
		repo.Name = name
	}
	if repo.Owner.Login == "" {
		repo.Owner = gh.User{Login: owner, Type: "Organization"}
	}
	if repo.DefaultBranch == "" {
		repo.DefaultBranch = "main"
	}
	if repo.HTMLURL == "" {
		repo.HTMLURL = "https://github.com/" + fullName
	}
	rs.repo = repo
}

// SetPull creates or replaces a pull request. Missing head and base
// repositories default to the repository itself, the state to open and the
// base branch to the default branch.
func (s *Server) SetPull(repo string, pr gh.PullRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	if pr.State == "" {
		pr.State = gh.IssueOpen
	}
	if pr.BaseRef == "" {
		pr.BaseRef = rs.repo.DefaultBranch
	}
	for _, side := range []*gh.PullBranch{&pr.Head, &pr.Base} {
		r := rs.repo
		if side.Repo != nil {
			r = *side.Repo
		}
		side.Repo = &r
	}
	if pr.HTMLURL == "" {
		pr.HTMLURL = fmt.Sprintf("https://github.com/%s/pull/%d", rs.repo.FullName, pr.Number)
	}
	pr.Labels = slices.Clone(pr.Labels)
	rs.pulls[pr.Number] = &pr
}

// SetReviews replaces the reviews of a pull request.
func (s *Server) SetReviews(repo string, number int, reviews []gh.Review) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	out := slices.Clone(reviews)
	for i := range out {
		if out[i].ID == 0 {
			out[i].ID = s.id()
		}
	}
	rs.reviews[number] = out
}

// SetFiles replaces the changed paths of a pull request.
func (s *Server) SetFiles(repo string, number int, paths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRepo(repo).files[number] = slices.Clone(paths)
}

// SetCollaboratorPermission sets a user's permission on a repository:
// admin, maintain, write, triage, read or none.
func (s *Server) SetCollaboratorPermission(repo, login, perm string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRepo(repo).collaborators[strings.ToLower(login)] = perm
}

// SetTeamMembership sets a user's membership state in a team, active or
// pending; "none" or "" removes it.
func (s *Server) SetTeamMembership(org, team, login, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(org, team, login)
	if state == "" || state == gh.MembershipNone {
		delete(s.teams, k)
		return
	}
	s.teams[k] = state
}

// SetContents stores a file at a ref; an empty ref is the default branch.
func (s *Server) SetContents(repo, ref, path string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	if ref == "" {
		ref = rs.repo.DefaultBranch
	}
	if rs.contents[ref] == nil {
		rs.contents[ref] = map[string][]byte{}
	}
	rs.contents[ref][strings.Trim(path, "/")] = slices.Clone(data)
}

// SetTags replaces the repository's tags and their refs/tags refs.
func (s *Server) SetTags(repo string, tags []gh.Tag) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	rs.tags = slices.Clone(tags)
	for _, t := range tags {
		rs.refs["tags/"+t.Name] = t.SHA
	}
}

// SetRef points a ref such as "heads/main" or "refs/tags/v1.0.0" at a
// commit.
func (s *Server) SetRef(repo, ref, sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRepo(repo).refs[strings.TrimPrefix(ref, "refs/")] = sha
}

// AddWorkflowRun stores a workflow run and returns it with defaults filled
// in: an id, status queued, attempt 1, timestamps and the HTML URL.
func (s *Server) AddWorkflowRun(repo string, run gh.WorkflowRun) gh.WorkflowRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.addRun(s.ensureRepo(repo), run)
}

func (s *Server) addRun(rs *repoState, run gh.WorkflowRun) *gh.WorkflowRun {
	if run.ID == 0 {
		run.ID = s.id()
	}
	if run.Status == "" {
		run.Status = gh.RunStatusQueued
	}
	if run.RunAttempt == 0 {
		run.RunAttempt = 1
	}
	if run.RunNumber == 0 {
		rs.runNumber++
		run.RunNumber = rs.runNumber
	}
	now := s.now()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	if run.HTMLURL == "" {
		run.HTMLURL = fmt.Sprintf("https://github.com/%s/actions/runs/%d", rs.repo.FullName, run.ID)
	}
	if run.Name == "" && run.Path != "" {
		run.Name = workflowName(run.Path)
	}
	if run.DisplayTitle == "" {
		run.DisplayTitle = run.Name
	}
	for i, r := range rs.runs {
		if r.ID == run.ID {
			rs.runs[i] = &run
			return &run
		}
	}
	rs.runs = append(rs.runs, &run)
	return &run
}

func workflowName(path string) string {
	base := path[strings.LastIndex(path, "/")+1:]
	return strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml")
}

func (s *Server) findRun(repo string, id int64) *gh.WorkflowRun {
	rs, ok := s.repos[strings.ToLower(repo)]
	if !ok {
		return nil
	}
	for _, r := range rs.runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// SetWorkflowRunStatus changes a run's status and returns it. A missing run
// is reported as a test error.
func (s *Server) SetWorkflowRunStatus(repo string, id int64, status string) gh.WorkflowRun {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.findRun(repo, id)
	if r == nil {
		s.t.Errorf("ghfake: workflow run %d not found in %s", id, repo)
		return gh.WorkflowRun{}
	}
	r.Status = status
	r.UpdatedAt = s.now()
	if status == gh.RunStatusInProgress && r.RunStartedAt.IsZero() {
		r.RunStartedAt = r.UpdatedAt
	}
	return *r
}

// CompleteWorkflowRun marks a run completed with conclusion and returns it.
// A missing run is reported as a test error.
func (s *Server) CompleteWorkflowRun(repo string, id int64, conclusion string) gh.WorkflowRun {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.findRun(repo, id)
	if r == nil {
		s.t.Errorf("ghfake: workflow run %d not found in %s", id, repo)
		return gh.WorkflowRun{}
	}
	r.Status = gh.RunStatusCompleted
	r.Conclusion = conclusion
	r.UpdatedAt = s.now()
	return *r
}

// SetJobs replaces the jobs of a workflow run.
func (s *Server) SetJobs(repo string, runID int64, jobs []gh.WorkflowJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(jobs)
	for i := range out {
		if out[i].ID == 0 {
			out[i].ID = s.id()
		}
		if out[i].RunID == 0 {
			out[i].RunID = runID
		}
	}
	s.ensureRepo(repo).jobs[runID] = out
}

// SetArtifacts replaces the artifacts of a workflow run.
func (s *Server) SetArtifacts(repo string, runID int64, artifacts []gh.Artifact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(artifacts)
	for i := range out {
		if out[i].ID == 0 {
			out[i].ID = s.id()
		}
	}
	s.ensureRepo(repo).artifacts[runID] = out
}

// SetPendingDeployments replaces the environments a run waits on.
func (s *Server) SetPendingDeployments(repo string, runID int64, pending []gh.PendingDeployment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRepo(repo).pending[runID] = slices.Clone(pending)
}

// AddUser registers a user for /user and the OAuth flow.
func (s *Server) AddUser(u gh.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addUser(u)
}

func (s *Server) addUser(u gh.User) gh.User {
	if u.ID == 0 {
		s.nextAccountID++
		u.ID = s.nextAccountID
	}
	if u.Type == "" {
		u.Type = "User"
	}
	if u.AvatarURL == "" {
		u.AvatarURL = fmt.Sprintf("https://avatars.githubusercontent.com/u/%d", u.ID)
	}
	s.users[strings.ToLower(u.Login)] = u
	return u
}

func (s *Server) user(login string) gh.User {
	if u, ok := s.users[strings.ToLower(login)]; ok {
		return u
	}
	return s.addUser(gh.User{Login: login})
}

// SetOrgMembership sets a user's organisation membership: state active or
// pending, role admin or member. An empty state removes it.
func (s *Server) SetOrgMembership(org, login, state, role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(org, login)
	if state == "" || state == gh.MembershipNone {
		delete(s.orgMembers, k)
		return
	}
	s.orgMembers[k] = orgMembership{state: state, role: role}
}

// SetOAuthClient makes the token exchange require these client
// credentials.
func (s *Server) SetOAuthClient(clientID, clientSecret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthClientID, s.oauthSecret = clientID, clientSecret
}

// AddOAuthCode registers a single-use OAuth code for a user.
func (s *Server) AddOAuthCode(code, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user(login)
	s.oauthCodes[code] = login
}

// UserToken mints a user access token directly.
func (s *Server) UserToken(login string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mintUserToken(login)
}

func (s *Server) mintUserToken(login string) string {
	s.user(login)
	s.userTokenSeq++
	tok := fmt.Sprintf("gho_fake_%d", s.userTokenSeq)
	s.userTokens[tok] = login
	return tok
}

// InstallationToken mints an installation token directly, bypassing the
// JWT exchange.
func (s *Server) InstallationToken(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mintToken(id).Token
}

func (s *Server) mintToken(id int64) gh.Token {
	s.tokenSeq[id]++
	tok := gh.Token{
		Token:               fmt.Sprintf("ghs_fake_%d_%d", id, s.tokenSeq[id]),
		ExpiresAt:           s.now().Add(time.Hour).Truncate(time.Second),
		Permissions:         maps.Clone(gh.DefaultPermissions),
		RepositorySelection: "selected",
	}
	s.tokens[tok.Token] = issuedToken{installationID: id, expiresAt: tok.ExpiresAt}
	return tok
}

// RevokeTokens invalidates every installation and user token issued so far,
// as a key rotation or an uninstall would.
func (s *Server) RevokeTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = map[string]issuedToken{}
	s.userTokens = map[string]string{}
}

// SetManifestConversion registers the credentials returned for a single-use
// manifest code.
func (s *Server) SetManifestConversion(code string, creds gh.AppCredentials) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifests[code] = creds
}

// OnDispatch registers a function called after each workflow dispatch, for
// tests that simulate the dispatched run.
func (s *Server) OnDispatch(fn func(Dispatch)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDispatch = fn
}

// AddComment stores a comment on an issue or pull request as if login had
// posted it, and returns it.
func (s *Server) AddComment(repo string, number int, login, body string) gh.Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.ensureRepo(repo)
	return s.addComment(rs, number, s.user(login), s.association(rs, login), body)
}

func (s *Server) addComment(rs *repoState, number int, u gh.User, assoc, body string) gh.Comment {
	id := s.id()
	now := s.now()
	c := gh.Comment{
		ID:                id,
		Body:              body,
		User:              u,
		AuthorAssociation: assoc,
		HTMLURL:           fmt.Sprintf("https://github.com/%s/pull/%d#issuecomment-%d", rs.repo.FullName, number, id),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	rs.comments = append(rs.comments, &storedComment{issue: number, comment: c})
	return c
}

func (s *Server) association(rs *repoState, login string) string {
	if strings.EqualFold(rs.repo.Owner.Login, login) {
		return "OWNER"
	}
	if m, ok := s.orgMembers[key(rs.repo.Owner.Login, login)]; ok && m.state == gh.MembershipActive {
		return "MEMBER"
	}
	switch rs.collaborators[strings.ToLower(login)] {
	case "", gh.PermissionNone:
		return "NONE"
	}
	return "COLLABORATOR"
}

// FailNext makes the next n requests matching route fail with status.
// route is a pattern as listed in Request.Pattern, a concrete
// "METHOD /path", or "" for any request. Status 0 drops the connection
// without a response; net/http may silently resend an idempotent request
// that was sent on a reused connection.
func (s *Server) FailNext(route string, status, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, failure{route: route, status: status, left: n})
}

// RateLimitNext makes the next request fail with a primary rate limit 403
// that resets at reset.
func (s *Server) RateLimitNext(reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := http.Header{}
	h.Set("X-RateLimit-Limit", "5000")
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Used", "5000")
	h.Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
	h.Set("X-RateLimit-Resource", "core")
	s.failures = append(s.failures, failure{status: http.StatusForbidden, left: 1, header: h, message: "API rate limit exceeded for installation."})
}

// SecondaryRateLimitNext makes the next request fail with a secondary rate
// limit 403 carrying Retry-After.
func (s *Server) SecondaryRateLimitNext(retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := http.Header{}
	h.Set("Retry-After", fmt.Sprint(int(retryAfter/time.Second)))
	s.failures = append(s.failures, failure{status: http.StatusForbidden, left: 1, header: h, message: "You have exceeded a secondary rate limit. Please wait a few minutes before you try again."})
}

// SetLatency delays every response by d.
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = d
}

// SetPageSize caps the page size of list endpoints, whatever per_page the
// client asks for, so pagination can be exercised with few items. 0 restores
// GitHub's per_page handling.
func (s *Server) SetPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageSize = n
}

// CheckRuns returns the repository's check runs in creation order.
func (s *Server) CheckRuns(repo string) []CheckRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repos[strings.ToLower(repo)]
	if !ok {
		return nil
	}
	out := make([]CheckRun, len(rs.checkRuns))
	for i, c := range rs.checkRuns {
		out[i] = *c
		out[i].History = slices.Clone(c.History)
	}
	return out
}

// Comments returns the comments on an issue or pull request, oldest first.
func (s *Server) Comments(repo string, number int) []gh.Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repos[strings.ToLower(repo)]
	if !ok {
		return nil
	}
	var out []gh.Comment
	for _, c := range rs.comments {
		if c.issue == number {
			out = append(out, c.comment)
		}
	}
	return out
}

// Reactions returns the reaction contents on a comment, in order.
func (s *Server) Reactions(commentID int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.reactions[commentID]))
	for _, r := range s.reactions[commentID] {
		out = append(out, r.content)
	}
	return out
}

// Dispatches returns every workflow dispatch, in order.
func (s *Server) Dispatches() []Dispatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.dispatches)
	for i := range out {
		out[i].Inputs = maps.Clone(out[i].Inputs)
	}
	return out
}

// Issues returns the repository's issues, excluding pull requests, by
// number.
func (s *Server) Issues(repo string) []gh.Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repos[strings.ToLower(repo)]
	if !ok {
		return nil
	}
	out := make([]gh.Issue, len(rs.issues))
	for i, is := range rs.issues {
		out[i] = *is
		out[i].Labels = slices.Clone(is.Labels)
	}
	return out
}

// WorkflowRuns returns the repository's workflow runs in creation order.
func (s *Server) WorkflowRuns(repo string) []gh.WorkflowRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repos[strings.ToLower(repo)]
	if !ok {
		return nil
	}
	out := make([]gh.WorkflowRun, len(rs.runs))
	for i, r := range rs.runs {
		out[i] = *r
	}
	return out
}

// ProtectionRuleDecisions returns every deployment protection rule review,
// in order.
func (s *Server) ProtectionRuleDecisions() []ProtectionRuleDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.decisions)
}

// Requests returns every request served, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *Server) sortedInstallations() []gh.Installation {
	ids := make([]int64, 0, len(s.installations))
	for id := range s.installations {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]gh.Installation, len(ids))
	for i, id := range ids {
		out[i] = s.installations[id].inst
	}
	return out
}
