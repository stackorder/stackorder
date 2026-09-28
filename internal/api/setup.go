package api

import (
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
	setupCookie   = "stackorder_setup"
	setupPurpose  = "setup"
	setupStateTTL = time.Hour
	maxAppName    = 34
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

func (s *server) setup(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !s.cfg.SetupMode && q.Get("force") != "1" {
		s.renderMessage(w, r, http.StatusOK, "Already configured", message{
			Heading: "This server already has a GitHub App",
			Lines: []string{
				"It was started with GitHub App credentials, so there is nothing to set up.",
				"Creating another App is only useful to replace the current one, for instance after moving the server to a new URL; the new App's credentials then replace the configured ones.",
			},
			Links: []pageLink{{Href: "/", Text: "Open Stackorder"}, {Href: "/setup?force=1", Text: "Create another App anyway"}},
		})
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
	creds, err := gh.CreateAppFromManifest(r.Context(), gh.Config{BaseURL: s.apiURL, HTTPClient: s.hc}, code)
	if err != nil {
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
