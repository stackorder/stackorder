package gh

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	jwtLifetime     = 10 * time.Minute
	jwtBackdate     = 60 * time.Second
	tokenRefreshGap = 5 * time.Minute
)

// Token is an installation access token.
type Token struct {
	Token               string            `json:"token"`
	ExpiresAt           time.Time         `json:"expires_at"`
	Permissions         map[string]string `json:"permissions,omitempty"`
	RepositorySelection string            `json:"repository_selection,omitempty"`
}

// AppInfo describes the authenticated App, from GET /app.
type AppInfo struct {
	ID          int64             `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	ClientID    string            `json:"client_id,omitempty"`
	Owner       User              `json:"owner"`
	HTMLURL     string            `json:"html_url,omitempty"`
	Permissions map[string]string `json:"permissions,omitempty"`
	Events      []string          `json:"events,omitempty"`
}

// Installation is one installation of the App on an account.
type Installation struct {
	ID                  int64             `json:"id"`
	Account             User              `json:"account"`
	AppID               int64             `json:"app_id,omitempty"`
	AppSlug             string            `json:"app_slug,omitempty"`
	TargetType          string            `json:"target_type,omitempty"`
	RepositorySelection string            `json:"repository_selection,omitempty"`
	HTMLURL             string            `json:"html_url,omitempty"`
	Permissions         map[string]string `json:"permissions,omitempty"`
	Events              []string          `json:"events,omitempty"`
	SuspendedAt         *time.Time        `json:"suspended_at,omitempty"`
}

// App is an authenticated GitHub App. It is safe for concurrent use.
type App struct {
	id  int64
	key *rsa.PrivateKey
	t   *transport

	mu       sync.Mutex
	tokens   map[int64]Token
	inflight map[int64]*tokenCall
	login    string
}

type tokenCall struct {
	done      chan struct{}
	tok       Token
	err       error
	abandoned bool
}

// NewApp validates cfg and returns an App.
func NewApp(cfg Config) (*App, error) {
	if cfg.AppID <= 0 {
		return nil, errors.New("gh: app id is required")
	}
	if cfg.PrivateKey == nil {
		return nil, errors.New("gh: app private key is required")
	}
	t, err := newTransport(cfg)
	if err != nil {
		return nil, err
	}
	return &App{
		id:       cfg.AppID,
		key:      cfg.PrivateKey,
		t:        t,
		tokens:   map[int64]Token{},
		inflight: map[int64]*tokenCall{},
	}, nil
}

// ID returns the App id.
func (a *App) ID() int64 { return a.id }

// JWT returns a fresh App JWT: RS256, valid for 10 minutes from an issue
// time 60 s in the past, so a clock up to a minute off GitHub's in either
// direction is tolerated.
func (a *App) JWT() (string, error) {
	issued := a.t.now().Add(-jwtBackdate)
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(a.id, 10),
		IssuedAt:  jwt.NewNumericDate(issued),
		ExpiresAt: jwt.NewNumericDate(issued.Add(jwtLifetime)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("gh: sign app jwt: %w", err)
	}
	return signed, nil
}

type appJWT struct{ app *App }

func (s appJWT) token(context.Context) (string, error) { return s.app.JWT() }

func (appJWT) invalidate() bool { return false }

func (a *App) jwtAuth() tokenSource { return appJWT{app: a} }

// InstallationToken returns a token for the installation, served from a
// per-installation cache until five minutes before it expires. Concurrent
// callers for the same installation share one exchange.
func (a *App) InstallationToken(ctx context.Context, installationID int64) (Token, error) {
	for {
		a.mu.Lock()
		if tok, ok := a.tokens[installationID]; ok && a.t.now().Add(tokenRefreshGap).Before(tok.ExpiresAt) {
			a.mu.Unlock()
			return tok, nil
		}
		if c, ok := a.inflight[installationID]; ok {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return Token{}, fmt.Errorf("gh: installation %d token: %w", installationID, ctx.Err())
			case <-c.done:
			}
			if c.abandoned && ctx.Err() == nil {
				continue
			}
			return c.tok, c.err
		}
		c := &tokenCall{done: make(chan struct{})}
		a.inflight[installationID] = c
		a.mu.Unlock()

		c.tok, c.err = a.exchange(ctx, installationID)
		c.abandoned = c.err != nil && ctx.Err() != nil

		a.mu.Lock()
		delete(a.inflight, installationID)
		if c.err == nil {
			a.tokens[installationID] = c.tok
		}
		a.mu.Unlock()
		close(c.done)
		return c.tok, c.err
	}
}

func (a *App) exchange(ctx context.Context, installationID int64) (Token, error) {
	var tok Token
	err := a.t.call(ctx, request{
		method: http.MethodPost,
		route:  "/app/installations/{installation_id}/access_tokens",
		path:   "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens",
		auth:   a.jwtAuth(),
	}, &tok)
	if err != nil {
		return Token{}, err
	}
	if tok.Token == "" {
		return Token{}, fmt.Errorf("gh: installation %d: empty token in response", installationID)
	}
	return tok, nil
}

func (a *App) invalidateToken(installationID int64) {
	a.mu.Lock()
	delete(a.tokens, installationID)
	a.mu.Unlock()
}

type installationAuth struct {
	app *App
	id  int64
}

func (s installationAuth) token(ctx context.Context) (string, error) {
	tok, err := s.app.InstallationToken(ctx, s.id)
	if err != nil {
		return "", err
	}
	return tok.Token, nil
}

func (s installationAuth) invalidate() bool {
	s.app.invalidateToken(s.id)
	return true
}

// Client returns a client scoped to one installation. It fetches a token
// eagerly so an unknown or suspended installation fails here; later calls
// refresh the token transparently.
func (a *App) Client(ctx context.Context, installationID int64) (*Client, error) {
	if _, err := a.InstallationToken(ctx, installationID); err != nil {
		return nil, err
	}
	return &Client{t: a.t, auth: installationAuth{app: a, id: installationID}, installationID: installationID, app: a}, nil
}

func (a *App) botLogin(ctx context.Context) (string, error) {
	a.mu.Lock()
	login := a.login
	a.mu.Unlock()
	if login != "" {
		return login, nil
	}
	info, err := a.AppInfo(ctx)
	if err != nil {
		return "", err
	}
	if info.Slug == "" {
		return "", errors.New("gh: app description has no slug")
	}
	login = info.Slug + "[bot]"
	a.mu.Lock()
	a.login = login
	a.mu.Unlock()
	return login, nil
}

// AppInfo returns the App's own description from GET /app.
func (a *App) AppInfo(ctx context.Context) (*AppInfo, error) {
	var info AppInfo
	if err := a.t.call(ctx, request{method: http.MethodGet, route: "/app", path: "/app", auth: a.jwtAuth()}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Installation returns one installation of the App.
func (a *App) Installation(ctx context.Context, id int64) (*Installation, error) {
	var inst Installation
	err := a.t.call(ctx, request{
		method: http.MethodGet,
		route:  "/app/installations/{installation_id}",
		path:   "/app/installations/" + strconv.FormatInt(id, 10),
		auth:   a.jwtAuth(),
	}, &inst)
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// ListInstallations returns every installation of the App.
func (a *App) ListInstallations(ctx context.Context) ([]Installation, error) {
	return getAll[Installation](ctx, a.t, request{
		method: http.MethodGet,
		route:  "/app/installations",
		path:   "/app/installations",
		auth:   a.jwtAuth(),
	}, "", 0)
}

// InstallationRepos returns the repositories an installation can access.
func (a *App) InstallationRepos(ctx context.Context, id int64) ([]Repository, error) {
	return getAll[Repository](ctx, a.t, request{
		method: http.MethodGet,
		route:  "/installation/repositories",
		path:   "/installation/repositories",
		auth:   installationAuth{app: a, id: id},
	}, "repositories", 0)
}

// ParsePrivateKey decodes the App's PEM private key, PKCS#1 or PKCS#8. It
// also accepts the PEM with literal "\n" sequences, as secrets stores often
// hold it, and base64 of the PEM.
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	text := strings.TrimSpace(string(data))
	if !strings.Contains(text, "-----BEGIN") {
		if decoded, err := base64.StdEncoding.DecodeString(text); err == nil {
			text = strings.TrimSpace(string(decoded))
		}
	}
	if !strings.Contains(text, "\n") && strings.Contains(text, `\n`) {
		text = strings.ReplaceAll(text, `\n`, "\n")
	}
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errors.New("gh: private key: no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("gh: private key: %w", err)
		}
		return key, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("gh: private key: %w", err)
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("gh: private key: %T is not an RSA key", parsed)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("gh: private key: unexpected PEM block %q", block.Type)
	}
}
