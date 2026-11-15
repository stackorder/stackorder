package client

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "seconds", value: "5", want: 5 * time.Second, ok: true},
		{name: "zero seconds", value: "0", want: 0, ok: true},
		{name: "padded seconds", value: "  12 ", want: 12 * time.Second, ok: true},
		{name: "negative seconds", value: "-3"},
		{name: "overflowing seconds saturate", value: "9223372037", want: time.Duration(math.MaxInt64), ok: true},
		{name: "out of int64 range", value: "99999999999999999999"},
		{name: "http date in the future", value: now.Add(30 * time.Second).UTC().Format(http.TimeFormat), want: 30 * time.Second, ok: true},
		{name: "http date in the past", value: now.Add(-time.Minute).UTC().Format(http.TimeFormat), want: 0, ok: true},
		{name: "empty", value: ""},
		{name: "garbage", value: "soon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tt.value, now)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTooManyRequestsIsRetried(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			writeJSON(w, http.StatusTooManyRequests, v1.Error{Code: "http_429", Message: "slow down"})
			return
		}
		writeJSON(w, http.StatusOK, v1.CreateRunResponse{RunID: testRunID})
	})
	ts := &countingSource{}
	got, err := fastClient(s.srv.URL, ts).CreateRun(context.Background(), v1.CreateRunRequest{Repo: "acme/infra", SHA: "abc"})
	require.NoError(t, err)
	assert.Equal(t, testRunID, got.RunID)
	assert.Equal(t, int32(2), n.Load())
	assert.Equal(t, int32(2), ts.calls.Load(), "each attempt asks for a token")
}

func TestTooManyRequestsExhaustedIsUnreachable(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		writeJSON(w, http.StatusTooManyRequests, v1.Error{Code: "http_429", Message: "slow down"})
	})
	_, err := fastClient(s.srv.URL, StaticTokenSource("tok")).GetRun(context.Background(), testRunID)
	require.Error(t, err)
	assert.True(t, IsUnreachable(err))
	assert.NotErrorIs(t, err, ErrRefused)
	assert.Equal(t, int32(DefaultRetries+1), n.Load())
	assert.Contains(t, err.Error(), "4 attempts")
}

func recordingClient(base string, ts TokenSource, opts ...Option) (*Client, *[]time.Duration) {
	var mu sync.Mutex
	var delays []time.Duration
	c := New(base, ts, append([]Option{WithBackoff(time.Millisecond)}, opts...)...)
	c.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		delays = append(delays, d)
		mu.Unlock()
		return nil
	}
	return c, &delays
}

func TestRetryAfterSecondsHonoured(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			writeJSON(w, http.StatusServiceUnavailable, v1.Error{Code: CodeInternal, Message: "starting"})
			return
		}
		writeJSON(w, http.StatusOK, v1.Whoami{Login: "runner"})
	})
	c, delays := recordingClient(s.srv.URL, StaticTokenSource("tok"))
	_, err := c.Whoami(context.Background())
	require.NoError(t, err)
	require.Equal(t, []time.Duration{7 * time.Second}, *delays, "Retry-After replaces the fixed backoff")
}

func TestRetryAfterBelowBackoffKeepsBackoff(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			writeJSON(w, http.StatusTooManyRequests, v1.Error{Code: "http_429", Message: "slow down"})
			return
		}
		writeJSON(w, http.StatusOK, v1.Whoami{Login: "runner"})
	})
	c, delays := recordingClient(s.srv.URL, StaticTokenSource("tok"), WithBackoff(50*time.Millisecond))
	_, err := c.Whoami(context.Background())
	require.NoError(t, err)
	require.Equal(t, []time.Duration{50 * time.Millisecond}, *delays, "a Retry-After below the backoff never shortens the wait")
}

func TestRetryAfterHTTPDateHonoured(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", now.Add(20*time.Second).UTC().Format(http.TimeFormat))
			writeJSON(w, http.StatusTooManyRequests, v1.Error{Code: "http_429", Message: "slow down"})
			return
		}
		writeJSON(w, http.StatusOK, v1.Whoami{Login: "runner"})
	})
	c, delays := recordingClient(s.srv.URL, StaticTokenSource("tok"))
	c.now = func() time.Time { return now }
	_, err := c.Whoami(context.Background())
	require.NoError(t, err)
	require.Equal(t, []time.Duration{20 * time.Second}, *delays)
}

func TestRetryAfterOverCapStops(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "120")
		writeJSON(w, http.StatusServiceUnavailable, v1.Error{Code: CodeInternal, Message: "starting"})
	})
	c, delays := recordingClient(s.srv.URL, StaticTokenSource("tok"), WithMaxRetryWait(30*time.Second))
	_, err := c.Whoami(context.Background())
	require.Error(t, err)
	assert.True(t, IsUnreachable(err))
	assert.Equal(t, int32(1), n.Load(), "a Retry-After over the cap stops the retries at once")
	assert.Empty(t, *delays)
	assert.Contains(t, err.Error(), "Retry-After 2m0s over the 30s cap")
}

func TestRetryAfterOverflowingSecondsStops(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "9223372037")
		writeJSON(w, http.StatusTooManyRequests, v1.Error{Code: "http_429", Message: "slow down"})
	})
	c, delays := recordingClient(s.srv.URL, StaticTokenSource("tok"))
	_, err := c.Whoami(context.Background())
	require.Error(t, err)
	assert.True(t, IsUnreachable(err))
	assert.Equal(t, int32(1), n.Load(), "a Retry-After that overflows stops the retries, never spins")
	assert.Empty(t, *delays)
	assert.Contains(t, err.Error(), "over the")
}

func TestSleepContext(t *testing.T) {
	require.NoError(t, sleepContext(context.Background(), time.Millisecond))
	require.NoError(t, sleepContext(context.Background(), 0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sleepContext(ctx, time.Hour), context.Canceled)
	require.ErrorIs(t, sleepContext(ctx, 0), context.Canceled)
}

func TestRetryAfterCancellation(t *testing.T) {
	var n atomic.Int32
	s := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusServiceUnavailable, v1.Error{Code: CodeInternal, Message: "starting"})
	})
	c := New(s.srv.URL, StaticTokenSource("tok"))
	c.sleep = func(ctx context.Context, _ time.Duration) error { return context.Canceled }
	_, err := c.Whoami(context.Background())
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, IsUnreachable(err))
	assert.Equal(t, int32(1), n.Load())
}

func TestTooManyRequestsCarriesFreshToken(t *testing.T) {
	tokens := newFakeTokenServer(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	var count int
	s := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.MapClaims
		if _, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &claims); err != nil {
			writeJSON(w, http.StatusUnauthorized, v1.Error{Code: CodeUnauthorized, Message: "malformed token"})
			return
		}
		id, _ := claims["jti"].(string)
		mu.Lock()
		replay := seen[id]
		seen[id] = true
		count++
		n := count
		mu.Unlock()
		switch {
		case replay:
			writeJSON(w, http.StatusUnauthorized, v1.Error{Code: CodeUnauthorized, Message: "jti already used"})
		case n <= 2:
			writeJSON(w, http.StatusTooManyRequests, v1.Error{Code: "http_429", Message: "slow down"})
		default:
			writeJSON(w, http.StatusOK, v1.Whoami{Login: "runner"})
		}
	})
	got, err := fastClient(s.srv.URL, OIDCTokenSource(testAudience)).Whoami(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "runner", got.Login)
	assert.Equal(t, int32(3), tokens.fetches.Load())
	assert.Len(t, s.requests(), 3)
}
