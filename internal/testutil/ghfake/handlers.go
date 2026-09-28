package ghfake

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stackorder/stackorder/internal/gh"
)

type authMode int

const (
	authNone authMode = iota
	authJWT
	authToken
	authUser
	authInstallation
)

type caller struct {
	kind           string
	installationID int64
	login          string
	reason         string
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, c *caller)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

const docsURL = "https://docs.github.com/rest"

func (s *Server) routes() {
	s.handle("GET /app", authJWT, s.getApp)
	s.handle("GET /app/installations", authJWT, s.listInstallations)
	s.handle("GET /app/installations/{installation_id}", authJWT, s.getInstallation)
	s.handle("POST /app/installations/{installation_id}/access_tokens", authJWT, s.createToken)
	s.handle("GET /installation/repositories", authInstallation, s.installationRepos)

	s.handle("GET /repos/{owner}/{repo}", authToken, s.getRepo)
	s.handle("POST /repos/{owner}/{repo}/check-runs", authToken, s.createCheckRun)
	s.handle("PATCH /repos/{owner}/{repo}/check-runs/{check_run_id}", authToken, s.updateCheckRun)
	s.handle("GET /repos/{owner}/{repo}/commits/{ref}/check-runs", authToken, s.listCheckRuns)

	s.handle("GET /repos/{owner}/{repo}/pulls/{pull_number}", authToken, s.getPull)
	s.handle("GET /repos/{owner}/{repo}/pulls/{pull_number}/reviews", authToken, s.listReviews)
	s.handle("GET /repos/{owner}/{repo}/pulls/{pull_number}/files", authToken, s.listFiles)

	s.handle("GET /repos/{owner}/{repo}/issues/{issue_number}/comments", authToken, s.listComments)
	s.handle("POST /repos/{owner}/{repo}/issues/{issue_number}/comments", authToken, s.createComment)
	s.handle("PATCH /repos/{owner}/{repo}/issues/comments/{comment_id}", authToken, s.updateComment)
	s.handle("DELETE /repos/{owner}/{repo}/issues/comments/{comment_id}", authToken, s.deleteComment)
	s.handle("POST /repos/{owner}/{repo}/issues/comments/{comment_id}/reactions", authToken, s.createReaction)
	s.handle("GET /repos/{owner}/{repo}/issues", authToken, s.listIssues)
	s.handle("POST /repos/{owner}/{repo}/issues", authToken, s.createIssue)
	s.handle("PATCH /repos/{owner}/{repo}/issues/{issue_number}", authToken, s.updateIssue)

	s.handle("GET /repos/{owner}/{repo}/collaborators/{username}/permission", authToken, s.collaboratorPermission)
	s.handle("GET /orgs/{org}/teams/{team_slug}/memberships/{username}", authToken, s.teamMembership)

	s.handle("POST /repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches", authToken, s.dispatch)
	s.handle("GET /repos/{owner}/{repo}/actions/runs", authToken, s.listRuns)
	s.handle("GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs", authToken, s.listRuns)
	s.handle("GET /repos/{owner}/{repo}/actions/runs/{run_id}", authToken, s.getRun)
	s.handle("GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs", authToken, s.listJobs)
	s.handle("GET /repos/{owner}/{repo}/actions/runs/{run_id}/artifacts", authToken, s.listArtifacts)
	s.handle("POST /repos/{owner}/{repo}/actions/runs/{run_id}/deployment_protection_rule", authToken, s.reviewProtectionRule)
	s.handle("GET /repos/{owner}/{repo}/actions/runs/{run_id}/pending_deployments", authToken, s.pendingDeployments)

	s.handle("GET /repos/{owner}/{repo}/contents/{path...}", authToken, s.getContents)
	s.handle("GET /repos/{owner}/{repo}/tags", authToken, s.listTags)
	s.handle("GET /repos/{owner}/{repo}/git/ref/{ref...}", authToken, s.getRef)

	s.handle("POST /login/oauth/access_token", authNone, s.oauthToken)
	s.handle("GET /user", authUser, s.getUser)
	s.handle("GET /user/orgs", authUser, s.userOrgs)
	s.handle("GET /user/memberships/orgs/{org}", authUser, s.userOrgMembership)
	s.handle("POST /app-manifests/{code}/conversions", authNone, s.manifestConversion)

	s.handle("/", authNone, func(w http.ResponseWriter, _ *http.Request, _ *caller) {
		writeError(w, http.StatusNotFound, "Not Found")
	})
}

