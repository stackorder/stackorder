package server

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
)

// Defaults LoadConfig and New apply to unset values.
const (
	DefaultListen            = ":8080"
	DefaultWorkers           = 4
	DefaultPlanTextRetention = 720 * time.Hour
	DefaultEventRetention    = 168 * time.Hour
	DefaultDriftRetention    = 2160 * time.Hour
	DefaultLogFormat         = LogFormatJSON
	// PoolConnsBeyondWorkers is how many pgx pool connections the server
	// keeps on top of one per worker, for the API, the webhook receiver,
	// the scheduler and its leader lock.
	PoolConnsBeyondWorkers = 8
)

// Log formats accepted in STACKORDER_LOG_FORMAT.
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// SessionKeySize is the length in bytes of STACKORDER_SESSION_KEY.
const SessionKeySize = 32

// SetupTokenSize is the number of random bytes in a generated setup token,
// which is their unpadded base64url encoding.
const SetupTokenSize = 32

// MinSetupTokenLength is the fewest characters STACKORDER_SETUP_TOKEN may
// have.
const MinSetupTokenLength = 32

var setupTokenChars = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// Environment variables LoadConfig reads.
const (
	EnvBaseURL             = "STACKORDER_BASE_URL"
	EnvListen              = "STACKORDER_LISTEN"
	EnvDatabaseURL         = "DATABASE_URL"
	EnvAppID               = "GITHUB_APP_ID"
	EnvAppPrivateKey       = "GITHUB_APP_PRIVATE_KEY"
	EnvWebhookSecret       = "GITHUB_WEBHOOK_SECRET" //nolint:gosec // a variable name, not a secret
	EnvOAuthClientID       = "GITHUB_OAUTH_CLIENT_ID"
	EnvOAuthClientSecret   = "GITHUB_OAUTH_CLIENT_SECRET" //nolint:gosec // a variable name, not a secret
	EnvGitHubAPIURL        = "GITHUB_API_URL"
	EnvGitHubWebURL        = "GITHUB_WEB_URL"
	EnvOIDCIssuer          = "GITHUB_OIDC_ISSUER"
	EnvOIDCJWKSURL         = "GITHUB_OIDC_JWKS_URL"
	EnvOIDCAudience        = "STACKORDER_OIDC_AUDIENCE"
	EnvRequiredWorkflowRef = "STACKORDER_REQUIRED_WORKFLOW_REF"
	EnvArtifactBucket      = "STACKORDER_ARTIFACT_BUCKET"
	EnvArtifactPrefix      = "STACKORDER_ARTIFACT_PREFIX"
	EnvArtifactEndpoint    = "AWS_ENDPOINT_URL_S3"
	EnvSessionKey          = "STACKORDER_SESSION_KEY"
	EnvMetricsToken        = "STACKORDER_METRICS_TOKEN"
	EnvPlanTextRetention   = "STACKORDER_PLAN_TEXT_RETENTION"
	EnvEventRetention      = "STACKORDER_EVENT_RETENTION"
	EnvDriftRetention      = "STACKORDER_DRIFT_RETENTION"
	EnvWorkers             = "STACKORDER_WORKERS"
	EnvAllowResetup        = "STACKORDER_ALLOW_RESETUP"
	EnvSetupToken          = "STACKORDER_SETUP_TOKEN" //nolint:gosec // a variable name, not a secret
	EnvLogLevel            = "STACKORDER_LOG_LEVEL"
	EnvLogFormat           = "STACKORDER_LOG_FORMAT"
	EnvOTLPEndpoint        = "OTEL_EXPORTER_OTLP_ENDPOINT"
)

