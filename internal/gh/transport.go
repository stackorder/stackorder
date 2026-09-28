package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 64 << 20
	maxPages         = 1000
	maxBackoff       = 30 * time.Second
)

type tokenSource interface {
	token(ctx context.Context) (string, error)
	invalidate() bool
}

type staticToken string

func (s staticToken) token(context.Context) (string, error) { return string(s), nil }

func (staticToken) invalidate() bool { return false }

type request struct {
	method  string
	route   string
	path    string
	rawURL  string
	query   url.Values
	body    any
	form    url.Values
	accept  string
	auth    tokenSource
	noRetry bool
}

type response struct {
	status int
	header http.Header
	body   []byte
}

type transport struct {
	base        *url.URL
	hc          *http.Client
	userAgent   string
	metrics     Metrics
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	jitter      func() float64
	maxAttempts int
	baseDelay   time.Duration
	maxWait     time.Duration
}

func newTransport(cfg Config) (*transport, error) {
	raw := cfg.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("gh: base url: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("gh: base url %q must be an absolute http or https url", raw)
	}
	t := &transport{
		base:        base,
		hc:          cfg.HTTPClient,
		userAgent:   cfg.UserAgent,
		metrics:     cfg.Metrics,
		now:         cfg.Clock,
		sleep:       sleepContext,
		jitter:      rand.Float64,
		maxAttempts: cfg.MaxAttempts,
		baseDelay:   cfg.RetryBaseDelay,
		maxWait:     cfg.MaxRetryWait,
	}
	if t.hc == nil {
		t.hc = &http.Client{Timeout: 30 * time.Second}
	}
	if t.userAgent == "" {
		t.userAgent = "stackorder"
	}
	if t.now == nil {
		t.now = time.Now
	}
	if t.maxAttempts <= 0 {
		t.maxAttempts = 5
	}
	if t.baseDelay <= 0 {
		t.baseDelay = time.Second
	}
	if t.maxWait <= 0 {
		t.maxWait = time.Minute
	}
	return t, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *transport) target(r request) string {
	if r.rawURL != "" {
		return r.rawURL
	}
	u := t.base.String() + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	return u
}

func (t *transport) do(ctx context.Context, r request) (*response, error) {
	var payload []byte
	contentType := ""
	switch {
	case r.form != nil:
		payload = []byte(r.form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case r.body != nil:
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, fmt.Errorf("gh: %s %s: encode body: %w", r.method, r.route, err)
		}
		payload = b
		contentType = "application/json"
	}
	target := t.target(r)
	attempts := t.maxAttempts
	if r.noRetry {
		attempts = 1
	}
	refreshed := false
	for attempt := 1; ; attempt++ {
		token := ""
		if r.auth != nil {
			tok, err := r.auth.token(ctx)
			if err != nil {
				return nil, fmt.Errorf("gh: %s %s: %w", r.method, r.route, err)
			}
			token = tok
		}
		resp, err := t.once(ctx, r, target, token, payload, contentType)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("gh: %s %s: %w", r.method, r.route, ctx.Err())
			}
			if attempt >= attempts {
				return nil, fmt.Errorf("gh: %s %s: %w", r.method, r.route, err)
			}
			if err := t.sleep(ctx, t.backoff(attempt)); err != nil {
				return nil, fmt.Errorf("gh: %s %s: %w", r.method, r.route, err)
			}
			continue
		}
		if resp.status < 300 || resp.status == http.StatusNotModified {
			return resp, nil
		}
		apiErr := newAPIError(r, resp)
		if resp.status == http.StatusUnauthorized && r.auth != nil && !refreshed && r.auth.invalidate() {
			refreshed = true
			continue
		}
		wait, retry := t.retryDelay(resp, apiErr, attempt)
		if !retry || attempt >= attempts || wait > t.maxWait {
			return nil, apiErr
		}
		if err := t.sleep(ctx, wait); err != nil {
			return nil, fmt.Errorf("gh: %s %s: %w", r.method, r.route, err)
		}
	}
}

