package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/oidc"
)

const (
	testDSN     = "postgres://stackorder:secret@db.internal:5432/stackorder?sslmode=require"
	testBaseURL = "https://stackorder.example.com"
)

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

func appKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		testKey = k
	})
	return testKey
}

func appKeyPEM(t *testing.T) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(appKey(t))}))
}

func envOf(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func minimalEnv() map[string]string {
	return map[string]string{EnvDatabaseURL: testDSN, EnvBaseURL: testBaseURL}
}

func appEnv(t *testing.T) map[string]string {
	t.Helper()
	env := minimalEnv()
	env[EnvAppID] = "12345"
	env[EnvAppPrivateKey] = appKeyPEM(t)
	env[EnvWebhookSecret] = "webhook-secret"
	return env
}

func with(env map[string]string, kv ...string) map[string]string {
	out := make(map[string]string, len(env)+len(kv)/2)
	for k, v := range env {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(envOf(minimalEnv()))
	require.NoError(t, err)

	assert.Equal(t, testBaseURL, cfg.BaseURL)
	assert.Equal(t, DefaultListen, cfg.Listen)
	assert.Equal(t, testDSN, cfg.DatabaseURL)
	assert.True(t, cfg.SetupMode, "no GitHub App variables means setup mode")
	assert.Zero(t, cfg.AppID)
	assert.Nil(t, cfg.AppPrivateKey)
	assert.Empty(t, cfg.WebhookSecret)
	assert.Equal(t, gh.DefaultBaseURL, cfg.GitHubAPIURL)
	assert.Equal(t, gh.DefaultWebURL, cfg.GitHubWebURL)
	assert.Equal(t, oidc.DefaultIssuer, cfg.OIDCIssuer)
	assert.Empty(t, cfg.OIDCJWKSURL)
	assert.Equal(t, testBaseURL, cfg.OIDCAudience, "the audience defaults to the base URL")
	assert.Empty(t, cfg.RequiredWorkflowRef)
	assert.Empty(t, cfg.ArtifactBucket)
	assert.Empty(t, cfg.ArtifactPrefix)
	assert.Empty(t, cfg.ArtifactEndpoint)
	assert.Len(t, cfg.SessionKey, SessionKeySize)
	assert.True(t, cfg.SessionKeyGenerated)
	assert.Empty(t, cfg.MetricsToken)
	assert.Equal(t, 720*time.Hour, cfg.PlanTextRetention)
	assert.Equal(t, 168*time.Hour, cfg.EventRetention)
	assert.Equal(t, 2160*time.Hour, cfg.DriftRetention)
	assert.Equal(t, 4, cfg.Workers)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, LogFormatJSON, cfg.LogFormat)
	assert.Empty(t, cfg.OTLPEndpoint)

	again, err := LoadConfig(envOf(minimalEnv()))
	require.NoError(t, err)
	assert.NotEqual(t, cfg.SessionKey, again.SessionKey, "every generated session key is random")
}

func TestLoadConfigEveryVariable(t *testing.T) {
	sessionKey := strings.Repeat("ab", SessionKeySize)
	env := map[string]string{
		EnvBaseURL:             "https://stackorder.example.com/",
		EnvListen:              "0.0.0.0:9090",
		EnvDatabaseURL:         testDSN,
		EnvAppID:               "12345",
		EnvAppPrivateKey:       appKeyPEM(t),
		EnvWebhookSecret:       "webhook-secret",
		EnvOAuthClientID:       "Iv1.client",
		EnvOAuthClientSecret:   "oauth-secret",
		EnvGitHubAPIURL:        "https://github.example.com/api/v3/",
		EnvGitHubWebURL:        "https://github.example.com",
		EnvOIDCIssuer:          "https://github.example.com/_services/token",
		EnvOIDCJWKSURL:         "https://github.example.com/_services/token/.well-known/jwks",
		EnvOIDCAudience:        "sts.stackorder.example.com",
		EnvRequiredWorkflowRef: oidc.DefaultWorkflowRefPattern,
		EnvArtifactBucket:      "acme-stackorder-artifacts",
		EnvArtifactPrefix:      "plans/",
		EnvArtifactEndpoint:    "http://localstack:4566",
		EnvSessionKey:          " " + sessionKey + "\n",
		EnvMetricsToken:        "metrics-token",
		EnvPlanTextRetention:   "240h",
		EnvEventRetention:      "72h",
		EnvDriftRetention:      "1h30m",
		EnvWorkers:             "16",
		EnvLogLevel:            "DEBUG",
		EnvLogFormat:           "Text",
		EnvOTLPEndpoint:        "http://otel-collector:4318/",
	}
	cfg, err := LoadConfig(envOf(env))
	require.NoError(t, err)

	wantKey, err := hex.DecodeString(sessionKey)
	require.NoError(t, err)
	assert.Equal(t, "https://stackorder.example.com", cfg.BaseURL, "the trailing slash is dropped")
	assert.Equal(t, "0.0.0.0:9090", cfg.Listen)
	assert.Equal(t, testDSN, cfg.DatabaseURL)
	assert.False(t, cfg.SetupMode)
	assert.Equal(t, int64(12345), cfg.AppID)
	require.NotNil(t, cfg.AppPrivateKey)
	assert.True(t, appKey(t).Equal(cfg.AppPrivateKey))
	assert.Equal(t, []byte("webhook-secret"), cfg.WebhookSecret)
	assert.Equal(t, "Iv1.client", cfg.OAuthClientID)
	assert.Equal(t, "oauth-secret", cfg.OAuthClientSecret)
	assert.Equal(t, "https://github.example.com/api/v3", cfg.GitHubAPIURL)
	assert.Equal(t, "https://github.example.com", cfg.GitHubWebURL)
	assert.Equal(t, "https://github.example.com/_services/token", cfg.OIDCIssuer)
	assert.Equal(t, "https://github.example.com/_services/token/.well-known/jwks", cfg.OIDCJWKSURL)
	assert.Equal(t, "sts.stackorder.example.com", cfg.OIDCAudience)
	assert.Equal(t, oidc.DefaultWorkflowRefPattern, cfg.RequiredWorkflowRef)
	assert.Equal(t, "acme-stackorder-artifacts", cfg.ArtifactBucket)
	assert.Equal(t, "plans/", cfg.ArtifactPrefix)
	assert.Equal(t, "http://localstack:4566", cfg.ArtifactEndpoint)
	assert.Equal(t, wantKey, cfg.SessionKey, "surrounding white space is trimmed")
	assert.False(t, cfg.SessionKeyGenerated)
	assert.Equal(t, "metrics-token", cfg.MetricsToken)
	assert.Equal(t, 240*time.Hour, cfg.PlanTextRetention)
	assert.Equal(t, 72*time.Hour, cfg.EventRetention)
	assert.Equal(t, 90*time.Minute, cfg.DriftRetention)
	assert.Equal(t, 16, cfg.Workers)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
	assert.Equal(t, LogFormatText, cfg.LogFormat)
	assert.Equal(t, "http://otel-collector:4318", cfg.OTLPEndpoint)
}

func TestLoadConfigPrivateKeySpellings(t *testing.T) {
	pemKey := appKeyPEM(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(appKey(t))
	require.NoError(t, err)
	spellings := map[string]string{
		"pkcs1 pem":        pemKey,
		"pkcs8 pem":        string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		"escaped newlines": strings.ReplaceAll(strings.TrimSpace(pemKey), "\n", `\n`),
		"base64 of pem":    base64.StdEncoding.EncodeToString([]byte(pemKey)),
	}
	for name, key := range spellings {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadConfig(envOf(with(appEnv(t), EnvAppPrivateKey, key)))
			require.NoError(t, err)
			assert.True(t, appKey(t).Equal(cfg.AppPrivateKey))
		})
	}
}

