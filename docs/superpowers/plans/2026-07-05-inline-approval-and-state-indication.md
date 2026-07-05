# Inline Approval and State Indication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render the command-approval prompt inline at the bottom of the transcript (instead of taking over the chat panel) and add clear visual distinctions for agent state — thinking, running a tool, running a shell command, awaiting approval, and delivering a final answer.

**Architecture:** All live state flows through `session.State` mutation, polled by the TUI's existing 150ms `agentTickMsg`. No new `tea.Msg` types, no `tea.Program` reference in the runner. The TUI remains rendering-only per CLAUDE.md. New `ActiveToolCall` state on `session.State` mirrors the existing `pendingApproval` pattern. A new `Final` flag on `Message` marks terminal answers. A new full-width state strip renders between chat and input.

**Tech Stack:** Go 1.22+, Bubble Tea (bubbletea), Lipgloss, SQLite (modernc.org/sqlite)

## Global Constraints

- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter dependency)
- Binary is named `marshal`; entrypoint is `cmd/marshal/main.go`
- TUI is rendering-only — no policy/routing/prompt logic may move into `internal/app/tui/`
- Agent → TUI communication is via `session.State` mutation + 150ms tick polling, not tea.Msgs
- Tests: `go test ./...`; format: `gofmt -w .`; vet: `go vet ./...`
- No comments in code unless explicitly requested by the user

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/app/session/session.go` | Add `ActiveToolCall` struct + accessors; add `Final` field to `Message` |
| `internal/agent/runner.go` | Set/clear `ActiveToolCall` around tool execution; set `Final=true` on terminal answer |
| `internal/agent/toolargs.go` | **New**: compact args-summary helper per tool name |
| `internal/app/tui/model.go` | Remove `renderApprovalArea` chat-takeover; append approval + live tool-call block in `refreshViewport`; add state strip in `View`; simplify status bar; distinct `Final` rendering; extend dirty-tracking; adjust `resize` |
| `internal/app/tui/renderers.go` | Add `renderFinalAnswer` (cyan left border + "Response" label) |
| `internal/db/sessions.go` | Add `Final` to `db.Message`; update `SaveMessage`/`GetMessages` queries |
| `internal/db/migrations.go` | Add `final` column to schema + migration |
| `internal/db/db.go` | Add `final` to the `messageColumnDefs` migration map |

---

## Task 1: Add `ActiveToolCall` state to `session.State`

**Files:**
- Modify: `internal/app/session/session.go:53-60` (add `Final` to `Message`), `:111-136` (add `activeToolCall` field), `:269-279` (add accessors after `PendingApproval`)
- Test: `internal/app/session/session_test.go`

**Interfaces:**
- Produces: `ActiveToolCall` struct, `State.SetActiveToolCall`, `State.ActiveToolCall`, `State.ClearActiveToolCall`, `Message.Final` field

- [ ] **Step 1: Write the failing test**

Create or append to `internal/app/session/session_test.go`:

```go
package session

import (
	"testing"
	"time"
)

func TestActiveToolCallSetAndGet(t *testing.T) {
	state := New(DefaultConfigForTest(), "/repo", time.Unix(100, 0), Persistence{})
	atc := ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Unix(200, 0),
	}
	state.SetActiveToolCall(atc)
	got, ok := state.ActiveToolCall()
	if !ok {
		t.Fatal("ActiveToolCall() returned ok=false, want true")
	}
	if got.Name != "shell.run" || got.Args != "go test ./..." {
		t.Fatalf("ActiveToolCall() = %+v, want {Name: shell.run, Args: go test ./...}", got)
	}
	if !got.StartedAt.Equal(time.Unix(200, 0)) {
		t.Fatalf("StartedAt = %v, want 200", got.StartedAt)
	}
}

func TestActiveToolCallClear(t *testing.T) {
	state := New(DefaultConfigForTest(), "/repo", time.Unix(100, 0), Persistence{})
	state.SetActiveToolCall(ActiveToolCall{Name: "file.read", Args: "/path"})
	state.ClearActiveToolCall()
	_, ok := state.ActiveToolCall()
	if ok {
		t.Fatal("ActiveToolCall() returned ok=true after ClearActiveToolCall, want false")
	}
}

func TestMessageFinalField(t *testing.T) {
	state := New(DefaultConfigForTest(), "/repo", time.Unix(100, 0), Persistence{})
	state.AddMessage(RoleAssistant, "here is the answer", ContentTypeMarkdown)
	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	if msgs[0].Final {
		t.Fatal("Final = true, want false (AddMessage does not set Final)")
	}
}
```

If `DefaultConfigForTest()` does not exist in the test file, add:

```go
func DefaultConfigForTest() config.Config {
	return config.Default()
}
```

and add `"marshal/internal/app/config"` to the test file's imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/session/ -run TestActiveToolCallSetAndGet -v`
Expected: FAIL — `undefined: ActiveToolCall`

- [ ] **Step 3: Write minimal implementation**

In `internal/app/session/session.go`, add the `ActiveToolCall` struct after `PendingToolCall` (around line 86):

```go
type ActiveToolCall struct {
	Name      string
	Args      string
	StartedAt time.Time
}
```

Add `activeToolCall *ActiveToolCall` field to the `State` struct (after `pendingApproval` at line 126):

```go
	pendingApproval *PendingToolCall
	activeToolCall  *ActiveToolCall
```

Add the `Final` field to the `Message` struct (after `CreatedAt` at line 59):

```go
type Message struct {
	Role          Role
	Content       string
	ContentType   ContentType
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
	Final         bool
}
```

Add the accessor methods after `PendingApproval()` (after line 279):

```go
func (s *State) SetActiveToolCall(atc ActiveToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeToolCall = &atc
}

func (s *State) ActiveToolCall() (ActiveToolCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeToolCall == nil {
		return ActiveToolCall{}, false
	}
	return *s.activeToolCall, true
}

func (s *State) ClearActiveToolCall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeToolCall = nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/session/ -run 'TestActiveToolCall|TestMessageFinalField' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): add ActiveToolCall state and Final flag to Message"
```

---

## Task 2: Add compact args-summary helper for tool calls

**Files:**
- Create: `internal/agent/toolargs.go`
- Test: `internal/agent/toolargs_test.go`

