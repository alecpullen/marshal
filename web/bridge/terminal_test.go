package bridge

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// termEnv is a fleet with one live agent, a fake runtime and a fake
// streamer.
type termEnv struct {
	f    *Fleet
	srv  *Server
	id   string
	tr   *scriptedTransport
	fs   *fakeStreamer
	root string
	now  time.Time
}

func newTermEnv(t *testing.T, container bool) *termEnv {
	t.Helper()
	root := t.TempDir()
	tr := &scriptedTransport{results: map[string]any{
		"session/files": map[string]any{"root": root, "entries": []any{}},
	}}
	srv, id, f := serveAgent(t, tr)
	fs := &fakeStreamer{}
	f.streamer = fs.start
	e := &termEnv{f: f, srv: srv, id: id, tr: tr, fs: fs, root: root, now: time.Unix(1_800_000_000, 0)}
	f.clock = func() time.Time { return e.now }
	if container {
		f.runner = newFakeImages().run
		rt, err := f.runtimeForAgent(id)
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		rt.child.Containerized = true
		f.mu.Unlock()
	}
	return e
}

func (e *termEnv) post(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func (e *termEnv) open(t *testing.T) string {
	t.Helper()
	rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal", `{"cols":100,"rows":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		TerminalID string `json:"terminalId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.TerminalID == "" {
		t.Fatalf("open body %s", rec.Body)
	}
	return out.TerminalID
}

func (e *termEnv) input(t *testing.T, tid, text string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(text))})
	return e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/input", string(body))
}

func (e *termEnv) holds(on bool) int {
	n := 0
	want := `"on":true`
	if !on {
		want = `"on":false`
	}
	for _, p := range e.tr.paramsOf("session/hold") {
		if strings.Contains(p, want) {
			n++
		}
	}
	return n
}

func (e *termEnv) holdDeltas() []string {
	var out []string
	for _, ev := range e.f.FleetLog().Tail(fleetStreamKey) {
		if strings.Contains(string(ev.Data), `"kind":"hold"`) {
			out = append(out, string(ev.Data))
		}
	}
	return out
}

func TestTerminalOpenInContainerModeExecsScript(t *testing.T) {
	e := newTermEnv(t, true)
	e.open(t)
	_, call := e.fs.last()
	if call.dir != "" {
		t.Errorf("dir = %q, want empty", call.dir)
	}
	got := strings.Join(call.args, " ")
	if call.name != "docker" || !strings.Contains(got, "exec -i -w /work -e TERM=xterm-256color "+containerNameFor(e.id)+" script -qfc") ||
		!strings.Contains(got, "stty rows 30 cols 100; exec ${SHELL:-sh} /dev/null") {
		t.Fatalf("%s %s", call.name, got)
	}
}

func TestTerminalProcessModeUsesTheActiveRoot(t *testing.T) {
	e := newTermEnv(t, false)
	e.open(t)
	_, call := e.fs.last()
	if call.name != "script" || call.dir != e.root {
		t.Fatalf("call = %+v, want script in %s", call, e.root)
	}
	if call.args[0] != "-qfc" || call.args[len(call.args)-1] != "/dev/null" {
		t.Fatalf("args = %v", call.args)
	}
}

func TestTerminalOutputArrivesOnTheTerminalKey(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	proc, _ := e.fs.last()
	proc.emit("hello\n")
	key := terminalKeyPrefix + tid
	waitFor(t, 5*time.Second, "output event", func() bool {
		for _, ev := range e.f.TerminalLog().Tail(key) {
			var p struct {
				Data string `json:"data"`
			}
			if json.Unmarshal(ev.Data, &p) == nil {
				if b, _ := base64.StdEncoding.DecodeString(p.Data); string(b) == "hello\n" {
					return true
				}
			}
		}
		return false
	})
	proc.exit()
	waitFor(t, 5*time.Second, "terminal removed", func() bool {
		_, err := e.f.terminalFor(e.id, tid)
		return err != nil
	})
}

func TestTerminalExitIsReportedBeforeTheTerminalIsDropped(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	ch, cancel := e.f.TerminalLog().Subscribe(terminalKeyPrefix + tid)
	defer cancel()
	proc, _ := e.fs.last()
	proc.exit()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if strings.Contains(string(ev.Data), `"exit":`) {
				return
			}
		case <-deadline:
			t.Fatal("no exit event")
		}
	}
}

