package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// relayPair is a relay between two in-memory connections, so a test can
// assert the exact bytes on either side of it without a socket in the way.
type relayPair struct {
	fleet       *Fleet
	pc          *projectChild
	client      net.Conn // the test's end of the client side
	agent       net.Conn // the test's end of the child side
	relayClient net.Conn
	relayAgent  net.Conn
	sessionID   string
	// allow is the list the relay's end carries: nil for a project attach,
	// containerViewerSet() for a container's (decisions 2 and 6).
	allow map[string]bool
}

func newRelayPair(t *testing.T, sessionID string) *relayPair {
	t.Helper()
	child := &Child{Transport: newFakeAgent()}
	if err := child.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(child.Stop)
	reg := NewRegistry(child)
	root := t.TempDir()
	reg.track(sessionID, AgentPath(root))
	reg.attachViewer(sessionID)

	relayClient, client := net.Pipe()
	relayAgent, agent := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = agent.Close()
	})
	return &relayPair{
		fleet:       testFleet(t),
		pc:          &projectChild{root: root, child: child, reg: reg, ready: true},
		client:      client,
		agent:       agent,
		relayClient: relayClient,
		relayAgent:  relayAgent,
		sessionID:   sessionID,
	}
}

// start runs the relay. A caller that passes its own reader can hand it one
// that already holds buffered bytes, as the hijacked connection does.
func (p *relayPair) start(reader *bufio.Reader) {
	if reader == nil {
		reader = bufio.NewReader(p.relayClient)
	}
	go p.fleet.relay(relayEnd{
		reg:    p.pc.reg,
		linger: func() { p.fleet.rearmLinger(p.pc) },
		root:   p.pc.root,
		allow:  p.allow,
	}, p.sessionID, "", p.relayClient, reader, p.relayAgent)
}

// restricted makes the pair's relay the one a container's attach runs: its end
// carries the methods a container's viewer may call (decisions 2 and 6).
func (p *relayPair) restricted() { p.allow = containerViewerSet() }

// write puts raw bytes on one side of the relay.
func relayWrite(t *testing.T, conn net.Conn, data string) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, data); err != nil {
		t.Fatalf("write %d bytes: %v", len(data), err)
	}
}

