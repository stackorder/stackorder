package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/stackorder/stackorder/internal/store"
	"github.com/stackorder/stackorder/internal/worker"
)

func producerSpan(t *testing.T, s *Server) (context.Context, trace.SpanContext) {
	t.Helper()
	ctx, span := s.tracing.tracer.Start(t.Context(), "producer")
	t.Cleanup(func() { span.End() })
	return ctx, span.SpanContext()
}

func TestInjectTraceRoundTripsThroughAQueuedRow(t *testing.T) {
	tr, _ := memoryTracing(t)
	s := &Server{tracing: tr}
	ctx, sc := producerSpan(t, s)

	raw := s.injectTrace(ctx)
	require.NotEmpty(t, raw, "a valid span is captured")

	var carrier map[string]string
	require.NoError(t, json.Unmarshal(raw, &carrier))
	assert.Contains(t, carrier, "traceparent")
	assert.Contains(t, carrier["traceparent"], sc.TraceID().String())

	out := s.extractTrace(context.Background(), raw)
	assert.Equal(t, sc.TraceID(), trace.SpanContextFromContext(out).TraceID())
	assert.Equal(t, sc.SpanID(), trace.SpanContextFromContext(out).SpanID())
}

func TestInjectTraceIsEmptyWithoutASpanOrTracing(t *testing.T) {
	tr, _ := memoryTracing(t)
	assert.Nil(t, (&Server{tracing: tr}).injectTrace(context.Background()), "no span means nothing to carry")

	off := &Server{}
	assert.Nil(t, off.injectTrace(context.Background()))
	ctx := context.Background()
	assert.Equal(t, ctx, off.extractTrace(ctx, json.RawMessage(`{"traceparent":"x"}`)), "tracing off leaves the context untouched")
}

func TestExtractTraceIgnoresEmptyOrMalformedContext(t *testing.T) {
	tr, _ := memoryTracing(t)
	s := &Server{tracing: tr}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{}`), json.RawMessage(`not json`)} {
		out := s.extractTrace(context.Background(), raw)
		assert.False(t, trace.SpanContextFromContext(out).IsValid(), "row with %q yields a root span", string(raw))
	}
}

func TestWorkerEventSpanContinuesTheWebhookTrace(t *testing.T) {
	tr, exp := memoryTracing(t)
	svc := &fakeRuns{}
	s := &Server{tracing: tr, log: slog.New(slog.DiscardHandler)}
	r := &registry{events: map[string]worker.EventHandler{}, jobs: map[string]worker.JobHandler{}}
	s.registerOn(r, svc)

	ctx, sc := producerSpan(t, s)
	raw := s.injectTrace(ctx)
	require.NoError(t, r.events["push"](t.Context(), store.Event{ID: "d-1", Kind: "push", Payload: json.RawMessage(`{}`), TraceContext: raw}))

	span := spanNamed(t, exp, "event push")
	assert.Equal(t, sc.TraceID(), span.SpanContext.TraceID(), "the worker span joins the webhook trace")
	assert.Equal(t, sc.SpanID(), span.Parent.SpanID(), "the webhook span is its parent")
}

func TestWorkerJobSpanContinuesTheEnqueuingTrace(t *testing.T) {
	tr, exp := memoryTracing(t)
	svc := &fakeRuns{}
	s := &Server{tracing: tr, log: slog.New(slog.DiscardHandler)}
	r := &registry{events: map[string]worker.EventHandler{}, jobs: map[string]worker.JobHandler{}}
	s.registerOn(r, svc)

	ctx, sc := producerSpan(t, s)
	raw := s.injectTrace(ctx)
	require.NoError(t, r.jobs[jobKinds[0]](t.Context(), store.Job{Kind: jobKinds[0], Payload: json.RawMessage(`{}`), TraceContext: raw}))

	span := spanNamed(t, exp, "job "+jobKinds[0])
	assert.Equal(t, sc.TraceID(), span.SpanContext.TraceID())
	assert.Equal(t, sc.SpanID(), span.Parent.SpanID())
}

func TestWorkerSpanIsRootWithoutStoredTrace(t *testing.T) {
	tr, exp := memoryTracing(t)
	svc := &fakeRuns{}
	s := &Server{tracing: tr, log: slog.New(slog.DiscardHandler)}
	r := &registry{events: map[string]worker.EventHandler{}, jobs: map[string]worker.JobHandler{}}
	s.registerOn(r, svc)

	require.NoError(t, r.events["push"](t.Context(), store.Event{ID: "legacy", Kind: "push", Payload: json.RawMessage(`{}`)}))
	span := spanNamed(t, exp, "event push")
	assert.False(t, span.Parent.IsValid(), "a row queued before the upgrade keeps a root span")
}
