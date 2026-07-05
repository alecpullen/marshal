# TUI Chronological Ordering and Input Redesign

**Date:** 2026-07-05
**Status:** Design approved

## Overview

Four TUI improvements:
1. Transcript rendered as a unified chronological timeline (thinking, tool calls, messages in strict order)
2. Remove "· Xs ago" elapsed suffix from completed tool call entries
3. Approval panel replaces input area (not in viewport)
4. Auto-growing multiline input (textarea with Enter=submit, Shift+Enter=newline)

## 1. Unified Chronological Timeline

### Problem

The current `refreshViewport` renders the transcript in three separate phases:
- Messages (each preceded by thinking summary)
- Audit events interleaved between messages by timestamp
- Active tools / live thinking / approval as trailing elements

Additionally, intermediate reasoning (thinking that leads to a tool call) is discarded by the agent runner. Only final-answer reasoning survives into a `Message.Reasoning` field. This means thinking blocks that preceded tool calls are lost from the transcript.

### Design

**New data model in session (`session.go`):**

```go
type ThinkingEntry struct {
    Text      string
    Duration  time.Duration
    StartedAt time.Time
}

func (s *State) LogThinking(entry ThinkingEntry) {
    s.mu.Lock()
    s.thinkingLog = append(s.thinkingLog, entry)
    s.mu.Unlock()
}
```

**Unified transcript (`session.go`):**

```go
type TranscriptKind int

const (
    KindMessage  TranscriptKind = iota
    KindThinking
    KindAudit
)

type TranscriptItem struct {
    Timestamp time.Time
    Kind      TranscriptKind
    Message   *Message       // set when Kind == KindMessage
    Audit     *AuditEvent    // set when Kind == KindAudit
    Thinking  *ThinkingEntry // set when Kind == KindThinking
}
```

The `Transcript()` method returns a sorted `[]TranscriptItem` by merging `s.messages`, `s.auditLog`, and `s.thinkingLog` by timestamp. Sorting is by `Timestamp()`.

**TUI rendering (`transcript.go`):**

The TUI package iterates `state.Transcript()` and dispatches to existing render functions based on `Kind`:

```go
func renderTranscriptItem(item session.TranscriptItem, thinkingExpanded bool, width int) string {
    switch item.Kind {
    case session.KindThinking:
        return renderThinkingSummary(item.Thinking.Text, item.Thinking.Duration, thinkingExpanded, width)
    case session.KindAudit:
        return renderCompletedToolCall(item.Audit, width)
    case session.KindMessage:
        msg := item.Message
        var b strings.Builder
        if msg.Reasoning != "" {
            b.WriteString(renderThinkingSummary(msg.Reasoning, msg.ThinkDuration, thinkingExpanded, width))
        }
        b.WriteString(renderMessage(msg, width))
        return b.String()
    }
    return ""
}
```

This keeps rendering logic in the TUI package and data in the session package — no coupling.

**Agent runner change (`runner.go`):**

In the execution loop, after `EndStreaming()` and before `executeToolCall()`, save the reasoning:

```go
if inProgress := r.State.InProgress(); inProgress.Reasoning != "" {
    r.State.LogThinking(session.ThinkingEntry{
        Text:      inProgress.Reasoning,
        Duration:  time.Since(inProgress.StartedAt),
        StartedAt: inProgress.StartedAt,
    })
}
```

**TUI `refreshViewport` rewrite (`model.go`):**

Replace the three-phase build with a single loop:

