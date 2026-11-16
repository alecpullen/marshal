package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer wires a registry-mode fake child through the whole
// stack — Registry, EventLog, Attach — before Start (Attach documents
// the hooks must be installed before traffic), then builds the HTTP
// server. An empty token means unauthenticated.
func newTestServer(t *testing.T, token string, transport ...agentTransport) (*Server, *Registry, *EventLog, *Child) {
	t.Helper()
	c := newTestChild(t, "registry")
	if len(transport) > 0 {
		c.Transport = transport[0]
	}
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

func TestSessionStackRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     string
		rpcErr *rpcError
		status int
		body   string
	}{
		{"snapshot", "s-1", nil, http.StatusOK, testStackResult},
		{"unknown", "nope", nil, http.StatusNotFound, `{"error":"bridge: unknown session"}`},
		{"unsupported", "s-1", &rpcError{Code: -32601, Message: "method not found"}, http.StatusNotImplemented, `{"error":"stack_unsupported"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, reg, _, _ := newTestServer(t, "", &captureTransport{stackResult: json.RawMessage(testStackResult), stackError: tc.rpcErr})
			reg.track("s-1", AgentPath("/tmp/work"))
			rec := doReq(t, s, http.MethodGet, "/api/sessions/"+tc.id+"/stack", nil, nil)
			if rec.Code != tc.status || strings.TrimSpace(rec.Body.String()) != tc.body {
				t.Fatalf("response = %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q", ct)
			}
		})
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

	if _, err := r.New(ctx, AgentPath("/tmp/work"), ""); err != nil {
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
	if _, err := r.New(ctx, AgentPath("/tmp/work"), ""); err != nil {
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
	if _, err := r.New(ctx, AgentPath("/tmp/work"), ""); err != nil {
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
	if _, err := r.New(ctx, AgentPath("/tmp/work"), ""); err != nil {
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

	if _, err := r.New(ctx, AgentPath("/tmp/work"), ""); err != nil {
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

func TestHTTPExitRoutesThrough(t *testing.T) {
	f := testFleetWithGate(t, gateResult{OK: false, FailedCommand: "go test ./..."})
	id := spawnGitAgent(t, f)
	srv := NewServer(f, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/agents/"+id+"/exit",
		strings.NewReader(`{"commitMessage":"work"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var out ExitResult
	decodeBody(t, rec, &out)
	if !out.Blocked {
		t.Fatalf("a failing gate must block: %+v", out)
	}
}

func TestHTTPPatchRoutesThrough(t *testing.T) {
	f := testFleetWithGate(t, gateResult{OK: true})
	id := spawnGitAgent(t, f)
	srv := NewServer(f, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/agents/"+id+"/patch", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/x-patch" {
		t.Errorf("Content-Type = %q, want text/x-patch", ct)
	}
}

func TestHTTPResolveQuestion(t *testing.T) {
	s, r, _, c := newTestServer(t, "")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, AgentPath("/tmp/work"), ""); err != nil {
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

func TestCreateClientReturnsTheTokenExactlyOnce(t *testing.T) {
	s := testServer(t)
	created := postJSON(t, s, "/api/clients", `{"name":"claude-code"}`)
	token, _ := created["token"].(string)
	if token == "" {
		t.Fatal("client creation did not return a token")
	}

	// Listing must never expose it again — that is the point of hashing.
	list := getJSON(t, s, "/api/clients")
	if strings.Contains(list, token) {
		t.Fatal("the plaintext token is retrievable from the client list")
	}
	if strings.Contains(list, "tokenHash") {
		t.Fatal("the token hash is exposed to the UI; it has no business there")
	}
}

func TestPendingEndpointsAreUnderAPIAuth(t *testing.T) {
	s := testServerWithToken(t, "shared")
	for _, path := range []string{"/api/pending", "/api/clients"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a token, want 401", path, rec.Code)
		}
	}
}

func postJSON(t *testing.T, s *Server, path, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("POST %s: status %d, body %s", path, rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return out
}

func getJSON(t *testing.T, s *Server, path string) string {
	t.Helper()
	rec := doReq(t, s, "GET", path, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func testServerWithAudit(t *testing.T) *Server {
	t.Helper()
	f := testFleet(t)
	f.audit = NewAuditLog(t.TempDir())
	return NewServer(f, "")
}

func TestAuditEndpointRequiresAuth(t *testing.T) {
	s := testServerWithToken(t, "shared")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/audit", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d without a token, want 401", rec.Code)
	}
}

func TestAuditEndpointBoundsTheLimit(t *testing.T) {
	s := testServerWithAudit(t)
	body := getJSON(t, s, "/api/audit?limit=100000")
	var out []AuditEvent
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) > maxAuditTail {
		t.Fatalf("returned %d records, want at most %d", len(out), maxAuditTail)
	}
}

func TestSessionProxyRoutes(t *testing.T) {
	routes := []struct {
		path    string
		method  string
		feature string
	}{
		{"/api/sessions/%s/stack?subagent=3", "session/stack", "stack"},
		{"/api/sessions/%s/nodes/tool%%3A40%%3Acall_1", "session/stack_node", "stack_node"},
		{"/api/sessions/%s/last-request", "session/last_request", "last_request"},
		{"/api/sessions/%s/step-diffs", "session/step_diffs", "step_diffs"},
	}
	for _, rt := range routes {
		t.Run(rt.feature, func(t *testing.T) {
			const body = `{"ok":true}`
			s, reg, _, _ := newTestServer(t, "", &captureTransport{results: map[string]json.RawMessage{rt.method: json.RawMessage(body)}})
			reg.track("s-1", AgentPath("/tmp/work"))
			if rec := doReq(t, s, http.MethodGet, fmt.Sprintf(rt.path, "s-1"), nil, nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != body {
				t.Fatalf("ok: %d %s", rec.Code, rec.Body.String())
			}
			if rec := doReq(t, s, http.MethodGet, fmt.Sprintf(rt.path, "nope"), nil, nil); rec.Code != http.StatusNotFound {
				t.Fatalf("unknown: %d", rec.Code)
			}
			s, reg, _, _ = newTestServer(t, "", &captureTransport{errs: map[string]*rpcError{rt.method: {Code: -32601, Message: "method not found"}}})
			reg.track("s-1", AgentPath("/tmp/work"))
			rec := doReq(t, s, http.MethodGet, fmt.Sprintf(rt.path, "s-1"), nil, nil)
			if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), rt.feature+"_unsupported") {
				t.Fatalf("unsupported: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSessionNodeIDSurvivesRoundTrip(t *testing.T) {
	tr := &captureTransport{results: map[string]json.RawMessage{"session/stack_node": json.RawMessage(`{}`)}}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))
	doReq(t, s, http.MethodGet, "/api/sessions/s-1/nodes/tool%3A40%3Acall_1", nil, nil)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	last := tr.seen[len(tr.seen)-1]
	if !strings.Contains(last.params, `"nodeId":"tool:40:call_1"`) {
		t.Fatalf("params = %s", last.params)
	}
}

func TestSessionStackBadSubagent(t *testing.T) {
	s, reg, _, _ := newTestServer(t, "", &captureTransport{})
	reg.track("s-1", AgentPath("/tmp/work"))
	if rec := doReq(t, s, http.MethodGet, "/api/sessions/s-1/stack?subagent=abc", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

const testRosterResult = `{"roles":[{"role":"implementer","preset":"fast","model":"m"}]}`

func TestSessionRosterRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     string
		rpcErr *rpcError
		status int
		body   string
	}{
		{"passthrough", "s-1", nil, http.StatusOK, testRosterResult},
		{"unknown", "nope", nil, http.StatusNotFound, `{"error":"bridge: unknown session"}`},
		{"unsupported", "s-1", &rpcError{Code: -32601, Message: "method not found"}, http.StatusNotImplemented, `{"error":"roster_unsupported"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct := &captureTransport{results: map[string]json.RawMessage{"session/agents_roster": json.RawMessage(testRosterResult)}}
			if tc.rpcErr != nil {
				ct.errs = map[string]*rpcError{"session/agents_roster": tc.rpcErr}
			}
			s, reg, _, _ := newTestServer(t, "", ct)
			reg.track("s-1", AgentPath("/tmp/work"))
			rec := doReq(t, s, http.MethodGet, "/api/sessions/"+tc.id+"/roster", nil, nil)
			if rec.Code != tc.status || strings.TrimSpace(rec.Body.String()) != tc.body {
				t.Fatalf("response = %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.body)
			}
		})
	}
}

// TestAccessRouteReadsFullState pins the authoritative read: GET /access
// proxies session/access_state and returns every field, including an explicit
// false, so a client never has to infer privileges from defaults.
func TestAccessRouteReadsFullState(t *testing.T) {
	tr := &captureTransport{results: map[string]json.RawMessage{
		"session/access_state": json.RawMessage(`{"mode":"act","auto":true,"system_access":false,"access_revision":9}`),
	}}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))

	rec := doReq(t, s, http.MethodGet, "/api/sessions/s-1/access", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var st AccessState
	decodeBody(t, rec, &st)
	if want := (AccessState{Mode: "act", Auto: true, System: false, Revision: 9}); st != want {
		t.Fatalf("state = %+v, want %+v", st, want)
	}
	body := rec.Body.String()
	for _, want := range []string{`"mode":"act"`, `"auto":true`, `"system_access":false`, `"access_revision":9`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s missing %s", body, want)
		}
	}

	// An untracked session is a 404, not an empty state.
	if rec := doReq(t, s, http.MethodGet, "/api/sessions/nope/access", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status = %d, want 404", rec.Code)
	}
}

// TestAccessRouteForwardsPatch pins the write half of the route: a valid
// atomic patch reaches session/set_mode with its explicit false intact, and
// the response is the full committed state.
func TestAccessRouteForwardsPatch(t *testing.T) {
	tr := &captureTransport{results: map[string]json.RawMessage{
		"session/set_mode": json.RawMessage(`{"mode":"act","auto":false,"system_access":false,"access_revision":5}`),
	}}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))

	rec := doReq(t, s, http.MethodPatch, "/api/sessions/s-1/access", map[string]any{"auto": false}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var st AccessState
	decodeBody(t, rec, &st)
	if want := (AccessState{Mode: "act", Auto: false, System: false, Revision: 5}); st != want {
		t.Fatalf("state = %+v, want %+v", st, want)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	var frame capturedFrame
	for _, f := range tr.seen {
		if f.method == "session/set_mode" {
			frame = f
		}
	}
	if frame.method == "" {
		t.Fatalf("no session/set_mode frame; got %+v", tr.seen)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(frame.params), &params); err != nil {
		t.Fatalf("decode set_mode params %s: %v", frame.params, err)
	}
	if v, ok := params["auto"]; !ok || v != false {
		t.Fatalf("auto = %#v (present %v) in %s; an explicit false must be forwarded", v, ok, frame.params)
	}
}

// TestAccessRouteRejectsNullPatch pins strict client-side validation: a null,
// unknown, wrong-typed or empty patch is rejected before it is forwarded, so a
// client that misspells a field is never told its patch was applied.
func TestAccessRouteRejectsNullPatch(t *testing.T) {
	tr := &captureTransport{}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))

	for _, tc := range []struct{ name, body string }{
		{"null", `null`},
		{"empty object", `{}`},
		{"unknown field", `{"mode":"act","yolo":true}`},
		{"null field", `{"auto":null}`},
		{"wrong type", `{"system_access":"yes"}`},
		{"unknown mode", `{"mode":"yolo"}`},
		{"array", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/sessions/s-1/access", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}

	// Nothing was forwarded: a rejected patch must not reach the agent.
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, f := range tr.seen {
		if f.method == "session/set_mode" {
			t.Fatalf("rejected patch was forwarded: %s", f.params)
		}
	}
}

// subagentFrames returns every frame the fake child received for one method.
func subagentFrames(t *testing.T, tr *captureTransport, method string) []capturedFrame {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []capturedFrame
	for _, f := range tr.seen {
		if f.method == method {
			out = append(out, f)
		}
	}
	return out
}

// silentTransport answers every method but one, which it receives and never
// answers: what the bridge sees when the child died, or lost the reply, after
// taking the request. sent counts what it took, since a swallowed frame never
// reaches the fake child's recorder.
type silentTransport struct {
	*captureTransport
	method string
	sent   *atomic.Int64
}

func (s silentTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdin, stdout, stderr, err := s.captureTransport.Open()
	if err != nil {
		return nil, nil, nil, err
	}
	return silentWriter{next: stdin, method: s.method, sent: s.sent}, stdout, stderr, nil
}

type silentWriter struct {
	next   io.WriteCloser
	method string
	sent   *atomic.Int64
}

func (w silentWriter) Write(p []byte) (int, error) {
	var frame struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(bytes.TrimSpace(p), &frame) == nil && frame.Method == w.method {
		// Taken, never answered: the frame is not forwarded to the fake child.
		w.sent.Add(1)
		return len(p), nil
	}
	return w.next.Write(p)
}

func (w silentWriter) Close() error { return w.next.Close() }

// TestSubagentSteerRouteForwards pins the route's shape: the session and the
// child come from the URL, the body carries only the request and its text, the
// forwarded frame names the owning session, and the answer is the session's
// acceptance — never a claim that the child read it.
func TestSubagentSteerRouteForwards(t *testing.T) {
	tr := &captureTransport{results: map[string]json.RawMessage{
		"session/subagent_steer": json.RawMessage(`{"requestId":"req-1","status":"accepted"}`),
	}}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))

	rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/subagents/7/steer",
		map[string]any{"requestId": "req-1", "message": "wrap up and report"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	decodeBody(t, rec, &out)
	if out.RequestID != "req-1" || out.Status != "accepted" {
		t.Fatalf("result = %+v, want request req-1 accepted", out)
	}
	// Acceptance is not consumption: the body says no more than the session did.
	if body := rec.Body.String(); strings.Contains(body, "consumed") || strings.Contains(body, "applied") {
		t.Fatalf("body %s claims delivery or consumption", body)
	}

	frames := subagentFrames(t, tr, "session/subagent_steer")
	if len(frames) != 1 {
		t.Fatalf("session/subagent_steer frames = %d, want 1", len(frames))
	}
	var params struct {
		SessionID  string `json:"sessionId"`
		SubagentID int64  `json:"subagentId"`
		RequestID  string `json:"requestId"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frames[0].params), &params); err != nil {
		t.Fatalf("decode params %s: %v", frames[0].params, err)
	}
	if params.SessionID != "s-1" || params.SubagentID != 7 || params.RequestID != "req-1" || params.Message != "wrap up and report" {
		t.Fatalf("params = %+v, want the route's session and child with the body's request", params)
	}
}

// TestSubagentCancelRouteUsesTheExistingControl pins the cancel half: it is a
// route onto session/subagent_cancel, not a second cancellation mechanism, and
// its answer is an acknowledgement rather than a stopped child.
func TestSubagentCancelRouteUsesTheExistingControl(t *testing.T) {
	tr := &captureTransport{results: map[string]json.RawMessage{
		"session/subagent_cancel": json.RawMessage(`{"cancelled":true}`),
	}}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))

	rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/subagents/3/cancel", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	decodeBody(t, rec, &out)
	if !out.Cancelled {
		t.Fatalf("cancelled = false, want the session's acknowledgement")
	}

	frames := subagentFrames(t, tr, "session/subagent_cancel")
	if len(frames) != 1 {
		t.Fatalf("session/subagent_cancel frames = %d, want 1", len(frames))
	}
	var params struct {
		SessionID  string `json:"sessionId"`
		SubagentID int64  `json:"subagentId"`
	}
	if err := json.Unmarshal([]byte(frames[0].params), &params); err != nil {
		t.Fatalf("decode params %s: %v", frames[0].params, err)
	}
	if params.SessionID != "s-1" || params.SubagentID != 3 {
		t.Fatalf("params = %+v, want s-1's child 3", params)
	}

	// No turn-level cancel rides along: the child's stop is the only one asked
	// for, through the one control that exists for it.
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, f := range tr.seen {
		if strings.Contains(f.method, "cancel") && f.method != "session/subagent_cancel" {
			t.Fatalf("cancel route sent %s: a second cancellation mechanism", f.method)
		}
	}
}

// TestSubagentRoutesRejectMalformedIDs keeps a typo out of the agent: a child
// ID that is not a positive integer, or a body missing its request or text, is
// refused here and never forwarded.
func TestSubagentRoutesRejectMalformedIDs(t *testing.T) {
	s, reg, _, _ := newTestServer(t, "", &captureTransport{})
	reg.track("s-1", AgentPath("/tmp/work"))

	for _, tc := range []struct{ name, path, body, code string }{
		{"child id is not a number", "/api/sessions/s-1/subagents/child-1/steer", `{"requestId":"r","message":"hi"}`, "invalid"},
		{"child id is zero", "/api/sessions/s-1/subagents/0/steer", `{"requestId":"r","message":"hi"}`, "invalid"},
		{"child id is negative", "/api/sessions/s-1/subagents/-2/steer", `{"requestId":"r","message":"hi"}`, "invalid"},
		{"child id is fractional", "/api/sessions/s-1/subagents/1.5/steer", `{"requestId":"r","message":"hi"}`, "invalid"},
		{"cancel child id is not a number", "/api/sessions/s-1/subagents/child-1/cancel", ``, "invalid"},
		{"missing request id", "/api/sessions/s-1/subagents/1/steer", `{"message":"hi"}`, "invalid"},
		{"blank request id", "/api/sessions/s-1/subagents/1/steer", `{"requestId":"  ","message":"hi"}`, "invalid"},
		{"missing message", "/api/sessions/s-1/subagents/1/steer", `{"requestId":"r"}`, "invalid"},
		{"blank message", "/api/sessions/s-1/subagents/1/steer", `{"requestId":"r","message":"  "}`, "invalid"},
		// A body that is not one JSON value is refused by the decoder itself.
		{"trailing value", "/api/sessions/s-1/subagents/1/steer", `{"requestId":"r","message":"hi"}{}`, ""},
		{"null body", "/api/sessions/s-1/subagents/1/steer", `null`, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if tc.code == "" {
				return
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			decodeBody(t, rec, &body)
			if body.Code != tc.code {
				t.Fatalf("code = %q, want %q (body %s)", body.Code, tc.code, rec.Body.String())
			}
		})
	}
}

// TestSubagentRoutesAreAuthorizedAndSessionScoped pins the two refusals around
// the controls — no token, no such session — and the rule that makes a child ID
// safe to route on: it is session-local, so two sessions holding child 1 each
// reach only their own.
func TestSubagentRoutesAreAuthorizedAndSessionScoped(t *testing.T) {
	tr := &captureTransport{results: map[string]json.RawMessage{
		"session/subagent_steer":  json.RawMessage(`{"requestId":"r","status":"accepted"}`),
		"session/subagent_cancel": json.RawMessage(`{"cancelled":true}`),
	}}
	s, reg, _, _ := newTestServer(t, "tok", tr)
	reg.track("s-1", AgentPath("/tmp/one"))
	reg.track("s-2", AgentPath("/tmp/two"))
	auth := map[string]string{"Authorization": "Bearer tok"}

	// No token: refused before the session is even resolved.
	rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/subagents/1/steer",
		map[string]any{"requestId": "r", "message": "hi"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}

	// A session this bridge does not serve: 404, with nothing forwarded.
	rec = doReq(t, s, http.MethodPost, "/api/sessions/nope/subagents/1/steer",
		map[string]any{"requestId": "r", "message": "hi"}, auth)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decodeBody(t, rec, &body)
	if body.Code != "unknown_session" {
		t.Fatalf("unknown session code = %q, want unknown_session", body.Code)
	}
	if got := len(subagentFrames(t, tr, "session/subagent_steer")); got != 0 {
		t.Fatalf("refused requests were forwarded: %d frames", got)
	}

	// Two sessions, the same child ID: each request reaches its own session.
	for _, session := range []string{"s-1", "s-2"} {
		rec := doReq(t, s, http.MethodPost, "/api/sessions/"+session+"/subagents/1/steer",
			map[string]any{"requestId": "req-" + session, "message": "hi"}, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", session, rec.Code, rec.Body.String())
		}
	}
	frames := subagentFrames(t, tr, "session/subagent_steer")
	if len(frames) != 2 {
		t.Fatalf("session/subagent_steer frames = %d, want 2", len(frames))
	}
	for i, session := range []string{"s-1", "s-2"} {
		var params struct {
			SessionID  string `json:"sessionId"`
			SubagentID int64  `json:"subagentId"`
			RequestID  string `json:"requestId"`
		}
		if err := json.Unmarshal([]byte(frames[i].params), &params); err != nil {
			t.Fatalf("decode params %s: %v", frames[i].params, err)
		}
		if params.SessionID != session || params.SubagentID != 1 || params.RequestID != "req-"+session {
			t.Fatalf("frame %d = %+v, want child 1 of %s carrying its own request", i, params, session)
		}
	}
}

// TestSubagentSteerUnsupportedIsNotAConflict pins an older agent's answer: a
// missing session/subagent_steer is 501 unsupported, never the session's 409.
func TestSubagentSteerUnsupportedIsNotAConflict(t *testing.T) {
	tr := &captureTransport{errs: map[string]*rpcError{
		"session/subagent_steer": {Code: rpcMethodNotFound, Message: "method not found: session/subagent_steer"},
	}}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))

	rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/subagents/9/steer",
		map[string]any{"requestId": "req-1", "message": "hi"}, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decodeBody(t, rec, &body)
	if body.Code != "unsupported" {
		t.Fatalf("code = %q, want unsupported", body.Code)
	}
	if !strings.Contains(body.Error, "subagent_steer") {
		t.Fatalf("error = %q, want the method named as unsupported", body.Error)
	}
}

// TestSubagentSteerRefusalsAreDistinct pins the session's own refusals: a
// request ID reused for something else is a conflict, a child that is unknown
// or no longer live is an unknown delivery, and anything the bridge does not
// recognise stays a plain refusal instead of being guessed into a conflict.
func TestSubagentSteerRefusalsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		status  int
		code    string
	}{
		{
			"reused request id",
			`session: subagent steer request "req-1" was already used for a different child or instruction`,
			http.StatusConflict, "conflict",
		},
		{
			"unknown child",
			`session: unknown subagent id 9`,
			http.StatusNotFound, "unknown_delivery",
		},
		{
			"finished child",
			`session: subagent 9 is no longer running; a message cannot reach a finished subagent`,
			http.StatusNotFound, "unknown_delivery",
		},
		{
			"child with no live session",
			`session: subagent 9 has no live session to steer`,
			http.StatusNotFound, "unknown_delivery",
		},
		{
			"unrecognised refusal",
			`session: this session refuses for its own reason`,
			http.StatusConflict, "refused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &captureTransport{errs: map[string]*rpcError{
				"session/subagent_steer": {Code: rpcServerError, Message: tc.message},
			}}
			s, reg, _, _ := newTestServer(t, "", tr)
			reg.track("s-1", AgentPath("/tmp/work"))

			rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/subagents/9/steer",
				map[string]any{"requestId": "req-1", "message": "hi"}, nil)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			decodeBody(t, rec, &body)
			if body.Code != tc.code {
				t.Fatalf("code = %q, want %q", body.Code, tc.code)
			}
			if body.Error != tc.message {
				t.Fatalf("error = %q, want the session's own sentence %q", body.Error, tc.message)
			}
		})
	}
}

