package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// settled waits for background hold calls to finish.
func (e *termEnv) settled(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	st := e.f.terminals()
	st.mu.Lock()
	var ts []*terminal
	for _, l := range st.byID {
		ts = append(ts, l...)
	}
	st.mu.Unlock()
	for _, tm := range ts {
		tm.holdMu.Lock()
		tm.holdMu.Unlock() //nolint:staticcheck // waiting for an in-flight call
	}
}

// waitHolds waits until the agent has seen n hold calls with that state.
func (e *termEnv) waitHolds(t *testing.T, on bool, n int) {
	t.Helper()
	waitFor(t, 5*time.Second, "hold calls", func() bool { return e.holds(on) == n })
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
	e.waitHolds(t, true, 1)
	e.settled(t)
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
	e.waitHolds(t, false, 1)
	if d := e.holdDeltas(); len(d) != 2 || !strings.Contains(d[1], `"held":false`) {
		t.Fatalf("deltas = %v", d)
	}
	// Typing again after a hand back holds again.
	e.input(t, tid, "x")
	e.waitHolds(t, true, 2)
}

func TestTerminalIdleTimerReleasesAfterTwoMinutes(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	e.input(t, tid, "a")
	e.waitHolds(t, true, 1)
	e.now = e.now.Add(terminalIdleRelease - time.Second)
	e.f.releaseIdleTerminals(e.f.now())
	if e.holds(false) != 0 {
		t.Fatal("released before the idle window")
	}
	e.now = e.now.Add(2 * time.Second)
	e.f.releaseIdleTerminals(e.f.now())
	e.waitHolds(t, false, 1)
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
	e.waitHolds(t, true, 1)
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	e.waitHolds(t, false, 1)
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

func TestTerminalResizeSetsThePTYWithoutTypingIntoTheShell(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	_, open := e.fs.last()
	if !strings.Contains(strings.Join(open.args, " "), "tty > /tmp/.marshal-tty-"+tid) {
		t.Fatalf("wrapper does not record its tty: %v", open.args)
	}
	rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/resize", `{"cols":120,"rows":40}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("resize = %d %s", rec.Code, rec.Body)
	}
	proc, _ := e.fs.last()
	if got := proc.written(); got != "" {
		t.Fatalf("resize typed into the shell: %q", got)
	}
	if len(e.fs.shorts) != 1 {
		t.Fatalf("shorts = %+v", e.fs.shorts)
	}
	c := e.fs.shorts[0]
	want := `stty -F "$(cat /tmp/.marshal-tty-` + tid + `)" rows 40 cols 120`
	if c.name != "sh" || c.dir != e.root || c.args[0] != "-c" || c.args[1] != want {
		t.Fatalf("resize call = %+v, want sh -c %s in %s", c, want, e.root)
	}
	if rec := e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/resize", `{"cols":0,"rows":40}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad resize = %d", rec.Code)
	}
}

func TestTerminalResizeInContainerModeExecsStty(t *testing.T) {
	e := newTermEnv(t, true)
	tid := e.open(t)
	e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/resize", `{"cols":80,"rows":24}`)
	if len(e.fs.shorts) != 1 {
		t.Fatalf("shorts = %+v", e.fs.shorts)
	}
	c := e.fs.shorts[0]
	got := strings.Join(c.args, " ")
	if c.name != "docker" || !strings.HasPrefix(got, "exec "+containerNameFor(e.id)+" sh -c stty -F") {
		t.Fatalf("resize call = %s %s", c.name, got)
	}
}

func TestTerminalUnsupportedHoldIsTriedOnceAndNeverBlocksInput(t *testing.T) {
	e := newTermEnv(t, false)
	e.tr.errs = map[string]*rpcError{"session/hold": {Code: -32601, Message: "method not found"}}
	tid := e.open(t)
	for i := 0; i < 5; i++ {
		if rec := e.input(t, tid, "k"); rec.Code != http.StatusNoContent {
			t.Fatalf("input %d = %d", i, rec.Code)
		}
		e.settled(t)
	}
	if n := len(e.tr.paramsOf("session/hold")); n != 1 {
		t.Fatalf("hold was tried %d times, want 1", n)
	}
	proc, _ := e.fs.last()
	if proc.written() != "kkkkk" {
		t.Fatalf("stdin = %q", proc.written())
	}
	if len(e.holdDeltas()) != 0 {
		t.Fatal("a hold that never happened was broadcast")
	}
}

