package gh_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

const tokenRoute = "POST /app/installations/{installation_id}/access_tokens"

func countRequests(fake *ghfake.Server, pattern string) int {
	n := 0
	for _, r := range fake.Requests() {
		if r.Pattern == pattern {
			n++
		}
	}
	return n
}

func TestNewAppValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tests := []struct {
		name string
		cfg  gh.Config
		want string
	}{
		{"missing id", gh.Config{PrivateKey: key}, "app id"},
		{"missing key", gh.Config{AppID: 1}, "private key"},
		{"bad url", gh.Config{AppID: 1, PrivateKey: key, BaseURL: "::"}, "base url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := gh.NewApp(tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	app, err := gh.NewApp(gh.Config{AppID: 7, PrivateKey: key})
	require.NoError(t, err)
	assert.Equal(t, int64(7), app.ID())
}

func TestJWTClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	clock := newClock()
	app, err := gh.NewApp(gh.Config{AppID: 12345, PrivateKey: key, Clock: clock.Now})
	require.NoError(t, err)

	signed, err := app.JWT()
	require.NoError(t, err)
	var claims jwt.RegisteredClaims
	tok, err := jwt.ParseWithClaims(signed, &claims, func(tok *jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithTimeFunc(clock.Now))
	require.NoError(t, err)
	assert.Equal(t, "RS256", tok.Header["alg"])
	assert.Equal(t, "12345", claims.Issuer)
	assert.Equal(t, clock.Now().Add(-60*time.Second), claims.IssuedAt.UTC())
	assert.Equal(t, clock.Now().Add(10*time.Minute), claims.ExpiresAt.UTC())
}

func TestInstallationTokenIsCachedAndRefreshedNearExpiry(t *testing.T) {
	fake := ghfake.New(t)
	clock := newClock()
	fake.SetClock(clock.Now)
	fake.AddInstallation(42, "acme", "acme/infra")
	cfg := fake.AppConfig()
	cfg.Clock = clock.Now
	app, err := gh.NewApp(cfg)
	require.NoError(t, err)
	ctx := context.Background()

	first, err := app.InstallationToken(ctx, 42)
	require.NoError(t, err)
	assert.Equal(t, "ghs_fake_42_1", first.Token)
	assert.Equal(t, clock.Now().Add(time.Hour), first.ExpiresAt)
	assert.Equal(t, "selected", first.RepositorySelection)
	assert.Equal(t, "write", first.Permissions["checks"])

	tests := []struct {
		advance time.Duration
		want    string
		calls   int
	}{
		{0, "ghs_fake_42_1", 1},
		{30 * time.Minute, "ghs_fake_42_1", 1},
		{24*time.Minute + 59*time.Second, "ghs_fake_42_1", 1},
		{time.Second, "ghs_fake_42_2", 2},
		{10 * time.Minute, "ghs_fake_42_2", 2},
		{50 * time.Minute, "ghs_fake_42_3", 3},
	}
	for _, tt := range tests {
		clock.Advance(tt.advance)
		tok, err := app.InstallationToken(ctx, 42)
		require.NoError(t, err)
		assert.Equal(t, tt.want, tok.Token, "after advancing %s", tt.advance)
		assert.Equal(t, tt.calls, countRequests(fake, tokenRoute))
	}

	fake.AddInstallation(43, "other")
	other, err := app.InstallationToken(ctx, 43)
	require.NoError(t, err)
	assert.Equal(t, "ghs_fake_43_1", other.Token)
}

func TestInstallationTokenSingleFlight(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme")
	fake.SetLatency(50 * time.Millisecond)
	app := fake.NewApp()

	const callers = 20
	var wg sync.WaitGroup
	tokens := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			tok, err := app.InstallationToken(context.Background(), 1)
			tokens[i], errs[i] = tok.Token, err
		})
	}
	wg.Wait()
	for i := range callers {
		require.NoError(t, errs[i])
		assert.Equal(t, "ghs_fake_1_1", tokens[i])
	}
	assert.Equal(t, 1, countRequests(fake, tokenRoute))
}

func TestInstallationTokenFollowerSurvivesCancelledLeader(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme")
	fake.SetLatency(150 * time.Millisecond)
	app := fake.NewApp()

	leaderCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	leaderErr := make(chan error, 1)
	go func() {
		_, err := app.InstallationToken(leaderCtx, 1)
		leaderErr <- err
	}()
	time.Sleep(10 * time.Millisecond)
	tok, err := app.InstallationToken(context.Background(), 1)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tok.Token, "ghs_fake_1_"))
	require.ErrorIs(t, <-leaderErr, context.DeadlineExceeded)
}

func TestInstallationTokenFollowerContextCancelled(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme")
	fake.SetLatency(200 * time.Millisecond)
	app := fake.NewApp()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = app.InstallationToken(context.Background(), 1)
	}()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := app.InstallationToken(ctx, 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	<-done
}

