# Agent Loop Optimization Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the agent loop smarter by adding a per-turn read-only tool cache, parallel execution of read-only actions, loop detection, and tool-output summarization/truncation, while keeping the existing single-action protocol backward-compatible.

**Architecture:** Extend the JSON action envelope with an optional `actions` array, add a small in-memory cache to `session.State`, teach `agent.Runner` to execute parallel read-only calls and detect repeated calls, and introduce a tool-agnostic summarizer that truncates oversized results before they hit the transcript. Native tool-calling support is kept as a documented future milestone.

**Tech Stack:** Go 1.22+, existing `internal/agent`, `internal/app/session`, `internal/tools/registry` packages.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/app/session/session.go` | Adds the per-turn `turnToolCache` and its accessors. |
| `internal/agent/protocol.go` | Extends `ModelAction` and `ParseAction` to support an `actions` array. |
| `internal/agent/prompts.go` | Updates the output-format instructions to advertise the `actions` array. |
| `internal/agent/summarize.go` | New file: `SummarizeToolResult` helper with tool-specific and generic truncation. |
| `internal/agent/runner.go` | Wires cache, parallel actions, loop detection, and summarization into the loop. |
| `internal/app/session/session_test.go` | Unit tests for the cache. |
| `internal/agent/protocol_test.go` | Tests for the `actions` array parser. |
| `internal/agent/summarize_test.go` | Tests for summarization/truncation. |
| `internal/agent/runner_test.go` | Runner-level tests for cache, parallelism, loop detection, and summarization. |

---

### Task 1: Add per-turn tool-result cache to `session.State`

**Files:**
- Modify: `internal/app/session/session.go:84-105`
- Test: `internal/app/session/session_test.go`

- [ ] **Step 1: Add cache storage and accessors to `State`**

Add the cache field inside `State` and initialize it in `New`:

```go
type State struct {
	// ... existing fields ...
	turnToolCache map[string]registry.ToolResult
}
```

```go
func New(cfg config.Config, workingDir string, now time.Time, p Persistence) *State {
	ctx, cancel := context.WithCancel(context.Background())
	return &State{
		Config:        cfg,
		WorkingDir:    workingDir,
		StartedAt:     now,
		db:            p.DB,
		sessionID:     p.SessionID,
		logger:        p.Logger,
		ctx:           ctx,
		cancel:        cancel,
		turnToolCache: make(map[string]registry.ToolResult),
	}
}
```

Append these methods at the end of `session.go`:

```go
func (s *State) ClearTurnToolCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnToolCache = make(map[string]registry.ToolResult)
}

func (s *State) GetTurnToolResult(toolName string, normalizedArgs []byte) (registry.ToolResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := toolName + "|" + string(normalizedArgs)
	result, ok := s.turnToolCache[key]
	return result, ok
}

