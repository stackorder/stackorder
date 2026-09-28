package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedMetrics struct {
	mu        sync.Mutex
	requests  []string
	remaining []int
}

func (m *recordedMetrics) ObserveRequest(method, route string, status int, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, fmt.Sprintf("%s %s %d", method, route, status))
}

func (m *recordedMetrics) ObserveRateLimit(remaining int, _ time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.remaining = append(m.remaining, remaining)
}

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleeps) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

var fixedNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func testTransport(t *testing.T, h http.Handler) (*transport, *sleeps, *recordedMetrics) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m := &recordedMetrics{}
	tr, err := newTransport(Config{BaseURL: srv.URL, HTTPClient: srv.Client(), Metrics: m, Clock: func() time.Time { return fixedNow }})
	require.NoError(t, err)
	sl := &sleeps{}
	tr.sleep = sl.sleep
	tr.jitter = func() float64 { return 0.5 }
	return tr, sl, m
}

func sequence(t *testing.T, steps ...func(w http.ResponseWriter, r *http.Request)) (http.Handler, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(steps) {
			t.Errorf("unexpected request %d: %s %s", i+1, r.Method, r.URL)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		steps[i](w, r)
	}), &n
}

func status(code int, headers ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"message":%q,"documentation_url":"https://docs.github.com/rest"}`, http.StatusText(code))
	}
}

func okJSON(body string, headers ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		_, _ = w.Write([]byte(body))
	}
}

func get(route string) request {
	return request{method: http.MethodGet, route: route, path: "/thing"}
}

func TestRetryPolicy(t *testing.T) {
	reset := strconv.FormatInt(fixedNow.Add(20*time.Second).Unix(), 10)
	tests := []struct {
		name       string
		steps      []func(http.ResponseWriter, *http.Request)
		wantErr    error
		wantStatus int
		wantSleeps []time.Duration
		wantCalls  int32
	}{
		{
			name:       "502 then success",
			steps:      []func(http.ResponseWriter, *http.Request){status(502), okJSON(`{"id":1}`)},
			wantSleeps: []time.Duration{750 * time.Millisecond},
			wantCalls:  2,
		},
		{
			name:       "exponential backoff on repeated 5xx",
			steps:      []func(http.ResponseWriter, *http.Request){status(500), status(503), status(504), okJSON(`{"id":1}`)},
			wantSleeps: []time.Duration{750 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second},
			wantCalls:  4,
		},
		{
			name:       "gives up after five attempts",
			steps:      []func(http.ResponseWriter, *http.Request){status(502), status(502), status(502), status(502), status(502)},
			wantStatus: 502,
			wantSleeps: []time.Duration{750 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second, 6 * time.Second},
			wantCalls:  5,
		},
		{
			name:       "429 honours Retry-After",
			steps:      []func(http.ResponseWriter, *http.Request){status(429, "Retry-After", "7"), okJSON(`{"id":1}`)},
			wantSleeps: []time.Duration{7 * time.Second},
			wantCalls:  2,
		},
		{
			name:       "secondary rate limit 403 honours Retry-After",
			steps:      []func(http.ResponseWriter, *http.Request){status(403, "Retry-After", "3"), okJSON(`{"id":1}`)},
			wantSleeps: []time.Duration{3 * time.Second},
			wantCalls:  2,
		},
		{
			name:       "primary rate limit 403 waits for reset",
			steps:      []func(http.ResponseWriter, *http.Request){status(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset), okJSON(`{"id":1}`)},
			wantSleeps: []time.Duration{21 * time.Second},
			wantCalls:  2,
		},
		{
			name:       "5xx honours Retry-After",
			steps:      []func(http.ResponseWriter, *http.Request){status(503, "Retry-After", "2"), okJSON(`{"id":1}`)},
			wantSleeps: []time.Duration{2 * time.Second},
			wantCalls:  2,
		},
		{
			name:       "429 without headers backs off",
			steps:      []func(http.ResponseWriter, *http.Request){status(429), okJSON(`{"id":1}`)},
			wantSleeps: []time.Duration{750 * time.Millisecond},
			wantCalls:  2,
		},
		{
			name:       "rate limit beyond max wait fails at once",
			steps:      []func(http.ResponseWriter, *http.Request){status(429, "Retry-After", "3600")},
			wantErr:    ErrRateLimited,
			wantStatus: 429,
			wantCalls:  1,
		},
		{
			name:       "plain 403 is not retried",
			steps:      []func(http.ResponseWriter, *http.Request){status(403)},
			wantStatus: 403,
			wantCalls:  1,
		},
		{
			name:       "404 is not retried",
			steps:      []func(http.ResponseWriter, *http.Request){status(404)},
			wantErr:    ErrNotFound,
			wantStatus: 404,
			wantCalls:  1,
		},
		{
			name:       "422 is not retried",
			steps:      []func(http.ResponseWriter, *http.Request){status(422)},
			wantStatus: 422,
			wantCalls:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, calls := sequence(t, tt.steps...)
			tr, sl, _ := testTransport(t, h)
			var out struct {
				ID int `json:"id"`
			}
			err := tr.call(context.Background(), get("/thing"), &out)
			assert.Equal(t, tt.wantCalls, calls.Load())
			assert.Equal(t, tt.wantSleeps, sl.all())
			if tt.wantStatus == 0 {
				require.NoError(t, err)
				assert.Equal(t, 1, out.ID)
				return
			}
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.wantStatus, apiErr.Status)
			assert.Equal(t, "/thing", apiErr.Route)
			assert.Equal(t, http.MethodGet, apiErr.Method)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestRateLimitedAfterExhaustingRetries(t *testing.T) {
	steps := make([]func(http.ResponseWriter, *http.Request), 5)
	for i := range steps {
		steps[i] = status(429, "Retry-After", "1")
	}
	h, _ := sequence(t, steps...)
	tr, _, _ := testTransport(t, h)
	err := tr.call(context.Background(), get("/thing"), nil)
	require.ErrorIs(t, err, ErrRateLimited)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.True(t, apiErr.RateLimited())
	assert.NotErrorIs(t, err, ErrNotFound)
}

func TestRetryOnNetworkError(t *testing.T) {
	var n atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write([]byte(`{"id":7}`))
	})
	tr, sl, m := testTransport(t, h)
	var out struct {
		ID int `json:"id"`
	}
	require.NoError(t, tr.call(context.Background(), get("/thing"), &out))
	assert.Equal(t, 7, out.ID)
	assert.Len(t, sl.all(), 1)
	assert.Equal(t, []string{"GET /thing 0", "GET /thing 200"}, m.requests)
}

func TestNetworkErrorExhaustsAttempts(t *testing.T) {
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	tr, sl, _ := testTransport(t, h)
	tr.maxAttempts = 2
	err := tr.call(context.Background(), get("/thing"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GET /thing")
	assert.Len(t, sl.all(), 1)
}

func TestNoRetryRequest(t *testing.T) {
	h, calls := sequence(t, status(502))
	tr, _, _ := testTransport(t, h)
	r := get("/once")
	r.noRetry = true
	require.Error(t, tr.call(context.Background(), r, nil))
	assert.Equal(t, int32(1), calls.Load())
}

func TestContextCancelledDuringBackoff(t *testing.T) {
	h, calls := sequence(t, status(502), okJSON(`{}`))
	tr, _, _ := testTransport(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	tr.sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	err := tr.call(ctx, get("/thing"), nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(1), calls.Load())
}

func TestContextCancelledBeforeRequest(t *testing.T) {
	h, calls := sequence(t)
	tr, _, _ := testTransport(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := tr.call(ctx, get("/thing"), nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), calls.Load())
}

func TestSleepContext(t *testing.T) {
	require.NoError(t, sleepContext(context.Background(), 0))
	require.NoError(t, sleepContext(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sleepContext(ctx, time.Hour), context.Canceled)
	require.ErrorIs(t, sleepContext(ctx, 0), context.Canceled)
}

type countingSource struct {
	tokens      []string
	invalidated int
}

func (c *countingSource) token(context.Context) (string, error) {
	return c.tokens[min(c.invalidated, len(c.tokens)-1)], nil
}

func (c *countingSource) invalidate() bool {
	c.invalidated++
	return true
}

func TestUnauthorizedRefreshesTokenOnce(t *testing.T) {
	var auths []string
	var mu sync.Mutex
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fresh" {
			status(401)(w, r)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	tr, sl, _ := testTransport(t, h)
	src := &countingSource{tokens: []string{"stale", "fresh"}}
	r := get("/thing")
	r.auth = src
	require.NoError(t, tr.call(context.Background(), r, nil))
	assert.Equal(t, []string{"Bearer stale", "Bearer fresh"}, auths)
	assert.Equal(t, 1, src.invalidated)
	assert.Empty(t, sl.all())

	src = &countingSource{tokens: []string{"stale"}}
	r.auth = src
	err := tr.call(context.Background(), r, nil)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Status)
	assert.Equal(t, 1, src.invalidated)
}

func TestStaticTokenIsNotRefreshed(t *testing.T) {
	h, calls := sequence(t, status(401))
	tr, _, _ := testTransport(t, h)
	r := get("/thing")
	r.auth = staticToken("user")
	require.Error(t, tr.call(context.Background(), r, nil))
	assert.Equal(t, int32(1), calls.Load())
}

type failingSource struct{}

func (failingSource) token(context.Context) (string, error) { return "", errors.New("no token") }

func (failingSource) invalidate() bool { return false }

func TestTokenSourceError(t *testing.T) {
	h, calls := sequence(t)
	tr, _, _ := testTransport(t, h)
	r := get("/thing")
	r.auth = failingSource{}
	err := tr.call(context.Background(), r, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no token")
	assert.Equal(t, int32(0), calls.Load())
}

func TestRequestHeadersAndBody(t *testing.T) {
	var got *http.Request
	var body map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	tr, _, _ := testTransport(t, h)
	tr.userAgent = "stackorder/test"
	var out struct {
		OK bool `json:"ok"`
	}
	err := tr.call(context.Background(), request{
		method: http.MethodPost, route: "/x", path: "/x", body: map[string]string{"a": "b"}, auth: staticToken("tok"),
	}, &out)
	require.NoError(t, err)
	assert.True(t, out.OK)
	assert.Equal(t, MediaType, got.Header.Get("Accept"))
	assert.Equal(t, APIVersion, got.Header.Get("X-GitHub-Api-Version"))
	assert.Equal(t, "stackorder/test", got.Header.Get("User-Agent"))
	assert.Equal(t, "Bearer tok", got.Header.Get("Authorization"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.Equal(t, map[string]any{"a": "b"}, body)
}

func TestFormBody(t *testing.T) {
	var ct, value string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		value = r.PostForm.Get("k")
		_, _ = w.Write([]byte(`{}`))
	})
	tr, _, _ := testTransport(t, h)
	require.NoError(t, tr.call(context.Background(), request{method: http.MethodPost, route: "/f", path: "/f", form: map[string][]string{"k": {"v"}}}, nil))
	assert.Equal(t, "application/x-www-form-urlencoded", ct)
	assert.Equal(t, "v", value)
}

func TestUnencodableBody(t *testing.T) {
	h, _ := sequence(t)
	tr, _, _ := testTransport(t, h)
	err := tr.call(context.Background(), request{method: http.MethodPost, route: "/x", path: "/x", body: map[string]any{"c": make(chan int)}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode body")
}

func TestDecodeError(t *testing.T) {
	h, _ := sequence(t, okJSON(`not json`))
	tr, _, _ := testTransport(t, h)
	var out map[string]any
	err := tr.call(context.Background(), get("/thing"), &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

func TestMetricsAndRateLimitObservation(t *testing.T) {
	reset := strconv.FormatInt(fixedNow.Add(time.Hour).Unix(), 10)
	h, _ := sequence(t,
		status(502, "X-RateLimit-Remaining", "4000", "X-RateLimit-Reset", reset),
		okJSON(`{}`, "X-RateLimit-Remaining", "3999", "X-RateLimit-Reset", reset),
		okJSON(`{}`, "X-RateLimit-Remaining", "bogus"),
		okJSON(`{}`, "X-RateLimit-Remaining", "10", "X-RateLimit-Reset", "bogus"),
	)
	tr, _, m := testTransport(t, h)
	require.NoError(t, tr.call(context.Background(), request{method: http.MethodGet, route: "/repos/{owner}/{repo}", path: "/thing"}, nil))
	require.NoError(t, tr.call(context.Background(), get("/b"), nil))
	require.NoError(t, tr.call(context.Background(), get("/c"), nil))
	assert.Equal(t, []string{"GET /repos/{owner}/{repo} 502", "GET /repos/{owner}/{repo} 200", "GET /b 200", "GET /c 200"}, m.requests)
	assert.Equal(t, []int{4000, 3999}, m.remaining)
}

func TestPaginationFollowsLinkHeader(t *testing.T) {
	var srvURL string
	var pages []string
	var mu sync.Mutex
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pages = append(pages, r.URL.RawQuery)
		mu.Unlock()
		page := r.URL.Query().Get("page")
		switch page {
		case "", "1":
			w.Header().Set("Link", fmt.Sprintf(`<%s/items?page=2&per_page=100>; rel="next", <%s/items?page=3&per_page=100>; rel="last"`, srvURL, srvURL))
			_, _ = w.Write([]byte(`{"total_count":5,"items":[{"n":1},{"n":2}]}`))
		case "2":
			w.Header().Set("Link", fmt.Sprintf(`<%s/items?page=1&per_page=100>; rel="prev", <%s/items?page=3&per_page=100>; rel="next"`, srvURL, srvURL))
			_, _ = w.Write([]byte(`{"total_count":5,"items":[{"n":3},{"n":4}]}`))
		default:
			_, _ = w.Write([]byte(`{"total_count":5,"items":[{"n":5}]}`))
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	tr, err := newTransport(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)

	type item struct {
		N int `json:"n"`
	}
	items, err := getAll[item](context.Background(), tr, request{method: http.MethodGet, route: "/items", path: "/items", query: map[string][]string{"state": {"open"}}}, "items", 0)
	require.NoError(t, err)
	assert.Equal(t, []item{{1}, {2}, {3}, {4}, {5}}, items)
	assert.Equal(t, []string{"per_page=100&state=open", "page=2&per_page=100", "page=3&per_page=100"}, pages)

	limited, err := getAll[item](context.Background(), tr, request{method: http.MethodGet, route: "/items", path: "/items"}, "items", 3)
	require.NoError(t, err)
	assert.Equal(t, []item{{1}, {2}, {3}}, limited)
}

func TestPaginationRejectsForeignHost(t *testing.T) {
	h, _ := sequence(t, okJSON(`[1]`, "Link", `<https://evil.example.com/items?page=2>; rel="next"`))
	tr, _, _ := testTransport(t, h)
	_, err := getAll[int](context.Background(), tr, get("/items"), "", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the api host")
}

