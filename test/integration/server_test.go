//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/server"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
	"github.com/stackorder/stackorder/migrations"
)

func TestMain(m *testing.M) {
	code := pgtest.Main(m, closeSuite)
	if suite.failed && code == 0 {
		code = 1
	}
	os.Exit(code)
}

const waitFor = 20 * time.Second

func latestMigration(t *testing.T) uint {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.up.sql")
	require.NoError(t, err)
	var latest uint
	for _, name := range names {
		n, err := strconv.ParseUint(name[:strings.IndexByte(name, '_')], 10, 32)
		require.NoError(t, err)
		latest = max(latest, uint(n))
	}
	require.NotZero(t, latest)
	return latest
}

func TestServerBootsAndMigrates(t *testing.T) {
	e := NewEnv(t)
	version, dirty, err := e.Store.SchemaVersion(t.Context())
	require.NoError(t, err)
	assert.Equal(t, latestMigration(t), version, "every migration is applied at start-up")
	assert.False(t, dirty)
	assert.False(t, e.Server.Config().SetupMode)
	assert.Equal(t, e.BaseURL, e.Server.Config().OIDCAudience)

	again, err := server.New(t.Context(), e.Server.Config(), server.WithListener(mustListen(t)))
	require.NoError(t, err, "a second server on a migrated database starts")
	require.NoError(t, again.Close())
}

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return ln
}

type health struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	SetupMode bool   `json:"setup_mode"`
}

func TestHealthAndReadiness(t *testing.T) {
	e := NewEnv(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		status, body := e.Get(path)
		require.Equal(t, http.StatusOK, status, "%s: %s", path, body)
		var h health
		require.NoError(t, json.Unmarshal([]byte(body), &h))
		assert.Equal(t, "ok", h.Status)
		assert.Equal(t, "dev", h.Version)
		assert.False(t, h.SetupMode)
	}
}

func TestMetricsExposeBuildInfo(t *testing.T) {
	e := NewEnv(t)
	status, _ := e.Get("/healthz")
	require.Equal(t, http.StatusOK, status)
	status, body := e.Get("/metrics")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `stackorder_build_info{commit="none",version="dev"} 1`)
	assert.Contains(t, body, `stackorder_http_requests_total{method="GET",route="GET /healthz",status="200"}`)
	assert.Contains(t, body, `stackorder_jobs_processed_total{kind="sync_installations",result="ok"} 1`)
	assert.Contains(t, body, "stackorder_scheduler_leader")
}

func TestMetricsToken(t *testing.T) {
	e := NewEnv(t, WithVar(server.EnvMetricsToken, "scrape-me"))
	status, _ := e.Get("/metrics")
	assert.Equal(t, http.StatusUnauthorized, status)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.URL("/metrics"), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer scrape-me")
	status, body := e.Do(req)
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "stackorder_build_info")
}

func installAcme(g *ghfake.Server) {
	g.SetRepo("acme/infra", gh.Repository{ID: 100, DefaultBranch: "main", Private: true})
	g.AddInstallation(7, "acme", "acme/infra")
	g.SetRef("acme/infra", "heads/main", "1111111111111111111111111111111111111111")
	g.SetContents("acme/infra", "main", "stackorder.yaml", []byte("version: 1\ntool: tofu\n"))
}

func TestWebhookSignatureAndProcessing(t *testing.T) {
	e := NewEnv(t)
	installAcme(e.GH)
	ev := e.GH.InstallationEvent("created", 7)

	bad := e.DeliverSigned([]byte("not-the-secret"), gh.EventInstallation, ev)
	assert.Equal(t, http.StatusUnauthorized, bad.Status, bad.Body)
	_, err := e.Store.GetEvent(t.Context(), bad.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "a delivery with a bad signature is not queued")

	payload, header := ghfake.SignedWebhook([]byte(WebhookSecret), gh.EventInstallation, ev)
	header.Del(gh.HeaderSignature)
	unsigned := e.Redeliver(Delivery{Payload: payload, Header: header})
	assert.Equal(t, http.StatusUnauthorized, unsigned.Status, "a delivery without a signature is refused")

	good := e.Deliver(gh.EventInstallation, ev)
	require.Equal(t, http.StatusAccepted, good.Status, good.Body)
	assert.JSONEq(t, `{"queued":true}`, good.Body)
	e.WaitFor(func() bool {
		done, _ := e.EventDone(good.ID)
		return done
	}, waitFor, "the worker pool processes the installation event")
	_, lastErr := e.EventDone(good.ID)
	assert.Empty(t, lastErr)

	repo, err := e.Store.GetRepo(t.Context(), 100)
	require.NoError(t, err, "runs.HandleInstallation recorded the repository")
	assert.Equal(t, "acme/infra", repo.FullName)
	assert.Equal(t, int64(7), repo.InstallationID)
	require.NotNil(t, repo.Config, "stackorder.yaml was loaded")
	assert.Equal(t, v1.ToolTofu, repo.Config.Tool)
	inst, err := e.Store.GetInstallation(t.Context(), 7)
	require.NoError(t, err)
	assert.Equal(t, "acme", inst.Account)

	again := e.Redeliver(good)
	assert.Equal(t, http.StatusAccepted, again.Status)
	assert.JSONEq(t, `{"queued":false}`, again.Body, "a redelivery is deduplicated by its id")
}

