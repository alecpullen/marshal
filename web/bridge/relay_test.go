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
	go p.fleet.relay(p.pc, p.sessionID, p.relayClient, reader, p.relayAgent)
}

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
