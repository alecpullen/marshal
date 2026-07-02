# Milestone C: Provider Abstraction — Execution Plan

## Context

Marshal is currently Milestones A–B complete (skeleton + Bubble Tea TUI shell with placeholder panels). Nothing in the codebase talks to an LLM yet. Milestone C introduces the provider abstraction layer described in `docs/03-provider-and-model-routing.md`: a `Provider` interface, the `ChatRequest`/`ChatMessage`/`ChatEvent` wire-agnostic types, a generic OpenAI-compatible implementation with streaming support, TOML config for `[providers.*]`, tests against real local Ollama/LM Studio servers, and a slot in the TUI to surface provider errors.

This is intentionally a narrow slice: it builds the *provider layer* only. It does not wire the TUI's Enter key to an actual live chat call (confirmed with user) — that belongs to Milestone H (agent loop), which will consume this layer. It does not touch tool-calling (Milestone D) or model routing/presets (Milestone L).

## Global Constraints

- Go 1.26.1, module `marshal`. Stdlib only for HTTP/streaming (`net/http`, `bufio`, `encoding/json`, `context`, `io`, `os`) — no new third-party dependencies.
- Follow existing repo conventions exactly:
  - Tests: plain `testing` package, table/closure style, `t.Fatalf`, `t.TempDir()`/`t.Setenv()` for isolation, no mocking framework, `_test.go` colocated with the code.
  - Config: the pointer-based `configFile` + `merge()` pattern in `internal/app/config/config.go` — scalar fields use pointers to distinguish "unset" from zero value; maps do not need pointer-wrapping (nil already means absent).
  - session/tui: mutex-guarded mutation in `session.State`, simple `fmt.Fprintf` labeled-section rendering in `tui/model.go`'s `View()`.
- `Provider` interface method signatures must match `docs/03-provider-and-model-routing.md` verbatim:
  ```go
  type Provider interface {
      Name() string
      Models(ctx context.Context) ([]schema.ModelInfo, error)
      Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error)
      Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error)
      Capabilities(ctx context.Context) schema.ProviderCapabilities
  }
  ```