func TestInstallationTokenErrors(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme")
	fake.AddInstallation(2, "suspended")
	fake.SuspendInstallation(2)
	app := fake.NewApp()
	ctx := context.Background()

	_, err := app.InstallationToken(ctx, 99)
	require.ErrorIs(t, err, gh.ErrNotFound)

	_, err = app.Client(ctx, 2)
	var apiErr *gh.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Status)
	assert.Equal(t, "/app/installations/{installation_id}/access_tokens", apiErr.Route)
}

func TestJWTRejectedWithWrongKey(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme")
	cfg := fake.AppConfig()
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	cfg.PrivateKey = other
	app, err := gh.NewApp(cfg)
	require.NoError(t, err)
	_, err = app.AppInfo(context.Background())
	var apiErr *gh.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Status)
}

func TestRevokedTokenIsRefreshed(t *testing.T) {
	fake := ghfake.New(t)
	fake.AddInstallation(1, "acme", "acme/infra")
	app := fake.NewApp()
	ctx := context.Background()
	c, err := app.Client(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c.InstallationID())

	fake.RevokeTokens()
	repo, err := c.GetRepository(ctx, "acme/infra")
	require.NoError(t, err)
	assert.Equal(t, "acme/infra", repo.FullName)
	assert.Equal(t, 2, countRequests(fake, tokenRoute))
}

func TestAppEndpoints(t *testing.T) {
	fake := ghfake.New(t)
	fake.SetPageSize(1)
	fake.AddInstallation(10, "acme", "acme/infra", "acme/modules")
	fake.AddInstallation(20, "globex", "globex/platform")
	fake.AddInstallation(30, "initech")
	app := fake.NewApp()
	ctx := context.Background()

	info, err := app.AppInfo(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.ID)
	assert.Equal(t, "stackorder-test", info.Slug)
	assert.Equal(t, gh.DefaultEvents, info.Events)

	inst, err := app.Installation(ctx, 20)
	require.NoError(t, err)
	assert.Equal(t, "globex", inst.Account.Login)
	assert.Equal(t, "Organization", inst.Account.Type)
	assert.Nil(t, inst.SuspendedAt)

	_, err = app.Installation(ctx, 404)
	require.ErrorIs(t, err, gh.ErrNotFound)

	all, err := app.ListInstallations(ctx)
	require.NoError(t, err)
	ids := make([]int64, len(all))
	for i, in := range all {
		ids[i] = in.ID
	}
	assert.Equal(t, []int64{10, 20, 30}, ids)
	assert.Equal(t, 3, countRequests(fake, "GET /app/installations"))

	repos, err := app.InstallationRepos(ctx, 10)
	require.NoError(t, err)
	require.Len(t, repos, 2)
	assert.Equal(t, "acme/infra", repos[0].FullName)
	assert.Equal(t, "acme/modules", repos[1].FullName)
	assert.Equal(t, "main", repos[0].DefaultBranch)

	for _, r := range fake.Requests() {
		switch r.Pattern {
		case "GET /app", "GET /app/installations", "GET /app/installations/{installation_id}", tokenRoute:
			assert.Equal(t, "jwt", r.Auth, r.Pattern)
		case "GET /installation/repositories":
			assert.Equal(t, "installation", r.Auth)
			assert.Equal(t, int64(10), r.InstallationID)
		}
		assert.Equal(t, gh.MediaType, r.Header.Get("Accept"))
		assert.Equal(t, gh.APIVersion, r.Header.Get("X-GitHub-Api-Version"))
	}
}

func encodePEM(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func TestParsePrivateKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pkcs1 := encodePEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pkcs8 := encodePEM(t, "PRIVATE KEY", pkcs8DER)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	require.NoError(t, err)

	tests := []struct {
		name    string
		input   []byte
		wantErr string
	}{
		{"pkcs1", pkcs1, ""},
		{"pkcs8", pkcs8, ""},
		{"escaped newlines", []byte(strings.ReplaceAll(strings.TrimSpace(string(pkcs1)), "\n", `\n`)), ""},
		{"base64 of pem", []byte(base64.StdEncoding.EncodeToString(pkcs1)), ""},
		{"surrounding whitespace", append([]byte("\n  "), pkcs8...), ""},
		{"garbage", []byte("not a key"), "no PEM block"},
		{"wrong block", encodePEM(t, "CERTIFICATE", []byte{1, 2, 3}), "unexpected PEM block"},
		{"corrupt pkcs1", encodePEM(t, "RSA PRIVATE KEY", []byte{1, 2, 3}), "private key"},
		{"corrupt pkcs8", encodePEM(t, "PRIVATE KEY", []byte{1, 2, 3}), "private key"},
		{"ecdsa", encodePEM(t, "PRIVATE KEY", ecDER), "not an RSA key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gh.ParsePrivateKey(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.True(t, got.Equal(key))
		})
	}
}
