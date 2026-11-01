package usability

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// fixtureModel is the single model the fixture provider advertises. The child
// config pinned by buildAgentChildEnv names it as the agent's preset.
const fixtureModel = "fixture-tool-model"

// scriptedReply is one assistant reply the fixture provider returns for one
// chat completion request. Replies are consumed in the order they were
// configured: exactly one per matching chat turn. A reply carrying ToolName is
// emitted as a native tool call; a reply carrying only Text ends the turn.
type scriptedReply struct {
	// ToolName is the tool the assistant calls. Empty means a text reply.
	ToolName string
	// ToolArgs is the JSON argument object for ToolName.
	ToolArgs json.RawMessage
	// Text is the assistant's content. For a tool call it is the (usually
	// empty) preamble and is sent as the same chunk's content.
	Text string
}

// capturedToolMessage is one tool-role message the agent sent back to the
// provider: the call it answers and the content it carried. It is the
// independent oracle for what a tool actually reported, as distinct from
// whatever the UI happened to render.
type capturedToolMessage struct {
	CallID  string
	Content string
}

// unexpectedRequest is one request the fixture provider refused to serve: an
// unexpected path, an unreadable or unsupported body, or a completion beyond
// the end of the script.
type unexpectedRequest struct {
	Method string
	Path   string
	Reason string
}

// fixtureProvider is a test-owned OpenAI-compatible endpoint. It serves
// exactly the scripted replies it was built with, records every tool-role
// message the agent sends back, and retains any request it could not serve so
// a test can assert the script matched what the agent actually did.
//
// It is deliberately hostile to improvisation: running out of replies, an
// unexpected path, and an unsupported request all answer with an error and are
// recorded, never a canned success. A test that forgets to script a turn fails
// through AssertNoUnexpectedRequests instead of passing on a fabricated reply.
type fixtureProvider struct {
	srv *httptest.Server

	// mu guards every mutable field. Handlers run on their own goroutines and
	// a test reads ToolMessages concurrently with the agent's next request.
	mu       sync.Mutex
	replies  []scriptedReply
	next     int
	toolMsgs []capturedToolMessage
	refused  []unexpectedRequest
}

// newFixtureProvider starts a fixture server for the duration of the test.
func newFixtureProvider(t *testing.T, replies []scriptedReply) *fixtureProvider {
	t.Helper()
	p := &fixtureProvider{replies: append([]scriptedReply(nil), replies...)}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(p.srv.Close)
	return p
}

// URL is the server's base URL. The child config appends "/v1".
func (p *fixtureProvider) URL() string { return p.srv.URL }

// ToolMessages returns a copy of every tool-role message received so far, in
// arrival order.
func (p *fixtureProvider) ToolMessages() []capturedToolMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedToolMessage(nil), p.toolMsgs...)
}

// unexpectedRequests returns a copy of every request the provider refused.
func (p *fixtureProvider) unexpectedRequests() []unexpectedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]unexpectedRequest(nil), p.refused...)
}

// AssertNoUnexpectedRequests fails the test for every request the script did
// not account for. A journey calls it after the turn settles: an extra
// completion, a stray endpoint or an unreadable body means the agent did
// something the test did not describe.
func (p *fixtureProvider) AssertNoUnexpectedRequests(t *testing.T) {
	t.Helper()
	for _, r := range p.unexpectedRequests() {
		t.Errorf("fixture provider received an unexpected request: %s %s (%s)", r.Method, r.Path, r.Reason)
	}
}

// serveHTTP routes the only two paths the agent is allowed to touch.
func (p *fixtureProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		writeFixtureJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []any{map[string]any{
				"id":       fixtureModel,
				"object":   "model",
				"owned_by": "usability-fixture",
			}},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		p.completion(w, r)
	default:
		p.refuse(w, r, "unexpected path")
	}
}

