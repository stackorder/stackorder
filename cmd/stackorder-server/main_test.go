package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envOf(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestHealthURL(t *testing.T) {
	tests := []struct {
		listen string
		want   string
		err    string
	}{
		{":8080", "http://127.0.0.1:8080/healthz", ""},
		{"0.0.0.0:9090", "http://127.0.0.1:9090/healthz", ""},
		{"[::]:9090", "http://127.0.0.1:9090/healthz", ""},
		{"127.0.0.1:8080", "http://127.0.0.1:8080/healthz", ""},
		{"10.0.0.5:8080", "http://10.0.0.5:8080/healthz", ""},
		{"[::1]:8080", "http://[::1]:8080/healthz", ""},
		{"localhost:8080", "http://localhost:8080/healthz", ""},
		{"8080", "", "is not host:port or :port"},
		{":0", "", "names no fixed port"},
	}
	for _, tt := range tests {
		t.Run(tt.listen, func(t *testing.T) {
			got, err := healthURL(tt.listen)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func listenOn(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(h)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return ":" + port
}

func TestHealthcheck(t *testing.T) {
	healthy := listenOn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/healthz", r.URL.Path)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{"healthcheck"}, envOf(map[string]string{"STACKORDER_LISTEN": healthy}), &stdout, &stderr))
	assert.Contains(t, stdout.String(), "200 OK")
	assert.Empty(t, stderr.String())

	failing := listenOn(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database unreachable", http.StatusServiceUnavailable)
	}))
	stdout.Reset()
	assert.Equal(t, 1, run([]string{"healthcheck"}, envOf(map[string]string{"STACKORDER_LISTEN": failing}), &stdout, &stderr))
	assert.Contains(t, stderr.String(), "503 Service Unavailable: database unreachable")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, ln.Close())
	stderr.Reset()
	assert.Equal(t, 1, run([]string{"healthcheck"}, envOf(map[string]string{"STACKORDER_LISTEN": ":" + port}), &stdout, &stderr))
	assert.Contains(t, stderr.String(), "connection refused")

	stderr.Reset()
	assert.Equal(t, 1, run([]string{"healthcheck"}, envOf(map[string]string{"STACKORDER_LISTEN": "nonsense"}), &stdout, &stderr))
	assert.Contains(t, stderr.String(), "STACKORDER_LISTEN")
}

func TestCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{"version"}, envOf(nil), &stdout, &stderr))
	assert.True(t, strings.HasPrefix(stdout.String(), "stackorder-server "), stdout.String())

	stdout.Reset()
	assert.Equal(t, 0, run([]string{"help"}, envOf(nil), &stdout, &stderr))
	assert.Contains(t, stdout.String(), "healthcheck")

	assert.Equal(t, 2, run([]string{"serve-forever"}, envOf(nil), &stdout, &stderr))
	assert.Contains(t, stderr.String(), `unknown command "serve-forever"`)

	stderr.Reset()
	assert.Equal(t, 1, run(nil, envOf(nil), &stdout, &stderr), "the server refuses to start without its configuration")
	assert.Contains(t, stderr.String(), "DATABASE_URL is required")
	assert.Contains(t, stderr.String(), "STACKORDER_BASE_URL is required")
}
