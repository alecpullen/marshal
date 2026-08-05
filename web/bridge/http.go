package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// maxBodyBytes bounds inbound request bodies. Prompt and steer text
// dominate; 1 MiB matches the ACP inbound frame cap.
const maxBodyBytes = 1 << 20

// Server exposes the Registry and EventLog over HTTP: a JSON REST API
// under /api plus the SSE event stream. When a token is configured,
// every /api route requires bearer auth.
type Server struct {
	reg  *Registry
	log  *EventLog
	mux  *http.ServeMux
	http http.Handler // mux wrapped in auth (or not)
}

// NewServer builds the HTTP layer on a started Registry and EventLog.
// A non-empty token enables bearer auth on every /api route; empty
// means unauthenticated (loopback-only deployments).
func NewServer(reg *Registry, log *EventLog, token string) *Server {
	s := &Server{reg: reg, log: log, mux: http.NewServeMux()}
	s.routes()
	s.http = s.mux
	if token != "" {
		s.http = bearerAuth(token, s.mux)
	}
	return s
}

// ServeHTTP implements http.Handler. Non-API paths are served by the
// embedded SPA (index.html fallback for client-side routing); /api paths
// go through the mux (with optional bearer auth) so method mismatches
// still produce 405.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		staticHandler().ServeHTTP(w, r)
		return
	}
	s.http.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/config", s.config)
	s.mux.HandleFunc("GET /api/sessions", s.listSessions)
	s.mux.HandleFunc("POST /api/sessions", s.newSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/load", s.loadSession)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/prompt", s.prompt)
	s.mux.HandleFunc("POST /api/sessions/{id}/cancel", s.cancel)
	s.mux.HandleFunc("POST /api/sessions/{id}/steer", s.steer)
	s.mux.HandleFunc("POST /api/sessions/{id}/mode", s.setMode)
	s.mux.HandleFunc("POST /api/permissions/{toolCallId}", s.resolvePermission)
	s.mux.HandleFunc("POST /api/questions/{questionId}", s.resolveQuestion)
	// NOTE: with a token configured this stream requires an
	// Authorization header, which the browser-native EventSource API
	// cannot send. The SPA must consume SSE over fetch (Task 7 does);
	// a query-param token fallback is deliberately not offered — the
	// token must never appear in URLs.
	s.mux.HandleFunc("GET /api/events", s.log.ServeSSE)
}

// writeJSON encodes v with a status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr maps bridge errors onto status codes: unknown session →
// 404, stale permission/question resolve → 410, anything from the
// child → 502.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnknownSession):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrGone):
		writeJSON(w, http.StatusGone, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}

// decodeJSON reads a size-limited JSON body into v: oversize bodies
// and trailing data after the first value are rejected. Returns false
// (and writes 400) on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must contain a single JSON value"})
		return false
	}
	return true
}

// config exposes read-only bridge configuration needed by the SPA.
func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"cwdRoot": s.reg.RootCwd})
}

// listSessions proxies ACP session/list: all sessions known to the
// child for a cwd, tracked or not.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cwd query parameter is required"})
		return
	}
	params := map[string]string{"cwd": cwd}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		params["cursor"] = cursor
	}
	res, err := s.reg.child.Request(r.Context(), "session/list", params)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res)
}

// newSession creates (or resumes) a session via Registry.New.
func (s *Server) newSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cwd       string `json:"cwd"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cwd is required"})
		return
	}
	id, err := s.reg.New(r.Context(), body.Cwd, body.SessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": id})
}

// loadSession attaches to an existing session and returns the retained
// event tail so the SPA can render current state without an SSE
// reconnect. The registry tracks the session under the bridge's
// configured root cwd. A session the child no longer knows surfaces as
// the child's error (502), not 404 — the bridge cannot distinguish a
// child-side "session not found" from a transport failure.
func (s *Server) loadSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Cwd string `json:"cwd"`
	}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &body) {
			return
		}
	}
	cwd := body.Cwd
	if cwd == "" {
		cwd = s.reg.RootCwd
	}
	if cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cwd is required (body or configured root)"})
		return
	}
	if err := s.reg.Load(r.Context(), cwd, id); err != nil {
		writeErr(w, err)
		return
	}
	events := s.log.Tail(id)
	if events == nil {
		events = []Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId": id,
		"events":    events,
	})
}

// deleteSession removes the session record.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.reg.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// prompt starts a turn. Registry.Prompt blocks until the turn
// completes, so it runs on its own goroutine; the HTTP round trip only
// confirms the turn was accepted. Turn output arrives over SSE, and
// the bridge-side turn_end event carries the terminal status.
func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, ok := s.reg.lookup(id)
	if !ok {
		writeErr(w, ErrUnknownSession)
		return
	}
	if info.Busy {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "turn already in flight"})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}
	go func() {
		if err := s.reg.Prompt(context.Background(), id, body.Text); err != nil {
			slog.Default().Warn("webbridge: prompt turn failed", "session", id, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

// cancel interrupts the in-flight turn.
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if err := s.reg.Cancel(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// steer redirects the in-flight turn.
func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}
	if err := s.reg.Steer(r.Context(), r.PathValue("id"), body.Text); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "steered"})
}

// setMode switches the session's permission mode.
func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Mode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode is required"})
		return
	}
	if err := s.reg.SetMode(r.Context(), r.PathValue("id"), body.Mode); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// resolvePermission delivers the SPA's decision to a pending
// session/request_permission.
func (s *Server) resolvePermission(w http.ResponseWriter, r *http.Request) {
	var body Decision
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.reg.ResolvePermission(r.PathValue("toolCallId"), body); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// resolveQuestion delivers the SPA's answers to a pending
// session/request_question.
func (s *Server) resolveQuestion(w http.ResponseWriter, r *http.Request) {
	var body Answers
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.reg.ResolveQuestion(r.PathValue("questionId"), body); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}
