package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
)

// scripted is an in-process LSP server for tests: per-method canned or
// computed results, recorded notifications and requests, and server-pushed
// publishDiagnostics.
type scripted struct {
	t        *testing.T
	mu       sync.Mutex
	handlers map[string]func(json.RawMessage) string
	notified map[string][]json.RawMessage
	requests map[string][]json.RawMessage
	out      io.WriteCloser
	outMu    sync.Mutex
}

func newScripted(t *testing.T) (*Client, *scripted) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	s := &scripted{t: t, handlers: map[string]func(json.RawMessage) string{}, notified: map[string][]json.RawMessage{}, requests: map[string][]json.RawMessage{}, out: s2cW}
	s.handle("initialize", `{"capabilities":{}}`)
	go s.loop(c2sR)
	c := newClient(c2sW, s2cR)
	t.Cleanup(func() { s.kill(); c.Close() })
	return c, s
}

func (s *scripted) handle(method, result string) {
	s.handleFunc(method, func(json.RawMessage) string { return result })
}

func (s *scripted) handleFunc(method string, fn func(json.RawMessage) string) {
	s.mu.Lock()
	s.handlers[method] = fn
	s.mu.Unlock()
}

func (s *scripted) loop(in io.Reader) {
	r := bufio.NewReader(in)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		s.mu.Lock()
		if m.ID == nil {
			s.notified[m.Method] = append(s.notified[m.Method], m.Params)
			s.mu.Unlock()
			continue
		}
		s.requests[m.Method] = append(s.requests[m.Method], m.Params)
		fn := s.handlers[m.Method]
		s.mu.Unlock()
		result := "null"
		if fn != nil {
			result = fn(m.Params)
		}
		s.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, m.ID, result))
	}
}

func (s *scripted) write(body string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_ = writeMessage(s.out, []byte(body))
}

func (s *scripted) publish(uri string, diags ...Diagnostic) {
	if diags == nil {
		diags = []Diagnostic{}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "diagnostics": diags}})
	s.write(string(b))
}

func (s *scripted) notes(method string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.notified[method]...)
}

func (s *scripted) lastRequest(method string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.requests[method]
	if len(rs) == 0 {
		return nil
	}
	return rs[len(rs)-1]
}

// kill closes the server→client stream, as a crashed server would.
func (s *scripted) kill() { _ = s.out.Close() }
