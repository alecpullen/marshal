package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxBodyBytes bounds inbound request bodies. Prompt and steer text
// dominate; 1 MiB matches the ACP inbound frame cap.
const maxBodyBytes = 1 << 20

// Server exposes the Registry and EventLog over HTTP: a JSON REST API
// under /api plus the SSE event stream. When a token is configured,
// every /api route requires bearer auth.
type Server struct {
	fleet *Fleet
	mux   *http.ServeMux
	http  http.Handler
	reg   *Registry
	log   *EventLog
	// publicURLBase is the externally reachable base URL, used to build
	// absolute links (e.g. the operator-approval URL an MCP spawn
	// returns). Empty falls back to the listen address.
	publicURLBase string
}

func NewServer(target any, args ...any) *Server {
	var fleet *Fleet
	var token string
	var reg *Registry
	var log *EventLog
	var publicURLBase string
	if f, ok := target.(*Fleet); ok {
		fleet = f
		if len(args) > 0 {
			token, _ = args[0].(string)
		}
		if len(args) > 1 {
			publicURLBase, _ = args[1].(string)
		}
	} else if r, ok := target.(*Registry); ok {
		reg = r
		if len(args) > 0 {
			log, _ = args[0].(*EventLog)
		}
		if len(args) > 1 {
			token, _ = args[1].(string)
		}
		if len(args) > 2 {
			publicURLBase, _ = args[2].(string)
		}
	}
	s := &Server{fleet: fleet, reg: reg, log: log, publicURLBase: publicURLBase, mux: http.NewServeMux()}
	s.routes()
	s.http = s.mux
	if token != "" {
		s.http = bearerAuth(token, s.mux)
	}
	return s
}

// publicURL builds an absolute URL from the configured public URL or
// falls back to the listen address.
func (s *Server) publicURL(path string) string {
	base := s.publicURLBase
	if base == "" {
		base = "http://localhost:7700"
	}
	return strings.TrimRight(base, "/") + path
}

// ServeHTTP implements http.Handler. Non-API paths are served by the
// embedded SPA (index.html fallback for client-side routing); /api paths
// go through the mux (with optional bearer auth) so method mismatches
// still produce 405.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// /mcp joins /api/ on the mux side. It is not under /api/ because it
	// authenticates per client rather than with the shared bearer token,
	// but it must still reach the mux rather than the SPA fallback.
	if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/mcp" {
		staticHandler().ServeHTTP(w, r)
		return
	}
	s.http.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/config", s.config)
	s.mux.HandleFunc("GET /api/projects", s.listProjects)
	s.mux.HandleFunc("POST /api/projects", s.addProject)
	s.mux.HandleFunc("DELETE /api/projects", s.removeProject)
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/agents", s.spawnAgent)
	s.mux.HandleFunc("GET /api/agents/{id}/diff", s.agentDiff)
	s.mux.HandleFunc("POST /api/agents/{id}/merge", s.agentMerge)
	s.mux.HandleFunc("POST /api/agents/{id}/discard", s.agentDiscard)
	s.mux.HandleFunc("POST /api/agents/{id}/exit", s.agentExit)
	s.mux.HandleFunc("GET /api/agents/{id}/patch", s.agentPatch)
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
	s.mux.HandleFunc("GET /api/clients", s.listClients)
	s.mux.HandleFunc("POST /api/clients", s.createClient)
	s.mux.HandleFunc("DELETE /api/clients/{id}", s.deleteClient)
	s.mux.HandleFunc("GET /api/pending", s.listPending)
	s.mux.HandleFunc("POST /api/pending/{id}/approve", s.approvePending)
	s.mux.HandleFunc("POST /api/pending/{id}/deny", s.denyPending)
	s.mux.HandleFunc("GET /api/repos/{id}/issues", s.listRepoIssues)
	s.mux.HandleFunc("POST /api/repos/{id}/issues/{number}/spawn", s.spawnFromIssue)
	// NOTE: with a token configured this stream requires an
	// Authorization header, which the browser-native EventSource API
	// cannot send. The SPA must consume SSE over fetch (Task 7 does);
	// a query-param token fallback is deliberately not offered — the
	// token must never appear in URLs.
	if s.fleet != nil {
		s.mux.HandleFunc("GET /api/events", s.serveEvents)
	} else {
		s.mux.HandleFunc("GET /api/events", s.log.ServeSSE)
	}

	// Deliberately NOT under /api/: this endpoint authenticates per
	// client rather than with the shared bearer token. See
	// authenticateMCP — it must reject on its own.
	s.mux.HandleFunc("POST /mcp", s.mcpHandler)
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
	case errors.Is(err, ErrUnknownAgent):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
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
	if s.fleet == nil {
		writeJSON(w, http.StatusOK, map[string]string{"cwdRoot": s.reg.RootCwd})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": s.fleet.ProjectStatus()})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.fleet.ProjectStatus())
}

func (s *Server) addProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := ValidateProjectRoot(body.Root); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.fleet.ws.AddProject(body.Root); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.fleet.ProjectStatus())
}