// completion records the request's tool-role messages and answers with the
// next scripted reply.
func (p *fixtureProvider) completion(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.refuse(w, r, "unreadable request body: "+err.Error())
		return
	}
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		p.refuse(w, r, "malformed request body: "+err.Error())
		return
	}

	p.mu.Lock()
	for _, m := range req.Messages {
		if m.Role != "tool" {
			continue
		}
		p.toolMsgs = append(p.toolMsgs, capturedToolMessage{
			CallID:  m.ToolCallID,
			Content: messageText(m.Content),
		})
	}
	if p.next >= len(p.replies) {
		p.mu.Unlock()
		p.refuse(w, r, "completion beyond the end of the scripted replies")
		return
	}
	reply := p.replies[p.next]
	turn := p.next + 1
	p.next++
	p.mu.Unlock()

	if req.Stream {
		p.writeStream(w, turn, reply)
		return
	}
	p.writeCompletion(w, turn, reply)
}

// callID is the deterministic id of the turn-th scripted call. Deriving it from
// the turn position is what lets a journey assert the agent echoed the id back
// in its tool-role message.
func callID(turn int) string { return fmt.Sprintf("call_%d", turn) }

// writeStream answers with OpenAI-compatible SSE chunks: the reply split over
// two deltas, a terminal finish_reason, and the closing sentinel.
func (p *fixtureProvider) writeStream(w http.ResponseWriter, turn int, reply scriptedReply) {
	chunks := []map[string]any{
		chunkDelta(turn, reply, false, ""),
	}
	if reply.ToolName != "" {
		args := string(reply.ToolArgs)
		if args == "" {
			args = "{}"
		}
		half := len(args) / 2
		chunks = []map[string]any{
			chunkDelta(turn, reply, true, args[:half]),
			chunkDelta(turn, reply, true, args[half:]),
		}
	}
	var b strings.Builder
	for i, c := range chunks {
		if i == len(chunks)-1 {
			// finish_reason rides the last content chunk, as the provider's
			// own fixture in internal/llm/provider does.
			c["choices"].([]any)[0].(map[string]any)["finish_reason"] = finishReason(reply)
		}
		data, err := json.Marshal(c)
		if err != nil {
			p.refuse(w, nil, "marshal SSE chunk: "+err.Error())
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

// chunkDelta builds one streaming chunk. toolCall selects a tool-call delta
// carrying the reply's call id, name and this chunk's slice of arguments.
func chunkDelta(turn int, reply scriptedReply, toolCall bool, argsPart string) map[string]any {
	delta := map[string]any{"role": "assistant", "content": reply.Text}
	if toolCall {
		delta["content"] = ""
		delta["tool_calls"] = []any{map[string]any{
			"index": 0,
			"id":    callID(turn),
			"type":  "function",
			"function": map[string]any{
				"name":      reply.ToolName,
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

func finishReason(reply scriptedReply) string {
	if reply.ToolName != "" {
		return "tool_calls"
	}
	return "stop"
}

// writeCompletion answers a non-streaming request with the JSON equivalent of
// the same reply.
func (p *fixtureProvider) writeCompletion(w http.ResponseWriter, turn int, reply scriptedReply) {
	message := map[string]any{"role": "assistant", "content": reply.Text}
	if reply.ToolName != "" {
		args := string(reply.ToolArgs)
		if args == "" {
			args = "{}"
		}
		message["content"] = ""
		message["tool_calls"] = []any{map[string]any{
			"id":   callID(turn),
			"type": "function",
			"function": map[string]any{
				"name":      reply.ToolName,
				"arguments": args,
			},
		}}
	}
	writeFixtureJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", turn),
		"object":  "chat.completion",
		"created": 0,
		"model":   fixtureModel,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason(reply),
		}},
	})
}

// refuse records the request and answers with an error, so an unscripted
// request cannot be mistaken for a successful turn.
func (p *fixtureProvider) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	method, path := "", ""
	if r != nil {
		method, path = r.Method, r.URL.Path
	}
	p.mu.Lock()
	p.refused = append(p.refused, unexpectedRequest{Method: method, Path: path, Reason: reason})
	p.mu.Unlock()

	if w == nil {
		return
	}
	writeFixtureJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"message": "usability fixture provider: " + reason,
			"type":    "fixture_error",
		},
	})
}

func writeFixtureJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "fixture provider: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// messageText decodes a message's content. Marshal sends a JSON string, but a
// non-string payload is reported verbatim rather than dropped: a test that
// asserts on tool content must not silently see "".
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// postChat sends one chat completion request. The caller owns the response
// body.
func postChat(t *testing.T, url string, stream bool, messages []map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    fixtureModel,
		"stream":   stream,
		"messages": messages,
	})
	if err != nil {
		t.Fatalf("marshal chat request: %v", err)
	}
	resp, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("post chat completion: %v", err)
	}
	return resp
}

// readSSEData returns the data payload of every SSE event in body, in order.
func readSSEData(t *testing.T, body []byte) []string {
	t.Helper()
	var out []string
	for _, event := range strings.Split(string(body), "\n\n") {
		event = strings.TrimSpace(event)
		if event == "" {
			continue
		}
		data, ok := strings.CutPrefix(event, "data: ")
		if !ok {
			t.Fatalf("SSE event is not a data line: %q", event)
		}
		out = append(out, data)
	}
	return out
}

// TestFixtureProviderProtocol drives the fixture directly. It proves the wire
// shape both ways, that the script's replies are what the agent would receive,
// and that a tool-role message is recorded before the next reply is served.
func TestFixtureProviderProtocol(t *testing.T) {
	t.Run("streaming_tool_call_then_text", func(t *testing.T) {
		args := json.RawMessage(`{"command":"echo fixture"}`)
		p := newFixtureProvider(t, []scriptedReply{
			{ToolName: "shell.run", ToolArgs: args},
			{Text: "finished"},
		})

		resp := postChat(t, p.URL(), true, []map[string]any{
			{"role": "user", "content": "run the command"},
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("streaming status = %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("streaming Content-Type = %q, want text/event-stream", ct)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read streaming body: %v", err)
		}
		events := readSSEData(t, body)
		if len(events) != 3 {
			t.Fatalf("streaming produced %d SSE events, want 3 (two chunks + [DONE]): %q", len(events), events)
		}
		if last := events[len(events)-1]; last != "[DONE]" {
			t.Fatalf("last SSE event = %q, want [DONE]", last)
		}

		// The two content chunks are valid JSON and reassemble the arguments.
		var gotArgs strings.Builder
		for i, raw := range events[:len(events)-1] {
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
				t.Fatalf("SSE chunk %d is not valid JSON: %v (%q)", i, err, raw)
			}
			if len(chunk.Choices) != 1 {
				t.Fatalf("SSE chunk %d carries %d choices, want 1", i, len(chunk.Choices))
			}
			for _, call := range chunk.Choices[0].Delta.ToolCalls {
				if call.ID != "call_1" {
					t.Fatalf("chunk %d tool call id = %q, want call_1", i, call.ID)
				}
				if call.Type != "function" {
					t.Fatalf("chunk %d tool call type = %q, want function", i, call.Type)
				}
				if call.Function.Name != "shell.run" {
					t.Fatalf("chunk %d tool name = %q, want shell.run", i, call.Function.Name)
				}
				gotArgs.WriteString(call.Function.Arguments)
			}
			if want, got := "tool_calls", chunk.Choices[0].FinishReason; i == len(events)-2 && got != want {
				t.Fatalf("last content chunk finish_reason = %q, want %q", got, want)
			}
		}
		if got, want := gotArgs.String(), string(args); got != want {
			t.Fatalf("reassembled arguments = %q, want %q", got, want)
		}

		// Nothing is recorded until the agent answers the call.
		if msgs := p.ToolMessages(); len(msgs) != 0 {
			t.Fatalf("recorded %d tool messages before any tool result was sent, want 0", len(msgs))
		}

		// The agent's tool result is recorded before the next reply is served.
		resp = postChat(t, p.URL(), true, []map[string]any{
			{"role": "user", "content": "run the command"},
			{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": "shell.run", "arguments": string(args)},
			}}},
			{"role": "tool", "content": "Tool shell.run result: ok", "tool_call_id": "call_1"},
		})
		defer resp.Body.Close()
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read second streaming body: %v", err)
		}
		events = readSSEData(t, body)
		if last := events[len(events)-1]; last != "[DONE]" {
			t.Fatalf("second stream last event = %q, want [DONE]", last)
		}
		if !strings.Contains(events[0], `"finished"`) {
			t.Fatalf("second stream did not carry the scripted text: %q", events[0])
		}

		msgs := p.ToolMessages()
		if len(msgs) != 1 {
			t.Fatalf("recorded %d tool messages, want 1: %+v", len(msgs), msgs)
		}
		if msgs[0].CallID != "call_1" {
			t.Fatalf("recorded tool message call id = %q, want call_1", msgs[0].CallID)
		}
		if want := "Tool shell.run result: ok"; msgs[0].Content != want {
			t.Fatalf("recorded tool content = %q, want %q", msgs[0].Content, want)
		}
		p.AssertNoUnexpectedRequests(t)
	})

	t.Run("non_streaming_tool_call", func(t *testing.T) {
		args := json.RawMessage(`{"questions":[{"question":"Name?"}]}`)
		p := newFixtureProvider(t, []scriptedReply{{ToolName: "question.ask", ToolArgs: args}})

		resp := postChat(t, p.URL(), false, []map[string]any{
			{"role": "user", "content": "ask me"},
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("non-streaming status = %d, want 200", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read non-streaming body: %v", err)
		}
		var completion struct {
			Choices []struct {
				Message struct {
					Role      string `json:"role"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &completion); err != nil {
			t.Fatalf("non-streaming body is not valid JSON: %v (%q)", err, body)
		}
		if len(completion.Choices) != 1 {
			t.Fatalf("non-streaming body carries %d choices, want 1", len(completion.Choices))
		}
		choice := completion.Choices[0]
		if choice.FinishReason != "tool_calls" {
			t.Fatalf("finish_reason = %q, want tool_calls", choice.FinishReason)
		}
		if got := len(choice.Message.ToolCalls); got != 1 {
			t.Fatalf("message carries %d tool calls, want 1", got)
		}
		call := choice.Message.ToolCalls[0]
		if call.ID != "call_1" || call.Type != "function" || call.Function.Name != "question.ask" {
			t.Fatalf("tool call = %+v, want call_1/function/question.ask", call)
		}
		if call.Function.Arguments != string(args) {
			t.Fatalf("arguments = %q, want %q", call.Function.Arguments, string(args))
		}
		p.AssertNoUnexpectedRequests(t)
	})

	t.Run("models", func(t *testing.T) {
		p := newFixtureProvider(t, nil)
		resp, err := http.Get(p.URL() + "/v1/models")
		if err != nil {
			t.Fatalf("get models: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("models status = %d, want 200", resp.StatusCode)
		}
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read models body: %v", err)
		}
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("models body is not valid JSON: %v", err)
		}
		if len(list.Data) != 1 || list.Data[0].ID != fixtureModel {
			t.Fatalf("models = %+v, want exactly %s", list.Data, fixtureModel)
		}
		p.AssertNoUnexpectedRequests(t)
	})

	t.Run("unexpected_path_and_exhausted_script_are_recorded", func(t *testing.T) {
		p := newFixtureProvider(t, []scriptedReply{{Text: "only reply"}})

		resp, err := http.Get(p.URL() + "/v1/embeddings")
		if err != nil {
			t.Fatalf("get unexpected path: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("unexpected path answered %d, want an error", resp.StatusCode)
		}

		// The scripted reply is served once.
		first := postChat(t, p.URL(), true, []map[string]any{{"role": "user", "content": "hi"}})
		first.Body.Close()
		if first.StatusCode != http.StatusOK {
			t.Fatalf("scripted completion status = %d, want 200", first.StatusCode)
		}
		// A second completion is beyond the script and must fail.
		second := postChat(t, p.URL(), true, []map[string]any{{"role": "user", "content": "hi"}})
		second.Body.Close()
		if second.StatusCode == http.StatusOK {
			t.Fatalf("completion beyond the script answered %d, want an error", second.StatusCode)
		}

		refused := p.unexpectedRequests()
		if len(refused) != 2 {
			t.Fatalf("refused %d requests, want 2 (unexpected path + exhausted script): %+v", len(refused), refused)
		}
		if refused[0].Path != "/v1/embeddings" {
			t.Fatalf("first refused request path = %q, want /v1/embeddings", refused[0].Path)
		}
		if !strings.Contains(refused[1].Reason, "beyond the end of the scripted replies") {
			t.Fatalf("second refused reason = %q, want script exhaustion", refused[1].Reason)
		}
	})
}

// fixtureHelperEnv marks the re-executed child of
// TestFixtureProviderUnexpectedRequestsFail.
const fixtureHelperEnv = "USABILITY_FIXTURE_HELPER"

// TestFixtureProviderUnexpectedRequestsFail proves AssertNoUnexpectedRequests
// really fails the test it is handed, rather than logging and passing. Go has
// no expect-failure, so the assertion runs in a child process and the parent
// asserts a nonzero exit — the same shape as TestRequireBinarySubprocess.
func TestFixtureProviderUnexpectedRequestsFail(t *testing.T) {
	if os.Getenv(fixtureHelperEnv) == "1" {
		p := newFixtureProvider(t, []scriptedReply{{Text: "only reply"}})
		resp, err := http.Get(p.URL() + "/v1/embeddings")
		if err != nil {
			t.Fatalf("get unexpected path: %v", err)
		}
		resp.Body.Close()
		// The assertion below must fail the child; returning normally would
		// make the parent's nonzero-exit expectation fail instead.
		p.AssertNoUnexpectedRequests(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestFixtureProviderUnexpectedRequestsFail$")
	cmd.Env = append(os.Environ(), fixtureHelperEnv+"=1")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err == nil {
		t.Fatalf("child exited 0: AssertNoUnexpectedRequests did not fail on a refused request")
	}
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("child exit = %v, want a nonzero *exec.ExitError", err)
	}
}

// TestFixtureProviderConcurrentRecords drives the server from several
// goroutines while readers take snapshots. Run under -race: the recorded tool
// messages must be complete and consistent, never a torn slice.
func TestFixtureProviderConcurrentRecords(t *testing.T) {
	const workers = 8
	replies := make([]scriptedReply, workers)
	for i := range replies {
		replies[i] = scriptedReply{Text: fmt.Sprintf("reply %d", i)}
	}
	p := newFixtureProvider(t, replies)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	// Readers run concurrently with the writers: a snapshot taken mid-write
	// must still be a valid slice of already-recorded messages.
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				for _, m := range p.ToolMessages() {
					if m.CallID == "" {
						t.Errorf("recorded tool message has no call id: %+v", m)
						return
					}
				}
				_ = p.unexpectedRequests()
			}
		}
	}()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			messages := []map[string]any{
				{"role": "user", "content": "go"},
				{"role": "tool", "content": fmt.Sprintf("result %d", i), "tool_call_id": fmt.Sprintf("call_%d", i)},
			}
			body, err := json.Marshal(map[string]any{
				"model": fixtureModel, "stream": true, "messages": messages,
			})
			if err != nil {
				t.Errorf("marshal concurrent request: %v", err)
				return
			}
			resp, err := http.Post(p.URL()+"/v1/chat/completions", "application/json", strings.NewReader(string(body)))
			if err != nil {
				t.Errorf("concurrent chat request: %v", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("concurrent request status = %d, want 200", resp.StatusCode)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
		}(i)
	}
	wg.Wait()
	close(stop)
	readers.Wait()

	msgs := p.ToolMessages()
	if len(msgs) != workers {
		t.Fatalf("recorded %d tool messages, want %d", len(msgs), workers)
	}
	seen := make(map[string]string, workers)
	for _, m := range msgs {
		if _, dup := seen[m.CallID]; dup {
			t.Fatalf("call id %q recorded twice: %+v", m.CallID, msgs)
		}
		seen[m.CallID] = m.Content
	}
	p.AssertNoUnexpectedRequests(t)
}
