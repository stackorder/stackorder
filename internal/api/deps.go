package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

// DefaultSessionTTL is how long a session lasts when Config.SessionTTL is
// zero.
const DefaultSessionTTL = 7 * 24 * time.Hour

// Config is the part of the server configuration the API needs.
type Config struct {
	// BaseURL is the public URL of the server, STACKORDER_BASE_URL. It
	// builds the OAuth and manifest callback URLs and the links in
	// responses, makes cookies Secure when it is https, and is the origin
	// state-changing requests from a browser must come from. It must be an
	// absolute URL.
	BaseURL string
	// SessionKey signs the session, OAuth state and setup cookies; 32
	// random bytes. When empty a random key is generated, which signs
	// everyone out whenever the server restarts.
	SessionKey []byte
	// SessionTTL is how long a session lasts; DefaultSessionTTL when zero.
	SessionTTL time.Duration
	// OAuthClientID and OAuthClientSecret are the App's OAuth client
	// credentials for human sign-in; sign-in answers 503 without them.
	OAuthClientID     string
	OAuthClientSecret string
	// GitHubWebURL is the web root used for sign-in, the manifest flow and
	// links; gh.DefaultWebURL when empty.
	GitHubWebURL string
	// GitHubAPIURL is the REST API root used for user lookups and the
	// manifest conversion; gh.DefaultBaseURL when empty.
	GitHubAPIURL string
	// SetupMode serves only /setup*, /healthz and /readyz, for a server
	// started without App credentials.
	SetupMode bool
	// AllowResetup lets a configured server create another App through
	// /setup?force=1 and /setup/callback, STACKORDER_ALLOW_RESETUP. When
	// false both answer 404 outside setup mode.
	AllowResetup bool
	// AppSlug is the App's slug, used for install links. When empty it is
	// read once from Deps.GitHub, if set.
	AppSlug string
	// OIDCAudience is the audience runner tokens carry. The Verifier
	// enforces it; the setup pages show it.
	OIDCAudience string
	// RequiredWorkflowRef, when set, is the pattern the job_workflow_ref
	// claim of every runner token must match, as oidc.MatchWorkflowRef
	// understands it.
	RequiredWorkflowRef string
	// MetricsToken, when set, is the bearer token GET /metrics requires.
	MetricsToken string
}

// RunService is the part of the run state machine the API delegates to.
// *runs.Service implements it. Methods return the sentinel and typed errors
// of internal/principal, which the API maps to HTTP statuses.
type RunService interface {
	// CreateRun finds or creates the run of a runner or automation call.
	CreateRun(ctx context.Context, p principal.Principal, req v1.CreateRunRequest) (*v1.CreateRunResponse, error)
	// UploadGraph stores a scanned graph and resolves the run against it.
	UploadGraph(ctx context.Context, p principal.Principal, runID string, req v1.GraphUploadRequest) (*v1.ResolveResponse, error)
	// RecordResult records one stack's plan, apply or drift outcome.
	RecordResult(ctx context.Context, p principal.Principal, runID, stackKey string, res v1.StackResult) (*v1.RunStack, error)
	// RecordCheck records a named check verdict on one stack of a run.
	RecordCheck(ctx context.Context, p principal.Principal, runID, stackKey, name string, verdict v1.CheckVerdict) (*v1.Check, error)
	// GetRunForPrincipal returns a run with its stacks; for a dispatched
	// job it also binds the job to its dispatch.
	GetRunForPrincipal(ctx context.Context, p principal.Principal, runID string) (*v1.Run, error)
	// Unlock releases the orchestration lock of a stack by id.
	Unlock(ctx context.Context, actor string, stackID string, req v1.UnlockRequest) (*v1.UnlockResponse, error)
	// UnlockByKey releases the orchestration lock of a stack addressed by
	// repository and key.
	UnlockByKey(ctx context.Context, actor string, req v1.UnlockRequest) (*v1.UnlockResponse, error)
	// Rerun starts a run again.
	Rerun(ctx context.Context, actor string, runID string) (*v1.Run, error)
	// CanActOnRepo reports whether a GitHub user has push permission on a
	// repository.
	CanActOnRepo(ctx context.Context, login string, repoID int64) (bool, error)
}

