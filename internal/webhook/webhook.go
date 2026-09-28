// Package webhook receives GitHub App deliveries on POST /webhooks/github.
//
// The handler does as little as it can so GitHub gets its answer well
// inside 50 ms: it bounds the body, verifies the X-Hub-Signature-256 HMAC,
// answers ping, and otherwise writes the delivery to the events table
// keyed by its X-GitHub-Delivery id, which deduplicates redeliveries. It
// then wakes the worker pool. Every other decision belongs to the event
// handlers registered on internal/worker.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/gh"
	"github.com/stackorder/stackorder/internal/metrics"
)

const (
	// Pattern is the ServeMux pattern the handler is meant to be served on.
	Pattern = "POST /webhooks/github"
	// MaxBodyBytes is the largest delivery accepted; larger bodies get 413.
	MaxBodyBytes = 5 << 20

	insertTimeout = 10 * time.Second
)

var knownEvents = append([]string{gh.EventPing, gh.EventInstallation, gh.EventInstallationRepositories}, gh.DefaultEvents...)

// EventInserter persists a delivery and reports whether its id was new.
// *store.Store implements it.
type EventInserter interface {
	InsertEvent(ctx context.Context, id, kind string, payload json.RawMessage) (bool, error)
}

// Handler is the webhook endpoint. It is safe for concurrent use.
type Handler struct {
	secret  []byte
	events  EventInserter
	notify  func()
	metrics *metrics.Registry
	logger  *slog.Logger
}

// New returns a Handler verifying deliveries against secret and queueing
// them in events. notify is called after a new delivery is queued so
// workers wake at once; m and logger may be nil.
func New(secret []byte, events EventInserter, notify func(), m *metrics.Registry, logger *slog.Logger) *Handler {
	if notify == nil {
		notify = func() {}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Handler{secret: bytes.Clone(secret), events: events, notify: notify, metrics: m, logger: logger}
}

// ServeHTTP handles one delivery. It answers 401 for a missing or wrong
// signature, 400 for missing delivery headers or a body that is not JSON,
// 413 for a body over MaxBodyBytes, 200 {"ok":true} for ping and 202
// {"queued":bool} otherwise, where queued is false for a redelivery of an
// id already stored. Unknown event names are queued like any other.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "invalid", "webhooks are delivered with POST")
		return
	}
	event := r.Header.Get(gh.HeaderEvent)
	delivery := r.Header.Get(gh.HeaderDelivery)
	log := h.logger.With("delivery", delivery, "event", event)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			log.DebugContext(r.Context(), "webhook body too large", "limit", MaxBodyBytes)
			writeError(w, http.StatusRequestEntityTooLarge, "invalid", "payload exceeds 5 MB")
			return
		}
		log.DebugContext(r.Context(), "webhook body unreadable", "error", err)
		writeError(w, http.StatusBadRequest, "invalid", "payload could not be read")
		return
	}
	if !gh.VerifySignature(h.secret, body, r.Header.Get(gh.HeaderSignature)) {
		log.DebugContext(r.Context(), "webhook signature rejected")
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid "+gh.HeaderSignature)
		return
	}
	if event == "" || delivery == "" {
		log.DebugContext(r.Context(), "webhook headers missing")
		writeError(w, http.StatusBadRequest, "invalid", gh.HeaderEvent+" and "+gh.HeaderDelivery+" are required")
		return
	}
	h.metrics.WebhookReceived(eventLabel(event))
	if event == gh.EventPing {
		log.DebugContext(r.Context(), "webhook ping")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if !json.Valid(body) {
		log.DebugContext(r.Context(), "webhook payload is not JSON")
		writeError(w, http.StatusBadRequest, "invalid", "payload is not JSON")
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), insertTimeout)
	defer cancel()
	queued, err := h.events.InsertEvent(ctx, delivery, event, body)
	if err != nil {
		log.ErrorContext(ctx, "webhook not queued", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "delivery could not be queued")
		return
	}
	if queued {
		h.notify()
	} else {
		h.metrics.WebhookDuplicate()
	}
	log.DebugContext(ctx, "webhook received", "queued", queued, "bytes", len(body))
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": queued})
}

func eventLabel(event string) string {
	if slices.Contains(knownEvents, event) {
		return event
	}
	return metrics.Other
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, v1.Error{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