- `ProviderCapabilities` fields verbatim from the doc: `Streaming, ToolCalling, JSONMode, StructuredOutput, Embeddings, Vision, ReasoningTokens bool`, `ContextWindow, MaxOutputTokens int`.
- Package layout (per `docs/02-system-architecture.md`): `internal/llm/schema/`, `internal/llm/provider/`, `internal/llm/streaming/`. Do NOT create `internal/llm/toolcalling/` (out of scope).
- Do NOT modify `internal/app/app.go` — no "active provider" concept exists yet (Milestone L's job); nothing in the current request path calls a provider.
- Do NOT wire the TUI's Enter-key handler to an actual provider call — Milestone H's job. This milestone only adds a place for a provider error to be *displayed*.
- Do NOT implement model presets, agent profiles, or `[routing.*]` config (Milestone L). Only `[providers.*]` config is in scope.
- `session.State` must not import `internal/llm/*` — keep `providerErr` as a plain `error` field to preserve the existing package boundary.
- Every task must leave `go build ./...`, `go vet ./...`, and `go test ./...` passing (excluding the `integration` build tag, which requires local servers).

## Task 1: `internal/llm/schema` package

Create a new package `internal/llm/schema` with these exact files and contents.

`internal/llm/schema/chat.go`:
```go
package schema

// Role identifies the speaker of a ChatMessage in the wire protocol sense.
// This is intentionally a separate type from session.Role: session.Role is a
// TUI/transcript concern, schema.Role is an LLM wire-protocol concern.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool is reserved for Milestone D tool-calling; not emitted or
	// consumed by anything in this milestone.
	RoleTool Role = "tool"
)

type ChatMessage struct {
	Role    Role
	Content string
}

// ChatRequest is the provider-agnostic chat request shape. Pointer fields
// (Temperature, TopP, MaxTokens) distinguish "unset" from "zero" so the
// provider can omit them from the wire request rather than sending
// temperature=0 unintentionally.
type ChatRequest struct {
	Model       string
	Messages    []ChatMessage
	Stream      bool
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	Stop        []string
}
```

`internal/llm/schema/event.go`:
```go
package schema

// ChatEventType discriminates the three shapes a ChatEvent can take. Both
// streaming (SSE) and non-streaming (single JSON body) provider responses
// are normalized into the same event stream: non-streaming responses are
// synthesized as exactly one Delta (the full content) followed by one Done.
type ChatEventType string

const (
	ChatEventDelta ChatEventType = "delta"
	ChatEventDone  ChatEventType = "done"
	ChatEventError ChatEventType = "error"
)

type ChatEvent struct {
	Type ChatEventType

	// Delta holds incremental (or, for non-streaming, complete) assistant
	// text content. Populated only when Type == ChatEventDelta.
	Delta string

	// FinishReason mirrors the upstream `finish_reason` field ("stop",
	// "length", etc.) when known. Populated only when Type == ChatEventDone.
	FinishReason string

	// Err is populated only when Type == ChatEventError. The channel is
	// always closed immediately after an error event.
	Err error
}
```

`internal/llm/schema/model.go`:
```go
package schema

// ModelInfo describes a model as reported by a provider's model-listing
// endpoint. Most OpenAI-compatible /v1/models endpoints (Ollama, LM Studio)
// return only id/owned_by — context window and output limits are not
// reliably available here, so they are omitted rather than guessed.
type ModelInfo struct {
	ID      string
	OwnedBy string
}
```

`internal/llm/schema/embed.go`:
```go
package schema

type EmbedRequest struct {
	Model string
	Input []string
}

type EmbedResponse struct {
	Model      string
	Embeddings [][]float64
}
```

`internal/llm/schema/capabilities.go`:
```go
package schema

type ProviderCapabilities struct {
	Streaming        bool
	ToolCalling      bool
	JSONMode         bool
	StructuredOutput bool
	Embeddings       bool
	Vision           bool
	ReasoningTokens  bool
	ContextWindow    int
	MaxOutputTokens  int
}
```

No test file is required for this task — these are plain type/const declarations with no behavior to test. Verify with `go build ./...` and `go vet ./...`.

Report: list the files created, confirm `go build ./...` and `go vet ./...` pass.

## Task 2: `internal/llm/streaming` package (generic SSE decoder)

Create `internal/llm/streaming/sse.go`. This package has zero knowledge of JSON, `[DONE]` sentinels, or chat-completions shapes — it only understands the SSE line grammar. That interpretation happens later in `internal/llm/provider`.

```go
package streaming

import (
	"bufio"
	"io"
	"strings"
)

// Event is one parsed SSE event. Data joins multiple `data:` lines (per the
// SSE spec, consecutive data: lines within one event are newline-joined).
type Event struct {
	ID    string
	Event string
	Data  string
}

// Decoder reads Server-Sent Events from r. It knows nothing about the
// payload format inside Data — callers decide how to interpret it.
type Decoder struct {
	scanner *bufio.Scanner
	event   Event
	err     error
}

func NewDecoder(r io.Reader) *Decoder {
	scanner := bufio.NewScanner(r)
	// Some providers emit large single-line chunks; raise the buffer above
	// bufio's 64KB default line limit.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	return &Decoder{scanner: scanner}
}

// Next advances to the next event. Returns false at EOF or on read error;
// check Err() to distinguish the two.
func (d *Decoder) Next() bool {
	d.event = Event{}
	var dataLines []string
	sawAny := false

	for d.scanner.Scan() {
		line := d.scanner.Text()
		if line == "" {
			if sawAny {
				d.event.Data = strings.Join(dataLines, "\n")
				return true
			}
			continue
		}
		sawAny = true
		switch {
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "event:"):
			d.event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "id:"):
			d.event.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, ":"):
			// comment/heartbeat line — ignore
		}
	}

	if err := d.scanner.Err(); err != nil {
		d.err = err
		return false
	}
	if sawAny {
		d.event.Data = strings.Join(dataLines, "\n")
		return true
	}
	return false
}

func (d *Decoder) Event() Event { return d.event }
func (d *Decoder) Err() error   { return d.err }
```

Write `internal/llm/streaming/sse_test.go` with table/unit tests covering:
- Single event with one `data:` line parses to `Event{Data: "hello"}`.
- Multiple consecutive `data:` lines within one event join with `\n`.
- `event:` and `id:` fields are parsed.
- Lines starting with `:` (comments/heartbeats) are ignored and don't appear in Data.
- Clean EOF (input ends after a valid blank-line-terminated event, or with no trailing event): `Next()` eventually returns false with `Err() == nil`.
- A reader that returns a non-EOF error produces `Next() == false` and `Err() != nil`.
- A single `data:` line larger than 64KB (e.g. 100KB) still parses correctly without a `bufio.ErrTooLong` failure.

Use `strings.NewReader` for normal cases and a custom `io.Reader` (or an `io.Reader` wrapping a pipe you close with an error) for the read-error case.

Report: files created, test names, and the `go test ./internal/llm/streaming/...` output showing all tests passing.

## Task 3: Provider config in `internal/app/config`

Modify `internal/app/config/config.go`. Read the current file first — it has `Config`, `ProjectConfig`, `CommandsConfig`, `ProfileConfig`, `PrivacyConfig`, `IndexingConfig`, `LoadOptions`, `configFile`, `Default()`, `Load()`, `loadFile()`, `merge()`.

1. Add a new field to `Config`:
   ```go
   Providers map[string]ProviderConfig `toml:"providers"`
   ```

2. Add a new type:
   ```go
   // ProviderConfig is one [providers.<name>] entry. Only the fields needed
   // for the generic OpenAI-compatible provider are present.
   type ProviderConfig struct {
       Type      string `toml:"type"`        // "openai_compatible" is the only supported value in this milestone
       BaseURL   string `toml:"base_url"`
       APIKey    string `toml:"api_key"`     // literal key; wins over APIKeyEnv if both set
       APIKeyEnv string `toml:"api_key_env"` // env var name to resolve at provider-construction time (NOT resolved here)
   }
   ```

3. Add to the `configFile` struct (unexported, used only during loading):
   ```go
   Providers map[string]ProviderConfig `toml:"providers"`
   ```
   Note: unlike the other `configFile` fields, this is NOT a pointer-to-anonymous-struct — a nil map already distinguishes "providers section absent from this file" from "present", so no pointer wrapping is needed.

4. In `merge(cfg *Config, file configFile)`, append (after the existing `Indexing` block):
   ```go
   if file.Providers != nil {
       if cfg.Providers == nil {
           cfg.Providers = make(map[string]ProviderConfig, len(file.Providers))
       }
       // Whole-entry overwrite by key: a provider name defined in both the
       // global and project file is fully replaced by the project file's
       // entry (not deep-merged field-by-field), mirroring how a later file
       // "wins" for scalar fields elsewhere in this function.
       for name, pc := range file.Providers {
           cfg.Providers[name] = pc
       }
   }
   ```

5. `Default()` must NOT populate `Providers` — leave it as the zero value (`nil`). Add a one-line comment in the returned struct literal explaining why (no built-in provider assumptions; Marshal is local-first and provider URLs are user-specific — see `docs/09-configuration-examples.md`).

Add tests to `internal/app/config/config_test.go` (existing file — read it first for style: it uses `t.TempDir()`, a `writeFile` helper, `reflect.DeepEqual`, `t.Fatalf`):
- `TestDefaultConfigHasNoProviders` — `Default().Providers` is nil or empty.
- `TestLoadParsesProvidersBlock` — write one config file (project-local is fine) with two `[providers.*]` entries, e.g. `[providers.ollama]` (`type`, `base_url`, `api_key`) and `[providers.openrouter]` (`type`, `base_url`, `api_key_env`); assert both parse into `cfg.Providers` with exact field values, and specifically that `APIKeyEnv` is stored verbatim as the string `"OPENROUTER_API_KEY"` (config package must NOT resolve environment variables itself — no `os.Getenv` call anywhere in this file).
- `TestLoadProjectProvidersOverwriteGlobalProvidersByKey` — global config file defines `[providers.ollama]` and `[providers.lmstudio]`; project config file redefines `[providers.ollama]` with a different `base_url` and adds `[providers.openrouter]`; assert the merged config has exactly 3 provider entries, `ollama`'s fields match the *project* file's values (full replacement, not a merge of individual fields), and `lmstudio` is untouched from the global file.

Report: exact diff summary of `config.go`, new test names, and `go test ./internal/app/config/...` output showing all tests (existing + new) passing.

## Task 4: `internal/llm/provider` — interface, errors, and the OpenAI-compatible implementation

This task depends on Task 1 (`internal/llm/schema`) and Task 2 (`internal/llm/streaming`), which are already complete and importable at `marshal/internal/llm/schema` and `marshal/internal/llm/streaming`.

Create four files in a new package `internal/llm/provider`:

`internal/llm/provider/provider.go`:
```go
package provider

import (
	"context"

	"marshal/internal/llm/schema"
)

// Provider is implemented by every LLM backend Marshal can talk to. Method
// signatures match docs/03-provider-and-model-routing.md exactly.
type Provider interface {
	Name() string
	Models(ctx context.Context) ([]schema.ModelInfo, error)
	Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error)
	Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error)
	Capabilities(ctx context.Context) schema.ProviderCapabilities
}
```

`internal/llm/provider/errors.go`:
```go
package provider

import "fmt"

// ProviderError wraps a non-2xx HTTP response from a provider so callers can
// use errors.As to inspect the status code instead of parsing error strings.
type ProviderError struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider %q returned HTTP %d: %s", e.Provider, e.StatusCode, e.Body)
}
```

`internal/llm/provider/openai_compatible_wire.go` (unexported JSON DTOs — this is the only file that knows the OpenAI-specific JSON shape):
```go
package provider

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type chatMessageBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequestBody struct {
	Model       string            `json:"model"`
	Messages    []chatMessageBody `json:"messages"`
	Stream      bool              `json:"stream"`
	Temperature *float64          `json:"temperature,omitempty"`
	TopP        *float64          `json:"top_p,omitempty"`
	MaxTokens   *int              `json:"max_tokens,omitempty"`
	Stop        []string          `json:"stop,omitempty"`
}

// chatCompletionChunk is a single SSE `data:` payload for streaming
// responses: choices[0].delta.content.
type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError `json:"error"`
}

// chatCompletionResponse is the full non-streaming response body:
// choices[0].message.content.
type chatCompletionResponse struct {
	Choices []struct {
		Message      chatMessageBody `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError `json:"error"`
}