**Interfaces:**
- Consumes: `registry.ToolCall` (from `internal/tools/registry`)
- Produces: `SummarizeToolArgs(toolName string, args json.RawMessage) string`

- [ ] **Step 1: Write the failing test**

Create `internal/agent/toolargs_test.go`:

```go
package agent

import (
	"encoding/json"
	"testing"
)

func TestSummarizeToolArgsShellRun(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "go test ./..."})
	got := SummarizeToolArgs("shell.run", args)
	if got != "go test ./..." {
		t.Fatalf("SummarizeToolArgs(shell.run) = %q, want %q", got, "go test ./...")
	}
}

func TestSummarizeToolArgsTestRun(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "npm test"})
	got := SummarizeToolArgs("test.run", args)
	if got != "npm test" {
		t.Fatalf("SummarizeToolArgs(test.run) = %q, want %q", got, "npm test")
	}
}

func TestSummarizeToolArgsFileRead(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"path": "/repo/main.go"})
	got := SummarizeToolArgs("file.read", args)
	if got != "/repo/main.go" {
		t.Fatalf("SummarizeToolArgs(file.read) = %q, want %q", got, "/repo/main.go")
	}
}

func TestSummarizeToolArgsRepoSearch(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"query": "func main", "pattern": "*.go"})
	got := SummarizeToolArgs("repo.search", args)
	if got != "func main" {
		t.Fatalf("SummarizeToolArgs(repo.search) = %q, want %q", got, "func main")
	}
}

func TestSummarizeToolArgsEmptyArgs(t *testing.T) {
	got := SummarizeToolArgs("unknown.tool", nil)
	if got != "" {
		t.Fatalf("SummarizeToolArgs(nil args) = %q, want empty", got)
	}
}

func TestSummarizeToolArgsPatch(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": "<<<<\nold\n====\nnew\n>>>>"})
	got := SummarizeToolArgs("file.write_patch", args)
	if got != "patch" {
		t.Fatalf("SummarizeToolArgs(file.write_patch) = %q, want %q", got, "patch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestSummarizeToolArgs -v`
Expected: FAIL — `undefined: SummarizeToolArgs`

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/toolargs.go`:

```go
package agent

import "encoding/json"

