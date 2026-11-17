package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// acpTestTimeout bounds every scripted exchange, so a fake that stops
// answering fails the test instead of hanging the suite.
const acpTestTimeout = 5 * time.Second

func acpContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), acpTestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// acpReply is what a scripted agent answers one request with. hold leaves the
// request unanswered so the test can answer it later, which is how a held turn
// is observed mid-flight.
type acpReply struct {
	hold   bool
	result any
	err    *rpcError
}

// acpFrame is one request or notification the scripted agent received.
type acpFrame struct {
	method string
	params string
}

// acpAnswer is one response the scripted agent received for a request it
// made itself.
type acpAnswer struct {
	id     string
	result string
	err    string
}

// scriptedAgent is an in-process ACP v1 agent: an agentTransport over io.Pipes
// speaking scripted newline JSON-RPC. No subprocess, no network, no timing
// assumptions — the test drives every answer explicitly.
//
// Behaviour is configured before Start and read under the same mutex the
// serve goroutines use, so a test may retune it between turns.
type scriptedAgent struct {
	mu sync.Mutex

	// Configuration.
	loadSession bool
	promptCaps  bool
	modes       []string
	currentMode string
	// setMode answers session/set_mode; nil echoes the requested mode.
	setMode func(requested string) *acpReply
	// sessionIDs are handed out in order by session/new; empty means s-1, s-2…
	sessionIDs []string
	// onPrompt answers session/prompt; nil answers end_turn at once.
	onPrompt func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply

	// Recorded state, per agent so two fakes in one test never mix.
	gens     []*scriptedGen
	cur      *scriptedGen
	sessions int
	frames   []acpFrame
	answers  []acpAnswer
	opens    int
	signals  int
}

func newScriptedAgent() *scriptedAgent {
	return &scriptedAgent{loadSession: true, promptCaps: true}
}

func (a *scriptedAgent) setPromptHook(hook func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply) {
	a.mu.Lock()
	a.onPrompt = hook
	a.mu.Unlock()
}

func (a *scriptedAgent) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	g := &scriptedGen{agent: a, exit: make(chan error)}
	g.stdinR, g.stdinW = io.Pipe()
	g.stdoutR, g.stdoutW = io.Pipe()

	a.mu.Lock()
	a.gens = append(a.gens, g)
	a.cur = g
	a.opens++
	a.mu.Unlock()

	go g.serve()
	return g.stdinW, g.stdoutR, io.NopCloser(strings.NewReader("")), nil
}

// Wait returns when the generation the child is waiting on has died.
func (a *scriptedAgent) Wait() error {
	a.mu.Lock()
	g := a.cur
	a.mu.Unlock()
	if g == nil {
		return nil
	}
	<-g.exit
	return nil
}

func (a *scriptedAgent) Signal(sig os.Signal) error {
	a.mu.Lock()
	a.signals++
	g := a.cur
	a.mu.Unlock()
	if g != nil {
		g.die()
	}
	return nil
}

func (a *scriptedAgent) Kill() error {
	a.hangUp()
	return nil
}

func (a *scriptedAgent) Detach() error { return nil }

// hangUp kills the current generation, as a crashing adapter would: both pipes
// close and nothing answers again.
func (a *scriptedAgent) hangUp() {
	a.mu.Lock()
	g := a.cur
	a.mu.Unlock()
	if g != nil {
		g.die()
	}
}

func (a *scriptedAgent) current() *scriptedGen {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cur
}

func (a *scriptedAgent) framesFor(method string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, f := range a.frames {
		if f.method == method {
			out = append(out, f.params)
		}
	}
	return out
}

func (a *scriptedAgent) countFrames(method string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, f := range a.frames {
		if f.method == method {
			n++
		}
	}
	return n
}

func (a *scriptedAgent) methodsSeen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.frames))
	for _, f := range a.frames {
		out = append(out, f.method)
	}
	return out
}

func (a *scriptedAgent) answerFor(id string) (acpAnswer, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, answer := range a.answers {
		if answer.id == id {
			return answer, true
		}
	}
	return acpAnswer{}, false
}

func (a *scriptedAgent) generationCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.gens)
}

