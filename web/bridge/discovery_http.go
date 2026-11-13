package bridge

import "net/http"

// discoveryRoutes registers the read-only discovery endpoint. Discovery is a
// POST because it may be expensive, not because it changes anything: the
// handler only reads the forge, the credential and the linked checkout, and
// never creates a project, an agent, a factory or a work item.
func (s *Server) discoveryRoutes() {
	s.mux.HandleFunc("POST /api/repos/{id}/discover", s.discoverRepo)
}

// discoverRepo returns one repository's capability summary and the report that
// explains it, so a caller can never observe capabilities without the facts,
// recommendations and gaps behind them.
//
// The request body is empty: the repository comes from its registered record,
// never from the caller. Bearer auth needs no code here because every /api
// route is wrapped when the server has a token.
func (s *Server) discoverRepo(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	repo, ok := s.fleet.ws.Repo(r.PathValue("id"))
	if !ok {
		writeErr(w, ErrUnknownRepo)
		return
	}
	report, err := s.fleet.DiscoverRepo(r.Context(), repo)
	if err != nil {
		// An unexpected in-process failure. writeErr is deliberately not used
		// here: its 502 default denotes a child-process failure, and the SPA
		// keys on the difference between that and 500.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}
