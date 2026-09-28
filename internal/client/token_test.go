package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAudience     = "https://stackorder.example"
	testRequestToken = "runtime-request-token"
)

type fakeTokenServer struct {
	t       *testing.T
	srv     *httptest.Server
	fetches atomic.Int32
	mu      sync.Mutex
	status  int
	body    string
	lastURL string
}

func newFakeTokenServer(t *testing.T) *fakeTokenServer {
	t.Helper()
	f := &fakeTokenServer{t: t, status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	t.Setenv(EnvOIDCRequestURL, f.srv.URL+"/idtoken?api-version=2.0")
	t.Setenv(EnvOIDCRequestToken, testRequestToken)
	return f
}

func (f *fakeTokenServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastURL = r.URL.String()
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testRequestToken {
		http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("api-version") != "2.0" {
		http.Error(w, "missing api-version", http.StatusBadRequest)
		return
	}
	n := int(f.fetches.Add(1))
	if f.status != http.StatusOK || f.body != "" {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
		return
	}
	claims := jwt.MapClaims{
		"aud": r.URL.Query().Get("audience"),
		"iss": "https://token.actions.githubusercontent.com",
		"jti": "jti-" + strconv.Itoa(n),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-key"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"count": 1, "value": tok})
}

func (f *fakeTokenServer) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func jti(t *testing.T, tok string) string {
	t.Helper()
	var claims jwt.MapClaims
	_, _, err := jwt.NewParser().ParseUnverified(tok, &claims)
	require.NoError(t, err)
	return claims["jti"].(string)
}

func TestOIDCTokenSourceRequest(t *testing.T) {
	f := newFakeTokenServer(t)
	tok, err := OIDCTokenSource(testAudience).Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "jti-1", jti(t, tok))
	assert.Equal(t, "/idtoken?api-version=2.0&audience=https%3A%2F%2Fstackorder.example", f.lastURL)

	var claims jwt.MapClaims
	_, _, err = jwt.NewParser().ParseUnverified(tok, &claims)
	require.NoError(t, err)
	assert.Equal(t, testAudience, claims["aud"])
}

func TestOIDCTokenSourceURLWithoutQuery(t *testing.T) {
	f := newFakeTokenServer(t)
	t.Setenv(EnvOIDCRequestURL, f.srv.URL+"/idtoken")
	_, err := OIDCTokenSource(testAudience).Token(context.Background())
	require.Error(t, err, "the fake requires api-version, so a request without it must fail")
	assert.Equal(t, "/idtoken?audience=https%3A%2F%2Fstackorder.example", f.lastURL)
}

func TestOIDCTokenSourceFetchesEveryCall(t *testing.T) {
	f := newFakeTokenServer(t)
	ts := OIDCTokenSource(testAudience)
	got := make([]string, 0, 3)
	for range 3 {
		tok, err := ts.Token(context.Background())
		require.NoError(t, err)
		got = append(got, jti(t, tok))
	}
	assert.Equal(t, []string{"jti-1", "jti-2", "jti-3"}, got)
	assert.Equal(t, int32(3), f.fetches.Load())
}

func TestOIDCTokenSourceErrors(t *testing.T) {
	tests := []struct {
		name            string
		setup           func(t *testing.T, f *fakeTokenServer)
		wantErr         error
		wantUnreachable bool
		wantMsg         string
	}{
		{
			name:    "no request url",
			setup:   func(t *testing.T, _ *fakeTokenServer) { t.Setenv(EnvOIDCRequestURL, "") },
			wantErr: ErrNoOIDC,
		},
		{
			name:    "no request token",
			setup:   func(t *testing.T, _ *fakeTokenServer) { t.Setenv(EnvOIDCRequestToken, "") },
			wantErr: ErrNoOIDC,
		},
		{
			name:    "wrong request token",
			setup:   func(t *testing.T, _ *fakeTokenServer) { t.Setenv(EnvOIDCRequestToken, "other") },
			wantMsg: "OIDC token endpoint returned 401",
		},
		{
			name:            "endpoint 5xx",
			setup:           func(_ *testing.T, f *fakeTokenServer) { f.set(http.StatusServiceUnavailable, "busy") },
			wantUnreachable: true,
		},
		{
			name:    "not json",
			setup:   func(_ *testing.T, f *fakeTokenServer) { f.set(http.StatusOK, "<html>") },
			wantMsg: "decoding OIDC token response",
		},
		{
			name:    "empty value",
			setup:   func(_ *testing.T, f *fakeTokenServer) { f.set(http.StatusOK, `{"value":""}`) },
			wantMsg: "returned no token",
		},
		{
			name: "endpoint down",
			setup: func(_ *testing.T, f *fakeTokenServer) {
				f.srv.Close()
			},
			wantUnreachable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTokenServer(t)
			tt.setup(t, f)
			_, err := OIDCTokenSource(testAudience).Token(context.Background())
			require.Error(t, err)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantUnreachable, IsUnreachable(err), "IsUnreachable(%v)", err)
			if tt.wantMsg != "" {
				assert.Contains(t, err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestStaticAndAPIKeyTokenSources(t *testing.T) {
	tests := []struct {
		name    string
		ts      TokenSource
		want    string
		wantErr error
	}{
		{name: "api key", ts: APIKeyTokenSource("sk_live_abc"), want: "sk_live_abc"},
		{name: "empty api key", ts: APIKeyTokenSource(""), wantErr: ErrNoAPIKey},
		{name: "static", ts: StaticTokenSource("tok"), want: "tok"},
		{name: "empty static", ts: StaticTokenSource(""), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.ts.Token(context.Background())
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