// readExactly reads n bytes from one side of the relay.
func readExactly(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func TestTheRelayChangesNoByte(t *testing.T) {
	frames := []string{
		// Unusual spacing must survive: the relay is a pipe, not a codec.
		`{ "jsonrpc" : "2.0" ,  "id" : 7 , "method" : "session/status" , "params" : { "sessionId" : "s-1" } }` + "\n",
		// A JSON escape, not a re-encoded rune.
		`{"jsonrpc":"2.0","id":8,"method":"session/steer","params":{"sessionId":"s-1","text":"caf\u00e9"}}` + "\n",
		// A notification has no id at all.
		`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s-1"}}` + "\n",
	}

	t.Run("client to child", func(t *testing.T) {
		p := newRelayPair(t, "s-1")
		p.start(nil)
		for i, frame := range frames {
			relayWrite(t, p.client, frame)
			got := readExactly(t, p.agent, len(frame))
			if string(got) != frame {
				t.Fatalf("frame %d changed:\n sent %q\n got  %q", i, frame, got)
			}
		}
	})

	t.Run("child to client", func(t *testing.T) {
		p := newRelayPair(t, "s-1")
		p.start(nil)
		for i, frame := range frames {
			relayWrite(t, p.agent, frame)
			got := readExactly(t, p.client, len(frame))
			if string(got) != frame {
				t.Fatalf("frame %d changed:\n sent %q\n got  %q", i, frame, got)
			}
		}
	})

	t.Run("a frame of exactly the cap", func(t *testing.T) {
		p := newRelayPair(t, "s-1")
		p.start(nil)
		frame := padFrame(maxFrameBytes-1) + "\n" // exactly maxFrameBytes with its newline
		relayWrite(t, p.client, frame)
		got := readExactly(t, p.agent, len(frame))
		if string(got) != frame {
			t.Fatalf("the %d-byte frame changed on the way", len(frame))
		}
	})

	t.Run("the hijacked reader's buffered bytes go first", func(t *testing.T) {
		p := newRelayPair(t, "s-1")
		frame := `{"jsonrpc":"2.0","id":9,"method":"session/status","params":{}}` + "\n"
		// Nothing is reading the pipe yet, so the write is made from its own
		// goroutine and consumed by the buffer below.
		wrote := make(chan error, 1)
		go func() {
			_, err := io.WriteString(p.client, frame)
			wrote <- err
		}()
		// Read the frame into a reader before the relay starts, exactly as
		// the HTTP server's hijacked reader holds it.
		buffered := bufio.NewReader(p.relayClient)
		if _, err := buffered.Peek(len(frame)); err != nil {
			t.Fatalf("buffer the frame: %v", err)
		}
		if err := <-wrote; err != nil {
			t.Fatalf("write the frame: %v", err)
		}
		p.start(buffered)
		got := readExactly(t, p.agent, len(frame))
		if string(got) != frame {
			t.Fatalf("the buffered frame changed:\n sent %q\n got  %q", frame, got)
		}
	})
}

// ownedByAgent makes sessionID an agent's on the pair's fleet, as Spawn leaves
// it: a record naming the session and the classifier's map that names the
// agent (P4 D10 / decision 2).
func (p *relayPair) ownedByAgent(t *testing.T, sessionID, id, name string) {
	t.Helper()
	if err := p.fleet.ws.PutAgent(Agent{
		ID: id, Project: p.pc.root, Name: name, SessionID: sessionID,
		OwnerID: "owner-1", Origin: OriginUI,
	}); err != nil {
		t.Fatalf("record agent %s: %v", id, err)
	}
	p.fleet.mu.Lock()
	p.fleet.sessionAgent[sessionID] = id
	p.fleet.mu.Unlock()
}

// readLine reads one newline-terminated frame from one side of the relay.
func readLine(t *testing.T, src *bufio.Reader) string {
	t.Helper()
	line, err := src.ReadString('\n')
	if err != nil {
		t.Fatalf("read a frame: %v", err)
	}
	return line
}

// readsNothing fails when the side delivers a frame within a short budget.
func readsNothing(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("the relay forwarded %d bytes: %q", n, buf[:n])
	}
	_ = conn.SetReadDeadline(time.Time{})
}

// checkRefusalAnswer decodes a relay's error answer and asserts its shape: the
// request's own id, -32000, and the sentence as both the message and the
// message of an acpwire.Error-shaped data with an empty code, which is how a
// marshal client reads it as that sentence.
func checkRefusalAnswer(t *testing.T, answer, wantID, wantSentence string) {
	t.Helper()
	var out struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(answer), &out); err != nil {
		t.Fatalf("decode the refusal %q: %v", answer, err)
	}
	if out.JSONRPC != "2.0" {
		t.Errorf("refusal jsonrpc = %q, want 2.0", out.JSONRPC)
	}
	if got := compactID(out.ID); got != wantID {
		t.Errorf("refusal id = %s, want %s", got, wantID)
	}
	if out.Error.Code != -32000 {
		t.Errorf("refusal code = %d, want -32000", out.Error.Code)
	}
	if out.Error.Message != wantSentence {
		t.Errorf("refusal message = %q, want %q", out.Error.Message, wantSentence)
	}
	if out.Error.Data.Code != "" {
		t.Errorf("refusal data code = %q, want empty", out.Error.Data.Code)
	}
	if out.Error.Data.Message != wantSentence {
		t.Errorf("refusal data message = %q, want %q", out.Error.Data.Message, wantSentence)
	}
}

// refusedSentence is decision 3's one sentence, named as the fleet names the
// agent.
const refusedSentence = "This is agent reviewer's session: finish it from the fleet screen."

// TestAViewerCannotCloseAnAgentsSession: a close naming an agent's session is
// answered to the client and never reaches the child (decision 1, F5).
func TestAViewerCannotCloseAnAgentsSession(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.ownedByAgent(t, p.sessionID, "agent-a", "reviewer")
	p.start(nil)

	relayWrite(t, p.client, `{"jsonrpc":"2.0","id":7,"method":"session/close","params":{"sessionId":"s-1"}}`+"\n")

	answer := readLine(t, bufio.NewReader(p.client))
	checkRefusalAnswer(t, answer, "7", refusedSentence)
	readsNothing(t, p.agent)
}

