package gh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/stackorder/stackorder/internal/gh/codeowners"
)

// CodeownersPaths are the locations GitHub reads CODEOWNERS from, in the
// order it looks for them; the first one found is used.
var CodeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// Tag is a git tag and the commit it points at.
type Tag struct {
	Name string
	SHA  string
}

type tagWire struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// UnmarshalJSON decodes GitHub's tag object with its nested commit.
func (t *Tag) UnmarshalJSON(b []byte) error {
	var w tagWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	t.Name, t.SHA = w.Name, w.Commit.SHA
	return nil
}

// MarshalJSON encodes the tag in GitHub's wire format.
func (t Tag) MarshalJSON() ([]byte, error) {
	var w tagWire
	w.Name, w.Commit.SHA = t.Name, t.SHA
	return json.Marshal(w)
}

// Codeowners is the CODEOWNERS file in effect for a ref.
type Codeowners struct {
	*codeowners.File
	// Path is where the file was found.
	Path string
}

// GetContents returns the content of a file at ref, the default branch
// when ref is empty. A missing file is ErrNotFound; a directory is an error.
func (c *Client) GetContents(ctx context.Context, repo, path, ref string) ([]byte, error) {
	body, err := c.contents(ctx, repo, path, ref)
	if err != nil {
		return nil, err
	}
	if isListing(body) {
		return nil, fmt.Errorf("gh: contents %s: is a directory", path)
	}
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, fmt.Errorf("gh: contents %s: decode response: %w", path, err)
	}
	if file.Type != "" && file.Type != "file" {
		return nil, fmt.Errorf("gh: contents %s: is a %s, not a file", path, file.Type)
	}
	if file.Encoding != "base64" {
		return nil, fmt.Errorf("gh: contents %s: unsupported encoding %q", path, file.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(file.Content))
	if err != nil {
		return nil, fmt.Errorf("gh: contents %s: %w", path, err)
	}
	return data, nil
}

// PathExists reports whether a file or directory exists at path on ref.
func (c *Client) PathExists(ctx context.Context, repo, path, ref string) (bool, error) {
	_, err := c.contents(ctx, repo, path, ref)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// DirEntry is an entry of a directory listing.
type DirEntry struct {
	// Name is the entry's base name.
	Name string `json:"name"`
	// Type is "file", "dir", "symlink" or "submodule".
	Type string `json:"type"`
}

// ListDir returns the entries of the directory at path on ref, the default
// branch when ref is empty, as the Contents API lists them: at most 1,000.
// A path that is missing or is not a directory is ErrNotFound.
func (c *Client) ListDir(ctx context.Context, repo, path, ref string) ([]DirEntry, error) {
	body, err := c.contents(ctx, repo, path, ref)
	if err != nil {
		return nil, err
	}
	if !isListing(body) {
		return nil, fmt.Errorf("gh: contents %s: not a directory: %w", path, ErrNotFound)
	}
	var entries []DirEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("gh: contents %s: decode response: %w", path, err)
	}
	return entries, nil
}

func (c *Client) contents(ctx context.Context, repo, path, ref string) ([]byte, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	ep, err := escapePath("path", path)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	resp, err := c.t.do(ctx, request{
		method: http.MethodGet,
		route:  "/repos/{owner}/{repo}/contents/{path}",
		path:   rp + "/contents/" + ep,
		query:  q,
		auth:   c.auth,
	})
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

func isListing(body []byte) bool {
	b := bytes.TrimSpace(body)
	return len(b) > 0 && b[0] == '['
}

// ListTags returns the repository's tags with the commits they point at.
func (c *Client) ListTags(ctx context.Context, repo string) ([]Tag, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return list[Tag](ctx, c, "/repos/{owner}/{repo}/tags", rp+"/tags", nil, "", 0)
}

// GetRef resolves a git ref such as "heads/main" or "tags/v1.2.0" (a
// leading "refs/" is accepted) to a commit SHA, peeling annotated tags.
func (c *Client) GetRef(ctx context.Context, repo, ref string) (string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	ref = strings.TrimPrefix(ref, "refs/")
	er, err := escapePath("ref", ref)
	if err != nil {
		return "", err
	}
	var out struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := c.get(ctx, "/repos/{owner}/{repo}/git/ref/{ref}", rp+"/git/ref/"+er, &out); err != nil {
		return "", err
	}
	for range 8 {
		if out.Object.Type != "tag" {
			return out.Object.SHA, nil
		}
		if err := c.get(ctx, "/repos/{owner}/{repo}/git/tags/{tag_sha}", rp+"/git/tags/"+url.PathEscape(out.Object.SHA), &out); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("gh: ref %s: too many nested tags", ref)
}

// CodeownersFor returns the CODEOWNERS file GitHub uses at ref, looking in
// .github/, the root and docs/ in that order. It is ErrNotFound when the
// repository has none.
func (c *Client) CodeownersFor(ctx context.Context, repo, ref string) (*Codeowners, error) {
	for _, p := range CodeownersPaths {
		data, err := c.GetContents(ctx, repo, p, ref)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		f, err := codeowners.Parse(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gh: %s: %w", p, err)
		}
		return &Codeowners{File: f, Path: p}, nil
	}
	return nil, fmt.Errorf("gh: CODEOWNERS in %s at %q: %w", repo, ref, ErrNotFound)
}
