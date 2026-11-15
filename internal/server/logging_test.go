package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceLogHandlerAddsSpanIDs(t *testing.T) {
	tr, _ := memoryTracing(t)
	var buf bytes.Buffer
	log := slog.New(newTraceLogHandler(slog.NewJSONHandler(&buf, nil)))

	ctx, sc := producerSpan(t, &Server{tracing: tr})
	log.InfoContext(ctx, "in a span")
	log.Info("outside any span")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 2)

	var withSpan map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &withSpan))
	assert.Equal(t, sc.TraceID().String(), withSpan["trace_id"])
	assert.Equal(t, sc.SpanID().String(), withSpan["span_id"])

	var withoutSpan map[string]any
	require.NoError(t, json.Unmarshal(lines[1], &withoutSpan))
	assert.NotContains(t, withoutSpan, "trace_id")
	assert.NotContains(t, withoutSpan, "span_id")
}

func TestTraceLogHandlerKeepsWithAttrsAndGroups(t *testing.T) {
	tr, _ := memoryTracing(t)
	var buf bytes.Buffer
	log := slog.New(newTraceLogHandler(slog.NewJSONHandler(&buf, nil))).With("component", "worker")

	ctx, sc := producerSpan(t, &Server{tracing: tr})
	log.InfoContext(ctx, "handled")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec))
	assert.Equal(t, "worker", rec["component"], "wrapped attributes survive")
	assert.Equal(t, sc.TraceID().String(), rec["trace_id"])
}
