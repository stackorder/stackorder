package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/metrics"
	"github.com/stackorder/stackorder/internal/testutil/metricstest"
	"github.com/stackorder/stackorder/internal/webhook"
)

var secret = []byte("s3cret")

type inserted struct {
	id, kind string
	payload  json.RawMessage
}

type fakeInserter struct {
	mu   sync.Mutex
	rows []inserted
	seen map[string]bool
	err  error

	ctxErr      error
	hasDeadline bool
}

func (f *fakeInserter) InsertEvent(ctx context.Context, id, kind string, payload json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxErr = ctx.Err()
	_, f.hasDeadline = ctx.Deadline()
	if f.err != nil {
		return false, f.err
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[id] {
		return false, nil
	}
	f.seen[id] = true
	f.rows = append(f.rows, inserted{id: id, kind: kind, payload: payload})
	return true, nil
}

type harness struct {
	h        *webhook.Handler
	store    *fakeInserter
	metrics  *metrics.Registry
	notified int
}

func newHarness(key []byte) *harness {
	hs := &harness{store: &fakeInserter{}, metrics: metrics.New()}
	hs.h = webhook.New(key, hs.store, func() { hs.notified++ }, hs.metrics, nil)
	return hs
}

type delivery struct {
	method    string
	event     string
	id        string
	body      string
	signature string
	unsigned  bool
}

func (hs *harness) send(d delivery) *httptest.ResponseRecorder {
	if d.method == "" {
		d.method = http.MethodPost
	}
	req := httptest.NewRequest(d.method, "/webhooks/github", strings.NewReader(d.body))
	if d.event != "" {
		req.Header.Set(gh.HeaderEvent, d.event)
	}
	if d.id != "" {
		req.Header.Set(gh.HeaderDelivery, d.id)
	}
	switch {
	case d.signature != "":
		req.Header.Set(gh.HeaderSignature, d.signature)
	case !d.unsigned:
		req.Header.Set(gh.HeaderSignature, gh.SignPayload(secret, []byte(d.body)))
	}
	rec := httptest.NewRecorder()
	hs.h.ServeHTTP(rec, req)
	return rec
}

func TestServeHTTP(t *testing.T) {
	cases := []struct {
		name       string
		delivery   delivery
		wantStatus int
		wantBody   string
		wantStored bool
	}{
		{
			name:       "valid delivery is queued",
			delivery:   delivery{event: "push", id: "d-1", body: `{"ref":"refs/heads/main"}`},
			wantStatus: http.StatusAccepted,
			wantBody:   `{"queued":true}`,
			wantStored: true,
		},
		{
			name:       "unknown event is still queued",
			delivery:   delivery{event: "sponsorship", id: "d-2", body: `{}`},
			wantStatus: http.StatusAccepted,
			wantBody:   `{"queued":true}`,
			wantStored: true,
		},
		{
			name:       "ping is answered and not queued",
			delivery:   delivery{event: "ping", id: "d-3", body: `{"zen":"Keep it logically awesome."}`},
			wantStatus: http.StatusOK,
			wantBody:   `{"ok":true}`,
		},
		{
			name:       "unsigned ping is refused like any delivery",
			delivery:   delivery{event: "ping", id: "d-3b", body: `{"zen":"z"}`, unsigned: true},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"code":"unauthorized","message":"missing or invalid X-Hub-Signature-256"}`,
		},
		{
			name:       "signature is checked before the headers",
			delivery:   delivery{body: `{}`, unsigned: true},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"code":"unauthorized","message":"missing or invalid X-Hub-Signature-256"}`,
		},
		{
			name:       "missing signature",
			delivery:   delivery{event: "push", id: "d-4", body: `{}`, unsigned: true},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"code":"unauthorized","message":"missing or invalid X-Hub-Signature-256"}`,
		},
		{
			name:       "signature under another secret",
			delivery:   delivery{event: "push", id: "d-5", body: `{}`, signature: gh.SignPayload([]byte("other"), []byte(`{}`))},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"code":"unauthorized","message":"missing or invalid X-Hub-Signature-256"}`,
		},
		{
			name:       "signature of another body",
			delivery:   delivery{event: "push", id: "d-6", body: `{"a":2}`, signature: gh.SignPayload(secret, []byte(`{"a":1}`))},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"code":"unauthorized","message":"missing or invalid X-Hub-Signature-256"}`,
		},
		{
			name:       "sha1 signature is not enough",
			delivery:   delivery{event: "push", id: "d-7", body: `{}`, signature: "sha1=0123456789abcdef0123456789abcdef01234567"},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"code":"unauthorized","message":"missing or invalid X-Hub-Signature-256"}`,
		},
		{
			name:       "missing event header",
			delivery:   delivery{id: "d-8", body: `{}`},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"invalid","message":"X-GitHub-Event and X-GitHub-Delivery are required"}`,
		},
		{
			name:       "missing delivery header",
			delivery:   delivery{event: "push", body: `{}`},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"invalid","message":"X-GitHub-Event and X-GitHub-Delivery are required"}`,
		},
		{
			name:       "body that is not JSON",
			delivery:   delivery{event: "push", id: "d-9", body: `payload=%7B%7D`},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"invalid","message":"payload is not JSON"}`,
		},
		{
			name:       "oversized body",
			delivery:   delivery{event: "push", id: "d-10", body: jsonOfSize(webhook.MaxBodyBytes + 1)},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"code":"invalid","message":"payload exceeds 5 MB"}`,
		},
		{
			name:       "body of exactly the limit",
			delivery:   delivery{event: "push", id: "d-11", body: jsonOfSize(webhook.MaxBodyBytes)},
			wantStatus: http.StatusAccepted,
			wantBody:   `{"queued":true}`,
			wantStored: true,
		},
		{
			name:       "other methods",
			delivery:   delivery{method: http.MethodGet, event: "push", id: "d-12"},
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   `{"code":"invalid","message":"webhooks are delivered with POST"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := newHarness(secret)
			rec := hs.send(tc.delivery)
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.JSONEq(t, tc.wantBody, rec.Body.String())
			if !tc.wantStored {
				assert.Empty(t, hs.store.rows)
				assert.Zero(t, hs.notified)
				return
			}
			require.Len(t, hs.store.rows, 1)
			row := hs.store.rows[0]
			assert.Equal(t, tc.delivery.id, row.id)
			assert.Equal(t, tc.delivery.event, row.kind)
			assert.Equal(t, tc.delivery.body, string(row.payload))
			assert.Equal(t, 1, hs.notified, "workers are woken once")
		})
	}
}

func TestDuplicateDeliveryIsAcknowledged(t *testing.T) {
	hs := newHarness(secret)
	first := hs.send(delivery{event: "pull_request", id: "dup", body: `{"action":"opened"}`})
	second := hs.send(delivery{event: "pull_request", id: "dup", body: `{"action":"opened"}`})

	assert.Equal(t, http.StatusAccepted, first.Code)
	assert.JSONEq(t, `{"queued":true}`, first.Body.String())
	assert.Equal(t, http.StatusAccepted, second.Code, "GitHub must not see a redelivery as a failure")
	assert.JSONEq(t, `{"queued":false}`, second.Body.String())
	assert.Len(t, hs.store.rows, 1)
	assert.Equal(t, 1, hs.notified, "nothing new to wake workers for")
	assert.InDelta(t, 1, metricstest.Value(t, hs.metrics, "stackorder_webhook_duplicates_total"), 0)
	assert.InDelta(t, 2, metricstest.Value(t, hs.metrics, `stackorder_webhook_received_total{event="pull_request"}`), 0)
}

func TestEmptySecretRejectsEverything(t *testing.T) {
	hs := newHarness(nil)
	rec := hs.send(delivery{event: "push", id: "d", body: `{}`, signature: gh.SignPayload(nil, []byte(`{}`))})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, hs.store.rows)
}

func TestSecretIsCopied(t *testing.T) {
	key := []byte("s3cret")
	hs := newHarness(key)
	key[0] = 'X'
	rec := hs.send(delivery{event: "push", id: "d", body: `{}`})
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestStoreFailure(t *testing.T) {
	hs := newHarness(secret)
	hs.store.err = errors.New("connection refused")
	rec := hs.send(delivery{event: "push", id: "d", body: `{}`})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"code":"internal","message":"delivery could not be queued"}`, rec.Body.String())
	assert.Zero(t, hs.notified)
}

