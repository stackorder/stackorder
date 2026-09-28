package metrics

import (
	"net/http"
	"strconv"
	"time"
)

// HTTPMiddleware returns middleware that counts requests to route in
// http_requests_total and observes their latency in
// http_request_duration_seconds. route should be the ServeMux pattern the
// handler is registered under, never the raw request path, so the label
// set stays bounded. A nil Registry returns handlers unchanged.
func (r *Registry) HTTPMiddleware(route string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if r == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, req)
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			method := normalizeMethod(req.Method)
			r.httpRequests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
			r.httpDuration.WithLabelValues(route, method).Observe(time.Since(start).Seconds())
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= http.StatusOK {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