// Deps are the collaborators of the API handler.
type Deps struct {
	// Store is required.
	Store *store.Store
	// Runs is required unless Config.SetupMode is set.
	Runs RunService
	// Verifier checks runner OIDC tokens; runner calls with one answer 503
	// without it.
	Verifier *oidc.Verifier
	// GitHub is the App, used only to look up its slug for install links
	// when Config.AppSlug is empty. Optional.
	GitHub *gh.App
	// HTTPClient sends the OAuth, user and manifest requests to GitHub; a
	// client with a 30 s timeout when nil.
	HTTPClient *http.Client
	// UI serves every path no other route claims; 404 when nil.
	UI http.Handler
	// Metrics serves GET /metrics; 404 when nil.
	Metrics http.Handler
	// Webhook serves POST /webhooks/github; 503 when nil.
	Webhook http.Handler
	// Instrument, when set, wraps the handler of every route with the
	// middleware it returns for that route's pattern.
	Instrument func(route string) func(http.Handler) http.Handler
	// Logger receives access, error and panic logs; slog.Default() when
	// nil.
	Logger *slog.Logger
	// Clock returns the current time; time.Now when nil.
	Clock func() time.Time
}

type dataStore interface {
	Ping(ctx context.Context) error
	SeenJTI(ctx context.Context, jti string, exp time.Time) (bool, error)
	VerifyAPIKey(ctx context.Context, plaintext string) (store.APIKey, error)
	GetSession(ctx context.Context, token string) (store.Session, error)
	CreateSession(ctx context.Context, n store.NewSession) (string, store.Session, error)
	DeleteSession(ctx context.Context, token string) error
	ListInstallations(ctx context.Context) ([]store.Installation, error)

	GetRepo(ctx context.Context, id int64) (store.Repo, error)
	GetRepoByName(ctx context.Context, fullName string) (store.Repo, error)
	Overview(ctx context.Context, accounts ...string) (v1.Overview, error)
	RepoSummaries(ctx context.Context, accounts ...string) ([]v1.RepoSummary, error)

	GetGraph(ctx context.Context, repoID int64, sha string) (*v1.Graph, uuid.UUID, error)
	GetGraphByID(ctx context.Context, id uuid.UUID) (*v1.Graph, error)
	GetDefaultGraph(ctx context.Context, repoID int64) (*v1.Graph, uuid.UUID, error)
	LatestGraph(ctx context.Context, repoID int64) (*v1.Graph, uuid.UUID, error)
	GraphStackIDs(ctx context.Context, graphID uuid.UUID) (map[string]uuid.UUID, error)

	GetRun(ctx context.Context, id uuid.UUID) (store.Run, error)
	ListRuns(ctx context.Context, f store.RunFilter) ([]store.Run, string, error)
	GetRunStacks(ctx context.Context, runID uuid.UUID) ([]store.RunStack, error)

	GetStack(ctx context.Context, id uuid.UUID) (store.Stack, error)
	ListStacks(ctx context.Context, repoID int64, includeRemoved bool) ([]store.Stack, error)
	LatestRunStackForStack(ctx context.Context, stackID uuid.UUID, mode v1.RunMode, statuses ...v1.StackStatus) (store.StackRun, error)
	StackHistory(ctx context.Context, stackID uuid.UUID, limit int, cursor string) ([]store.StackRun, string, error)
	LatestDrift(ctx context.Context, stackID uuid.UUID) (store.Drift, error)
	LatestDriftForRepo(ctx context.Context, repoID int64) ([]store.Drift, error)
	GetLock(ctx context.Context, stackID uuid.UUID) (store.Lock, error)
	ListLocks(ctx context.Context, repoID int64) ([]store.Lock, error)
	StackModules(ctx context.Context, stackID uuid.UUID) ([]store.ModuleConsumer, error)

	GetModule(ctx context.Context, id uuid.UUID) (store.Module, error)
	GetModuleByKey(ctx context.Context, key string) (store.Module, error)
	ListModules(ctx context.Context, f store.ModuleFilter) ([]store.Module, error)
	ListModuleVersions(ctx context.Context, moduleID uuid.UUID) ([]store.ModuleVersion, error)
	ModuleConsumers(ctx context.Context, moduleID uuid.UUID) ([]store.ModuleConsumer, error)

	ListAudit(ctx context.Context, f store.AuditFilter) ([]store.AuditEntry, string, error)
}

var _ dataStore = (*store.Store)(nil)