// Config is the server configuration. LoadConfig reads it from the
// environment; tests may build one directly, and New fills the zero
// fields that have a default.
type Config struct {
	// BaseURL is STACKORDER_BASE_URL, the public URL of the server without
	// a trailing slash. OAuth and manifest callbacks, the OIDC audience
	// default and links derive from it. Required.
	BaseURL string
	// Listen is STACKORDER_LISTEN, the listen address; DefaultListen.
	Listen string
	// DatabaseURL is DATABASE_URL, the Postgres DSN. Required.
	DatabaseURL string

	// AppID, AppPrivateKey and WebhookSecret are GITHUB_APP_ID,
	// GITHUB_APP_PRIVATE_KEY and GITHUB_WEBHOOK_SECRET: all three, or none
	// of them in setup mode.
	AppID         int64
	AppPrivateKey *rsa.PrivateKey
	WebhookSecret []byte
	// OAuthClientID and OAuthClientSecret are GITHUB_OAUTH_CLIENT_ID and
	// GITHUB_OAUTH_CLIENT_SECRET, for human sign-in; both or neither.
	OAuthClientID     string
	OAuthClientSecret string
	// GitHubAPIURL and GitHubWebURL are GITHUB_API_URL and
	// GITHUB_WEB_URL; gh.DefaultBaseURL and gh.DefaultWebURL.
	GitHubAPIURL string
	GitHubWebURL string

	// OIDCIssuer is GITHUB_OIDC_ISSUER; oidc.DefaultIssuer.
	OIDCIssuer string
	// OIDCJWKSURL is GITHUB_OIDC_JWKS_URL; empty derives it from the
	// issuer.
	OIDCJWKSURL string
	// OIDCAudience is STACKORDER_OIDC_AUDIENCE; BaseURL.
	OIDCAudience string
	// RequiredWorkflowRef is STACKORDER_REQUIRED_WORKFLOW_REF, an optional
	// pattern for the job_workflow_ref claim as oidc.MatchWorkflowRef
	// understands it.
	RequiredWorkflowRef string

	// ArtifactBucket and ArtifactPrefix are STACKORDER_ARTIFACT_BUCKET and
	// STACKORDER_ARTIFACT_PREFIX; an empty bucket keeps full plan text out
	// of S3.
	ArtifactBucket string
	ArtifactPrefix string
	// ArtifactEndpoint is AWS_ENDPOINT_URL_S3. When set with a bucket, the
	// artifact store uses it with path-style addressing, which LocalStack
	// and other S3-compatible services need.
	ArtifactEndpoint string

	// SessionKey is STACKORDER_SESSION_KEY decoded from hex,
	// SessionKeySize bytes that sign cookies.
	SessionKey []byte
	// SessionKeyGenerated reports that STACKORDER_SESSION_KEY was unset
	// and SessionKey was generated, so sessions end when the server
	// restarts; New logs a warning.
	SessionKeyGenerated bool
	// MetricsToken is STACKORDER_METRICS_TOKEN; when set, GET /metrics
	// requires it as a bearer token.
	MetricsToken string

	// PlanTextRetention, EventRetention and DriftRetention are
	// STACKORDER_PLAN_TEXT_RETENTION, STACKORDER_EVENT_RETENTION and
	// STACKORDER_DRIFT_RETENTION, applied by the hourly prune job.
	PlanTextRetention time.Duration
	EventRetention    time.Duration
	DriftRetention    time.Duration
	// Workers is STACKORDER_WORKERS, the worker goroutines; DefaultWorkers.
	Workers int

	// LogLevel and LogFormat are STACKORDER_LOG_LEVEL and
	// STACKORDER_LOG_FORMAT; info and DefaultLogFormat.
	LogLevel  slog.Level
	LogFormat string
	// OTLPEndpoint is OTEL_EXPORTER_OTLP_ENDPOINT, the OTLP HTTP base URL
	// traces are exported to; empty disables tracing.
	OTLPEndpoint string

	// SetupMode is set when none of the GitHub App variables is: the
	// server then migrates the database and serves only /setup*, /healthz
	// and /readyz.
	SetupMode bool
	// AllowResetup is STACKORDER_ALLOW_RESETUP, false by default. Only
	// when it is true does a server with App credentials serve
	// /setup?force=1 and /setup/callback to create another App; otherwise
	// they answer 404.
	AllowResetup bool
	// SetupToken is STACKORDER_SETUP_TOKEN, the one-time bootstrap token
	// GET /setup requires to create an App: at least MinSetupTokenLength
	// URL-safe characters. When it is empty and /setup can create an App,
	// in setup mode or with AllowResetup, New generates one from
	// SetupTokenSize random bytes and Run logs the setup URL carrying it.
	SetupToken string
	// SetupTokenGenerated reports that New generated SetupToken.
	SetupTokenGenerated bool
}

