package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stackorder/stackorder/internal/tf"
	"github.com/stackorder/stackorder/internal/version"
)

const (
	maxCheckSummary = 60000
	maxCheckTitle   = 250
	checkTimeout    = 30 * time.Second
)

var errNoCheckTarget = errors.New("the repository and commit are unknown")

var githubHTTP = &http.Client{Timeout: checkTimeout}

type checkRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type checkRunRequest struct {
	Name        string         `json:"name"`
	HeadSHA     string         `json:"head_sha"`
	Status      string         `json:"status"`
	Conclusion  string         `json:"conclusion"`
	CompletedAt string         `json:"completed_at"`
	DetailsURL  string         `json:"details_url,omitempty"`
	Output      checkRunOutput `json:"output"`
}

func createNeutralCheck(ctx context.Context, gh *Context, name, title, summary string) error {
	owner, repo, ok := strings.Cut(gh.Repository, "/")
	if !ok || owner == "" || repo == "" || gh.SHA == "" {
		return errNoCheckTarget
	}
	title = clip(title, maxCheckTitle)
	summary, _ = tf.Truncate(summary, maxCheckSummary)
	body, err := json.Marshal(checkRunRequest{
		Name:        name,
		HeadSHA:     gh.SHA,
		Status:      "completed",
		Conclusion:  "neutral",
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		DetailsURL:  gh.JobURL(),
		Output:      checkRunOutput{Title: title, Summary: summary},
	})
	if err != nil {
		return fmt.Errorf("encoding the check run: %w", err)
	}
	endpoint := gh.APIURL + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/check-runs"
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the check run request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+gh.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "stackorder/"+version.Version)
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("creating check run %q: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("creating check run %q: GitHub returned %d: %s", name, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (a *app) neutralCheck(ctx context.Context, gh *Context, name, title, summary string) {
	if !gh.CI {
		return
	}
	if gh.Token == "" {
		msg := fmt.Sprintf("GITHUB_TOKEN is not set, so the neutral %q check was not created", name)
		a.log.Warn(msg)
		a.annotate("warning", msg)
		return
	}
	if err := createNeutralCheck(context.WithoutCancel(ctx), gh, name, title, summary); err != nil {
		msg := fmt.Sprintf("the neutral %q check was not created: %v", name, err)
		a.log.Warn(msg)
		a.annotate("warning", msg)
		return
	}
	a.log.Info("created neutral check", "check", name)
}
