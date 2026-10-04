package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
)

// errInvalidLibrary marks a library request the bridge refuses itself.
// The HTTP layer maps it to 400.
var errInvalidLibrary = errors.New("bridge: invalid library request")

// errScopeMismatch is returned when a staged install is confirmed under a
// different scope or project than it was previewed for. The agent keeps
// staged installs per session, so the token would not be found there.
var errScopeMismatch = errors.New("bridge: staged install belongs to a different scope; preview it again")

// maxStagedTokens bounds the token map. Tokens are short-lived and a
// discarded or confirmed one is forgotten; this only guards a client that
// previews forever and never finishes.
const maxStagedTokens = 256

// libraryState remembers which control-agent session staged each install
// token. The agent scopes staging by session, and the scope directory by
// the session's roots, so preview, confirm and discard must all reach the
// same session.
type libraryState struct {
	mu     sync.Mutex
	staged map[string]string // token -> session id
	order  []string          // tokens, oldest first
}

func (l *libraryState) remember(token, sessionID string) {
	if token == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.staged == nil {
		l.staged = make(map[string]string)
	}
	if _, ok := l.staged[token]; !ok {
		for len(l.order) >= maxStagedTokens {
			delete(l.staged, l.order[0])
			l.order = l.order[1:]
		}
		l.order = append(l.order, token)
	}
	l.staged[token] = sessionID
}

func (l *libraryState) lookup(token string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sid, ok := l.staged[token]
	return sid, ok
}

func (l *libraryState) forget(token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.staged, token)
	for i, t := range l.order {
		if t == token {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
}

// librarySession picks the control-agent session a library operation runs
// on: the control session for global scope, the project's own session for
// project scope. The scope string is also sent to the agent, which
// validates it.
func (f *Fleet) librarySession(ctx context.Context, scope, project string) (string, error) {
	switch scope {
	case "", "global":
		return f.controlSessionID(ctx)
	case "project":
		if project == "" {
			return "", fmt.Errorf("%w: project scope needs a project", errInvalidLibrary)
		}
		return f.projectSession(ctx, project)
	default:
		return "", fmt.Errorf("%w: scope must be %q or %q", errInvalidLibrary, "global", "project")
	}
}

// libraryCall sends one session-scoped method on the library session for
// scope and project.
func (f *Fleet) libraryCall(ctx context.Context, scope, project, method string, params map[string]any) (json.RawMessage, error) {
	sid, err := f.librarySession(ctx, scope, project)
	if err != nil {
		return nil, err
	}
	return f.libraryCallOn(ctx, sid, method, params)
}

func (f *Fleet) libraryCallOn(ctx context.Context, sid, method string, params map[string]any) (json.RawMessage, error) {
	p := make(map[string]any, len(params)+1)
	for k, v := range params {
		p[k] = v
	}
	p["sessionId"] = sid
	return f.controlRequest(ctx, method, p)
}

// filterByScope keeps the entries of raw[key] whose "scope" equals scope.
// A project session lists both scopes, but the page asks for one. A body
// that is not the expected shape passes through untouched.
func filterByScope(raw json.RawMessage, key, scope string) json.RawMessage {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return raw
	}
	var entries []json.RawMessage
	if json.Unmarshal(body[key], &entries) != nil {
		return raw
	}
	kept := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		var s struct {
			Scope string `json:"scope"`
		}
		if json.Unmarshal(e, &s) == nil && s.Scope == scope {
			kept = append(kept, e)
		}
	}
	body[key], _ = json.Marshal(kept)
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}

// libraryScope reads and validates scope and project from the query. A
// bad project root writes 400 and returns ok=false.
func libraryScope(w http.ResponseWriter, scope, project string) (string, string, bool) {
	if scope == "" {
		scope = "global"
	}
	if scope == "project" {
		if err := ValidateProjectRoot(project); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return "", "", false
		}
	}
	return scope, project, true
}

// libraryKind is one installable thing: skills and plugins share a shape
// and differ only in method names and the staging token's field name.
type libraryKind struct {
	name       string // audit and error text: "skill", "plugin"
	listMethod string
	listKey    string
	stageMeth  string // preview / scan
	confirm    string
	discard    string
	remove     string
	tokenField string
	installed  string // audit event names
	removed    string
}

