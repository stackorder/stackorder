package gh

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

var (
	// DefaultEvents are the webhook events the App subscribes to. GitHub
	// delivers installation and installation_repositories to every App
	// without a subscription, so they are not listed.
	DefaultEvents = []string{
		EventPullRequest,
		EventPullRequestReview,
		EventIssueComment,
		EventPush,
		EventCheckRun,
		EventCheckSuite,
		EventWorkflowRun,
		EventWorkflowJob,
		EventDeploymentProtectionRule,
	}
	// DefaultPermissions is the App's permission set from the design.
	DefaultPermissions = map[string]string{
		"metadata":      "read",
		"contents":      "read",
		"pull_requests": "write",
		"checks":        "write",
		"actions":       "write",
		"issues":        "write",
		"members":       "read",
		"deployments":   "write",
	}
)

// AppCredentials is what GitHub returns once for an App created from a
// manifest.
type AppCredentials struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name,omitempty"`
	Owner         User   `json:"owner,omitzero"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	HTMLURL       string `json:"html_url"`
}

// Manifest returns the GitHub App manifest for a server at baseURL. With
// ghes set, the optional deployment protection rule event and Deployments
// permission are left out, since not every GitHub Enterprise Server release
// offers custom protection rules and an unknown event fails the whole
// registration; they can be added later in the App settings.
func Manifest(baseURL, name string, ghes bool) map[string]any {
	base := strings.TrimRight(baseURL, "/")
	perms := map[string]any{}
	for k, v := range DefaultPermissions {
		if ghes && k == "deployments" {
			continue
		}
		perms[k] = v
	}
	events := make([]string, 0, len(DefaultEvents))
	for _, e := range DefaultEvents {
		if ghes && e == EventDeploymentProtectionRule {
			continue
		}
		events = append(events, e)
	}
	return map[string]any{
		"name":        name,
		"url":         base,
		"description": "Stackorder orders Terraform and OpenTofu stacks by dependency and runs them on GitHub Actions.",
		"hook_attributes": map[string]any{
			"url":    base + "/webhooks/github",
			"active": true,
		},
		"redirect_url":             base + "/setup/callback",
		"callback_urls":            []string{base + "/auth/callback"},
		"setup_url":                base + "/setup/installed",
		"setup_on_update":          false,
		"request_oauth_on_install": false,
		"public":                   false,
		"default_permissions":      perms,
		"default_events":           events,
	}
}

// CreateAppFromManifest completes the manifest flow: it converts the code
// GitHub passed to the redirect URL into the new App's credentials. cfg
// needs only BaseURL and HTTPClient. The call is never retried, because a
// code can be converted only once.
func CreateAppFromManifest(ctx context.Context, cfg Config, code string) (*AppCredentials, error) {
	if code == "" {
		return nil, errors.New("gh: manifest code is required")
	}
	c, err := segment("manifest code", code)
	if err != nil {
		return nil, err
	}
	t, err := newTransport(cfg)
	if err != nil {
		return nil, err
	}
	var creds AppCredentials
	err = t.call(ctx, request{
		method:  http.MethodPost,
		route:   "/app-manifests/{code}/conversions",
		path:    "/app-manifests/" + c + "/conversions",
		noRetry: true,
	}, &creds)
	if err != nil {
		return nil, err
	}
	return &creds, nil
}
