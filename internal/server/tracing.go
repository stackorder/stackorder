package server

import (
	"context"
	"encoding/json"
	"fmt"
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

func payloadRunID(payload json.RawMessage) (string, bool) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil || p.RunID == "" {
		return "", false
	}
	return p.RunID, true
}
