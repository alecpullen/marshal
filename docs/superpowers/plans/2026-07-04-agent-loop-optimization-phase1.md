# Agent Loop Optimization — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-_SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce local-model latency and iteration waste by hardening prompts, adding a per-request timeout, and optionally enabling JSON-mode responses.

**Architecture:** Keep the existing JSON action protocol and `Runner` shape. Changes are additive: better examples in the system prompt, an inner timeout around each `chatOnce` call, and a capability-gated `response_format` field on chat requests.

**Tech Stack:** Go, existing Marshal agent/provider packages.

---

## Task 1: Few-shot examples for every action type

**Files:**
- Modify: `internal/agent/prompts.go`
- Test: `internal/agent/prompts_test.go`

Currently the system prompt lists action shapes but only gives one example per role. The live tests showed the model producing plain-text plans and invalid unified diffs. We will add a concrete example for each action type and explicitly tell the model not to use unified diff syntax.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/prompts_test.go`:

```go
func TestBuildSystemPromptContainsActionExamples(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools())
	content := msg.Content

	for _, marker := range []string{
		`"type": "answer"`,
		`"type": "tool_call"`,
		`"type": "patch"`,
		`"type": "final"`,
		"<<<<<<< SEARCH",
		">>>>>>> REPLACE",
		"Do not use unified diff syntax",
	} {
		if !strings.Contains(content, marker) {
			t.Errorf("prompt missing %q", marker)
		}
	}
}
```

Run:

```bash
go test ./internal/agent/... -run TestBuildSystemPromptContainsActionExamples -count=1 -v
```

Expected: FAIL — markers missing.

- [ ] **Step 2: Update the base output format**

In `internal/agent/prompts.go`, replace `baseOutputFormat` with:

```go
const baseOutputFormat = `Respond with exactly one JSON object and nothing else.

Allowed action shapes:
{"rationale": "short reason", "action": {"type": "answer", "content": "plain text answer"}}
{"rationale": "short reason", "action": {"type": "tool_call", "tool": "tool.name", "args": {"key": "value"}}}
{"rationale": "short reason", "action": {"type": "patch", "content": "File: path/to/file.go\n<<<<<<< SEARCH\nold line\n=======\nnew line\n>>>>>>> REPLACE"}}
{"rationale": "short reason", "action": {"type": "final", "content": "concise summary of what was done"}}

For patch actions use search/replace blocks, one block per file. Do not use unified diff syntax.`
```

- [ ] **Step 3: Update the implementer role example**

In `internal/agent/prompts.go`, change the `RoleImplementer` example to a patch:

```go
RoleImplementer: {
	focus:          "You are an implementer. Make focused edits. After each edit, run the narrowest useful validation. Prefer file.read and file.write_patch over shell commands when possible.",
	allowedActions: []string{"tool_call", "patch", "final"},
	example:        `{"rationale": "The parser expects an integer but receives a string.", "action": {"type": "patch", "content": "File: parser.go\n<<<<<<< SEARCH\nfunc parse(input string) int {\n=======\nfunc parse(input string) (int, error) {\n>>>>>>> REPLACE"}}`,
},
```

Run:

```bash
go test ./internal/agent/... -run TestBuildSystemPrompt -count=1 -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/prompts.go internal/agent/prompts_test.go
git commit -m "feat(agent): add few-shot examples for every action type and correct patch format"
```

---

## Task 2: Per-request timeout in the runner

**Files:**
- Modify: `internal/agent/runner.go`
- Test: `internal/agent/runner_test.go`

Currently `chatOnce` passes the caller context straight to `p.Chat`. A hung local model call can block the whole turn. We will add a `RequestTimeout` field to `Runner` and wrap each request in an inner timeout.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/runner_test.go`:

```go
func TestChatOnceTimesOutPerRequest(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{"ignored"},
		errs:      []error{context.DeadlineExceeded},
	}
	reg := registry.New()
	state := session.New(config.Config{}, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.RequestTimeout = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := runner.chatOnce(ctx, p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
```

Note: this test relies on `scriptedProvider` blocking until context cancellation. The existing `scriptedProvider.Chat` does not block on `errs`; it returns immediately. Adjust `scriptedProvider` in the test file to respect ctx cancellation before returning errors, or create a new fake provider that blocks. For planning, document that the test needs a blocking provider. A minimal fake:

```go
type blockingProvider struct{}

func (p *blockingProvider) Name() string { return "blocking" }
func (p *blockingProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }
func (p *blockingProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) { return schema.EmbedResponse{}, nil }
func (p *blockingProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	events := make(chan schema.ChatEvent)
	go func() {
		defer close(events)
		<-ctx.Done()
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
	}()
	return events, nil
}
```

Use this fake in the test instead of `scriptedProvider`.

Run:

```bash
go test ./internal/agent/... -run TestChatOnceTimesOutPerRequest -count=1 -v
```

Expected: FAIL — `RequestTimeout` field does not exist.

- [ ] **Step 2: Add RequestTimeout field and apply it in chatOnce**

In `internal/agent/runner.go`, add to `Runner` struct:

```go
RequestTimeout time.Duration
```

In `NewRunner`, default it to `0` (no inner timeout). Optionally set a sensible default later once tests pass.

Modify `chatOnce`:

```go
func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (string, error) {
	if r.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.RequestTimeout)
		defer cancel()
	}

	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
	})
	// ... rest unchanged
}
```

Run:

```bash
go test ./internal/agent/... -run TestChatOnceTimesOutPerRequest -count=1 -v
```

Expected: PASS.

- [ ] **Step 3: Wire RequestTimeout from app config**

In `internal/app/app.go`, inside `buildAgentRunner`, set a default after constructing the runner:

