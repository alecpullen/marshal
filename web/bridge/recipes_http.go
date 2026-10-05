package bridge

import (
	"net/http"
)

func (s *Server) recipeList(w http.ResponseWriter, r *http.Request) {
	recs, err := s.fleet.recipes.List()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recs)
}

func (s *Server) recipeGet(w http.ResponseWriter, r *http.Request) {
	rec, err := s.fleet.recipes.Get(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) recipePut(w http.ResponseWriter, r *http.Request) {
	var rec Recipe
	if !decodeJSON(w, r, &rec) {
		return
	}
	// The name is the path's; a body that disagrees is a mistake.
	if rec.Name != "" && rec.Name != r.PathValue("name") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name in body does not match the path"})
		return
	}
	rec.Name, rec.Builtin = r.PathValue("name"), false
	if err := s.fleet.recipes.Put(rec); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditRecipeSaved, OwnerID: DefaultOwnerID, Detail: rec.Name})
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) recipeDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.fleet.recipes.Delete(name); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditRecipeDeleted, OwnerID: DefaultOwnerID, Detail: name})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) recipeCopy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	rec, err := s.fleet.recipes.Copy(r.PathValue("name"), body.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditRecipeSaved, OwnerID: DefaultOwnerID, Detail: rec.Name})
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) recipeRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string            `json:"project"`
		RepoID  string            `json:"repoId"`
		Ref     string            `json:"ref"`
		Inputs  map[string]string `json:"inputs"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RepoID == "" {
		if err := ValidateProjectRoot(body.Project); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	id, err := s.fleet.RunRecipe(r.Context(), r.PathValue("name"), RecipeRunRequest{
		Project: body.Project, RepoID: body.RepoID, Ref: body.Ref, Inputs: body.Inputs, Origin: OriginUI,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"agentId": id})
}

func (s *Server) recipeRoutes() {
	s.mux.HandleFunc("GET /api/recipes", s.recipeList)
	s.mux.HandleFunc("GET /api/recipes/{name}", s.recipeGet)
	s.mux.HandleFunc("PUT /api/recipes/{name}", s.recipePut)
	s.mux.HandleFunc("DELETE /api/recipes/{name}", s.recipeDelete)
	s.mux.HandleFunc("POST /api/recipes/{name}/copy", s.recipeCopy)
	s.mux.HandleFunc("POST /api/recipes/{name}/run", s.recipeRun)
}