// TestAViewerCannotReplaceAnAgentsSession: a replacing session/new naming an
// agent's session is refused the same way, and the child never sees it
// (decision 1).
func TestAViewerCannotReplaceAnAgentsSession(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.ownedByAgent(t, p.sessionID, "agent-a", "reviewer")
	p.start(nil)

	relayWrite(t, p.client, `{"jsonrpc":"2.0","id":8,"method":"session/new","params":{"cwd":"/tmp","replaces":"s-1","mcpServers":[]}}`+"\n")

	answer := readLine(t, bufio.NewReader(p.client))
	checkRefusalAnswer(t, answer, "8", refusedSentence)
	readsNothing(t, p.agent)
}

// TestAProjectViewerCannotCloseAnotherAgentsSession: the rule is one rule, on
// every relay. A project viewer attached to its own session cannot close a
// different session a fleet agent owns on the same child (decision 1).
func TestAProjectViewerCannotCloseAnotherAgentsSession(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.ownedByAgent(t, "s-2", "agent-a", "reviewer")
	p.start(nil)

	relayWrite(t, p.client, `{"jsonrpc":"2.0","id":9,"method":"session/close","params":{"sessionId":"s-2"}}`+"\n")

	answer := readLine(t, bufio.NewReader(p.client))
	checkRefusalAnswer(t, answer, "9", refusedSentence)
	readsNothing(t, p.agent)
}

// TestOtherFramesStillPass: the refusal is narrow. A prompt and a close of a
// session no agent owns are forwarded unchanged (decision 1).
func TestOtherFramesStillPass(t *testing.T) {
	p := newRelayPair(t, "s-1")
	// An agent owns a different session: the pair's own is nobody's.
	p.ownedByAgent(t, "agent-a-session", "agent-a", "reviewer")
	p.start(nil)

	prompt := `{"jsonrpc":"2.0","id":10,"method":"session/prompt","params":{"sessionId":"s-1","prompt":[{"type":"text","text":"hi"}]}}` + "\n"
	relayWrite(t, p.client, prompt)
	if got := string(readExactly(t, p.agent, len(prompt))); got != prompt {
		t.Fatalf("a prompt changed on the way:\n sent %q\n got  %q", prompt, got)
	}

	closing := `{"jsonrpc":"2.0","id":11,"method":"session/close","params":{"sessionId":"s-1"}}` + "\n"
	relayWrite(t, p.client, closing)
	if got := string(readExactly(t, p.agent, len(closing))); got != closing {
		t.Fatalf("a close of a session no agent owns changed on the way:\n sent %q\n got  %q", closing, got)
	}
	// Nothing was answered to the client: the child's answer is the client's
	// answer, and the relay adds none of its own.
	readsNothing(t, p.client)
}

// assertDropped writes frame and then a listed request after it, and asserts
// the child's next bytes are that request: the relay dropped frame rather than
// forwarding it, and forwarded what followed.
func assertDropped(t *testing.T, p *relayPair, frame string) {
	t.Helper()
	follow := `{"jsonrpc":"2.0","id":77,"method":"session/status","params":{"sessionId":"s-1"}}` + "\n"
	relayWrite(t, p.client, frame)
	relayWrite(t, p.client, follow)
	if got := string(readExactly(t, p.agent, len(follow))); got != follow {
		t.Fatalf("the frame was not dropped, or the next frame changed:\n sent %q\n got  %q", follow, got)
	}
	// A dropped frame is never answered to the viewer.
	readsNothing(t, p.client)
}

