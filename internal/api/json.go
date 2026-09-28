package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

const (
	maxBodyBytes  = 1 << 20
	maxGraphBytes = 8 << 20
)

func decodeJSON(r *http.Request, v any, required bool) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || (mt != "application/json" && !strings.HasSuffix(mt, "+json")) {
			return invalid("the request body must be application/json")
		}
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			if required {
				return invalid("a JSON request body is required")
			}
			return nil
		}
		return bodyError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return bodyError(err)
		}
		return invalid("the request body has data after the JSON value")
	}
	return nil
}

func bodyError(err error) error {
	var (
		tooLarge *http.MaxBytesError
		typeErr  *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &tooLarge):
		return newError(http.StatusRequestEntityTooLarge, codeInvalid, fmt.Sprintf("the request body exceeds %d bytes", tooLarge.Limit))
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			field = "body"
		}
		return invalid(fmt.Sprintf("field %s: cannot use a JSON %s as %s", field, typeErr.Value, typeErr.Type))
	}
	return invalid("the request body is not valid JSON: " + err.Error())
}

func limitBody(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.writeError(w, r, fmt.Errorf("encode response: %w", err))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(append(data, '\n'))
	}
}

func (s *server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	e, unexpected := toAPIError(err)
	if unexpected {
		s.log.ErrorContext(r.Context(), "request failed",
			"request_id", requestIDOf(r), "method", r.Method, "path", r.URL.Path,
			"status", e.status, "error", err.Error())
	}
	if e.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="stackorder"`)
	}
	s.writeJSON(w, r, e.status, e.body)
}
