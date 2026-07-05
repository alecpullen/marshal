# TUI Chronological Ordering and Input Redesign

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reorder the TUI transcript into a strict chronological timeline, remove elapsed-time suffix from completed tool calls, move the approval panel into the input area, and replace the single-line text input with an auto-growing multiline textarea.

**Architecture:** Session gains a `Transcript()` method returning a sorted merged timeline of messages, thinking entries, and audit events. The TUI renders this as a single unified pass. The agent runner preserves intermediate reasoning via `LogThinking()`. The approval panel moves from the viewport into the input area. The Charm `textinput` is swapped for a `textarea` with custom keybindings (Enter=submit, Shift+Enter=newline) and dynamic height.

**Tech Stack:** Go, Charmbracelet bubbles/textarea, lipgloss

## Global Constraints

- Local-first: no hosted model assumptions
- TUI is rendering-only per CLAUDE.md design constraints
- All changes must pass `go vet ./...` and `go test ./...`
- Follow existing Go conventions: no comments unless clarifying non-obvious intent
- Enter submits, Shift+Enter inserts newline
- Max textarea height: 8 rows

---

## File Structure

| File | Role |
|------|------|
| `internal/app/session/session.go` | `ThinkingEntry`, `TranscriptItem`, `TranscriptKind`, `LogThinking()`, `Transcript()` |
| `internal/agent/runner.go` | `LogThinking()` call before tool execution |
| `internal/app/tui/model.go` | Unified `refreshViewport`, textarea swap, approval removal, dirty tracking, dynamic height, custom textarea keybindings |
| `internal/app/tui/view.go` | Approval panel in input area, dynamic input height, textarea rendering |
| `internal/app/tui/transcript.go` | `renderTranscriptItem`, remove `"· Xs ago"`, `renderApprovalPanel` |
| `internal/app/tui/model_test.go` | Update tests for textarea, approval position, transcript ordering |
| `internal/app/tui/view_test.go` | Update input area rendering tests |
| `internal/app/tui/transcript_test.go` | Update tool call render tests |
| `internal/app/session/session_test.go` | Tests for `LogThinking`, `Transcript()` |

---

### Task 1: Add ThinkingEntry, TranscriptItem, and Transcript() to session

**Files:**
- Modify: `internal/app/session/session.go:44-46, 136, 422`

**Interfaces:**
- Produces: `ThinkingEntry` struct, `TranscriptKind` type + constants, `TranscriptItem` struct, `LogThinking(ThinkingEntry)`, `Transcript() []TranscriptItem`

- [ ] **Step 1: Add ThinkingEntry struct after ContentTypeToolResult block**

```go
// ThinkingEntry captures reasoning text that led to a tool call. Unlike
// Message.Reasoning (which is attached to a final answer), ThinkingEntry
// preserves intermediate reasoning that the agent produced before calling a
// tool — reasoning that would otherwise be lost when the next BeginStreaming
// call resets the in-progress buffer.
type ThinkingEntry struct {
	Text      string
	Duration  time.Duration
	StartedAt time.Time
}
```

- [ ] **Step 2: Add TranscriptKind and TranscriptItem after ThinkingEntry**

```go
type TranscriptKind int

const (
	KindMessage TranscriptKind = iota
	KindThinking
	KindAudit
)

type TranscriptItem struct {
	Timestamp time.Time
	Kind      TranscriptKind
	Message   *Message
	Audit     *registry.AuditEvent
	Thinking  *ThinkingEntry
}
```

- [ ] **Step 3: Add thinkLog field to State struct**

```go
thinkingLog []ThinkingEntry
```

- [ ] **Step 4: Add LogThinking method after EndStreaming**

```go
func (s *State) LogThinking(entry ThinkingEntry) {
	s.mu.Lock()
	s.thinkingLog = append(s.thinkingLog, entry)
	s.mu.Unlock()
}
```

- [ ] **Step 5: Add Transcript() method after AuditLog**

