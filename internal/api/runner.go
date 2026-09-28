package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func parseID(raw, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, notFound(fmt.Sprintf("%s %q not found", what, raw))
	}
	return id, nil
}

func (s *server) runURL(id string) string { return s.cfg.BaseURL + "/runs/" + id }

func (s *server) createRun(w http.ResponseWriter, r *http.Request, id identity) error {
	var req v1.CreateRunRequest
	if err := decodeJSON(r, &req, true); err != nil {
		return err
	}
	resp, err := s.runs.CreateRun(r.Context(), id.Principal, req)
	if err != nil {
		return err
	}
	if resp.Run != nil && resp.Run.HTMLURL == "" {
		resp.Run.HTMLURL = s.runURL(resp.Run.ID)
	}
	status := http.StatusCreated
	if resp.Existing {
		status = http.StatusOK
	}
	s.writeJSON(w, r, status, resp)
	return nil
}

func (s *server) uploadGraph(w http.ResponseWriter, r *http.Request, id identity) error {
	runID, err := parseID(r.PathValue("id"), "run")
	if err != nil {
		return err
	}
	var req v1.GraphUploadRequest
	if err := decodeJSON(r, &req, true); err != nil {
		return err
	}
	resp, err := s.runs.UploadGraph(r.Context(), id.Principal, runID.String(), req)
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, resp)
	return nil
}

func (s *server) recordResult(w http.ResponseWriter, r *http.Request, id identity) error {
	runID, err := parseID(r.PathValue("id"), "run")
	if err != nil {
		return err
	}
	var res v1.StackResult
	if err := decodeJSON(r, &res, true); err != nil {
		return err
	}
	out, err := s.runs.RecordResult(r.Context(), id.Principal, runID.String(), r.PathValue("key"), res)
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, out)
	return nil
}

func (s *server) recordCheck(w http.ResponseWriter, r *http.Request, id identity) error {
	runID, err := parseID(r.PathValue("id"), "run")
	if err != nil {
		return err
	}
	var verdict v1.CheckVerdict
	if err := decodeJSON(r, &verdict, true); err != nil {
		return err
	}
	out, err := s.runs.RecordCheck(r.Context(), id.Principal, runID.String(), r.PathValue("key"), r.PathValue("name"), verdict)
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, out)
	return nil
}

func (s *server) getRun(w http.ResponseWriter, r *http.Request, id identity) error {
	stored, _, err := s.visibleRun(r, id)
	if err != nil {
		return err
	}
	run, err := s.runs.GetRunForPrincipal(r.Context(), id.Principal, stored.ID.String())
	if err != nil {
		return err
	}
	if run.HTMLURL == "" {
		run.HTMLURL = s.runURL(run.ID)
	}
	s.writeJSON(w, r, http.StatusOK, run)
	return nil
}

func (s *server) visibleRun(r *http.Request, id identity) (store.Run, store.Repo, error) {
	runID, err := parseID(r.PathValue("id"), "run")
	if err != nil {
		return store.Run{}, store.Repo{}, err
	}
	missing := notFound(fmt.Sprintf("run %q not found", r.PathValue("id")))
	run, err := s.db.GetRun(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Run{}, store.Repo{}, missing
	}
	if err != nil {
		return store.Run{}, store.Repo{}, fmt.Errorf("get run: %w", err)
	}
	repo, err := s.db.GetRepo(r.Context(), run.RepoID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Run{}, store.Repo{}, missing
	}
	if err != nil {
		return store.Run{}, store.Repo{}, fmt.Errorf("get repository: %w", err)
	}
	visible := id.sees(repo.Account)
	if id.Kind == principal.OIDC {
		visible = strconv.FormatInt(repo.ID, 10) == id.Claims.RepositoryID
	}
	if !visible {
		return store.Run{}, store.Repo{}, missing
	}
	return run, repo, nil
}