func TestTerminalFailedHoldBacksOffThenRetries(t *testing.T) {
	e := newTermEnv(t, false)
	calls := 0
	var mu sync.Mutex
	e.f.holdCall = func(_ context.Context, _ string, on bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errors.New("agent hiccup")
		}
		return nil
	}
	tid := e.open(t)
	e.input(t, tid, "a")
	e.settled(t)
	e.input(t, tid, "b") // inside the backoff: no new call
	e.settled(t)
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("calls inside the backoff = %d, want 1", n)
	}
	e.now = e.now.Add(terminalHoldBackoff + time.Second)
	e.input(t, tid, "c")
	waitFor(t, 5*time.Second, "retry", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls == 2
	})
}

func TestTerminalHungHoldDoesNotDelayTheKeystroke(t *testing.T) {
	e := newTermEnv(t, false)
	release := make(chan struct{})
	e.f.holdCall = func(ctx context.Context, _ string, on bool) error {
		<-release
		return nil
	}
	tid := e.open(t)
	done := make(chan struct{})
	go func() {
		e.input(t, tid, "x")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("input waited on the hold call")
	}
	proc, _ := e.fs.last()
	if proc.written() != "x" {
		t.Fatalf("stdin = %q", proc.written())
	}
	close(release)
	e.settled(t)
}

func TestTerminalHoldCallsAreOrdered(t *testing.T) {
	e := newTermEnv(t, false)
	var mu sync.Mutex
	var seq []bool
	firstIn := make(chan struct{})
	release := make(chan struct{})
	e.f.holdCall = func(_ context.Context, _ string, on bool) error {
		mu.Lock()
		first := len(seq) == 0
		seq = append(seq, on)
		mu.Unlock()
		if first {
			close(firstIn)
			<-release
		}
		return nil
	}
	tid := e.open(t)
	e.input(t, tid, "x")
	<-firstIn // hold(true) is in flight
	// A hand back arrives while it is: it must run after, not alongside.
	released := make(chan struct{})
	go func() {
		_ = e.f.ReleaseTerminal(e.id, tid)
		close(released)
	}()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(seq) != 1 {
		mu.Unlock()
		t.Fatalf("release overtook the hold: %v", seq)
	}
	mu.Unlock()
	close(release)
	<-released
	mu.Lock()
	defer mu.Unlock()
	if len(seq) != 2 || seq[0] != true || seq[1] != false {
		t.Fatalf("hold calls = %v, want [true false]", seq)
	}
	tm, _ := e.f.terminalFor(e.id, tid)
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.held || tm.want {
		t.Fatalf("terminal still holds: held=%v want=%v", tm.held, tm.want)
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
		"marshal-derived-svc script -qfc tty > /tmp/.marshal-tty-",
		"; stty rows 20 cols 90; exec sh /dev/null",
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

func TestTerminalFinalReleaseIsRetriedWhenTheAgentIsUnreachable(t *testing.T) {
	e := newTermEnv(t, false)
	e.f.releaseRetry = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	var mu sync.Mutex
	offCalls := 0
	e.f.holdCall = func(_ context.Context, _ string, on bool) error {
		if on {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		offCalls++
		if offCalls <= 2 {
			return errors.New("unreachable")
		}
		return nil
	}
	tid := e.open(t)
	e.input(t, tid, "a")
	e.settled(t)
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	waitFor(t, 5*time.Second, "release retried to success", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return offCalls == 3
	})
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if offCalls != 3 {
		t.Fatalf("off calls = %d, want to stop after the first success", offCalls)
	}
}

func TestTerminalFinalReleaseRetryYieldsToANewTerminal(t *testing.T) {
	e := newTermEnv(t, false)
	e.f.releaseRetry = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}
	var mu sync.Mutex
	offCalls := 0
	e.f.holdCall = func(_ context.Context, _ string, on bool) error {
		if on {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		offCalls++
		return errors.New("unreachable")
	}
	tid := e.open(t)
	e.input(t, tid, "a")
	e.settled(t)
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	e.open(t) // a new terminal now owns the hold
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if offCalls != 1 {
		t.Fatalf("off calls = %d, want only the first attempt", offCalls)
	}
}

func TestTerminalCloseRemovesTheTTYFile(t *testing.T) {
	e := newTermEnv(t, false)
	tid := e.open(t)
	f := ttyFile(tid)
	if err := os.WriteFile(f, []byte("/dev/pts/9\n"), 0o600); err != nil {
		t.Skipf("cannot write %s: %v", f, err)
	}
	t.Cleanup(func() { _ = os.Remove(f) })
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	waitFor(t, 5*time.Second, "tty file removed", func() bool {
		_, err := os.Stat(f)
		return os.IsNotExist(err)
	})
}

func TestTerminalCloseRemovesTheTTYFileInTheContainer(t *testing.T) {
	e := newTermEnv(t, true)
	tid := e.open(t)
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	waitFor(t, 5*time.Second, "rm exec", func() bool {
		e.fs.mu.Lock()
		defer e.fs.mu.Unlock()
		for _, c := range e.fs.shorts {
			if strings.Join(c.args, " ") == "exec "+containerNameFor(e.id)+" rm -f "+ttyFile(tid) {
				return true
			}
		}
		return false
	})
}

func TestTerminalRetryHandsTheHoldToANewTerminal(t *testing.T) {
	e := newTermEnv(t, false)
	e.f.releaseRetry = []time.Duration{50 * time.Millisecond}
	var mu sync.Mutex
	failOff := true
	offs := 0
	e.f.holdCall = func(_ context.Context, _ string, on bool) error {
		mu.Lock()
		defer mu.Unlock()
		if !on {
			offs++
			if failOff {
				return errors.New("unreachable")
			}
		}
		return nil
	}
	tid := e.open(t)
	e.input(t, tid, "a")
	e.settled(t)
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	second := e.open(t)
	time.Sleep(300 * time.Millisecond) // the retry wakes and yields
	mu.Lock()
	failOff = false
	mu.Unlock()
	// Without any input, releasing the new terminal still hands the agent back.
	e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+second+"/release", "")
	waitFor(t, 5*time.Second, "hand-back by the new terminal", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return offs == 2
	})
}