// reply is the scripted agent's answer to one request. It always returns
// something: a method the script does not model is answered with an empty
// result, so a test can never hang on a hole in the script.
func (a *scriptedAgent) reply(g *scriptedGen, id json.RawMessage, method string, params json.RawMessage) *acpReply {
	a.mu.Lock()
	loadSession, promptCaps := a.loadSession, a.promptCaps
	modes := append([]string(nil), a.modes...)
	currentMode := a.currentMode
	sessionIDs := append([]string(nil), a.sessionIDs...)
	setMode, onPrompt := a.setMode, a.onPrompt
	a.mu.Unlock()

	if method == "session/prompt" && onPrompt != nil {
		return onPrompt(g, id, params)
	}

	switch method {
	case "initialize":
		agentCaps := map[string]any{"loadSession": loadSession}
		if promptCaps {
			agentCaps["promptCapabilities"] = map[string]any{
				"image": false, "audio": false, "embeddedContext": true,
			}
		}
		return &acpReply{result: map[string]any{
			"protocolVersion":   1,
			"agentInfo":         map[string]any{"name": "claude-code-acp", "version": "test"},
			"agentCapabilities": agentCaps,
		}}
	case "session/new":
		a.mu.Lock()
		a.sessions++
		n := a.sessions
		a.mu.Unlock()
		sessionID := fmt.Sprintf("s-%d", n)
		if n <= len(sessionIDs) {
			sessionID = sessionIDs[n-1]
		}
		result := map[string]any{"sessionId": sessionID}
		if len(modes) > 0 {
			available := make([]map[string]any, 0, len(modes))
			for _, mode := range modes {
				available = append(available, map[string]any{"id": mode, "name": mode})
			}
			result["modes"] = map[string]any{
				"currentModeId":  currentMode,
				"availableModes": available,
			}
		}
		return &acpReply{result: result}
	case "session/set_mode":
		var p struct {
			ModeID string `json:"modeId"`
		}
		_ = json.Unmarshal(params, &p)
		if setMode != nil {
			return setMode(p.ModeID)
		}
		return &acpReply{result: map[string]any{"modeId": p.ModeID}}
	case "session/prompt":
		return &acpReply{result: map[string]any{"stopReason": "end_turn"}}
	}
	return &acpReply{result: map[string]any{}}
}

// scriptedGen is one generation of the scripted agent: its pipes, its recorded
// death and the frames it writes back.
type scriptedGen struct {
	agent   *scriptedAgent
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	exit    chan error

	dieOnce sync.Once
	writeMu sync.Mutex
}

