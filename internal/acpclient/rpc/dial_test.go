package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpwire"
)

// pinnedInitializeParams is exactly what Dial's initialize must encode.
const pinnedInitializeParams = `{"protocolVersion":1,"clientCapabilities":{"marshal":{"protocolVersion":1}}}`

// fakeRequest is one frame the fake agent read.
type fakeRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// recordingTransport records whether Close was called on the transport it
// wraps.
type recordingTransport struct {
	Transport

	mu     sync.Mutex
	closed bool
}

// Close records the call and closes the transport underneath.
func (r *recordingTransport) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return r.Transport.Close()
}

// wasClosed reports whether Close was called.
func (r *recordingTransport) wasClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// startFakeAgent serves the agent end of a pipe: it reads frames, hands each
// to handle, and writes the answer back with the request's id. An error from
// handle travels as a JSON-RPC error. The returned transport is the client's
// end, wrapped so a test can see whether Dial closed it.
func startFakeAgent(t *testing.T, handle func(fakeRequest) (any, error)) *recordingTransport {
	t.Helper()

	client, agent := Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(agent)
		sc.Buffer(make([]byte, 0, 64*1024), MaxFrameBytes)
		for sc.Scan() {
			var req fakeRequest
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				return
			}
			frame := map[string]any{"jsonrpc": "2.0", "id": req.ID}
			result, err := handle(req)
			if err != nil {
				frame["error"] = map[string]any{"code": -32603, "message": err.Error()}
			} else {
				frame["result"] = result
			}
			body, merr := json.Marshal(frame)
			if merr != nil {
				return
			}
			if _, werr := agent.Write(append(body, '\n')); werr != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the fake agent did not stop")
		}
	})
	return &recordingTransport{Transport: client}
}

// marshalAnswer is the initialize result the fake agents below answer with: the
// capability at the pinned version and the given agent version.
func marshalAnswer(version string) map[string]any {
	return map[string]any{
		"protocolVersion": 1,
		"agentInfo": map[string]any{
			"name":    "marshal",
			"title":   "Marshal",
			"version": version,
		},
		"agentCapabilities": map[string]any{
			"marshal": map[string]any{"protocolVersion": acpwire.MarshalProtocolVersion},
		},
	}
}

func TestDialSendsTheMarshalCapability(t *testing.T) {
	params := make(chan string, 1)
	client := startFakeAgent(t, func(req fakeRequest) (any, error) {
		params <- string(req.Params)
		return marshalAnswer("1.2.3"), nil
	})

	host, err := Dial(context.Background(), client, DialOptions{SharedFilesystem: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer host.Close()

	select {
	case got := <-params:
		if got != pinnedInitializeParams {
			t.Fatalf("initialize params = %s, want %s", got, pinnedInitializeParams)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the agent never saw initialize")
	}

	if got := host.Agent().Version; got != "1.2.3" {
		t.Fatalf("Agent().Version = %q, want %q", got, "1.2.3")
	}
	if !host.SharedFilesystem() {
		t.Fatal("SharedFilesystem() = false, want the option")
	}
	if host.Err() != nil {
		t.Fatalf("Err() = %v, want nil while the connection lives", host.Err())
	}
	select {
	case <-host.Done():
		t.Fatal("Done() closed while the connection lives")
	default:
	}
}

func TestDialRefusesAnAgentWithoutTheCapability(t *testing.T) {
	client := startFakeAgent(t, func(req fakeRequest) (any, error) {
		return map[string]any{
			"protocolVersion":   1,
			"agentInfo":         map[string]any{"name": "other", "version": "1.2.3"},
			"agentCapabilities": map[string]any{"loadSession": true},
		}, nil
	})

	host, err := Dial(context.Background(), client, DialOptions{})
	if host != nil {
		t.Fatalf("Dial returned a host (%+v) for an agent without the capability", host)
	}
	const want = "rpc: the agent does not speak marshal protocol 1"
	if err == nil || err.Error() != want {
		t.Fatalf("Dial error = %v, want %q", err, want)
	}
	if !client.wasClosed() {
		t.Fatal("Dial left the transport open")
	}
}

func TestDialRefusesAnotherVersion(t *testing.T) {
	client := startFakeAgent(t, func(req fakeRequest) (any, error) {
		return marshalAnswer("1.2.3"), nil
	})

	host, err := Dial(context.Background(), client, DialOptions{AgentVersion: "1.2.4"})
	if host != nil {
		t.Fatalf("Dial returned a host (%+v) for a mismatched version", host)
	}
	const want = `rpc: agent version "1.2.3", want "1.2.4"`
	if err == nil || err.Error() != want {
		t.Fatalf("Dial error = %v, want %q", err, want)
	}
	if !client.wasClosed() {
		t.Fatal("Dial left the transport open")
	}
}
