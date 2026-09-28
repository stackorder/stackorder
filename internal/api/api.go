package api

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	instrument func(route string) func(http.Handler) http.Handler
	log        *slog.Logger
	now        func() time.Time

	origin     string
	secure     bool
	webURL     string
	apiURL     string
	sessionKey []byte
	csp        string

	slugMu sync.Mutex
	slug   string
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
		instrument: d.Instrument,
		log:        d.Logger,
		now:        d.Clock,
		origin:     base.Scheme + "://" + strings.ToLower(base.Host),
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
	return s
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
	if s.cfg.SetupMode {
		s.handle(mux, "/", 0, http.HandlerFunc(s.setupRequired))
		return s.observe(s.securityHeaders(mux))
	}
	s.handle(mux, "GET /metrics", 0, http.HandlerFunc(s.serveMetrics))
	s.handle(mux, "POST /webhooks/github", 0, http.HandlerFunc(s.serveWebhook))

	s.handle(mux, "POST /v1/runs", maxBodyBytes, s.with(runnerAuth, s.createRun))
	s.handle(mux, "POST /v1/runs/{id}/graph", maxGraphBytes, s.with(runnerAuth, s.uploadGraph))
	s.handle(mux, "POST /v1/runs/{id}/stacks/{key}/result", maxBodyBytes, s.with(runnerAuth, s.recordResult))
	s.handle(mux, "POST /v1/runs/{id}/stacks/{key}/checks/{name}", maxBodyBytes, s.with(runnerAuth, s.recordCheck))
	s.handle(mux, "GET /v1/runs/{id}", 0, s.with(anyAuth, s.getRun))

	s.handle(mux, "GET /v1/me", 0, s.with(humanAuth, s.me))
	s.handle(mux, "/v1/", 0, http.HandlerFunc(s.unknownEndpoint))
	s.handle(mux, "/auth/", 0, http.HandlerFunc(s.unknownEndpoint))
	s.handle(mux, "/", 0, http.HandlerFunc(s.serveUI))
	return s.observe(s.securityHeaders(mux))
}

func (s *server) unknownEndpoint(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, notFound("no such endpoint: "+r.Method+" "+r.URL.Path))
}

func (s *server) setupRequired(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, unavailable("setup is required: the GitHub App is not configured yet; open "+s.cfg.BaseURL+"/setup"))
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