// LoadConfig reads the configuration from getenv, normally os.Getenv.
// Values are trimmed of surrounding white space and empty ones count as
// unset. Every invalid or missing variable is reported, each error naming
// its variable, and a random session key is generated when
// STACKORDER_SESSION_KEY is unset. The pgx pool the server opens on
// DATABASE_URL has STACKORDER_WORKERS + PoolConnsBeyondWorkers connections
// (Config.PoolMaxConns) unless the DSN sets pool_max_conns, which then
// wins; the scheduler's leader lock holds one of them for as long as the
// server leads.
func LoadConfig(getenv func(string) string) (Config, error) {
	e := &env{get: getenv}
	cfg := Config{
		BaseURL:             e.absoluteURL(EnvBaseURL, ""),
		Listen:              e.listen(EnvListen),
		DatabaseURL:         e.databaseURL(EnvDatabaseURL),
		OAuthClientID:       e.str(EnvOAuthClientID),
		OAuthClientSecret:   e.str(EnvOAuthClientSecret),
		GitHubAPIURL:        e.absoluteURL(EnvGitHubAPIURL, gh.DefaultBaseURL),
		GitHubWebURL:        e.absoluteURL(EnvGitHubWebURL, gh.DefaultWebURL),
		OIDCIssuer:          e.absoluteURL(EnvOIDCIssuer, oidc.DefaultIssuer),
		OIDCJWKSURL:         e.absoluteURL(EnvOIDCJWKSURL, ""),
		OIDCAudience:        e.str(EnvOIDCAudience),
		RequiredWorkflowRef: e.workflowRef(EnvRequiredWorkflowRef),
		ArtifactBucket:      e.bucket(EnvArtifactBucket),
		ArtifactPrefix:      e.str(EnvArtifactPrefix),
		MetricsToken:        e.str(EnvMetricsToken),
		PlanTextRetention:   e.duration(EnvPlanTextRetention, DefaultPlanTextRetention),
		EventRetention:      e.duration(EnvEventRetention, DefaultEventRetention),
		DriftRetention:      e.duration(EnvDriftRetention, DefaultDriftRetention),
		Workers:             e.positiveInt(EnvWorkers, DefaultWorkers),
		AllowResetup:        e.boolean(EnvAllowResetup),
		SetupToken:          e.setupToken(EnvSetupToken),
		LogLevel:            e.logLevel(EnvLogLevel),
		LogFormat:           e.logFormat(EnvLogFormat),
		OTLPEndpoint:        e.absoluteURL(EnvOTLPEndpoint, ""),
	}
	if cfg.BaseURL == "" && e.str(EnvBaseURL) == "" {
		e.fail(EnvBaseURL, "is required: the public URL of the server, such as https://stackorder.example.com")
	}
	if cfg.OIDCAudience == "" {
		cfg.OIDCAudience = cfg.BaseURL
	}
	e.app(&cfg)
	e.oauth(&cfg)
	if cfg.ArtifactBucket != "" {
		cfg.ArtifactEndpoint = e.absoluteURL(EnvArtifactEndpoint, "")
	} else if cfg.ArtifactPrefix != "" {
		e.fail(EnvArtifactPrefix, "is set without %s", EnvArtifactBucket)
	}
	cfg.SessionKey = e.hexKey(EnvSessionKey, SessionKeySize)
	if cfg.SessionKey == nil && e.str(EnvSessionKey) == "" {
		key, err := generateSessionKey()
		if err != nil {
			e.fail(EnvSessionKey, "is unset and a key could not be generated: %v", err)
		}
		cfg.SessionKey, cfg.SessionKeyGenerated = key, true
	}
	if err := errors.Join(e.errs...); err != nil {
		return Config{}, fmt.Errorf("server: invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// PoolMaxConns is the size of the database pool the server asks for when
// DATABASE_URL does not set pool_max_conns: one connection per worker plus
// PoolConnsBeyondWorkers. Workers below one count as DefaultWorkers.
func (c Config) PoolMaxConns() int32 {
	workers := c.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	return int32(min(workers, 1<<20) + PoolConnsBeyondWorkers) //nolint:gosec
}

func generateSessionKey() ([]byte, error) {
	key := make([]byte, SessionKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	return key, nil
}

func generateSetupToken() (string, error) {
	b := make([]byte, SetupTokenSize)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate setup token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func checkSetupToken(v string) error {
	if len(v) < MinSetupTokenLength || !setupTokenChars.MatchString(v) {
		return fmt.Errorf("must be at least %d letters, digits, dots, dashes, underscores or tildes, such as the output of `openssl rand -hex 32`", MinSetupTokenLength)
	}
	return nil
}

// servesSetup reports whether /setup can create an App, which then needs
// a setup token.
func (c *Config) servesSetup() bool {
	return c.SetupMode || c.AllowResetup
}

func (c *Config) withSetupToken() error {
	if !c.servesSetup() || c.SetupToken != "" {
		return nil
	}
	token, err := generateSetupToken()
	if err != nil {
		return err
	}
	c.SetupToken, c.SetupTokenGenerated = token, true
	return nil
}

func (c *Config) withDefaults() {
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.GitHubAPIURL == "" {
		c.GitHubAPIURL = gh.DefaultBaseURL
	}
	if c.GitHubWebURL == "" {
		c.GitHubWebURL = gh.DefaultWebURL
	}
	if c.OIDCIssuer == "" {
		c.OIDCIssuer = oidc.DefaultIssuer
	}
	if c.OIDCAudience == "" {
		c.OIDCAudience = c.BaseURL
	}
	if c.PlanTextRetention <= 0 {
		c.PlanTextRetention = DefaultPlanTextRetention
	}
	if c.EventRetention <= 0 {
		c.EventRetention = DefaultEventRetention
	}
	if c.DriftRetention <= 0 {
		c.DriftRetention = DefaultDriftRetention
	}
	if c.Workers <= 0 {
		c.Workers = DefaultWorkers
	}
	if c.LogFormat == "" {
		c.LogFormat = DefaultLogFormat
	}
}

func (c *Config) validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, fmt.Errorf("%s is required", EnvDatabaseURL))
	}
	if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("%s must be an absolute http or https URL, got %q", EnvBaseURL, c.BaseURL))
	}
	if !c.SetupMode {
		if c.AppID <= 0 {
			errs = append(errs, fmt.Errorf("%s is required outside setup mode", EnvAppID))
		}
		if c.AppPrivateKey == nil {
			errs = append(errs, fmt.Errorf("%s is required outside setup mode", EnvAppPrivateKey))
		}
		if len(c.WebhookSecret) == 0 {
			errs = append(errs, fmt.Errorf("%s is required outside setup mode", EnvWebhookSecret))
		}
	}
	if c.RequiredWorkflowRef != "" {
		if err := oidc.ValidateWorkflowRefPattern(c.RequiredWorkflowRef); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvRequiredWorkflowRef, err))
		}
	}
	if c.SetupToken != "" {
		if err := checkSetupToken(c.SetupToken); err != nil {
			errs = append(errs, fmt.Errorf("%s %w", EnvSetupToken, err))
		}
	}
	if len(c.SessionKey) != 0 && len(c.SessionKey) != SessionKeySize {
		errs = append(errs, fmt.Errorf("%s must be %d bytes, got %d", EnvSessionKey, SessionKeySize, len(c.SessionKey)))
	}
	if c.LogFormat != LogFormatJSON && c.LogFormat != LogFormatText {
		errs = append(errs, fmt.Errorf("%s must be %q or %q, got %q", EnvLogFormat, LogFormatJSON, LogFormatText, c.LogFormat))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("server: invalid configuration:\n%w", err)
	}
	return nil
}