func TestInsertSurvivesClientDisconnect(t *testing.T) {
	hs := newHarness(secret)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/webhooks/github", strings.NewReader(`{}`))
	req.Header.Set(gh.HeaderEvent, "push")
	req.Header.Set(gh.HeaderDelivery, "gone")
	req.Header.Set(gh.HeaderSignature, gh.SignPayload(secret, []byte(`{}`)))
	hs.h.ServeHTTP(httptest.NewRecorder(), req)

	require.Len(t, hs.store.rows, 1, "a verified delivery is stored even if GitHub hung up")
	require.NoError(t, hs.store.ctxErr)
	assert.True(t, hs.store.hasDeadline, "the insert is still bounded")
}

func TestMetricsLabelUnknownEventsAsOther(t *testing.T) {
	hs := newHarness(secret)
	hs.send(delivery{event: "push", id: "a", body: `{}`})
	hs.send(delivery{event: "ping", id: "b", body: `{}`})
	hs.send(delivery{event: "made_up_event", id: "c", body: `{}`})
	hs.send(delivery{event: "push", id: "d", body: `{}`, unsigned: true})

	samples := metricstest.Scrape(t, hs.metrics)
	assert.InDelta(t, 1, samples[`stackorder_webhook_received_total{event="push"}`], 0, "unsigned deliveries are not counted")
	assert.InDelta(t, 1, samples[`stackorder_webhook_received_total{event="ping"}`], 0)
	assert.InDelta(t, 1, samples[`stackorder_webhook_received_total{event="other"}`], 0)
	_, raw := samples[`stackorder_webhook_received_total{event="made_up_event"}`]
	assert.False(t, raw, "header values never become label values")
}

func TestNilDependencies(t *testing.T) {
	store := &fakeInserter{}
	h := webhook.New(secret, store, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(`{}`))
	req.Header.Set(gh.HeaderEvent, "push")
	req.Header.Set(gh.HeaderDelivery, "x")
	req.Header.Set(gh.HeaderSignature, gh.SignPayload(secret, []byte(`{}`)))
	rec := httptest.NewRecorder()
	require.NotPanics(t, func() { h.ServeHTTP(rec, req) })
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestErrorBodyIsV1Error(t *testing.T) {
	hs := newHarness(secret)
	rec := hs.send(delivery{event: "push", id: "x", body: `{}`, unsigned: true})
	var e v1.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	assert.Equal(t, "unauthorized", e.Code)
}

func jsonOfSize(n int) string {
	const prefix, suffix = `{"pad":"`, `"}`
	return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
}