func (t *transport) once(ctx context.Context, r request, target, token string, payload []byte, contentType string) (*response, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, body)
	if err != nil {
		return nil, err
	}
	accept := r.accept
	if accept == "" {
		accept = MediaType
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	req.Header.Set("User-Agent", t.userAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	resp, err := t.hc.Do(req)
	if err != nil {
		t.observe(r, 0, time.Since(start))
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	t.observe(r, resp.StatusCode, time.Since(start))
	t.observeRateLimit(resp.Header)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func (t *transport) observe(r request, status int, d time.Duration) {
	if t.metrics != nil {
		t.metrics.ObserveRequest(r.method, r.route, status, d)
	}
}

func (t *transport) observeRateLimit(h http.Header) {
	if t.metrics == nil {
		return
	}
	remaining, err := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return
	}
	t.metrics.ObserveRateLimit(remaining, time.Unix(reset, 0).UTC())
}

func (t *transport) backoff(attempt int) time.Duration {
	d := t.baseDelay << (attempt - 1)
	if d <= 0 || d > maxBackoff {
		d = maxBackoff
	}
	half := d / 2
	return half + time.Duration(t.jitter()*float64(half))
}

func (t *transport) retryDelay(resp *response, e *APIError, attempt int) (time.Duration, bool) {
	switch {
	case resp.status == http.StatusTooManyRequests, resp.status == http.StatusForbidden && isRateLimit(resp, e):
		e.rateLimited = true
		if d, ok := headerDelay(resp.header, t.now()); ok {
			return d, true
		}
		return t.backoff(attempt), true
	case resp.status >= 500:
		if d, ok := headerDelay(resp.header, t.now()); ok {
			return d, true
		}
		return t.backoff(attempt), true
	}
	return 0, false
}

func isRateLimit(resp *response, e *APIError) bool {
	if resp.header.Get("Retry-After") != "" || resp.header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	return strings.Contains(strings.ToLower(e.Message), "rate limit")
}

func headerDelay(h http.Header, now time.Time) (time.Duration, bool) {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
		if when, err := http.ParseTime(v); err == nil {
			return max(when.Sub(now), 0), true
		}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(time.Unix(reset, 0).Sub(now)+time.Second, 0), true
		}
	}
	return 0, false
}

func newAPIError(r request, resp *response) *APIError {
	e := &APIError{Status: resp.status, Method: r.method, Route: r.route}
	var body struct {
		Message          string        `json:"message"`
		DocumentationURL string        `json:"documentation_url"`
		Errors           []ErrorDetail `json:"errors"`
	}
	if json.Unmarshal(resp.body, &body) == nil {
		e.Message = body.Message
		e.DocumentationURL = body.DocumentationURL
		e.Errors = body.Errors
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.status)
	}
	return e
}

func (t *transport) decode(r request, resp *response, out any) error {
	if out == nil || len(resp.body) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return fmt.Errorf("gh: %s %s: decode response: %w", r.method, r.route, err)
	}
	return nil
}

func (t *transport) call(ctx context.Context, r request, out any) error {
	resp, err := t.do(ctx, r)
	if err != nil {
		return err
	}
	return t.decode(r, resp, out)
}

func getAll[T any](ctx context.Context, t *transport, r request, key string, limit int) ([]T, error) {
	q := url.Values{}
	for k, v := range r.query {
		q[k] = append([]string(nil), v...)
	}
	if q.Get("per_page") == "" {
		q.Set("per_page", "100")
	}
	r.query = q
	var out []T
	for range maxPages {
		resp, err := t.do(ctx, r)
		if err != nil {
			return nil, err
		}
		items, err := decodeItems[T](resp.body, key)
		if err != nil {
			return nil, fmt.Errorf("gh: %s %s: decode response: %w", r.method, r.route, err)
		}
		out = append(out, items...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		next := nextLink(resp.header.Get("Link"))
		if next == "" {
			return out, nil
		}
		if err := t.sameOrigin(next); err != nil {
			return nil, fmt.Errorf("gh: %s %s: %w", r.method, r.route, err)
		}
		r.rawURL = next
		r.query = nil
	}
	return nil, fmt.Errorf("gh: %s %s: more than %d pages", r.method, r.route, maxPages)
}

func decodeItems[T any](body []byte, key string) ([]T, error) {
	var items []T
	if key == "" {
		err := json.Unmarshal(body, &items)
		return items, err
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, err
	}
	raw, ok := wrapper[key]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	err := json.Unmarshal(raw, &items)
	return items, err
}

func (t *transport) sameOrigin(next string) error {
	u, err := url.Parse(next)
	if err != nil {
		return fmt.Errorf("next page link: %w", err)
	}
	if u.Scheme != t.base.Scheme || u.Host != t.base.Host {
		return errors.New("next page link points outside the api host")
	}
	return nil
}

func nextLink(header string) string {
	for part := range strings.SplitSeq(header, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		link := strings.TrimSpace(segs[0])
		if !strings.HasPrefix(link, "<") || !strings.HasSuffix(link, ">") {
			continue
		}
		for _, param := range segs[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || strings.TrimSpace(name) != "rel" {
				continue
			}
			for rel := range strings.FieldsSeq(strings.Trim(strings.TrimSpace(value), `"`)) {
				if rel == "next" {
					return link[1 : len(link)-1]
				}
			}
		}
	}
	return ""
}
