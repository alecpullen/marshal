package bridge

import (
	"errors"
	"net/http"
	"strings"
)

// secretRef normalises the {ref...} path value: the "vault:" prefix is
// optional on the wire and validated either way.
func secretRefFromPath(raw string) (string, error) {
	if !strings.HasPrefix(raw, secretRefPrefix) {
		raw = secretRefPrefix + raw
	}
	return ParseSecretRef(raw)
}

// secretsStatus reports the backend and whether it answers. Health is a
// List on the owner root, which every backend supports.
func (s *Server) secretsStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	p := s.fleet.secrets
	out := map[string]any{"backend": p.Name(), "healthy": true}
	if _, err := p.List(r.Context(), DefaultOwnerID, ""); err != nil {
		out["healthy"] = false
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// listSecrets returns refs only; a value has no route out.
func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	prefix := strings.TrimPrefix(r.URL.Query().Get("prefix"), secretRefPrefix)
	paths, err := s.fleet.secrets.List(r.Context(), DefaultOwnerID, prefix)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	refs := make([]string, 0, len(paths))
	for _, p := range paths {
		refs = append(refs, secretRefPrefix+p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"refs": refs})
}

func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	path, err := secretRefFromPath(r.PathValue("ref"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "value is required"})
		return
	}
	if err := s.fleet.secrets.Put(r.Context(), DefaultOwnerID, path, []byte(body.Value)); err != nil {
		writeSecretErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditSecretSet, OwnerID: DefaultOwnerID, Detail: secretRefPrefix + path})
	go s.fleet.egressRefresh() // injected values follow the stored secret
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	path, err := secretRefFromPath(r.PathValue("ref"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.fleet.secrets.Delete(r.Context(), DefaultOwnerID, path); err != nil {
		writeSecretErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditSecretDeleted, OwnerID: DefaultOwnerID, Detail: secretRefPrefix + path})
	go s.fleet.egressRefresh()
	w.WriteHeader(http.StatusNoContent)
}

func writeSecretErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSecretNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrSecretsReadOnly), errors.Is(err, ErrInjectionUnavailable):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}