func (s *Server) handle(pattern string, mode authMode, h handlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		c := s.authenticate(r)
		defer s.record(r, body, c, sw)

		s.mu.Lock()
		latency := s.latency
		s.mu.Unlock()
		if latency > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(latency):
			}
		}
		s.rateHeaders(sw)
		if s.injectFailure(sw, r) {
			return
		}
		if !s.authorize(sw, c, mode) {
			return
		}
		h(sw, r, c)
	})
}

func (s *Server) record(r *http.Request, body []byte, c *caller, w *statusWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{
		Method:         r.Method,
		Path:           r.URL.Path,
		RawQuery:       r.URL.RawQuery,
		Pattern:        r.Pattern,
		Auth:           c.kind,
		InstallationID: c.installationID,
		Login:          c.login,
		Header:         r.Header.Clone(),
		Body:           body,
		Status:         w.status,
	})
}

func (s *Server) rateHeaders(w http.ResponseWriter) {
	s.mu.Lock()
	reset := s.now().Add(time.Hour).Unix()
	s.mu.Unlock()
	h := w.Header()
	h.Set("X-RateLimit-Limit", "5000")
	h.Set("X-RateLimit-Remaining", "4999")
	h.Set("X-RateLimit-Used", "1")
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
	h.Set("X-RateLimit-Resource", "core")
}

func (s *Server) injectFailure(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	var hit *failure
	for i := range s.failures {
		f := &s.failures[i]
		if f.left <= 0 {
			continue
		}
		if f.route == "" || f.route == r.Pattern || f.route == r.Method+" "+r.URL.Path {
			f.left--
			cp := *f
			hit = &cp
			break
		}
	}
	s.mu.Unlock()
	if hit == nil {
		return false
	}
	if hit.status == 0 {
		panic(http.ErrAbortHandler)
	}
	for k, v := range hit.header {
		w.Header()[k] = v
	}
	msg := hit.message
	if msg == "" {
		msg = http.StatusText(hit.status)
	}
	writeError(w, hit.status, msg)
	return true
}

