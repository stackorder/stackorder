package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func (s *server) createKey(w http.ResponseWriter, r *http.Request, id identity) error {
	var req v1.CreateAPIKeyRequest
	if err := decodeJSON(r, &req, true); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return &principal.InvalidError{Field: "name", Reason: "is required"}
	case utf8.RuneCountInString(name) > store.MaxAPIKeyNameLen:
		return &principal.InvalidError{Field: "name", Reason: fmt.Sprintf("must be at most %d characters", store.MaxAPIKeyNameLen)}
	}
	n := store.NewAPIKey{Name: name, CreatedBy: id.Actor(), ReadOnly: req.ReadOnly}
	if req.InstallationID != 0 {
		if _, err := s.db.GetInstallation(r.Context(), req.InstallationID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &principal.InvalidError{Field: "installation_id", Reason: fmt.Sprintf("%d is not an installation of this server", req.InstallationID)}
			}
			return fmt.Errorf("look up installation: %w", err)
		}
		n.InstallationID = &req.InstallationID
	}
	if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(s.now()) {
			return &principal.InvalidError{Field: "expires_at", Reason: "must be in the future"}
		}
		exp := req.ExpiresAt.UTC()
		n.ExpiresAt = &exp
	}
	secret, key, err := s.db.CreateAPIKey(r.Context(), n)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	s.writeJSON(w, r, http.StatusCreated, v1.CreatedAPIKey{APIKey: key.ToV1(), Secret: secret})
	return nil
}

func (s *server) listKeys(w http.ResponseWriter, r *http.Request, _ identity) error {
	keys, err := s.db.ListAPIKeys(r.Context())
	if err != nil {
		return fmt.Errorf("list api keys: %w", err)
	}
	out := v1.APIKeyList{Keys: make([]v1.APIKey, 0, len(keys))}
	for _, k := range keys {
		out.Keys = append(out.Keys, k.ToV1())
	}
	s.writeJSON(w, r, http.StatusOK, out)
	return nil
}

func (s *server) revokeKey(w http.ResponseWriter, r *http.Request, id identity) error {
	raw := r.PathValue("id")
	keyID, err := uuid.Parse(raw)
	if err != nil {
		return notFound("no such API key: " + raw)
	}
	key, err := s.db.RevokeAPIKey(r.Context(), keyID, id.Actor())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("no such API key: " + keyID.String())
		}
		return fmt.Errorf("revoke api key: %w", err)
	}
	s.writeJSON(w, r, http.StatusOK, key.ToV1())
	return nil
}