func (e *termEnv) heldInSnapshot() bool {
	for _, a := range e.f.Snapshot() {
		if a.ID == e.id {
			return a.Held
		}
	}
	return false
}

func TestAgentListReportsHeld(t *testing.T) {
	e := newTermEnv(t, false)
	if e.heldInSnapshot() {
		t.Fatal("held before any terminal")
	}
	tid := e.open(t)
	e.input(t, tid, "a")
	e.waitHolds(t, true, 1)
	waitFor(t, 5*time.Second, "held in the agent list", e.heldInSnapshot)
	e.post(t, "POST", "/api/agents/"+e.id+"/terminal/"+tid+"/release", "")
	waitFor(t, 5*time.Second, "not held after hand-back", func() bool { return !e.heldInSnapshot() })
	rec := e.post(t, "GET", "/api/agents", "")
	if strings.Contains(rec.Body.String(), `"held"`) {
		t.Fatalf("held is omitted when false: %s", rec.Body)
	}
}

func TestAgentListStaysHeldUntilAFailedHandBackLands(t *testing.T) {
	e := newTermEnv(t, false)
	e.f.releaseRetry = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}
	var mu sync.Mutex
	failOff := true
	e.f.holdCall = func(_ context.Context, _ string, on bool) error {
		mu.Lock()
		defer mu.Unlock()
		if !on && failOff {
			return errors.New("unreachable")
		}
		return nil
	}
	tid := e.open(t)
	e.input(t, tid, "a")
	e.settled(t)
	e.post(t, "DELETE", "/api/agents/"+e.id+"/terminal/"+tid, "")
	waitFor(t, 5*time.Second, "terminal gone", func() bool { _, err := e.f.terminalFor(e.id, tid); return err != nil })
	if !e.heldInSnapshot() {
		t.Fatal("the agent is still held, but the list says otherwise")
	}
	mu.Lock()
	failOff = false
	mu.Unlock()
	waitFor(t, 5*time.Second, "cleared once handed back", func() bool { return !e.heldInSnapshot() })
}