```go
runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
if runner.RequestTimeout == 0 {
	runner.RequestTimeout = 60 * time.Second
}
```

This gives local models a 60s per-request ceiling without changing existing tests.

Run:

```bash
go test ./internal/app/... -run TestRun -count=1 -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go internal/app/app.go
git commit -m "feat(agent): add per-request timeout to runner"
```

---

## Task 3: Optional JSON-mode / response_format support

**Files:**
- Modify: `internal/llm/schema/chat.go`
- Modify: `internal/llm/provider/openai_compatible.go`
- Modify: `internal/llm/provider/openai_compatible_wire.go`
- Modify: `internal/llm/provider/openai_compatible_test.go`
- Modify: `internal/agent/runner.go`
- Modify: `internal/app/app.go`

Some local servers support OpenAI-style `response_format: {"type":"json_object"}`, which strongly encourages valid JSON and reduces parse-error loops.

- [ ] **Step 1: Add ResponseFormat to the schema**

In `internal/llm/schema/chat.go`, add:

```go
type ResponseFormat struct {
	Type       string `json:"type"`
}
```

And add a field to `ChatRequest`:

```go
ResponseFormat *ResponseFormat
```

- [ ] **Step 2: Wire response_format through the provider body**

In `internal/llm/provider/openai_compatible_wire.go`, import `"marshal/internal/llm/schema"` and add to `chatCompletionRequestBody`:

```go
ResponseFormat *schema.ResponseFormat `json:"response_format,omitempty"`
```

In `internal/llm/provider/openai_compatible.go`, update `buildChatRequestBody` to copy the field:

```go
return json.Marshal(chatCompletionRequestBody{
	Model:          req.Model,
	Messages:       messages,
	Stream:         req.Stream,
	Temperature:    req.Temperature,
	TopP:           req.TopP,
	MaxTokens:      req.MaxTokens,
	Stop:           req.Stop,
	ResponseFormat: req.ResponseFormat,
})
```

- [ ] **Step 3: Add a unit test for response_format**

In `internal/llm/provider/openai_compatible_test.go`, add a test that builds a request with `ResponseFormat` and asserts the JSON body contains `response_format`:

```go
func TestBuildChatRequestBodyIncludesResponseFormat(t *testing.T) {
	rf := schema.ResponseFormat{Type: "json_object"}
	body, err := buildChatRequestBody(schema.ChatRequest{
		Model:          "test",
		Messages:       []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		ResponseFormat: &rf,
	})
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	if !strings.Contains(string(body), `"response_format":{"type":"json_object"}`) {
		t.Fatalf("body missing response_format: %s", body)
	}
}
```

Run:

```bash
go test ./internal/llm/provider/... -run TestBuildChatRequestBodyIncludesResponseFormat -count=1 -v
```

Expected: PASS after implementation.

- [ ] **Step 4: Enable JSON mode for the runner when the model preset asks for it**

In `internal/agent/runner.go`, add to `Runner`:

```go
ResponseFormat *schema.ResponseFormat
```

In `chatOnce`, copy it into the request:

```go
req := schema.ChatRequest{
	Model:          model,
	Messages:       messages,
	Stream:         true,
	ResponseFormat: r.ResponseFormat,
}
```

In `internal/llm/provider/openai_compatible.go`, update `defaultCapabilities()` to advertise JSON mode for OpenAI-compatible servers (the `response_format` parameter is part of the standard chat completions API):

```go
func defaultCapabilities() schema.ProviderCapabilities {
	return schema.ProviderCapabilities{
		Streaming:        true,
		Embeddings:       true,
		ToolCalling:      false,
		JSONMode:         true,
		StructuredOutput: true,
	}
}
```

In `internal/app/app.go`, after resolving the route, check the preset's `ToolCalling` field. If it equals `"json"` and the provider reports JSON mode support, set the runner field:

```go
if route.Preset.ToolCalling == "json" {
	if caps := resolvedProvider.Capabilities(ctx); caps.JSONMode {
		runner.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
	}
}
```

Servers that do not support `response_format` will return an HTTP error; the user can then remove `tool_calling = "json"` from the preset.

Add `ctx` to the early part of `buildAgentRunner` if not already present (it is).

Run:

```bash
go test ./internal/agent/... ./internal/app/... ./internal/llm/provider/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Document the new preset field behavior**

In `docs/09-configuration-examples.md`, under the model presets section, add a note:

```toml
[models.presets.qwen3]
provider = "lmstudio"
model = "qwen/qwen3.6-35b-a3b"
tool_calling = "json"  # requests response_format=json_object if the provider supports it
```

- [ ] **Step 6: Commit**

```bash
git add internal/llm/schema/chat.go internal/llm/provider/openai_compatible.go internal/llm/provider/openai_compatible_wire.go internal/llm/provider/openai_compatible_test.go internal/agent/runner.go internal/app/app.go docs/09-configuration-examples.md
git commit -m "feat(provider): optional response_format/json_object support"
```

---

## Phase 1 Verification

After all tasks:

```bash
go test ./...
go vet ./...
```

Expected: all packages pass, vet clean.

Then run the live integration suite to measure improvement:

```bash
go test -tags=integration ./internal/app/... -run TestLiveAgent -count=1 -v
```

Capture the new timing/failure output and compare to the baseline collected before Phase 1.

---

## Spec Coverage Check

| Spec Section | Implementing Task |
|---|---|
| 1.1 Few-shot action examples | Task 1 |
| 1.2 Optional structured-output mode | Task 3 |
| 1.3 Per-request timeout | Task 2 |
| 1.4 Skip planning for simple tasks | Deferred; current `ClassQuestion` path already skips planning |
| Phase 2 tool cache / parallel calls / loop detection | Will be planned separately after Phase 1 lands |
| Phase 3 native tool calling | Future milestone, not in this plan |
