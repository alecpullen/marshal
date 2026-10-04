package bridge

import (
	"encoding/json"
	"net/http"
)

// Section names recorded in the models_changed audit event's Detail.
const (
	sectionProviders   = "providers"
	sectionProviderKey = "provider_key"
	sectionPresets     = "presets"
	sectionRouting     = "routing"
)

func (s *Server) modelsGet(w http.ResponseWriter, r *http.Request) {
	raw, err := s.fleet.controlCall(r.Context(), "config/get", nil, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

// modelsMutation proxies one config/set_* method. The body is the
// method's params verbatim: the engine owns validation, so the bridge does
// not restate the schema.
//
// Only the section name reaches the audit log. Provider bodies can carry
// secrets, and AuditEvent has no field that could hold one.
func (s *Server) modelsMutation(section, method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if !decodeJSON(w, r, &body) {
			return
		}
		params := make(map[string]any, len(body))
		for k, v := range body {
			params[k] = v
		}
		raw, err := s.fleet.controlCall(r.Context(), method, params, false)
		if err != nil {
			writeErr(w, err)
			return
		}
		s.fleet.auditf(AuditEvent{Event: AuditModelsChanged, OwnerID: DefaultOwnerID, Detail: section})
		writeRaw(w, raw)
	}
}

func (s *Server) modelsProviderKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	if name == "" || body.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider name and key are required"})
		return
	}
	raw, err := s.fleet.controlCall(r.Context(), "config/set_provider_key",
		map[string]any{"name": name, "key": body.Key}, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The provider name identifies what changed; the key is never logged.
	s.fleet.auditf(AuditEvent{Event: AuditModelsChanged, OwnerID: DefaultOwnerID,
		Detail: sectionProviderKey + ":" + name})
	writeRaw(w, raw)
}

func (s *Server) modelsProbe(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if !decodeJSON(w, r, &body) {
		return
	}
	params := make(map[string]any, len(body))
	for k, v := range body {
		params[k] = v
	}
	raw, err := s.fleet.controlCall(r.Context(), "config/probe_provider", params, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

func (s *Server) modelsRoutes() {
	s.mux.HandleFunc("GET /api/models", s.modelsGet)
	s.mux.HandleFunc("PUT /api/models/providers", s.modelsMutation(sectionProviders, "config/set_providers"))
	s.mux.HandleFunc("PUT /api/models/providers/{name}/key", s.modelsProviderKey)
	s.mux.HandleFunc("PUT /api/models/presets", s.modelsMutation(sectionPresets, "config/set_presets"))
	s.mux.HandleFunc("PUT /api/models/routing", s.modelsMutation(sectionRouting, "config/set_routing"))
	s.mux.HandleFunc("POST /api/models/probe", s.modelsProbe)
}