type modelsResponseBody struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

type embedRequestBody struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponseBody struct {
	Model string `json:"model"`
	Data  []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *apiError `json:"error"`
}
```

`internal/llm/provider/openai_compatible.go`:
```go
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"marshal/internal/llm/schema"
	"marshal/internal/llm/streaming"
)

type Options struct {
	Name       string
	BaseURL    string
	APIKey     string // already resolved — see factory.go (Task 5)
	HTTPClient *http.Client
	// Capabilities overrides the default capability set. Leave nil to use
	// defaultCapabilities().
	Capabilities *schema.ProviderCapabilities
}

type OpenAICompatible struct {
	name         string
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	capabilities schema.ProviderCapabilities
}

func NewOpenAICompatible(opts Options) (*OpenAICompatible, error) {
	if opts.Name == "" {
		return nil, errors.New("provider: name is required")
	}
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("provider %q: base_url is required", opts.Name)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	caps := defaultCapabilities()
	if opts.Capabilities != nil {
		caps = *opts.Capabilities
	}
	return &OpenAICompatible{
		name:         opts.Name,
		baseURL:      strings.TrimRight(opts.BaseURL, "/"),
		apiKey:       opts.APIKey,
		httpClient:   client,
		capabilities: caps,
	}, nil
}

