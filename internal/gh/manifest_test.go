package gh_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/testutil/ghfake"
)

func TestManifest(t *testing.T) {
	got := gh.Manifest("https://stackorder.acme.example/", "Stackorder ACME", false)
	data, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"name": "Stackorder ACME",
		"url": "https://stackorder.acme.example",
		"description": "Stackorder orders Terraform and OpenTofu stacks by dependency and runs them on GitHub Actions.",
		"hook_attributes": {"url": "https://stackorder.acme.example/webhooks/github", "active": true},
		"redirect_url": "https://stackorder.acme.example/setup/callback",
		"callback_urls": ["https://stackorder.acme.example/auth/callback"],
		"setup_url": "https://stackorder.acme.example/setup/installed",
		"setup_on_update": false,
		"request_oauth_on_install": false,
		"public": false,
		"default_permissions": {
			"metadata": "read",
			"contents": "read",
			"pull_requests": "write",
			"checks": "write",
			"actions": "write",
			"issues": "write",
			"members": "read",
			"deployments": "write"
		},
		"default_events": [
			"pull_request",
			"pull_request_review",
			"issue_comment",
			"push",
			"check_run",
			"check_suite",
			"workflow_run",
			"workflow_job",
			"deployment_protection_rule"
		]
	}`, string(data))
}

func TestManifestForEnterpriseServer(t *testing.T) {
	got := gh.Manifest("https://stackorder.corp.example", "Stackorder", true)
	perms, ok := got["default_permissions"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, perms, "deployments")
	assert.Equal(t, "write", perms["checks"])
	events, ok := got["default_events"].([]string)
	require.True(t, ok)
	assert.NotContains(t, events, gh.EventDeploymentProtectionRule)
	assert.Contains(t, events, gh.EventWorkflowJob)
	assert.Equal(t, "https://stackorder.corp.example/webhooks/github", got["hook_attributes"].(map[string]any)["url"])
	assert.Len(t, gh.DefaultEvents, 9)
	assert.Len(t, gh.DefaultPermissions, 8)
}

func TestCreateAppFromManifest(t *testing.T) {
	fake := ghfake.New(t)
	want := gh.AppCredentials{
		ID:            385163,
		Slug:          "stackorder-acme",
		Name:          "Stackorder ACME",
		Owner:         gh.User{Login: "acme", Type: "Organization"},
		PEM:           "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----\n",
		WebhookSecret: "e340154128314309424b7c8e90325147d99fdafa",
		ClientID:      "Iv1.8a61f9b3a7aba766",
		ClientSecret:  "1726be1638095a19edd134c77bde3aa2ece1e5d8",
		HTMLURL:       "https://github.com/apps/stackorder-acme",
	}
	fake.SetManifestConversion("code-1", want)
	cfg := gh.Config{BaseURL: fake.URL(), HTTPClient: fake.HTTPClient()}
	ctx := context.Background()

	got, err := gh.CreateAppFromManifest(ctx, cfg, "code-1")
	require.NoError(t, err)
	assert.Equal(t, want, *got)

	_, err = gh.CreateAppFromManifest(ctx, cfg, "code-1")
	require.ErrorIs(t, err, gh.ErrNotFound)

	_, err = gh.CreateAppFromManifest(ctx, cfg, "")
	require.Error(t, err)

	_, err = gh.CreateAppFromManifest(ctx, gh.Config{BaseURL: "::"}, "c")
	require.Error(t, err)

	fake.SetManifestConversion("code-2", want)
	fake.FailNext("POST /app-manifests/{code}/conversions", 502, 1)
	_, err = gh.CreateAppFromManifest(ctx, cfg, "code-2")
	var apiErr *gh.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 502, apiErr.Status)
	attempts := 0
	for _, r := range fake.Requests() {
		assert.Empty(t, r.Header.Get("Authorization"))
		if r.Path == "/app-manifests/code-2/conversions" {
			attempts++
		}
	}
	assert.Equal(t, 1, attempts)
}

func TestCreateAppFromManifestNetworkErrorOmitsCode(t *testing.T) {
	fake := ghfake.New(t)
	const code = "single-use-manifest-code"
	fake.SetManifestConversion(code, gh.AppCredentials{ID: 1})
	fake.FailNext("POST /app-manifests/{code}/conversions", 0, 1)

	_, err := gh.CreateAppFromManifest(context.Background(), gh.Config{BaseURL: fake.URL(), HTTPClient: fake.HTTPClient()}, code)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /app-manifests/{code}/conversions")
	assert.NotContains(t, err.Error(), code)
}
