package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/stackorder/stackorder/internal/artifacts"
	"github.com/stackorder/stackorder/internal/store"
)

var _ ArtifactReader = (*artifacts.S3Store)(nil)

func (s *server) planText(w http.ResponseWriter, r *http.Request, id identity) error {
	run, _, err := s.visibleRun(r, id)
	if err != nil {
		return err
	}
	key := r.PathValue("key")
	rows, err := s.db.GetRunStacks(r.Context(), run.ID)
	if err != nil {
		return fmt.Errorf("run stacks: %w", err)
	}
	var row *store.RunStack
	for i := range rows {
		if rows[i].Key == key {
			row = &rows[i]
			break
		}
	}
	switch {
	case row == nil:
		return notFound(fmt.Sprintf("stack %q is not part of run %s", key, run.ID))
	case row.PlanURL == "":
		return notFound(fmt.Sprintf("run %s keeps no full plan text of %s; its plan_text is all there is", run.ID, key))
	case s.artifacts == nil:
		return notFound("this server has no artifact bucket, so it serves no full plan text")
	}
	body, _, err := s.artifacts.Get(r.Context(), store.PlanTextArtifactKey(run.ID, key))
	if errors.Is(err, artifacts.ErrNotFound) {
		return notFound(fmt.Sprintf("the full plan text of %s in run %s is no longer in the artifact bucket", key, run.ID))
	}
	if err != nil {
		return fmt.Errorf("read plan text: %w", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:gosec
	return nil
}
