package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/stackorder/stackorder/internal/version"
)

// ServiceName is the service.name resource attribute of exported spans.
const ServiceName = "stackorder-server"

// Span attributes the server sets.
const (
	AttrRunID    = attribute.Key("stackorder.run_id")
	AttrEvent    = attribute.Key("stackorder.event")
	AttrDelivery = attribute.Key("stackorder.delivery")
	AttrJob      = attribute.Key("stackorder.job")
	AttrJobID    = attribute.Key("stackorder.job_id")
)

const (
	tracerName = "github.com/stackorder/stackorder/internal/server"
	tracesPath = "/v1/traces"
)

var untraced = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}

type tracing struct {
	provider   *sdktrace.TracerProvider
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

func newTracing(ctx context.Context, endpoint string) (*tracing, error) {
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(tracesURL(endpoint)))
	if err != nil {
		return nil, fmt.Errorf("server: OTLP trace exporter for %s: %w", endpoint, err)
	}
	return newTracingWith(sdktrace.WithBatcher(exporter))
}

func newTracingWith(export sdktrace.TracerProviderOption) (*tracing, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(ServiceName),
		semconv.ServiceVersion(version.Version),
	))
	if err != nil {
		return nil, fmt.Errorf("server: trace resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(export, sdktrace.WithResource(res))
	return &tracing{
		provider:   provider,
		tracer:     provider.Tracer(tracerName, trace.WithInstrumentationVersion(version.Version)),
		propagator: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
	}, nil
}

func tracesURL(endpoint string) string {
	return strings.TrimRight(endpoint, "/") + tracesPath
}

func (t *tracing) wrap(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, ServiceName,
		otelhttp.WithTracerProvider(t.provider),
		otelhttp.WithPropagators(t.propagator),
		otelhttp.WithFilter(func(r *http.Request) bool { return !untraced[r.URL.Path] }),
	)
}

func (t *tracing) shutdown(ctx context.Context) error {
	if err := t.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("server: flush traces: %w", err)
	}
	return nil
}

func (s *Server) tracer() trace.Tracer {
	if s.tracing == nil {
		return noop.NewTracerProvider().Tracer(tracerName)
	}
	return s.tracing.tracer
}

func (s *Server) instrument(route string) func(http.Handler) http.Handler {
	count := s.metrics.HTTPMiddleware(route)
	return func(next http.Handler) http.Handler {
		counted := count(next)
		if s.tracing == nil {
			return counted
		}
		runRoute := strings.Contains(route, "/runs/{id}")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			span := trace.SpanFromContext(r.Context())
			span.SetName(route)
			span.SetAttributes(semconv.HTTPRoute(route))
			if runRoute {
				span.SetAttributes(AttrRunID.String(r.PathValue("id")))
			}
			counted.ServeHTTP(w, r)
		})
	}
}

func (s *Server) traced(ctx context.Context, name string, fn func(context.Context) error, attrs ...attribute.KeyValue) error {
	ctx, span := s.tracer().Start(ctx, name, trace.WithAttributes(attrs...))
	defer span.End()
	err := fn(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// injectTrace serializes the W3C trace context of ctx, so a queued row can
// carry it to the worker. It returns nil when tracing is off or ctx carries
// no span, which leaves the row untraced.
func (s *Server) injectTrace(ctx context.Context) json.RawMessage {
	if s.tracing == nil || !trace.SpanContextFromContext(ctx).IsValid() {
		return nil
	}
	carrier := propagation.MapCarrier{}
	s.tracing.propagator.Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]string(carrier))
	if err != nil {
		return nil
	}
	return raw
}

// extractTrace returns ctx with the trace context stored in raw as its
// remote parent, so a span started from it continues the trace that queued
// the row. It returns ctx unchanged when tracing is off or raw is empty or
// malformed, as for rows queued before the upgrade or with tracing off.
func (s *Server) extractTrace(ctx context.Context, raw json.RawMessage) context.Context {
	if s.tracing == nil || len(raw) == 0 {
		return ctx
	}
	var carrier map[string]string
	if err := json.Unmarshal(raw, &carrier); err != nil || len(carrier) == 0 {
		return ctx
	}
	return s.tracing.propagator.Extract(ctx, propagation.MapCarrier(carrier))
}

// traceLogHandler wraps a slog.Handler to add trace_id and span_id to every
// record whose context carries a recording span, so logs can be joined with
// the traces of the same request, event or job. Records logged without such
// a context, as before tracing starts or outside any span, are unchanged.
//
// The ids stay at the top level of the record, so they remain joinable by
// key even when the logger groups its other attributes: they are stamped onto
// the record when no group is open, and otherwise added to the base handler
// before any group opened with WithGroup.
type traceLogHandler struct {
	base     slog.Handler
	prepared slog.Handler
	mods     []func(slog.Handler) slog.Handler
	grouped  bool
}

func newTraceLogHandler(h slog.Handler) slog.Handler {
	return traceLogHandler{base: h, prepared: h}
}

func (h traceLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.prepared.Enabled(ctx, level)
}

func (h traceLogHandler) Handle(ctx context.Context, rec slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return h.prepared.Handle(ctx, rec)
	}
	ids := []slog.Attr{
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	}
	if !h.grouped {
		stamped := rec.Clone()
		stamped.AddAttrs(ids...)
		return h.prepared.Handle(ctx, stamped)
	}
	handler := h.base.WithAttrs(ids)
	for _, mod := range h.mods {
		handler = mod(handler)
	}
	return handler.Handle(ctx, rec)
}

func (h traceLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.extend(false, func(inner slog.Handler) slog.Handler { return inner.WithAttrs(attrs) })
}

func (h traceLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.extend(true, func(inner slog.Handler) slog.Handler { return inner.WithGroup(name) })
}

func (h traceLogHandler) extend(group bool, mod func(slog.Handler) slog.Handler) traceLogHandler {
	mods := make([]func(slog.Handler) slog.Handler, len(h.mods)+1)
	copy(mods, h.mods)
	mods[len(h.mods)] = mod
	return traceLogHandler{base: h.base, prepared: mod(h.prepared), mods: mods, grouped: h.grouped || group}
}

func payloadRunID(payload json.RawMessage) (string, bool) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil || p.RunID == "" {
		return "", false
	}
	return p.RunID, true
}