func (g *scriptedGen) serve() {
	defer g.stdinR.Close()
	defer g.stdoutW.Close()

	sc := bufio.NewScanner(g.stdinR)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		if frame.Method == "" {
			if len(frame.ID) > 0 {
				g.agent.mu.Lock()
				g.agent.answers = append(g.agent.answers, acpAnswer{
					id:     compactID(frame.ID),
					result: string(frame.Result),
					err:    string(frame.Error),
				})
				g.agent.mu.Unlock()
			}
			continue
		}
		g.agent.mu.Lock()
		g.agent.frames = append(g.agent.frames, acpFrame{method: frame.Method, params: string(frame.Params)})
		g.agent.mu.Unlock()

		if len(frame.ID) == 0 {
			continue // the bridge's notification
		}
		reply := g.agent.reply(g, frame.ID, frame.Method, frame.Params)
		if reply == nil || reply.hold {
			continue
		}
		if reply.err != nil {
			g.write(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": reply.err})
			continue
		}
		g.write(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": reply.result})
	}
}

func (g *scriptedGen) write(v any) {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	_ = json.NewEncoder(g.stdoutW).Encode(v)
}

// respond answers a request the script held.
func (g *scriptedGen) respond(id json.RawMessage, result any) {
	g.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// notify pushes one notification at the bridge.
func (g *scriptedGen) notify(method string, params any) {
	g.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// request makes the agent ask the bridge something, as an adapter asking for
// permission does.
func (g *scriptedGen) request(id any, method string, params any) {
	g.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (g *scriptedGen) die() {
	g.dieOnce.Do(func() {
		_ = g.stdinR.Close()
		_ = g.stdoutW.Close()
		close(g.exit)
	})
}

// eventsMatching returns the recorded events of one kind for one session.
func eventsMatching(events []workerEvent, sessionID, kind string) []workerEvent {
	var out []workerEvent
	for _, ev := range events {
		if ev.sessionID == sessionID && ev.kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// eventsForSession returns every event recorded for one session.
func eventsForSession(events []workerEvent, sessionID string) []workerEvent {
	var out []workerEvent
	for _, ev := range events {
		if ev.sessionID == sessionID {
			out = append(out, ev)
		}
	}
	return out
}

// assertSessionSequence pins that a session's events start at 1 and strictly
// increase, which is the ordering contract the planner journals against.
func assertSessionSequence(t *testing.T, events []workerEvent, sessionID string) {
	t.Helper()
	own := eventsForSession(events, sessionID)
	if len(own) == 0 {
		t.Fatalf("session %q recorded no events", sessionID)
	}
	var last uint64
	for _, ev := range own {
		if ev.sequence <= last {
			t.Errorf("session %q event %q has sequence %d after %d, want strictly increasing", sessionID, ev.kind, ev.sequence, last)
		}
		last = ev.sequence
		if !json.Valid(ev.data) {
			t.Errorf("session %q event %q data = %s, want valid JSON", sessionID, ev.kind, ev.data)
		}
	}
	if own[0].sequence != 1 {
		t.Errorf("session %q first event sequence = %d, want 1", sessionID, own[0].sequence)
	}
}

type promptOutcome struct {
	result WorkerTurnResult
	err    error
}

// promptAsync dispatches a turn without blocking the test, so the fake can be
// driven while the turn is in flight.
func promptAsync(ctx context.Context, backend WorkerBackend, sessionID, text string) <-chan promptOutcome {
	out := make(chan promptOutcome, 1)
	go func() {
		result, err := backend.Prompt(ctx, sessionID, text)
		out <- promptOutcome{result: result, err: err}
	}()
	return out
}

// TestACPWorkerHandshakeAndMode pins the handshake the adapter must see, the
// capabilities it reported, the negotiation of the access mode and the order
// the frames go out in: the mode is selected before any prompt is dispatched.
func TestACPWorkerHandshakeAndMode(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.modes = []string{"ask", "act"}
	agent.currentMode = "ask"
	agent.sessionIDs = []string{"sess-1"}

	var rec eventRecorder
	backend := newACPWorkerBackend(agent, rec.sink, nil)

	dir := t.TempDir()
	sessionID, caps, err := backend.Start(ctx, WorkerSessionSpec{
		WorkerID: "w1", WorkspaceID: "ws1", ProjectRoot: dir, Mode: "act",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if sessionID != "sess-1" {
		t.Fatalf("session id = %q, want the adapter's own sess-1", sessionID)
	}
	if !caps.Prompt || !caps.Cancel || !caps.SessionLoad || !caps.PermissionRequests {
		t.Errorf("capabilities = %+v, want prompt, cancel, session load and permission requests", caps)
	}
	if caps.WorkspaceEdits {
		t.Errorf("capabilities = %+v, want no workspace edits: the adapter never advertised them", caps)
	}
	if got := strings.Join(caps.Modes, ","); got != "ask,act" {
		t.Errorf("capability modes = %q, want the session's own ask,act", got)
	}

	init := agent.framesFor("initialize")
	if len(init) != 1 {
		t.Fatalf("initialize frames = %d, want 1", len(init))
	}
	for _, want := range []string{
		`"protocolVersion":1`,
		`"clientCapabilities":{}`,
		`"clientInfo":{"name":"marshal-bridge","version":"1"}`,
	} {
		if !strings.Contains(init[0], want) {
			t.Errorf("initialize params = %s, want it to carry %s", init[0], want)
		}
	}

	created := agent.framesFor("session/new")
	if len(created) != 1 {
		t.Fatalf("session/new frames = %d, want 1", len(created))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs %q: %v", dir, err)
	}
	if !strings.Contains(created[0], `"cwd":"`+abs+`"`) {
		t.Errorf("session/new params = %s, want the absolute cwd %q", created[0], abs)
	}
	if !strings.Contains(created[0], `"mcpServers":[]`) {
		t.Errorf("session/new params = %s, want an explicit empty mcpServers", created[0])
	}

	modes := agent.framesFor("session/set_mode")
	if len(modes) != 1 {
		t.Fatalf("session/set_mode frames = %d, want 1", len(modes))
	}
	if !strings.Contains(modes[0], `"modeId":"act"`) || !strings.Contains(modes[0], `"sessionId":"sess-1"`) {
		t.Errorf("session/set_mode params = %s, want the requested act mode for sess-1", modes[0])
	}
	if got := strings.Join(agent.methodsSeen(), ","); got != "initialize,session/new,session/set_mode" {
		t.Fatalf("frames before the first prompt = %q, want the mode selected before any prompt", got)
	}

	result, err := backend.Prompt(ctx, sessionID, "fix the build")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("stop reason = %q, want end_turn", result.StopReason)
	}
	prompts := agent.framesFor("session/prompt")
	if len(prompts) != 1 {
		t.Fatalf("session/prompt frames = %d, want 1", len(prompts))
	}
	for _, want := range []string{`"sessionId":"sess-1"`, `"text":"fix the build"`, `"type":"text"`} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("session/prompt params = %s, want it to carry %s", prompts[0], want)
		}
	}

	events := rec.all()
	own := eventsForSession(events, sessionID)
	if len(own) == 0 || own[0].kind != workerEventSessionStarted {
		t.Fatalf("first event for %q = %+v, want session.started", sessionID, own)
	}
	assertSessionSequence(t, events, sessionID)
	modeEvents := eventsMatching(events, sessionID, workerEventSessionMode)
	if len(modeEvents) == 0 || !strings.Contains(string(modeEvents[0].data), `"modeId":"act"`) {
		t.Errorf("session.mode events = %+v, want the negotiated act mode", modeEvents)
	}

	// An adapter that answers set_mode with a different mode has refused the
	// requested access level: Start must not hand back a usable session.
	refusing := newScriptedAgent()
	refusing.modes = []string{"ask", "act"}
	refusing.currentMode = "ask"
	refusing.setMode = func(string) *acpReply {
		return &acpReply{result: map[string]any{"modeId": "ask"}}
	}
	second := newACPWorkerBackend(refusing, nil, nil)
	if _, _, err := second.Start(ctx, WorkerSessionSpec{WorkerID: "w2", ProjectRoot: t.TempDir(), Mode: "act"}); err == nil {
		t.Fatal("start succeeded although set_mode reported another mode, want an error")
	} else if !strings.Contains(err.Error(), "act") || !strings.Contains(err.Error(), "ask") {
		t.Errorf("start error = %v, want it to name the requested and reported modes", err)
	}
	if n := refusing.countFrames("session/prompt"); n != 0 {
		t.Errorf("session/prompt frames = %d after a refused mode, want none", n)
	}
}

// TestACPWorkerModeUnavailable pins that a mode the session does not offer is
// refused by name, with the available modes, before anything is dispatched.
func TestACPWorkerModeUnavailable(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.modes = []string{"ask"}
	agent.currentMode = "ask"
	agent.sessionIDs = []string{"sess-1"}

	var rec eventRecorder
	backend := newACPWorkerBackend(agent, rec.sink, nil)

	_, _, err := backend.Start(ctx, WorkerSessionSpec{
		WorkerID: "w1", ProjectRoot: t.TempDir(), Mode: "act",
	})
	if err == nil {
		t.Fatal("start with an unavailable mode succeeded, want an error")
	}
	if !strings.Contains(err.Error(), `"act"`) {
		t.Errorf("start error = %v, want it to name the requested mode", err)
	}
	if !strings.Contains(err.Error(), "ask") {
		t.Errorf("start error = %v, want it to name the available modes", err)
	}
	if n := agent.countFrames("session/set_mode"); n != 0 {
		t.Errorf("session/set_mode frames = %d, want none", n)
	}
	if n := agent.countFrames("session/prompt"); n != 0 {
		t.Errorf("session/prompt frames = %d, want none", n)
	}

	// The refused session is not one this backend holds: nothing to cancel,
	// and nothing to prompt either.
	if err := backend.Cancel(ctx, "sess-1"); err != nil {
		t.Errorf("cancel of an unregistered session = %v, want nil", err)
	}
	if _, err := backend.Prompt(ctx, "sess-1", "hello"); err == nil {
		t.Error("prompt of an unregistered session succeeded, want an error")
	}
	if n := agent.countFrames("session/prompt"); n != 0 {
		t.Errorf("session/prompt frames = %d after prompting an unregistered session, want none", n)
	}
}

// TestACPWorkerPermissionAndQuestion pins that a permission request and an
// elicitation reach the request sink under the ids the adapter sent, that the
// sink's raw answer goes back to the adapter verbatim, and that both are
// journaled as events.
func TestACPWorkerPermissionAndQuestion(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.sessionIDs = []string{"sess-1"}

	promptIDs := make(chan json.RawMessage, 4)
	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		g.request(9000, "session/request_permission", map[string]any{
			"sessionId":  "sess-1",
			"toolCallId": "tc-1",
			"toolName":   "shell.run",
			"options":    []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		})
		promptIDs <- id
		return &acpReply{hold: true}
	})

	type sinkCall struct {
		sessionID string
		requestID string
		method    string
	}
	asked := make(chan sinkCall, 4)
	var rec eventRecorder
	backend := newACPWorkerBackend(agent, rec.sink, func(ctx context.Context, sessionID, requestID, method string, params json.RawMessage) (json.RawMessage, error) {
		asked <- sinkCall{sessionID: sessionID, requestID: requestID, method: method}
		if method == "session/request_question" {
			return json.RawMessage(`{"answers":[{"question":"proceed?","answer":"yes"}]}`), nil
		}
		return json.RawMessage(`{"optionId":"allow"}`), nil
	})

	sessionID, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	outcome := promptAsync(ctx, backend, sessionID, "run the tests")
	var permission sinkCall
	select {
	case permission = <-asked:
	case <-ctx.Done():
		t.Fatal("the permission request never reached the request sink")
	}
	want := sinkCall{sessionID: sessionID, requestID: "tc-1", method: "session/request_permission"}
	if permission != want {
		t.Fatalf("permission request = %+v, want %+v", permission, want)
	}
	waitFor(t, acpTestTimeout, "the adapter to receive the permission answer", func() bool {
		answer, ok := agent.answerFor("9000")
		return ok && strings.Contains(answer.result, `"optionId":"allow"`)
	})

	agent.current().respond(<-promptIDs, map[string]any{"stopReason": "end_turn"})
	if out := <-outcome; out.err != nil {
		t.Fatalf("prompt with a permission request: %v", out.err)
	}
	if events := eventsMatching(rec.all(), sessionID, workerEventTurnPermission); len(events) == 0 {
		t.Error("no turn.permission event was journaled for the permission request")
	} else if !strings.Contains(string(events[0].data), `"requestId":"tc-1"`) {
		t.Errorf("turn.permission data = %s, want it to carry the request id tc-1", events[0].data)
	}

	// The same for an elicitation, answered verbatim under its own string id.
	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		g.request("q-1", "session/request_question", map[string]any{
			"sessionId":  "sess-1",
			"questionId": "q-1",
			"questions":  []map[string]any{{"question": "proceed?"}},
		})
		promptIDs <- id
		return &acpReply{hold: true}
	})
	outcome = promptAsync(ctx, backend, sessionID, "carry on")
	var question sinkCall
	select {
	case question = <-asked:
	case <-ctx.Done():
		t.Fatal("the elicitation never reached the request sink")
	}
	wantQuestion := sinkCall{sessionID: sessionID, requestID: "q-1", method: "session/request_question"}
	if question != wantQuestion {
		t.Fatalf("question request = %+v, want %+v", question, wantQuestion)
	}
	waitFor(t, acpTestTimeout, "the adapter to receive the question answer", func() bool {
		answer, ok := agent.answerFor(`"q-1"`)
		return ok && strings.Contains(answer.result, `"answer":"yes"`)
	})
	agent.current().respond(<-promptIDs, map[string]any{"stopReason": "end_turn"})
	if out := <-outcome; out.err != nil {
		t.Fatalf("prompt with a question: %v", out.err)
	}
	if events := eventsMatching(rec.all(), sessionID, workerEventTurnQuestion); len(events) == 0 {
		t.Error("no turn.question event was journaled for the elicitation")
	} else if !strings.Contains(string(events[0].data), `"requestId":"q-1"`) {
		t.Errorf("turn.question data = %s, want it to carry the requested id q-1", events[0].data)
	}
}

// TestACPWorkerUnsupportedRequestIsAnswered pins that an adapter request this
// backend cannot answer is refused with JSON-RPC's method-not-found and
// journaled, rather than left hanging or dropped silently.
func TestACPWorkerUnsupportedRequestIsAnswered(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.sessionIDs = []string{"sess-1"}

	promptIDs := make(chan json.RawMessage, 1)
	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		g.request(7000, "fs/read_text_file", map[string]any{"sessionId": "sess-1", "path": "/etc/hosts"})
		promptIDs <- id
		return &acpReply{hold: true}
	})

	var rec eventRecorder
	backend := newACPWorkerBackend(agent, rec.sink, nil)
	sessionID, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	outcome := promptAsync(ctx, backend, sessionID, "read a file")
	promptID := <-promptIDs

	waitFor(t, acpTestTimeout, "the adapter to receive a method-not-found answer", func() bool {
		answer, ok := agent.answerFor("7000")
		return ok && strings.Contains(answer.err, `"code":-32601`)
	})

	events := eventsMatching(rec.all(), sessionID, workerEventUnsupported)
	if len(events) == 0 {
		t.Fatal("no unsupported.request event was journaled for fs/read_text_file")
	}
	if !strings.Contains(string(events[0].data), "fs/read_text_file") {
		t.Errorf("unsupported.request data = %s, want it to carry the method", events[0].data)
	}

	// The turn itself is unaffected: the adapter is still answered.
	agent.current().respond(promptID, map[string]any{"stopReason": "end_turn"})
	if out := <-outcome; out.err != nil {
		t.Fatalf("prompt after an unsupported request: %v", out.err)
	}
}

