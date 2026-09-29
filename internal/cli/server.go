package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/stackorder/stackorder/internal/client"
)

var errNoServer = errors.New("no server is configured; set --server or " + EnvServerURL)

var clientOptions []client.Option

func (a *app) newClient(gh *Context, apiKey bool) (*client.Client, error) {
	if a.server == "" {
		return nil, errNoServer
	}
	var ts client.TokenSource
	if gh.CI && !apiKey {
		audience := os.Getenv(EnvOIDCAudience)
		if audience == "" {
			audience = a.server
		}
		ts = client.OIDCTokenSource(audience)
	} else {
		ts = client.APIKeyTokenSource(strings.TrimSpace(os.Getenv(EnvAPIKey)))
	}
	opts := append([]client.Option{client.WithLogger(a.log)}, clientOptions...)
	return client.New(a.server, ts, opts...), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func isUnreachable(err error) bool { return client.IsUnreachable(err) }

func (a *app) warn(msg string) {
	msg = a.redactMessage(msg)
	if inActions() {
		a.annotate("warning", msg)
		return
	}
	a.log.Warn(msg)
}