func TestTerminalFirstInputHoldsAndReleaseUnholds(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	if e.holds(true) != 0 {
		t.Fatal("opening alone must not hold")
	}
	if rec := e.input(t, tid, "ls\n"); rec.Code != http.StatusNoContent {
		t.Fatalf("input = %d %s", rec.Code, rec.Body)
	}
	e.input(t, tid, "pwd\n")
	if e.holds(true) != 1 {
		t.Fatalf("hold on count = %d, want 1", e.holds(true))
	}
	if d := e.holdDeltas(); len(d) != 1 || !strings.Contains(d[0], `"held":true`) || !strings.Contains(d[0], `"by":"terminal"`) {
		t.Fatalf("deltas = %v", d)
	}
	proc, _ := e.fs.last()
	if got := proc.written(); got != "ls\npwd\n" {
		t.Fatalf("stdin = %q", got)
	}
	if rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/release", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("release = %d", rec.Code)
	}
	if e.holds(false) != 1 {
		t.Fatalf("hold off count = %d, want 1", e.holds(false))
	}
	if d := e.holdDeltas(); len(d) != 2 || !strings.Contains(d[1], `"held":false`) {
		t.Fatalf("deltas = %v", d)
	}
	// Typing again after a hand back holds again.
	e.input(t, tid, "x")
	if e.holds(true) != 2 {
		t.Fatalf("hold on count = %d, want 2", e.holds(true))
	}
}

func TestTerminalIdleTimerReleasesAfterTwoMinutes(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	e.input(t, tid, "a")
	e.now = e.now.Add(terminalIdleRelease - time.Second)
	e.f.releaseIdleTerminals(e.f.now())
	if e.holds(false) != 0 {
		t.Fatal("released before the idle window")
	}
	e.now = e.now.Add(2 * time.Second)
	e.f.releaseIdleTerminals(e.f.now())
	if e.holds(false) != 1 {
		t.Fatalf("hold off count = %d, want 1", e.holds(false))
	}
	// Already released: another tick does nothing.
	e.f.releaseIdleTerminals(e.f.now())
	if e.holds(false) != 1 {
		t.Fatalf("released twice")
	}
}

func TestTerminalLimitIsTwoPerAgent(t *testing.T) {
	e := newTermEnv(t, false)
	e.open(t)
	e.open(t)
	rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal", `{}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third terminal = %d %s", rec.Code, rec.Body)
	}
}

func TestTerminalRecordingHoldsInputAndOutputAndMode(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	e.input(t, tid, "echo hi\n")
	proc, _ := e.fs.last()
	proc.emit("hi\n")
	waitFor(t, 5*time.Second, "output recorded", func() bool {
		tm, err := e.f.terminalFor(e.id, tid)
		return err == nil && tm.bytesOutLoad() == 3
	})
	if rec := e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("close = %d", rec.Code)
	}
	logs, _ := filepath.Glob(filepath.Join(e.f.stateDir, "terminal", e.id, "*.log"))
	if len(logs) != 1 {
		t.Fatalf("logs = %v", logs)
	}
	data, _ := os.ReadFile(logs[0])
	if !strings.Contains(string(data), "echo hi\n") || !strings.Contains(string(data), "hi\n") {
		t.Fatalf("recording = %q", data)
	}
	if fi, _ := os.Stat(logs[0]); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	if !proc.wasKilled() {
		t.Fatal("process not killed on close")
	}
}

func TestTerminalCloseAndOpenAreAudited(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	e.input(t, tid, "abc")
	proc, _ := e.fs.last()
	proc.emit("12345")
	waitFor(t, 5*time.Second, "output", func() bool {
		tm, err := e.f.terminalFor(e.id, tid)
		return err == nil && tm.bytesOutLoad() == 5
	})
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	evs, err := e.f.audit.Tail(50)
	if err != nil {
		t.Fatal(err)
	}
	var opened, closed *AuditEvent
	for i := range evs {
		switch evs[i].Event {
		case AuditTerminalOpened:
			opened = &evs[i]
		case AuditTerminalClosed:
			closed = &evs[i]
		}
	}
	if opened == nil || opened.AgentID != e.id {
		t.Fatalf("no terminal_opened: %+v", evs)
	}
	if closed == nil || closed.Detail != "in=3 out=5" {
		t.Fatalf("terminal_closed = %+v", closed)
	}
}

func TestTerminalCloseReleasesAHeldAgent(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	e.input(t, tid, "a")
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	if e.holds(false) != 1 {
		t.Fatalf("hold off count = %d, want 1", e.holds(false))
	}
}

func TestTerminalInputValidation(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	if rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/input", `{"data":"!!!"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad base64 = %d", rec.Code)
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, maxTerminalInput+1))
	if rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/input", `{"data":"`+big+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize = %d", rec.Code)
	}
	if rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/nope/input", `{"data":""}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown terminal = %d", rec.Code)
	}
	if e.holds(true) != 0 {
		t.Fatal("a rejected input must not hold the agent")
	}
}

func TestTerminalResizeWritesStty(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/resize", `{"cols":120,"rows":40}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("resize = %d %s", rec.Code, rec.Body)
	}
	proc, _ := e.fs.last()
	if got := proc.written(); got != "stty rows 40 cols 120\n" {
		t.Fatalf("stdin = %q", got)
	}
	if rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/resize", `{"cols":0,"rows":40}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad resize = %d", rec.Code)
	}
}