type env struct {
	get  func(string) string
	errs []error
}

func (e *env) str(name string) string {
	return strings.TrimSpace(e.get(name))
}

func (e *env) fail(name, format string, args ...any) {
	e.errs = append(e.errs, fmt.Errorf("%s %s", name, fmt.Sprintf(format, args...)))
}

func (e *env) absoluteURL(name, def string) string {
	v := e.str(name)
	if v == "" {
		return def
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		e.fail(name, "must be an absolute http or https URL, got %q", v)
		return ""
	}
	if u.RawQuery != "" || u.Fragment != "" {
		e.fail(name, "must not carry a query or fragment, got %q", v)
		return ""
	}
	return strings.TrimRight(v, "/")
}

func (e *env) listen(name string) string {
	v := e.str(name)
	if v == "" {
		return DefaultListen
	}
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		e.fail(name, "must be host:port or :port, got %q", v)
		return ""
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || (n == 0 && port != "0") {
		e.fail(name, "has an invalid port %q", port)
		return ""
	}
	return v
}

func (e *env) databaseURL(name string) string {
	v := e.str(name)
	if v == "" {
		e.fail(name, "is required: the Postgres connection string")
		return ""
	}
	if _, err := pgxpool.ParseConfig(v); err != nil {
		e.fail(name, "is not a valid Postgres connection string: %v", err)
		return ""
	}
	return v
}

