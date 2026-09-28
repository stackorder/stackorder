package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/config"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

// Defaults applied by New to zero Config fields.
const (
	DefaultWorkflowFile           = "stackorder-run.yml"
	DefaultCommentRateLimit       = 10
	DefaultTeamCacheTTL           = 60 * time.Second
	DefaultUnboundDispatchTimeout = 30 * time.Minute
)

// Job kinds the service enqueues and executes.
const (
	JobDispatchWave  = "dispatch_wave"
	JobDrift         = "drift_stack"
	JobScheduleDrift = "schedule_drift"
	JobCrossRepoPlan = "cross_repo_plan"
	JobReconcile     = "reconcile"
	JobPrune         = "prune"
	JobStaleLocks    = "stale_locks"
)

const (
	pullHeadTTL       = 30 * time.Second
	unboundGrace      = 60 * time.Second
	staleLockAge      = 24 * time.Hour
	commentWindow     = time.Minute
	artifactTextLimit = 8 << 10
	driftLabel        = "stackorder-drift"
	schedulerActor    = "stackorder"
)

// Config configures a Service. Zero fields take the Default values.
type Config struct {
	// BaseURL is the server's public URL, used for links in checks and
	// comments.
	BaseURL string
	// WorkflowFile is the workflow the server dispatches.
	WorkflowFile string
	// CommentRateLimit is how many comment commands one pull request may
	// issue per minute.
	CommentRateLimit int
	// TeamCacheTTL is how long team memberships and repository
	// permissions are cached.
	TeamCacheTTL time.Duration
	// UnboundDispatchTimeout is how long a dispatch may stay without a
	// workflow run before its stacks are marked unknown.
	UnboundDispatchTimeout time.Duration
	// Clock returns the current time; time.Now when nil.
	Clock func() time.Time
}

// GitHub hands out installation clients. *gh.App satisfies it.
type GitHub interface {
	Client(ctx context.Context, installationID int64) (*gh.Client, error)
}

// Metrics receives the service's observations. Implementations must be
// safe for concurrent use.
type Metrics interface {
	RunStatusChanged(status v1.RunStatus, trigger v1.Trigger, mode v1.RunMode)
	StackFinished(mode v1.RunMode, status v1.StackStatus, d time.Duration)
	Dispatched(mode v1.RunMode, ok bool)
	SetDriftedStacks(n int)
	SetLocksHeld(n int)
	CommandReceived(verb string, accepted bool)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

// RunStatusChanged implements Metrics.
func (NopMetrics) RunStatusChanged(v1.RunStatus, v1.Trigger, v1.RunMode) {}

// StackFinished implements Metrics.
func (NopMetrics) StackFinished(v1.RunMode, v1.StackStatus, time.Duration) {}

// Dispatched implements Metrics.
func (NopMetrics) Dispatched(v1.RunMode, bool) {}

// SetDriftedStacks implements Metrics.
func (NopMetrics) SetDriftedStacks(int) {}

// SetLocksHeld implements Metrics.
func (NopMetrics) SetLocksHeld(int) {}

// CommandReceived implements Metrics.
func (NopMetrics) CommandReceived(string, bool) {}

// EnqueueFunc queues a job of kind with a JSON encodable payload, due at
// runAfter (now when zero). A non-empty dedupeKey makes a second enqueue
// with the same key a no-op.
type EnqueueFunc func(ctx context.Context, kind string, payload any, runAfter time.Time, dedupeKey string) error

// StoreEnqueue returns an EnqueueFunc that writes jobs to the store's
// queue.
func StoreEnqueue(st *store.Store) EnqueueFunc {
	return func(ctx context.Context, kind string, payload any, runAfter time.Time, dedupeKey string) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("runs: encode %s job: %w", kind, err)
		}
		if _, _, err := st.EnqueueJob(ctx, kind, b, runAfter, dedupeKey); err != nil {
			return fmt.Errorf("runs: enqueue %s job: %w", kind, err)
		}
		return nil
	}
}

