package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stackorder/stackorder/internal/gh"
)

const (
	setupCookie      = "stackorder_setup"
	setupPurpose     = "setup"
	setupStateTTL    = time.Hour
	setupAuthCookie  = "stackorder_setup_auth"
	setupAuthPurpose = "setup-auth"
	setupAuthTTL     = time.Hour
	maxAppName       = 34
)

var (
	githubLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	appName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*$`)
)

type permission struct {
	Name  string
	Level string
}

type envVar struct {
	Name  string
	Value string
}

type setupPage struct {
	Name        string
	Org         string
	Action      string
	Manifest    string
	OrgHint     string
	WebhookURL  string
	CallbackURL string
	Permissions []permission
	Events      []string
}

type setupDonePage struct {
	Name       string
	Env        []envVar
	PrivateKey string
	InstallURL string
	NextSteps  []string
}

func (s *server) ghes() bool {
	return !strings.EqualFold(s.webURL, gh.DefaultWebURL)
}

func (s *server) webOrigin() string {
	u, err := url.Parse(s.webURL)
	if err != nil || u.Host == "" {
		return "'none'"
	}
	return u.Scheme + "://" + u.Host
}

func (s *server) defaultAppName() string {
	host := s.origin[strings.Index(s.origin, "://")+3:]
	name := "stackorder-" + strings.NewReplacer(".", "-", ":", "-").Replace(host)
	if len(name) > maxAppName {
		name = name[:maxAppName]
	}
	return strings.TrimRight(name, "-")
}

func (s *server) resetupClosed(w http.ResponseWriter, r *http.Request) {
	s.renderMessage(w, r, http.StatusNotFound, "Not found", message{
		Heading: "Setup is closed",
		Lines:   []string{"This server already has a GitHub App, and creating another one is disabled."},
		Links:   []pageLink{{Href: "/", Text: "Open Stackorder"}},
	})
}

func (s *server) setupTokenMatches(presented string) bool {
	if s.cfg.SetupToken == "" || s.setupUsed.Load() {
		return false
	}
	got, want := sha256.Sum256([]byte(presented)), sha256.Sum256([]byte(s.cfg.SetupToken))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func (s *server) setupTokenProof() string {
	return s.sign("setup-token-proof\x00" + s.cfg.SetupToken)
}

func (s *server) hasSetupProof(r *http.Request) bool {
	if s.cfg.SetupToken == "" || s.setupUsed.Load() {
		return false
	}
	c, err := r.Cookie(setupAuthCookie)
	if err != nil {
		return false
	}
	st, ok := s.unseal(setupAuthPurpose, c.Value)
	return ok && subtle.ConstantTimeCompare([]byte(st.Value), []byte(s.setupTokenProof())) == 1
}

func (s *server) setupTokenRequired(w http.ResponseWriter, r *http.Request) {
	setupURL := "/setup?token="
	if !s.cfg.SetupMode {
		setupURL = "/setup?force=1&token="
	}
	s.renderMessage(w, r, http.StatusForbidden, "Setup token required", message{
		Heading: "Setup needs the setup token",
		Lines: []string{
			"Creating the GitHub App needs the one-time setup token the server prints in its log when it starts, so that nobody else who reaches this page can register the App under their own account.",
			"Find the log line with setup_url, with docker compose logs or in the server's CloudWatch log group, and open that URL in this browser. If you set STACKORDER_SETUP_TOKEN, open " + setupURL + " followed by its value.",
			"The token works until an App is created. A generated token changes each time the server starts.",
		},
	})
}

func (s *server) setupAlreadyUsed(w http.ResponseWriter, r *http.Request, status int) {
	s.clearCookie(w, setupCookie, "/setup")
	s.clearCookie(w, setupAuthCookie, "/setup")
	s.renderMessage(w, r, status, "Already configured", message{
		Heading: "This server already has a GitHub App",
		Lines: []string{
			"A GitHub App was created from this page since the server started, so its setup token no longer opens setup.",
			"Restart the server with the App's credentials. If they were lost, delete the App in its GitHub settings and restart the server: it prints a new setup token.",
		},
	})
}

func (s *server) acceptSetupToken(w http.ResponseWriter, r *http.Request, q url.Values) {
	if !s.setupTokenMatches(q.Get("token")) {
		s.setupTokenRequired(w, r)
		return
	}
	s.setCookie(w, setupAuthCookie, s.seal(setupAuthPurpose, sealed{Value: s.setupTokenProof(), Expires: s.now().Add(setupAuthTTL).Unix()}), "/setup", setupAuthTTL)
	q.Del("token")
	target := "/setup"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // always the local path /setup; only the query comes from the request
}

func (s *server) setup(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !s.cfg.SetupMode && q.Get("force") != "1" {
		m := message{
			Heading: "This server already has a GitHub App",
			Lines:   []string{"It was started with GitHub App credentials, so there is nothing to set up."},
			Links:   []pageLink{{Href: "/", Text: "Open Stackorder"}},
		}
		switch {
		case s.cfg.AllowResetup && s.setupUsed.Load():
			m.Lines = append(m.Lines, "Another App was created from this page since the server started; restart the server with its credentials.")
		case s.cfg.AllowResetup:
			m.Lines = append(m.Lines, "Creating another App is only useful to replace the current one, for instance after moving the server to a new URL; the new App's credentials then replace the configured ones. It needs the setup token the server prints in its log when it starts.")
			m.Links = append(m.Links, pageLink{Href: "/setup?force=1", Text: "Create another App anyway"})
		}
		s.renderMessage(w, r, http.StatusOK, "Already configured", m)
		return
	}
	if !s.cfg.SetupMode && !s.cfg.AllowResetup {
		s.resetupClosed(w, r)
		return
	}
	if s.setupUsed.Load() {
		s.setupAlreadyUsed(w, r, http.StatusOK)
		return
	}
	if q.Has("token") {
		s.acceptSetupToken(w, r, q)
		return
	}
	if !s.hasSetupProof(r) {
		s.setupTokenRequired(w, r)
		return
	}
	org := q.Get("org")
	if org != "" && !githubLogin.MatchString(org) {
		s.renderMessage(w, r, http.StatusBadRequest, "Setup", message{
			Heading: "That is not a GitHub organisation name",
			Lines:   []string{"Organisation names are up to 39 letters, digits and dashes, starting with a letter or digit."},
			Links:   []pageLink{{Href: "/setup", Text: "Create the App on your personal account"}},
		})
		return
	}
	name := q.Get("name")
	if name == "" {
		name = s.defaultAppName()
	}
	if len(name) > maxAppName || !appName.MatchString(name) {
		s.renderMessage(w, r, http.StatusBadRequest, "Setup", message{
			Heading: "That App name is not allowed",
			Lines:   []string{"GitHub App names are at most 34 letters, digits, spaces, dots, dashes and underscores."},
			Links:   []pageLink{{Href: "/setup", Text: "Use the default name"}},
		})
		return
	}

	manifest := gh.Manifest(s.cfg.BaseURL, name, s.ghes())
	data, err := json.Marshal(manifest)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	nonce := randomToken()
	s.setCookie(w, setupCookie, s.seal(setupPurpose, sealed{Value: nonce, Expires: s.now().Add(setupStateTTL).Unix()}), "/setup", setupStateTTL)

	action := s.webURL + "/settings/apps/new"
	if org != "" {
		action = s.webURL + "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	page := setupPage{
		Name:        name,
		Org:         org,
		Action:      action + "?state=" + url.QueryEscape(nonce),
		Manifest:    string(data),
		OrgHint:     s.cfg.BaseURL + "/setup?org=<organisation>",
		WebhookURL:  s.cfg.BaseURL + "/webhooks/github",
		CallbackURL: s.cfg.BaseURL + "/auth/callback",
	}
	perms, _ := manifest["default_permissions"].(map[string]any)
	for k, v := range perms {
		level, _ := v.(string)
		page.Permissions = append(page.Permissions, permission{Name: k, Level: level})
	}
	slices.SortFunc(page.Permissions, func(a, b permission) int { return strings.Compare(a.Name, b.Name) })
	page.Events, _ = manifest["default_events"].([]string)
	s.renderPage(w, r, http.StatusOK, "setup", "Create the GitHub App", s.webOrigin(), page)
}

func (s *server) setupCallback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.SetupMode && !s.cfg.AllowResetup {
		s.resetupClosed(w, r)
		return
	}
	if s.setupUsed.Load() {
		s.setupAlreadyUsed(w, r, http.StatusConflict)
		return
	}
	q := r.URL.Query()
	c, err := r.Cookie(setupCookie)
	var st sealed
	ok := err == nil
	if ok {
		st, ok = s.unseal(setupPurpose, c.Value)
	}
	restart := []pageLink{{Href: "/setup?force=1", Text: "Start again"}}
	if !ok || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.Value)) != 1 {
		s.clearCookie(w, setupCookie, "/setup")
		s.renderMessage(w, r, http.StatusBadRequest, "Setup failed", message{
			Heading: "This App creation cannot be completed here",
			Lines:   []string{"It expired, was already completed, or was started from another browser. Start again from /setup."},
			Links:   restart,
		})
		return
	}
	code := q.Get("code")
	if code == "" {
		s.clearCookie(w, setupCookie, "/setup")
		s.renderMessage(w, r, http.StatusBadRequest, "Setup failed", message{
			Heading: "GitHub did not send a code",
			Lines:   []string{"The App was not created. Start again from /setup."},
			Links:   restart,
		})
		return
	}
	if !s.setupUsed.CompareAndSwap(false, true) {
		s.setupAlreadyUsed(w, r, http.StatusConflict)
		return
	}
	creds, err := gh.CreateAppFromManifest(r.Context(), gh.Config{BaseURL: s.apiURL, HTTPClient: s.hc}, code)
	if err != nil {
		s.setupUsed.Store(false)
		if errors.Is(err, gh.ErrNotFound) {
			s.clearCookie(w, setupCookie, "/setup")
			s.renderMessage(w, r, http.StatusBadRequest, "Setup failed", message{
				Heading: "GitHub no longer accepts this code",
				Lines:   []string{"A manifest code works once and only for an hour. If the App was created, its credentials were already shown; otherwise start again."},
				Links:   restart,
			})
			return
		}
		s.log.ErrorContext(r.Context(), "convert app manifest", "request_id", requestIDOf(r), "error", err.Error())
		s.renderMessage(w, r, http.StatusBadGateway, "Setup failed", message{
			Heading: "GitHub could not be reached",
			Lines:   []string{"The App's credentials could not be fetched. Reload this page to try the same code again."},
		})
		return
	}
	s.clearCookie(w, setupCookie, "/setup")
	s.clearCookie(w, setupAuthCookie, "/setup")
	s.log.InfoContext(r.Context(), "github app created from manifest", "request_id", requestIDOf(r), "app_id", creds.ID, "slug", creds.Slug)

	name := creds.Name
	if name == "" {
		name = creds.Slug
	}
	page := setupDonePage{
		Name: name,
		Env: []envVar{
			{"GITHUB_APP_ID", strconv.FormatInt(creds.ID, 10)},
			{"GITHUB_WEBHOOK_SECRET", creds.WebhookSecret},
			{"GITHUB_OAUTH_CLIENT_ID", creds.ClientID},
			{"GITHUB_OAUTH_CLIENT_SECRET", creds.ClientSecret},
		},
		PrivateKey: "GITHUB_APP_PRIVATE_KEY=\n" + strings.TrimSpace(creds.PEM),
		InstallURL: s.installURL(creds.Slug),
	}
	if s.ghes() {
		page.Env = append(page.Env, envVar{"GITHUB_API_URL", s.apiURL}, envVar{"GITHUB_WEB_URL", s.webURL})
	}
	audience := s.cfg.OIDCAudience
	if audience == "" {
		audience = s.cfg.BaseURL
	}
	page.NextSteps = []string{
		"Add .github/workflows/stackorder-plan.yml and .github/workflows/stackorder-run.yml to each repository with stacks, calling the reusable plan.yml and run.yml of stackorder/actions with server-url " + s.cfg.BaseURL + ".",
		"Runner jobs authenticate with their GitHub OIDC token requested for the audience " + audience + "; no secret is shared with the runners.",
		"Create the AWS plan role and one apply role per environment, trusting token.actions.githubusercontent.com with sub pinned to the repository and, for applies, the environment.",
		"Protect the default branch by requiring the stackorder/plan and stackorder/apply checks.",
		"Sign in at " + s.cfg.BaseURL + " with GitHub to see the repositories of the organisations the App is installed on.",
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex")
	s.renderPage(w, r, http.StatusOK, "setup_done", "App created", "", page)
}

func (s *server) setupInstalled(w http.ResponseWriter, r *http.Request) {
	m := message{Heading: "The App is installed", Links: []pageLink{{Href: "/auth/login", Text: "Sign in"}, {Href: "/", Text: "Open Stackorder"}}}
	if id, err := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64); err == nil && id > 0 {
		m.Lines = append(m.Lines, "GitHub installed it as installation "+strconv.FormatInt(id, 10)+".")
	}
	if s.cfg.SetupMode {
		m.Lines = append(m.Lines, "This server is still in setup mode: restart it with the App's credentials, then sign in.")
		m.Links = nil
	} else {
		m.Lines = append(m.Lines, "Stackorder receives the installation's repositories through the App's webhook; sign in to see them.")
	}
	s.renderMessage(w, r, http.StatusOK, "Installed", m)
}
