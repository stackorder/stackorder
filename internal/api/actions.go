package api

import (
	"errors"
	"fmt"
	"net/http"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

func (s *server) requirePush(r *http.Request, id identity, repo store.Repo) error {
	if id.Kind != principal.Session {
		return nil
	}
	ok, err := s.runs.CanActOnRepo(r.Context(), id.Login, repo.ID)
	if err != nil {
		return fmt.Errorf("check push permission of %s on %s: %w", id.Login, repo.FullName, err)
	}
	if !ok {
		return forbidden(id.Login + " needs push permission on " + repo.FullName)
	}
	return nil
}

func (s *server) unlockStack(w http.ResponseWriter, r *http.Request, id identity) error {
	st, repo, err := s.visibleStack(r, id)
	if err != nil {
		return err
	}
	var req v1.UnlockRequest
	if err := decodeJSON(r, &req, false); err != nil {
		return err
	}
	if err := s.requirePush(r, id, repo); err != nil {
		return err
	}
	resp, err := s.runs.Unlock(r.Context(), id.Actor(), st.ID.String(), req)
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, resp)
	return nil
}

func (s *server) unlockByKey(w http.ResponseWriter, r *http.Request, id identity) error {
	var req v1.UnlockRequest
	if err := decodeJSON(r, &req, true); err != nil {
		return err
	}
	switch {
	case req.Repo == "":
		return &principal.InvalidError{Field: "repo", Reason: "is required"}
	case req.StackKey == "":
		return &principal.InvalidError{Field: "stack_key", Reason: "is required"}
	}
	if id.Kind == principal.Session {
		repo, err := s.db.GetRepoByName(r.Context(), req.Repo)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("get repository: %w", err)
		}
		if err != nil || !id.sees(repo.Account) {
			return notFound("repository " + req.Repo + " not found")
		}
		if err := s.requirePush(r, id, repo); err != nil {
			return err
		}
	}
	resp, err := s.runs.UnlockByKey(r.Context(), id.Actor(), req)
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, resp)
	return nil
}

func (s *server) rerun(w http.ResponseWriter, r *http.Request, id identity) error {
	run, repo, err := s.visibleRun(r, id)
	if err != nil {
		return err
	}
	var body struct{}
	if err := decodeJSON(r, &body, false); err != nil {
		return err
	}
	if err := s.requirePush(r, id, repo); err != nil {
		return err
	}
	out, err := s.runs.Rerun(r.Context(), id.Actor(), run.ID.String())
	if err != nil {
		return err
	}
	if out.HTMLURL == "" {
		out.HTMLURL = s.runURL(out.ID)
	}
	resp := v1.CreateRunResponse{RunID: out.ID, Status: out.Status, Existing: out.ID == run.ID.String(), Run: out}
	status := http.StatusCreated
	if resp.Existing {
		status = http.StatusOK
	}
	s.writeJSON(w, r, status, resp)
	return nil
}