// TestACPWorkerPromptCancel pins that a cancel is the protocol's notification
// for the turn in flight, and that the turn's own stop reason is still what
// Prompt reports.
func TestACPWorkerPromptCancel(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.sessionIDs = []string{"sess-1"}

	promptIDs := make(chan json.RawMessage, 1)
	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		promptIDs <- id
		return &acpReply{hold: true}
	})

	var rec eventRecorder
	backend := newACPWorkerBackend(agent, rec.sink, nil)
	sessionID, caps, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !caps.Cancel {
		t.Fatalf("capabilities = %+v, want cancel", caps)
	}

	outcome := promptAsync(ctx, backend, sessionID, "a long turn")
	promptID := <-promptIDs

	if err := backend.Cancel(ctx, sessionID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// The notification is written synchronously; the adapter parses it on its
	// own read goroutine, so the frame is awaited rather than assumed parsed.
	waitFor(t, acpTestTimeout, "the cancel frame to reach the adapter", func() bool {
		return len(agent.framesFor("session/cancel")) == 1
	})
	cancels := agent.framesFor("session/cancel")
	if len(cancels) != 1 {
		t.Fatalf("session/cancel frames = %d, want 1", len(cancels))
	}
	if !strings.Contains(cancels[0], `"sessionId":"sess-1"`) {
		t.Errorf("session/cancel params = %s, want the session id", cancels[0])
	}

	agent.current().respond(promptID, map[string]any{"stopReason": "cancelled"})
	out := <-outcome
	if out.err != nil {
		t.Fatalf("prompt after cancel: %v", out.err)
	}
	if out.result.StopReason != "cancelled" {
		t.Errorf("stop reason = %q, want the adapter's cancelled", out.result.StopReason)
	}
	if events := eventsMatching(rec.all(), sessionID, workerEventTurnCancelled); len(events) != 1 {
		t.Errorf("turn.cancelled events = %d, want 1", len(events))
	}

	// A cancel for a session that was closed is harmless, not an error.
	if err := backend.Close(ctx, sessionID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := backend.Cancel(ctx, sessionID); err != nil {
		t.Errorf("cancel after close = %v, want nil", err)
	}
}

// TestACPWorkerTransportLossIsUncertain pins that a turn whose adapter died
// after the prompt was dispatched is uncertain — never a finished turn — and
// that the same session never dispatches again into the replacement process.
func TestACPWorkerTransportLossIsUncertain(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.sessionIDs = []string{"sess-1"}

	promptIDs := make(chan json.RawMessage, 2)
	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		promptIDs <- id
		return &acpReply{hold: true}
	})

	var rec eventRecorder
	started := newACPWorkerBackend(agent, rec.sink, nil)
	backend := started.(*acpWorkerBackend)
	sessionID, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	outcome := promptAsync(ctx, backend, sessionID, "change the tree")
	<-promptIDs

	// The turn is on the wire; now the adapter dies mid-turn.
	agent.hangUp()

	out := <-outcome
	if out.err == nil {
		t.Fatal("prompt over a dead adapter returned no error")
	}
	if !errors.Is(out.err, ErrWorkerUncertain) {
		t.Fatalf("prompt error = %v, want ErrWorkerUncertain", out.err)
	}
	if errors.Is(out.err, ErrWorkerUnsupported) {
		t.Errorf("an uncertain turn also reads as unsupported")
	}
	if out.result.StopReason != "" {
		t.Errorf("result = %+v, want no stop reason for an uncertain turn", out.result)
	}
	if n := agent.countFrames("session/prompt"); n != 1 {
		t.Fatalf("session/prompt frames = %d, want exactly 1 (no replay)", n)
	}
	if events := eventsMatching(rec.all(), sessionID, workerEventTurnUncertain); len(events) == 0 {
		t.Error("no turn.uncertain event was journaled")
	}

	// The child respawns; the session's conversation went with the process
	// that died, so a later turn is uncertain rather than silently replayed.
	waitFor(t, acpTestTimeout, "the adapter generation to be replaced", func() bool {
		return backend.currentGeneration() == 2
	})
	second := promptAsync(ctx, backend, sessionID, "try again")
	select {
	case out := <-second:
		if !errors.Is(out.err, ErrWorkerUncertain) {
			t.Fatalf("prompt into the replacement process = %v, want ErrWorkerUncertain", out.err)
		}
	case <-ctx.Done():
		t.Fatal("a prompt for a lost session blocked instead of failing")
	}
	if n := agent.countFrames("session/prompt"); n != 1 {
		t.Fatalf("session/prompt frames = %d, want the lost session never to reach the new process", n)
	}
}