func SummarizeToolArgs(toolName string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	switch toolName {
	case "shell.run", "test.run":
		if c, ok := m["command"].(string); ok {
			return c
		}
		return ""
	case "file.read":
		if p, ok := m["path"].(string); ok {
			return p
		}
		return ""
	case "repo.search":
		if q, ok := m["query"].(string); ok {
			return q
		}
		return ""
	case "file.write_patch":
		return "patch"
	default:
		for _, v := range m {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestSummarizeToolArgs -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/toolargs.go internal/agent/toolargs_test.go
git commit -m "feat(agent): add SummarizeToolArgs helper for live tool-call display"
```

---

## Task 3: Wire `ActiveToolCall` into the runner

**Files:**
- Modify: `internal/agent/runner.go:205-210` (set `Final=true` on terminal answer), `:451-459` (set/clear `ActiveToolCall` around handler)

**Interfaces:**
- Consumes: `session.ActiveToolCall`, `session.Message.Final`, `SummarizeToolArgs` (from Task 1 & 2)
- Produces: runner that sets live tool-call state and marks final answers

- [ ] **Step 1: Write the failing test**

Create or append to `internal/agent/runner_test.go` (if it doesn't exist, create it). This test uses a fake provider + fake tool to verify that `ActiveToolCall` is set during tool execution and cleared after, and that the terminal answer is marked `Final`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

func TestRunnerSetsAndClearsActiveToolCall(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	toolCalled := make(chan struct{}, 1)
	tool := registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			atc, ok := state.ActiveToolCall()
			if !ok {
				t.Error("ActiveToolCall not set during tool handler execution")
			}
			if atc.Name != "file.read" {
				t.Errorf("ActiveToolCall.Name = %q, want file.read", atc.Name)
			}
			toolCalled <- struct{}{}
			return registry.ToolResult{Summary: "read ok", Content: "file contents"}, nil
		},
	}
	reg := registry.New()
	reg.Register(tool)

	provider := &fakeProvider{
		responses: []string{
			`{"type":"tool_call","tool":"file.read","args":{"path":"/repo/main.go"}}`,
			`{"type":"answer","content":"done"}`,
		},
	}
	runner := NewRunner(provider, reg, nil, state, "test-model")
	runner.Now = func() time.Time { return time.Unix(150, 0) }

	if err := runner.Run(context.Background(), "read the file"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	select {
	case <-toolCalled:
	default:
		t.Fatal("tool handler was never called")
	}

	_, ok := state.ActiveToolCall()
	if ok {
		t.Fatal("ActiveToolCall still set after Run completed, want cleared")
	}
}

func TestRunnerMarksFinalAnswer(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	provider := &fakeProvider{
		responses: []string{
			`{"type":"answer","content":"here is the answer"}`,
		},
	}
	runner := NewRunner(provider, registry.New(), nil, state, "test-model")

	if err := runner.Run(context.Background(), "what is 2+2?"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	msgs := state.Messages()
	var answer session.Message
	found := false
	for _, m := range msgs {
		if m.Role == session.RoleAssistant && m.Content == "here is the answer" {
			answer = m
			found = true
		}
	}
	if !found {
		t.Fatal("final answer message not found in state")
	}
	if !answer.Final {
		t.Fatal("answer message Final = false, want true")
	}
}

type fakeProvider struct {
	responses []string
	callIdx   int
}

func (f *fakeProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	ch := make(chan schema.ChatEvent, 2)
	go func() {
		defer close(ch)
		if f.callIdx < len(f.responses) {
			ch <- schema.ChatEvent{Type: schema.ChatEventDone, Delta: f.responses[f.callIdx]}
			f.callIdx++
		}
	}()
	return ch, nil
}
```

Note: If `fakeProvider` already exists in `runner_test.go`, do not redefine it — just use the existing one. Check the file first.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run 'TestRunnerSetsAndClearsActiveToolCall|TestRunnerMarksFinalAnswer' -v`
Expected: FAIL — `ActiveToolCall not set during tool handler execution` and/or `answer message Final = false`

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/runner.go`, modify the terminal answer branch in `Run` (line 205-210). Change:

```go
	case ActionAnswer, ActionFinal:
		task.Summary = action.Content
		task.Status = TaskStatusCompleted
		r.State.AddMessage(session.RoleAssistant, action.Content, session.ContentTypeMarkdown)
		return nil
```

to:

```go
	case ActionAnswer, ActionFinal:
		task.Summary = action.Content
		task.Status = TaskStatusCompleted
		r.State.AddMessageFinal(session.RoleAssistant, action.Content, session.ContentTypeMarkdown)
		return nil
```

In `internal/agent/runner.go`, modify the tool execution section in `executeToolCall` (lines 451-459). Change:

```go
	label := toolName
	if command, ok := argsMap["command"].(string); ok && command != "" {
		label = fmt.Sprintf("%s: %s", toolName, command)
	}
	r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
	result, execErr := tool.Handler(ctx, call)
```

to:

```go
	label := toolName
	if command, ok := argsMap["command"].(string); ok && command != "" {
		label = fmt.Sprintf("%s: %s", toolName, command)
	}
	r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
	r.State.SetActiveToolCall(session.ActiveToolCall{
		Name:      toolName,
		Args:      SummarizeToolArgs(toolName, args),
		StartedAt: r.Now(),
	})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
	defer r.State.ClearActiveToolCall()

	call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
	result, execErr := tool.Handler(ctx, call)
```

In `internal/app/session/session.go`, add the `AddMessageFinal` method after `AddMessage` (after line 188):

```go
func (s *State) AddMessageFinal(role Role, content string, contentType ContentType) {
	s.mu.Lock()
	reasoning := s.inProgress.Reasoning
	var thinkDuration time.Duration
	if reasoning != "" {
		thinkDuration = time.Since(s.inProgress.StartedAt)
		if thinkDuration <= 0 {
			thinkDuration = time.Millisecond
		}
	}
	s.inProgress = InProgressMessage{}

	msg := Message{
		Role:          role,
		Content:       content,
		ContentType:   contentType,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     time.Now(),
		Final:         true,
	}
	s.messages = append(s.messages, msg)
	s.mu.Unlock()

	if s.persistenceEnabled() {
		if err := s.db.SaveMessage(s.sessionID, string(role), content, string(contentType), msg.CreatedAt, reasoning, thinkDuration, true); err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
		}
	}
}
```

Note: This calls `SaveMessage` with a new `final` parameter — that change is in Task 5. For now, the test only checks in-memory state; the persistence path is nil (`Persistence{}`) in tests, so the signature mismatch won't matter. **Wait — Go won't compile with a signature mismatch even if the path isn't taken.** Instead, defer the `SaveMessage` signature change to Task 5. For now, keep `AddMessageFinal` calling the existing `SaveMessage` signature (without the `final` param) and add a `// TODO: persist Final flag in Task 5` comment. Actually, per the no-comments rule, just call the existing signature. The `final` column migration in Task 5 will update this.

Revised `AddMessageFinal` — call existing `SaveMessage` (without `final` param for now):

```go
func (s *State) AddMessageFinal(role Role, content string, contentType ContentType) {
	s.mu.Lock()
	reasoning := s.inProgress.Reasoning
	var thinkDuration time.Duration
	if reasoning != "" {
		thinkDuration = time.Since(s.inProgress.StartedAt)
		if thinkDuration <= 0 {
			thinkDuration = time.Millisecond
		}
	}
	s.inProgress = InProgressMessage{}

	msg := Message{
		Role:          role,
		Content:       content,
		ContentType:   contentType,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     time.Now(),
		Final:         true,
	}
	s.messages = append(s.messages, msg)
	s.mu.Unlock()

	if s.persistenceEnabled() {
		if err := s.db.SaveMessage(s.sessionID, string(role), content, string(contentType), msg.CreatedAt, reasoning, thinkDuration); err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run 'TestRunnerSetsAndClearsActiveToolCall|TestRunnerMarksFinalAnswer' -v`
Expected: PASS

- [ ] **Step 5: Run full test suite to check for regressions**

Run: `go test ./...`
Expected: PASS (all packages)

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go internal/app/session/session.go
git commit -m "feat(agent): set ActiveToolCall during tool execution and mark final answers"
```

---

## Task 4: Inline approval rendering (remove chat-takeover)

**Files:**
- Modify: `internal/app/tui/model.go:511-536` (`refreshViewport` — append approval block), `:993-1004` (`renderChatPanel` — remove takeover branch), `:1006-1054` (`renderApprovalArea` — repurpose as inline block renderer)

**Interfaces:**
- Consumes: `session.PendingToolCall` (existing)
- Produces: approval rendered inline at bottom of transcript

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/model_test.go`:

```go
func TestApprovalRendersInlineInChat(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	state.AddMessage(session.RoleUser, "run the tests", session.ContentTypePlain)
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call_1",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		Reason:       "needs confirmation",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})

	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "go test ./...") {
		t.Fatalf("View() does not contain the approval command:\n%s", view)
	}
	if !strings.Contains(view, "Approval") {
		t.Fatalf("View() does not contain the Approval panel title:\n%s", view)
	}
	if !strings.Contains(view, "run the tests") {
		t.Fatalf("View() does not contain the prior user message (approval took over chat):\n%s", view)
	}
}

func TestApprovalKeyHandlingStillWorksInline(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	ch := make(chan session.UserApprovalDecision, 1)
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call_1",
		Name:         "shell.run",
		Command:      "echo hi",
		Risk:         "command",
		Reason:       "needs confirmation",
		ResponseChan: ch,
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	select {
	case decision := <-ch:
		if !decision.Approved {
			t.Fatal("approval decision = false, want true (Enter should approve)")
		}
	default:
		t.Fatal("no decision sent on ResponseChan after Enter")
	}
	if state.PendingApproval() != nil {
		t.Fatal("PendingApproval still set after Enter, want nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestApprovalRendersInlineInChat|TestApprovalKeyHandlingStillWorksInline' -v`
Expected: FAIL — `View() does not contain the prior user message` (because `renderApprovalArea` replaces the chat)

- [ ] **Step 3: Write minimal implementation**

In `internal/app/tui/model.go`, modify `refreshViewport` (line 511-536). After the `inProgress.Active` thinking box block (line 531-533), add approval block rendering. Replace:

```go
	if inProgress.Active {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.viewport.Width))
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
```

with:

```go
	if inProgress.Active {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.viewport.Width))
	}
	if tc := m.state.PendingApproval(); tc != nil {
		b.WriteString(renderApprovalInline(tc, m.viewport.Width))
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
```

Modify `renderChatPanel` (line 993-1004). Remove the `tc != nil` takeover branch. Replace:

```go
func (m Model) renderChatPanel(tc *session.PendingToolCall) string {
	if err := m.state.ProviderError(); err != nil {
		body := lipgloss.NewStyle().
			Foreground(errorColor).
			Render("! " + truncateRunes(err.Error(), max(m.leftWidth-2, 1)))
		return renderPanel("Provider Error", "fits AltScreen", body, m.leftWidth, m.chatHeight)
	}
	if tc != nil {
		return m.renderApprovalArea(tc)
	}
	return renderPanel("Chat", "live transcript", m.viewport.View(), m.leftWidth, m.chatHeight)
}
```

with:

```go
func (m Model) renderChatPanel(tc *session.PendingToolCall) string {
	if err := m.state.ProviderError(); err != nil {
		body := lipgloss.NewStyle().
			Foreground(errorColor).
			Render("! " + truncateRunes(err.Error(), max(m.leftWidth-2, 1)))
		return renderPanel("Provider Error", "fits AltScreen", body, m.leftWidth, m.chatHeight)
	}
	return renderPanel("Chat", "live transcript", m.viewport.View(), m.leftWidth, m.chatHeight)
}
```

Replace the entire `renderApprovalArea` function (line 1006-1054) with the new inline renderer:

```go
func renderApprovalInline(tc *session.PendingToolCall, width int) string {
	if width < 10 {
		width = 10
	}
	helpLine := "Enter approve · d deny · e edit · a always"
	innerWidth := max(width-2, 1)

	var b strings.Builder
	b.WriteString(panelTitleStyle.Foreground(warningColor).Render("⚠ Approval needed"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("Agent wants to run:"))
	b.WriteString("\n")
	b.WriteString(truncateRunes(tc.Command, innerWidth))
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render("Risk: "))
	b.WriteString(truncateRunes(riskText(tc), innerWidth))
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render(helpLine))

	style := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(warningColor).
		Padding(0, 1)
	return style.Render(b.String()) + "\n\n"
}
```

Also update the dirty-tracking in `refreshViewport` (line 515-517). The current check:

```go
	if len(messages) == m.lastMessageCount && streamLen == m.lastStreamLen && !m.busy {
		return
	}
```

needs to also rebuild when approval state changes. Replace with:

```go
	hasApproval := m.state.PendingApproval() != nil
	if len(messages) == m.lastMessageCount && streamLen == m.lastStreamLen && !m.busy && hasApproval == m.lastHadApproval {
		return
	}
	m.lastMessageCount = len(messages)
	m.lastStreamLen = streamLen
	m.lastHadApproval = hasApproval
```

Add `lastHadApproval bool` field to the `Model` struct (after `lastStreamLen` at line 81):

```go
	lastMessageCount  int
	lastStreamLen     int
	lastHadApproval   bool
	thinkingExpanded  bool
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/ -run 'TestApprovalRendersInlineInChat|TestApprovalKeyHandlingStillWorksInline' -v`
Expected: PASS

- [ ] **Step 5: Run full TUI test suite**

Run: `go test ./internal/app/tui/... -v`
Expected: PASS (note: some existing approval-related tests may need updating if they checked the old `renderApprovalArea` layout — fix any failures by updating assertions to match the new inline layout)

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): render approval prompt inline at bottom of transcript"
```

---

## Task 5: Persist `Final` flag to SQLite

**Files:**
- Modify: `internal/db/migrations.go:32-41` (add `final` column to schema), `internal/db/db.go:73-89` (add to migration map), `internal/db/sessions.go:10-18` (add to `Message` struct), `:94-116` (update `SaveMessage`), `:119-162` (update `GetMessages`), `internal/app/session/session.go` (`AddMessageFinal` — update `SaveMessage` call)

**Interfaces:**
- Consumes: `Message.Final` from Task 1
- Produces: `final` column persisted and reloaded

- [ ] **Step 1: Write the failing test**

Append to `internal/db/sessions_test.go`:

```go
func TestSaveMessageWithFinalFlag(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	sessionID := createTestSession(t, db)

	now := time.Now().UTC()
	if err := db.SaveMessage(sessionID, "assistant", "the answer", "markdown", now, "", 0, true); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	if !msgs[0].Final {
		t.Fatal("Final = false, want true")
	}
}

func TestSaveMessageWithoutFinalFlag(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	sessionID := createTestSession(t, db)

	now := time.Now().UTC()
	if err := db.SaveMessage(sessionID, "user", "hello", "plain", now, "", 0, false); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	if msgs[0].Final {
		t.Fatal("Final = true, want false")
	}
}
```

If `newTestDB` and `createTestSession` helpers don't exist, add them:

```go
func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return db
}

func createTestSession(t *testing.T, db *DB) string {
	t.Helper()
	sessionID := "test-session-1"
	if err := db.CreateSession(sessionID, 1, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return sessionID
}
```

Note: check if these helpers already exist in the test file first. Also, `CreateSession` requires a project to exist — check if `GetOrCreateProject` is called first in existing tests and follow that pattern.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run 'TestSaveMessageWithFinalFlag|TestSaveMessageWithoutFinalFlag' -v`
Expected: FAIL — `too many call arguments` or `not enough arguments in call to SaveMessage` (signature mismatch)

- [ ] **Step 3: Write minimal implementation**

In `internal/db/migrations.go`, add `final INTEGER DEFAULT 0` to the `messages` table schema (after `created_at`):

```sql
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    content_type TEXT,
    reasoning TEXT,
    think_duration_ms INTEGER,
    created_at TEXT NOT NULL,
    final INTEGER DEFAULT 0
);
```

In `internal/db/db.go`, add `"final": "INTEGER DEFAULT 0"` to the `messageColumnDefs` map (around line 77):

```go
	messageColumnDefs := map[string]string{
		"reasoning":         "TEXT",
		"think_duration_ms": "INTEGER",
		"final":             "INTEGER DEFAULT 0",
	}
```

In `internal/db/sessions.go`, add `Final bool` to the `db.Message` struct (after `CreatedAt`):

```go
type Message struct {
	ID              int64
	Role            string
	Content         string
	ContentType     string
	Reasoning       string
	ThinkDurationMs int64
	CreatedAt       time.Time
	Final           bool
}
```

Update `SaveMessage` signature to accept `final bool`:

```go
func (db *DB) SaveMessage(sessionID string, role string, content string, contentType string, createdAt time.Time, reasoning string, thinkDuration time.Duration, final bool) error {
	var reasoningArg sql.NullString
	if reasoning != "" {
		reasoningArg = sql.NullString{String: reasoning, Valid: true}
	}
	var thinkDurationArg sql.NullInt64
	if thinkDuration > 0 {
		thinkDurationArg = sql.NullInt64{Int64: thinkDuration.Milliseconds(), Valid: true}
	}
	var contentTypeArg sql.NullString
	if contentType != "" && contentType != "plain" {
		contentTypeArg = sql.NullString{String: contentType, Valid: true}
	}
	_, err := db.exec(
		`INSERT INTO messages (session_id, role, content, content_type, reasoning, think_duration_ms, created_at, final)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, role, content, contentTypeArg, reasoningArg, thinkDurationArg, createdAt.UTC().Format(time.RFC3339), final,
	)
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}
```

Update `GetMessages` to select and scan the `final` column. Change the query to:

```go
	rows, err := db.sqlDB.Query(
		`SELECT id, role, content, content_type, reasoning, think_duration_ms, created_at, final
		 FROM messages
		 WHERE session_id = ?
		 ORDER BY id ASC`,
		sessionID,
	)