```go
func (s *State) Transcript() []TranscriptItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]TranscriptItem, 0, len(s.messages)+len(s.auditLog)+len(s.thinkingLog))

	for i := range s.messages {
		msg := s.messages[i]
		items = append(items, TranscriptItem{
			Timestamp: msg.CreatedAt,
			Kind:      KindMessage,
			Message:   &msg,
		})
	}

	for i := range s.auditLog {
		evt := s.auditLog[i]
		items = append(items, TranscriptItem{
			Timestamp: evt.Timestamp,
			Kind:      KindAudit,
			Audit:     &evt,
		})
	}

	for i := range s.thinkingLog {
		t := s.thinkingLog[i]
		items = append(items, TranscriptItem{
			Timestamp: t.StartedAt,
			Kind:      KindThinking,
			Thinking:  &t,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		ti := items[i].Timestamp
		tj := items[j].Timestamp
		if ti.IsZero() || tj.IsZero() {
			return !ti.IsZero() && tj.IsZero()
		}
		return ti.Before(tj)
	})
	return items
}
```

- [ ] **Step 6: Run tests to verify compilation and existing tests pass**

Run: `go build ./internal/app/session/ && go test ./internal/app/session/ -v -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/session/session.go
git commit -m "feat(session): add ThinkingEntry, TranscriptItem, and Transcript()"
```

---

### Task 2: Call LogThinking in agent runner before tool execution

**Files:**
- Modify: `internal/agent/runner.go:212-217`

**Interfaces:**
- Consumes: `session.ThinkingEntry`, `state.LogThinking()`, `state.InProgress()`
- Produces: Preserved intermediate reasoning in transcript

- [ ] **Step 1: After chat completes and action is parsed, before dispatching, save reasoning**

In the execution loop (around line 217), after `ParseAction(raw)` succeeds and before the `switch action.Type` block, add a call to preserve the thinking that led to this tool call. The reasoning is in `state.InProgress()` after `chatWithRetry` ends (EndStreaming preserves it, AddMessage is not called yet for tool-call iterations).

Find the block after `ParseAction` (around line 217-223):
```go
		action, parseErr := ParseAction(raw)
		if parseErr != nil {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
			messages = append(messages, BuildCorrectionMessage(parseErr))
			continue
		}
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
```

Insert after line 223 (after the `messages = append(...)` line):
```go
		if inProgress := r.State.InProgress(); inProgress.Reasoning != "" && action.Type != ActionAnswer && action.Type != ActionFinal {
			r.State.LogThinking(session.ThinkingEntry{
				Text:      inProgress.Reasoning,
				Duration:  time.Since(inProgress.StartedAt),
				StartedAt: inProgress.StartedAt,
			})
		}
```

This saves reasoning for tool-call iterations only. Final-answer reasoning is already captured by `AddMessageFinal` in the `ActionAnswer`/`ActionFinal` branch.

- [ ] **Step 2: Run agent tests**

