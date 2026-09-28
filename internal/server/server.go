// Package server composes the stackorder control plane into one process:
// the store and its migrations, the GitHub App, the OIDC verifier, the run
// service, the webhook receiver, the worker pool, the scheduler, the
// metrics registry and the HTTP API with the embedded UI, behind one
// http.Server. cmd/stackorder-server and the integration tests use it.
//
// Without GitHub App credentials the server runs in setup mode: it
// migrates the database and serves only the /setup pages, /healthz and
// /readyz, with no GitHub client, workers or scheduler. With them it also
// syncs the App's installations at start-up, so installations made during
// setup mode are learned, and the scheduler repeats the sync daily.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stackorder/stackorder/internal/api"
	"github.com/stackorder/stackorder/internal/artifacts"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/runs"
	"github.com/stackorder/stackorder/internal/sched"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/ui"
	"github.com/stackorder/stackorder/internal/version"
	"github.com/stackorder/stackorder/internal/webhook"
	"github.com/stackorder/stackorder/internal/worker"
)

// HTTP server limits.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 2 * time.Minute
	WriteTimeout      = 2 * time.Minute
	IdleTimeout       = 2 * time.Minute
	MaxHeaderBytes    = 1 << 20
	// ShutdownTimeout bounds how long Run waits for in-flight requests
	// once its context ends, before the workers drain.
	ShutdownTimeout = 15 * time.Second
)

const closeTimeout = 10 * time.Second

// ErrRunning is returned by Run when the server is running or has run.
var ErrRunning = errors.New("server: Run was already called")

// Option changes an optional part of a Server.
type Option func(*options)

type options struct {
	listener net.Listener
	clock    func() time.Time
	logger   *slog.Logger
}

// WithListener makes the server accept connections on ln instead of
// listening on Config.Listen. The server takes ownership of ln and closes
// it when Run returns, when Close is called or when New fails.
func WithListener(ln net.Listener) Option {
	return func(o *options) { o.listener = ln }
}

// WithClock replaces time.Now in every component that takes a clock: the
// run service, the OIDC verifier, the GitHub App, the API, the worker pool
// and the scheduler.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.clock = now }
}