func defaultCapabilities() schema.ProviderCapabilities {
	return schema.ProviderCapabilities{
		Streaming:   true,
		Embeddings:  true,
		ToolCalling: false,
	}
}

func (p *OpenAICompatible) Name() string { return p.name }

func (p *OpenAICompatible) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.capabilities
}

func (p *OpenAICompatible) setHeaders(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func (p *OpenAICompatible) httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &ProviderError{Provider: p.name, StatusCode: resp.StatusCode, Body: string(body)}
}

// Models lists available models via GET {base_url}/models.
func (p *OpenAICompatible) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("provider %q: build models request: %w", p.name, err)
	}
	p.setHeaders(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q: models request failed: %w", p.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, p.httpError(resp)
	}

	var parsed modelsResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("provider %q: decode models response: %w", p.name, err)
	}
	models := make([]schema.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		models = append(models, schema.ModelInfo{ID: m.ID, OwnedBy: m.OwnedBy})
	}
	return models, nil
}

// Chat sends a chat completion request. req.Stream controls the wire format:
// callers explicitly opt into SSE streaming; Chat mechanically supports both
// and normalizes the result into the same <-chan schema.ChatEvent shape
// either way.
//
// HTTP-level failures (connection errors, non-2xx status) are returned
// synchronously as the second return value. Once the channel is handed
// back, all further failures (malformed SSE payload, an embedded
// {"error":...} object, a dropped connection, or context cancellation) are
// delivered as a single ChatEventError event, after which the channel is
// closed.
func (p *OpenAICompatible) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	body, err := buildChatRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider %q: build chat request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider %q: chat request failed: %w", p.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, p.httpError(resp)
	}

	events := make(chan schema.ChatEvent)
	if req.Stream {
		go streamChatEvents(resp.Body, events)
	} else {
		go readChatResponse(resp.Body, events)
	}
	return events, nil
}

