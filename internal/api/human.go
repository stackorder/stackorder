package api

import (
	"net/http"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
)

func (s *server) me(w http.ResponseWriter, r *http.Request, id identity) error {
	if id.Kind == principal.APIKey {
		s.writeJSON(w, r, http.StatusOK, v1.Whoami{Login: id.Actor(), Orgs: []string{}, Admin: true})
		return nil
	}
	s.writeJSON(w, r, http.StatusOK, id.session.ToV1())
	return nil
}