// WithLogger sends the server's logs to l instead of a logger built from
// Config.LogLevel and Config.LogFormat on standard error.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// NewLogger returns the server's logger: JSON lines on w unless format is
// LogFormatText, at level and above.
func NewLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == LogFormatText {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// Server is the composed control plane. Create it with New, then call Run
// once.
type Server struct {
	cfg     Config
	log     *slog.Logger
	now     func() time.Time
	st      *store.Store
	metrics *metrics.Registry
	app     *gh.App
	runs    *runs.Service
	pool    *worker.Pool
	sched   *sched.Scheduler
	tracing *tracing
	handler http.Handler
	http    *http.Server
	ln      net.Listener

	started   atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// New validates cfg, opens the store and runs the migrations, builds every
// component and binds the listener, but starts nothing: Run does. In setup
// mode only the store, the metrics registry and the API in setup mode are
// built. On error everything opened so far is closed.
func New(ctx context.Context, cfg Config, opts ...Option) (_ *Server, err error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	s := &Server{ln: o.listener, now: o.clock, log: o.logger}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if s.log == nil {
		s.log = NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if len(cfg.SessionKey) == 0 {
		key, err := generateSessionKey()
		if err != nil {
			return nil, fmt.Errorf("server: %w", err)
		}
		cfg.SessionKey, cfg.SessionKeyGenerated = key, true
	}
	if cfg.SessionKeyGenerated {
		s.log.Warn(EnvSessionKey+" is not set; generated a random session key, so sessions end when the server restarts and are not shared between instances",
			"hint", "set it to the output of `openssl rand -hex 32`")
	}
	s.cfg = cfg

	if cfg.OTLPEndpoint != "" {
		if s.tracing, err = newTracing(ctx, cfg.OTLPEndpoint); err != nil {
			return nil, err
		}
	}
	if s.st, err = store.Open(ctx, cfg.DatabaseURL); err != nil {
		return nil, fmt.Errorf("server: open the database: %w", err)
	}
	if err := s.st.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("server: migrate the database: %w", err)
	}
	s.metrics = metrics.New()

	deps := api.Deps{
		Store:      s.st,
		Instrument: s.instrument,
		Logger:     s.log,
		Clock:      s.now,
	}
	if !cfg.SetupMode {
		if err := s.compose(ctx, &deps); err != nil {
			return nil, err
		}
	}
	s.handler = api.New(s.apiConfig(), deps)
	if s.tracing != nil {
		s.handler = s.tracing.wrap(s.handler)
	}
	if s.ln == nil {
		lc := net.ListenConfig{}
		if s.ln, err = lc.Listen(ctx, "tcp", cfg.Listen); err != nil {
			return nil, fmt.Errorf("server: listen on %s: %w", cfg.Listen, err)
		}
	}
	s.http = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	return s, nil
}

func (s *Server) compose(ctx context.Context, deps *api.Deps) error {
	cfg := s.cfg
	app, err := gh.NewApp(gh.Config{
		AppID:      cfg.AppID,
		PrivateKey: cfg.AppPrivateKey,
		BaseURL:    cfg.GitHubAPIURL,
		UserAgent:  "stackorder-server/" + version.Version,
		Metrics:    s.metrics,
		Clock:      s.now,
	})
	if err != nil {
		return fmt.Errorf("server: GitHub App: %w", err)
	}
	s.app = app
	verifier, err := oidc.New(oidc.Config{
		Issuer:   cfg.OIDCIssuer,
		JWKSURL:  cfg.OIDCJWKSURL,
		Audience: cfg.OIDCAudience,
		Clock:    s.now,
	})
	if err != nil {
		return fmt.Errorf("server: OIDC verifier: %w", err)
	}
	s.pool = worker.New(s.st, worker.Options{
		Workers: cfg.Workers,
		Clock:   s.now,
		Logger:  s.log.With("component", "worker"),
		Metrics: s.metrics,
	})
	runOpts := []runs.Option{runs.WithEnqueue(s.pool.Enqueue)}
	if cfg.ArtifactBucket != "" {
		var storeOpts []artifacts.Option
		if cfg.ArtifactEndpoint != "" {
			storeOpts = append(storeOpts, artifacts.WithEndpoint(cfg.ArtifactEndpoint))
		}
		a, err := artifacts.NewS3Store(ctx, cfg.ArtifactBucket, cfg.ArtifactPrefix, storeOpts...)
		if err != nil {
			return fmt.Errorf("server: artifact bucket %s: %w", cfg.ArtifactBucket, err)
		}
		runOpts = append(runOpts, runs.WithArtifactStore(a))
	}
	s.runs = runs.New(s.st, app, runs.Config{BaseURL: cfg.BaseURL, Clock: s.now}, s.log.With("component", "runs"), s.metrics, runOpts...)
	s.register(s.runs)
	s.sched = sched.New(s.st, s.pool, sched.Options{
		Clock:   s.now,
		Logger:  s.log.With("component", "sched"),
		Metrics: s.metrics,
	})

	deps.Runs = s.runs
	deps.Verifier = verifier
	deps.GitHub = app
	deps.UI = ui.Handler()
	deps.Metrics = s.metrics.Handler()
	deps.Webhook = webhook.New(cfg.WebhookSecret, s.st, s.pool.Notify, s.metrics, s.log.With("component", "webhook"))
	return nil
}

func (s *Server) apiConfig() api.Config {
	return api.Config{
		BaseURL:             s.cfg.BaseURL,
		SessionKey:          s.cfg.SessionKey,
		OAuthClientID:       s.cfg.OAuthClientID,
		OAuthClientSecret:   s.cfg.OAuthClientSecret,
		GitHubWebURL:        s.cfg.GitHubWebURL,
		GitHubAPIURL:        s.cfg.GitHubAPIURL,
		SetupMode:           s.cfg.SetupMode,
		OIDCAudience:        s.cfg.OIDCAudience,
		RequiredWorkflowRef: s.cfg.RequiredWorkflowRef,
		MetricsToken:        s.cfg.MetricsToken,
	}
}

// Handler returns the server's root HTTP handler, for in-process tests
// that do not go through the listener.
func (s *Server) Handler() http.Handler { return s.handler }

// Addr returns the address the server accepts connections on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Config returns the configuration the server runs with, defaults filled
// in.
func (s *Server) Config() Config { return s.cfg }

// Run serves until ctx ends, then shuts down: the listener stops
// accepting and in-flight requests get up to ShutdownTimeout to finish,
// then the scheduler stops and the worker pool drains its handlers, and
// finally the store is closed. Outside setup mode it first enqueues an
// installation sync and starts the worker pool and the scheduler. It
// returns nil after a clean shutdown, the listener's error when serving
// fails, and ErrRunning when called twice.
func (s *Server) Run(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return ErrRunning
	}
	background, stopBackground := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBackground()
	var wg sync.WaitGroup
	var poolErr error
	if s.pool != nil {
		s.enqueueStartupSync(ctx)
		wg.Go(func() { poolErr = s.pool.Run(background) })
		wg.Go(func() { _ = s.sched.Run(background) })
	}
	served := make(chan error, 1)
	go func() { served <- s.http.Serve(s.ln) }()
	s.log.InfoContext(ctx, "stackorder server started", "addr", s.Addr(), "base_url", s.cfg.BaseURL,
		"setup_mode", s.cfg.SetupMode, "version", version.Version, "commit", version.Commit)

	var err error
	select {
	case <-ctx.Done():
		s.log.Info("shutting down", "timeout", ShutdownTimeout)
	case serveErr := <-served:
		served = nil
		err = fmt.Errorf("server: serve: %w", serveErr)
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()
	if shutdownErr := s.http.Shutdown(shutdownCtx); shutdownErr != nil {
		s.log.Warn("in-flight requests did not finish in time; closing their connections", "error", shutdownErr)
		_ = s.http.Close()
	}
	if served != nil {
		if serveErr := <-served; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			err = errors.Join(err, fmt.Errorf("server: serve: %w", serveErr))
		}
	}
	stopBackground()
	wg.Wait()
	if poolErr != nil && !errors.Is(poolErr, worker.ErrDrainTimeout) {
		err = errors.Join(err, fmt.Errorf("server: worker pool: %w", poolErr))
	}
	if closeErr := s.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	s.log.Info("stackorder server stopped")
	return err
}

func (s *Server) enqueueStartupSync(ctx context.Context) {
	key := fmt.Sprintf("%s:start:%d", runs.JobSyncInstallations, s.now().UTC().Truncate(time.Minute).Unix())
	if err := s.pool.Enqueue(ctx, runs.JobSyncInstallations, runs.SyncInstallationsJob{}, time.Time{}, key); err != nil {
		s.log.WarnContext(ctx, "could not enqueue the start-up installation sync; the daily sync will catch up", "error", err)
	}
}

// Close releases what New opened: the listener, the store and the trace
// exporter, flushing pending spans. Run calls it on return; call it
// directly only for a Server that is never run. It is safe to call more
// than once.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		var errs []error
		if s.ln != nil {
			if err := s.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, fmt.Errorf("server: close listener: %w", err))
			}
		}
		if s.st != nil {
			s.st.Close()
		}
		if s.tracing != nil {
			ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
			defer cancel()
			if err := s.tracing.shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}