// ArtifactStore keeps full plan text and plan JSON outside Postgres, such
// as in the optional S3 artifact bucket.
type ArtifactStore interface {
	// Put stores body under key and returns a URL where it can be read.
	Put(ctx context.Context, key string, contentType string, body []byte) (url string, err error)
}

// Option changes an optional part of a Service.
type Option func(*Service)

// WithEnqueue makes the service queue follow-up work, such as the next
// wave's dispatch, instead of doing it inline.
func WithEnqueue(fn EnqueueFunc) Option { return func(s *Service) { s.enqueue = fn } }

// WithArtifactStore makes RecordResult keep full plan text in a, storing
// only its beginning and a link in Postgres.
func WithArtifactStore(a ArtifactStore) Option { return func(s *Service) { s.artifacts = a } }

// Service is the run state machine of the server. It is safe for
// concurrent use; see the package documentation for how concurrent
// workers and servers are kept consistent.
type Service struct {
	st        *store.Store
	gh        GitHub
	cfg       Config
	log       *slog.Logger
	m         Metrics
	enqueue   EnqueueFunc
	artifacts ArtifactStore
	cache     *ttlCache
}

// New returns a Service. A nil logger discards logs and nil metrics are
// NopMetrics.
func New(st *store.Store, gh GitHub, cfg Config, logger *slog.Logger, m Metrics, opts ...Option) *Service {
	if cfg.WorkflowFile == "" {
		cfg.WorkflowFile = DefaultWorkflowFile
	}
	if cfg.CommentRateLimit <= 0 {
		cfg.CommentRateLimit = DefaultCommentRateLimit
	}
	if cfg.TeamCacheTTL <= 0 {
		cfg.TeamCacheTTL = DefaultTeamCacheTTL
	}
	if cfg.UnboundDispatchTimeout <= 0 {
		cfg.UnboundDispatchTimeout = DefaultUnboundDispatchTimeout
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if m == nil {
		m = NopMetrics{}
	}
	s := &Service{st: st, gh: gh, cfg: cfg, log: logger, m: m, cache: newTTLCache()}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Service) now() time.Time { return s.cfg.Clock().UTC() }

func (s *Service) client(ctx context.Context, repo store.Repo) (*gh.Client, error) {
	c, err := s.gh.Client(ctx, repo.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("runs: github client for %s: %w", repo.FullName, err)
	}
	return c, nil
}

func (s *Service) later(ctx context.Context, kind string, payload any, dedupeKey string, inline func(context.Context) error) error {
	if s.enqueue == nil {
		return inline(ctx)
	}
	return s.enqueue(ctx, kind, payload, time.Time{}, dedupeKey)
}

func (s *Service) runURL(id uuid.UUID) string {
	if s.cfg.BaseURL == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.BaseURL, "/") + "/runs/" + id.String()
}

func repoConfig(repo store.Repo) *v1.RepoConfig {
	if repo.Config == nil {
		return config.Default()
	}
	c := *repo.Config
	config.ApplyDefaults(&c)
	return &c
}

func parseRunID(id string) (uuid.UUID, error) {
	u, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return uuid.Nil, &principal.InvalidError{Field: "run_id", Reason: fmt.Sprintf("%q is not a run id", id)}
	}
	return u, nil
}

func (s *Service) loadRun(ctx context.Context, id string) (store.Run, store.Repo, error) {
	u, err := parseRunID(id)
	if err != nil {
		return store.Run{}, store.Repo{}, err
	}
	run, err := s.st.GetRun(ctx, u)
	if err != nil {
		return store.Run{}, store.Repo{}, storeErr(err, "run %s", id)
	}
	repo, err := s.st.GetRepo(ctx, run.RepoID)
	if err != nil {
		return store.Run{}, store.Repo{}, storeErr(err, "repository of run %s", id)
	}
	return run, repo, nil
}