func (s *Server) authenticate(r *http.Request) *caller {
	h := r.Header.Get("Authorization")
	if h == "" {
		return &caller{kind: "none"}
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || (!strings.EqualFold(scheme, "bearer") && !strings.EqualFold(scheme, "token")) {
		return &caller{kind: "invalid", reason: "Bad credentials"}
	}
	tok = strings.TrimSpace(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	if it, ok := s.tokens[tok]; ok {
		if !s.now().Before(it.expiresAt) {
			return &caller{kind: "invalid", reason: "Bad credentials"}
		}
		return &caller{kind: "installation", installationID: it.installationID, login: s.app.Slug + "[bot]"}
	}
	if login, ok := s.userTokens[tok]; ok {
		return &caller{kind: "user", login: login}
	}
	if strings.Count(tok, ".") == 2 {
		if reason := s.checkJWT(tok); reason != "" {
			return &caller{kind: "invalid", reason: reason}
		}
		return &caller{kind: "jwt"}
	}
	return &caller{kind: "invalid", reason: "Bad credentials"}
}

func (s *Server) checkJWT(tok string) string {
	var claims jwt.RegisteredClaims
	now := s.now()
	if s.appPublicKey != nil {
		_, err := jwt.ParseWithClaims(tok, &claims, func(*jwt.Token) (any, error) { return s.appPublicKey, nil },
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithTimeFunc(func() time.Time { return now }),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(5*time.Second),
			jwt.WithExpirationRequired(),
		)
		if err != nil {
			return "A JSON web token could not be decoded: " + err.Error()
		}
	} else if _, _, err := jwt.NewParser().ParseUnverified(tok, &claims); err != nil {
		return "A JSON web token could not be decoded"
	}
	if claims.Issuer != strconv.FormatInt(s.app.ID, 10) {
		return "'Issuer' claim ('iss') must be the App id"
	}
	if claims.ExpiresAt == nil || claims.IssuedAt == nil {
		return "'Expiration time' and 'Issued at' claims are required"
	}
	if claims.ExpiresAt.After(now.Add(10*time.Minute + 5*time.Second)) {
		return "'Expiration time' claim ('exp') is too far in the future"
	}
	if !claims.ExpiresAt.After(now) {
		return "'Expiration time' claim ('exp') must be a numeric value representing the future time at which the assertion expires"
	}
	return ""
}

func (s *Server) authorize(w http.ResponseWriter, c *caller, mode authMode) bool {
	if c.kind == "invalid" {
		writeError(w, http.StatusUnauthorized, c.reason)
		return false
	}
	switch mode {
	case authJWT:
		if c.kind != "jwt" {
			writeError(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
			return false
		}
	case authToken:
		if c.kind != "installation" && c.kind != "user" {
			writeError(w, http.StatusUnauthorized, "Requires authentication")
			return false
		}
	case authUser:
		if c.kind == "installation" {
			writeError(w, http.StatusForbidden, "Resource not accessible by integration")
			return false
		}
		if c.kind != "user" {
			writeError(w, http.StatusUnauthorized, "Requires authentication")
			return false
		}
	case authInstallation:
		if c.kind == "user" {
			writeError(w, http.StatusForbidden, "You must authenticate with an installation access token")
			return false
		}
		if c.kind != "installation" {
			writeError(w, http.StatusUnauthorized, "Requires authentication")
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg, "documentation_url": docsURL})
}

func writeValidation(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"message":           "Validation Failed",
		"errors":            []map[string]string{{"code": "custom", "message": msg}},
		"documentation_url": docsURL,
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Problems parsing JSON")
		return false
	}
	return true
}

func pathInt(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n
}

func (s *Server) botUser() gh.User {
	return gh.User{Login: s.app.Slug + "[bot]", ID: 41898282 + s.app.ID, Type: "Bot"}
}

func (s *Server) actor(c *caller) gh.User {
	if c.kind == "user" {
		return s.user(c.login)
	}
	return s.botUser()
}

func (s *Server) repoFor(w http.ResponseWriter, r *http.Request, c *caller) (*repoState, bool) {
	rs, ok := s.repos[key(r.PathValue("owner"), r.PathValue("repo"))]
	if !ok || (c.kind == "installation" && rs.installation != 0 && rs.installation != c.installationID) {
		writeError(w, http.StatusNotFound, "Not Found")
		return nil, false
	}
	return rs, true
}

func pageOf[T any](s *Server, w http.ResponseWriter, r *http.Request, items []T) []T {
	q := r.URL.Query()
	per, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || per <= 0 {
		per = 30
	}
	per = min(per, 100)
	if s.pageSize > 0 {
		per = min(per, s.pageSize)
	}
	page, err := strconv.Atoi(q.Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	last := max((len(items)+per-1)/per, 1)
	link := func(p int, rel string) string {
		lq := r.URL.Query()
		lq.Set("page", strconv.Itoa(p))
		return fmt.Sprintf(`<%s%s?%s>; rel="%s"`, s.srv.URL, r.URL.EscapedPath(), lq.Encode(), rel)
	}
	var links []string
	if page < last {
		links = append(links, link(page+1, "next"), link(last, "last"))
	}
	if page > 1 {
		links = append(links, link(page-1, "prev"), link(1, "first"))
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	lo := min((page-1)*per, len(items))
	hi := min(lo+per, len(items))
	return items[lo:hi]
}

func (s *Server) getApp(w http.ResponseWriter, _ *http.Request, _ *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.app)
}

func (s *Server) listInstallations(w http.ResponseWriter, r *http.Request, _ *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, pageOf(s, w, r, s.sortedInstallations()))
}

func (s *Server) getInstallation(w http.ResponseWriter, r *http.Request, _ *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := pathInt(r, "installation_id")
	in, ok := s.installations[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, in.inst)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, _ *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := pathInt(r, "installation_id")
	if _, ok := s.installations[id]; !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	if s.suspendedInsts[id] {
		writeError(w, http.StatusForbidden, "This installation has been suspended")
		return
	}
	writeJSON(w, http.StatusCreated, s.mintToken(id))
}

func (s *Server) installationRepos(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.installations[c.installationID]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	repos := make([]gh.Repository, 0, len(in.repos))
	for _, full := range in.repos {
		repos = append(repos, s.repos[strings.ToLower(full)].repo)
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repos), "repositories": pageOf(s, w, r, repos)})
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, ok := s.repoFor(w, r, c); ok {
		writeJSON(w, http.StatusOK, rs.repo)
	}
}

var (
	checkStatuses    = map[string]bool{"queued": true, "in_progress": true, "completed": true, "waiting": true, "requested": true, "pending": true}
	checkConclusions = map[string]bool{"success": true, "failure": true, "neutral": true, "cancelled": true, "skipped": true, "timed_out": true, "action_required": true}
)

