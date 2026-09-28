// Package client is the stackorder CLI's HTTP client for the server API. It
// authenticates with a GitHub Actions OIDC token or an automation API key,
// retries network failures and 5xx answers, and maps v1.Error bodies to
// sentinel errors so the CLI can tell an unreachable server, which it
// degrades on, from a refusal, which it reports.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/version"
)

const (
	// DefaultRetries is how many times a request is retried after its first
	// attempt fails with a network error, a timeout or a 5xx answer.
	DefaultRetries = 3
	// DefaultTimeout bounds each attempt, including obtaining its token.
	DefaultTimeout = 30 * time.Second
)

var defaultBackoff = []time.Duration{200 * time.Millisecond, 800 * time.Millisecond, 2 * time.Second}

// Client calls the stackorder server API. It is safe for concurrent use.
type Client struct {
	baseURL   string
	ts        TokenSource
	http      *http.Client
	userAgent string
	retries   int
	backoff   []time.Duration
	timeout   time.Duration
	logger    *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP client; its own Timeout, if any,
// applies in addition to WithTimeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.http = hc
		}
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithRetries sets how many times a failed attempt is retried; 0 disables
// retries. Only network errors, timeouts and 5xx answers are retried.
func WithRetries(n int) Option {
	return func(c *Client) { c.retries = max(n, 0) }
}

// WithBackoff sets the delays before each retry; the last delay repeats. The
// default is 200ms, 800ms, 2s.
func WithBackoff(delays ...time.Duration) Option {
	return func(c *Client) {
		if len(delays) > 0 {
			c.backoff = append([]time.Duration(nil), delays...)
		}
	}
}

// WithTimeout bounds each attempt; the default is DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithLogger sets the logger that records retries at debug level.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// New returns a Client for the server at baseURL, which may carry a path
// prefix. ts supplies the bearer token and may be nil for unauthenticated
// calls.
func New(baseURL string, ts TokenSource, opts ...Option) *Client {
	c := &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		ts:        ts,
		http:      &http.Client{},
		userAgent: "stackorder/" + version.Version,
		retries:   DefaultRetries,
		backoff:   defaultBackoff,
		timeout:   DefaultTimeout,
		logger:    slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// BaseURL returns the server base URL without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// CreateRun calls POST /v1/runs to find or create the run for a commit.
func (c *Client) CreateRun(ctx context.Context, req v1.CreateRunRequest) (*v1.CreateRunResponse, error) {
	var out v1.CreateRunResponse
	if err := c.do(ctx, http.MethodPost, "/v1/runs", true, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadGraph calls POST /v1/runs/{id}/graph and returns the resolution.
func (c *Client) UploadGraph(ctx context.Context, runID string, req v1.GraphUploadRequest) (*v1.ResolveResponse, error) {
	p, err := runPath(runID, "graph")
	if err != nil {
		return nil, err
	}
	var out v1.ResolveResponse
	if err := c.do(ctx, http.MethodPost, p, true, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PostResult calls POST /v1/runs/{id}/stacks/{key}/result with a plan, apply
// or drift outcome.
func (c *Client) PostResult(ctx context.Context, runID, stackKey string, res v1.StackResult) (*v1.RunStack, error) {
	p, err := stackPath(runID, stackKey, "result")
	if err != nil {
		return nil, err
	}
	var out v1.RunStack
	if err := c.do(ctx, http.MethodPost, p, true, res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PostCheck calls POST /v1/runs/{id}/stacks/{key}/checks/{name} with a named
// check verdict.
func (c *Client) PostCheck(ctx context.Context, runID, stackKey, name string, verdict v1.CheckVerdict) (*v1.Check, error) {
	if name == "" {
		return nil, errors.New("client: check name is required")
	}
	p, err := stackPath(runID, stackKey, "checks", url.PathEscape(name))
	if err != nil {
		return nil, err
	}
	var out v1.Check
	if err := c.do(ctx, http.MethodPost, p, true, verdict, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRun calls GET /v1/runs/{id}.
func (c *Client) GetRun(ctx context.Context, runID string) (*v1.Run, error) {
	p, err := runPath(runID)
	if err != nil {
		return nil, err
	}
	var out v1.Run
	if err := c.do(ctx, http.MethodGet, p, true, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Unlock calls POST /v1/unlock, addressing the stack by repository and key.
func (c *Client) Unlock(ctx context.Context, req v1.UnlockRequest) (*v1.UnlockResponse, error) {
	var out v1.UnlockResponse
	if err := c.do(ctx, http.MethodPost, "/v1/unlock", true, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Whoami calls GET /v1/me.
func (c *Client) Whoami(ctx context.Context) (*v1.Whoami, error) {
	var out v1.Whoami
	if err := c.do(ctx, http.MethodGet, "/v1/me", true, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Healthz calls GET /healthz without credentials and returns nil on any 2xx.
func (c *Client) Healthz(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", false, nil, nil)
}

func runPath(runID string, rest ...string) (string, error) {
	if runID == "" {
		return "", errors.New("client: run id is required")
	}
	return "/v1/runs/" + url.PathEscape(runID) + joinSegments(rest), nil
}

func stackPath(runID, stackKey string, rest ...string) (string, error) {
	if stackKey == "" {
		return "", errors.New("client: stack key is required")
	}
	return runPath(runID, append([]string{"stacks", url.PathEscape(stackKey)}, rest...)...)
}

func joinSegments(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	return "/" + strings.Join(segments, "/")
}

func (c *Client) do(ctx context.Context, method, path string, auth bool, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("client: encoding %s %s: %w", method, path, err)
		}
		body = b
	}
	reauthenticated := false
	attempts := 0
	for {
		attempts++
		err := c.attempt(ctx, method, path, auth, body, out)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("client: %s %s: %w", method, path, ctx.Err())
		}
		if auth && !reauthenticated && errors.Is(err, ErrUnauthorized) {
			if inv, ok := c.ts.(invalidator); ok {
				inv.Invalidate()
				reauthenticated = true
				attempts--
				continue
			}
		}
		if !IsUnreachable(err) {
			return fmt.Errorf("client: %s %s: %w", method, path, err)
		}
		if attempts > c.retries {
			return fmt.Errorf("client: %s %s: %d attempts: %w", method, path, attempts, err)
		}
		delay := c.delay(attempts - 1)
		c.logger.DebugContext(ctx, "retrying server request",
			slog.String("method", method), slog.String("path", path),
			slog.Int("attempt", attempts), slog.Duration("delay", delay), slog.Any("error", err))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("client: %s %s: %w", method, path, ctx.Err())
		case <-timer.C:
		}
	}
}

func (c *Client) delay(retry int) time.Duration {
	if retry < len(c.backoff) {
		return c.backoff[retry]
	}
	return c.backoff[len(c.backoff)-1]
}

func (c *Client) attempt(ctx context.Context, method, path string, auth bool, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var rdr io.Reader = http.NoBody
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && c.ts != nil {
		tok, err := c.ts.Token(ctx)
		if err != nil {
			return fmt.Errorf("obtaining token: %w", err)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := decodeError(resp)
		if resp.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("%w: %w", ErrUnreachable, apiErr)
		}
		return apiErr
	}
	if out == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		if err != nil {
			return fmt.Errorf("%w: reading response: %w", ErrUnreachable, err)
		}
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: reading response: %w", ErrUnreachable, err)
		}
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