```

Add `var final sql.NullInt64` to the scan variables and `m.Final = final.Valid && final.Int64 != 0` after the scan:

```go
	for rows.Next() {
		var m Message
		var created string
		var reasoning sql.NullString
		var thinkDurationMs sql.NullInt64
		var contentType sql.NullString
		var final sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &contentType, &reasoning, &thinkDurationMs, &created, &final); err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		if contentType.Valid {
			m.ContentType = contentType.String
		}
		if reasoning.Valid {
			m.Reasoning = reasoning.String
		}
		if thinkDurationMs.Valid {
			m.ThinkDurationMs = thinkDurationMs.Int64
		}
		m.Final = final.Valid && final.Int64 != 0
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		m.CreatedAt = parsed.UTC()
		messages = append(messages, m)
	}
```

Update all existing `SaveMessage` callers to pass `false` as the final argument. Search for all call sites:

Run: `grep -rn 'SaveMessage(' --include='*.go' .`

Update each call to add `false` (or `true` for final answers) as the last argument. Specifically:
- `internal/app/session/session.go` `AddMessage` (line 184): change `SaveMessage(..., reasoning, thinkDuration)` to `SaveMessage(..., reasoning, thinkDuration, false)`
- `internal/app/session/session.go` `AddMessageFinal` (from Task 3): change `SaveMessage(..., reasoning, thinkDuration)` to `SaveMessage(..., reasoning, thinkDuration, true)`
- `internal/db/sessions_test.go`: update existing test calls to add the `false`/`true` argument
- Any other callers found by grep

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run 'TestSaveMessage' -v`
Expected: PASS