func storeErr(err error, format string, args ...any) error {
	what := fmt.Sprintf(format, args...)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("%w: %s: %w", principal.ErrNotFound, what, err)
	case errors.Is(err, store.ErrConflict):
		return fmt.Errorf("%w: %s: %w", principal.ErrConflict, what, err)
	case errors.Is(err, store.ErrInvalid):
		return fmt.Errorf("%w: %s: %w", principal.ErrInvalid, what, err)
	}
	return fmt.Errorf("runs: %s: %w", what, err)
}

func permanentGitHubError(err error) bool {
	var apiErr *gh.APIError
	if !errors.As(err, &apiErr) || apiErr.RateLimited() {
		return false
	}
	return apiErr.Status >= http.StatusBadRequest && apiErr.Status < http.StatusInternalServerError
}

func (s *Service) refreshLocksGauge(ctx context.Context) {
	locks, err := s.st.ListLocks(ctx, 0)
	if err != nil {
		s.log.WarnContext(ctx, "count locks", "error", err)
		return
	}
	s.m.SetLocksHeld(len(locks))
}

func (s *Service) refreshDriftGauge(ctx context.Context) {
	o, err := s.st.Overview(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "count drifted stacks", "error", err)
		return
	}
	s.m.SetDriftedStacks(o.Drifted)
}

func (s *Service) audit(ctx context.Context, actor, action, target string, details map[string]any) {
	if actor == "" {
		actor = schedulerActor
	}
	if _, err := s.st.RecordAudit(ctx, store.AuditEntry{Actor: actor, Action: action, Target: target, Details: details}); err != nil {
		s.log.WarnContext(ctx, "record audit", "action", action, "target", target, "error", err)
	}
}

type ttlCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value string
	at    time.Time
}

const maxCacheEntries = 4096

func newTTLCache() *ttlCache { return &ttlCache{entries: map[string]cacheEntry{}} }

func (c *ttlCache) get(key string, ttl time.Duration, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.Sub(e.at) >= ttl || now.Before(e.at) {
		return "", false
	}
	return e.value, true
}

func (c *ttlCache) put(key, value string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		clear(c.entries)
	}
	c.entries[key] = cacheEntry{value: value, at: now}
}

func (s *Service) pullHead(ctx context.Context, c *gh.Client, repo store.Repo, number int, fresh bool) (string, error) {
	key := "head:" + strconv.FormatInt(repo.ID, 10) + "#" + strconv.Itoa(number)
	if !fresh {
		if v, ok := s.cache.get(key, pullHeadTTL, s.now()); ok {
			return v, nil
		}
	}
	pr, err := c.GetPull(ctx, repo.FullName, number)
	if err != nil {
		return "", fmt.Errorf("runs: pull request %s#%d: %w", repo.FullName, number, err)
	}
	s.cache.put(key, pr.HeadSHA, s.now())
	return pr.HeadSHA, nil
}

func (s *Service) permission(ctx context.Context, c *gh.Client, repo store.Repo, login string) (string, error) {
	key := "perm:" + strconv.FormatInt(repo.ID, 10) + ":" + strings.ToLower(login)
	if v, ok := s.cache.get(key, s.cfg.TeamCacheTTL, s.now()); ok {
		return v, nil
	}
	perm, err := c.CollaboratorPermission(ctx, repo.FullName, login)
	if err != nil {
		return "", fmt.Errorf("runs: permission of %s on %s: %w", login, repo.FullName, err)
	}
	s.cache.put(key, perm, s.now())
	return perm, nil
}

func (s *Service) teamMember(ctx context.Context, c *gh.Client, org, team, login string) (bool, error) {
	key := "team:" + strings.ToLower(org+"/"+team+":"+login)
	if v, ok := s.cache.get(key, s.cfg.TeamCacheTTL, s.now()); ok {
		return v == gh.MembershipActive, nil
	}
	state, err := c.TeamMembership(ctx, org, team, login)
	if err != nil {
		return false, fmt.Errorf("runs: membership of %s in %s/%s: %w", login, org, team, err)
	}
	s.cache.put(key, state, s.now())
	return state == gh.MembershipActive, nil
}