func TestLoadConfigSetupModeDetection(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		setupMode bool
		missing   []string
	}{
		{"no app variables", minimalEnv(), true, nil},
		{"blank app variables", with(minimalEnv(), EnvAppID, " ", EnvAppPrivateKey, "\n", EnvWebhookSecret, ""), true, nil},
		{"all three", appEnv(t), false, nil},
		{"only the id", with(minimalEnv(), EnvAppID, "1"), false, []string{EnvAppPrivateKey, EnvWebhookSecret}},
		{"no secret", with(appEnv(t), EnvWebhookSecret, ""), false, []string{EnvWebhookSecret}},
		{"no key", with(appEnv(t), EnvAppPrivateKey, ""), false, []string{EnvAppPrivateKey}},
		{"no id", with(appEnv(t), EnvAppID, ""), false, []string{EnvAppID}},
		{"setup mode ignores oauth", with(minimalEnv(), EnvOAuthClientID, "id", EnvOAuthClientSecret, "secret"), true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadConfig(envOf(tt.env))
			if len(tt.missing) > 0 {
				require.Error(t, err)
				for _, name := range tt.missing {
					assert.Contains(t, err.Error(), name+" is required when any of "+EnvAppID)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.setupMode, cfg.SetupMode)
		})
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no database", with(minimalEnv(), EnvDatabaseURL, ""), EnvDatabaseURL + " is required"},
		{"malformed database", with(minimalEnv(), EnvDatabaseURL, "postgres://u:p@host:notaport/db"), EnvDatabaseURL + " is not a valid Postgres connection string"},
		{"no base url", with(minimalEnv(), EnvBaseURL, ""), EnvBaseURL + " is required"},
		{"relative base url", with(minimalEnv(), EnvBaseURL, "stackorder.example.com"), EnvBaseURL + " must be an absolute http or https URL"},
		{"base url with a query", with(minimalEnv(), EnvBaseURL, "https://stackorder.example.com/?x=1"), EnvBaseURL + " must not carry a query"},
		{"ftp base url", with(minimalEnv(), EnvBaseURL, "ftp://stackorder.example.com"), EnvBaseURL + " must be an absolute http or https URL"},
		{"listen without port", with(minimalEnv(), EnvListen, "8080"), EnvListen + " must be host:port or :port"},
		{"listen with a bad port", with(minimalEnv(), EnvListen, ":http"), EnvListen + " has an invalid port"},
		{"listen out of range", with(minimalEnv(), EnvListen, ":70000"), EnvListen + " has an invalid port"},
		{"app id not a number", with(appEnv(t), EnvAppID, "stackorder"), EnvAppID + " must be the numeric App id"},
		{"app id zero", with(appEnv(t), EnvAppID, "0"), EnvAppID + " must be the numeric App id"},
		{"private key not pem", with(appEnv(t), EnvAppPrivateKey, "not a key"), EnvAppPrivateKey + " is not a usable App private key"},
		{"oauth id without secret", with(appEnv(t), EnvOAuthClientID, "Iv1.x"), EnvOAuthClientSecret + " is required when " + EnvOAuthClientID + " is set"},
		{"oauth secret without id", with(appEnv(t), EnvOAuthClientSecret, "s"), EnvOAuthClientID + " is required when " + EnvOAuthClientSecret + " is set"},
		{"api url relative", with(minimalEnv(), EnvGitHubAPIURL, "api.github.com"), EnvGitHubAPIURL + " must be an absolute"},
		{"web url relative", with(minimalEnv(), EnvGitHubWebURL, "/github"), EnvGitHubWebURL + " must be an absolute"},
		{"issuer relative", with(minimalEnv(), EnvOIDCIssuer, "token.actions.githubusercontent.com"), EnvOIDCIssuer + " must be an absolute"},
		{"jwks relative", with(minimalEnv(), EnvOIDCJWKSURL, "jwks"), EnvOIDCJWKSURL + " must be an absolute"},
		{"workflow ref without @", with(minimalEnv(), EnvRequiredWorkflowRef, "stackorder/actions/.github/workflows/*.yml"), EnvRequiredWorkflowRef + " is invalid"},
		{"workflow ref bad glob", with(minimalEnv(), EnvRequiredWorkflowRef, "stackorder/actions/[.yml@refs/tags/v1"), EnvRequiredWorkflowRef + " is invalid"},
		{"bucket url", with(minimalEnv(), EnvArtifactBucket, "s3://acme-artifacts"), EnvArtifactBucket + " must be a bucket name"},
		{"bucket path", with(minimalEnv(), EnvArtifactBucket, "acme-artifacts/plans"), EnvArtifactBucket + " must be a bucket name"},
		{"prefix without bucket", with(minimalEnv(), EnvArtifactPrefix, "plans"), EnvArtifactPrefix + " is set without " + EnvArtifactBucket},
		{"artifact endpoint relative", with(minimalEnv(), EnvArtifactBucket, "b", EnvArtifactEndpoint, "localstack:4566"), EnvArtifactEndpoint + " must be an absolute"},
		{"session key not hex", with(minimalEnv(), EnvSessionKey, "not-hex"), EnvSessionKey + " must be hex encoded"},
		{"session key short", with(minimalEnv(), EnvSessionKey, "abcd"), EnvSessionKey + " must decode to 32 bytes"},
		{"session key long", with(minimalEnv(), EnvSessionKey, strings.Repeat("ab", 33)), EnvSessionKey + " must decode to 32 bytes"},
		{"plan text retention", with(minimalEnv(), EnvPlanTextRetention, "30d"), EnvPlanTextRetention + " must be a positive Go duration"},
		{"event retention zero", with(minimalEnv(), EnvEventRetention, "0s"), EnvEventRetention + " must be a positive Go duration"},
		{"drift retention negative", with(minimalEnv(), EnvDriftRetention, "-1h"), EnvDriftRetention + " must be a positive Go duration"},
		{"workers not a number", with(minimalEnv(), EnvWorkers, "four"), EnvWorkers + " must be a positive integer"},
		{"workers zero", with(minimalEnv(), EnvWorkers, "0"), EnvWorkers + " must be a positive integer"},
		{"log level", with(minimalEnv(), EnvLogLevel, "verbose"), EnvLogLevel + " must be debug, info, warn or error"},
		{"log format", with(minimalEnv(), EnvLogFormat, "logfmt"), EnvLogFormat + ` must be "json" or "text"`},
		{"otlp endpoint relative", with(minimalEnv(), EnvOTLPEndpoint, "collector:4318"), EnvOTLPEndpoint + " must be an absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadConfig(envOf(tt.env))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, Config{}, cfg, "no partial configuration is returned")
		})
	}
}

