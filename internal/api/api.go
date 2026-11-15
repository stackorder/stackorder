package api

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
)

type server struct {
	cfg        Config
	db         dataStore
	runs       RunService
	verifier   *oidc.Verifier
	app        *gh.App
	hc         *http.Client
	ui         http.Handler
	metrics    http.Handler
	webhook    http.Handler
	artifacts  ArtifactReader
	artifactM  ArtifactMetrics
	instrument func(route string) func(http.Handler) http.Handler
	log        *slog.Logger
	now        func() time.Time

	origin     string
	secure     bool
	hsts       string
	webURL     string
	apiURL     string
	sessionKey []byte
	csp        string

	slugMu sync.Mutex
	slug   string

	setupState atomic.Int32
}

// New returns the HTTP handler serving every endpoint of the server: the
// runner, human and automation API under /v1, sign-in under /auth, the App
// setup pages under /setup, health and metrics, the webhook receiver and,
// for every other path, the UI. It panics when Deps.Store is nil, when
// Deps.Runs is nil outside setup mode, or when Config.BaseURL is not an
// absolute URL, all of which are programming errors.
func New(cfg Config, d Deps) http.Handler {
	if d.Store == nil {
		panic("api: Deps.Store is required")
	}
	if d.Runs == nil && !cfg.SetupMode {
		panic("api: Deps.Runs is required outside setup mode")
	}
	return newServer(cfg, d, d.Store).routes()
}

func newServer(cfg Config, d Deps, db dataStore) *server {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		panic("api: Config.BaseURL must be an absolute http or https URL, got " + cfg.BaseURL)
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	s := &server{
		cfg:        cfg,
		db:         db,
		runs:       d.Runs,
		verifier:   d.Verifier,
		app:        d.GitHub,
		hc:         d.HTTPClient,
		ui:         d.UI,
		metrics:    d.Metrics,
		webhook:    d.Webhook,
		artifacts:  d.Artifacts,
		artifactM:  d.ArtifactMetrics,
		instrument: d.Instrument,
		log:        d.Logger,
		now:        d.Clock,
		origin:     originOf(base),
		secure:     base.Scheme == "https",
		webURL:     strings.TrimRight(cfg.GitHubWebURL, "/"),
		apiURL:     strings.TrimRight(cfg.GitHubAPIURL, "/"),
		sessionKey: cfg.SessionKey,
		slug:       cfg.AppSlug,
	}
	s.cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.hc == nil {
		s.hc = &http.Client{Timeout: 30 * time.Second}
	}
	if s.webURL == "" {
		s.webURL = gh.DefaultWebURL
	}
	if s.apiURL == "" {
		s.apiURL = gh.DefaultBaseURL
	}
	if len(s.sessionKey) == 0 {
		s.sessionKey = make([]byte, 32)
		_, _ = rand.Read(s.sessionKey)
		s.log.Warn("no session key configured; generated one, so sessions end when the server restarts")
	}
	s.csp = s.defaultCSP()
	s.hsts = hstsHeader(cfg)
	return s
}

func originOf(u *url.URL) string {
	host := strings.ToLower(u.Host)
	if port := u.Port(); (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		host = strings.TrimSuffix(host, ":"+port)
	}
	return u.Scheme + "://" + host
}

