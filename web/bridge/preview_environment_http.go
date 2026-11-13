package bridge

import "net/http"

// previewRoutes registers the read-only preview-environments endpoint. It is a
// GET because it changes nothing: the handler reads the forge through the
// repository's registered credential and writes a report. It never creates a
// deployment, a status or an environment, and it never persists anything.
func (s *Server) previewRoutes() {
	s.mux.HandleFunc("GET /api/repos/{id}/preview-environments", s.repoPreviewEnvironments)
}

// repoPreviewEnvironments returns what a registered forge instance and its
// credential expose about the repository's preview deployments and test
// environments, scoped by the optional ref and sha query parameters.
//
// The body is not read: the repository comes from its registered record, never
// from the caller. Bearer auth needs no code here because every /api route is
// wrapped when the server has a token.
func (s *Server) repoPreviewEnvironments(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	repo, ok := s.fleet.ws.Repo(r.PathValue("id"))
	if !ok {
		writeErr(w, ErrUnknownRepo)
		return
	}
	query := r.URL.Query()
	report, err := s.fleet.PreviewEnvironments(r.Context(), repo, PreviewFilter{
		Ref: query.Get("ref"),
		SHA: query.Get("sha"),
	})
	if err != nil {
		// An unexpected in-process failure. writeErr is deliberately not used
		// here: its 502 default denotes a child-process failure, and the SPA
		// keys on the difference between that and 500.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}