func (s *Server) removeProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Root == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root is required"})
		return
	}
	s.fleet.StopProject(body.Root)
	if err := s.fleet.ws.RemoveProject(body.Root); err != nil {
		writeErr(w, err)
		return
	}
	_, _ = s.fleet.FleetLog().Append(fleetStreamKey, map[string]any{"kind": "project_removed", "project": body.Root})
	writeJSON(w, http.StatusOK, s.fleet.ProjectStatus())
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.fleet.Snapshot())
}

// ValidateProjectRoot checks that root is an absolute path under one of the
// bridge's trusted roots. It is exported for CLI startup validation.
func ValidateProjectRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("project root %q must be an absolute path", root)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolved = root
	}
	bases := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		if p, err := filepath.EvalSymlinks(home); err == nil {
			bases = append(bases, p)
		}
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	if p, err := filepath.EvalSymlinks(tmp); err == nil {
		bases = append(bases, p)
	}
	if wd, err := os.Getwd(); err == nil {
		if p, err := filepath.EvalSymlinks(wd); err == nil {
			bases = append(bases, p)
		}
	}
	for _, base := range bases {
		rel, err := filepath.Rel(base, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("project root %q is outside the trusted-roots allow-list (your home directory, %s, or the bridge's working directory)", root, tmp)
}

func (s *Server) spawnAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project  string `json:"project"`
		Name     string `json:"name"`
		Mode     string `json:"mode"`
		Prompt   string `json:"prompt"`
		Isolated bool   `json:"isolated"`
		Branch   string `json:"branch"`
		BaseRef  string `json:"baseRef"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := ValidateProjectRoot(body.Project); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	id, err := s.fleet.Spawn(r.Context(), body.Project, SpawnOptions{
		Name: body.Name, Mode: body.Mode, Isolated: body.Isolated, Branch: body.Branch, BaseRef: body.BaseRef,
	})
	if id == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if body.Prompt != "" {
		if rt, e := s.fleet.RuntimeForSession(id); e == nil {
			go func() {
				if e := rt.reg.Prompt(context.Background(), rt.sessionID, body.Prompt); e != nil {
					slog.Default().Warn("webbridge: first prompt failed", "agent", id, "err", e)
				}
			}()
		}
	}
	resp := map[string]any{"agentId": id}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) agentDiff(w http.ResponseWriter, r *http.Request) {
	raw, err := s.fleet.Diff(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) agentMerge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CommitMessage string `json:"commitMessage"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	raw, err := s.fleet.Merge(r.Context(), r.PathValue("id"), body.CommitMessage)
	if err != nil {
		writeErr(w, err)
		return
	}
	// A refusal carries a reason and merged:false. Map it to 409 so the UI
	// branches on a code rather than parsing prose.
	var probe struct {
		Merged bool   `json:"merged"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &probe)
	status := http.StatusOK
	if !probe.Merged && probe.Reason != "" {
		status = http.StatusConflict
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func (s *Server) agentDiscard(w http.ResponseWriter, r *http.Request) {
	if err := s.fleet.Discard(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

func (s *Server) agentExit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CommitMessage string        `json:"commitMessage"`
		Override      *GateOverride `json:"override,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	res, err := s.fleet.Exit(r.Context(), r.PathValue("id"), ExitOptions{
		CommitMessage: body.CommitMessage,
		Override:      body.Override,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) agentPatch(w http.ResponseWriter, r *http.Request) {
	patch, err := s.fleet.Patch(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-patch")
	w.Header().Set("Content-Disposition", `attachment; filename="marshal-`+r.PathValue("id")+`.patch"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(patch)
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "fleet" {
		s.fleet.FleetLog().ServeSSEKey(w, r, fleetStreamKey)
		return
	}
	id := r.URL.Query().Get("sessionId")
	if id == "" {
		http.Error(w, "bridge: sessionId or stream=fleet is required", http.StatusBadRequest)
		return
	}
	log, err := s.fleet.LogForSession(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	log.ServeSSE(w, r)
}

// registryForSession resolves an id (agent id or ACP session id) to the
// owning Registry, its event log, and the ACP session id to address
// registry calls with. In the non-fleet path the id is already the
// session id.
func (s *Server) registryForSession(id string) (*Registry, *EventLog, string, error) {
	if s.fleet != nil {
		rt, err := s.fleet.RuntimeForSession(id)
		if err != nil {
			return nil, nil, "", err
		}
		return rt.reg, rt.log, rt.sessionID, nil
	}
	return s.reg, s.log, id, nil
}

func (s *Server) registryForRoot(root string) (*Registry, *EventLog, error) {
	if s.fleet != nil {
		rt, err := s.fleet.runtimeForRoot(root)
		if err != nil {
			return nil, nil, err
		}
		return rt.reg, rt.log, nil
	}
	return s.reg, s.log, nil
}

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
	reg, _, err := s.registryForRoot(cwd)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := reg.child.Request(r.Context(), "session/list", params)
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
	var id string
	var err error
	if s.fleet != nil {
		id, err = s.fleet.Spawn(r.Context(), body.Cwd, SpawnOptions{})
	} else {
		id, err = s.reg.New(r.Context(), body.Cwd, body.SessionID)
	}
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
	var reg *Registry
	var log *EventLog
	var sessionID string
	var err error
	cwd := body.Cwd
	if s.fleet != nil {
		reg, log, sessionID, err = s.registryForSession(id)
	} else {
		if cwd == "" {
			cwd = s.reg.RootCwd
		}
		reg, log, sessionID = s.reg, s.log, id
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cwd is required (body or configured root)"})
		return
	}
	if err := reg.Load(r.Context(), cwd, sessionID); err != nil {
		writeErr(w, err)
		return
	}
	events := log.Tail(sessionID)
	if events == nil {
		events = []Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId": sessionID,
		"events":    events,
	})
}

// deleteSession removes the session record.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := reg.Delete(r.Context(), sessionID); err != nil {
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
	reg, _, sessionID, err := s.registryForSession(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	info, ok := reg.lookup(sessionID)
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
		if err := reg.Prompt(context.Background(), sessionID, body.Text); err != nil {
			slog.Default().Warn("webbridge: prompt turn failed", "session", sessionID, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

// cancel interrupts the in-flight turn.
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := reg.Cancel(r.Context(), sessionID); err != nil {
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
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := reg.Steer(r.Context(), sessionID, body.Text); err != nil {
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
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := reg.SetMode(r.Context(), sessionID, body.Mode); err != nil {
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
	var err error
	if s.fleet != nil {
		err = s.fleet.ResolvePermission(r.PathValue("toolCallId"), body)
	} else {
		err = s.reg.ResolvePermission(r.PathValue("toolCallId"), body)
	}
	if err != nil {
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
	var err error
	if s.fleet != nil {
		err = s.fleet.ResolveQuestion(r.PathValue("questionId"), body)
	} else {
		err = s.reg.ResolveQuestion(r.PathValue("questionId"), body)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// listClients returns all registered MCP clients, omitting token hashes.
// The hash has no business reaching the UI — it widens the blast radius
// of any XSS.
func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	clients := s.fleet.Clients()
	out := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		out = append(out, map[string]any{
			"id":            c.ID,
			"name":          c.Name,
			"autonomous":    c.Autonomous,
			"maxConcurrent": c.MaxConcurrent,
			"maxPerDay":     c.MaxPerDay,
			"allowedRepos":  c.AllowedRepos,
			"ownerId":       c.OwnerID,
			"createdAt":     c.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// createClient mints a new MCP client token, persists only the hash,
// and returns the plaintext once.
func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string   `json:"name"`
		Autonomous    bool     `json:"autonomous"`
		MaxConcurrent int      `json:"maxConcurrent"`
		MaxPerDay     int      `json:"maxPerDay"`
		AllowedRepos  []string `json:"allowedRepos"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	plain, hash, err := NewClientToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	c := MCPClient{
		ID:            newAgentID(),
		Name:          body.Name,
		TokenHash:     hash,
		Autonomous:    body.Autonomous,
		MaxConcurrent: body.MaxConcurrent,
		MaxPerDay:     body.MaxPerDay,
		AllowedRepos:  body.AllowedRepos,
		OwnerID:       DefaultOwnerID,
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.fleet.ws.PutClient(c); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditClientCreated, OwnerID: c.OwnerID,
		ClientID: c.ID, Detail: c.Name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         c.ID,
		"name":       c.Name,
		"token":      plain,
		"autonomous": c.Autonomous,
	})
}

// deleteClient removes an MCP client.
func (s *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	if err := s.fleet.ws.DeleteClient(id); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditClientRevoked, OwnerID: DefaultOwnerID, ClientID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// listPending returns all pending submissions awaiting confirmation.
func (s *Server) listPending(w http.ResponseWriter, r *http.Request) {
	pending := s.fleet.ws.Pending()
	out := make([]map[string]any, 0, len(pending))
	for _, p := range pending {
		out = append(out, map[string]any{
			"id":        p.ID,
			"origin":    p.Origin,
			"clientId":  p.ClientID,
			"title":     p.Title,
			"repoId":    p.RepoID,
			"ref":       p.Ref,
			"prompt":    p.Prompt,
			"plan":      p.Plan,
			"mode":      p.Mode,
			"createdAt": p.CreatedAt,
			"expiresAt": p.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// approvePending confirms a submission and starts its agent.
func (s *Server) approvePending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	agentID, err := s.fleet.Approve(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agentId": agentID, "status": "running"})
}

// denyPending discards a submission without spawning.
func (s *Server) denyPending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	if err := s.fleet.Deny(id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "denied"})
}

// listRepoIssues returns the open issues for a registered repo.
func (s *Server) listRepoIssues(w http.ResponseWriter, r *http.Request) {
	issues, err := s.fleet.ListRepoIssues(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, issues)
}

// spawnFromIssue turns one issue into a submission.
func (s *Server) spawnFromIssue(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	if number == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issue number is required"})
		return
	}
	var n int
	if _, err := fmt.Sscanf(number, "%d", &n); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issue number must be an integer"})
		return
	}
	res, err := s.fleet.SubmitIssue(r.Context(), r.PathValue("id"), n)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}