// TestARestrictedRelayLogsOneDropPerMethod pins decision 2's once-per-method
// drop log: a viewer retrying a refused notification gets one line per method
// per attach, where a line per frame would let it flood the log.
func TestARestrictedRelayLogsOneDropPerMethod(t *testing.T) {
	sink := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := newRelayPair(t, "s-1")
	p.restricted()
	p.start(nil)

	// The same unlisted notification twice: both are dropped, the first is
	// logged.
	frame := `{"jsonrpc":"2.0","method":"session/reload_config","params":{"sessionId":"s-1"}}` + "\n"
	assertDropped(t, p, frame)
	assertDropped(t, p, frame)

	if got := strings.Count(sink.String(), "restricted relay dropped a frame"); got != 1 {
		t.Fatalf("the relay logged %d drops for one method, want 1:\n%s", got, sink.String())
	}
}

// TestARestrictedRelayRefusesAnUnlistedRequest: a container's viewer may not
// call a method outside its session screen's surface, and the relay answers
// with decision 2's sentence under the request's own id (W9, the relay's end).
func TestARestrictedRelayRefusesAnUnlistedRequest(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.restricted()
	p.start(nil)

	relayWrite(t, p.client, `{"jsonrpc":"2.0","id":21,"method":"config/set_value","params":{"key":"theme","value":"dark"}}`+"\n")

	answer := readLine(t, bufio.NewReader(p.client))
	checkRefusalAnswer(t, answer, "21", "config/set_value is not available for a containerized agent")
	readsNothing(t, p.agent)
}