func buildChatRequestBody(req schema.ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("chat request: model is required")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("chat request: at least one message is required")
	}
	messages := make([]chatMessageBody, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, chatMessageBody{Role: string(m.Role), Content: m.Content})
	}
	return json.Marshal(chatCompletionRequestBody{
		Model:       req.Model,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
	})
}

func streamChatEvents(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	dec := streaming.NewDecoder(body)
	for dec.Next() {
		data := strings.TrimSpace(dec.Event().Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			events <- schema.ChatEvent{Type: schema.ChatEventDone}
			return
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode stream chunk: %w", err)}
			return
		}
		if chunk.Error != nil {
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(chunk.Error.Message)}
			return
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: choice.Delta.Content}
		}
		if choice.FinishReason != "" {
			events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: choice.FinishReason}
			return
		}
	}
	if err := dec.Err(); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("read stream: %w", err)}
		return
	}
	// Stream ended cleanly without an explicit [DONE] or finish_reason
	// (some servers omit both) — still signal completion.
	events <- schema.ChatEvent{Type: schema.ChatEventDone}
}

func readChatResponse(body io.ReadCloser, events chan<- schema.ChatEvent) {
	defer close(events)
	defer body.Close()

	var parsed chatCompletionResponse
	if err := json.NewDecoder(body).Decode(&parsed); err != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: fmt.Errorf("decode chat response: %w", err)}
		return
	}
	if parsed.Error != nil {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New(parsed.Error.Message)}
		return
	}
	if len(parsed.Choices) == 0 {
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: errors.New("chat response contained no choices")}
		return
	}
	choice := parsed.Choices[0]
	if choice.Message.Content != "" {
		events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: choice.Message.Content}
	}
	events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: choice.FinishReason}
}

