package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

type authMode uint8

const (
	viaOIDC authMode = 1 << iota
	viaAPIKey
	viaSession
	sameOrigin
	adminKey
)

const (
	runnerAuth = viaOIDC | viaAPIKey
	humanAuth  = viaAPIKey | viaSession
	anyAuth    = viaOIDC | viaAPIKey | viaSession
)

type identity struct {
	principal.Principal
	session *store.Session
	scope   *keyScope
}

// keyScope is the resolved installation scope of an installation-scoped API
// key. account is the installation's current account, through which the key
// reuses the session visibility machinery; known is false when the
// installation is no longer installed, in which case the key sees nothing.
type keyScope struct {
	account string
	known   bool
}

// noAccount is the scope account of an installation-scoped key whose
// installation is gone: non-empty, so it is not read as "every account", and
// equal to no real GitHub account, so it matches nothing.
const noAccount = "\x00"

// isAdmin reports whether the caller may manage API keys: an unscoped,
// read-write, installation-wide API key. A scoped key is never an
// administrator. This is the single chokepoint the adminKey mode enforces.
func (id identity) isAdmin() bool {
	return id.Kind == principal.APIKey && !id.ReadOnly && id.Installation == 0
}

// readOnly reports whether the caller is a read-only API key, which the
// with() middleware refuses every mutating route.
func (id identity) readOnly() bool {
	return id.Kind == principal.APIKey && id.ReadOnly
}

func (id identity) sees(account string) bool {
	switch id.Kind {
	case principal.APIKey:
		if id.scope == nil {
			return true
		}
		return id.scope.known && strings.EqualFold(id.scope.account, account)
	case principal.Session:
		for _, a := range id.accounts() {
			if strings.EqualFold(a, account) {
				return true
			}
		}
	}
	return false
}

func (id identity) accounts() []string {
	switch id.Kind {
	case principal.APIKey:
		if id.scope == nil {
			return nil
		}
		if !id.scope.known {
			return []string{noAccount}
		}
		return []string{id.scope.account}
	case principal.Session:
		if id.session == nil {
			return nil
		}
		return append([]string{id.session.Login}, id.session.Orgs...)
	}
	return nil
}

type handler func(w http.ResponseWriter, r *http.Request, id identity) error

func (s *server) with(mode authMode, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.authenticate(r, mode)
		if err != nil {
			viaCookie := mode&viaSession != 0 && len(r.Header.Values("Authorization")) == 0
			if _, cerr := r.Cookie(sessionCookie); cerr == nil && viaCookie && statusOf(err) == http.StatusUnauthorized {
				s.clearCookie(w, sessionCookie, "/")
			}
			s.writeError(w, r, err)
			return
		}
		infoOf(r).kind = id.Kind
		if id.Kind == principal.Session && mode&sameOrigin != 0 {
			if err := s.checkSameOrigin(r); err != nil {
				s.writeError(w, r, err)
				return
			}
		}
		if id.readOnly() && !safeMethod(r.Method) {
			s.writeError(w, r, forbidden("this API key is read-only and cannot make "+r.Method+" requests"))
			return
		}
		if mode&adminKey != 0 && !id.isAdmin() {
			s.writeError(w, r, forbidden("managing API keys requires an administrator API key"))
			return
		}
		if err := h(w, r, id); err != nil {
			s.writeError(w, r, err)
		}
	})
}