- [ ] **Step 5: Run full test suite for regressions**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/db/ internal/app/session/session.go
git commit -m "feat(db): persist Final flag on messages with additive migration"
```

---

## Task 6: Live tool-call block in transcript

**Files:**
- Modify: `internal/app/tui/model.go:511-536` (`refreshViewport` — append live tool-call block), `:515-517` (dirty-tracking)

**Interfaces:**
- Consumes: `session.ActiveToolCall` (from Task 1), `m.spinnerFrame` (existing)
- Produces: inline live tool-call block at bottom of transcript during tool execution

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/model_test.go`:

```go
func TestActiveToolCallRendersInline(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true
	m.spinnerFrame = "⠹"

	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Unix(100, 0),
	})
	m.now = func() time.Time { return time.Unix(103, 0) }

	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "shell.run") {
		t.Fatalf("View() does not show active tool name:\n%s", view)
	}
	if !strings.Contains(view, "go test ./...") {
		t.Fatalf("View() does not show active tool args:\n%s", view)
	}
}

func TestActiveToolCallClearsFromView(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true

	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "file.read",
		Args:      "/repo/main.go",
		StartedAt: time.Unix(100, 0),
	})
	m.refreshViewport()
	viewWithTool := m.View()

	state.ClearActiveToolCall()
	m.lastMessageCount = -1
	m.refreshViewport()
	viewWithoutTool := m.View()

	if strings.Contains(viewWithoutTool, "/repo/main.go") && !strings.Contains(viewWithTool, "/repo/main.go") {
		t.Fatalf("tool-call block did not clear from view")
	}
}
```

Note: This test requires a `now` field on the Model. If `Model` doesn't have a `now` field, add one (see Task 7 where it's added — but for this test, use a simpler approach: just check the tool name appears, not the elapsed time). Simplify the first test to remove the `m.now` line if the field doesn't exist yet:

```go
func TestActiveToolCallRendersInline(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true
	m.spinnerFrame = "⠹"

	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Now(),
	})

	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "shell.run") {
		t.Fatalf("View() does not show active tool name:\n%s", view)
	}
	if !strings.Contains(view, "go test ./...") {
		t.Fatalf("View() does not show active tool args:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestActiveToolCallRendersInline|TestActiveToolCallClearsFromView' -v`
Expected: FAIL — `View() does not show active tool name` (not rendered yet)

- [ ] **Step 3: Write minimal implementation**

Add a `now` field to the `Model` struct (after `lastActivityKind` at line 88):

```go
	lastActivityKind  session.ActivityKind
	now               func() time.Time
```

Initialize it in `New` (after line 148, inside the `Model{...}` literal):

```go
		spinner:        NewSpinner(),
		now:             time.Now,
```

Add the live tool-call block renderer after `renderThinkingBox` (around line 734):