func TestUnhandledEventsAreCompleted(t *testing.T) {
	e := NewEnv(t)
	d := e.Deliver("meta", map[string]any{"action": "deleted", "hook_id": 1})
	require.Equal(t, http.StatusAccepted, d.Status, d.Body)
	e.WaitFor(func() bool {
		done, _ := e.EventDone(d.ID)
		return done
	}, waitFor, "the pool completes an event no handler takes")

	ping := e.Deliver(gh.EventPing, map[string]any{"zen": "Keep it logically awesome.", "hook_id": 1})
	assert.Equal(t, http.StatusOK, ping.Status)
}

func TestStartupSyncLearnsInstallations(t *testing.T) {
	e := NewEnv(t, WithGitHub(installAcme))
	repo, err := e.Store.GetRepo(t.Context(), 100)
	require.NoError(t, err, "an installation GitHub knows is learned at start-up, without its webhook")
	assert.Equal(t, "acme/infra", repo.FullName)
	require.NotNil(t, repo.Config)
	assert.Equal(t, v1.ToolTofu, repo.Config.Tool)
}

var manifestInput = regexp.MustCompile(`<input type="hidden" name="manifest" value="([^"]+)">`)

func TestSetupMode(t *testing.T) {
	e := NewEnv(t, SetupMode())
	assert.True(t, e.Server.Config().SetupMode)

	for _, path := range []string{"/healthz", "/readyz"} {
		status, body := e.Get(path)
		require.Equal(t, http.StatusOK, status, "%s: %s", path, body)
		var h health
		require.NoError(t, json.Unmarshal([]byte(body), &h))
		assert.True(t, h.SetupMode, path)
	}

	status, body := e.Get("/setup")
	require.Equal(t, http.StatusOK, status, body)
	m := manifestInput.FindStringSubmatch(body)
	require.NotNil(t, m, "the setup page carries the App manifest")
	var manifest struct {
		URL            string `json:"url"`
		HookAttributes struct {
			URL string `json:"url"`
		} `json:"hook_attributes"`
		RedirectURL   string            `json:"redirect_url"`
		CallbackURLs  []string          `json:"callback_urls"`
		DefaultEvents []string          `json:"default_events"`
		Permissions   map[string]string `json:"default_permissions"`
	}
	require.NoError(t, json.Unmarshal([]byte(html.UnescapeString(m[1])), &manifest))
	assert.Equal(t, e.BaseURL, manifest.URL)
	assert.Equal(t, e.BaseURL+"/webhooks/github", manifest.HookAttributes.URL)
	assert.Equal(t, e.BaseURL+"/setup/callback", manifest.RedirectURL)
	assert.Equal(t, []string{e.BaseURL + "/auth/callback"}, manifest.CallbackURLs)
	ghes := e.Server.Config().GitHubWebURL != gh.DefaultWebURL
	assert.ElementsMatch(t, gh.Manifest(e.BaseURL, "stackorder", ghes)["default_events"], manifest.DefaultEvents,
		"the fake GitHub's web URL makes the manifest the Enterprise Server one")
	assert.Equal(t, "write", manifest.Permissions["checks"])

	for _, path := range []string{"/v1/overview", "/metrics", "/"} {
		status, _ := e.Get(path)
		assert.Equal(t, http.StatusServiceUnavailable, status, "%s is not served in setup mode", path)
	}
	d := e.Deliver(gh.EventInstallation, map[string]any{"action": "created"})
	assert.Equal(t, http.StatusServiceUnavailable, d.Status, "no webhook secret, no webhooks")

	var jobs int
	require.NoError(t, e.Store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM jobs`).Scan(&jobs))
	assert.Zero(t, jobs, "setup mode runs no workers and no scheduler")
}

func buildServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stackorder-server")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "github.com/stackorder/stackorder/cmd/stackorder-server")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build: %s", out)
	return bin
}

func runBinary(t *testing.T, bin string, env []string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out)
	}
	require.NoError(t, err, "run %s %v", bin, args)
	return 0, string(out)
}

func TestHealthcheckCommand(t *testing.T) {
	e := NewEnv(t)
	bin := buildServer(t)
	_, port, err := net.SplitHostPort(e.Server.Addr())
	require.NoError(t, err)

	code, out := runBinary(t, bin, []string{server.EnvListen + "=:" + port}, "healthcheck")
	assert.Equal(t, 0, code, out)
	assert.Contains(t, out, "200 OK")

	free := mustListen(t)
	_, freePort, _ := net.SplitHostPort(free.Addr().String())
	require.NoError(t, free.Close())
	code, out = runBinary(t, bin, []string{server.EnvListen + "=:" + freePort}, "healthcheck")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "connection refused")

	code, out = runBinary(t, bin, nil, "version")
	assert.Equal(t, 0, code, out)
	assert.True(t, strings.HasPrefix(out, "stackorder-server "), out)
}

func TestSecondSignalStopsTheServerAtOnce(t *testing.T) {
	bin := buildServer(t)
	free := mustListen(t)
	addr := free.Addr().String()
	require.NoError(t, free.Close())
	cmd := exec.CommandContext(t.Context(), bin)
	cmd.Env = append(os.Environ(),
		server.EnvDatabaseURL+"="+pgtest.DSN(t),
		server.EnvBaseURL+"=http://"+addr,
		server.EnvListen+"="+addr,
	)
	require.NoError(t, cmd.Start())
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	client := &http.Client{Timeout: time.Second}
	require.Eventually(t, func() bool {
		resp, err := client.Get("http://" + addr + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, waitFor, 50*time.Millisecond, "the binary answers /readyz")

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: " + addr + "\r\n"))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, cmd.Process.Signal(os.Interrupt))
	select {
	case <-exited:
		t.Fatal("the server stopped while a request was still being read")
	case <-time.After(time.Second):
	}
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("a second interrupt did not stop the server at once")
	}
}

func TestGracefulShutdown(t *testing.T) {
	e := NewEnv(t)
	installAcme(e.GH)
	body, header := ghfake.SignedWebhook([]byte(WebhookSecret), gh.EventInstallation, e.GH.InstallationEvent("created", 7))
	reader, writer := io.Pipe()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.URL("/webhooks/github"), reader)
	require.NoError(t, err)
	req.Header = header

	type answer struct {
		status int
		body   string
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		resp, err := e.Client.Do(req)
		if err != nil {
			answered <- answer{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		answered <- answer{status: resp.StatusCode, body: string(b)}
	}()
	half := len(body) / 2
	_, err = writer.Write(body[:half])
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	started := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop() }()
	e.WaitFor(func() bool {
		conn, err := net.DialTimeout("tcp", e.Server.Addr(), time.Second)
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	}, waitFor, "the listener stops accepting")
	select {
	case <-stopped:
		t.Fatal("the server stopped while a request was still in flight")
	case <-time.After(200 * time.Millisecond):
	}

	_, err = writer.Write(body[half:])
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	got := <-answered
	require.NoError(t, got.err)
	assert.Equal(t, http.StatusAccepted, got.status, "the in-flight request is finished: %s", got.body)

	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(server.ShutdownTimeout + 45*time.Second):
		t.Fatal("the server did not finish shutting down")
	}
	assert.Less(t, time.Since(started), server.ShutdownTimeout+45*time.Second)
	ev, err := e.Store.GetEvent(t.Context(), header.Get(gh.HeaderDelivery))
	require.NoError(t, err, "the delivery accepted during shutdown was stored")
	assert.Equal(t, gh.EventInstallation, ev.Kind)
}
