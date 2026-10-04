package bridge

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (t *scriptedTransport) methods() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.seen...)
}

func count(ms []string, m string) int {
	n := 0
	for _, x := range ms {
		if x == m {
			n++
		}
	}
	return n
}

func postBody(srv *Server, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}

const goodComment = `{"path":"a.go","line":4,"side":"new","quote":"x := 1","body":"rename x"}`

func TestReviewCommentIdleSendsPrompt(t *testing.T) {
	tr := &scriptedTransport{gate: gateResult{OK: true}}
	f := testFleetScripted(t, tr)
	f.audit = NewAuditLog(t.TempDir())
	id := spawnGitAgent(t, f)
	srv := NewServer(f, "")
	rec := postBody(srv, "/api/agents/"+id+"/review/comments", goodComment)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for count(tr.methods(), "session/prompt") < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count(tr.methods(), "session/steer") != 0 || count(tr.methods(), "session/prompt") < 1 {
		t.Fatalf("methods = %v, want a prompt and no steer", tr.methods())
	}
	if !hasEvent(auditTail(t, f), AuditReviewComment, id) {
		t.Fatal("no review_comment audit record")
	}
}

func TestReviewCommentBusySteers(t *testing.T) {
	tr := &scriptedTransport{gate: gateResult{OK: true}}
	f := testFleetScripted(t, tr)
	id := spawnGitAgent(t, f)
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	rt.reg.mu.Lock()
	rt.reg.sessions[rt.sessionID].Busy = true
	rt.reg.mu.Unlock()
	srv := NewServer(f, "")
	if rec := postBody(srv, "/api/agents/"+id+"/review/comments", goodComment); rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if count(tr.methods(), "session/steer") != 1 {
		t.Fatalf("methods = %v, want one steer", tr.methods())
	}
}

func TestReviewCommentValidation(t *testing.T) {
	f := testFleetScripted(t, &scriptedTransport{})
	id := spawnGitAgent(t, f)
	srv := NewServer(f, "")
	for _, body := range []string{
		`{"path":"","line":1,"side":"new","body":"x"}`,
		`{"path":"a","line":1,"side":"new","body":""}`,
		`{"path":"a","line":0,"side":"new","body":"x"}`,
		`{"path":"a","line":1,"side":"middle","body":"x"}`,
	} {
		if rec := postBody(srv, "/api/agents/"+id+"/review/comments", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, rec.Code)
		}
	}
	if rec := postBody(srv, "/api/agents/nope/review/comments", goodComment); rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent = %d, want 404", rec.Code)
	}
}

func TestReviewCommentListResolveAndRemove(t *testing.T) {
	f := testFleetScripted(t, &scriptedTransport{})
	id := spawnGitAgent(t, f)
	srv := NewServer(f, "")
	first := `{"path":"a.go","line":1,"side":"old","body":"first"}`
	for _, b := range []string{first, goodComment} {
		if rec := postBody(srv, "/api/agents/"+id+"/review/comments", b); rec.Code != 200 {
			t.Fatalf("create = %d %s", rec.Code, rec.Body)
		}
		time.Sleep(2 * time.Millisecond)
	}
	cs := f.ws.ReviewComments(id)
	if len(cs) != 2 || cs[0].Body != "first" {
		t.Fatalf("comments = %+v", cs)
	}
	if rec := postBody(srv, "/api/agents/"+id+"/review/comments/"+cs[0].ID+"/resolve", ""); rec.Code != 200 {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body)
	}
	if rec := postBody(srv, "/api/agents/"+id+"/review/comments/nope/resolve", ""); rec.Code != 404 {
		t.Fatalf("resolve unknown = %d", rec.Code)
	}
	if rec := get(srv, "GET", "/api/agents/"+id+"/review/comments"); !strings.Contains(rec.Body.String(), `"resolvedAt"`) {
		t.Fatalf("list = %s", rec.Body)
	}
	if err := f.ws.RemoveAgent(id); err != nil {
		t.Fatal(err)
	}
	if got := f.ws.ReviewComments(id); len(got) != 0 {
		t.Fatalf("comments survived agent removal: %+v", got)
	}
}