func (s *server) defaultCSP() string {
	images := []string{"'self'", "data:", "https://avatars.githubusercontent.com"}
	if u, err := url.Parse(s.webURL); err == nil && u.Host != "" && !strings.EqualFold(u.Host, "github.com") {
		images = append(images, u.Scheme+"://"+u.Host, u.Scheme+"://avatars."+u.Host)
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src " + strings.Join(images, " "),
		"connect-src 'self'",
		"font-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	s.handle(mux, "GET /healthz", 0, http.HandlerFunc(s.healthz))
	s.handle(mux, "GET /readyz", 0, http.HandlerFunc(s.readyz))
	s.handle(mux, "GET /setup", 0, http.HandlerFunc(s.setup))
	s.handle(mux, "GET /setup/callback", 0, http.HandlerFunc(s.setupCallback))
	s.handle(mux, "GET /setup/installed", 0, http.HandlerFunc(s.setupInstalled))
	s.handle(mux, "GET /setup/logo.png", 0, http.HandlerFunc(s.setupLogo))
	if s.cfg.SetupMode {
		s.handle(mux, "/", 0, http.HandlerFunc(s.setupRequired))
		return s.observe(s.securityHeaders(mux))
	}
	s.handle(mux, "GET /metrics", 0, http.HandlerFunc(s.serveMetrics))
	s.handle(mux, "POST /webhooks/github", 0, http.HandlerFunc(s.serveWebhook))

	for _, rt := range authRoutes(s) {
		s.handle(mux, rt.pattern, rt.limit, s.with(rt.mode, rt.handler))
	}

	s.handle(mux, "GET /auth/login", 0, http.HandlerFunc(s.login))
	s.handle(mux, "GET /auth/callback", 0, http.HandlerFunc(s.callback))
	s.handle(mux, "POST /auth/logout", maxBodyBytes, http.HandlerFunc(s.logout))

	s.handle(mux, "/v1/", 0, http.HandlerFunc(s.unknownEndpoint))
	s.handle(mux, "/auth/", 0, http.HandlerFunc(s.unknownEndpoint))
	s.handle(mux, "/", 0, http.HandlerFunc(s.serveUI))
	return s.observe(s.securityHeaders(mux))
}

// authRoute is one authenticated /v1 route. The read-only and installation
// scope of an API key is enforced centrally in with() and, for routes that
// reach the run service, in internal/runs, so the table is the full list a
// test enumerates to assert that every mutating route an API key can present
// refuses a read-only key, rather than a hand-written one that a new route
// could escape.
type authRoute struct {
	pattern string
	limit   int64
	mode    authMode
	handler handler
}

// method returns the HTTP method of the route's pattern.
func (rt authRoute) method() string {
	method, _, _ := strings.Cut(rt.pattern, " ")
	return method
}

func authRoutes(s *server) []authRoute {
	return []authRoute{
		{"POST /v1/runs", maxBodyBytes, runnerAuth, s.createRun},
		{"POST /v1/runs/{id}/graph", maxGraphBytes, runnerAuth, s.uploadGraph},
		{"PUT /v1/repos/{owner}/{repo}/default-graph", maxGraphBytes, runnerAuth, s.recordDefaultGraph},
		{"POST /v1/runs/{id}/stacks/{key}/result", maxResultBytes, runnerAuth, s.recordResult},
		{"POST /v1/runs/{id}/stacks/{key}/checks/{name}", maxBodyBytes, runnerAuth, s.recordCheck},
		{"GET /v1/runs/{id}", 0, anyAuth, s.getRun},
		{"GET /v1/runs/{id}/stacks/{key}/plan", 0, humanAuth, s.planText},
		{"POST /v1/unlock", maxBodyBytes, humanAuth | sameOrigin, s.unlockByKey},
		{"POST /v1/stacks/{id}/unlock", maxBodyBytes, humanAuth | sameOrigin, s.unlockStack},
		{"POST /v1/runs/{id}/rerun", maxBodyBytes, humanAuth | sameOrigin, s.rerun},
		{"GET /v1/me", 0, humanAuth, s.me},
		{"GET /v1/overview", 0, humanAuth, s.overview},
		{"GET /v1/repos", 0, humanAuth, s.repos},
		{"GET /v1/repos/{owner}/{repo}/graph", 0, humanAuth, s.repoGraph},
		{"GET /v1/repos/{owner}/{repo}/runs", 0, humanAuth, s.repoRuns},
		{"GET /v1/repos/{owner}/{repo}/stacks", 0, humanAuth, s.repoStacks},
		{"GET /v1/repos/{owner}/{repo}/stacks/{key}/outputs", 0, runnerAuth, s.stackOutputs},
		{"GET /v1/stacks", 0, humanAuth, s.stacks},
		{"GET /v1/stacks/{id}", 0, humanAuth, s.stack},
		{"GET /v1/stacks/{id}/runs", 0, humanAuth, s.stackRuns},
		{"GET /v1/stacks/{id}/resources", 0, humanAuth, s.stackResources},
		{"GET /v1/modules", 0, humanAuth, s.modules},
		{"GET /v1/modules/{id}", 0, humanAuth, s.module},
		{"GET /v1/audit", 0, humanAuth, s.audit},
		{"GET /v1/keys", 0, humanAuth | adminKey, s.listKeys},
		{"POST /v1/keys", maxBodyBytes, humanAuth | sameOrigin | adminKey, s.createKey},
		{"DELETE /v1/keys/{id}", 0, humanAuth | sameOrigin | adminKey, s.revokeKey},
	}
}

func (s *server) unknownEndpoint(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, notFound("no such endpoint: "+r.Method+" "+r.URL.Path))
}

func (s *server) setupRequired(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, unavailable("setup is required: the GitHub App is not configured yet; open "+s.cfg.BaseURL+"/setup with the setup token from the server log"))
}

func (s *server) serveUI(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.NotFound(w, r)
		return
	}
	s.ui.ServeHTTP(w, r)
}

func (s *server) serveWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhook == nil {
		s.writeError(w, r, unavailable("the webhook receiver is not configured"))
		return
	}
	s.webhook.ServeHTTP(w, r)
}