func validateCheck(p gh.CheckRunParams) string {
	if p.Status != "" && !checkStatuses[p.Status] {
		return "status is not included in the list"
	}
	if p.Conclusion != "" && !checkConclusions[p.Conclusion] {
		return "conclusion is not included in the list"
	}
	if p.Status == gh.CheckRunCompleted && p.Conclusion == "" {
		return "conclusion is required when status is completed"
	}
	if (p.Output.Summary != "" || p.Output.Text != "") && p.Output.Title == "" {
		return "output.title is required"
	}
	if p.Output.Title != "" && p.Output.Summary == "" {
		return "output.summary is required"
	}
	if len(p.Actions) > 3 {
		return "actions must have at most 3 elements"
	}
	return ""
}

func (s *Server) applyCheck(run *gh.CheckRun, p gh.CheckRunParams) {
	if p.Name != "" {
		run.Name = p.Name
	}
	if p.Conclusion != "" {
		run.Conclusion = p.Conclusion
		run.Status = gh.CheckRunCompleted
	}
	if p.Status != "" {
		run.Status = p.Status
	}
	if p.DetailsURL != "" {
		run.DetailsURL = p.DetailsURL
	}
	if p.ExternalID != "" {
		run.ExternalID = p.ExternalID
	}
	if p.Output != (gh.CheckRunOutput{}) {
		run.Output = p.Output
	}
	if !p.StartedAt.IsZero() {
		run.StartedAt = p.StartedAt
	}
	if !p.CompletedAt.IsZero() {
		run.CompletedAt = p.CompletedAt
	}
	if run.Status == gh.CheckRunCompleted && run.CompletedAt.IsZero() {
		run.CompletedAt = s.now().Truncate(time.Second)
	}
}

func (s *Server) createCheckRun(w http.ResponseWriter, r *http.Request, c *caller) {
	var p gh.CheckRunParams
	if !decodeBody(w, r, &p) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	if p.Name == "" || p.HeadSHA == "" {
		writeValidation(w, "name and head_sha are required")
		return
	}
	if msg := validateCheck(p); msg != "" {
		writeValidation(w, msg)
		return
	}
	id := s.id()
	run := gh.CheckRun{
		ID:         id,
		HeadSHA:    p.HeadSHA,
		Status:     gh.CheckRunQueued,
		HTMLURL:    fmt.Sprintf("https://github.com/%s/runs/%d", rs.repo.FullName, id),
		App:        &gh.AppRef{ID: s.app.ID, Slug: s.app.Slug},
		StartedAt:  s.now().Truncate(time.Second),
		CheckSuite: &gh.CheckSuiteRef{ID: id + 500000, HeadSHA: p.HeadSHA},
	}
	s.applyCheck(&run, p)
	rs.checkRuns = append(rs.checkRuns, &CheckRun{CheckRun: run, Repo: rs.repo.FullName, History: []gh.CheckRunParams{p}})
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) updateCheckRun(w http.ResponseWriter, r *http.Request, c *caller) {
	var raw map[string]json.RawMessage
	body, _ := io.ReadAll(r.Body)
	if json.Unmarshal(body, &raw) != nil {
		writeError(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	if _, ok := raw["head_sha"]; ok {
		writeValidation(w, "head_sha is not a permitted key")
		return
	}
	var p gh.CheckRunParams
	if json.Unmarshal(body, &p) != nil {
		writeError(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	id := pathInt(r, "check_run_id")
	for _, cr := range rs.checkRuns {
		if cr.ID != id {
			continue
		}
		merged := p
		if merged.Status == "" && merged.Conclusion == "" {
			merged.Status = cr.Status
			merged.Conclusion = cr.Conclusion
		}
		if msg := validateCheck(merged); msg != "" {
			writeValidation(w, msg)
			return
		}
		s.applyCheck(&cr.CheckRun, p)
		cr.History = append(cr.History, p)
		writeJSON(w, http.StatusOK, cr.CheckRun)
		return
	}
	writeError(w, http.StatusNotFound, "Not Found")
}

func (s *Server) resolveRef(rs *repoState, ref string) string {
	for _, prefix := range []string{"heads/", "tags/"} {
		if sha, ok := rs.refs[prefix+ref]; ok {
			return sha
		}
	}
	return ref
}

func (s *Server) listCheckRuns(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	sha := s.resolveRef(rs, r.PathValue("ref"))
	q := r.URL.Query()
	name, status, filter := q.Get("check_name"), q.Get("status"), q.Get("filter")
	latest := map[string]int{}
	var out []gh.CheckRun
	for _, cr := range rs.checkRuns {
		if cr.HeadSHA != sha || (name != "" && cr.Name != name) || (status != "" && cr.Status != status) {
			continue
		}
		if filter != "all" {
			if i, seen := latest[cr.Name]; seen {
				out[i] = cr.CheckRun
				continue
			}
			latest[cr.Name] = len(out)
		}
		out = append(out, cr.CheckRun)
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(out), "check_runs": pageOf(s, w, r, out)})
}

func (s *Server) pullFor(w http.ResponseWriter, r *http.Request, c *caller) (*repoState, *gh.PullRequest, bool) {
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return nil, nil, false
	}
	n := pathInt(r, "pull_number")
	pr, ok := rs.pulls[int(n)]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return nil, nil, false
	}
	return rs, pr, true
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, pr, ok := s.pullFor(w, r, c); ok {
		writeJSON(w, http.StatusOK, pr)
	}
}

func (s *Server) listReviews(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, pr, ok := s.pullFor(w, r, c); ok {
		writeJSON(w, http.StatusOK, pageOf(s, w, r, append([]gh.Review{}, rs.reviews[pr.Number]...)))
	}
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, pr, ok := s.pullFor(w, r, c)
	if !ok {
		return
	}
	type file struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
	}
	files := []file{}
	for _, p := range rs.files[pr.Number] {
		files = append(files, file{Filename: p, Status: "modified"})
	}
	writeJSON(w, http.StatusOK, pageOf(s, w, r, files))
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	n := pathInt(r, "issue_number")
	out := []gh.Comment{}
	for _, cm := range rs.comments {
		if cm.issue == int(n) {
			out = append(out, cm.comment)
		}
	}
	writeJSON(w, http.StatusOK, pageOf(s, w, r, out))
}