```go
func renderActiveToolCall(atc session.ActiveToolCall, spinnerFrame string, now time.Time, width int) string {
	if width < 10 {
		width = 10
	}
	innerWidth := max(width-2, 1)
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	elapsedStr := formatElapsed(elapsed)
	label := fmt.Sprintf("%s %s", spinnerFrame, atc.Name)
	line := fmt.Sprintf("%s  %s  · %s", label, truncateRunes(atc.Args, innerWidth-len(label)-len(elapsedStr)-6), elapsedStr)
	style := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(warningColor).
		Foreground(warningColor).
		Padding(0, 1)
	return style.Render(line) + "\n\n"
}

func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}
```

In `refreshViewport`, after the approval block check and before `SetContent`, add the active tool-call block. The current code (after Task 4) looks like:

```go
	if tc := m.state.PendingApproval(); tc != nil {
		b.WriteString(renderApprovalInline(tc, m.viewport.Width))
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
```

Replace with:

```go
	if tc := m.state.PendingApproval(); tc != nil {
		b.WriteString(renderApprovalInline(tc, m.viewport.Width))
	}
	if atc, ok := m.state.ActiveToolCall(); ok {
		b.WriteString(renderActiveToolCall(atc, m.spinnerFrame, m.now(), m.viewport.Width))
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
```

Update the dirty-tracking to also check `ActiveToolCall` state. After Task 4, the check is:

```go
	hasApproval := m.state.PendingApproval() != nil
	if len(messages) == m.lastMessageCount && streamLen == m.lastStreamLen && !m.busy && hasApproval == m.lastHadApproval {
		return
	}
```

Since live tool-call state only changes while `m.busy` is true, and the existing `!m.busy` early-return already forces rebuilds while busy, this is sufficient — the tick re-renders every 150ms while busy. No additional dirty-tracking is needed for the tool-call block because its elapsed time updates on every tick render anyway.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/ -run 'TestActiveToolCallRendersInline|TestActiveToolCallClearsFromView' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): render live tool-call block inline in transcript"
```

---

## Task 7: Live command execution indicator (expanded panel for shell/test)

**Files:**
- Modify: `internal/app/tui/model.go` (`renderActiveToolCall` — expand for shell.run/test.run)

**Interfaces:**
- Consumes: `session.ActiveToolCall` with `Name` = "shell.run" or "test.run"
- Produces: 2-line panel with command + elapsed for command tools

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/model_test.go`:

```go
func TestShellCommandShowsExpandedPanel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true
	m.spinnerFrame = "⠹"

	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Now(),
	})
	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "$ go test ./...") {
		t.Fatalf("View() does not show command with $ prefix:\n%s", view)
	}
}

func TestNonCommandToolShowsSingleLine(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true
	m.spinnerFrame = "⠹"

	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "file.read",
		Args:      "/repo/main.go",
		StartedAt: time.Now(),
	})
	m.refreshViewport()
	view := m.View()

	if strings.Contains(view, "$ /repo/main.go") {
		t.Fatalf("file.read should not show $ prefix (single-line only):\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestShellCommandShowsExpandedPanel|TestNonCommandToolShowsSingleLine' -v`
Expected: FAIL — `View() does not show command with $ prefix`

- [ ] **Step 3: Write minimal implementation**

Modify `renderActiveToolCall` (added in Task 6) to branch on tool name. Replace the entire function with:

```go
func renderActiveToolCall(atc session.ActiveToolCall, spinnerFrame string, now time.Time, width int) string {
	if width < 10 {
		width = 10
	}
	innerWidth := max(width-2, 1)
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	elapsedStr := formatElapsed(elapsed)

	if atc.Name == "shell.run" || atc.Name == "test.run" {
		return renderCommandToolCall(atc, spinnerFrame, elapsedStr, innerWidth)
	}
	return renderSimpleToolCall(atc, spinnerFrame, elapsedStr, innerWidth)
}

func renderCommandToolCall(atc session.ActiveToolCall, spinnerFrame, elapsedStr string, innerWidth int) string {
	label := fmt.Sprintf("%s %s", spinnerFrame, atc.Name)
	header := fmt.Sprintf("%s  · %s", label, elapsedStr)
	cmdLine := fmt.Sprintf("$ %s", truncateRunes(atc.Args, innerWidth-2))
	body := header + "\n" + cmdLine
	style := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(warningColor).
		Foreground(warningColor).
		Padding(0, 1)
	return style.Render(body) + "\n\n"
}

func renderSimpleToolCall(atc session.ActiveToolCall, spinnerFrame, elapsedStr string, innerWidth int) string {
	label := fmt.Sprintf("%s %s", spinnerFrame, atc.Name)
	argsBudget := innerWidth - len(label) - len(elapsedStr) - 6
	if argsBudget < 1 {
		argsBudget = 1
	}
	line := fmt.Sprintf("%s  %s  · %s", label, truncateRunes(atc.Args, argsBudget), elapsedStr)
	style := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(warningColor).
		Foreground(warningColor).
		Padding(0, 1)
	return style.Render(line) + "\n\n"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/ -run 'TestShellCommandShowsExpandedPanel|TestNonCommandToolShowsSingleLine' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): expand live panel for shell/test commands with $ prefix"
```

---

## Task 8: Prominent state strip between chat and input

**Files:**
- Modify: `internal/app/tui/model.go:42-43` (layout constant), `:160-215` (`resize` — subtract strip row when active), `:835-883` (`renderStatusBar` — simplify busy cell), `:960-991` (`View` — add state strip), new `renderStateStrip` function

**Interfaces:**
- Consumes: `session.Activity()`, `session.ActiveToolCall()`, `session.PendingApproval()`, `m.spinnerFrame`
- Produces: full-width colored strip between chat and input

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/model_test.go`:

```go
func TestStateStripShowsThinking(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true
	m.spinnerFrame = "⠹"
	state.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: time.Now()})

	view := m.View()
	if !strings.Contains(view, "thinking") {
		t.Fatalf("View() does not show thinking state strip:\n%s", view)
	}
}

func TestStateStripShowsApproval(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call_1",
		Name:         "shell.run",
		Command:      "echo hi",
		Risk:         "command",
		Reason:       "needs confirmation",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})

	view := m.View()
	if !strings.Contains(strings.ToLower(view), "approval") {
		t.Fatalf("View() does not show approval state strip:\n%s", view)
	}
}