func statusOf(err error) int {
	e, _ := toAPIError(err)
	return e.status
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func (s *server) authenticate(r *http.Request, mode authMode) (identity, error) {
	ctx := r.Context()
	if len(r.Header.Values("Authorization")) > 0 {
		raw, ok := oidc.BearerToken(r)
		if !ok {
			return identity{}, unauthorized("the Authorization header must carry a single Bearer token")
		}
		if strings.HasPrefix(raw, store.APIKeyPrefix) {
			if mode&viaAPIKey == 0 {
				return identity{}, unauthorized("API keys are not accepted on this endpoint")
			}
			return s.apiKeyIdentity(ctx, raw)
		}
		if mode&viaOIDC == 0 {
			return identity{}, unauthorized("runner tokens are not accepted on this endpoint; sign in or use an API key")
		}
		return s.runnerIdentity(ctx, raw)
	}
	if mode&viaSession != 0 {
		if c, err := r.Cookie(sessionCookie); err == nil {
			return s.sessionIdentity(ctx, c.Value)
		}
	}
	switch {
	case mode&viaSession == 0:
		return identity{}, unauthorized("a runner OIDC token or an API key is required")
	case mode&viaOIDC != 0:
		return identity{}, unauthorized("a runner OIDC token, an API key or a session is required")
	default:
		return identity{}, unauthorized("sign in or use an API key")
	}
}

func (s *server) apiKeyIdentity(ctx context.Context, raw string) (identity, error) {
	key, err := s.db.VerifyAPIKey(ctx, raw)
	if errors.Is(err, store.ErrNotFound) {
		return identity{}, unauthorized("the API key is unknown, revoked or expired")
	}
	if err != nil {
		return identity{}, fmt.Errorf("verify api key: %w", err)
	}
	id := identity{Principal: principal.Principal{
		Kind:     principal.APIKey,
		Login:    key.Name,
		APIKeyID: key.ID.String(),
		ReadOnly: key.ReadOnly,
	}}
	if key.InstallationID != nil {
		id.Installation = *key.InstallationID
		inst, err := s.db.GetInstallation(ctx, *key.InstallationID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			id.scope = &keyScope{}
		case err != nil:
			return identity{}, fmt.Errorf("look up api key installation: %w", err)
		default:
			id.scope = &keyScope{account: inst.Account, known: true}
		}
	}
	return id, nil
}

func (s *server) runnerIdentity(ctx context.Context, raw string) (identity, error) {
	if s.verifier == nil {
		return identity{}, unavailable("runner tokens cannot be verified: no OIDC verifier is configured")
	}
	claims, err := s.verifier.VerifyOnce(ctx, raw, s.db)
	if err != nil {
		return identity{}, fmt.Errorf("verify runner token: %w", err)
	}
	repo, err := s.db.GetRepoByName(ctx, claims.Repository)
	if errors.Is(err, store.ErrNotFound) {
		return identity{}, forbidden(fmt.Sprintf("repository %q is not installed on this server", claims.Repository))
	}
	if err != nil {
		return identity{}, fmt.Errorf("look up runner repository: %w", err)
	}
	if claims.RepositoryID != strconv.FormatInt(repo.ID, 10) {
		return identity{}, forbidden(fmt.Sprintf("repository_id %q is not the id of %s", claims.RepositoryID, repo.FullName))
	}
	if repo.Suspended {
		return identity{}, forbidden("the App installation of " + repo.FullName + " is suspended")
	}
	if err := oidc.BindWorkflowRef(claims, s.cfg.RequiredWorkflowRef); err != nil {
		return identity{}, fmt.Errorf("runner workflow: %w", err)
	}
	return identity{Principal: principal.Principal{Kind: principal.OIDC, Login: claims.Actor, Claims: claims}}, nil
}

func (s *server) sessionIdentity(ctx context.Context, value string) (identity, error) {
	token, ok := s.openSessionCookie(value)
	if !ok {
		return identity{}, unauthorized("the session cookie is invalid; sign in again")
	}
	sess, err := s.db.GetSession(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return identity{}, unauthorized("the session has expired; sign in again")
	}
	if err != nil {
		return identity{}, fmt.Errorf("load session: %w", err)
	}
	return identity{Principal: principal.Principal{Kind: principal.Session, Login: sess.Login}, session: &sess}, nil
}

func (s *server) checkSameOrigin(r *http.Request) error {
	if origin := r.Header.Get("Origin"); origin != "" {
		if strings.EqualFold(origin, s.origin) {
			return nil
		}
		return forbidden("cross-origin request refused: Origin " + origin + " is not " + s.origin)
	}
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "same-origin":
		return nil
	case "":
		return forbidden("cross-origin request refused: a browser request must carry Origin or Sec-Fetch-Site")
	default:
		return forbidden("cross-origin request refused: Sec-Fetch-Site is " + site)
	}
}