func TestLoadConfigReportsEveryProblem(t *testing.T) {
	_, err := LoadConfig(envOf(map[string]string{EnvWorkers: "-1", EnvLogFormat: "xml"}))
	require.Error(t, err)
	for _, name := range []string{EnvDatabaseURL, EnvBaseURL, EnvWorkers, EnvLogFormat} {
		assert.Contains(t, err.Error(), name)
	}
}

func TestLoadConfigKeepsSecretsOutOfErrors(t *testing.T) {
	_, err := LoadConfig(envOf(with(appEnv(t),
		EnvDatabaseURL, "postgres://stackorder:hunter2@db:notaport/stackorder",
		EnvSessionKey, "zz-super-secret-zz",
		EnvAppPrivateKey, "-----BEGIN RSA PRIVATE KEY-----\ntopsecret\n-----END RSA PRIVATE KEY-----")))
	require.Error(t, err)
	for _, secret := range []string{"hunter2", "super-secret", "topsecret"} {
		assert.NotContains(t, err.Error(), secret)
	}
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	cfg := Config{BaseURL: testBaseURL + "/", DatabaseURL: testDSN, SetupMode: true}
	cfg.withDefaults()
	require.NoError(t, cfg.validate())
	assert.Equal(t, testBaseURL, cfg.BaseURL)
	assert.Equal(t, testBaseURL, cfg.OIDCAudience)
	assert.Equal(t, DefaultListen, cfg.Listen)
	assert.Equal(t, DefaultWorkers, cfg.Workers)
	assert.Equal(t, DefaultPlanTextRetention, cfg.PlanTextRetention)
	assert.Equal(t, DefaultEventRetention, cfg.EventRetention)
	assert.Equal(t, DefaultDriftRetention, cfg.DriftRetention)
	assert.Equal(t, gh.DefaultBaseURL, cfg.GitHubAPIURL)
	assert.Equal(t, gh.DefaultWebURL, cfg.GitHubWebURL)
	assert.Equal(t, oidc.DefaultIssuer, cfg.OIDCIssuer)
	assert.Equal(t, LogFormatJSON, cfg.LogFormat)

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{"empty", Config{}, []string{EnvDatabaseURL, EnvBaseURL, EnvAppID, EnvAppPrivateKey, EnvWebhookSecret}},
		{"configured without key", Config{BaseURL: testBaseURL, DatabaseURL: testDSN, AppID: 1, WebhookSecret: []byte("s")}, []string{EnvAppPrivateKey}},
		{"bad workflow ref", Config{BaseURL: testBaseURL, DatabaseURL: testDSN, SetupMode: true, RequiredWorkflowRef: "x"}, []string{EnvRequiredWorkflowRef}},
		{"short session key", Config{BaseURL: testBaseURL, DatabaseURL: testDSN, SetupMode: true, SessionKey: []byte("short")}, []string{EnvSessionKey}},
		{"bad log format", Config{BaseURL: testBaseURL, DatabaseURL: testDSN, SetupMode: true, LogFormat: "xml"}, []string{EnvLogFormat}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.cfg
			if c.LogFormat == "" {
				c.withDefaults()
			}
			err := c.validate()
			require.Error(t, err)
			for _, name := range tt.want {
				assert.Contains(t, err.Error(), name)
			}
		})
	}
}

