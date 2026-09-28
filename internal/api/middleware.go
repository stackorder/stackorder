package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/stackorder/stackorder/internal/principal"
)

const (
	requestIDHeader = "X-Request-Id"
	maxRequestID    = 128
)

type ctxKey int

const requestInfoKey ctxKey = iota

type requestInfo struct {
	id    string
	route string
	kind  principal.Kind
}

func infoOf(r *http.Request) *requestInfo {
	if info, ok := r.Context().Value(requestInfoKey).(*requestInfo); ok {
		return info
	}
	return &requestInfo{}
}

func requestIDOf(r *http.Request) string { return infoOf(r).id }

type statusWriter struct {
	http.ResponseWriter
	csp    string
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		h := w.Header()
		ct := h.Get("Content-Type")
		if (ct == "" || strings.HasPrefix(ct, "text/html")) && h.Get("Content-Security-Policy") == "" {
			h.Set("Content-Security-Policy", w.csp)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestID {
		return false
	}
	for i := range len(id) {
		if c := id[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

func (s *server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		info := &requestInfo{id: id}
		r = r.WithContext(context.WithValue(r.Context(), requestInfoKey, info))
		sw := &statusWriter{ResponseWriter: w, csp: s.csp}
		sw.Header().Set(requestIDHeader, id)

		defer func() {
			p := recover()
			if p != nil {
				if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					s.access(r, info, sw, start)
					panic(p)
				}
				s.log.ErrorContext(r.Context(), "panic serving request",
					"request_id", id, "method", r.Method, "path", r.URL.Path,
					"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				if sw.status == 0 {
					e := newError(http.StatusInternalServerError, codeInternal, "internal server error")
					s.writeJSON(sw, r, e.status, e.body)
				}
			}
			s.access(r, info, sw, start)
		}()
		next.ServeHTTP(sw, r)
	})
}

func (s *server) access(r *http.Request, info *requestInfo, sw *statusWriter, start time.Time) {
	status := sw.status
	if status == 0 {
		status = http.StatusOK
	}
	route := info.route
	if route == "" {
		route = "unmatched"
	}
	kind := string(info.kind)
	if kind == "" {
		kind = "none"
	}
	s.log.InfoContext(r.Context(), "http request",
		"request_id", info.id, "method", r.Method, "route", route, "status", status,
		"duration_ms", s.now().Sub(start).Milliseconds(), "bytes", sw.bytes, "principal", kind)
}

func (s *server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *server) handle(mux *http.ServeMux, pattern string, limit int64, h http.Handler) {
	if limit > 0 {
		h = limitBody(limit, h)
	}
	if s.instrument != nil {
		h = s.instrument(pattern)(h)
	}
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		infoOf(r).route = pattern
		h.ServeHTTP(w, r)
	}))
}
