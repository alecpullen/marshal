# Rich Content Rendering Implementation Plan

> **For agentic workers:** Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add typed content rendering to the TUI so agent output like markdown, plans, diffs, code, and tool results each get styled visual treatment.

**Architecture:** Add a `ContentType` enum to `session.Message`, update `AddMessage()` to accept it, map agent action types to content types in the runner, and dispatch to specialized renderers in a new `internal/app/tui/renderers.go`. A lightweight markdown parser handles inline formatting. The DB schema gains a `content_type` column.

**Tech Stack:** Go 1.26, Bubble Tea, Lipgloss, charmbracelet/x/ansi, SQLite

## Global Constraints

- All existing `AddMessage()` callers must compile without changes to content type behavior (default `ContentTypePlain`)
- `ContentTypePlain` messages must render identically to the current `renderMessage()` output
- DB `content_type` column must be nullable for backward compatibility
- No changes to the JSON action protocol envelope

---

### Task 1: Add ContentType type and constants to session package

**Files:**
- Modify: `internal/app/session/session.go`

**Interfaces:**
- Produces: `ContentType` type, `ContentTypePlain`, `ContentTypeMarkdown`, `ContentTypeCode`, `ContentTypePlan`, `ContentTypeDiff`, `ContentTypeToolResult` constants

- [ ] **Step 1: Add ContentType definition**

In `internal/app/session/session.go`, add after the `Role` type block (after line 31):

```go
type ContentType string

const (
	ContentTypePlain      ContentType = "plain"
	ContentTypeMarkdown   ContentType = "markdown"
	ContentTypeCode       ContentType = "code"
	ContentTypePlan       ContentType = "plan"
	ContentTypeDiff       ContentType = "diff"
	ContentTypeToolResult ContentType = "tool_result"
)
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/app/session/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/app/session/session.go
git commit -m "feat(session): add ContentType enum for typed message content"
```

---

### Task 2: Add ContentType field to Message struct and update AddMessage

**Files:**
- Modify: `internal/app/session/session.go:42-48` (Message struct)
- Modify: `internal/app/session/session.go:149-177` (AddMessage)
- Modify: `internal/app/session/session.go:173` (SaveMessage call)

**Interfaces:**
- Produces: `Message.ContentType` field, `AddMessage(role Role, content string, contentType ContentType)` signature

- [ ] **Step 1: Add ContentType to Message struct**

Change the `Message` struct at line 42-48 of `session.go` from:

```go
type Message struct {
	Role          Role
	Content       string
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
}
```

To:

```go
type Message struct {
	Role          Role
	Content       string
	ContentType   ContentType
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
}
```

- [ ] **Step 2: Update AddMessage signature and body**

Change the `AddMessage` signature at line 149 from:

```go
func (s *State) AddMessage(role Role, content string) {
```

To:

```go
func (s *State) AddMessage(role Role, content string, contentType ContentType) {
```

And in the body, add `ContentType` to the `Message` literal at line 161-167:

```go
msg := Message{
	Role:          role,
	Content:       content,
	ContentType:   contentType,
	Reasoning:     reasoning,
	ThinkDuration: thinkDuration,
	CreatedAt:     time.Now(),
}
```

- [ ] **Step 3: Update SaveMessage call**

At line 171-175, update the `SaveMessage` call to pass the content type. Since `SaveMessage` doesn't yet accept it, change the call site to pass an empty string for now (will be wired in Task 4):

```go
if s.persistenceEnabled() {
	if err := s.db.SaveMessage(s.sessionID, string(role), content, string(contentType), msg.CreatedAt, reasoning, thinkDuration); err != nil {
		s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
	}
}
```

- [ ] **Step 4: Verify compilation fails on callers (expected)**

Run: `go build ./...`
Expected: compilation errors on all `AddMessage()` call sites with wrong number of arguments

- [ ] **Step 5: Commit**

```bash
git add internal/app/session/session.go
git commit -m "feat(session): add ContentType to Message and AddMessage signature"
```

---

### Task 3: Update all AddMessage call sites to pass ContentTypePlain