// TestSubagentSteerLostReplyIsConnectionLoss pins the one outcome a retry must
// treat differently: the request reached the child and no answer came back, so
// whether it was accepted is unknown and the answer says so.
func TestSubagentSteerLostReplyIsConnectionLoss(t *testing.T) {
	var sent atomic.Int64
	tr := silentTransport{captureTransport: &captureTransport{}, method: "session/subagent_steer", sent: &sent}
	s, reg, _, _ := newTestServer(t, "", tr)
	reg.track("s-1", AgentPath("/tmp/work"))

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s-1/subagents/5/steer",
		strings.NewReader(`{"requestId":"req-1","message":"wrap up"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decodeBody(t, rec, &body)
	if body.Code != "connection_lost" {
		t.Fatalf("code = %q, want connection_lost", body.Code)
	}
	// The frame was written, which is exactly why the outcome is unknown.
	if got := sent.Load(); got != 1 {
		t.Fatalf("swallowed frames = %d, want the request to have been sent once", got)
	}
}

// TestSubagentSteerWithoutAnAcceptanceIsNotSuccess pins a reply that arrives
// but carries no acceptance: it is not reported as one.
func TestSubagentSteerWithoutAnAcceptanceIsNotSuccess(t *testing.T) {
	// The fake child's default answer to an unknown method is an empty object.
	s, reg, _, _ := newTestServer(t, "", &captureTransport{})
	reg.track("s-1", AgentPath("/tmp/work"))

	rec := doReq(t, s, http.MethodPost, "/api/sessions/s-1/subagents/5/steer",
		map[string]any{"requestId": "req-1", "message": "wrap up"}, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	decodeBody(t, rec, &body)
	if body.Code != "connection_lost" {
		t.Fatalf("code = %q, want connection_lost", body.Code)
	}
}