func TestStateStripHiddenWhenIdle(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	view := m.View()
	chatPanelIdx := strings.Index(view, "Chat")
	if chatPanelIdx == -1 {
		t.Fatal("View() does not contain Chat panel")
	}
	if strings.Contains(view, "thinking") {
		t.Fatalf("View() shows thinking strip when idle:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestStateStripShows' -v`
Expected: FAIL — `View() does not show thinking state strip`

- [ ] **Step 3: Write minimal implementation**

Add a layout constant for the strip (after `chatBelowViewportRows` at line 43):

```go
	chatBelowViewportRows       = 4  // bordered input box (3) + help line (1)
	stateStripRows              = 1  // colored activity strip, shown only when active
```

Add `stateStripActive bool` field to `Model` (after `busy`):

```go
	busy            bool
	stateStripActive bool
```

Modify `resize` to compute `stateStripActive` and adjust `chatHeight`. In `resize` (around line 200), replace:

```go
	m.chatHeight = m.contentHeight - chatBelowViewportRows
	if m.chatHeight < 1 {
		m.chatHeight = 1
	}
```

with:

```go
	m.stateStripActive = m.state.Activity().Kind != session.ActivityIdle ||
		m.state.PendingApproval() != nil
	stripRows := 0
	if m.stateStripActive {
		stripRows = stateStripRows
	}
	m.chatHeight = m.contentHeight - chatBelowViewportRows - stripRows
	if m.chatHeight < 1 {
		m.chatHeight = 1
	}
```

Add the `renderStateStrip` method after `renderStatusBar` (around line 883):

```go
func (m Model) renderStateStrip(width int) string {
	if !m.stateStripActive {
		return ""
	}
	activity := m.state.Activity()
	tc := m.state.PendingApproval()

	var text string
	var bg lipgloss.Color

	switch {
	case tc != nil:
		text = fmt.Sprintf("⚠ awaiting approval · %s", truncateRunes(tc.Name, width-30))
		bg = errorColor
	case activity.Kind == session.ActivityThinking:
		text = fmt.Sprintf("%s thinking...", m.spinnerFrame)
		bg = accentColor
	case activity.Kind == session.ActivityTool:
		if atc, ok := m.state.ActiveToolCall(); ok {
			text = fmt.Sprintf("%s running %s · %s", m.spinnerFrame, atc.Name, truncateRunes(atc.Args, width-len(atc.Name)-20))
		} else {
			text = fmt.Sprintf("%s %s", m.spinnerFrame, truncateRunes(activity.Label, width-4))
		}
		bg = warningColor
	default:
		return ""
	}

	return lipgloss.NewStyle().
		Width(max(width, 1)).
		MaxWidth(max(width, 1)).
		Background(bg).
		Foreground(lipgloss.Color("0")).
		Bold(true).
		Padding(0, 1).
		Render(" " + truncateRunes(text, width-2))
}
```

Modify `View` (line 960-991) to insert the state strip between chat and input. Replace:

```go
	chatPanel := m.renderChatPanel(tc)
	inputPanel := m.renderInputArea()
	leftColumn := lipgloss.JoinVertical(lipgloss.Left, chatPanel, inputPanel)
```

with:

```go
	chatPanel := m.renderChatPanel(tc)
	stateStrip := m.renderStateStrip(m.leftWidth)
	inputPanel := m.renderInputArea()
	leftColumn := lipgloss.JoinVertical(lipgloss.Left, chatPanel, stateStrip, inputPanel)
```

Simplify the status bar's busy cell. In `renderStatusBar` (line 851-871), replace the `busyText` switch with:

```go
	activity := m.state.Activity()
	var busyText string
	switch {
	case m.state.PendingApproval() != nil:
		busyText = "APPROVAL"
	case activity.Kind != session.ActivityIdle:
		busyText = "ACTIVE"
	case m.lastActivityLabel != "" && time.Since(m.lastActivityDone) < doneDisplayDuration:
		busyText = fmt.Sprintf("✓ %s", truncateRunes(m.lastActivityLabel, 9))
	default:
		busyText = "IDLE"
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/ -run 'TestStateStrip' -v`
Expected: PASS

- [ ] **Step 5: Run full TUI test suite**

Run: `go test ./internal/app/tui/... -v`
Expected: PASS (fix any layout-related test failures by updating expected dimensions)

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): add prominent colored state strip between chat and input"
```

---

## Task 9: Distinct final answer rendering

**Files:**
- Modify: `internal/app/tui/model.go:1074-1089` (`renderMessage` — dispatch `Final` messages), `internal/app/tui/renderers.go` (add `renderFinalAnswer`)

**Interfaces:**
- Consumes: `session.Message.Final` (from Task 1)
- Produces: final answers rendered with cyan left border + "Response" label

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/model_test.go`:

```go
func TestFinalAnswerRendersWithResponseLabel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	state.AddMessageFinal(session.RoleAssistant, "here is the answer", session.ContentTypeMarkdown)

	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "Response") {
		t.Fatalf("View() does not show Response label for final answer:\n%s", view)
	}
}

func TestNonFinalAnswerRendersWithAgentLabel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	state.AddMessage(session.RoleAssistant, "intermediate text", session.ContentTypeMarkdown)

	m.refreshViewport()
	view := m.View()

	if !strings.Contains(view, "assistant") {
		t.Fatalf("View() does not show assistant label for non-final message:\n%s", view)
	}
	if strings.Contains(view, "Response") {
		t.Fatalf("View() shows Response label for non-final message:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestFinalAnswerRendersWithResponseLabel|TestNonFinalAnswerRendersWithAgentLabel' -v`
Expected: FAIL — `View() does not show Response label`

- [ ] **Step 3: Write minimal implementation**

In `internal/app/tui/model.go`, modify `renderMessage` (line 1074-1089) to check `msg.Final` first:

```go
func renderMessage(msg session.Message, width int) string {
	if msg.Final {
		return renderFinalAnswer(msg.Content, width)
	}
	switch msg.ContentType {
	case session.ContentTypeMarkdown:
		return renderMarkdown(string(msg.Role), msg.Content, width)
	case session.ContentTypeCode:
		return renderCode(string(msg.Role), msg.Content, width)
	case session.ContentTypePlan:
		return renderPlan(string(msg.Role), msg.Content, width)
	case session.ContentTypeDiff:
		return renderDiff(string(msg.Role), msg.Content, width)
	case session.ContentTypeToolResult:
		return renderToolResult(string(msg.Role), msg.Content, width)
	default:
		return renderPlain(string(msg.Role), msg.Content, width)
	}
}
```

In `internal/app/tui/renderers.go`, add `renderFinalAnswer` at the end of the file:

```go
func renderFinalAnswer(content string, width int) string {
	if width < 10 {
		width = 10
	}
	prefixWidth := 10
	contentWidth := max(width-prefixWidth-4, 1)
	label := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("Response")

	blocks := splitFencedBlocks(content)
	var b strings.Builder
	b.WriteString(label)
	b.WriteString("  ")
	firstBlock := true

	for _, block := range blocks {
		switch block.kind {
		case "code":
			rendered := renderCodeBlock(block.text, contentWidth)
			codeLines := strings.Split(rendered, "\n")
			for _, line := range codeLines {
				if line == "" {
					continue
				}
				if firstBlock {
					b.WriteString(line)
					b.WriteString("\n")
					firstBlock = false
				} else {
					b.WriteString(strings.Repeat(" ", prefixWidth+2))
					b.WriteString(line)
					b.WriteString("\n")
				}
			}
		case "prose":
			proseLines := strings.Split(block.text, "\n")
			if len(proseLines) == 1 && proseLines[0] == "" {
				continue
			}
			for _, pLine := range proseLines {
				style, transformed := parseMarkdownLine(pLine)
				wrapped := ansi.Wrap(transformed, contentWidth, "")
				wrappedLines := strings.Split(wrapped, "\n")
				for _, wl := range wrappedLines {
					if firstBlock {
						b.WriteString(style.Render(wl))
						b.WriteString("\n")
						firstBlock = false
					} else {
						b.WriteString(strings.Repeat(" ", prefixWidth+2))
						b.WriteString(style.Render(wl))
						b.WriteString("\n")
					}
				}
			}
		}
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.LeftBorder()).
		BorderForeground(accentColor).
		BorderBackground(accentColor).
		PaddingLeft(1)
	return borderStyle.Render(b.String()) + "\n\n"
}
```

Note: `lipgloss.LeftBorder()` renders only a left border. The `BorderBackground` makes the border solid cyan. If `LeftBorder` is not available in the installed lipgloss version, use `lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true)` (left-only). Check the lipgloss version's API.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/ -run 'TestFinalAnswerRendersWithResponseLabel|TestNonFinalAnswerRendersWithAgentLabel' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/renderers.go internal/app/tui/model_test.go
git commit -m "feat(tui): render final answers with cyan border and Response label"
```

---

## Task 10: Final integration test and cleanup

**Files:**
- Modify: `internal/app/tui/model_test.go` (add end-to-end test), run full suite

- [ ] **Step 1: Write an integration test**

Append to `internal/app/tui/model_test.go`:

```go
func TestFullTranscriptRendersAllStates(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(120, 40)
	m.busy = true
	m.spinnerFrame = "⠹"
	m.now = func() time.Time { return time.Unix(105, 0) }

	state.AddMessage(session.RoleUser, "run the tests and tell me the result", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "I'll run the tests for you.", session.ContentTypeMarkdown)
	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Unix(100, 0),
	})

	m.refreshViewport()
	view := m.View()

	mustContain := []string{
		"run the tests",
		"I'll run the tests",
		"shell.run",
		"$ go test ./...",
		"running",
	}
	for _, want := range mustContain {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q:\n%s", want, view)
		}
	}

	state.ClearActiveToolCall()
	state.AddMessage(session.RoleSystem, "command exited 0", session.ContentTypeToolResult)
	state.AddMessageFinal(session.RoleAssistant, "All tests passed.", session.ContentTypeMarkdown)
	m.lastMessageCount = -1
	m.refreshViewport()
	view = m.View()

	if !strings.Contains(view, "Response") {
		t.Errorf("View() missing Response label after final answer:\n%s", view)
	}
	if !strings.Contains(view, "All tests passed.") {
		t.Errorf("View() missing final answer text:\n%s", view)
	}
}
```

- [ ] **Step 2: Run the integration test**

Run: `go test ./internal/app/tui/ -run TestFullTranscriptRendersAllStates -v`
Expected: PASS

- [ ] **Step 3: Run the full test suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 4: Format and vet**

Run: `gofmt -w . && go vet ./...`
Expected: no output (clean)

- [ ] **Step 5: Build**

Run: `CGO_ENABLED=1 go build ./cmd/marshal`
Expected: succeeds, produces `marshal` binary

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model_test.go
git commit -m "test(tui): add integration test for inline approval and state indication"
```

