package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	oauthCookie   = "stackorder_oauth"
	oauthPurpose  = "oauth"
	oauthStateTTL = 10 * time.Minute
	oauthScope    = "read:org"
)

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	if strings.ContainsFunc(next, func(r rune) bool { return r < ' ' || r == 0x7f || r == '\\' }) {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	if u.Path == "/auth" || strings.HasPrefix(u.Path, "/auth/") {
		return "/"
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func (s *server) oauthConfig() gh.OAuthConfig {
	return gh.OAuthConfig{
		ClientID:     s.cfg.OAuthClientID,
		ClientSecret: s.cfg.OAuthClientSecret,
		BaseWebURL:   s.webURL,
		RedirectURL:  s.cfg.BaseURL + "/auth/callback",
		HTTPClient:   s.hc,
	}
}

func (s *server) signInLinks() []pageLink {
	return []pageLink{{Href: "/auth/login", Text: "Sign in again"}}
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OAuthClientID == "" || s.cfg.OAuthClientSecret == "" {
		s.renderMessage(w, r, http.StatusServiceUnavailable, "Sign-in unavailable", message{
			Heading: "GitHub sign-in is not configured",
			Lines:   []string{"Set GITHUB_OAUTH_CLIENT_ID and GITHUB_OAUTH_CLIENT_SECRET to the App's OAuth credentials and restart the server."},
		})
		return
	}
	state := randomToken()
	s.setCookie(w, oauthCookie, s.seal(oauthPurpose, sealed{
		Value:   state,
		Next:    safeNext(r.URL.Query().Get("next")),
		Expires: s.now().Add(oauthStateTTL).Unix(),
	}), "/auth", oauthStateTTL)
	target := s.oauthConfig().AuthorizeURL(state) + "&scope=" + url.QueryEscape(oauthScope)
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *server) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	c, err := r.Cookie(oauthCookie)
	var st sealed
	ok := err == nil
	if ok {
		st, ok = s.unseal(oauthPurpose, c.Value)
	}
	s.clearCookie(w, oauthCookie, "/auth")
	if !ok || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.Value)) != 1 {
		s.renderMessage(w, r, http.StatusBadRequest, "Sign-in failed", message{
			Heading: "This sign-in attempt cannot be completed",
			Lines:   []string{"It expired, was already used, or was not started from this browser. Start again."},
			Links:   s.signInLinks(),
		})
		return
	}
	if e := q.Get("error"); e != "" {
		reason := q.Get("error_description")
		if reason == "" {
			reason = e
		}
		s.renderMessage(w, r, http.StatusForbidden, "Sign-in cancelled", message{
			Heading: "GitHub did not authorize the sign-in",
			Lines:   []string{reason},
			Links:   s.signInLinks(),
		})
		return
	}
	token, err := gh.ExchangeOAuthCode(ctx, s.oauthConfig(), q.Get("code"))
	if err != nil {
		var oe *gh.OAuthError
		if errors.As(err, &oe) || q.Get("code") == "" {
			s.renderMessage(w, r, http.StatusBadRequest, "Sign-in failed", message{
				Heading: "GitHub refused the sign-in code",
				Lines:   []string{"The code expired or was already used. Start again."},
				Links:   s.signInLinks(),
			})
			return
		}
		s.githubUnavailable(w, r, "exchange oauth code", err)
		return
	}
	client, err := gh.NewTokenClient(gh.Config{BaseURL: s.apiURL, HTTPClient: s.hc, MaxAttempts: 3}, token)
	if err != nil {
		s.githubUnavailable(w, r, "user client", err)
		return
	}
	user, err := client.AuthenticatedUser(ctx)
	if err != nil {
		s.githubUnavailable(w, r, "get authenticated user", err)
		return
	}
	orgs, err := client.UserOrgs(ctx)
	if err != nil {
		s.githubUnavailable(w, r, "list user organisations", err)
		return
	}
	slices.Sort(orgs)
	orgs = slices.Compact(orgs)

	installs, err := s.db.ListInstallations(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "sign-in failed", "request_id", requestIDOf(r), "error", err.Error())
		s.renderMessage(w, r, http.StatusServiceUnavailable, "Sign-in failed", message{
			Heading: "Sign-in is temporarily unavailable",
			Lines:   []string{"The server could not read its installations. Try again in a moment."},
			Links:   s.signInLinks(),
		})
		return
	}
	probe := identity{
		Principal: principal.Principal{Kind: principal.Session, Login: user.Login},
		session:   &store.Session{Login: user.Login, Orgs: orgs},
	}
	installed := slices.ContainsFunc(installs, func(in store.Installation) bool {
		return in.SuspendedAt == nil && probe.sees(in.Account)
	})
	if !installed {
		s.notInstalled(w, r, user.Login, orgs)
		return
	}

	sessionToken, _, err := s.db.CreateSession(ctx, store.NewSession{
		Login:     user.Login,
		UserID:    user.ID,
		AvatarURL: user.AvatarURL,
		Orgs:      orgs,
		TTL:       s.cfg.SessionTTL,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "create session", "request_id", requestIDOf(r), "error", err.Error())
		s.renderMessage(w, r, http.StatusServiceUnavailable, "Sign-in failed", message{
			Heading: "Sign-in is temporarily unavailable",
			Lines:   []string{"The session could not be stored. Try again in a moment."},
			Links:   s.signInLinks(),
		})
		return
	}
	infoOf(r).kind = principal.Session
	s.setCookie(w, sessionCookie, s.sessionCookieValue(sessionToken), "/", s.cfg.SessionTTL)
	http.Redirect(w, r, st.Next, http.StatusFound)
}