// Embed requests embeddings via POST {base_url}/embeddings.
func (p *OpenAICompatible) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	body, err := json.Marshal(embedRequestBody{Model: req.Model, Input: req.Input})
	if err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: encode embed request: %w", p.name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: build embed request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: embed request failed: %w", p.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return schema.EmbedResponse{}, p.httpError(resp)
	}

	var parsed embedResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return schema.EmbedResponse{}, fmt.Errorf("provider %q: decode embed response: %w", p.name, err)
	}
	if parsed.Error != nil {
		return schema.EmbedResponse{}, errors.New(parsed.Error.Message)
	}
	embeddings := make([][]float64, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		embeddings = append(embeddings, d.Embedding)
	}
	return schema.EmbedResponse{Model: parsed.Model, Embeddings: embeddings}, nil
}
```

Write `internal/llm/provider/openai_compatible_test.go` using `net/http/httptest.NewServer` (no external services). Cover:
- Streaming: server writes several `data: {"choices":[{"delta":{"content":"..."}}]}\n\n` chunks then `data: [DONE]\n\n`; assert ordered Delta events with correct content, and a final Done event.
- Streaming ending on `finish_reason` without a `[DONE]` sentinel (connection closes after the finish_reason chunk); assert Done event carries `FinishReason: "stop"`.
- Streaming ending cleanly with neither `[DONE]` nor `finish_reason` (connection just closes after a delta); assert a Done event is still synthesized.
- Non-streaming (`req.Stream = false`): server returns `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`; assert exactly one Delta("hi") event then one Done(FinishReason:"stop") event.
- Streaming: malformed JSON in a `data:` line (e.g. `data: {not json}\n\n`) produces a ChatEventError and the channel closes (no further events, no panic, no goroutine leak — use a timeout/select when reading from the channel in the test).
- Streaming: a chunk with an embedded `{"error":{"message":"boom"}}` produces a ChatEventError whose `Err.Error()` contains "boom".
- Non-streaming: `{"choices":[]}` produces a ChatEventError.
- `Chat()` itself (not the channel) returns an error when the server responds with HTTP 500 plus a body — assert this is synchronous (returned before any channel), and that `errors.As(err, &providerErr)` succeeds with `providerErr.StatusCode == 500`.
- The `Authorization: Bearer <key>` header is present when `APIKey` is set (assert via the httptest handler inspecting `r.Header`), and absent entirely when `APIKey` is `""` (the local Ollama/LM Studio no-auth case).
- `Models()` parses `{"data":[{"id":"llama3","owned_by":"local"}]}` into `[]schema.ModelInfo`, and returns a `*ProviderError` on non-200.
- `Embed()` parses `{"data":[{"embedding":[0.1,0.2]}]}` into `EmbedResponse.Embeddings`, and returns a `*ProviderError` on non-200.

Report: files created, full list of test names, `go test ./internal/llm/provider/...` output (all passing), `go vet ./...` output.

## Task 5: `internal/llm/provider/factory.go` — config-driven construction

This task depends on Task 3 (`config.ProviderConfig` exists at `marshal/internal/app/config`) and Task 4 (`provider.Provider`, `provider.NewOpenAICompatible`, `provider.Options` exist), both already complete.

Create `internal/llm/provider/factory.go`. This is the ONLY file in the `internal/llm/provider` package that imports `marshal/internal/app/config` — keeping the rest of the package (HTTP/streaming logic) testable without any dependency on the config layer.

```go
package provider

import (
	"fmt"
	"os"

	"marshal/internal/app/config"
)

// NewFromConfig builds a Provider from a single [providers.<name>] entry.
// API key resolution (api_key literal vs api_key_env lookup) happens here,
// once, at construction time — OpenAICompatible itself only ever sees an
// already-resolved bearer token string, never an env var name. If both
// api_key and api_key_env are set, the literal api_key wins.
func NewFromConfig(name string, pc config.ProviderConfig) (Provider, error) {
	switch pc.Type {
	case "", "openai_compatible":
		apiKey, err := resolveAPIKey(pc)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		return NewOpenAICompatible(Options{
			Name:    name,
			BaseURL: pc.BaseURL,
			APIKey:  apiKey,
		})
	default:
		return nil, fmt.Errorf("provider %q: unsupported type %q", name, pc.Type)
	}
}