**Files:**
- Modify: `internal/agent/runner.go` (lines 152, 204, 245, 259, 351, 544, 610)
- Modify: `internal/app/session/session.go` (line 415)
- Modify: `internal/app/tui/model.go` (lines 445, 574, 580, 588, 605, 619, 626, 631, 666, 675, 691, 693)
- Modify: `internal/app/session/session_test.go` (lines 22, 23, 39, 262, 263, 333, 350, 387)
- Modify: `internal/commands/commands_test.go` (lines 167, 168, 183, 184)
- Modify: `internal/knowledge/knowledge_test.go` (lines 108, 217, 246, 276, 313)
- Modify: `internal/app/app_test.go` (lines 433, 486, 546)
- Modify: `internal/app/tui/model_test.go` (all AddMessage calls)

**Interfaces:**
- Consumes: `AddMessage(role, content, contentType)` from Task 2

- [ ] **Step 1: Update runner.go — user goal message (line 152)**

From:
```go
r.State.AddMessage(session.RoleUser, goal)
```
To:
```go
r.State.AddMessage(session.RoleUser, goal, session.ContentTypePlain)
```

- [ ] **Step 2: Update runner.go — plan message (line 204)**

From:
```go
r.State.AddMessage(session.RoleAssistant, "Plan:\n"+planText)
```
To:
```go
r.State.AddMessage(session.RoleAssistant, planText, session.ContentTypePlan)
```

- [ ] **Step 3: Update runner.go — final answer (line 245)**

From:
```go
r.State.AddMessage(session.RoleAssistant, action.Content)
```
To:
```go
r.State.AddMessage(session.RoleAssistant, action.Content, session.ContentTypeMarkdown)
```

- [ ] **Step 4: Update runner.go — system messages (lines 259, 351, 544, 610)**

Replace each:
```go
r.State.AddMessage(session.RoleSystem, "...")
```
With:
```go
r.State.AddMessage(session.RoleSystem, "...", session.ContentTypePlain)
```

- [ ] **Step 5: Update session.go internal self-call (line 415)**

From:
```go
s.AddMessage(RoleSystem, "System notice: The user has rolled back...")
```
To:
```go
s.AddMessage(RoleSystem, "System notice: The user has rolled back...", ContentTypePlain)
```

- [ ] **Step 6: Update model.go AddMessage calls**

Every `m.state.AddMessage(...)` call adds `session.ContentTypePlain` as the third argument. There are ~12 call sites in `internal/app/tui/model.go` at lines 445, 574, 580, 588, 605, 619, 626, 631, 666, 675, 691, 693.

- [ ] **Step 7: Update test files — all AddMessage calls**

For every test file, add `, session.ContentTypePlain` (or `ContentTypePlain` for internal tests) as the third argument. Affected files:
- `internal/app/session/session_test.go` — ~8 calls
- `internal/commands/commands_test.go` — ~4 calls
- `internal/knowledge/knowledge_test.go` — ~5 calls
- `internal/app/app_test.go` — ~3 calls
- `internal/app/tui/model_test.go` — ~15 calls

- [ ] **Step 8: Verify compilation and tests**

Run: `go build ./... && go test ./...`
Expected: all pass (existing behavior unchanged since all content is `ContentTypePlain`)

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "feat: update all AddMessage callers to pass ContentTypePlain"
```

---

### Task 4: Update DB schema and persistence for content_type

**Files:**
- Modify: `internal/db/migrations.go:32-40` (messages table DDL)
- Modify: `internal/db/sessions.go:10-17` (db.Message struct)
- Modify: `internal/db/sessions.go:93-111` (SaveMessage)
- Modify: `internal/db/sessions.go:114-154` (GetMessages)
- Modify: `internal/db/sessions_test.go:30-37` (test calls)

**Interfaces:**
- Produces: `SaveMessage(sessionID, role, content, contentType, createdAt, reasoning, thinkDuration)` with `contentType` param

- [ ] **Step 1: Add content_type column to DDL**

In `internal/db/migrations.go`, add `content_type TEXT` after `content TEXT NOT NULL,` in the messages table definition.

- [ ] **Step 2: Add ContentType to db.Message struct**

In `internal/db/sessions.go`, add `ContentType string` field to the `Message` struct.

- [ ] **Step 3: Update SaveMessage to accept and persist content_type**

Change `SaveMessage` signature to include `contentType string` parameter. Use `sql.NullString` (only stored if non-empty and not "plain"). Update the INSERT statement to include `content_type` column.

- [ ] **Step 4: Update GetMessages to read content_type**

Add `content_type` to SELECT, `Scan`, and populate `m.ContentType` from a `sql.NullString`.

- [ ] **Step 5: Update sessions_test.go SaveMessage calls**

Add `"plain"` and `"markdown"` as the new `contentType` argument in the two test call sites.

- [ ] **Step 6: Verify compilation and tests**

Run: `go build ./... && go test ./...`
Expected: all pass

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(db): add content_type column to messages table"
```