// TestARestrictedRelayDropsAnUnlistedNotification: a notification the list
// does not name has no answer: it is dropped, and the child reads nothing
// (decision 2).
func TestARestrictedRelayDropsAnUnlistedNotification(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.restricted()
	p.start(nil)

	assertDropped(t, p, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s-1"}}`+"\n")
}

// TestARestrictedRelayForwardsAListedRequest: a method the list names reaches
// the child, and its answer comes back, both unchanged (decision 7).
func TestARestrictedRelayForwardsAListedRequest(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.restricted()
	p.start(nil)

	request := `{"jsonrpc":"2.0","id":22,"method":"session/status","params":{"sessionId":"s-1"}}` + "\n"
	relayWrite(t, p.client, request)
	if got := string(readExactly(t, p.agent, len(request))); got != request {
		t.Fatalf("a listed request changed:\n sent %q\n got  %q", request, got)
	}

	answer := `{"jsonrpc":"2.0","id":22,"result":{"status":"idle"}}` + "\n"
	relayWrite(t, p.agent, answer)
	if got := string(readExactly(t, p.client, len(answer))); got != answer {
		t.Fatalf("the answer changed:\n sent %q\n got  %q", answer, got)
	}
}

// TestARestrictedRelayForwardsAnswersToTheAgent: an answer to one of the
// agent's own requests carries no method and passes unchanged, so approvals
// and questions still reach the viewer and its answers reach the agent
// (decision 7).
func TestARestrictedRelayForwardsAnswersToTheAgent(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.restricted()
	p.start(nil)

	ask := `{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"s-1","options":[]}}` + "\n"
	relayWrite(t, p.agent, ask)
	if got := string(readExactly(t, p.client, len(ask))); got != ask {
		t.Fatalf("the agent's own request changed:\n sent %q\n got  %q", ask, got)
	}

	answer := `{"jsonrpc":"2.0","id":"perm-1","result":{"outcome":{"outcome":"selected","optionId":"allow"}}}` + "\n"
	relayWrite(t, p.client, answer)
	if got := string(readExactly(t, p.agent, len(answer))); got != answer {
		t.Fatalf("an answer to the agent's own request changed:\n sent %q\n got  %q", answer, got)
	}
}

// TestARestrictedRelayDropsABatch: a batch is not one JSON-RPC message, so the
// restricted relay fails closed and drops it (decision 7).
func TestARestrictedRelayDropsABatch(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.restricted()
	p.start(nil)

	assertDropped(t, p, `[{"jsonrpc":"2.0","id":23,"method":"session/status","params":{"sessionId":"s-1"}}]`+"\n")
}

// TestARestrictedRelayDropsMalformedJSON: a frame that is not JSON is dropped
// rather than forwarded (decision 7).
func TestARestrictedRelayDropsMalformedJSON(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.restricted()
	p.start(nil)

	assertDropped(t, p, `{"jsonrpc":"2.0","id":24,"method":`+"\n")
}

// TestARestrictedRelayStillRefusesClose: the restriction comes before the
// close rule, and a listed request still meets it, so a container's viewer can
// no more finish the agent's session than any other viewer (decision 1).
func TestARestrictedRelayStillRefusesClose(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.ownedByAgent(t, p.sessionID, "agent-a", "reviewer")
	p.restricted()
	p.start(nil)

	relayWrite(t, p.client, `{"jsonrpc":"2.0","id":25,"method":"session/close","params":{"sessionId":"s-1"}}`+"\n")

	answer := readLine(t, bufio.NewReader(p.client))
	checkRefusalAnswer(t, answer, "25", refusedSentence)
	readsNothing(t, p.agent)
}

// TestAnUnrestrictedRelayForwardsEverything: a project attach's relay carries
// no list, so a method a container's viewer may not call, a notification, a
// batch and a malformed frame all pass exactly as they did before (decision
// 6).
func TestAnUnrestrictedRelayForwardsEverything(t *testing.T) {
	p := newRelayPair(t, "s-1")
	p.start(nil)

	frames := []string{
		`{"jsonrpc":"2.0","id":31,"method":"config/set_value","params":{"key":"theme","value":"dark"}}` + "\n",
		`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s-1"}}` + "\n",
		`[{"jsonrpc":"2.0","id":32,"method":"session/status","params":{"sessionId":"s-1"}}]` + "\n",
		`{"jsonrpc":"2.0","id":33,"method":` + "\n",
	}
	for _, frame := range frames {
		relayWrite(t, p.client, frame)
		if got := string(readExactly(t, p.agent, len(frame))); got != frame {
			t.Fatalf("an unrestricted relay changed a frame:\n sent %q\n got  %q", frame, got)
		}
	}
	// The relay answers none of them: the child's answer is the viewer's
	// answer, and the relay adds none of its own.
	readsNothing(t, p.client)
}

// padFrame builds a valid JSON frame of exactly n bytes.
func padFrame(n int) string {
	head := `{"jsonrpc":"2.0","id":1,"method":"session/status","params":{"pad":"`
	tail := `"}}`
	filler := n - len(head) - len(tail)
	if filler < 0 {
		panic("padFrame: n too small")
	}
	return head + strings.Repeat("x", filler) + tail
}

func TestAReplacementRenamesTheSession(t *testing.T) {
	t.Run("a successful replacement renames", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)
		res := h.attach(http.MethodPost, root, "", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		oldID := res.header.Get(sessionHeader)
		pc, err := h.fleet.projectChildFor(context.Background(), root)
		if err != nil {
			t.Fatalf("projectChildFor: %v", err)
		}

		res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":42,"method":"session/new","params":{"cwd":%q,"replaces":%q,"mcpServers":[]}}`, root, oldID))
		answer := res.readLine(t)
		var out struct {
			Result struct {
				SessionID string `json:"sessionId"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(answer), &out); err != nil {
			t.Fatalf("decode the replacement's answer %q: %v", answer, err)
		}
		newID := out.Result.SessionID
		if newID == "" || newID == oldID {
			t.Fatalf("replacement session = %q, want a new id (old %q)", newID, oldID)
		}

		eventually(t, "the registry to follow the replacement", func() bool {
			ids := summaryIDs(pc.reg.summaries())
			return len(ids) == 1 && ids[0] == newID
		})

		// The next close the client sends names the new ID, and the bridge
		// closes that one.
		res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":43,"method":"session/close","params":{"sessionId":%q}}`, newID))
		_ = res.readLine(t)
		eventually(t, "the bridge to close the new id", func() bool {
			// Only the child's own connection is the bridge's close: the
			// client's frame is relayed on its viewer connection.
			for _, f := range framesOf(t, h.fake(root), stdioConn, "session/close") {
				if paramsOf(t, f.params)["sessionId"] == newID {
					return true
				}
			}
			return false
		})
		eventually(t, "the registry to drop the closed session", func() bool {
			return pc.reg.count() == 0
		})
	})

	t.Run("an error answer renames nothing", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)
		res := h.attach(http.MethodPost, root, "", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		oldID := res.header.Get(sessionHeader)
		pc, err := h.fleet.projectChildFor(context.Background(), root)
		if err != nil {
			t.Fatalf("projectChildFor: %v", err)
		}
		fake := h.fake(root)
		fake.mu.Lock()
		fake.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
			if method == "session/new" {
				return nil, &rpcError{Code: -32000, Message: "replacement refused"}, true
			}
			return nil, nil, false
		}
		fake.mu.Unlock()

		res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":44,"method":"session/new","params":{"cwd":%q,"replaces":%q}}`, root, oldID))
		answer := res.readLine(t)
		if !strings.Contains(answer, "error") {
			t.Fatalf("replacement answer = %q, want an error", answer)
		}
		time.Sleep(20 * time.Millisecond)
		ids := summaryIDs(pc.reg.summaries())
		if len(ids) != 1 || ids[0] != oldID {
			t.Fatalf("registry after a refused replacement = %v, want [%s]", ids, oldID)
		}
	})
}