type commentBody struct {
	Body string `json:"body"`
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request, c *caller) {
	var body commentBody
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	if body.Body == "" {
		writeValidation(w, "body cannot be blank")
		return
	}
	n := pathInt(r, "issue_number")
	u := s.actor(c)
	assoc := "NONE"
	if c.kind == "user" {
		assoc = s.association(rs, c.login)
	}
	writeJSON(w, http.StatusCreated, s.addComment(rs, int(n), u, assoc, body.Body))
}

func (s *Server) commentFor(w http.ResponseWriter, r *http.Request, c *caller) (*repoState, int, bool) {
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return nil, 0, false
	}
	id := pathInt(r, "comment_id")
	for i, cm := range rs.comments {
		if cm.comment.ID == id {
			return rs, i, true
		}
	}
	writeError(w, http.StatusNotFound, "Not Found")
	return nil, 0, false
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request, c *caller) {
	var body commentBody
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, i, ok := s.commentFor(w, r, c)
	if !ok {
		return
	}
	if body.Body == "" {
		writeValidation(w, "body cannot be blank")
		return
	}
	cm := &rs.comments[i].comment
	cm.Body = body.Body
	cm.UpdatedAt = s.now()
	writeJSON(w, http.StatusOK, cm)
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, i, ok := s.commentFor(w, r, c)
	if !ok {
		return
	}
	rs.comments = append(rs.comments[:i], rs.comments[i+1:]...)
	w.WriteHeader(http.StatusNoContent)
}

var reactionContents = map[string]bool{"+1": true, "-1": true, "laugh": true, "confused": true, "heart": true, "hooray": true, "rocket": true, "eyes": true}