Run: `go build ./internal/agent/ && go test ./internal/agent/ -v -count=1`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/agent/runner.go
git commit -m "feat(agent): preserve intermediate reasoning with LogThinking"
```

---

### Task 3: Rework refreshViewport with unified transcript timeline

**Files:**
- Modify: `internal/app/tui/model.go:478-535, 71-76`
- Modify: `internal/app/tui/transcript.go` (add `renderTranscriptItem`)

**Interfaces:**
- Consumes: `state.Transcript()`, `session.TranscriptItem`, `session.TranscriptKind`
- Produces: `renderTranscriptItem`, simplified dirty tracking, single-pass viewport build

- [ ] **Step 1: Add renderTranscriptItem to transcript.go**

Add after the `renderMessage` function:
```go
func renderTranscriptItem(item session.TranscriptItem, thinkingExpanded bool, width int) string {
	switch item.Kind {
	case session.KindThinking:
		if item.Thinking == nil {
			return ""
		}
		return renderThinkingSummary(item.Thinking.Text, item.Thinking.Duration, thinkingExpanded, width)
	case session.KindAudit:
		if item.Audit == nil {
			return ""
		}
		return renderCompletedToolCall(*item.Audit, width)
	case session.KindMessage:
		if item.Message == nil {
			return ""
		}
		var b strings.Builder
		if item.Message.Reasoning != "" {
			b.WriteString(renderThinkingSummary(item.Message.Reasoning, item.Message.ThinkDuration, thinkingExpanded, width))
		}
		b.WriteString(renderMessage(*item.Message, width))
		return b.String()
	}
	return ""
}
```

- [ ] **Step 2: Replace refreshViewport in model.go**

Replace the existing `refreshViewport` (lines 478-535):
```go
func (m *Model) refreshViewport() {
	items := m.state.Transcript()
	inProgress := m.state.InProgress()
	streamLen := len(inProgress.Reasoning)
	_, activeTool := m.state.ActiveToolCall()
	busy := m.busy || activeTool || streamLen > 0

	hash := transcriptHash(items, streamLen, busy)
	if hash == m.lastTranscriptHash {
		return
	}
	m.lastTranscriptHash = hash

	var b strings.Builder
	if len(items) == 0 {
		b.WriteString("  No messages yet.\n")
	}
	for _, item := range items {
		b.WriteString(renderTranscriptItem(item, m.thinkingExpanded, m.viewport.Width))
	}

	if inProgress.Active && inProgress.Reasoning != "" {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.spinnerFrame, m.viewport.Width))
	}
	if atc, ok := m.state.ActiveToolCall(); ok {
		b.WriteString(renderActiveToolCall(atc, m.spinnerFrame, m.now(), m.viewport.Width))
	}
	if err := m.state.ProviderError(); err != nil {
		b.WriteString(renderProviderError(err, m.viewport.Width))
	}

	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}
```

- [ ] **Step 3: Add transcriptHash function at bottom of model.go**

```go
func transcriptHash(items []session.TranscriptItem, streamLen int, busy bool) uint64 {
	var h uint64
	h = uint64(len(items)) ^ (uint64(streamLen) << 20)
	for i, item := range items {
		h ^= uint64(item.Timestamp.UnixNano()) * uint64(i+1)
	}
	if busy {
		h ^= 0xDEADBEEF
	}
	return h
}
```

- [ ] **Step 4: Replace dirty tracking fields in Model struct**

Replace lines 71-76:
```go
	// Viewport dirty tracking.
	lastMessageCount int
	lastStreamLen    int
	lastHadApproval  bool
	lastHadError     bool
	lastActiveTool   string
	lastAuditCount   int
	thinkingExpanded bool
```

With:
```go
	lastTranscriptHash uint64
	thinkingExpanded  bool
```

- [ ] **Step 5: Remove import "sort" and "registry" if no longer needed in model.go**

The sort import is used by `sortedAuditEvents` which is removed. The registry import was only used for AuditEvent in the old interleave logic. Remove both imports from the `import` block.

- [ ] **Step 6: Update thinking toggle to use new hash field**

Change line 354 from `m.lastMessageCount = -1` to `m.lastTranscriptHash = 0`:
```go
	case tea.KeyCtrlG:
		m.thinkingExpanded = !m.thinkingExpanded
		m.lastTranscriptHash = 0
		m.refreshViewport()
		return m, nil
```

- [ ] **Step 7: Update renderCompletedToolCall signature in transcript.go**

Remove the `now time.Time` parameter since elapsed suffix is being removed in Task 4. For now, just update the signature in the call site from `renderTranscriptItem` and keep the existing body (no behavior change yet):

Change `renderCompletedToolCall(event registry.AuditEvent, now time.Time, width int)` to `renderCompletedToolCall(event registry.AuditEvent, width int)`.

- [ ] **Step 8: Build and verify compilation**

Run: `go build ./internal/app/tui/`
Expected: PASS (no errors)

- [ ] **Step 9: Run TUI tests**

Run: `go test ./internal/app/tui/ -v -count=1 -run TestModel`
Expected: tests fail/need update (expected — Task 8 will fix them)

- [ ] **Step 10: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/transcript.go
git commit -m "feat(tui): unified transcript timeline with Transcript()"
```