func resolveAPIKey(pc config.ProviderConfig) (string, error) {
	if pc.APIKey != "" {
		return pc.APIKey, nil
	}
	if pc.APIKeyEnv != "" {
		v, ok := os.LookupEnv(pc.APIKeyEnv)
		if !ok || v == "" {
			return "", fmt.Errorf("environment variable %q (from api_key_env) is not set", pc.APIKeyEnv)
		}
		return v, nil
	}
	return "", nil // no auth — normal for local Ollama/LM Studio
}
```

Write `internal/llm/provider/factory_test.go`:
- `TestNewFromConfigUsesLiteralAPIKey` — `pc.APIKey` set, `pc.APIKeyEnv` empty; construct, then make a real request against an `httptest.Server` and assert the `Authorization` header carries the literal key (don't just check an unexported field — verify via the actual HTTP round trip, since `resolveAPIKey` is unexported).
- `TestNewFromConfigResolvesAPIKeyEnv` — use `t.Setenv("SOME_ENV_VAR", "resolved-value")`, `pc.APIKeyEnv = "SOME_ENV_VAR"`, `pc.APIKey` empty; assert the resolved value reaches the `Authorization` header via an httptest round trip.
- `TestNewFromConfigPrefersLiteralAPIKeyOverAPIKeyEnv` — both set; assert the literal wins.
- `TestNewFromConfigErrorsWhenAPIKeyEnvUnset` — `pc.APIKeyEnv` points at an env var that is not set; assert `NewFromConfig` returns a descriptive error (no panic).
- `TestNewFromConfigErrorsOnUnsupportedProviderType` — `pc.Type = "native_anthropic"` (or similar); assert an error mentioning the unsupported type.

Report: files created, test names, `go test ./internal/llm/provider/...` output (all tests from Task 4 and Task 5 passing together), `go vet ./...` output.

## Task 6: `session.State` — assistant role and provider-error slot

Modify `internal/app/session/session.go`. Read the current file first.

1. Add a new role constant alongside the existing `RoleSystem`/`RoleUser`:
   ```go
   RoleAssistant Role = "assistant"
   ```

2. Add a mutex-guarded `providerErr error` field to `State`, alongside the existing `messages []Message` field (same `mu sync.Mutex` already guards it — do not add a second mutex).

3. Add two methods:
   ```go
   // SetProviderError records the most recent provider-level failure (HTTP
   // error, malformed response, connection failure, etc.) for display in the
   // TUI. Passing nil clears it — callers should clear on the next
   // successful call. Nothing in this milestone calls this yet; it exists so
   // a future agent loop has a place to report provider failures without
   // further session.State changes.
   func (s *State) SetProviderError(err error) {
       s.mu.Lock()
       defer s.mu.Unlock()
       s.providerErr = err
   }

   func (s *State) ProviderError() error {
       s.mu.Lock()
       defer s.mu.Unlock()
       return s.providerErr
   }
   ```

Constraint: `session.go` must not import anything from `marshal/internal/llm/*` — `providerErr` stays a plain `error`.

Add tests to `internal/app/session/session_test.go` (existing file — read it first for style: uses `New(config.Default(), "/repo", time.Unix(100, 0))`, `t.Fatalf`):
- `TestSetProviderErrorStoresAndRetrieves` — set a non-nil error, assert `ProviderError()` returns it (compare via `errors.Is` or direct equality since it's the same error value).
- `TestSetProviderErrorNilClearsExistingError` — set an error, then `SetProviderError(nil)`, assert `ProviderError()` returns nil.

Report: exact diff summary of `session.go`, new test names, `go test ./internal/app/session/...` output showing all tests (existing + new) passing.

## Task 7: TUI provider-error display

This task depends on Task 6 (`session.State.ProviderError()` exists).

Modify `internal/app/tui/model.go`. Read the current file first — `View()` renders labeled sections in order via `fmt.Fprintf(&b, ...)`: header/status, "Transcript", "Streaming Output", "Command Palette", "Tool Log", "Diff", then the input box.

Add a new conditional section immediately after the existing "Diff" section (`fmt.Fprintf(&b, "\nDiff\n")` / `fmt.Fprintf(&b, "  No patch proposed.\n")`) and before the input box line, matching the exact same `fmt.Fprintf(&b, ...)` style already used for every other section:

```go
if err := m.state.ProviderError(); err != nil {
    fmt.Fprintf(&b, "\nProvider Error\n")
    fmt.Fprintf(&b, "  %s\n", err.Error())
}
```

The section must be entirely absent from the rendered output when `ProviderError()` returns nil — do not print an empty "Provider Error" header in that case.

Add tests to `internal/app/tui/model_test.go` (read the existing file first — if it doesn't exist yet, create it following the same construction pattern as `session_test.go`: build a `session.State` via `session.New(config.Default(), "/repo", time.Unix(100, 0))`, then `tui.New(state)`):
- `TestViewShowsProviderErrorWhenSet` — call `state.SetProviderError(errors.New("dial tcp: connection refused"))` on the state backing the model, call `model.View()`, assert the output contains both the substring `"Provider Error"` and `"connection refused"`.
- `TestViewOmitsProviderErrorSectionByDefault` — fresh state/model with no error set, assert `model.View()` does NOT contain the substring `"Provider Error"`.

Report: exact diff summary of `model.go`, confirmation whether `model_test.go` was created or modified, new test names, `go test ./internal/app/tui/...` output showing all tests passing.

## Task 8: Ollama and LM Studio integration tests

This task depends on Task 4/Task 5 (`provider.NewOpenAICompatible`, `provider.NewFromConfig` exist and are tested).

Create `internal/llm/provider/integration_test.go` with a build tag so it never compiles or runs under plain `go test ./...`:

```go
//go:build integration

package provider
```

Add two tests:

- `TestOllamaChatIntegration`: reads `MARSHAL_TEST_OLLAMA_URL` from the environment via `os.Getenv`; if empty, `t.Skip("set MARSHAL_TEST_OLLAMA_URL to run this test against a local Ollama server")`. Reads `MARSHAL_TEST_OLLAMA_MODEL`, defaulting to `"qwen2.5-coder:1.5b"` if unset. Constructs an `OpenAICompatible` via `NewOpenAICompatible(Options{Name: "ollama", BaseURL: <the URL>})` (Ollama's default local setup needs no API key). Uses `context.WithTimeout` (30s is reasonable) and sends a `schema.ChatRequest{Model: <the model>, Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "Say the word 'test' and nothing else."}}, Stream: true}`. Drains the returned channel, accumulating `Delta` content, and asserts: no `ChatEventError` was received, a `ChatEventDone` was received, and the accumulated content is non-empty.

- `TestLMStudioChatIntegration`: identical shape, gated on `MARSHAL_TEST_LMSTUDIO_URL` / `MARSHAL_TEST_LMSTUDIO_MODEL` (default model can be any small local model string, e.g. `"local-model"` — LM Studio typically ignores the exact model field when only one model is loaded, so document this assumption in a comment).

At the top of the file, above the two test functions, add a doc comment block with the exact manual run commands:
```go
// Run manually against a local Ollama server:
//   MARSHAL_TEST_OLLAMA_URL=http://localhost:11434/v1 \
//     go test -tags=integration ./internal/llm/provider/... -run Ollama -v
//
// Run manually against a local LM Studio server:
//   MARSHAL_TEST_LMSTUDIO_URL=http://localhost:1234/v1 \
//     go test -tags=integration ./internal/llm/provider/... -run LMStudio -v
```

Verification for this task (the implementer does NOT need a running Ollama/LM Studio instance — that's a manual step for the human afterward):
- `go test ./...` (no `-tags=integration`) must show these tests do not run and the package still builds/tests cleanly otherwise.
- `go build -tags=integration ./...` must succeed (the file compiles under the integration tag even without a live server, since the tests just skip at runtime without the env var).
- `go vet -tags=integration ./...` must pass.

Report: file created, confirmation of the three verification commands above with their output, and the exact manual-run commands documented in the file.

## Global follow-up (controller, not a subagent task)

After all 8 tasks pass task review and the final whole-branch review is clean, the controller (not a subagent) marks the 8 Milestone C checklist items `[x]` in `docs/10-mvp-implementation-checklist.md` and commits that as a final small commit, then proceeds to `superpowers:finishing-a-development-branch`.