---

### Task 5: Create renderers.go with renderPlain, renderMarkdown, and markdown parser

**Files:**
- Create: `internal/app/tui/renderers.go`

**Interfaces:**
- Produces: `renderPlain(role, content string, width int) string`, `renderMarkdown(role, content string, width int) string`, `renderCodeBlock(content string, width int) string`, `splitFencedBlocks(content string) []mdBlock`, `parseMarkdownLine(line string) (lipgloss.Style, string)`
- Consumes: lipgloss styles from `model.go` (shared in package)

- [ ] **Step 1: Create renderers.go with renderPlain**

Create `internal/app/tui/renderers.go`. Move the current `renderMessage` body into `renderPlain` (identical logic, just renamed). The function renders a colored role label prefix + word-wrapped content.

```go
package tui

import (
	"strings"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func renderPlain(role, content string, width int) string {
	if width < 1 { width = 1 }
	label := strings.ToLower(role)
	roleStyle := mutedStyle
	switch label {
	case "user": roleStyle = userRoleStyle
	case "agent", "assistant": roleStyle = agentRoleStyle
	case "tool": roleStyle = toolRoleStyle
	case "output": roleStyle = outputRoleStyle
	}
	prefixWidth := 10
	contentWidth := max(width-prefixWidth-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", prefixWidth+2))
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}
```

- [ ] **Step 2: Define mdBlock and splitFencedBlocks**

Add `mdBlock` struct `{kind string; text string}` and `splitFencedBlocks(content string) []mdBlock`. This function splits content into prose and code blocks based on ``` fences. Empty fence lines become a single-space line in the code block.

- [ ] **Step 3: Add renderMarkdown**

`renderMarkdown(role, content, width)` first calls `splitFencedBlocks`. For prose blocks, iterates lines calling `parseMarkdownLine` per line. For code blocks, delegates to `renderCodeBlock`. Prepend role label on first line of each block.

- [ ] **Step 4: Add parseMarkdownLine inline formatter**

`parseMarkdownLine(line string) (lipgloss.Style, string)` handles per-line markdown: `---`/`***`/`___` horizontal rules, `> ` blockquotes (`│ ` prefix, dimmed), `#`/`##`/`###` headings (accent/violet color, bold), `- `/`* ` unordered list items (`  • ` prefix).

- [ ] **Step 5: Add renderCodeBlock**

`renderCodeBlock(content string, width int)` wraps trimmed content in a Lipgloss rounded-border panel with dim color foreground.

- [ ] **Step 6: Verify compilation**

Run: `go mod tidy && go build ./...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/renderers.go
git commit -m "feat(tui): add renderPlain, renderMarkdown, renderCodeBlock, and markdown parser"
```

---

### Task 6: Add renderPlan, renderDiff, renderToolResult renderers

**Files:**
- Modify: `internal/app/tui/renderers.go`

**Interfaces:**
- Produces: `renderPlan(role, content string, width int) string`, `renderDiff(role, content string, width int) string`, `renderToolResult(role, content string, width int) string`

- [ ] **Step 1: Add renderPlan**

Renders a "Plan" labeled panel with accent-colored bullet points for each step line. Uses `renderPanel()` for the bordered container.

- [ ] **Step 2: Add renderDiff**

Renders a "Diff" labeled panel. Lines starting with `+` get green (successColor), `-` get red (errorColor), `@@` get dimmed. Uses `renderPanel()`.

- [ ] **Step 3: Add renderToolResult**

Renders with tool role style. First line is the summary header (bold tool role style). Remaining lines are dimmed. No panel wrapper — sits inline in chat.

- [ ] **Step 4: Verify compilation**

Run: `go mod tidy && go build ./...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/renderers.go
git commit -m "feat(tui): add renderPlan, renderDiff, renderToolResult renderers"
```

---

### Task 7: Add renderMessage dispatcher and wire it into refreshViewport