func TestPaginationStopsAtMaxPages(t *testing.T) {
	var srvURL string
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", fmt.Sprintf(`<%s/items?page=2>; rel="next"`, srvURL))
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	tr, err := newTransport(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = getAll[int](context.Background(), tr, get("/items"), "", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than")
}

func TestPaginationErrors(t *testing.T) {
	tests := []struct {
		name string
		step func(http.ResponseWriter, *http.Request)
		key  string
		want string
	}{
		{"bad array", okJSON(`{"a":1}`), "", "decode response"},
		{"bad wrapper", okJSON(`[1]`), "items", "decode response"},
		{"bad items", okJSON(`{"items":{"a":1}}`), "items", "decode response"},
		{"bad link", okJSON(`[]`, "Link", "<:bad>; rel=\"next\""), "", "next page link"},
		{"api error", status(404), "", "404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := sequence(t, tt.step)
			tr, _, _ := testTransport(t, h)
			_, err := getAll[int](context.Background(), tr, get("/items"), tt.key, 0)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestDecodeItemsMissingKey(t *testing.T) {
	items, err := decodeItems[int]([]byte(`{"total_count":0}`), "items")
	require.NoError(t, err)
	assert.Nil(t, items)
	items, err = decodeItems[int]([]byte(`{"items":null}`), "items")
	require.NoError(t, err)
	assert.Nil(t, items)
}

func TestNextLink(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"", ""},
		{`<https://api.github.com/x?page=2>; rel="next"`, "https://api.github.com/x?page=2"},
		{`<https://a/x?page=1>; rel="prev", <https://a/x?page=3>; rel="next"`, "https://a/x?page=3"},
		{`<https://a/x?page=9>; rel="last"`, ""},
		{`https://a/x; rel="next"`, ""},
		{`<https://a/x>`, ""},
		{`<https://a/x>; title="t"; rel="next last"`, "https://a/x"},
		{`<https://a/x>; rel`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			assert.Equal(t, tt.want, nextLink(tt.header))
		})
	}
}

func TestHeaderDelay(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
		ok     bool
	}{
		{"none", http.Header{}, 0, false},
		{"seconds", http.Header{"Retry-After": {"12"}}, 12 * time.Second, true},
		{"http date", http.Header{"Retry-After": {fixedNow.Add(30 * time.Second).Format(http.TimeFormat)}}, 30 * time.Second, true},
		{"past http date", http.Header{"Retry-After": {fixedNow.Add(-time.Minute).Format(http.TimeFormat)}}, 0, true},
		{"garbage retry after", http.Header{"Retry-After": {"soon"}}, 0, false},
		{"reset", http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(fixedNow.Add(9*time.Second).Unix(), 10)}}, 10 * time.Second, true},
		{"reset in the past", http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(fixedNow.Add(-time.Hour).Unix(), 10)}}, 0, true},
		{"remaining not zero", http.Header{"X-Ratelimit-Remaining": {"5"}, "X-Ratelimit-Reset": {"1"}}, 0, false},
		{"bad reset", http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"x"}}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := headerDelay(tt.header, fixedNow)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, d)
		})
	}
}