func TestAViewersCloseClosesTheRuntime(t *testing.T) {
	t.Run("a successful close closes the runtime", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)

		var mu sync.Mutex
		var armed []func()
		h.fleet.afterFunc = func(d time.Duration, fn func()) func() {
			mu.Lock()
			armed = append(armed, fn)
			n := len(armed)
			mu.Unlock()
			if want := h.fleet.lingerDuration(); d != want {
				t.Errorf("linger armed for %v, want %v", d, want)
			}
			_ = n
			return func() {}
		}
		armedCount := func() int {
			mu.Lock()
			defer mu.Unlock()
			return len(armed)
		}

		res := h.attach(http.MethodPost, root, "", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		sessionID := res.header.Get(sessionHeader)
		pc, err := h.fleet.projectChildFor(context.Background(), root)
		if err != nil {
			t.Fatalf("projectChildFor: %v", err)
		}
		fake := h.fake(root)

		// A timer armed while the session is tracked must not reap the child.
		if armedCount() != 1 {
			t.Fatalf("the start armed %d lingers, want 1", armedCount())
		}
		mu.Lock()
		first := armed[0]
		mu.Unlock()
		first()
		if fake.wasStopped() {
			t.Fatal("the linger reaped a child that still tracks a session")
		}
		if pc.reg.count() != 1 {
			t.Fatalf("the registry tracks %d sessions, want 1", pc.reg.count())
		}

		res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":51,"method":"session/close","params":{"sessionId":%q}}`, sessionID))
		_ = res.readLine(t)

		eventually(t, "the bridge to close the session on its own connection", func() bool {
			for _, f := range framesOf(t, fake, stdioConn, "session/close") {
				if paramsOf(t, f.params)["sessionId"] == sessionID {
					return true
				}
			}
			return false
		})
		eventually(t, "the session to be untracked", func() bool {
			return pc.reg.count() == 0
		})

		// The relay ends when the viewer goes; with the registry empty the
		// linger is armed again.
		_ = res.conn.Close()
		eventually(t, "the linger to arm on an empty registry", func() bool {
			return armedCount() == 2
		})
		mu.Lock()
		second := armed[1]
		mu.Unlock()
		second()
		eventually(t, "the empty child to stop", func() bool { return fake.wasStopped() })
	})

	t.Run("an error answer closes nothing", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)
		res := h.attach(http.MethodPost, root, "", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		sessionID := res.header.Get(sessionHeader)
		pc, err := h.fleet.projectChildFor(context.Background(), root)
		if err != nil {
			t.Fatalf("projectChildFor: %v", err)
		}
		fake := h.fake(root)
		fake.mu.Lock()
		fake.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
			if method == "session/close" {
				return nil, &rpcError{Code: -32000, Message: "cannot close"}, true
			}
			return nil, nil, false
		}
		fake.mu.Unlock()

		res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":52,"method":"session/close","params":{"sessionId":%q}}`, sessionID))
		answer := res.readLine(t)
		if !strings.Contains(answer, "error") {
			t.Fatalf("close answer = %q, want an error", answer)
		}
		time.Sleep(50 * time.Millisecond)
		if got := len(framesOf(t, fake, stdioConn, "session/close")); got != 0 {
			t.Errorf("the bridge closed the session %d times despite the error answer", got)
		}
		if pc.reg.count() != 1 {
			t.Errorf("a refused close untracked the session")
		}
	})
}

