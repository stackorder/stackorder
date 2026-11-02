package api

import (
	"errors"
	"fmt"
	"net/http"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/store"
)

func (s *server) stackResources(w http.ResponseWriter, r *http.Request, id identity) error {
	st, _, err := s.visibleStack(r, id)
	if err != nil {
		return err
	}
	rec, err := s.db.GetStackResources(r.Context(), st.ID)
	if errors.Is(err, store.ErrNotFound) {
		s.writeJSON(w, r, http.StatusOK, v1.StackResources{Items: []v1.StackResource{}})
		return nil
	}
	if err != nil {
		return fmt.Errorf("stack resources: %w", err)
	}
	out := rec.ToV1()
	addresses := make([]string, len(out.Items))
	for i, it := range out.Items {
		addresses[i] = it.Address
	}
	changes, err := s.db.StackResourceChanges(r.Context(), st.ID, addresses)
	if err != nil {
		return fmt.Errorf("stack resource changes: %w", err)
	}
	byAddress := make(map[string]v1.ResourceChange, len(changes))
	for _, c := range changes {
		byAddress[c.Address] = c.ToV1()
	}
	for i := range out.Items {
		if c, ok := byAddress[out.Items[i].Address]; ok {
			out.Items[i].LastChange = &c
		}
	}
	s.writeJSON(w, r, http.StatusOK, out)
	return nil
}
