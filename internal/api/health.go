package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/stackorder/stackorder/internal/oidc"
	"github.com/stackorder/stackorder/internal/version"
)

const readyTimeout = 2 * time.Second

type healthBody struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	SetupMode bool   `json:"setup_mode,omitempty"`
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, healthBody{Status: "ok", Version: version.Version, SetupMode: s.cfg.SetupMode})
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		s.log.WarnContext(r.Context(), "readiness check failed", "request_id", requestIDOf(r), "error", err.Error())
		s.writeError(w, r, unavailable("the database is unreachable"))
		return
	}
	s.writeJSON(w, r, http.StatusOK, healthBody{Status: "ok", Version: version.Version, SetupMode: s.cfg.SetupMode})
}

func (s *server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if s.cfg.MetricsToken != "" {
		token, ok := oidc.BearerToken(r)
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.MetricsToken)) != 1 {
			s.writeError(w, r, unauthorized("GET /metrics requires the metrics bearer token"))
			return
		}
	}
	if s.metrics == nil {
		s.writeError(w, r, notFound("metrics are not enabled"))
		return
	}
	s.metrics.ServeHTTP(w, r)
}
