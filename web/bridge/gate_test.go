package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveAgent(t *testing.T, tr *scriptedTransport) (*Server, string, *Fleet) {
	t.Helper()
	f := testFleetScripted(t, tr)
	return NewServer(f, ""), spawnGitAgent(t, f), f
}

func get(srv *Server, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestAgentVerifyStoresGateAndBroadcasts(t *testing.T) {
	srv, id, f := serveAgent(t, &scriptedTransport{gate: gateResult{OK: false, FailedCommand: "go test"}})

	if rec := get(srv, "GET", "/api/agents/"+id+"/gate"); rec.Code != http.StatusNoContent {
		t.Fatalf("gate before run = %d, want 204", rec.Code)
	}
	rec := get(srv, "POST", "/api/agents/"+id+"/verify")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"failedCommand":"go test"`) {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body)
	}
	rec = get(srv, "GET", "/api/agents/"+id+"/gate")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"failedCommand":"go test"`) {
		t.Fatalf("gate = %d %s", rec.Code, rec.Body)
	}
	var sawGate bool
	for _, ev := range f.FleetLog().Tail(fleetStreamKey) {
		if strings.Contains(string(ev.Data), `"kind":"gate"`) {
			sawGate = true
		}
	}
	if !sawGate {
		t.Fatal("no gate delta on the fleet stream")
	}
}

func TestExitStoresGate(t *testing.T) {
	srv, id, f := serveAgent(t, &scriptedTransport{gate: gateResult{OK: false}})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/"+id+"/exit", strings.NewReader(`{"commitMessage":"w"}`)))
	if _, ok := f.Gate(id); !ok {
		t.Fatal("exit did not store the gate result")
	}
}

func TestAgentFilesAndCommitDraft(t *testing.T) {
	srv, id, _ := serveAgent(t, &scriptedTransport{results: map[string]any{
		"session/files":        map[string]any{"entries": []any{}},
		"session/file":         map[string]any{"content": "hi"},
		"session/commit_draft": map[string]any{"message": "feat: x"},
	}})
	for _, c := range []struct{ path, want string }{
		{"/api/agents/" + id + "/files?path=src", `"entries"`},
		{"/api/agents/" + id + "/file?path=a.go", `"content":"hi"`},
		{"/api/agents/" + id + "/commit-draft", `"message":"feat: x"`},
	} {
		if rec := get(srv, "GET", c.path); rec.Code != 200 || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s = %d %s", c.path, rec.Code, rec.Body)
		}
	}
	if rec := get(srv, "GET", "/api/agents/nope/files"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent = %d, want 404", rec.Code)
	}
}

func TestAgentFilesUnsupported(t *testing.T) {
	nf := &rpcError{Code: -32601, Message: "method not found"}
	srv, id, _ := serveAgent(t, &scriptedTransport{errs: map[string]*rpcError{
		"session/files": nf, "session/file": nf, "session/commit_draft": nf,
	}})
	for path, want := range map[string]string{
		"/api/agents/" + id + "/files":        "files_unsupported",
		"/api/agents/" + id + "/file":         "file_unsupported",
		"/api/agents/" + id + "/commit-draft": "commit_draft_unsupported",
	} {
		if rec := get(srv, "GET", path); rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s = %d %s", path, rec.Code, rec.Body)
		}
	}
}