// TestACPWorkerNoAutomaticTurnReplay pins that nothing reissues a turn: after
// the adapter is replaced, no second session/prompt and no automatic
// re-handshake appears on the wire.
func TestACPWorkerNoAutomaticTurnReplay(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.sessionIDs = []string{"sess-1"}

	promptIDs := make(chan json.RawMessage, 2)
	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		promptIDs <- id
		return &acpReply{hold: true}
	})

	started := newACPWorkerBackend(agent, nil, nil)
	backend := started.(*acpWorkerBackend)
	sessionID, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	outcome := promptAsync(ctx, backend, sessionID, "do the thing")
	<-promptIDs
	agent.hangUp()
	if out := <-outcome; !errors.Is(out.err, ErrWorkerUncertain) {
		t.Fatalf("prompt error = %v, want ErrWorkerUncertain", out.err)
	}

	waitFor(t, acpTestTimeout, "the adapter to be respawned", func() bool {
		return agent.generationCount() >= 2 && backend.currentGeneration() == 2
	})
	// Give a reissue every chance to happen before asserting it did not.
	time.Sleep(100 * time.Millisecond)

	if n := agent.countFrames("session/prompt"); n != 1 {
		t.Errorf("session/prompt frames = %d, want exactly the one dispatch", n)
	}
	if n := agent.countFrames("initialize"); n != 1 {
		t.Errorf("initialize frames = %d, want no automatic re-handshake", n)
	}
	if n := agent.countFrames("session/new"); n != 1 {
		t.Errorf("session/new frames = %d, want no automatic session replay", n)
	}
}