func (s *State) SetTurnToolResult(toolName string, normalizedArgs []byte, result registry.ToolResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := toolName + "|" + string(normalizedArgs)
	s.turnToolCache[key] = result
}
```

- [ ] **Step 2: Write the cache unit test**

Append to `internal/app/session/session_test.go`:

```go
func TestTurnToolCacheCachesAndClears(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	args := []byte(`{"path":"a.go"}`)
	want := registry.ToolResult{Summary: "read ok", Content: "package a"}

	state.SetTurnToolResult("file.read", args, want)
	got, ok := state.GetTurnToolResult("file.read", args)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Content != want.Content {
		t.Fatalf("cached content = %q, want %q", got.Content, want.Content)
	}

	state.ClearTurnToolCache()
	if _, ok := state.GetTurnToolResult("file.read", args); ok {
		t.Fatal("expected cache miss after clear")
	}
}
```

- [ ] **Step 3: Run the cache test**

Run:

```bash
go test ./internal/app/session -run TestTurnToolCacheCachesAndClears -v
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): per-turn read-only tool result cache"
```

---

### Task 2: Canonicalize tool arguments for stable cache keys

**Files:**
- Modify: `internal/agent/runner.go`

- [ ] **Step 1: Add `normalizeArgs` helper**

Insert near the top of `runner.go` after the imports/const block:

```go
// normalizeArgs returns a canonical JSON representation of a tool's
// arguments so that {"b":1,"a":2} and {"a":2,"b":1} share the same
// cache key. Empty arguments normalise to {}.
func normalizeArgs(args json.RawMessage) ([]byte, error) {
	if len(args) == 0 {
		return []byte("{}"), nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}
```

- [ ] **Step 2: Add a unit test for normalization**

Append to `internal/agent/runner_test.go`:

```go
func TestNormalizeArgsIsStableAcrossKeyOrder(t *testing.T) {
	a, err := normalizeArgs(json.RawMessage(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatalf("normalizeArgs error: %v", err)
	}
	b, err := normalizeArgs(json.RawMessage(`{"a":2,"b":1}`))
	if err != nil {
		t.Fatalf("normalizeArgs error: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("keys ordered differently produced different normalization: %q vs %q", a, b)
	}
	if string(a) != `{"a":2,"b":1}` {
		t.Fatalf("unexpected normalized form: %q", a)
	}
}
```

- [ ] **Step 3: Run the test**

```bash
go test ./internal/agent -run TestNormalizeArgsIsStableAcrossKeyOrder -v
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): canonicalize tool arguments for cache keys"
```

---

### Task 3: Extend the action protocol with an `actions` array

**Files:**
- Modify: `internal/agent/protocol.go`
- Test: `internal/agent/protocol_test.go`

- [ ] **Step 1: Update the protocol types and parser**

Replace the contents of `internal/agent/protocol.go` with:

```go
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ActionType string

const (
	ActionAnswer   ActionType = "answer"
	ActionToolCall ActionType = "tool_call"
	ActionPatch    ActionType = "patch"
	ActionFinal    ActionType = "final"
)

var (
	ErrNoActionFound     = errors.New("agent: no JSON action object found in model output")
	ErrUnknownActionType = errors.New("agent: unknown action type")
	ErrMissingTool       = errors.New("agent: tool_call action missing tool name")
)

// ModelAction is the parsed form of the JSON action-protocol envelope
// described in docs/07-agent-runtime-and-swarm.md. When Actions is set,
// the single-action fields are empty and vice-versa.
type ModelAction struct {
	Rationale string
	Type      ActionType
	Tool      string
	Args      json.RawMessage
	Content   string
	Actions   []ModelAction // parallel read-only tool calls
}

type actionEnvelope struct {
	Rationale string          `json:"rationale"`
	Action    actionPayload   `json:"action"`
	Actions   []actionPayload `json:"actions,omitempty"`
}

type actionPayload struct {
	Type    ActionType      `json:"type"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Content string          `json:"content,omitempty"`
}

// ParseAction extracts and validates the JSON action envelope. It tolerates a
// leading/trailing ```json fence, since local models frequently wrap JSON in
// markdown even when told not to.
func ParseAction(raw string) (ModelAction, error) {
	jsonText, err := extractJSONObject(raw)
	if err != nil {
		return ModelAction{}, err
	}

	var envelope actionEnvelope
	if err := json.Unmarshal([]byte(jsonText), &envelope); err != nil {
		return ModelAction{}, fmt.Errorf("agent: malformed action JSON: %w", err)
	}

	if len(envelope.Actions) > 0 {
		actions := make([]ModelAction, 0, len(envelope.Actions))
		for _, p := range envelope.Actions {
			ma, err := validatePayload(p)
			if err != nil {
				return ModelAction{}, err
			}
			actions = append(actions, ma)
		}
		return ModelAction{Rationale: envelope.Rationale, Actions: actions}, nil
	}

	ma, err := validatePayload(envelope.Action)
	if err != nil {
		return ModelAction{}, err
	}
	return ModelAction{
		Rationale: envelope.Rationale,
		Type:      ma.Type,
		Tool:      ma.Tool,
		Args:      ma.Args,
		Content:   ma.Content,
	}, nil
}

func validatePayload(p actionPayload) (ModelAction, error) {
	switch p.Type {
	case ActionAnswer, ActionToolCall, ActionPatch, ActionFinal:
	default:
		return ModelAction{}, fmt.Errorf("%w: %q", ErrUnknownActionType, p.Type)
	}
	if p.Type == ActionToolCall && strings.TrimSpace(p.Tool) == "" {
		return ModelAction{}, ErrMissingTool
	}
	return ModelAction{Type: p.Type, Tool: p.Tool, Args: p.Args, Content: p.Content}, nil
}

func extractJSONObject(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end == -1 || end < start {
		return "", ErrNoActionFound
	}
	return trimmed[start : end+1], nil
}
```

- [ ] **Step 2: Add parser tests for the array form**

Append to `internal/agent/protocol_test.go`:

```go
func TestParseActionAcceptsActionsArray(t *testing.T) {
	raw := `{"rationale":"read two files","actions":[{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}},{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}]}`

	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if len(action.Actions) != 2 {
		t.Fatalf("len(Actions) = %d, want 2", len(action.Actions))
	}
	if action.Actions[0].Tool != "file.read" {
		t.Fatalf("first tool = %q, want file.read", action.Actions[0].Tool)
	}
	if action.Type != "" {
		t.Fatalf("single-action Type should be empty when Actions is set, got %q", action.Type)
	}
}

func TestParseActionRejectsActionsWithMissingTool(t *testing.T) {
	raw := `{"rationale":"bad","actions":[{"type":"tool_call","args":{}}]}`

	_, err := ParseAction(raw)
	if !errors.Is(err, ErrMissingTool) {
		t.Fatalf("err = %v, want ErrMissingTool", err)
	}
}

func TestParseActionBackwardCompatibleWithSingleAction(t *testing.T) {
	raw := `{"rationale":"r","action":{"type":"final","content":"done"}}`

	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if action.Type != ActionFinal || action.Content != "done" {
		t.Fatalf("action = %#v", action)
	}
	if len(action.Actions) != 0 {
		t.Fatalf("Actions should be empty for single-action envelope")
	}
}
```

- [ ] **Step 3: Run the protocol tests**

```bash
go test ./internal/agent -run TestParseAction -v
```

Expected: all PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/protocol.go internal/agent/protocol_test.go
git commit -m "feat(agent): parse parallel actions array in protocol"
```

---

### Task 4: Advertise the `actions` array in the system prompt

**Files:**
- Modify: `internal/agent/prompts.go:74-87`
- Test: `internal/agent/prompts_test.go`

- [ ] **Step 1: Update `baseOutputFormat`**

Replace the `baseOutputFormat` constant in `prompts.go` with:

```go
const baseOutputFormat = `Respond with exactly one JSON object and nothing else.

Examples:

{"rationale": "The user asked a direct factual question.", "action": {"type": "answer", "content": "Go added generics in version 1.18."}}

{"rationale": "I need to read the relevant source file before editing.", "action": {"type": "tool_call", "tool": "file.read", "args": {"path": "internal/agent/prompts.go"}}}

{"rationale": "Replace the placeholder patch example with a concrete search/replace block.", "action": {"type": "patch", "content": "File: path/to/file.go\n<<<<<<< SEARCH\nold line\n=======\nnew line\n>>>>>>> REPLACE"}}

{"rationale": "The task is finished and all tests pass.", "action": {"type": "final", "content": "Updated the system prompt with few-shot examples for every action type."}}

For parallel read-only work, you may return multiple tool calls in one response using the "actions" array. Every entry must be a read-only "tool_call". Example:

{"rationale": "Read both files at once.", "actions": [{"type": "tool_call", "tool": "file.read", "args": {"path": "a.go"}}, {"type": "tool_call", "tool": "file.read", "args": {"path": "b.go"}}]}

For patch actions use search/replace blocks, one block per file. Do not use unified diff syntax.`
```

- [ ] **Step 2: Update the prompt test expectations**

Append to `internal/agent/prompts_test.go`:

```go
func TestBuildSystemPromptDescribesParallelActionsArray(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools())
	content := msg.Content

	if !strings.Contains(content, `"actions"`) {
		t.Error("system prompt missing parallel actions array description")
	}
	if !strings.Contains(content, "parallel read-only work") {
		t.Error("system prompt missing parallel read-only guidance")
	}
}
```

- [ ] **Step 3: Run the prompt tests**

```bash
go test ./internal/agent -run TestBuildSystemPrompt -v
```

Expected: all PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/prompts.go internal/agent/prompts_test.go
git commit -m "feat(agent): document parallel actions array in system prompt"
```

---

### Task 5: Add tool-output summarization and truncation

**Files:**
- Create: `internal/agent/summarize.go`
- Test: `internal/agent/summarize_test.go`

- [ ] **Step 1: Create the summarizer**

Create `internal/agent/summarize.go`:

```go
package agent

import (
	"fmt"
	"strings"

	"marshal/internal/tools/registry"
)

// DefaultMaxToolResultChars is a rough 4-char-per-token budget for 2000 tokens.
const DefaultMaxToolResultChars = 8000

// SummarizeToolResult truncates oversized tool output before it reaches the
// transcript. It preserves the original Summary unless truncation occurs.
func SummarizeToolResult(toolName string, result registry.ToolResult, maxChars int) registry.ToolResult {
	if maxChars <= 0 {
		maxChars = DefaultMaxToolResultChars
	}

	out := result
	content := result.Content
	if content == "" {
		return out
	}

	switch toolName {
	case "repo.search":
		content = limitLines(content, 50, "more matches omitted")
	case "git.diff":
		content = limitLines(content, 200, "more diff lines omitted")
	}

	if len(content) > maxChars {
		content = content[:maxChars] + "\n\n...[truncated]"
	}

	if content != result.Content && !strings.HasSuffix(out.Summary, "[truncated]") {
		out.Summary = out.Summary + " [truncated]"
	}
	out.Content = content
	return out
}

func limitLines(content string, maxLines int, label string) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n") +
		fmt.Sprintf("\n\n... %d %s", len(lines)-maxLines, label)
}
```

- [ ] **Step 2: Write summarizer tests**

Create `internal/agent/summarize_test.go`:

```go
package agent

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestSummarizeToolResultTruncatesGenericContent(t *testing.T) {
	long := strings.Repeat("x", DefaultMaxToolResultChars+100)
	result := SummarizeToolResult("file.read", registry.ToolResult{Summary: "read ok", Content: long}, 0)

	if len(result.Content) >= len(long) {
		t.Fatalf("content was not truncated")
	}
	if !strings.HasSuffix(result.Content, "[truncated]") {
		t.Fatalf("missing truncation marker: %q", result.Content)
	}
	if !strings.Contains(result.Summary, "[truncated]") {
		t.Fatalf("summary should note truncation: %q", result.Summary)
	}
}

func TestSummarizeToolResultLimitsRepoSearchLines(t *testing.T) {
	content := strings.Repeat("match\n", 60)
	result := SummarizeToolResult("repo.search", registry.ToolResult{Summary: "found 60", Content: content}, 0)

	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 51 { // 50 matches + omission notice
		t.Fatalf("got %d lines, want 51", len(lines))
	}
	if !strings.Contains(result.Content, "more matches omitted") {
		t.Fatalf("missing omission notice: %q", result.Content)
	}
}

func TestSummarizeToolResultLeavesSmallResultsUnchanged(t *testing.T) {
	result := SummarizeToolResult("file.read", registry.ToolResult{Summary: "ok", Content: "hello"}, 0)
	if result.Content != "hello" {
		t.Fatalf("content changed unexpectedly: %q", result.Content)
	}
	if result.Summary != "ok" {
		t.Fatalf("summary changed unexpectedly: %q", result.Summary)
	}
}
```

- [ ] **Step 3: Run the summarizer tests**

```bash
go test ./internal/agent -run TestSummarizeToolResult -v
```

Expected: all PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/summarize.go internal/agent/summarize_test.go
git commit -m "feat(agent): summarize and truncate oversized tool results"
```

---

### Task 6: Wire cache, summarization, parallel actions, and loop detection into `Runner`

**Files:**
- Modify: `internal/agent/runner.go`
- Test: `internal/agent/runner_test.go`

- [ ] **Step 1: Add runner fields for loop detection and summarization budget**

Update the `Runner` struct definition:

```go
type Runner struct {
	Provider           provider.Provider
	Registry           *registry.Registry
	Policy             *policy.PolicyEngine
	State              *session.State
	Model              string
	RouteResolver      RouteResolver
	MemoryProvider     MemoryProvider
	ProjectID          int64
	Now                func() time.Time
	MaxToolIterations  int
	MaxRetries         int
	RequestTimeout     time.Duration
	ResponseFormat     *schema.ResponseFormat
	MaxToolResultChars int

	callHistory    []toolCallKey
	callHistoryMu  sync.Mutex
	loopNudgeSent  bool
}

type toolCallKey struct {
	Name string
	Args string
}
```

Add `sync` to the imports.

- [ ] **Step 2: Reset per-turn state and clear the cache at the start of `Run`**

Inside `Run`, immediately after adding the user message:

```go
func (r *Runner) Run(ctx context.Context, goal string) error {
	r.State.AddMessage(session.RoleUser, goal)
	r.State.ClearTurnToolCache()
	r.callHistoryMu.Lock()
	r.callHistory = nil
	r.loopNudgeSent = false
	r.callHistoryMu.Unlock()

	task := NewTask(goal, r.Now())
	// ... rest unchanged ...
```

- [ ] **Step 3: Handle the `actions` array in the execution loop**

Replace the execution-loop body (the `for iteration := 0; ...` block) with:

```go
	task.Status = TaskStatusExecuting
	for iteration := 0; iteration < r.MaxToolIterations; iteration++ {
		raw, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages)
		if err != nil {
			return r.fail(task, err)
		}

		action, parseErr := ParseAction(raw)
		if parseErr != nil {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
			messages = append(messages, BuildCorrectionMessage(parseErr))
			continue
		}
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})

		if len(action.Actions) > 0 {
			if !r.allReadOnly(action.Actions) {
				messages = append(messages, BuildCorrectionMessage(errors.New("the 'actions' array may only contain read-only tool_call entries")))
				continue
			}
			resultMsgs, execErr := r.executeActions(ctx, action.Actions)
			if execErr != nil {
				return r.fail(task, execErr)
			}
			messages = append(messages, resultMsgs...)
			continue
		}

		switch action.Type {
		case ActionAnswer, ActionFinal:
			task.Summary = action.Content
			task.Status = TaskStatusCompleted
			r.State.AddMessage(session.RoleAssistant, action.Content)
			return nil
		case ActionToolCall, ActionPatch:
			resultMsgs, err := r.executeToolCall(ctx, action)
			if err != nil {
				return r.fail(task, err)
			}
			messages = append(messages, resultMsgs...)
		default:
			messages = append(messages, BuildCorrectionMessage(fmt.Errorf("unsupported action type %q", action.Type)))
		}
	}
```

- [ ] **Step 4: Add helpers for read-only checks and parallel execution**

Append to `runner.go`:

```go
func (r *Runner) allReadOnly(actions []ModelAction) bool {
	for _, a := range actions {
		if a.Type != ActionToolCall {
			return false
		}
		tool, ok := r.Registry.Lookup(a.Tool)
		if !ok || tool.Risk != registry.RiskReadOnly {
			return false
		}
	}
	return true
}

func (r *Runner) executeActions(ctx context.Context, actions []ModelAction) ([]schema.ChatMessage, error) {
	results := make([]schema.ChatMessage, len(actions))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, a := range actions {
		wg.Add(1)
		go func(idx int, act ModelAction) {
			defer wg.Done()
			msgs, err := r.executeToolCall(ctx, act)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			results[idx] = joinMessages(msgs)
		}(i, a)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func joinMessages(msgs []schema.ChatMessage) schema.ChatMessage {
	if len(msgs) == 0 {
		return schema.ChatMessage{Role: schema.RoleUser, Content: ""}
	}
	if len(msgs) == 1 {
		return msgs[0]
	}
	var parts []string
	for _, m := range msgs {
		parts = append(parts, m.Content)
	}
	return schema.ChatMessage{Role: schema.RoleUser, Content: strings.Join(parts, "\n\n")}
}
```

- [ ] **Step 5: Rewrite `executeToolCall` to return slices and integrate cache/summarization/loop detection**

Replace the entire `executeToolCall` method with:

```go
func (r *Runner) executeToolCall(ctx context.Context, action ModelAction) ([]schema.ChatMessage, error) {
	toolName := action.Tool
	if action.Type == ActionPatch {
		toolName = "file.write_patch"
	}

	tool, ok := r.Registry.Lookup(toolName)
	if !ok {
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, "unknown tool")}, nil
	}

	args := action.Args
	if action.Type == ActionPatch {
		encoded, err := json.Marshal(map[string]string{"patch": action.Content})
		if err != nil {
			return []schema.ChatMessage{BuildToolErrorMessage(toolName, "failed to encode patch arguments")}, nil
		}
		args = encoded
	}

	argsMap := map[string]interface{}{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return []schema.ChatMessage{BuildToolErrorMessage(toolName, "arguments are not a valid JSON object")}, nil
		}
	}

	normalizedArgs, normErr := normalizeArgs(args)
	if normErr != nil {
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, "failed to normalize arguments")}, nil
	}

	// Read-only cache lookup.
	if tool.Risk == registry.RiskReadOnly {
		if cached, hit := r.State.GetTurnToolResult(toolName, normalizedArgs); hit {
			r.recordToolCall(toolName, string(normalizedArgs))
			return []schema.ChatMessage{BuildCachedToolResultMessage(toolName, cached)}, nil
		}
	}

	r.Policy.SetSessionRules(r.State.SessionRules())
	decision, reason, err := r.Policy.Evaluate(toolName, argsMap)
	if err != nil {
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, err.Error())}, nil
	}

	approval := registry.ApprovalNotRequired
	switch decision {
	case policy.DecisionDeny:
		event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("denied: %s", reason))
		r.State.LogToolCall(event)
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, "denied by policy: "+reason)}, nil
	case policy.DecisionConfirm:
		approved, edited, waitErr := r.requestApproval(ctx, tool, toolName, args, argsMap, reason)
		if waitErr != nil {
			return nil, waitErr
		}
		if !approved {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, errors.New("denied by user"))
			r.State.LogToolCall(event)
			return []schema.ChatMessage{BuildToolErrorMessage(toolName, "denied by user")}, nil
		}
		approval = registry.ApprovalApproved
		if edited != "" {
			argsMap["command"] = edited
			if remarshalled, merr := json.Marshal(argsMap); merr == nil {
				args = remarshalled
				normalizedArgs, _ = normalizeArgs(args)
			}
		}
	case policy.DecisionAllow:
		approval = registry.ApprovalNotRequired
	}

	call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
	result, execErr := tool.Handler(ctx, call)
	if execErr != nil {
		event := registry.NewAuditEvent(r.Now(), tool, call, registry.ToolResult{}, approval, execErr)
		r.State.LogToolCall(event)
		return []schema.ChatMessage{BuildToolErrorMessage(toolName, execErr.Error())}, nil
	}

	summarized := SummarizeToolResult(toolName, result, r.MaxToolResultChars)
	if tool.Risk == registry.RiskReadOnly {
		r.State.SetTurnToolResult(toolName, normalizedArgs, summarized)
	}
	event := registry.NewAuditEvent(r.Now(), tool, call, summarized, approval, nil)
	r.State.LogToolCall(event)

	msgs := []schema.ChatMessage{BuildToolResultMessage(toolName, summarized)}
	r.recordToolCall(toolName, string(normalizedArgs))
	if r.shouldNudgeLoop() {
		msgs = append(msgs, schema.ChatMessage{
			Role:    schema.RoleSystem,
			Content: "You appear to be repeating the same step. Either produce a final answer or ask the user for clarification.",
		})
	}
	return msgs, nil
}

func (r *Runner) recordToolCall(name, args string) {
	r.callHistoryMu.Lock()
	defer r.callHistoryMu.Unlock()
	r.callHistory = append(r.callHistory, toolCallKey{Name: name, Args: args})
}

func (r *Runner) shouldNudgeLoop() bool {
	r.callHistoryMu.Lock()
	defer r.callHistoryMu.Unlock()
	if r.loopNudgeSent {
		return false
	}
	n := len(r.callHistory)
	if n < 3 {
		return false
	}
	if r.callHistory[n-1] == r.callHistory[n-2] && r.callHistory[n-2] == r.callHistory[n-3] {
		r.loopNudgeSent = true
		return true
	}
	return false
}
```

- [ ] **Step 6: Add `BuildCachedToolResultMessage` helper**

Append to `prompts.go`:

```go
func BuildCachedToolResultMessage(name string, result registry.ToolResult) schema.ChatMessage {
	cached := result
	cached.Summary = "(cached) " + result.Summary
	return BuildToolResultMessage(name, cached)
}
```

- [ ] **Step 7: Update `fail` to handle slice returns from `executeToolCall`**

No change is needed to `fail`; callers now handle the slice directly.

- [ ] **Step 8: Build and run unit tests**

```bash
go build ./...
go test ./internal/agent ./internal/app/session -v
```

Expected: existing tests still pass; new tests added next.

- [ ] **Step 9: Add runner-level integration tests**

Append to `internal/agent/runner_test.go`:

```go
func TestRunCachesReadOnlyToolResults(t *testing.T) {
	calls := 0
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calls++
			return registry.ToolResult{Summary: "ok", Content: "demo content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []string{
		"1. Read the demo value twice.",
		`{"rationale":"read","action":{"type":"tool_call","tool":"demo.read","args":{"key":"value"}}}`,
		`{"rationale":"read again","action":{"type":"tool_call","tool":"demo.read","args":{"key":"value"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Read the demo value twice"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler called %d times, want 1 (second call should be cached)", calls)
	}
	foundCached := false
	for _, ev := range state.AuditLog() {
		if strings.Contains(ev.ResultSummary, "(cached)") {
			foundCached = true
		}
	}
	if !foundCached {
		t.Fatalf("audit log missing cached result marker: %#v", state.AuditLog())
	}
}

func TestRunExecutesParallelReadOnlyActions(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.a",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "a ok", Content: "alpha"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "demo.b",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "b ok", Content: "beta"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []string{
		"1. Read both demo values.",
		`{"rationale":"read both","actions":[{"type":"tool_call","tool":"demo.a","args":{}},{"type":"tool_call","tool":"demo.b","args":{}}]}`,
		`{"rationale":"done","action":{"type":"final","content":"Got alpha and beta."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Read both demo values"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(state.AuditLog()) != 2 {
		t.Fatalf("len(auditLog) = %d, want 2", len(state.AuditLog()))
	}
	final := state.Messages()[len(state.Messages())-1].Content
	if !strings.Contains(final, "alpha") || !strings.Contains(final, "beta") {
		t.Fatalf("final answer missing parallel results: %s", final)
	}
}

func TestRunDetectsRepeatedToolCalls(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []string{
		"1. Read the demo value.",
		`{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.MaxToolIterations = 5

	if err := runner.Run(context.Background(), "Read the demo value"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	found := false
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "repeating the same step") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing loop-detection nudge in transcript: %#v", state.Messages())
	}
}

func TestRunSummarizesLargeToolResults(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "big file", Content: strings.Repeat("x", DefaultMaxToolResultChars+100)}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []string{
		"1. Read the big file.",
		`{"rationale":"read","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Read the big file"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var toolResult string
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "Tool demo.read result") {
			toolResult = m.Content
		}
	}
	if !strings.Contains(toolResult, "[truncated]") {
		t.Fatalf("large tool result was not truncated: %q", toolResult)
	}
}
```

- [ ] **Step 10: Run the new runner tests**

```bash
go test ./internal/agent -run 'TestRunCachesReadOnlyToolResults|TestRunExecutesParallelReadOnlyActions|TestRunDetectsRepeatedToolCalls|TestRunSummarizesLargeToolResults' -v
```

Expected: all PASS

- [ ] **Step 11: Commit**

```bash
git add internal/agent/runner.go internal/agent/prompts.go internal/agent/runner_test.go
git commit -m "feat(agent): wire cache, parallel actions, loop detection, and summarization"
```

---

### Task 7: Verify the full unit-test suite

- [ ] **Step 1: Run all tests**

```bash
go test ./... -count=1
```

Expected: PASS for all packages.

- [ ] **Step 2: Commit any test-only fixes**

If only test changes were needed:

```bash
git add -A
git commit -m "test(agent): add Phase 2 loop/cache/parallel/summarize coverage"
```

---

### Task 8: Validate against the live integration suite

- [ ] **Step 1: Run the live suite and record timing**

```bash
go test -tags integration ./internal/app -run TestLiveAgent -v 2>&1 | tee /tmp/live-suite-phase2.log
```

- [ ] **Step 2: Compare with Phase 1 baseline**

From the Phase 1 plan, the baseline was `273.867s` for the full suite. After Phase 2, expect:

- `TestLiveAgentReadsFile`, `TestLiveAgentRunsShellCommand`, and other read-only tests to be noticeably faster on repeated reads due to caching.
- `TestLiveAgentIndexesAndMapsRepo`, `TestLiveAgentIndexesAndFindsSymbols`, and `TestLiveAgentRepoCard` to show reduced latency from summarization.
- No new `ErrMaxIterationsExceeded` failures.

Capture the final `ok` line and elapsed time from the log.

- [ ] **Step 3: Commit the timing note (optional)**

If the timing log is worth keeping, append a one-line note to the plan file or a separate benchmark doc, then commit:

```bash
git add docs/superpowers/plans/2026-07-04-agent-loop-optimization-phase2.md
git commit -m "docs(plan): record Phase 2 live suite timing"
```

---

### Task 9: Document native tool-calling as a future milestone

**Files:**
- No code changes required; the milestone is already specified in `docs/superpowers/specs/2026-07-04-agent-loop-optimization-design.md` under "Phase 3 — Native Tool Calling (future milestone)".

- [ ] **Step 1: Verify the spec section is complete**

Read `docs/superpowers/specs/2026-07-04-agent-loop-optimization-design.md` lines 93–103 and confirm it describes:

1. A new provider capability `ToolCalling = native`.
2. Converting Marshal's tool registry into native function schemas.
3. Returning tool results in the standard `tool` message role.
4. Keeping the JSON action protocol as a fallback.

- [ ] **Step 2: Add a cross-reference in the Phase 2 plan**

Append to this plan file:

```markdown
## Future Milestone: Native Tool Calling

See `docs/superpowers/specs/2026-07-04-agent-loop-optimization-design.md`, Section "Phase 3 — Native Tool Calling (future milestone)".
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/plans/2026-07-04-agent-loop-optimization-phase2.md
git commit -m "docs(plan): cross-reference native tool-calling future milestone"
```

---

## Self-Review

1. **Spec coverage:**
   - 2.1 Tool-result cache → Task 1 + Task 6. ✓
   - 2.2 Parallel read-only actions → Task 3 + Task 4 + Task 6. ✓
   - 2.3 Loop detection → Task 6. ✓
   - 2.4 Tool-output summarization/truncation → Task 5 + Task 6. ✓
   - Phase 3 native tool-calling → Task 9. ✓

2. **Placeholder scan:** No TBD/TODO/"implement later"/"similar to Task N" patterns. Every step includes exact file paths, code blocks, commands, and expected outputs. ✓

3. **Type consistency:**
   - `executeToolCall` now returns `([]schema.ChatMessage, error)` everywhere. ✓
   - `ModelAction.Actions` is `[]ModelAction`. ✓
   - Cache key uses `toolName + "|" + string(normalizedArgs)`. ✓
   - `SummarizeToolResult` signature `(toolName, result, maxChars)` is used consistently. ✓