var (
	skillsKind = libraryKind{
		name: "skill", listMethod: "session/skills_list", listKey: "skills",
		stageMeth: "session/skills_install_preview", confirm: "session/skills_install_confirm",
		discard: "session/skills_install_discard", remove: "session/skills_remove",
		tokenField: "stagingToken", installed: AuditSkillInstalled, removed: AuditSkillRemoved,
	}
	pluginsKind = libraryKind{
		name: "plugin", listMethod: "session/plugins_list", listKey: "plugins",
		stageMeth: "session/plugins_install_scan", confirm: "session/plugins_install_confirm",
		discard: "session/plugins_install_discard", remove: "session/plugins_remove",
		tokenField: "scanToken", installed: AuditPluginInstalled, removed: AuditPluginRemoved,
	}
)

func (s *Server) libraryList(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, project, ok := libraryScope(w, r.URL.Query().Get("scope"), r.URL.Query().Get("project"))
		if !ok {
			return
		}
		raw, err := s.fleet.libraryCall(r.Context(), scope, project, k.listMethod, map[string]any{"scope": scope})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeRaw(w, filterByScope(raw, k.listKey, scope))
	}
}

// libraryStage previews a skill or scans a plugin. The install is staged
// on the session that will own it, chosen by the optional scope and
// project; global by default.
func (s *Server) libraryStage(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Source  string `json:"source"`
			Ref     string `json:"ref"`
			Scope   string `json:"scope"`
			Project string `json:"project"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if body.Source == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source is required"})
			return
		}
		scope, project, ok := libraryScope(w, body.Scope, body.Project)
		if !ok {
			return
		}
		sid, err := s.fleet.librarySession(r.Context(), scope, project)
		if err != nil {
			writeErr(w, err)
			return
		}
		params := map[string]any{"source": body.Source}
		if body.Ref != "" {
			params["ref"] = body.Ref
		}
		raw, err := s.fleet.libraryCallOn(r.Context(), sid, k.stageMeth, params)
		if err != nil {
			writeErr(w, err)
			return
		}
		var res map[string]json.RawMessage
		if json.Unmarshal(raw, &res) == nil {
			var token string
			_ = json.Unmarshal(res[k.tokenField], &token)
			s.fleet.lib.remember(token, sid)
		}
		writeRaw(w, raw)
	}
}

func (s *Server) libraryConfirm(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{}
		if !decodeJSON(w, r, &body) {
			return
		}
		token := body[k.tokenField]
		if token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": k.tokenField + " is required"})
			return
		}
		scope, project, ok := libraryScope(w, body["scope"], body["project"])
		if !ok {
			return
		}
		sid, err := s.fleet.librarySession(r.Context(), scope, project)
		if err != nil {
			writeErr(w, err)
			return
		}
		if staged, known := s.fleet.lib.lookup(token); known && staged != sid {
			writeErr(w, errScopeMismatch)
			return
		}
		raw, err := s.fleet.libraryCallOn(r.Context(), sid, k.confirm,
			map[string]any{k.tokenField: token, "scope": scope})
		if err != nil {
			writeErr(w, err)
			return
		}
		s.fleet.lib.forget(token)
		var res struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &res)
		s.fleet.auditf(AuditEvent{Event: k.installed, OwnerID: DefaultOwnerID, Detail: res.Name + " (" + scope + ")"})
		writeRaw(w, raw)
	}
}

func (s *Server) libraryDiscard(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{}
		if !decodeJSON(w, r, &body) {
			return
		}
		token := body[k.tokenField]
		if token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": k.tokenField + " is required"})
			return
		}
		sid, known := s.fleet.lib.lookup(token)
		if !known {
			// Never staged by this bridge (or already forgotten): the
			// control session is where an unscoped preview lands.
			var err error
			if sid, err = s.fleet.controlSessionID(r.Context()); err != nil {
				writeErr(w, err)
				return
			}
		}
		raw, err := s.fleet.libraryCallOn(r.Context(), sid, k.discard, map[string]any{k.tokenField: token})
		if err != nil {
			writeErr(w, err)
			return
		}
		s.fleet.lib.forget(token)
		writeRaw(w, raw)
	}
}

func (s *Server) libraryRemove(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, project, ok := libraryScope(w, r.URL.Query().Get("scope"), r.URL.Query().Get("project"))
		if !ok {
			return
		}
		name := r.PathValue("name")
		raw, err := s.fleet.libraryCall(r.Context(), scope, project, k.remove,
			map[string]any{"name": name, "scope": scope})
		if err != nil {
			writeErr(w, err)
			return
		}
		s.fleet.auditf(AuditEvent{Event: k.removed, OwnerID: DefaultOwnerID, Detail: name + " (" + scope + ")"})
		writeRaw(w, raw)
	}
}

// memoryProject validates the required ?project= of a memory route.
func memoryProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	project := r.URL.Query().Get("project")
	if err := ValidateProjectRoot(project); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return "", false
	}
	return project, true
}

func memoryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "memory id must be a non-zero integer"})
		return 0, false
	}
	return id, true
}

func (s *Server) memoryList(w http.ResponseWriter, r *http.Request) {
	project, ok := memoryProject(w, r)
	if !ok {
		return
	}
	// The scope filter (project, workspace, global) is validated by the
	// agent, which answers an unknown one with invalid params.
	var params map[string]any
	if scope := r.URL.Query().Get("scope"); scope != "" {
		params = map[string]any{"scope": scope}
	}
	raw, err := s.fleet.libraryCall(r.Context(), "project", project, "session/memory_list", params)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

func (s *Server) memoryDelete(w http.ResponseWriter, r *http.Request) {
	project, ok := memoryProject(w, r)
	if !ok {
		return
	}
	id, ok := memoryID(w, r)
	if !ok {
		return
	}
	raw, err := s.fleet.libraryCall(r.Context(), "project", project, "session/memory_delete", map[string]any{"id": id})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditMemoryDeleted, OwnerID: DefaultOwnerID, Detail: strconv.FormatInt(id, 10)})
	writeRaw(w, raw)
}

func (s *Server) memorySetConfidence(w http.ResponseWriter, r *http.Request) {
	project, ok := memoryProject(w, r)
	if !ok {
		return
	}
	id, ok := memoryID(w, r)
	if !ok {
		return
	}
	var body struct {
		Confidence string `json:"confidence"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Confidence == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confidence is required"})
		return
	}
	raw, err := s.fleet.libraryCall(r.Context(), "project", project, "session/memory_set_confidence",
		map[string]any{"id": id, "confidence": body.Confidence})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