func TestReviewMessageFormat(t *testing.T) {
	c := ReviewComment{Path: "a.go", Line: 3, Side: "new", Quote: "1\n2\n3\n4\n5\n6\n7\n8", Body: "why?"}
	want := "Review comment on a.go:3 (new):\n> 1\n> 2\n> 3\n> 4\n> 5\n> 6\nwhy?"
	if got := reviewMessage(c); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestWorkspaceV6LoadsAndSavesAtCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	os.WriteFile(path, []byte(`{"version":6,"projects":[],"agents":[{"id":"a1","project":"/p"}]}`), 0o600)
	ws := NewWorkspace(path)
	if backup, err := ws.Load(); err != nil || backup != "" {
		t.Fatalf("Load = %q, %v", backup, err)
	}
	if got := ws.ReviewComments("a1"); len(got) != 0 {
		t.Fatalf("reviews = %+v", got)
	}
	if err := ws.PutReviewComment(ReviewComment{ID: "c", AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), fmt.Sprintf(`"version": %d`, workspaceVersion)) || !strings.Contains(string(data), `"reviews"`) {
		t.Fatalf("saved file = %s", data)
	}
}

func TestRecentPromptsDedupAndOrder(t *testing.T) {
	f := testFleet(t)
	now := time.Now()
	for i, a := range []Agent{
		{ID: "a", Project: "/p", Prompt: "fix bug", CreatedAt: now.Add(-3 * time.Hour)},
		{ID: "b", Project: "/p", Prompt: " add tests ", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "c", Project: "/p", Prompt: "fix bug", CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "d", Project: "/other", Prompt: "elsewhere", CreatedAt: now},
		{ID: "e", Project: "/p", Prompt: "  ", CreatedAt: now},
	} {
		_ = i
		if err := f.ws.PutAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	got := f.RecentPrompts("/p", 0)
	if len(got) != 2 || got[0] != "fix bug" || got[1] != "add tests" {
		t.Fatalf("prompts = %q", got)
	}
	srv := NewServer(f, "")
	rec := get(srv, "GET", "/api/prompts/recent?project=%2Fp&limit=1")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"fix bug"`) || strings.Contains(rec.Body.String(), "add tests") {
		t.Fatalf("route = %d %s", rec.Code, rec.Body)
	}
}

func TestReviewCommentStoredEvenWhenDeliveryFails(t *testing.T) {
	tr := &scriptedTransport{errs: map[string]*rpcError{"session/prompt": {Code: -32000, Message: "boom"}}}
	f := testFleetScripted(t, tr)
	id := spawnGitAgent(t, f)
	srv := NewServer(f, "")
	if rec := postBody(srv, "/api/agents/"+id+"/review/comments", goodComment); rec.Code != http.StatusBadGateway {
		t.Fatalf("create = %d %s, want 502", rec.Code, rec.Body)
	}
	cs := f.ws.ReviewComments(id)
	if len(cs) != 1 || !cs[0].SentAt.IsZero() {
		t.Fatalf("comments = %+v, want one stored comment with no SentAt", cs)
	}
}

func TestReviewCommentSteerFailureFallsBackToPrompt(t *testing.T) {
	tr := &scriptedTransport{errs: map[string]*rpcError{"session/steer": {Code: -32000, Message: "no active turn"}}}
	f := testFleetScripted(t, tr)
	id := spawnGitAgent(t, f)
	rt, _ := f.RuntimeForSession(id)
	rt.reg.mu.Lock()
	rt.reg.sessions[rt.sessionID].Busy = true
	rt.reg.mu.Unlock()
	srv := NewServer(f, "")
	if rec := postBody(srv, "/api/agents/"+id+"/review/comments", goodComment); rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if count(tr.methods(), "session/prompt") != 1 {
		t.Fatalf("methods = %v, want a prompt after the failed steer", tr.methods())
	}
	if cs := f.ws.ReviewComments(id); len(cs) != 1 || cs[0].SentAt.IsZero() {
		t.Fatalf("comments = %+v", cs)
	}
}