// TestACPWorkerCloseSettlesRequests pins that closing a session settles the
// request the planner never answered as cancelled exactly once, and that the
// turn it belonged to ends as uncertain rather than successful.
func TestACPWorkerCloseSettlesRequests(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.sessionIDs = []string{"sess-1"}

	promptIDs := make(chan json.RawMessage, 1)
	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		g.request("q-1", "session/request_question", map[string]any{
			"sessionId":  "sess-1",
			"questionId": "q-1",
			"questions":  []map[string]any{{"question": "proceed?"}},
		})
		promptIDs <- id
		return &acpReply{hold: true}
	})

	entered := make(chan struct{}, 1)
	settled := make(chan struct{}, 4)
	backend := newACPWorkerBackend(agent, nil, func(ctx context.Context, sessionID, requestID, method string, params json.RawMessage) (json.RawMessage, error) {
		entered <- struct{}{}
		<-ctx.Done()
		settled <- struct{}{}
		return nil, ctx.Err()
	})

	sessionID, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	outcome := promptAsync(ctx, backend, sessionID, "ask me something")
	<-promptIDs
	<-entered

	if err := backend.Close(ctx, sessionID); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal("close did not settle the unanswered request")
	}

	// Exactly once: closing again settles nothing more.
	if err := backend.Close(ctx, sessionID); err != nil {
		t.Errorf("second close = %v, want nil", err)
	}
	select {
	case <-settled:
		t.Fatal("the request was settled a second time")
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case out := <-outcome:
		if !errors.Is(out.err, ErrWorkerUncertain) {
			t.Fatalf("prompt of a closed session = %v, want ErrWorkerUncertain", out.err)
		}
	case <-ctx.Done():
		t.Fatal("the turn of a closed session blocked instead of settling")
	}
}