func TestTerminalUnknownAgentIs404(t *testing.T) {
	e := newTermEnv(t, false)
	if rec := e.post(t, "POST", "/api/agents/nope/terminal", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", rec.Code)
	}
}

func TestTerminalScriptMissingIsReported(t *testing.T) {
	e := newTermEnv(t, true)
	tid := e.open(t)
	ch, cancel := e.f.TerminalLog().Subscribe(terminalKeyPrefix + tid)
	defer cancel()
	proc, _ := e.fs.last()
	proc.emit(`OCI runtime exec failed: exec: "script": executable file not found in $PATH` + "\n")
	proc.exit()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if strings.Contains(string(ev.Data), `"exit"`) {
				if !strings.Contains(string(ev.Data), "script not found in image") {
					t.Fatalf("exit event = %s", ev.Data)
				}
				return
			}
		case <-deadline:
			t.Fatal("no exit event")
		}
	}
}

func TestTestShellRunsTheBuiltImageWithMountsAndEnv(t *testing.T) {
	e := newWSSpawnEnv(t)
	doc := sampleDoc("svc")
	doc.Mounts = []WSMount{{Volume: "gocache", Target: "/go/pkg"}}
	e.builtTemplate(t, "svc", doc)
	fs := &fakeStreamer{}
	e.f.streamer = fs.start
	srv := NewServer(e.f, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/workspaces/svc/shell", strings.NewReader(`{"cols":90,"rows":20}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("shell = %d %s", rec.Code, rec.Body)
	}
	_, call := fs.last()
	got := strings.Join(call.args, " ")
	for _, want := range []string{
		"run --rm -i --name marshal-shell-",
		"--mount type=volume,source=gocache,target=/go/pkg",
		"-e MARSHAL_WORKSPACE=svc",
		"-e TERM=xterm-256color",
		"marshal-derived-svc script -qfc stty rows 20 cols 90; exec sh /dev/null",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("run args lack %q:\n%s", want, got)
		}
	}
	if call.name != "docker" || call.dir != "" {
		t.Errorf("call = %+v", call)
	}
	// A test shell must not receive the agent provider keys.
	if strings.Contains(got, "API_KEY") {
		t.Errorf("provider key leaked into the shell: %s", got)
	}
}

func TestTestShellClosingKillsTheProcessAndRemovesTheContainer(t *testing.T) {
	e := newWSSpawnEnv(t)
	e.builtTemplate(t, "svc", sampleDoc("svc"))
	fs := &fakeStreamer{}
	e.f.streamer = fs.start
	srv := NewServer(e.f, "")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/workspaces/svc/shell", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("shell = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		TerminalID string `json:"terminalId"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	proc, _ := fs.last()

	// Input reaches the shell and never touches an agent hold.
	body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte("ls\n"))})
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/workspaces/svc/shell/"+out.TerminalID+"/input", strings.NewReader(string(body))))
	if rec.Code != http.StatusNoContent || proc.written() != "ls\n" {
		t.Fatalf("input = %d, stdin %q", rec.Code, proc.written())
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/workspaces/svc/shell/"+out.TerminalID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("close = %d %s", rec.Code, rec.Body)
	}
	if !proc.wasKilled() {
		t.Fatal("process not killed")
	}
	var removed bool
	e.imgs.mu.Lock()
	for _, c := range e.imgs.cmds {
		if len(c) >= 4 && c[1] == "rm" && c[2] == "-f" && strings.HasPrefix(c[3], shellContainerPrefix) {
			removed = true
		}
	}
	e.imgs.mu.Unlock()
	if !removed {
		t.Fatal("container was not removed")
	}
}

func TestTestShellRefusesAnUnbuiltTemplate(t *testing.T) {
	e := newWSSpawnEnv(t)
	publishDoc(t, e.f, "raw", sampleDoc("raw"))
	e.f.streamer = (&fakeStreamer{}).start
	srv := NewServer(e.f, "")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/workspaces/raw/shell", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workspace_not_built") {
		t.Fatalf("unbuilt = %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/workspaces/missing/shell", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d %s", rec.Code, rec.Body)
	}
}

func TestTestShellIsLimitedToTwoPerWorkspace(t *testing.T) {
	e := newWSSpawnEnv(t)
	e.builtTemplate(t, "svc", sampleDoc("svc"))
	e.f.streamer = (&fakeStreamer{}).start
	srv := NewServer(e.f, "")
	for i, want := range []int{200, 200, 429} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/workspaces/svc/shell", nil))
		if rec.Code != want {
			t.Fatalf("shell %d = %d %s, want %d", i, rec.Code, rec.Body, want)
		}
	}
}