func (s *server) githubUnavailable(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.log.ErrorContext(r.Context(), "sign-in failed", "request_id", requestIDOf(r), "op", op, "error", err.Error())
	s.renderMessage(w, r, http.StatusBadGateway, "Sign-in failed", message{
		Heading: "GitHub could not be reached",
		Lines:   []string{"The sign-in could not be completed because GitHub did not answer. Try again in a moment."},
		Links:   s.signInLinks(),
	})
}

func (s *server) notInstalled(w http.ResponseWriter, r *http.Request, login string, orgs []string) {
	m := message{
		Heading: "Stackorder is not installed for your organisations",
		Lines: []string{
			"You are signed in to GitHub as " + login + ", but the Stackorder App is not installed on your account or on any organisation you belong to, so there is nothing to show you.",
			"Ask an organisation owner to install the App, or, if your organisation restricts OAuth access, to grant this App access to it. The organisations GitHub reported for you are:",
		},
		Items: orgs,
		Links: s.signInLinks(),
	}
	if len(orgs) == 0 {
		m.Lines[1] = "GitHub reported no organisations for you. If you belong to one, ask its owner to install the App or to grant it access."
	}
	if slug := s.appSlug(r); slug != "" {
		m.Links = append([]pageLink{{Href: s.installURL(slug), Text: "Install the App"}}, m.Links...)
	}
	s.renderMessage(w, r, http.StatusForbidden, "Not installed", m)
}

func (s *server) installURL(slug string) string {
	return s.webURL + "/apps/" + url.PathEscape(slug) + "/installations/new"
}

func (s *server) appSlug(r *http.Request) string {
	s.slugMu.Lock()
	slug := s.slug
	s.slugMu.Unlock()
	if slug != "" || s.app == nil {
		return slug
	}
	info, err := s.app.AppInfo(r.Context())
	if err != nil {
		s.log.WarnContext(r.Context(), "look up app slug", "request_id", requestIDOf(r), "error", err.Error())
		return ""
	}
	s.slugMu.Lock()
	s.slug = info.Slug
	s.slugMu.Unlock()
	return info.Slug
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.checkSameOrigin(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if token, ok := s.openSessionCookie(c.Value); ok {
			infoOf(r).kind = principal.Session
			if err := s.db.DeleteSession(r.Context(), token); err != nil {
				s.writeError(w, r, err)
				return
			}
		}
	}
	s.clearCookie(w, sessionCookie, "/")
	w.WriteHeader(http.StatusNoContent)
}
