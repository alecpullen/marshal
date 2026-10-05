package bridge

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func libraryServer(t *testing.T) (*Server, *Fleet, *fakeAgent) {
	t.Helper()
	agent := newFakeAgent()
	f := ctlFleet(t, agent)
	return NewServer(f, ""), f, agent
}

func TestLibraryListUsesTheRightSession(t *testing.T) {
	s, _, agent := libraryServer(t)
	agent.results["session/skills_list"] = map[string]any{"skills": []map[string]any{
		{"name": "g", "scope": "global"}, {"name": "p", "scope": "project"},
	}}
	root := t.TempDir()

	rec := doReq(t, s, http.MethodGet, "/api/library/skills", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("global: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Skills []struct{ Name string } `json:"skills"`
	}
	decodeBody(t, rec, &got)
	if len(got.Skills) != 1 || got.Skills[0].Name != "g" {
		t.Fatalf("global list not filtered to global: %s", rec.Body.String())
	}

	rec = doReq(t, s, http.MethodGet, "/api/library/skills?scope=project&project="+root, nil, nil)
	decodeBody(t, rec, &got)
	if len(got.Skills) != 1 || got.Skills[0].Name != "p" {
		t.Fatalf("project list not filtered to project: %s", rec.Body.String())
	}

	calls := agent.calls("session/skills_list")
	// s-1 is the control session, s-2 the project's.
	if len(calls) != 2 || !strings.Contains(calls[0], `"sessionId":"s-1"`) || !strings.Contains(calls[1], `"sessionId":"s-2"`) {
		t.Fatalf("session ids = %v", calls)
	}
	if news := agent.calls("session/new"); len(news) != 2 || !strings.Contains(news[1], root) {
		t.Fatalf("project session = %v", news)
	}
}

func TestLibrarySkillInstallFlowProxiesAndAudits(t *testing.T) {
	s, f, agent := libraryServer(t)
	agent.results["session/skills_install_preview"] = map[string]any{"stagingToken": "tok", "name": "lint"}
	agent.results["session/skills_install_confirm"] = map[string]any{"name": "lint"}

	rec := doReq(t, s, http.MethodPost, "/api/library/skills/preview", map[string]any{"source": "github.com/x/y"}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stagingToken":"tok"`) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(t, s, http.MethodPost, "/api/library/skills/confirm",
		map[string]any{"stagingToken": "tok", "scope": "global"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("session/skills_install_confirm"); len(got) != 1 ||
		!strings.Contains(got[0], `"stagingToken":"tok"`) || !strings.Contains(got[0], `"scope":"global"`) ||
		!strings.Contains(got[0], `"sessionId":"s-1"`) {
		t.Fatalf("confirm params = %v", got)
	}
	if e := findEvent(auditTail(t, f), AuditSkillInstalled); e == nil || !strings.Contains(e.Detail, "lint") {
		t.Fatalf("install not audited: %+v", e)
	}
}

func TestLibraryDiscardRoutesToTheStagingSession(t *testing.T) {
	s, _, agent := libraryServer(t)
	root := t.TempDir()
	agent.results["session/skills_install_preview"] = map[string]any{"stagingToken": "tok"}
	doReq(t, s, http.MethodPost, "/api/library/skills/preview",
		map[string]any{"source": "x", "scope": "project", "project": root}, nil)
	rec := doReq(t, s, http.MethodPost, "/api/library/skills/discard", map[string]any{"stagingToken": "tok"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("discard: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("session/skills_install_discard"); len(got) != 1 || !strings.Contains(got[0], `"sessionId":"s-2"`) {
		t.Fatalf("discard params = %v", got)
	}
}

func TestLibraryConfirmUnderAnotherScopeIsRefused(t *testing.T) {
	s, _, agent := libraryServer(t)
	agent.results["session/skills_install_preview"] = map[string]any{"stagingToken": "tok"}
	doReq(t, s, http.MethodPost, "/api/library/skills/preview", map[string]any{"source": "x"}, nil)
	rec := doReq(t, s, http.MethodPost, "/api/library/skills/confirm",
		map[string]any{"stagingToken": "tok", "scope": "project", "project": t.TempDir()}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if n := len(agent.calls("session/skills_install_confirm")); n != 0 {
		t.Fatalf("a mismatched confirm reached the agent")
	}
}

func TestLibrarySkillRemoveAudits(t *testing.T) {
	s, f, agent := libraryServer(t)
	rec := doReq(t, s, http.MethodDelete, "/api/library/skills/lint?scope=global", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("session/skills_remove"); len(got) != 1 ||
		!strings.Contains(got[0], `"name":"lint"`) || !strings.Contains(got[0], `"scope":"global"`) {
		t.Fatalf("remove params = %v", got)
	}
	if findEvent(auditTail(t, f), AuditSkillRemoved) == nil {
		t.Fatal("removal not audited")
	}
}

func TestLibraryPluginFlow(t *testing.T) {
	s, f, agent := libraryServer(t)
	agent.results["session/plugins_list"] = map[string]any{"plugins": []map[string]any{{"name": "p", "scope": "global"}}}
	agent.results["session/plugins_install_scan"] = map[string]any{"scanToken": "scan1", "name": "p"}
	agent.results["session/plugins_install_confirm"] = map[string]any{"name": "p"}

	if rec := doReq(t, s, http.MethodGet, "/api/library/plugins", nil, nil); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"name":"p"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec := doReq(t, s, http.MethodPost, "/api/library/plugins/scan", map[string]any{"source": "x", "ref": "v1"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("session/plugins_install_scan"); len(got) != 1 || !strings.Contains(got[0], `"ref":"v1"`) {
		t.Fatalf("scan params = %v", got)
	}
	doReq(t, s, http.MethodPost, "/api/library/plugins/confirm", map[string]any{"scanToken": "scan1", "scope": "global"}, nil)
	if got := agent.calls("session/plugins_install_confirm"); len(got) != 1 || !strings.Contains(got[0], `"scanToken":"scan1"`) {
		t.Fatalf("confirm params = %v", got)
	}
	if findEvent(auditTail(t, f), AuditPluginInstalled) == nil {
		t.Fatal("plugin install not audited")
	}
	doReq(t, s, http.MethodDelete, "/api/library/plugins/p?scope=global", nil, nil)
	if findEvent(auditTail(t, f), AuditPluginRemoved) == nil {
		t.Fatal("plugin removal not audited")
	}
}

func TestLibraryMemoryRoutes(t *testing.T) {
	s, f, agent := libraryServer(t)
	root := t.TempDir()
	agent.results["session/memory_list"] = map[string]any{"entries": []any{}}

	if rec := doReq(t, s, http.MethodGet, "/api/library/memory?project="+root, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/library/memory/7?project="+root, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("session/memory_delete"); len(got) != 1 || !strings.Contains(got[0], `"id":7`) || !strings.Contains(got[0], `"sessionId":"s-2"`) {
		t.Fatalf("delete params = %v", got)
	}
	if e := findEvent(auditTail(t, f), AuditMemoryDeleted); e == nil || e.Detail != "7" {
		t.Fatalf("memory delete not audited: %+v", e)
	}
	rec := doReq(t, s, http.MethodPost, "/api/library/memory/7/confidence?project="+root,
		map[string]any{"confidence": "high"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("confidence: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("session/memory_set_confidence"); len(got) != 1 || !strings.Contains(got[0], `"confidence":"high"`) {
		t.Fatalf("confidence params = %v", got)
	}
	for _, path := range []string{"/api/library/memory", "/api/library/memory/abc?project=" + root} {
		method := http.MethodGet
		if strings.Contains(path, "abc") {
			method = http.MethodDelete
		}
		if rec := doReq(t, s, method, path, nil, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: %d, want 400", method, path, rec.Code)
		}
	}
}

func TestLibraryProjectScopeWithoutAMountIs501(t *testing.T) {
	s, f, _ := libraryServer(t)
	if _, err := f.control(ctlContext(t)); err != nil {
		t.Fatal(err)
	}
	f.ctl.mu.Lock()
	f.ctl.containerized, f.ctl.boundRoots = true, []string{"/somewhere/else"}
	f.ctl.mu.Unlock()
	rec := doReq(t, s, http.MethodGet, "/api/library/skills?scope=project&project="+t.TempDir(), nil, nil)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "project_library_unsupported") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestLibraryUnsupportedAgentIs501(t *testing.T) {
	s, _, agent := libraryServer(t)
	agent.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "session/skills_list" {
			return nil, &rpcError{Code: -32601, Message: "method not found"}, true
		}
		return nil, nil, false
	}
	rec := doReq(t, s, http.MethodGet, "/api/library/skills", nil, nil)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "skills_list_unsupported") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestLibraryRejectsBadScope(t *testing.T) {
	s, _, _ := libraryServer(t)
	for _, path := range []string{
		"/api/library/skills?scope=planet",
		"/api/library/skills?scope=project",
	} {
		if rec := doReq(t, s, http.MethodGet, path, nil, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestLibraryMemoryScopeSuggestionsAndPromote(t *testing.T) {
	s, f, agent := libraryServer(t)
	root := t.TempDir()
	agent.results["session/memory_list"] = map[string]any{"entries": []any{}}
	agent.results["session/memory_suggestions"] = map[string]any{"suggestions": []map[string]any{
		{"memoryId": 7, "matchProjectRoot": "/other", "suggestedScope": "global"}}}
	agent.results["session/memory_promote"] = map[string]any{}

	// scope reaches memory_list; without it, none is sent.
	if rec := doReq(t, s, http.MethodGet, "/api/library/memory?project="+root+"&scope=workspace", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, s, http.MethodGet, "/api/library/memory?project="+root, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	got := agent.calls("session/memory_list")
	if len(got) != 2 || !strings.Contains(got[0], `"scope":"workspace"`) || strings.Contains(got[1], `"scope"`) {
		t.Fatalf("memory_list params = %v", got)
	}

	rec := doReq(t, s, http.MethodGet, "/api/library/memory/suggestions?project="+root, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"memoryId":7`) {
		t.Fatalf("suggestions: %d %s", rec.Code, rec.Body.String())
	}
	if got := agent.calls("session/memory_suggestions"); len(got) != 1 || !strings.Contains(got[0], `"sessionId":"s-2"`) {
		t.Fatalf("suggestions params = %v", got)
	}

	rec = doReq(t, s, http.MethodPost, "/api/library/memory/7/promote?project="+root,
		map[string]any{"scope": "workspace", "scopeKey": "go-service"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body.String())
	}
	p := agent.calls("session/memory_promote")
	if len(p) != 1 || !strings.Contains(p[0], `"id":7`) || !strings.Contains(p[0], `"scope":"workspace"`) || !strings.Contains(p[0], `"scopeKey":"go-service"`) {
		t.Fatalf("promote params = %v", p)
	}
	if e := findEvent(auditTail(t, f), AuditMemoryPromoted); e == nil || !strings.Contains(e.Detail, "7") || !strings.Contains(e.Detail, "workspace") {
		t.Fatalf("promote not audited: %+v", e)
	}

	// Bad requests never reach the agent.
	for name, tc := range map[string]struct {
		path string
		body any
	}{
		"no scope":   {"/api/library/memory/7/promote?project=" + root, map[string]any{}},
		"no project": {"/api/library/memory/7/promote", map[string]any{"scope": "global"}},
		"bad id":     {"/api/library/memory/abc/promote?project=" + root, map[string]any{"scope": "global"}},
	} {
		if rec := doReq(t, s, http.MethodPost, tc.path, tc.body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := doReq(t, s, http.MethodGet, "/api/library/memory/suggestions", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("suggestions without a project: %d", rec.Code)
	}
	if n := len(agent.calls("session/memory_promote")); n != 1 {
		t.Errorf("a rejected promote reached the agent (%d calls)", n)
	}
}

func TestLibraryMemoryScopeRoutesAnswer501WithoutTheMethods(t *testing.T) {
	s, f, agent := libraryServer(t)
	agent.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "session/memory_suggestions" || m == "session/memory_promote" {
			return nil, &rpcError{Code: -32601, Message: "method not found"}, true
		}
		return nil, nil, false
	}
	root := t.TempDir()
	rec := doReq(t, s, http.MethodGet, "/api/library/memory/suggestions?project="+root, nil, nil)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "memory_suggestions_unsupported") {
		t.Errorf("suggestions: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(t, s, http.MethodPost, "/api/library/memory/7/promote?project="+root, map[string]any{"scope": "global"}, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("promote: %d %s", rec.Code, rec.Body.String())
	}
	if findEvent(auditTail(t, f), AuditMemoryPromoted) != nil {
		t.Error("a failed promote was audited")
	}
}
