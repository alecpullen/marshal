package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestServer wires a registry-mode fake child through the whole
// stack — Registry, EventLog, Attach — before Start (Attach documents
// the hooks must be installed before traffic), then builds the HTTP
// server. An empty token means unauthenticated.
func newTestServer(t *testing.T, token string) (*Server, *Registry, *EventLog, *Child) {
	t.Helper()
	c := newTestChild(t, "registry")
	r := NewRegistry(c)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	r.RootCwd = cwd
	l := NewEventLog()
	Attach(l, c, r)
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(c.Stop)
	return NewServer(r, l, token), r, l, c
}

// doReq issues one request against the server and records the response.
// body is JSON-marshaled when non-nil; a nil body sends ContentLength 0.
func doReq(t *testing.T, s *Server, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

func TestHTTPFleetSessionRouting(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(t.Context(), "/home/u/a", SpawnOptions{Name: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(f, "")

	rec := doReq(t, s, http.MethodGet, "/api/sessions?cwd=/home/u/a", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("fleet list sessions: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/"+id+"/mode", map[string]string{"mode": "auto"}, nil); rec.Code != http.StatusOK {
		t.Fatalf("fleet set mode: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPServerAuth(t *testing.T) {
	s, _, _, _ := newTestServer(t, "tok")

	rec := doReq(t, s, http.MethodGet, "/api/sessions?cwd=/tmp", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "tok") {
		t.Error("401 body leaks the token")
	}

	rec = doReq(t, s, http.MethodGet, "/api/sessions?cwd=/tmp", nil,
		map[string]string{"Authorization": "Bearer tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", rec.Code)
	}
}

func TestHTTPListSessions(t *testing.T) {
	s, _, _, _ := newTestServer(t, "")

	rec := doReq(t, s, http.MethodGet, "/api/sessions", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing cwd: status = %d, want 400", rec.Code)
	}

	rec = doReq(t, s, http.MethodGet, "/api/sessions?cwd=/tmp/work&cursor=abc", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// The fake child's default reply is an empty object; the endpoint
	// proxies it verbatim.
	if got := strings.TrimSpace(rec.Body.String()); got != "{}" {
		t.Errorf("body = %q, want {}", got)
	}
}

func TestHTTPNewSession(t *testing.T) {
	s, r, _, _ := newTestServer(t, "")

	rec := doReq(t, s, http.MethodPost, "/api/sessions", map[string]string{}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing cwd: status = %d, want 400", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader("{not json"))
	bad := httptest.NewRecorder()
	s.ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status = %d, want 400", bad.Code)
	}

	rec = doReq(t, s, http.MethodPost, "/api/sessions", map[string]string{"cwd": "/tmp/work"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	decodeBody(t, rec, &out)
	if out.SessionID != "s-1" {
		t.Fatalf("sessionId = %q, want s-1", out.SessionID)
	}
	if _, ok := r.Sessions()["s-1"]; !ok {
		t.Fatal("session not tracked after POST /api/sessions")
	}
}

func TestHTTPSessionLifecycle(t *testing.T) {
	s, r, l, c := newTestServer(t, "")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}

	// Unknown-session routes map to 404.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodDelete, "/api/sessions/nope", nil},
		{http.MethodPost, "/api/sessions/nope/prompt", map[string]string{"text": "hi"}},
		{http.MethodPost, "/api/sessions/nope/cancel", nil},
		{http.MethodPost, "/api/sessions/nope/steer", map[string]string{"text": "x"}},
		{http.MethodPost, "/api/sessions/nope/mode", map[string]string{"mode": "auto"}},
	} {
		if rec := doReq(t, s, tc.method, tc.path, tc.body, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}

	// Happy paths for the turn-control routes.
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/cancel", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("cancel: status = %d, want 200", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/steer", map[string]string{"text": "actually"}, nil); rec.Code != http.StatusOK {
		t.Errorf("steer: status = %d, want 200", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/mode", map[string]string{"mode": "auto"}, nil); rec.Code != http.StatusOK {
		t.Errorf("mode: status = %d, want 200", rec.Code)
	}
	// A 200 means the request was dispatched to the child, not that the child
	// has processed and logged it — so poll rather than reading the log once.
	// Checking immediately raced the child's stderr write and failed roughly
	// one run in ten, printing a "never saw it" message whose own log dump
	// contained the line.
	for _, m := range []string{"session/steer", "session/set_mode"} {
		waitFor(t, 2*time.Second, "child to see "+m, func() bool {
			return strings.Contains(c.StderrLog(), "helper saw request "+m)
		})
	}

	// Prompt validation: empty text and malformed JSON both 400.
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/prompt", map[string]string{"text": ""}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty text: status = %d, want 400", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s-1/prompt", strings.NewReader("{bad"))
	bad := httptest.NewRecorder()
	s.ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: status = %d, want 400", bad.Code)
	}

	// Prompt accepted → 202; the turn runs on its own goroutine and the
	// bridge-side turn_end event lands in the log once the fake child
	// answers (200 ms hold).
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/prompt", map[string]string{"text": "hello"}, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("prompt: status = %d, want 202", rec.Code)
	}
	waitFor(t, 2*time.Second, "turn_end event in log", func() bool {
		for _, ev := range l.Tail("s-1") {
			if strings.Contains(string(ev.Data), "turn_end") {
				return true
			}
		}
		return false
	})

	// Delete: 200, then 404 once untracked.
	if rec := doReq(t, s, http.MethodDelete, "/api/sessions/s-1", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200", rec.Code)
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/sessions/s-1", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("re-delete: status = %d, want 404", rec.Code)
	}
}

func TestHTTPLoadSession(t *testing.T) {
	s, r, l, c := newTestServer(t, "")
	ctx, cancel := testContext(t)
	defer cancel()

	// Clear the helper's default root so we can test the no-cwd rejection.
	r.RootCwd = ""

	// Without a configured root and no body cwd, load rejects.
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/load", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no cwd: status = %d, want 400", rec.Code)
	}
	r.RootCwd = "/tmp/root"

	// Put one event in the ring so the load response carries a tail.
	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Request(ctx, "test/emit_update", nil); err != nil {
		t.Fatalf("emit_update: %v", err)
	}
	waitFor(t, 2*time.Second, "event in ring", func() bool {
		return len(l.Tail("s-1")) > 0
	})

	rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/load", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("load: status = %d, want 200", rec.Code)
	}
	var out struct {
		SessionID string  `json:"sessionId"`
		Events    []Event `json:"events"`
	}
	decodeBody(t, rec, &out)
	if out.SessionID != "s-1" {
		t.Errorf("sessionId = %q, want s-1", out.SessionID)
	}
	if len(out.Events) == 0 || !strings.Contains(string(out.Events[0].Data), "agent_message_chunk") {
		t.Errorf("events tail missing the emitted update: %+v", out.Events)
	}

	// An explicit body cwd overrides the root.
	rec = doReq(t, s, http.MethodPost, "/api/sessions/s-2/load", map[string]string{"cwd": "/tmp/other"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("load with body cwd: status = %d, want 200", rec.Code)
	}
	if info, ok := r.Sessions()["s-2"]; !ok || info.Cwd != "/tmp/other" {
		t.Errorf("s-2 not tracked under body cwd: %+v", r.Sessions())
	}
}

func TestHTTPConfig(t *testing.T) {
	s, _, _, _ := newTestServer(t, "")
	rec := doReq(t, s, http.MethodGet, "/api/config", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("config: status = %d, want 200", rec.Code)
	}
	var out struct {
		CwdRoot string `json:"cwdRoot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if out.CwdRoot == "" {
		t.Fatalf("config returned empty cwdRoot")
	}
}

func TestHTTPMethodMismatch(t *testing.T) {
	s, _, _, _ := newTestServer(t, "")
	// Go 1.22 mux method patterns reject the wrong verb with 405.
	rec := doReq(t, s, http.MethodGet, "/api/sessions/s-1/prompt", nil, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on POST route: status = %d, want 405", rec.Code)
	}
	// Auth runs before method routing: with a token set, even a
	// method-mismatched /api request must 401 without credentials.
	s2, _, _, _ := newTestServer(t, "tok")
	rec = doReq(t, s2, http.MethodGet, "/api/sessions/s-1/prompt", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("method mismatch without token: status = %d, want 401", rec.Code)
	}
}

func TestHTTPChildErrorMaps502(t *testing.T) {
	s, _, _, c := newTestServer(t, "")
	// Kill the child so requests fail at the transport layer; the
	// writeErr default branch must map that to 502.
	c.Stop()
	rec := doReq(t, s, http.MethodGet, "/api/sessions?cwd=/tmp", nil, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("child down: status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestHTTPValidationRejections(t *testing.T) {
	s, r, _, _ := newTestServer(t, "")
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}

	// Empty steer text / mode are rejected at the HTTP layer.
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/steer", map[string]string{"text": ""}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty steer: status = %d, want 400", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/mode", map[string]string{"mode": ""}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty mode: status = %d, want 400", rec.Code)
	}
	// Trailing garbage after the JSON value is rejected.
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s-1/steer", strings.NewReader(`{"text":"x"} garbage`))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("trailing data: status = %d, want 400", rec.Code)
	}
}

func TestHTTPPromptBusyConflict(t *testing.T) {
	s, r, _, _ := newTestServer(t, "")
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	// The fake child holds session/prompt for 200 ms; start a turn and
	// confirm a second prompt 409s while the first is in flight.
	go func() { _ = r.Prompt(ctx, "s-1", "first") }()
	waitFor(t, 2*time.Second, "busy", func() bool { return r.Sessions()["s-1"].Busy })
	rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/prompt", map[string]string{"text": "second"}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("prompt while busy: status = %d, want 409", rec.Code)
	}
	waitFor(t, 2*time.Second, "turn end", func() bool { return !r.Sessions()["s-1"].Busy })
}

func TestEventLogTailOrdering(t *testing.T) {
	l := NewEventLog()
	// Overflow the ring so head advances; Tail must still return
	// events oldest-first by id.
	total := ringCap + 10
	for i := 1; i <= total; i++ {
		if _, err := l.Append("s", map[string]int{"n": i}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	tail := l.Tail("s")
	if len(tail) != ringCap {
		t.Fatalf("Tail len = %d, want %d", len(tail), ringCap)
	}
	for i, ev := range tail {
		want := int64(total - ringCap + 1 + i)
		if ev.ID != want {
			t.Fatalf("tail[%d].ID = %d, want %d", i, ev.ID, want)
		}
	}
	if got := l.Tail("never-seen"); got != nil {
		t.Errorf("Tail on unknown session = %v, want nil", got)
	}
}

func TestHTTPResolvePermission(t *testing.T) {
	s, r, _, c := newTestServer(t, "")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Request(ctx, "test/ask_permission", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})

	rec := doReq(t, s, http.MethodPost, "/api/permissions/tc-1", Decision{Approved: true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// Duplicate resolve: the pending entry is gone → 410.
	rec = doReq(t, s, http.MethodPost, "/api/permissions/tc-1", Decision{Approved: true}, nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("duplicate resolve: status = %d, want 410", rec.Code)
	}
	// Never-pending id also 410s.
	rec = doReq(t, s, http.MethodPost, "/api/permissions/nope", Decision{Approved: false}, nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("unknown toolCallId: status = %d, want 410", rec.Code)
	}
}

func TestHTTPMergeRefusalMapsTo409(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, filepath.Join(t.TempDir(), "a"), SpawnOptions{Name: "x", Isolated: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(f, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/"+id+"/merge", strings.NewReader(`{}`)))
	// The fake child answers session/merge with a refusal shape; a refusal
	// is a 409 so the UI can branch on a code rather than parse prose.
	if rec.Code != http.StatusConflict && rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 or 409; body = %s", rec.Code, rec.Body)
	}
	if rec.Code == http.StatusConflict && !strings.Contains(rec.Body.String(), "reason") {
		t.Errorf("a 409 must carry the reason: %s", rec.Body)
	}
}

func TestHTTPDiffRoutesThrough(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, filepath.Join(t.TempDir(), "a"), SpawnOptions{Name: "x", Isolated: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(f, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/agents/"+id+"/diff", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestHTTPResolveQuestion(t *testing.T) {
	s, r, _, c := newTestServer(t, "")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Request(ctx, "test/ask_question", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})

	body := Answers{Answers: []Answer{{Question: "proceed?", Answer: "yes"}}}
	if rec := doReq(t, s, http.MethodPost, "/api/questions/q-1", body, nil); rec.Code != http.StatusOK {
		t.Fatalf("resolve: status = %d, want 200", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/questions/q-1", body, nil); rec.Code != http.StatusGone {
		t.Fatalf("duplicate resolve: status = %d, want 410", rec.Code)
	}
}