---

## Self-Review

**1. Spec coverage:**
- Section 1 (Inline Approval): Task 4 ✓
- Section 2 (Live Tool-Call Blocks): Task 1 (state) + Task 3 (runner wiring) + Task 6 (rendering) ✓
- Section 3 (Prominent State Pill): Task 8 ✓
- Section 4 (Distinct Final Answer): Task 1 (`Final` field) + Task 5 (persistence) + Task 9 (rendering) ✓
- Section 5 (Live Command Indicator): Task 7 ✓

**2. Placeholder scan:** No TBD/TODO. All steps contain actual code.

**3. Type consistency:**
- `ActiveToolCall` struct: defined in Task 1, used in Task 3 (runner), Task 6 (rendering), Task 7, Task 8 (state strip) — consistent field names `Name`, `Args`, `StartedAt`.
- `SetActiveToolCall(ActiveToolCall)` / `ActiveToolCall() (ActiveToolCall, bool)` / `ClearActiveToolCall()` — consistent across all tasks.
- `Message.Final bool` — defined in Task 1, set in Task 3, persisted in Task 5, rendered in Task 9.
- `AddMessageFinal` — defined in Task 3, used in Task 9 and Task 10 tests.
- `SummarizeToolArgs(toolName string, args json.RawMessage) string` — defined in Task 2, used in Task 3.
- `renderActiveToolCall`, `renderCommandToolCall`, `renderSimpleToolCall`, `renderApprovalInline`, `renderStateStrip`, `renderFinalAnswer`, `formatElapsed` — all defined and used consistently.
- `SaveMessage` signature change in Task 5 — all callers updated in Task 5 Step 3.
