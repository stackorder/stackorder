package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateNeutralCheck(t *testing.T) {
	var got checkRunRequest
	var headers http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, headers = r.URL.EscapedPath(), r.Header.Clone()
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	gh := &Context{CI: true, Repository: "acme/infra", SHA: headSHA, APIURL: srv.URL, Token: "ghs_abc", RunID: 5, ServerURL: "https://github.com"}

	title := strings.Repeat("é", 200)
	summary := strings.Repeat("line\n", 20000)
	require.NoError(t, createNeutralCheck(context.Background(), gh, "stackorder/plan: stacks/app", title, summary))
	assert.Equal(t, "/repos/acme/infra/check-runs", path)
	assert.Equal(t, "Bearer ghs_abc", headers.Get("Authorization"))
	assert.Equal(t, "application/vnd.github+json", headers.Get("Accept"))
	assert.Equal(t, "2022-11-28", headers.Get("X-GitHub-Api-Version"))
	assert.Equal(t, "stackorder/plan: stacks/app", got.Name)
	assert.Equal(t, headSHA, got.HeadSHA)
	assert.Equal(t, "completed", got.Status)
	assert.Equal(t, "neutral", got.Conclusion)
	assert.Equal(t, "https://github.com/acme/infra/actions/runs/5", got.DetailsURL)
	_, err := time.Parse(time.RFC3339, got.CompletedAt)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(got.Output.Title), maxCheckTitle)
	assert.True(t, strings.HasPrefix(title, got.Output.Title))
	assert.LessOrEqual(t, len(got.Output.Summary), maxCheckSummary)
	assert.Contains(t, got.Output.Summary, "output truncated")
}

func TestCreateNeutralCheckErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	tests := []struct {
		name string
		gh   Context
		want string
	}{
		{name: "rejected", gh: Context{Repository: "acme/infra", SHA: headSHA, APIURL: srv.URL}, want: `GitHub returned 401: {"message":"Bad credentials"}`},
		{name: "unreachable", gh: Context{Repository: "acme/infra", SHA: headSHA, APIURL: deadURL(t)}, want: "connection refused"},
		{name: "bad url", gh: Context{Repository: "acme/infra", SHA: headSHA, APIURL: "http://[::1"}, want: "building the check run request"},
		{name: "no repository", gh: Context{SHA: headSHA, APIURL: srv.URL}, want: "the repository and commit are unknown"},
		{name: "no commit", gh: Context{Repository: "acme/infra", APIURL: srv.URL}, want: "the repository and commit are unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := createNeutralCheck(context.Background(), &tt.gh, "stackorder/resolve", "t", "s")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestNeutralCheckOutsideActions(t *testing.T) {
	var logs strings.Builder
	a := newTestApp(&logs)
	a.neutralCheck(context.Background(), &Context{Token: "x", Repository: "acme/infra", SHA: headSHA, APIURL: deadURL(t)}, "n", "t", "s")
	assert.Empty(t, logs.String())
}