func TestAnOversizeFrameEndsTheRelay(t *testing.T) {
	const cap = 1024
	root := t.TempDir()
	h := newAttachHarness(t)
	// The cap belongs to this fleet rather than the package: a neighbouring
	// test's relay pumps may still be reading the global.
	h.fleet.relayFrameCap = cap
	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("attach = %d, want 101", res.status)
	}
	sessionID := res.header.Get(sessionHeader)
	pc, err := h.fleet.projectChildFor(context.Background(), root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}

	// A frame past the cap ends both sides rather than forwarding a prefix.
	relayWrite(t, res.conn, padFrame(cap+8)+"\n")

	_ = res.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := res.reader.ReadByte(); err == nil {
		t.Fatal("the relay kept the client's side open after an oversize frame")
	}
	eventually(t, "the viewer to be uncounted", func() bool {
		for _, s := range pc.reg.summaries() {
			if s.ID == sessionID {
				return s.Viewers == 0
			}
		}
		return false
	})
}

func TestEitherSideEndingEndsTheOther(t *testing.T) {
	t.Run("the child's death closes the client", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)
		res := h.attach(http.MethodPost, root, "", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		h.fake(root).die()

		_ = res.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := res.reader.ReadByte(); err == nil {
			t.Fatal("the client's stream stayed open after the child died")
		}
	})

	t.Run("the client hanging up ends the relay", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)
		res := h.attach(http.MethodPost, root, "", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		sessionID := res.header.Get(sessionHeader)
		pc, err := h.fleet.projectChildFor(context.Background(), root)
		if err != nil {
			t.Fatalf("projectChildFor: %v", err)
		}
		_ = res.conn.Close()

		eventually(t, "the viewer to be uncounted", func() bool {
			for _, s := range pc.reg.summaries() {
				if s.ID == sessionID {
					return s.Viewers == 0
				}
			}
			return false
		})
	})
}

func TestTheRelayTouchesTheSession(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)
	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("attach = %d, want 101", res.status)
	}
	sessionID := res.header.Get(sessionHeader)
	pc, err := h.fleet.projectChildFor(context.Background(), root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	lastActive := func() int64 {
		for _, s := range pc.reg.summaries() {
			if s.ID == sessionID {
				return s.LastActive
			}
		}
		return 0
	}
	before := lastActive()
	if before == 0 {
		t.Fatal("the session has no last-active time")
	}
	time.Sleep(10 * time.Millisecond)

	res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":61,"method":"session/status","params":{"sessionId":%q}}`, sessionID))
	_ = res.readLine(t)

	eventually(t, "the relayed frame to touch the session", func() bool {
		return lastActive() > before
	})
}

// syncBuffer is a log sink a test reads while another goroutine writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestTheRelayLogsNoParams(t *testing.T) {
	sink := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	root := t.TempDir()
	h := newAttachHarness(t)
	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("attach = %d, want 101", res.status)
	}
	sessionID := res.header.Get(sessionHeader)
	const secret = "sk-live-do-not-log-me"
	res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":71,"method":"session/status","params":{"sessionId":%q,"secret":%q}}`, sessionID, secret))
	_ = res.readLine(t)
	_ = res.conn.Close()

	eventually(t, "the detach to be logged", func() bool {
		return strings.Contains(sink.String(), "detached")
	})
	if strings.Contains(sink.String(), secret) {
		t.Fatalf("the relay's log carried a frame's params:\n%s", sink.String())
	}
	if !strings.Contains(sink.String(), root) {
		t.Errorf("the relay's log does not name the root:\n%s", sink.String())
	}
}

// registryForRoot is the registry of the project child the harness started for
// root: the registry a restart of that child counts its viewers on.
func (h *attachHarness) registryForRoot(t *testing.T, root string) *Registry {
	t.Helper()
	pc, err := h.fleet.projectChildFor(context.Background(), root)
	if err != nil {
		t.Fatalf("projectChildFor(%s): %v", root, err)
	}
	return pc.reg
}