// TestACPWorkerSessionsDoNotCrossRoute pins that two sessions on one adapter
// keep their own ids: updates, prompts and requests are routed by the session
// the adapter names, never to a neighbour.
func TestACPWorkerSessionsDoNotCrossRoute(t *testing.T) {
	ctx := acpContext(t)
	agent := newScriptedAgent()
	agent.sessionIDs = []string{"sess-1", "sess-2"}

	agent.setPromptHook(func(g *scriptedGen, id json.RawMessage, params json.RawMessage) *acpReply {
		sessionID := acpParamSessionID(params)
		g.notify("session/update", map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"kind":    "agent_message_chunk",
				"content": map[string]any{"type": "text", "text": "text for " + sessionID},
			},
		})
		if sessionID == "sess-2" {
			g.request("q-2", "session/request_question", map[string]any{
				"sessionId":  sessionID,
				"questionId": "q-2",
				"questions":  []map[string]any{{"question": "which one?"}},
			})
		}
		return &acpReply{result: map[string]any{"stopReason": "end_turn"}}
	})

	type sinkCall struct{ sessionID, requestID string }
	asked := make(chan sinkCall, 2)
	var rec eventRecorder
	backend := newACPWorkerBackend(agent, rec.sink, func(ctx context.Context, sessionID, requestID, method string, params json.RawMessage) (json.RawMessage, error) {
		asked <- sinkCall{sessionID: sessionID, requestID: requestID}
		return json.RawMessage(`{"answers":[]}`), nil
	})

	first, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start first session: %v", err)
	}
	second, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w2", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("start second session: %v", err)
	}
	if first == second {
		t.Fatalf("both sessions share the id %q", first)
	}

	firstOut := promptAsync(ctx, backend, first, "one")
	secondOut := promptAsync(ctx, backend, second, "two")
	for _, out := range []<-chan promptOutcome{firstOut, secondOut} {
		select {
		case o := <-out:
			if o.err != nil {
				t.Fatalf("concurrent prompt: %v", o.err)
			}
		case <-ctx.Done():
			t.Fatal("a concurrent prompt blocked")
		}
	}

	select {
	case call := <-asked:
		if call != (sinkCall{sessionID: second, requestID: "q-2"}) {
			t.Fatalf("question request = %+v, want it for %q only", call, second)
		}
	case <-ctx.Done():
		t.Fatal("the second session's question never reached the request sink")
	}

	if n := agent.countFrames("session/prompt"); n != 2 {
		t.Fatalf("session/prompt frames = %d, want one per session", n)
	}
	for _, params := range agent.framesFor("session/prompt") {
		switch {
		case strings.Contains(params, `"sessionId":"`+first+`"`), strings.Contains(params, `"sessionId":"`+second+`"`):
		default:
			t.Errorf("session/prompt params = %s, want it to name one of the two sessions", params)
		}
	}

	events := rec.all()
	for _, sessionID := range []string{first, second} {
		texts := eventsMatching(events, sessionID, workerEventTurnText)
		if len(texts) == 0 {
			t.Fatalf("session %q recorded no text event", sessionID)
		}
		want := "text for " + sessionID
		for _, ev := range texts {
			var data struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.data, &data); err != nil {
				t.Fatalf("unmarshal text event: %v", err)
			}
			if data.Text != want {
				t.Errorf("session %q saw text %q, want only %q", sessionID, data.Text, want)
			}
		}
		assertSessionSequence(t, events, sessionID)
	}
}