func TestPoolMaxConns(t *testing.T) {
	assert.Equal(t, int32(DefaultWorkers+PoolConnsBeyondWorkers), Config{}.PoolMaxConns())
	assert.Equal(t, int32(12), Config{Workers: 4}.PoolMaxConns())
	assert.Equal(t, int32(40), Config{Workers: 32}.PoolMaxConns())
	cfg, err := LoadConfig(func(name string) string {
		return map[string]string{EnvBaseURL: testBaseURL, EnvDatabaseURL: testDSN, EnvWorkers: "6"}[name]
	})
	require.NoError(t, err)
	assert.Equal(t, int32(14), cfg.PoolMaxConns(), "STACKORDER_WORKERS plus eight")
}

func TestAllowResetup(t *testing.T) {
	load := func(v string) (Config, error) {
		return LoadConfig(func(name string) string {
			return map[string]string{EnvBaseURL: testBaseURL, EnvDatabaseURL: testDSN, EnvAllowResetup: v}[name]
		})
	}
	cfg, err := load("")
	require.NoError(t, err)
	assert.False(t, cfg.AllowResetup, "re-running setup is off by default")
	for _, v := range []string{"true", "TRUE", "1"} {
		cfg, err = load(v)
		require.NoError(t, err, v)
		assert.True(t, cfg.AllowResetup, v)
	}
	cfg, err = load("false")
	require.NoError(t, err)
	assert.False(t, cfg.AllowResetup)
	_, err = load("yes please")
	require.ErrorContains(t, err, EnvAllowResetup+" must be true or false")

	s := &Server{cfg: Config{BaseURL: testBaseURL, AllowResetup: true}}
	assert.True(t, s.apiConfig().AllowResetup, "the flag reaches the API")
}