func TestBackoffIsBoundedAndJittered(t *testing.T) {
	tr, err := newTransport(Config{})
	require.NoError(t, err)
	tr.jitter = func() float64 { return 0 }
	assert.Equal(t, 500*time.Millisecond, tr.backoff(1))
	assert.Equal(t, time.Second, tr.backoff(2))
	assert.Equal(t, maxBackoff/2, tr.backoff(10))
	assert.Equal(t, maxBackoff/2, tr.backoff(70))
	tr.jitter = func() float64 { return 0.999 }
	assert.Less(t, tr.backoff(1), time.Second)
}

func TestNewTransportDefaultsAndValidation(t *testing.T) {
	tr, err := newTransport(Config{})
	require.NoError(t, err)
	assert.Equal(t, DefaultBaseURL, tr.base.String())
	assert.Equal(t, "stackorder", tr.userAgent)
	assert.Equal(t, 5, tr.maxAttempts)
	assert.Equal(t, time.Second, tr.baseDelay)
	assert.Equal(t, time.Minute, tr.maxWait)
	assert.NotNil(t, tr.hc)

	tr, err = newTransport(Config{BaseURL: "https://ghe.example.com/api/v3/"})
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example.com/api/v3/repos/o/r", tr.target(request{path: "/repos/o/r"}))

	for _, bad := range []string{"ftp://x", "not a url", "/relative", "http://%zz"} {
		_, err := newTransport(Config{BaseURL: bad})
		assert.Error(t, err, bad)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := newAPIError(request{method: "POST", route: "/repos/{owner}/{repo}/issues"}, &response{
		status: 422,
		body:   []byte(`{"message":"Validation Failed","errors":[{"resource":"Issue","field":"title","code":"missing_field"},{"message":"custom thing"},{}],"documentation_url":"https://docs"}`),
	})
	assert.Equal(t, "gh: POST /repos/{owner}/{repo}/issues: 422 Validation Failed; Issue.title missing_field; custom thing", e.Error())
	assert.Equal(t, "https://docs", e.DocumentationURL)
	assert.Len(t, e.Errors, 3)

	e = newAPIError(request{method: "GET", route: "/x"}, &response{status: 502, body: []byte("<html>")})
	assert.Equal(t, "gh: GET /x: 502 Bad Gateway", e.Error())
	assert.False(t, e.Is(ErrNotFound))
	assert.False(t, e.Is(errors.New("other")))

	e = &APIError{Method: "GET", Route: "/x", Status: 418}
	assert.Equal(t, "gh: GET /x: 418", e.Error())
}
