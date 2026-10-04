package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent is an in-memory agentTransport for the control-plane tests.
// It records every request, answers from results/handler, hands out
// distinct session ids, and can inject notifications or hang up, so tests
// observe exactly what the bridge sent without a child process.
type fakeAgent struct {
	mu     sync.Mutex
	frames []capturedFrame
	// results answers a method with a fixed JSON value; handler, when set,
	// is consulted first and may return an rpc error.
	results map[string]any
	handler func(method string, params json.RawMessage) (any, *rpcError, bool)
	// caps replaces the capabilities initialize advertises.
	caps     map[string]any
	sessions int
	gens     int
	writeMu  sync.Mutex
	out      io.WriteCloser
}

func newFakeAgent() *fakeAgent { return &fakeAgent{results: map[string]any{}} }

func (a *fakeAgent) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	a.mu.Lock()
	a.gens++
	a.mu.Unlock()
	a.writeMu.Lock()
	a.out = stdoutW
	a.writeMu.Unlock()
	go a.serve(stdinR, stdoutW)
	return stdinW, stdoutR, io.NopCloser(strings.NewReader("")), nil
}

func (a *fakeAgent) serve(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.Method == "" {
			continue
		}
		a.mu.Lock()
		a.frames = append(a.frames, capturedFrame{method: req.Method, params: string(req.Params)})
		a.mu.Unlock()
		if len(req.ID) == 0 {
			continue // notification
		}
		a.reply(w, req.ID, req.Method, req.Params)
	}
}

func (a *fakeAgent) reply(w io.Writer, id json.RawMessage, method string, params json.RawMessage) {
	send := func(frame map[string]any) {
		frame["jsonrpc"], frame["id"] = "2.0", id
		a.writeMu.Lock()
		_ = json.NewEncoder(w).Encode(frame)
		a.writeMu.Unlock()
	}
	a.mu.Lock()
	h := a.handler
	a.mu.Unlock()
	if h != nil {
		if res, rerr, ok := h(method, params); ok {
			if rerr != nil {
				send(map[string]any{"error": rerr})
			} else {
				send(map[string]any{"result": res})
			}
			return
		}
	}
	a.mu.Lock()
	res, ok := a.results[method]
	caps := a.caps
	var sid string
	if method == "session/new" {
		a.sessions++
		sid = "s-" + strconv.Itoa(a.sessions)
	}
	a.mu.Unlock()
	switch {
	case ok:
		send(map[string]any{"result": res})
	case method == "initialize":
		if caps == nil {
			caps = map[string]any{"configAccess": map[string]any{}, "watchAccess": map[string]any{}}
		}
		send(map[string]any{"result": map[string]any{
			"protocolVersion":   1,
			"agentInfo":         map[string]any{"version": "dev"},
			"agentCapabilities": caps,
		}})
	case method == "session/new":
		send(map[string]any{"result": map[string]any{"sessionId": sid}})
	default:
		send(map[string]any{"result": map[string]any{}})
	}
}

// notify injects a notification into the current generation.
func (a *fakeAgent) notify(method string, params any) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if a.out != nil {
		_ = json.NewEncoder(a.out).Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
}

// die hangs up the current generation, as a crashed agent would.
func (a *fakeAgent) die() {
	a.writeMu.Lock()
	out := a.out
	a.writeMu.Unlock()
	if out != nil {
		_ = out.Close()
	}
}

// calls returns the params of every request seen for method, in order.
func (a *fakeAgent) calls(method string) []string {
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

func (a *fakeAgent) generations() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gens
}

func (a *fakeAgent) Wait() error                { return nil }
func (a *fakeAgent) Signal(sig os.Signal) error { return nil }
func (a *fakeAgent) Kill() error                { return nil }
func (a *fakeAgent) Detach() error              { return nil }

// ctlFleet builds a fleet whose control agent is agent. Project agents use
// newAgent when non-nil, else the registry helper child.
func ctlFleet(t *testing.T, agent *fakeAgent) *Fleet {
	t.Helper()
	f := testFleet(t)
	f.audit = NewAuditLog(t.TempDir())
	f.newControl = func() (*Child, error) { return &Child{Transport: agent}, nil }
	return f
}

// agentFleet builds a fleet in which every project agent (and the control
// agent) is a fresh fakeAgent, returned by id for inspection.
func agentFleet(t *testing.T) (*Fleet, *fakeAgent, func(agentID string) *fakeAgent) {
	t.Helper()
	ctl := newFakeAgent()
	f := ctlFleet(t, ctl)
	var mu sync.Mutex
	byID := map[string]*fakeAgent{}
	f.newRuntime = func(a Agent) (*Child, error) {
		fa := newFakeAgent()
		mu.Lock()
		byID[a.ID] = fa
		mu.Unlock()
		return &Child{Transport: fa}, nil
	}
	return f, ctl, func(id string) *fakeAgent {
		mu.Lock()
		defer mu.Unlock()
		return byID[id]
	}
}

func ctlContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
