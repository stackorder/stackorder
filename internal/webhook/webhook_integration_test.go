//go:build integration

package webhook_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
	"github.com/stackorder/stackorder/internal/testutil/pgtest"
	"github.com/stackorder/stackorder/internal/webhook"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

type pgHarness struct {
	t       *testing.T
	st      *store.Store
	srv     *httptest.Server
	metrics *metrics.Registry
	wakes   chan struct{}
}

func newPGHarness(t *testing.T) *pgHarness {
	t.Helper()
	ph := &pgHarness{t: t, st: pgtest.New(t), metrics: metrics.New(), wakes: make(chan struct{}, 16)}
	h := webhook.New(secret, ph.st, func() { ph.wakes <- struct{}{} }, ph.metrics, nil)
	mux := http.NewServeMux()
	mux.Handle(webhook.Pattern, h)
	ph.srv = httptest.NewServer(mux)
	t.Cleanup(ph.srv.Close)
	return ph
}

func (ph *pgHarness) post(event, id, body, signature string) int {
	ph.t.Helper()
	req, err := http.NewRequestWithContext(ph.t.Context(), http.MethodPost, ph.srv.URL+"/webhooks/github", strings.NewReader(body))
	require.NoError(ph.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gh.HeaderEvent, event)
	req.Header.Set(gh.HeaderDelivery, id)
	if signature != "" {
		req.Header.Set(gh.HeaderSignature, signature)
	}
	resp, err := ph.srv.Client().Do(req)
	require.NoError(ph.t, err)
	require.NoError(ph.t, resp.Body.Close())
	return resp.StatusCode
}

func (ph *pgHarness) count() int {
	ph.t.Helper()
	var n int
	require.NoError(ph.t, ph.st.Pool().QueryRow(ph.t.Context(), `SELECT count(*) FROM events`).Scan(&n))
	return n
}

func sign(body string) string { return gh.SignPayload(secret, []byte(body)) }

func TestDeliveryIsStored(t *testing.T) {
	ph := newPGHarness(t)
	body := `{"action":"opened","number":7}`
	require.Equal(t, http.StatusAccepted, ph.post("pull_request", "72d3162e-cc78-11e3-81ab-4c9367dc0958", body, sign(body)))

	ev, err := ph.st.GetEvent(t.Context(), "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	require.NoError(t, err)
	assert.Equal(t, "pull_request", ev.Kind)
	assert.JSONEq(t, body, string(ev.Payload))
	assert.Nil(t, ev.ClaimedAt)
	assert.Nil(t, ev.DoneAt)
	assert.Len(t, ph.wakes, 1)
}

func TestRedeliveryIsDeduplicated(t *testing.T) {
	ph := newPGHarness(t)
	body := `{"ref":"refs/heads/main"}`
	for range 3 {
		require.Equal(t, http.StatusAccepted, ph.post("push", "same-delivery", body, sign(body)))
	}
	assert.Equal(t, 1, ph.count())
	assert.Len(t, ph.wakes, 1)
	assert.InDelta(t, 2, metricstest.Value(t, ph.metrics, "stackorder_webhook_duplicates_total"), 0)
	assert.InDelta(t, 3, metricstest.Value(t, ph.metrics, `stackorder_webhook_received_total{event="push"}`), 0)
}

func TestRejectedDeliveriesAreNotStored(t *testing.T) {
	ph := newPGHarness(t)
	cases := []struct {
		name       string
		event      string
		body       string
		signature  string
		wantStatus int
	}{
		{"missing signature", "push", `{}`, "", http.StatusUnauthorized},
		{"invalid signature", "push", `{}`, sign(`{"x":1}`), http.StatusUnauthorized},
		{"ping", "ping", `{"zen":"z"}`, sign(`{"zen":"z"}`), http.StatusOK},
		{"oversized", "push", jsonOfSize(webhook.MaxBodyBytes + 10), sign(jsonOfSize(webhook.MaxBodyBytes + 10)), http.StatusRequestEntityTooLarge},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantStatus, ph.post(tc.event, fmt.Sprintf("rejected-%d", i), tc.body, tc.signature))
		})
	}
	assert.Zero(t, ph.count())
	assert.Empty(t, ph.wakes)
}

func TestUnknownEventIsQueued(t *testing.T) {
	ph := newPGHarness(t)
	body := `{"action":"created"}`
	require.Equal(t, http.StatusAccepted, ph.post("sponsorship", "unknown-1", body, sign(body)))
	ev, err := ph.st.GetEvent(t.Context(), "unknown-1")
	require.NoError(t, err)
	assert.Equal(t, "sponsorship", ev.Kind)
}

func BenchmarkServeHTTP(b *testing.B) {
	st := pgtest.New(b)
	h := webhook.New(secret, st, nil, metrics.New(), nil)
	body := `{"action":"completed","workflow_run":{"id":1}}`
	sig := sign(body)
	b.ResetTimer()
	for i := range b.N {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
		req.Header.Set(gh.HeaderEvent, "workflow_run")
		req.Header.Set(gh.HeaderDelivery, fmt.Sprintf("bench-%d", i))
		req.Header.Set(gh.HeaderSignature, sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			b.Fatalf("status %d", rec.Code)
		}
	}
}
