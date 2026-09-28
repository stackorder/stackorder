package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client calls the REST API with an installation token, or with a user
// token for the human sign-in flow. It is safe for concurrent use.
type Client struct {
	t              *transport
	auth           tokenSource
	installationID int64
}

// NewTokenClient returns a client that authenticates with a fixed token,
// typically a user access token from ExchangeOAuthCode. AppID and
// PrivateKey in cfg are ignored.
func NewTokenClient(cfg Config, token string) (*Client, error) {
	if token == "" {
		return nil, errors.New("gh: token is required")
	}
	t, err := newTransport(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{t: t, auth: staticToken(token)}, nil
}

// InstallationID returns the installation the client is scoped to, or 0 for
// a token client.
func (c *Client) InstallationID() int64 { return c.installationID }

func (c *Client) call(ctx context.Context, method, route, path string, body, out any) error {
	return c.t.call(ctx, request{method: method, route: route, path: path, body: body, auth: c.auth}, out)
}

func (c *Client) get(ctx context.Context, route, path string, out any) error {
	return c.call(ctx, http.MethodGet, route, path, nil, out)
}

func list[T any](ctx context.Context, c *Client, route, path string, query url.Values, key string, limit int) ([]T, error) {
	return getAll[T](ctx, c.t, request{method: http.MethodGet, route: route, path: path, query: query, auth: c.auth}, key, limit)
}

func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("gh: repository %q must be owner/name", repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func escapePath(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func itoa(n int) string { return strconv.Itoa(n) }

func i64(n int64) string { return strconv.FormatInt(n, 10) }

// User is a GitHub account: a user, a bot or an organisation.
type User struct {
	Login     string `json:"login"`
	ID        int64  `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	HTMLURL   string `json:"html_url,omitempty"`
}

// Repository is a GitHub repository.
type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name,omitempty"`
	FullName      string `json:"full_name"`
	Owner         User   `json:"owner,omitzero"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
	Archived      bool   `json:"archived,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	HTMLURL       string `json:"html_url,omitempty"`
}

// GetRepository returns a repository's metadata.
func (c *Client) GetRepository(ctx context.Context, repo string) (*Repository, error) {
	p, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var r Repository
	if err := c.get(ctx, "/repos/{owner}/{repo}", p, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
