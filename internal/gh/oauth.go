package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthConfig identifies the App's OAuth client for the user sign-in flow.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	// BaseWebURL is the web root, DefaultWebURL when empty; GitHub
	// Enterprise Server uses https://HOST.
	BaseWebURL string
	// RedirectURL must match the redirect_uri of the authorize request
	// when one was sent.
	RedirectURL string
	// HTTPClient sends the request; a client with a 30 s timeout when nil.
	HTTPClient *http.Client
}

// OAuthError is an error answer from GitHub's token endpoint, such as
// bad_verification_code.
type OAuthError struct {
	Code        string
	Description string
}

// Error implements the error interface.
func (e *OAuthError) Error() string {
	if e.Description == "" {
		return "gh: oauth: " + e.Code
	}
	return "gh: oauth: " + e.Code + ": " + e.Description
}

// AuthorizeURL returns the URL that starts the user sign-in flow.
func (cfg OAuthConfig) AuthorizeURL(state string) string {
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("state", state)
	if cfg.RedirectURL != "" {
		q.Set("redirect_uri", cfg.RedirectURL)
	}
	return webURL(cfg.BaseWebURL) + "/login/oauth/authorize?" + q.Encode()
}

func webURL(base string) string {
	if base == "" {
		base = DefaultWebURL
	}
	return strings.TrimRight(base, "/")
}

// ExchangeOAuthCode trades the code from the sign-in callback for a user
// access token. It is never retried, because a code can be used only once.
func ExchangeOAuthCode(ctx context.Context, cfg OAuthConfig, code string) (string, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return "", errors.New("gh: oauth: client id and secret are required")
	}
	if code == "" {
		return "", errors.New("gh: oauth: code is required")
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("code", code)
	if cfg.RedirectURL != "" {
		form.Set("redirect_uri", cfg.RedirectURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webURL(cfg.BaseWebURL)+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("gh: oauth: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "stackorder")
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("gh: oauth: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("gh: oauth: read response: %w", err)
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("gh: oauth: status %d: decode response: %w", resp.StatusCode, err)
	}
	if out.Error != "" {
		return "", &OAuthError{Code: out.Error, Description: out.ErrorDescription}
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return "", fmt.Errorf("gh: oauth: status %d without an access token", resp.StatusCode)
	}
	return out.AccessToken, nil
}
