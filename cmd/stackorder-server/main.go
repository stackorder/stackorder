// Command stackorder-server is the control plane: webhooks, the runner API,
// the human API and the embedded web UI in one binary.
//
// Usage:
//
//	stackorder-server              run the server, configured by the environment
//	stackorder-server healthcheck  exit 0 when GET /healthz on this host answers 200
//	stackorder-server version      print the build information
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stackorder/stackorder/internal/server"
	"github.com/stackorder/stackorder/internal/version"
)

const healthcheckTimeout = 3 * time.Second

const usage = `usage: stackorder-server [command]

Commands:
  (none)       run the server; configuration comes from the environment
  healthcheck  GET http://127.0.0.1<STACKORDER_LISTEN>/healthz, exit 0 on 200
  version      print the build information
`

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return serve(getenv, stderr)
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, "stackorder-server", version.String())
		return 0
	case "healthcheck":
		return healthcheck(getenv, stdout, stderr)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "stackorder-server: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func serve(getenv func(string) string, stderr io.Writer) int {
	cfg, err := server.LoadConfig(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server:", err)
		return 1
	}
	logger := server.NewLogger(stderr, cfg.LogLevel, cfg.LogFormat)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)
	srv, err := server.New(ctx, cfg, server.WithLogger(logger))
	if err != nil {
		logger.Error("stackorder server failed to start", "error", err)
		return 1
	}
	if err := srv.Run(ctx); err != nil {
		logger.Error("stackorder server failed", "error", err)
		return 1
	}
	return 0
}

func healthcheck(getenv func(string) string, stdout, stderr io.Writer) int {
	listen := strings.TrimSpace(getenv(server.EnvListen))
	if listen == "" {
		listen = server.DefaultListen
	}
	target, err := healthURL(listen)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server healthcheck:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server healthcheck:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "stackorder-server healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "stackorder-server healthcheck: GET %s: %s: %s\n", target, resp.Status, strings.TrimSpace(string(body)))
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "stackorder-server healthcheck: GET %s: %s\n", target, resp.Status)
	return 0
}

func healthURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("%s=%q is not host:port or :port: %w", server.EnvListen, listen, err)
	}
	if port == "" || port == "0" {
		return "", fmt.Errorf("%s=%q names no fixed port", server.EnvListen, listen)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