func (s *Server) createReaction(w http.ResponseWriter, r *http.Request, c *caller) {
	var body struct {
		Content string `json:"content"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, i, ok := s.commentFor(w, r, c)
	if !ok {
		return
	}
	if !reactionContents[body.Content] {
		writeValidation(w, "content is not included in the list")
		return
	}
	id := rs.comments[i].comment.ID
	u := s.actor(c)
	resp := map[string]any{"id": s.id(), "content": body.Content, "user": u}
	for _, re := range s.reactions[id] {
		if re.content == body.Content && re.login == u.Login {
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	s.reactions[id] = append(s.reactions[id], reaction{content: body.Content, login: u.Login})
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) nextIssueNumber(rs *repoState) int {
	n := 0
	for num := range rs.pulls {
		n = max(n, num)
	}
	for _, is := range rs.issues {
		n = max(n, is.Number)
	}
	return n + 1
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request, c *caller) {
	var p gh.IssueParams
	if !decodeBody(w, r, &p) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	if p.Title == "" {
		writeValidation(w, "title cannot be blank")
		return
	}
	now := s.now()
	is := &gh.Issue{
		Number:    s.nextIssueNumber(rs),
		Title:     p.Title,
		Body:      p.Body,
		State:     gh.IssueOpen,
		Labels:    append(gh.Labels{}, p.Labels...),
		User:      s.actor(c),
		CreatedAt: now,
		UpdatedAt: now,
	}
	is.HTMLURL = fmt.Sprintf("https://github.com/%s/issues/%d", rs.repo.FullName, is.Number)
	rs.issues = append(rs.issues, is)
	writeJSON(w, http.StatusCreated, is)
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request, c *caller) {
	var p gh.IssueParams
	if !decodeBody(w, r, &p) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	n := pathInt(r, "issue_number")
	for _, is := range rs.issues {
		if is.Number != int(n) {
			continue
		}
		if p.State != "" && p.State != gh.IssueOpen && p.State != gh.IssueClosed {
			writeValidation(w, "state is not included in the list")
			return
		}
		if p.Title != "" {
			is.Title = p.Title
		}
		if p.Body != "" {
			is.Body = p.Body
		}
		if p.Labels != nil {
			is.Labels = append(gh.Labels{}, p.Labels...)
		}
		now := s.now()
		switch p.State {
		case gh.IssueClosed:
			is.State, is.ClosedAt = gh.IssueClosed, &now
		case gh.IssueOpen:
			is.State, is.ClosedAt = gh.IssueOpen, nil
		}
		is.UpdatedAt = now
		writeJSON(w, http.StatusOK, is)
		return
	}
	writeError(w, http.StatusNotFound, "Not Found")
}

func hasLabels(have gh.Labels, want []string) bool {
	for _, l := range want {
		found := false
		for _, h := range have {
			if strings.EqualFold(h, l) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = gh.IssueOpen
	}
	var labels []string
	if l := q.Get("labels"); l != "" {
		labels = strings.Split(l, ",")
	}
	match := func(st string, have gh.Labels) bool {
		return (state == gh.IssueAll || st == state) && hasLabels(have, labels)
	}
	out := []gh.Issue{}
	for _, is := range rs.issues {
		if match(is.State, is.Labels) {
			out = append(out, *is)
		}
	}
	for _, pr := range rs.pulls {
		if match(pr.State, pr.Labels) {
			out = append(out, gh.Issue{
				Number:      pr.Number,
				Title:       pr.Title,
				State:       pr.State,
				Labels:      pr.Labels,
				User:        pr.User,
				HTMLURL:     pr.HTMLURL,
				PullRequest: &gh.IssuePullRequest{HTMLURL: pr.HTMLURL},
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	writeJSON(w, http.StatusOK, pageOf(s, w, r, out))
}

func (s *Server) collaboratorPermission(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	login := r.PathValue("username")
	perm := rs.collaborators[strings.ToLower(login)]
	if perm == "" {
		perm = gh.PermissionNone
	}
	legacy := perm
	switch perm {
	case "maintain":
		legacy = gh.PermissionWrite
	case "triage":
		legacy = gh.PermissionRead
	}
	writeJSON(w, http.StatusOK, map[string]any{"permission": legacy, "role_name": perm, "user": s.user(login)})
}

func (s *Server) teamMembership(w http.ResponseWriter, r *http.Request, _ *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.teams[key(r.PathValue("org"), r.PathValue("team_slug"), r.PathValue("username"))]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": state, "role": "member"})
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, c *caller) {
	var body struct {
		Ref    string         `json:"ref"`
		Inputs map[string]any `json:"inputs"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		s.mu.Unlock()
		return
	}
	if body.Ref == "" {
		s.mu.Unlock()
		writeValidation(w, "ref is required")
		return
	}
	sha, known := "", false
	for _, prefix := range []string{"heads/", "tags/"} {
		if v, ok := rs.refs[prefix+body.Ref]; ok {
			sha, known = v, true
			break
		}
	}
	if !known && body.Ref != rs.repo.DefaultBranch {
		s.mu.Unlock()
		writeValidation(w, "No ref found for: "+body.Ref)
		return
	}
	inputs := map[string]string{}
	for k, v := range body.Inputs {
		if str, isStr := v.(string); isStr {
			inputs[k] = str
		} else {
			inputs[k] = fmt.Sprint(v)
		}
	}
	file := r.PathValue("workflow_id")
	run := s.addRun(rs, gh.WorkflowRun{
		Path:       ".github/workflows/" + file,
		Event:      "workflow_dispatch",
		HeadBranch: body.Ref,
		HeadSHA:    sha,
	})
	d := Dispatch{Repo: rs.repo.FullName, Workflow: file, Ref: body.Ref, Inputs: inputs, RunID: run.ID, At: s.now()}
	s.dispatches = append(s.dispatches, d)
	hook := s.onDispatch
	s.mu.Unlock()
	if hook != nil {
		hook(d)
	}
	w.WriteHeader(http.StatusNoContent)
}

