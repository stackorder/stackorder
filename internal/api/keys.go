package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

const maxAPIKeyNameLen = 100

func (s *server) createKey(w http.ResponseWriter, r *http.Request, id identity) error {
	var req v1.CreateAPIKeyRequest
	if err := decodeJSON(r, &req, true); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return &principal.InvalidError{Field: "name", Reason: "is required"}
	case len(name) > maxAPIKeyNameLen:
		return &principal.InvalidError{Field: "name", Reason: fmt.Sprintf("must be at most %d characters", maxAPIKeyNameLen)}
	}
	secret, key, err := s.db.CreateAPIKey(r.Context(), name, id.Actor())
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	s.auditKey(r.Context(), id.Actor(), "api_key_created", key)
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
	if err := s.db.RevokeAPIKey(r.Context(), keyID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("no such API key: " + keyID.String())
		}
		return fmt.Errorf("revoke api key: %w", err)
	}
	key, err := s.db.GetAPIKey(r.Context(), keyID)
	if err != nil {
		return fmt.Errorf("load revoked api key: %w", err)
	}
	s.auditKey(r.Context(), id.Actor(), "api_key_revoked", key)
	s.writeJSON(w, r, http.StatusOK, key.ToV1())
	return nil
}

func (s *server) auditKey(ctx context.Context, actor, action string, key store.APIKey) {
	_, err := s.db.RecordAudit(ctx, store.AuditEntry{
		Actor:   actor,
		Action:  action,
		Target:  key.Name,
		Details: map[string]any{"key_id": key.ID.String(), "prefix": key.Prefix},
	})
	if err != nil {
		s.log.WarnContext(ctx, "record audit", "action", action, "target", key.Name, "error", err)
	}
}