// trackedRelays is how many live relays the fleet has counted on reg.
func (f *Fleet) trackedRelays(reg *Registry) int {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	return len(f.relays[reg])
}

// keptRegistry reports whether reg still has an entry in the fleet's map.
func (f *Fleet) keptRegistry(reg *Registry) bool {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	_, ok := f.relays[reg]
	return ok
}

// TestTheRelayIsTrackedWhileItRuns: an attach is in f.relays under the child's
// registry for as long as it runs, and its registry's entry goes with it.
func TestTheRelayIsTrackedWhileItRuns(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("attach = %d, want 101", res.status)
	}
	reg := h.registryForRoot(t, root)
	eventually(t, "the relay to be tracked", func() bool {
		return h.fleet.trackedRelays(reg) == 1
	})

	_ = res.conn.Close()
	eventually(t, "the relay to be untracked", func() bool {
		return h.fleet.trackedRelays(reg) == 0
	})
	if h.fleet.keptRegistry(reg) {
		t.Fatal("an emptied registry kept its entry in the fleet's relay map")
	}
}

// TestTheViewersHearTheRestart: one announcement reaches every viewer of the
// child it names, as the agent's own marshal/restarting frame.
func TestTheViewersHearTheRestart(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	first := h.attach(http.MethodPost, root, "", true)
	if first.status != http.StatusSwitchingProtocols {
		t.Fatalf("first attach = %d, want 101", first.status)
	}
	sessionID := first.header.Get(sessionHeader)
	second := h.attach(http.MethodPost, root, sessionID, true)
	if second.status != http.StatusSwitchingProtocols {
		t.Fatalf("second attach = %d, want 101", second.status)
	}

	reg := h.registryForRoot(t, root)
	eventually(t, "both viewers to be tracked", func() bool {
		return h.fleet.trackedRelays(reg) == 2
	})

	if got := h.fleet.announceRestart(reg, "a new marshal build", "v2"); got != 2 {
		t.Fatalf("announceRestart reached %d viewers, want 2", got)
	}
	want := `{"jsonrpc":"2.0","method":"marshal/restarting","params":{"reason":"a new marshal build","version":"v2"}}` + "\n"
	for i, res := range []*attachResult{first, second} {
		if line := readLine(t, res.reader); line != want {
			t.Errorf("viewer %d heard %q, want %q", i+1, line, want)
		}
	}
}

// TestAnAnnouncementSkipsOtherChildren: an announcement is the child's, so a
// viewer of another child hears nothing. The frame carries no version when
// there is none, as the agent's own does not.
func TestAnAnnouncementSkipsOtherChildren(t *testing.T) {
	firstRoot := t.TempDir()
	otherRoot := t.TempDir()
	h := newAttachHarness(t)

	first := h.attach(http.MethodPost, firstRoot, "", true)
	if first.status != http.StatusSwitchingProtocols {
		t.Fatalf("first attach = %d, want 101", first.status)
	}
	other := h.attach(http.MethodPost, otherRoot, "", true)
	if other.status != http.StatusSwitchingProtocols {
		t.Fatalf("other attach = %d, want 101", other.status)
	}

	firstReg := h.registryForRoot(t, firstRoot)
	otherReg := h.registryForRoot(t, otherRoot)
	eventually(t, "both children's viewers to be tracked", func() bool {
		return h.fleet.trackedRelays(firstReg) == 1 && h.fleet.trackedRelays(otherReg) == 1
	})

	if got := h.fleet.announceRestart(firstReg, "a new marshal build", ""); got != 1 {
		t.Fatalf("announceRestart reached %d viewers, want 1", got)
	}
	want := `{"jsonrpc":"2.0","method":"marshal/restarting","params":{"reason":"a new marshal build"}}` + "\n"
	if line := readLine(t, first.reader); line != want {
		t.Errorf("the child's viewer heard %q, want %q", line, want)
	}

	_ = other.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if line, err := other.reader.ReadString('\n'); err == nil {
		t.Fatalf("the other child's viewer heard %q", line)
	}
	_ = other.conn.SetReadDeadline(time.Time{})
}