func matchCreated(filter string, t time.Time) bool {
	if filter == "" {
		return true
	}
	parse := func(v string) (time.Time, bool) {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if ts, err := time.Parse(layout, v); err == nil {
				return ts, true
			}
		}
		return time.Time{}, false
	}
	switch {
	case strings.HasPrefix(filter, ">="):
		ts, ok := parse(filter[2:])
		return ok && !t.Before(ts)
	case strings.HasPrefix(filter, ">"):
		ts, ok := parse(filter[1:])
		return ok && t.After(ts)
	case strings.HasPrefix(filter, "<="):
		ts, ok := parse(filter[2:])
		return ok && !t.After(ts)
	case strings.HasPrefix(filter, "<"):
		ts, ok := parse(filter[1:])
		return ok && t.Before(ts)
	}
	ts, ok := parse(filter)
	return ok && t.UTC().Format("2006-01-02") == ts.UTC().Format("2006-01-02")
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	q := r.URL.Query()
	workflow := r.PathValue("workflow_id")
	out := []gh.WorkflowRun{}
	for _, run := range rs.runs {
		switch {
		case workflow != "" && !strings.HasSuffix(run.Path, "/"+workflow) && strconv.FormatInt(run.WorkflowID, 10) != workflow,
			q.Get("event") != "" && run.Event != q.Get("event"),
			q.Get("branch") != "" && run.HeadBranch != q.Get("branch"),
			q.Get("head_sha") != "" && run.HeadSHA != q.Get("head_sha"),
			q.Get("status") != "" && run.Status != q.Get("status") && run.Conclusion != q.Get("status"),
			!matchCreated(q.Get("created"), run.CreatedAt):
			continue
		}
		out = append(out, *run)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(out), "workflow_runs": pageOf(s, w, r, out)})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	id := pathInt(r, "run_id")
	if run := s.findRun(rs.repo.FullName, id); run != nil {
		writeJSON(w, http.StatusOK, run)
		return
	}
	writeError(w, http.StatusNotFound, "Not Found")
}

func (s *Server) runData(w http.ResponseWriter, r *http.Request, c *caller) (*repoState, int64, bool) {
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return nil, 0, false
	}
	id := pathInt(r, "run_id")
	_, hasJobs := rs.jobs[id]
	_, hasArtifacts := rs.artifacts[id]
	_, hasPending := rs.pending[id]
	if s.findRun(rs.repo.FullName, id) == nil && !hasJobs && !hasArtifacts && !hasPending {
		writeError(w, http.StatusNotFound, "Not Found")
		return nil, 0, false
	}
	return rs, id, true
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, id, ok := s.runData(w, r, c); ok {
		jobs := append([]gh.WorkflowJob{}, rs.jobs[id]...)
		writeJSON(w, http.StatusOK, map[string]any{"total_count": len(jobs), "jobs": pageOf(s, w, r, jobs)})
	}
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, id, ok := s.runData(w, r, c); ok {
		arts := append([]gh.Artifact{}, rs.artifacts[id]...)
		writeJSON(w, http.StatusOK, map[string]any{"total_count": len(arts), "artifacts": pageOf(s, w, r, arts)})
	}
}

func (s *Server) pendingDeployments(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, id, ok := s.runData(w, r, c); ok {
		writeJSON(w, http.StatusOK, append([]gh.PendingDeployment{}, rs.pending[id]...))
	}
}

