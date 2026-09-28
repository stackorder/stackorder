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
)

const (
	runnerAuth = viaOIDC | viaAPIKey
	humanAuth  = viaAPIKey | viaSession
	anyAuth    = viaOIDC | viaAPIKey | viaSession
)

type identity struct {
	principal.Principal
	session *store.Session
}

func (id identity) sees(account string) bool {
	switch id.Kind {
	case principal.APIKey:
		return true
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
	if id.Kind != principal.Session || id.session == nil {
		return nil
	}
	return append([]string{id.session.Login}, id.session.Orgs...)
}

type handler func(w http.ResponseWriter, r *http.Request, id identity) error

func (s *server) with(mode authMode, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.authenticate(r, mode)
		if err != nil {
			if _, cerr := r.Cookie(sessionCookie); cerr == nil && mode&viaSession != 0 && statusOf(err) == http.StatusUnauthorized {
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
		if err := h(w, r, id); err != nil {
			s.writeError(w, r, err)
		}
	})
}

func statusOf(err error) int {
	e, _ := toAPIError(err)
	return e.status
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
		return identity{}, unauthorized("the API key is unknown or revoked")
	}
	if err != nil {
		return identity{}, fmt.Errorf("verify api key: %w", err)
	}
	return identity{Principal: principal.Principal{Kind: principal.APIKey, Login: key.Name, APIKeyID: key.ID.String()}}, nil
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