---

### Task 4: Remove "· Xs ago" from completed tool calls

**Files:**
- Modify: `internal/app/tui/transcript.go:412-439`

- [ ] **Step 1: Remove elapsed suffix from renderCompletedToolCall**

Replace the existing function:
```go
func renderCompletedToolCall(event registry.AuditEvent, width int) string {
	state := "done"
	style := statusOkStyle
	if event.Error != "" {
		state = "failed"
		style = statusErrStyle
	}
	head := fmt.Sprintf("✓ %s %s", event.ToolName, state)
	var b strings.Builder
	b.WriteString(style.Render(truncateRunes(head, max(width-2, 1))))
	b.WriteString("\n")
	summary := event.ResultSummary
	if event.Error != "" {
		summary = event.Error
	}
	if summary != "" {
		b.WriteString(mutedStyle.Render(truncateRunes("  "+summary, max(width-2, 1))))
		b.WriteString("\n")
	}
	return b.String()
}
```

- [ ] **Step 2: Remove "time" import from transcript.go if no longer used**

Check if `time` is used elsewhere in transcript.go (it's used in `renderThinkingSummary` and `renderActiveToolCall`). Keep it.

- [ ] **Step 3: Run tests**

Run: `go build ./internal/app/tui/ && go test ./internal/app/tui/ -v -count=1 -run TestRender`
Expected: some tests will need updating for removed elapsed

- [ ] **Step 4: Remove the `formatElapsed` tests that reference completed-tool elapsed suffix — done in Task 8**

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/transcript.go
git commit -m "feat(tui): remove elapsed suffix from completed tool calls"
```

---

### Task 5: Move approval panel from viewport to input area

**Files:**
- Modify: `internal/app/tui/view.go:50-69` (`renderInputArea`)
- Modify: `internal/app/tui/model.go:271-289` (approval key handling for edit mode)
- Modify: `internal/app/tui/transcript.go:467-492` (`renderApprovalInline` → `renderApprovalPanel`)

**Interfaces:**
- Produces: `renderApprovalPanel` renders approval content without border wrapper; `renderInputArea` dispatches to it

- [ ] **Step 1: Rename renderApprovalInline to renderApprovalPanel and remove border wrapper**

Remove the bordered-panel wrapper from the function since the input area's border now wraps it:
```go
func renderApprovalPanel(tc *session.PendingToolCall, width int) string {
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
	return b.String()
}
```

- [ ] **Step 2: Update renderInputArea in view.go to render approval panel when pending**

Replace `renderInputArea`:
```go
func (m Model) renderInputArea() string {
	inputStyle := lipgloss.NewStyle().
		Width(max(m.width-2, 1)).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Padding(0, 1)

	rows := make([]string, 0, 4)

	if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			editLine := lipgloss.JoinHorizontal(
				lipgloss.Top,
				promptPrefixStyle.Render("❯ "),
				m.input.View(),
			)
			rows = append(rows, inputStyle.Render(editLine))
		} else {
			rows = append(rows, inputStyle.Render(renderApprovalPanel(tc, max(m.width-4, 1))))
		}
	} else {
		rows = append(rows, m.renderActivityStrip())
		if len(m.commandSuggestions) > 0 {
			rows = append(rows, m.renderCommandSuggestions())
		}
		inputLine := lipgloss.JoinHorizontal(
			lipgloss.Top,
			promptPrefixStyle.Render("❯ "),
			m.input.View(),
		)
		rows = append(rows, inputStyle.Render(inputLine))
	}

	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}