func (s *Server) reviewProtectionRule(w http.ResponseWriter, r *http.Request, c *caller) {
	var body struct {
		EnvironmentName string `json:"environment_name"`
		State           string `json:"state"`
		Comment         string `json:"comment"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	if c.kind != "installation" {
		writeError(w, http.StatusForbidden, "Only GitHub Apps can review deployment protection rules")
		return
	}
	if body.EnvironmentName == "" || (body.State != gh.DeploymentApproved && body.State != gh.DeploymentRejected) {
		writeValidation(w, "environment_name and a state of approved or rejected are required")
		return
	}
	id := pathInt(r, "run_id")
	s.decisions = append(s.decisions, ProtectionRuleDecision{
		Repo: rs.repo.FullName, RunID: id, EnvironmentName: body.EnvironmentName,
		State: body.State, Comment: body.Comment, At: s.now(),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getContents(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	files := s.contentsAt(rs, r.URL.Query().Get("ref"))
	p := strings.Trim(r.PathValue("path"), "/")
	if data, ok := files[p]; ok {
		enc := base64.StdEncoding.EncodeToString(data)
		var wrapped strings.Builder
		for len(enc) > 60 {
			wrapped.WriteString(enc[:60])
			wrapped.WriteString("\n")
			enc = enc[60:]
		}
		wrapped.WriteString(enc)
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "file", "encoding": "base64", "size": len(data),
			"name": p[strings.LastIndex(p, "/")+1:], "path": p, "content": wrapped.String(),
		})
		return
	}
	var entries []map[string]any
	for fp := range files {
		if rest, ok := strings.CutPrefix(fp, p+"/"); ok && p != "" {
			entries = append(entries, map[string]any{"type": "file", "name": rest, "path": fp})
		}
	}
	if len(entries) > 0 {
		writeJSON(w, http.StatusOK, entries)
		return
	}
	writeError(w, http.StatusNotFound, "Not Found")
}

func (s *Server) contentsAt(rs *repoState, ref string) map[string][]byte {
	if ref == "" {
		ref = rs.repo.DefaultBranch
	}
	if files, ok := rs.contents[ref]; ok {
		return files
	}
	if files, ok := rs.contents[s.resolveRef(rs, ref)]; ok {
		return files
	}
	for name, sha := range rs.refs {
		if sha != ref {
			continue
		}
		short := name[strings.Index(name, "/")+1:]
		if files, ok := rs.contents[short]; ok {
			return files
		}
	}
	return nil
}

func (s *Server) listTags(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, ok := s.repoFor(w, r, c); ok {
		writeJSON(w, http.StatusOK, pageOf(s, w, r, append([]gh.Tag{}, rs.tags...)))
	}
}

func (s *Server) getRef(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.repoFor(w, r, c)
	if !ok {
		return
	}
	ref := r.PathValue("ref")
	sha, ok := rs.refs[ref]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": "refs/" + ref, "object": map[string]string{"type": "commit", "sha": sha}})
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request, _ *caller) {
	values := url.Values{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body map[string]string
		if !decodeBody(w, r, &body) {
			return
		}
		for k, v := range body {
			values.Set(k, v)
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "Problems parsing form")
			return
		}
		values = r.PostForm
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oauthClientID != "" && (values.Get("client_id") != s.oauthClientID || values.Get("client_secret") != s.oauthSecret) {
		writeJSON(w, http.StatusOK, map[string]string{
			"error": "incorrect_client_credentials", "error_description": "The client_id and/or client_secret passed are incorrect.",
		})
		return
	}
	login, ok := s.oauthCodes[values.Get("code")]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{
			"error": "bad_verification_code", "error_description": "The code passed is incorrect or expired.",
		})
		return
	}
	delete(s.oauthCodes, values.Get("code"))
	writeJSON(w, http.StatusOK, map[string]string{"access_token": s.mintUserToken(login), "token_type": "bearer", "scope": ""})
}

func (s *Server) getUser(w http.ResponseWriter, _ *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.user(c.login))
}

func (s *Server) userOrgs(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orgs := []gh.User{}
	for k, m := range s.orgMembers {
		org, login, _ := strings.Cut(k, "/")
		if login == strings.ToLower(c.login) && m.state == gh.MembershipActive {
			orgs = append(orgs, gh.User{Login: org, Type: "Organization"})
		}
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].Login < orgs[j].Login })
	writeJSON(w, http.StatusOK, pageOf(s, w, r, orgs))
}

func (s *Server) userOrgMembership(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := r.PathValue("org")
	m, ok := s.orgMembers[key(org, c.login)]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": m.state, "role": m.role,
		"organization": gh.User{Login: org, Type: "Organization"}, "user": s.user(c.login),
	})
}

func (s *Server) manifestConversion(w http.ResponseWriter, r *http.Request, _ *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := r.PathValue("code")
	creds, ok := s.manifests[code]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	delete(s.manifests, code)
	writeJSON(w, http.StatusCreated, creds)
}