func (e *env) workflowRef(name string) string {
	v := e.str(name)
	if v == "" {
		return ""
	}
	if err := oidc.ValidateWorkflowRefPattern(v); err != nil {
		e.fail(name, "is invalid: %v", err)
		return ""
	}
	return v
}

func (e *env) bucket(name string) string {
	v := e.str(name)
	if strings.Contains(v, "/") || strings.ContainsAny(v, " \t") || strings.HasPrefix(strings.ToLower(v), "s3:") {
		e.fail(name, "must be a bucket name, not a URL or path, got %q", v)
		return ""
	}
	return v
}

func (e *env) duration(name string, def time.Duration) time.Duration {
	v := e.str(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.fail(name, "must be a positive Go duration such as 720h, got %q", v)
		return 0
	}
	return d
}

func (e *env) positiveInt(name string, def int) int {
	v := e.str(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		e.fail(name, "must be a positive integer, got %q", v)
		return 0
	}
	return n
}

func (e *env) boolean(name string) bool {
	v := e.str(name)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(name, "must be true or false, got %q", v)
		return false
	}
	return b
}

func (e *env) hexKey(name string, size int) []byte {
	v := e.str(name)
	if v == "" {
		return nil
	}
	key, err := hex.DecodeString(v)
	if err != nil {
		e.fail(name, "must be hex encoded, such as the output of `openssl rand -hex %d`", size)
		return nil
	}
	if len(key) != size {
		e.fail(name, "must decode to %d bytes (%d hex characters), got %d bytes", size, 2*size, len(key))
		return nil
	}
	return key
}

func (e *env) setupToken(name string) string {
	v := e.str(name)
	if v == "" {
		return ""
	}
	if err := checkSetupToken(v); err != nil {
		e.fail(name, "%v", err)
		return ""
	}
	return v
}

func (e *env) logLevel(name string) slog.Level {
	v := e.str(name)
	if v == "" {
		return slog.LevelInfo
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(v)); err != nil {
		e.fail(name, "must be debug, info, warn or error, got %q", v)
		return slog.LevelInfo
	}
	return level
}

func (e *env) logFormat(name string) string {
	v := strings.ToLower(e.str(name))
	switch v {
	case "":
		return DefaultLogFormat
	case LogFormatJSON, LogFormatText:
		return v
	}
	e.fail(name, "must be %q or %q, got %q", LogFormatJSON, LogFormatText, v)
	return DefaultLogFormat
}

func (e *env) app(cfg *Config) {
	id, key, secret := e.str(EnvAppID), e.str(EnvAppPrivateKey), e.str(EnvWebhookSecret)
	if id == "" && key == "" && secret == "" {
		cfg.SetupMode = true
		return
	}
	for _, v := range []struct{ name, value string }{{EnvAppID, id}, {EnvAppPrivateKey, key}, {EnvWebhookSecret, secret}} {
		if v.value == "" {
			e.fail(v.name, "is required when any of %s, %s and %s is set; set all three, or none to start in setup mode",
				EnvAppID, EnvAppPrivateKey, EnvWebhookSecret)
		}
	}
	if id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 {
			e.fail(EnvAppID, "must be the numeric App id, got %q", id)
		}
		cfg.AppID = n
	}
	if key != "" {
		pk, err := gh.ParsePrivateKey([]byte(key))
		if err != nil {
			e.fail(EnvAppPrivateKey, "is not a usable App private key: %v", err)
		}
		cfg.AppPrivateKey = pk
	}
	if secret != "" {
		cfg.WebhookSecret = []byte(secret)
	}
}

func (e *env) oauth(cfg *Config) {
	switch {
	case cfg.OAuthClientID != "" && cfg.OAuthClientSecret == "":
		e.fail(EnvOAuthClientSecret, "is required when %s is set", EnvOAuthClientID)
	case cfg.OAuthClientID == "" && cfg.OAuthClientSecret != "":
		e.fail(EnvOAuthClientID, "is required when %s is set", EnvOAuthClientSecret)
	}
}