```

- [ ] **Step 3: Update model.go approval key handling with proper edit-mode branching**

Replace the approval + edit-mode block (around lines 271-332) with:
```go
	if tc != nil {
		if m.editingCommand {
			switch msg.Type {
			case tea.KeyEsc:
				m.editingCommand = false
				m.input.Reset()
				m.input.Placeholder = "Ask Marshal..."
				m.lastTranscriptHash = 0
				return m, nil
			case tea.KeyEnter:
				value := strings.TrimSpace(m.input.Value())
				if value != "" {
					tc.ResponseChan <- session.UserApprovalDecision{Approved: true, Edited: value}
					m.editingCommand = false
					m.input.Reset()
					m.input.Placeholder = "Ask Marshal..."
					m.state.SetPendingApproval(nil)
				}
				m.lastTranscriptHash = 0
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}

		switch msg.Type {
		case tea.KeyEnter:
			tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
			m.state.SetPendingApproval(nil)
			m.lastTranscriptHash = 0
			return m, nil
		case tea.KeyEsc:
			tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
			m.state.SetPendingApproval(nil)
			m.lastTranscriptHash = 0
			return m, nil
		default:
			switch msg.String() {
			case "d":
				tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
				m.state.SetPendingApproval(nil)
				m.lastTranscriptHash = 0
				return m, nil
			case "a":
				m.state.AddSessionRule(tc.Command)
				tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
				m.state.SetPendingApproval(nil)
				m.lastTranscriptHash = 0
				return m, nil
			case "e":
				m.editingCommand = true
				m.input.SetValue(tc.Command)
				m.input.Placeholder = "Edit command..."
				m.input.Focus()
				m.lastTranscriptHash = 0
				return m, nil
			case "r":
				if m.state.HasBackup() {
					_ = m.state.RollbackBackup()
					m.state.LogToolCall(registry.AuditEvent{
						Timestamp:     time.Now(),
						ToolName:      "rollback",
						ResultSummary: "Rollback applied successfully",
					})
					m.lastTranscriptHash = 0
					m.refreshViewport()
					return m, nil
				}
			}
			return m, nil
		}
	}
```

- [ ] **Step 4: Build and run tests**

Run: `go build ./internal/app/tui/ && go test ./internal/app/tui/ -v -count=1 -run TestView`
Expected: compilation succeeds; rendering tests may need updating

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/view.go internal/app/tui/model.go internal/app/tui/transcript.go
git commit -m "feat(tui): move approval panel from viewport to input area"
```

---

### Task 6: Replace textinput with textarea (multiline input)

**Files:**
- Modify: `internal/app/tui/model.go:11, 44-46, 139-145, 162-164, 271-332, 385-420, 432-476`
- Modify: `internal/app/tui/view.go:50-69`

**Interfaces:**
- Produces: textarea.Model with custom keybindings, `blinkCmd()` replacing `textinput.Blink`

- [ ] **Step 1: Add import for textarea in model.go**

Replace:
```go
import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)
```

With (removing `sort`, adding `textarea`, removing unused `textinput`):
```go
import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)
```

- [ ] **Step 2: Replace Model fields**

Replace:
```go
	input                  textinput.Model
	editingCommand         bool
```

With:
```go
	input          textarea.Model
	editingCommand bool
```

- [ ] **Step 3: Update New() function to initialize textarea with custom keybindings**

Replace input initialization (lines 139-149):
```go
func New(state *session.State, opts ...Option) Model {
	input := textarea.New()
	input.ShowLineNumbers = false
	input.Placeholder = "Ask Marshal..."
	input.CharLimit = 4000
	input.MaxHeight = 8
	input.SetHeight(1)
	input.SetWidth(80)

	km := textarea.DefaultKeyMap
	km.InsertNewline.SetKeys("shift+enter")
	input.KeyMap = km

	m := Model{
		state:          state,
		input:          input,
		editingCommand: false,
		ctx:            context.Background(),
		viewport:       viewport.New(0, 0),
		spinner:        NewSpinner(),
		now:            time.Now,
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}
```

- [ ] **Step 4: Define custom Blink command for textarea cursor**

Add at package level:
```go
func blinkCmd() tea.Cmd {
	return textarea.Blink
}
```

- [ ] **Step 5: Update Init()**

Replace:
```go
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}
```

With:
```go
func (m Model) Init() tea.Cmd {
	return blinkCmd()
}
```

- [ ] **Step 6: Update resize() for textarea width**

Replace:
```go
	m.input.Width = max(width-8, 1)
```

With:
```go
	m.input.SetWidth(max(width-8, 1))
```

- [ ] **Step 7: Update Enter key handling to submit (Enter=submit, Shift+Enter is handled by textarea's InsertNewline mapping to shift+enter)**

In the update method, the existing `tea.KeyEnter` block already submits. The textarea's default maps insert newline to Enter, but we remapped it to Shift+Enter. However, the tea.KeyEnter still fires for Enter. The textarea will handle it if we don't intercept it. But we want Enter to submit.

Replace the `case tea.KeyEnter:` block (around line 390):
```go
		case tea.KeyEnter:
			value := strings.TrimSpace(m.input.Value())
			if value == "" {
				return m, nil
			}
			m.input.Reset()
			m.updateCommandSuggestions()

			if strings.HasPrefix(value, "/") {
				return m.dispatchCommand(value)
			}

			if m.busy {
				return m, nil
			}
			if m.runner == nil {
				m.state.AddMessage(session.RoleUser, value, session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			m.busy = true
			m.lastTranscriptHash = 0
			agentCtx, cancel := context.WithCancel(m.ctx)
			m.agentCancel = cancel
			return m, tea.Batch(runAgentCmd(agentCtx, m.runner, value), tickCmd())
```

- [ ] **Step 8: Skip textarea Up/Down handling when viewport should scroll**

The textarea handles Up/Down internally. When the cursor is at the top/bottom boundary, we want those keys to pass through to the viewport. But Bubble Tea doesn't easily support key pass-through. The simplest approach: only let the textarea handle Up/Down by always calling `m.input.Update(msg)`. If the user wants to scroll, they use PgUp/PgDown / Ctrl+U / Ctrl+D which still work.

This means: always call `m.input.Update(msg)` in the final fallthrough. Remove the existing `tea.KeyUp`/`tea.KeyDown` viewport scrolling blocks, since the textarea now consumes Up/Down for cursor navigation.

In the `!tc` branch, remove the `tea.KeyUp`/`tea.KeyDown` blocks (around lines 378-384):
```go
	// Remove these:
	case tea.KeyUp:
		if m.moveCommandSuggestion(-1) {
			return m, nil
		}
	case tea.KeyDown:
		if m.moveCommandSuggestion(1) {
			return m, nil
		}
```

And remove the `tea.KeyUp`/`tea.KeyDown` viewport update blocks in the non-suggestion path (around lines 368-371):
```go
	// Remove these:
	case tea.KeyUp:
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd
	case tea.KeyDown:
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd
```

- [ ] **Step 9: Update acceptCommandSuggestion for textarea**

Replace:
```go
func (m *Model) acceptCommandSuggestion() bool {
	if len(m.commandSuggestions) == 0 {
		return false
	}
	cmd := m.commandSuggestions[m.commandSuggestionIndex]
	m.input.SetValue("/" + cmd.Name + " ")
	m.input.CursorEnd()
	m.updateCommandSuggestions()
	return true
}
```

With:
```go
func (m *Model) acceptCommandSuggestion() bool {
	if len(m.commandSuggestions) == 0 {
		return false
	}
	cmd := m.commandSuggestions[m.commandSuggestionIndex]
	m.input.SetValue("/" + cmd.Name + " ")
	m.updateCommandSuggestions()
	return true
}
```

- [ ] **Step 10: Build and verify compilation**

Run: `go build ./cmd/marshal/`
Expected: PASS (no errors; some unused import cleanup may be needed for `sort` in test files)

- [ ] **Step 11: Run quick manual check**

Run: `go run ./cmd/marshal/` — just to verify startup with textarea
Press Ctrl+C to exit.
Expected: no panic; input area shows multiline-capable field

- [ ] **Step 12: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/view.go
git commit -m "feat(tui): replace textinput with auto-growing multiline textarea"
```

---

### Task 7: Dynamic height for textarea and input area

**Files:**
- Modify: `internal/app/tui/model.go:177-179, 424-430`
- Modify: `internal/app/tui/view.go:12-18`

**Interfaces:**
- Produces: Dynamic viewport height based on textarea line count

- [ ] **Step 1: Update constants in view.go**

Replace the constant block:
```go
const (
	inputBorderRows  = 2
	activityStripRows = 1
	commandSuggestionRows = 1
	transcriptFrameRows = 2
	statusLineRows    = 1
)
```

- [ ] **Step 2: Replace inputAreaRows() with dynamic calculation**

```go
func (m Model) inputAreaRows() int {
	rows := inputBorderRows + activityStripRows
	if m.state.PendingApproval() != nil {
		rows += 5 // approval panel: title + command + risk + help line + spacing
	} else {
		inputHeight := max(m.input.Height(), 1)
		if inputHeight > m.input.MaxHeight {
			inputHeight = m.input.MaxHeight
		}
		rows += inputHeight
	}
	if len(m.commandSuggestions) > 0 {
		rows += commandSuggestionRows
	}
	return rows
}
```

- [ ] **Step 3: Update resize() and textarea SetHeight dynamically on each render**

Update `resize()` to set the textarea height based on content:
```go
func (m *Model) resize(width, height int) {
	if width < minTerminalWidth {
		width = minTerminalWidth
	}
	if height < minTerminalHeight {
		height = minTerminalHeight
	}

	m.width = width
	m.height = height

	m.viewport.Width = max(width-4, 1)
	m.viewport.Height = max(height-transcriptFrameRows-m.inputAreaRows()-statusLineRows, 1)

	m.input.SetWidth(max(width-8, 1))
}
```

The textarea's height auto-adjusts based on content via its own internal logic (LineCount). We set MaxHeight=8 to cap it.

- [ ] **Step 4: Add viewport height recalculation on text input change**

At the end of `Update`, after `m.input.Update(msg)`, recalculate viewport height if textarea height changed:
```go
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateCommandSuggestions()

	// Recalculate viewport if input area height changed
	newViewportHeight := max(m.height-transcriptFrameRows-m.inputAreaRows()-statusLineRows, 1)
	if newViewportHeight != m.viewport.Height {
		m.viewport.Height = newViewportHeight
		m.lastTranscriptHash = 0
		m.refreshViewport()
	}

	return m, cmd
```

- [ ] **Step 5: Build and test**

Run: `go build ./cmd/marshal/ && go test ./internal/app/tui/ -v -count=1 -run TestResize`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/view.go
git commit -m "feat(tui): dynamic input area and viewport height for textarea"
```

---

### Task 8: Update tests for all changes

**Files:**
- Modify: `internal/app/tui/model_test.go`
- Modify: `internal/app/tui/view_test.go`
- Modify: `internal/app/tui/transcript_test.go`
- Create: `internal/app/session/session_test.go` (add tests if not present)

- [ ] **Step 1: Check existing session tests**

```bash
ls internal/app/session/*_test.go
```

If no test file exists for session yet, we'll add basic tests for the new methods.

- [ ] **Step 2: Add session tests for Transcript(), LogThinking(), and ThinkingEntry**

First check what test infrastructure exists:
```bash
cat internal/app/session/*_test.go 2>/dev/null || echo "no test file"
```

If none, create basic tests. Let me check the structure first.

Run: `go test ./internal/app/session/ -v -count=1 2>&1 | tail -20`
Expected: see existing test output

- [ ] **Step 3: Update TUI model tests for textarea**

Search for references to `textinput`, `input.Width`, `LastMessageCount`, `editingCommand`, and replace with textarea equivalents.

Key changes needed:
- `textinput.Blink` → `blinkCmd()`
- `"github.com/charmbracelet/bubbles/textinput"` → `"github.com/charmbracelet/bubbles/textarea"`
- `m.input.Width` → `m.input.SetWidth(...)` / `m.input.Width()` (textarea has Width() method)
- Any test referencing `lastMessageCount`, `lastStreamLen`, `lastAuditCount` → remove or replace with `lastTranscriptHash`
- Any test referencing `renderApprovalInline` → `renderApprovalPanel`

Run: `go test ./internal/app/tui/ -v -count=1 2>&1 | head -50`
Expected: compilation errors showing which tests need updating

- [ ] **Step 4: Fix each test compilation error, then run again**

Iteratively run `go test ./internal/app/tui/ -v -count=1` and fix errors until all pass.

- [ ] **Step 5: Add new test for TranscriptItem rendering**

```go
func TestRenderTranscriptItem(t *testing.T) {
	width := 80

	t.Run("thinking entry collapsed", func(t *testing.T) {
		item := session.TranscriptItem{
			Kind: session.KindThinking,
			Thinking: &session.ThinkingEntry{
				Text:      "Should check the file",
				Duration:  2 * time.Second,
				StartedAt: time.Now(),
			},
		}
		result := renderTranscriptItem(item, false, width)
		if !strings.Contains(result, "thought for 2s") {
			t.Errorf("expected thinking summary, got: %s", result)
		}
	})

	t.Run("audit entry success", func(t *testing.T) {
		item := session.TranscriptItem{
			Kind: session.KindAudit,
			Audit: &registry.AuditEvent{
				ToolName:      "file.read",
				ResultSummary: "file contents here",
			},
		}
		result := renderTranscriptItem(item, false, width)
		if !strings.Contains(result, "file.read done") {
			t.Errorf("expected completed tool call, got: %s", result)
		}
		if strings.Contains(result, "ago") {
			t.Errorf("should not contain elapsed suffix, got: %s", result)
		}
	})

	t.Run("message entry with reasoning", func(t *testing.T) {
		msg := session.Message{
			Role:          session.RoleAssistant,
			Content:       "hello",
			ContentType:   session.ContentTypePlain,
			Reasoning:     "thinking about greeting",
			ThinkDuration: 1 * time.Second,
			CreatedAt:     time.Now(),
		}
		item := session.TranscriptItem{
			Kind:    session.KindMessage,
			Message: &msg,
		}
		result := renderTranscriptItem(item, false, width)
		if !strings.Contains(result, "thought for 1s") {
			t.Errorf("expected thinking summary before message, got: %s", result)
		}
	})
}
```

- [ ] **Step 6: Run full test suite**

Run: `go test ./internal/... -count=1`
Expected: all tests PASS

- [ ] **Step 7: Run vet**

Run: `go vet ./internal/...`
Expected: no warnings

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/model_test.go internal/app/tui/view_test.go internal/app/tui/transcript_test.go
git commit -m "test(tui): update tests for unified timeline, textarea, and approval changes"
```

---

### Task 9: Final integration verification

**Files:** None (verification only)

- [ ] **Step 1: Full build**

Run: `go build ./cmd/marshal/`
Expected: build succeeds with no errors

- [ ] **Step 2: Full test suite**

Run: `go test ./... -count=1`
Expected: all tests PASS

- [ ] **Step 3: Run go vet**

Run: `go vet ./...`
Expected: no warnings

- [ ] **Step 4: Run gofmt**

Run: `gofmt -w . && gofmt -l .`
Expected: no files listed (all properly formatted)

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore: final verification and formatting pass"
```
