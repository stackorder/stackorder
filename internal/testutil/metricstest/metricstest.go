// Package metricstest reads samples back from a metrics.Registry in tests,
// through the same text exposition Prometheus scrapes.
package metricstest

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stackorder/stackorder/internal/metrics"
)

// Scrape returns every sample r exposes, keyed by series as it appears in
// the text exposition, such as `stackorder_runs_total{mode="plan",status="planned",trigger="push"}`.
// Labels appear sorted by name; histograms contribute their _bucket, _sum
// and _count series.
func Scrape(t testing.TB, r *metrics.Registry) map[string]float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metricstest: scrape answered %d", rec.Code)
	}
	out := map[string]float64{}
	for line := range strings.Lines(rec.Body.String()) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i <= 0 {
			t.Fatalf("metricstest: malformed line %q", line)
		}
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("metricstest: line %q: %v", line, err)
		}
		out[line[:i]] = v
	}
	return out
}

// Value returns the sample of series, or 0 when r does not expose it,
// which is what a counter or gauge that was never touched reads as.
func Value(t testing.TB, r *metrics.Registry, series string) float64 {
	t.Helper()
	return Scrape(t, r)[series]
}