**Files:**
- Modify: `internal/app/tui/model.go:1097-1133` (replace renderMessage with dispatcher)
- Modify: `internal/app/tui/model.go:512-537` (refreshViewport)

**Interfaces:**
- Produces: `renderMessage(msg session.Message, width int) string` (dispatcher), `renderCode(role, content string, width int) string`
- Consumes: `session.Message` with `ContentType`, all renderers from Tasks 5-6

- [ ] **Step 1: Replace renderMessage with dispatcher**

Replace the current `renderMessage` function body (lines 1097-1133) with a switch on `msg.ContentType` that delegates to the appropriate renderer. Fall through to `renderPlain` for `""` / unknown types.

- [ ] **Step 2: Add renderCode wrapper**

Add a `renderCode(role, content, width)` function that wraps `renderCodeBlock` with a role label prefix.

- [ ] **Step 3: Update refreshViewport to pass session.Message**

At line 530, change `renderMessage(string(message.Role), message.Content, m.viewport.Width)` to `renderMessage(message, m.viewport.Width)`.

- [ ] **Step 4: Remove old renderMessage body from model.go**

The old function body now lives in `renderPlain` in `renderers.go`. Remove from model.go, keep only the dispatcher.

- [ ] **Step 5: Verify compilation**

Run: `go mod tidy && go build ./...`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go
git commit -m "feat(tui): add content-type dispatch to renderMessage"
```

---

### Task 8: Update model_test.go and add renderer tests

**Files:**
- Modify: `internal/app/tui/model_test.go:345` (renderMessage call)
- Modify: `internal/app/tui/model_test.go` (add test functions)

**Interfaces:**
- Consumes: `renderMessage(msg session.Message, width int)` from Task 7

- [ ] **Step 1: Update direct renderMessage call in test**

At line 345, update `renderMessage(string(session.RoleUser), message, width)` to `renderMessage(session.Message{Role: session.RoleUser, Content: message, ContentType: session.ContentTypePlain}, width)`.

- [ ] **Step 2: Add unit tests for each renderer**

Add tests: `TestRenderPlainPreservesExistingBehavior`, `TestRenderMarkdownHandlesBoldAndItalic`, `TestRenderMarkdownHandlesHeadings`, `TestRenderMarkdownHandlesBlockquote`, `TestRenderMarkdownHandlesFencedCode`, `TestRenderCodeBlockWrapsInBorder`, `TestRenderPlanShowsSteps`, `TestRenderDiffColorizesAdditions`, `TestRenderToolResultShowsSummary`, `TestRenderMessageDispatchesByContentType`.

Each test verifies the output string contains expected content.

- [ ] **Step 3: Run full test suite**

Run: `go test ./... -count=1`
Expected: all pass

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "test(tui): add tests for all content type renderers and dispatch"
```

---

### Task 9: Map agent action types to content types in runner.go

**Files:**
- Verify: `internal/agent/runner.go:245,204,152,259,351,544,610`

**Note:** Content type mappings were applied in Task 3. This task verifies correctness:
- Line 152: user goal → `ContentTypePlain` ✓
- Line 204: plan → `ContentTypePlan` ✓
- Line 245: final answer → `ContentTypeMarkdown` ✓
- Line 259: exceeded iterations → `ContentTypePlain` ✓
- Line 351: agent failed → `ContentTypePlain` ✓
- Line 544: loop nudge → `ContentTypePlain` ✓
- Line 610: loop nudge → `ContentTypePlain` ✓

- [ ] **Step 1: Verify all AddMessage calls in runner.go are correctly mapped**

Review each call site in `internal/agent/runner.go`.

- [ ] **Step 2: Run agent tests**

Run: `go test ./internal/agent/ -v`
Expected: all pass

- [ ] **Step 3: Commit**

```bash
git add internal/agent/runner.go
git commit -m "feat(agent): map action types to content types for TUI rendering"
```

---

### Task 10: Final integration — run all tests and verify

- [ ] **Step 1: Run all tests**

```bash
go build ./... && go test ./... -count=1
```
Expected: all pass, no compilation errors

- [ ] **Step 2: Run go vet**

```bash
go vet ./...
```
Expected: no issues

- [ ] **Step 3: Build binary**

```bash
go build ./cmd/marshal
```
Expected: builds successfully

- [ ] **Step 4: Commit final state**

```bash
git add -A
git commit -m "chore: final integration verification, all tests passing"
```
