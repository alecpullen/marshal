package daemon_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// fixtureModel is the single model the fixture provider advertises, and the one
// the child config names.
const fixtureModel = "daemon-fixture-model"

// scriptedTurn is one assistant reply the fixture provider serves. A turn
// carrying ToolName is emitted as a native tool call; one carrying only Text
// ends the turn. gate, when set, holds the reply until the test opens it (or
// the request is cancelled), which is how a run is kept in flight while a
// viewer detaches and another attaches.
type scriptedTurn struct {
	ToolName string
	ToolArgs json.RawMessage
	Text     string
	gate     <-chan struct{}
}

// providerFixture is a test-owned OpenAI-compatible endpoint for the daemon's
// project child. It serves exactly the scripted turns it was built with and
// refuses everything else, so a test that overruns its script fails instead of
// passing on a fabricated reply.
type providerFixture struct {
	srv *httptest.Server

	mu          sync.Mutex
	turns       []scriptedTurn
	next        int
	completions int
	refused     []string
}

func newProviderFixture(t *testing.T, turns []scriptedTurn) *providerFixture {
	t.Helper()
	p := &providerFixture{turns: append([]scriptedTurn(nil), turns...)}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(p.srv.Close)
	// A refused request is usually why a turn failed, so name them when the
	// test does.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, r := range p.Refusals() {
			t.Logf("fixture provider refused: %s", r)
		}
	})
	return p
}

// URL is the server's base URL; the child config appends "/v1".
func (p *providerFixture) URL() string { return p.srv.URL }

// Completions reports how many chat completions the provider served.
func (p *providerFixture) Completions() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completions
}

// Refusals reports every request the provider could not serve.
func (p *providerFixture) Refusals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.refused...)
}

// useProvider writes the child's config for this fixture into a fresh directory
// and points MARSHAL_CONFIG_DIR at it. It must run before the harness starts its
// first project child: a child inherits this process's environment.
func (p *providerFixture) useProvider(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(childConfig(p.URL())), 0o644); err != nil {
		t.Fatalf("write child config: %v", err)
	}
	t.Setenv("MARSHAL_CONFIG_DIR", dir)
}

// childConfig is the project child's config for the fixture provider. It
// follows internal/test/usability's agentChildConfig: the fixture is the only
// provider and advertises native tool calling, one local-only preset carries
// the profile, the interaction stays Act with Auto off so an approval reaches
// the viewer, and the side calls that would consume a scripted reply are off.
//
// The base_url carries the /v1 suffix: the client appends only the operation's
// path ("/chat/completions", "/models"), so the version segment belongs here.
func childConfig(baseURL string) string {
	return fmt.Sprintf(`[providers.fixture]
type = "openai_compatible"
base_url = %q
tool_calling = true

[models.presets.coder]
provider = "fixture"
model = %q
context_window = 32768
temperature = 0.0
local_only = true

[profile]
default = "local"

[agent_profiles.local]
implementer = "coder"

[agent]
mode = "act"
auto = false
narration_prompt = false
intent_nudge = false

[titling]
enabled = false

[postmortem]
on_exit = "never"
`, baseURL+"/v1", fixtureModel)
}

func (p *providerFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []any{map[string]any{
				"id":       fixtureModel,
				"object":   "model",
				"owned_by": "daemon-fixture",
			}},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		p.completion(w, r)
	default:
		p.refuse(w, r, "unexpected path")
	}
}

// completion takes the next scripted turn, waits out its gate, and answers it.
func (p *providerFixture) completion(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.refuse(w, r, "unreadable request body")
		return
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		p.refuse(w, r, "malformed request body")
		return
	}

	p.mu.Lock()
	if p.next >= len(p.turns) {
		p.mu.Unlock()
		p.refuse(w, r, "completion beyond the end of the scripted turns")
		return
	}
	turn := p.turns[p.next]
	index := p.next
	p.next++
	p.completions++
	p.mu.Unlock()

	if turn.gate != nil {
		select {
		case <-turn.gate:
		case <-r.Context().Done():
			// The run went away while it was held: the scripted turn is
			// consumed, so a retry gets the next one and the test's
			// expectation is visibly wrong rather than silently shifted.
			p.mu.Lock()
			p.refused = append(p.refused, fmt.Sprintf("turn %d cancelled while gated", index))
			p.mu.Unlock()
			return
		}
	}

	if req.Stream {
		p.writeStream(w, turn)
		return
	}
	p.writeCompletion(w, turn)
}

// writeStream answers with OpenAI-compatible SSE chunks: the reply over two
// deltas, a terminal finish_reason and the closing sentinel.
func (p *providerFixture) writeStream(w http.ResponseWriter, turn scriptedTurn) {
	var chunks []map[string]any
	if turn.ToolName != "" {
		args := string(turn.ToolArgs)
		if args == "" {
			args = "{}"
		}
		// Split on a rune boundary so the reassembled arguments round-trip.
		cut := len(args) / 2
		for cut < len(args) && !utf8.RuneStart(args[cut]) {
			cut++
		}
		chunks = []map[string]any{
			chunkDelta(turn, true, args[:cut]),
			chunkDelta(turn, true, args[cut:]),
		}
	} else {
		chunks = []map[string]any{chunkDelta(turn, false, "")}
	}
	chunks[len(chunks)-1]["choices"].([]any)[0].(map[string]any)["finish_reason"] = finishReason(turn)

	var b strings.Builder
	for _, c := range chunks {
		data, err := json.Marshal(c)
		if err != nil {
			p.refuse(w, nil, "marshal SSE chunk")
			return
		}
		b.WriteString("data: ")
		b.Write(data)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// chunkDelta builds one streaming chunk.
func chunkDelta(turn scriptedTurn, toolCall bool, argsPart string) map[string]any {
	delta := map[string]any{"role": "assistant", "content": turn.Text}
	if toolCall {
		delta["content"] = ""
		delta["tool_calls"] = []any{map[string]any{
			"index": 0,
			"id":    "call_1",
			"type":  "function",
			"function": map[string]any{
				"name":      turn.ToolName,
				"arguments": argsPart,
			},
		}}
	}
	return map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}},
	}
}

func finishReason(turn scriptedTurn) string {
	if turn.ToolName != "" {
		return "tool_calls"
	}
	return "stop"
}

// writeCompletion answers a non-streaming request with the JSON equivalent.
func (p *providerFixture) writeCompletion(w http.ResponseWriter, turn scriptedTurn) {
	message := map[string]any{"role": "assistant", "content": turn.Text}
	if turn.ToolName != "" {
		args := string(turn.ToolArgs)
		if args == "" {
			args = "{}"
		}
		message["content"] = ""
		message["tool_calls"] = []any{map[string]any{
			"id":   "call_1",
			"type": "function",
			"function": map[string]any{
				"name":      turn.ToolName,
				"arguments": args,
			},
		}}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-1",
		"object":  "chat.completion",
		"created": 0,
		"model":   fixtureModel,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason(turn),
		}},
	})
}

// refuse records the request and answers with an error, so an unscripted
// request can never look like a successful turn.
func (p *providerFixture) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	method, path := "", ""
	if r != nil {
		method, path = r.Method, r.URL.Path
	}
	p.mu.Lock()
	p.refused = append(p.refused, fmt.Sprintf("%s %s: %s", method, path, reason))
	p.mu.Unlock()
	if w == nil {
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{"message": "daemon fixture provider: " + reason, "type": "fixture_error"},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "fixture provider: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// mustJSON marshals a tool call's argument object.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal tool arguments: %v", err)
	}
	return data
}
