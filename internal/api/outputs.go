package api

import "net/http"

func (s *server) stackOutputs(w http.ResponseWriter, r *http.Request, id identity) error {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	q := r.URL.Query()
	out, err := s.runs.StackOutputs(r.Context(), id.Principal, repo, r.PathValue("key"), q.Get("run"), q.Get("reader"))
	if err != nil {
		return err
	}
	s.writeJSON(w, r, http.StatusOK, out)
	return nil
}