```go
func (m *Model) refreshViewport() {
    items := m.state.Transcript()
    inProgress := m.state.InProgress()
    streamLen := len(inProgress.Reasoning)
    busy := m.busy || m.state.ActiveToolCall() != nil || streamLen > 0

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

    // Live elements (not in transcript, always at bottom)
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

**Dirty tracking simplification:**

Remove `lastMessageCount`, `lastStreamLen`, `lastAuditCount`, `lastHadApproval`, `lastHadError`, `lastActiveTool`. Replace with single `lastTranscriptHash uint64` computed by hashing transcript item count, timestamps, stream length, and busy state. Approval is tracked by `busy` (pending approval means agent is blocked).

### TranscriptItem Rendering

The TUI renders each `TranscriptItem` by `Kind`:

- `KindThinking` → `renderThinkingSummary` (collapsed/expanded)
- `KindAudit` → `renderCompletedToolCall` (modified: no "· Xs ago")
- `KindMessage` → thinking block (if `msg.Reasoning != ""`) + `renderMessage`

## 2. Remove "· Xs ago" from Completed Tool Calls

### Change

In `transcript.go`, `renderCompletedToolCall` skips the elapsed suffix. The header line becomes:

```
✓ file.read done
```

No `· 12s ago` appended. The `now time.Time` parameter is removed from `renderCompletedToolCall`'s signature since it's only used for the elapsed suffix.

### Preserved

In-progress timers kept in:
- `renderActiveToolCall`: `⠋ file.read · 3s` (in-progress elapsed)
- `renderThinkingBox`: `⠋ thinking ...` (spinner, no duration)
- `renderThinkingSummary`: `⚙ thought for 2s` (completed thinking duration)
- Status bar and activity strip unchanged

## 3. Approval Panel Replaces Input Area

### Change

When `PendingApproval() != nil`, `renderInputArea()` returns the approval panel instead of the normal input box. The panel occupies the input area's screen position (bottom of screen, above status bar).

**Layout:**
```
┌──────────────────────────────┐
│  transcript viewport          │
├──────────────────────────────┤
│  ⚠ Approval needed           │
│  Agent wants to run: rm -rf / │
│  Risk: high                   │
│  Enter approve · d deny · e edit · a always │
├──────────────────────────────┤
│  status bar                   │
└──────────────────────────────┘
```

### Code changes

- **`view.go`**: `renderInputArea` checks `m.state.PendingApproval()` and returns `renderApprovalPanel(...)` when active. Uses same rounded-border input-box style. Input area height auto-adjusts to ~5 rows for approval content.
- **`transcript.go`**: Rename `renderApprovalInline` → `renderApprovalPanel`. Remove internal bordered-panel wrapper (input area provides outer border).
- **`model.go`**: Remove approval rendering from `refreshViewport`. Remove `lastHadApproval` from dirty tracking.
- Dynamic input area height: the area height is now `max(inputBoxHeight, approvalPanelHeight)` where approvalPanelHeight is ~5 rows.

### Edit mode

When user presses `e` during approval, the approval panel content is replaced with the textarea input pre-filled with the command text. `Esc` cancels, `Enter` submits edited command. The new textarea component handles both normal input and edit mode.

## 4. Auto-Growing Multiline Input

### Change

Replace `textinput.Model` with `textarea.Model` from `github.com/charmbracelet/bubbles/textarea`.

**Configuration:**

| Setting | Value |
|---------|-------|
| Placeholder | "Ask Marshal..." |
| CharLimit | 4000 |
| MaxHeight | 8 |
| ShowLineNumbers | false |

**Key bindings:**

| Key | Action |
|-----|--------|
| Enter | Submit |
| Shift+Enter | Insert newline |
| Up/Down | Navigate multiline; pass to viewport when at boundary |

**Dynamic height:**

```
inputContentHeight = min(max(1, textarea.LineCount()), 8)
inputAreaHeight = inputContentHeight + inputBorder(2) + activityStrip(1)
viewportHeight = screenHeight - statusBar(1) - inputAreaHeight - transcriptBorder(2)
```

On resize, `m.input.SetWidth` and `m.viewport.Height` recalculated.

### Field changes in Model

```go
type Model struct {
    // replaced
    input textarea.Model
    // removed
    // editingCommand bool (textarea handles both modes natively)
}
```

### Command suggestions

Render above textarea as a single line. Tab auto-complete sets value via `SetValue`.

### go.mod

`github.com/charmbracelet/bubbles v1.0.0` already includes `textarea`. No dependency changes.

## High-Level Execution Order

1. Add `ThinkingEntry`, `LogThinking()`, `Transcript()` to session
2. Wire `LogThinking()` into runner execution loop
3. Replace `refreshViewport` with unified timeline loop
4. Implement `TranscriptEntry` wrappers in transcript.go
5. Remove "· Xs ago" from completed tool calls
6. Move approval panel from viewport to input area
7. Replace textinput with textarea
8. Wire dynamic height calculations
9. Update all tests

## Files Changed

| File | Changes |
|------|---------|
| `internal/app/session/session.go` | ThinkingEntry, LogThinking, thinkLog field, Transcript() |
| `internal/agent/runner.go` | LogThinking() call before tool execution |
| `internal/app/tui/model.go` | Unified refreshViewport, textarea, approval removal, dirty tracking, dynamic height |
| `internal/app/tui/view.go` | Approval in input area, dynamic input height, textarea rendering |
| `internal/app/tui/transcript.go` | TranscriptEntry impls, remove "· Xs ago", renderApprovalPanel |
| `internal/app/tui/model_test.go` | Update tests |
| `internal/app/tui/view_test.go` | Update tests |
| `internal/app/tui/transcript_test.go` | Update tests |
| `internal/app/session/session_test.go` | Tests for new methods |