// memorySuggestions lists project memories another project holds too, with
// the scope each could be promoted to.
func (s *Server) memorySuggestions(w http.ResponseWriter, r *http.Request) {
	project, ok := memoryProject(w, r)
	if !ok {
		return
	}
	raw, err := s.fleet.libraryCall(r.Context(), "project", project, "session/memory_suggestions", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

// memoryPromote moves a memory to a wider scope.
func (s *Server) memoryPromote(w http.ResponseWriter, r *http.Request) {
	project, ok := memoryProject(w, r)
	if !ok {
		return
	}
	id, ok := memoryID(w, r)
	if !ok {
		return
	}
	var body struct {
		Scope    string `json:"scope"`
		ScopeKey string `json:"scopeKey"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Scope == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope is required"})
		return
	}
	params := map[string]any{"id": id, "scope": body.Scope}
	if body.ScopeKey != "" {
		params["scopeKey"] = body.ScopeKey
	}
	raw, err := s.fleet.libraryCall(r.Context(), "project", project, "session/memory_promote", params)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditMemoryPromoted, OwnerID: DefaultOwnerID,
		Detail: strconv.FormatInt(id, 10) + " -> " + body.Scope})
	writeRaw(w, raw)
}

func (s *Server) libraryRoutes() {
	for _, k := range []libraryKind{skillsKind, pluginsKind} {
		base := "/api/library/" + k.name + "s"
		stage := "/preview"
		if k.name == "plugin" {
			stage = "/scan"
		}
		s.mux.HandleFunc("GET "+base, s.libraryList(k))
		s.mux.HandleFunc("POST "+base+stage, s.libraryStage(k))
		s.mux.HandleFunc("POST "+base+"/confirm", s.libraryConfirm(k))
		s.mux.HandleFunc("POST "+base+"/discard", s.libraryDiscard(k))
		s.mux.HandleFunc("DELETE "+base+"/{name}", s.libraryRemove(k))
	}
	s.mux.HandleFunc("GET /api/library/memory", s.memoryList)
	s.mux.HandleFunc("GET /api/library/memory/suggestions", s.memorySuggestions)
	s.mux.HandleFunc("DELETE /api/library/memory/{id}", s.memoryDelete)
	s.mux.HandleFunc("POST /api/library/memory/{id}/promote", s.memoryPromote)
	s.mux.HandleFunc("POST /api/library/memory/{id}/confidence", s.memorySetConfidence)
}
