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
		ts = client.OIDCTokenSource(a.oidcAudience())
	} else {
		ts = client.APIKeyTokenSource(strings.TrimSpace(os.Getenv(EnvAPIKey)))
	}
	opts := append([]client.Option{client.WithLogger(a.log)}, clientOptions...)
	return client.New(a.server, ts, opts...), nil
}

// oidcAudience returns the audience for the runner's GitHub OIDC token. It is
// STACKORDER_OIDC_AUDIENCE when set, trimmed of surrounding whitespace to
// match how the server reads it and with any trailing slash kept; otherwise
// it is the server URL with trailing slashes removed, matching the audience
// the server derives from STACKORDER_BASE_URL.
func (a *app) oidcAudience() string {
	if audience := strings.TrimSpace(os.Getenv(EnvOIDCAudience)); audience != "" {
		return audience
	}
	return strings.TrimRight(a.server, "/")
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
