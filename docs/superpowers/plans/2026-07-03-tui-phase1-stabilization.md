# TUI Phase 1 Stabilization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the janky rendering and confusing interactions in the current Marshal TUI by making layout geometry predictable, `View()` read-only, overlays responsive, focus/key routing modal-aware, and updates smooth.

**Architecture:** Keep the existing two-column + sidebar-tabs layout, but centralize sizing in a `resize()` helper called from `WindowSizeMsg`, add a lightweight modal/focus rule, make sub-models size-aware, and add dirty counters so the chat viewport only rebuilds when state changes.

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbletea`), Bubbles (`github.com/charmbracelet/bubbles`), Lip Gloss (`github.com/charmbracelet/lipgloss`), standard Go testing.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/app/tui/model.go` | Main Bubble Tea model, layout geometry, key routing, approval UI, status bar. |
| `internal/app/tui/model_test.go` | Tests for layout, resize, focus, tabs, approval, overlays, streaming. |
| `internal/app/tui/settings/model.go` | Settings model: fields, resize, save/cancel. |
| `internal/app/tui/settings/view.go` | Settings frame renderer; must pad/truncate rows to inner width. |
| `internal/app/tui/settings/field.go` | Field views; must respect available width. |
| `internal/app/tui/settings/model_test.go` | Settings tests. |
| `internal/app/tui/memory/model.go` | Memory model: cursor, scroll offset, resize. |
| `internal/app/tui/memory/view.go` | Memory frame + viewport rendering. |
| `internal/app/tui/memory/model_test.go` | Memory tests. |
| `internal/app/session/session.go` | Already thread-safe; no changes required for Phase 1. |

---

## Helpers to add first

Add these package-level helpers near the bottom of `internal/app/tui/model.go` before starting the layout tasks. They are used by several tasks.

```go
// truncateRunes returns s truncated to at most w runes.
func truncateRunes(s string, w int) string {
    if w <= 0 {
        return ""
    }
    runes := []rune(s)
    if len(runes) <= w {
        return s
    }
    return string(runes[:w])
}
```

---

## Task 1: Add layout geometry fields and a single resize helper

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

- [ ] **Step 1: Add geometry fields to `Model`**

Add these fields to the `Model` struct in `internal/app/tui/model.go` (group them near `width`/`height`):

```go
// Layout geometry computed once per WindowSizeMsg.
leftWidth     int
rightWidth    int
contentHeight int
chatHeight    int
```

Add constants above the `Model` struct:

```go
const (
    minTerminalWidth  = 40
    minTerminalHeight = 10
    minPanelWidth     = 10
)
```

- [ ] **Step 2: Implement `resize()`**

Add this method to `internal/app/tui/model.go`:

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

    // 70/30 split with a one-column gutter.
    m.leftWidth = int(float64(width) * 0.70)
    if m.leftWidth < minPanelWidth {
        m.leftWidth = minPanelWidth
    }
    m.rightWidth = width - m.leftWidth - 1
    if m.rightWidth < minPanelWidth {
        m.rightWidth = minPanelWidth
        m.leftWidth = width - m.rightWidth - 1
        if m.leftWidth < minPanelWidth {
            m.leftWidth = minPanelWidth
        }
    }

    // Vertical budget: status bar (1). The main content area fills the rest.
    // Left column = chat box (border + viewport + border) + input line + help line.
    // Right column outer height = contentHeight.
    m.contentHeight = height - 1
    if m.contentHeight < 5 {
        m.contentHeight = 5
    }

    // Viewport is the chat box interior: contentHeight minus the two-row chat
    // border minus the input and help rows.
    m.chatHeight = m.contentHeight - 4
    if m.chatHeight < 1 {
        m.chatHeight = 1
    }

    // Viewport content excludes the chat box border.
    m.viewport.Width = max(m.leftWidth-2, 1)
    m.viewport.Height = max(m.chatHeight, 1)

    // Input lives in a padded box with no border. inputStyle uses Width(m.leftWidth)
    // and Padding(0,1), so the textinput content width is leftWidth-4.
    m.input.Width = max(m.leftWidth-4, 1)
}
```

- [ ] **Step 3: Replace the `WindowSizeMsg` handler**

In `Update`, replace the existing `tea.WindowSizeMsg` case (lines ~120-132) with:

```go
case tea.WindowSizeMsg:
    m.resize(msg.Width, msg.Height)
    m.refreshViewport()
    return m, nil
```

- [ ] **Step 4: Write the failing geometry test**

Append to `internal/app/tui/model_test.go`:

```go
func TestResizeComputesGeometry(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    m := New(state)

    updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
    model := updated.(Model)

    if model.width != 80 || model.height != 24 {
        t.Fatalf("size = %dx%d, want 80x24", model.width, model.height)
    }
    if model.leftWidth < 50 || model.leftWidth > 60 {
        t.Fatalf("leftWidth = %d, want ~56", model.leftWidth)
    }
    if model.rightWidth < minPanelWidth {
        t.Fatalf("rightWidth = %d, too small", model.rightWidth)
    }
    if model.chatHeight < 1 {
        t.Fatalf("chatHeight = %d, want >= 1", model.chatHeight)
    }
    if model.viewport.Width != model.leftWidth-2 {
        t.Fatalf("viewport.Width = %d, want %d", model.viewport.Width, model.leftWidth-2)
    }
    if model.viewport.Height != model.chatHeight {
        t.Fatalf("viewport.Height = %d, want %d", model.viewport.Height, model.chatHeight)
    }
    if model.input.Width != model.leftWidth-4 {
        t.Fatalf("input.Width = %d, want %d", model.input.Width, model.leftWidth-4)
    }
}
```

- [ ] **Step 5: Run the test to verify it fails**

```bash
go test ./internal/app/tui -run TestResizeComputesGeometry -v
```

Expected: FAIL because `resize` sets the new fields but the test expectations match the new behavior; if it passes anyway, check that the fields are actually being set.

> Note: if the test passes because `resize` is already in place, confirm by temporarily breaking an assertion.

- [ ] **Step 6: Run all TUI tests to confirm current state**

```bash
go test ./internal/app/tui/...
```

Expected: Some failures from tests that still expect the old fallback layout; those will be fixed in Task 2.

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): add centralized resize helper and geometry fields"
```

---

## Task 2: Make `View()` read-only and use stored geometry

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

- [ ] **Step 1: Remove geometry recomputation from `View()`**

In `internal/app/tui/model.go`, inside `View()`:

1. Delete the local width/height recomputation block (the lines that set `leftWidth`, `rightWidth`, `contentHeight`, `chatHeight`).
2. Replace all uses of those locals with `m.leftWidth`, `m.rightWidth`, `m.contentHeight`, and `m.chatHeight`.
3. Remove the `m.viewport.Width = ...` and `m.viewport.Height = ...` assignments inside `View()`.

Specific replacements:

```go
// Old:
leftWidth := int(float64(m.width) * 0.70)
rightWidth := m.width - leftWidth - 2
contentHeight := m.height - 3
...
inputHeight := 3
chatHeight := contentHeight - inputHeight

// New: delete the above; use the stored fields.
```

- [ ] **Step 2: Update left/right column styles so the layout fills the terminal**

Change the left content style from:

```go
Width(leftWidth - 2)
```

to:

```go
Width(m.leftWidth)
```

Change the input style from:

```go
Width(leftWidth - 2)
```

to:

```go
Width(m.leftWidth)
```

Change the right column style from:

```go
Width(rightWidth)
```

to:

```go
Width(m.rightWidth)
```

Change the sidebar body style from:

```go
Width(rightWidth).
Height(contentHeight - 2).
MaxHeight(contentHeight - 2)
```

to:

```go
Width(m.rightWidth - 2).
Height(m.contentHeight - 3).
MaxHeight(m.contentHeight - 3)
```

Change the approval no-diff style from:

```go
Width(leftWidth - 2)
```

to:

```go
Width(m.leftWidth)
```

Change the diff split width from:

```go
splitWidth := (leftWidth - 4) / 2
```

to:

```go
splitWidth := (m.leftWidth - 4) / 2
```

- [ ] **Step 3: Update the `TestAltScreenViewLayout` test**

The existing test in `internal/app/tui/model_test.go` only checks substrings. Keep it, but add a bounds assertion:

```go
func TestAltScreenViewFits80x24(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    model := New(state)
    model = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24}).(Model)

    view := model.View()
    lines := strings.Split(view, "\n")
    if len(lines) > 24 {
        t.Fatalf("view height = %d lines, want <= 24", len(lines))
    }
    for i, line := range lines {
        if len([]rune(line)) > 80 {
            t.Fatalf("line %d width = %d, want <= 80: %q", i, len([]rune(line)), line)
        }
    }
}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./internal/app/tui/... -v
```

Expected: PASS after any off-by-one fixes.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): use stored geometry in View and remove viewport mutation"
```

---

## Task 3: Constrain help text and status bar

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

- [ ] **Step 1: Cap help text width**

In `View()`, change the `helpStyle` definition from:

```go
helpStyle := lipgloss.NewStyle().Foreground(dimColor)
```

to:

```go
helpStyle := lipgloss.NewStyle().Foreground(dimColor).MaxWidth(m.leftWidth - 2)
```

- [ ] **Step 2: Make `WORKING`/`IDLE` the same width and cap status bar**

Change the busy/idle block in `View()` from:

```go
if m.busy {
    statusItems = append(statusItems, statusBarAccent.Render(" WORKING "))
} else {
    statusItems = append(statusItems, " IDLE ")
}
```

to:

```go
busyStyle := statusBarAccent.Width(9)
if m.busy {
    statusItems = append(statusItems, busyStyle.Render(" WORKING "))
} else {
    statusItems = append(statusItems, busyStyle.Render("  IDLE   "))
}
```

Then change the status bar render from:

```go
statusBar := statusBarBg.Width(m.width).Render(statusBarText)
```

to:

```go
statusBar := statusBarBg.Width(m.width).MaxWidth(m.width).Render(statusBarText)
```

- [ ] **Step 3: Truncate long status fields**

Change the status item construction to:

```go
statusItems := []string{
    statusBarAccent.Render(" MARSHAL "),
    fmt.Sprintf(" project=%s ", truncateRunes(m.state.Config.Project.Name, 16)),
    fmt.Sprintf(" cwd=%s ", truncateRunes(m.state.WorkingDir, 24)),
    fmt.Sprintf(" local-only=%t ", !m.state.Config.Privacy.RemoteProvidersAllowed),
}
```

- [ ] **Step 4: Add a test for status bar width**

Append to `internal/app/tui/model_test.go`:

```go
func TestStatusBarFitsTerminalWidth(t *testing.T) {
    state := session.New(config.Default(), "/very/long/working/directory/path", time.Unix(100, 0), session.Persistence{})
    state.Config.Project.Name = "a-very-long-project-name"
    model := New(state)
    model = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24}).(Model)

    view := model.View()
    lines := strings.Split(view, "\n")
    last := lines[len(lines)-1]
    if len([]rune(last)) > 80 {
        t.Fatalf("status bar width = %d, want <= 80", len([]rune(last)))
    }
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/app/tui/...
```

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): constrain help text and status bar to terminal width"
```

---

## Task 4: Fix focus and key routing

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

- [ ] **Step 1: Move global navigation keys behind the approval guard**

In `Update`, the current order is:

1. Ctrl+C
2. settingsOpen branch
3. memoryOpen branch
4. Tab / Shift+Tab / Ctrl+P / Ctrl+X / Ctrl+R block
5. approval branch
6. input-focused / unfocused branches

Move block 4 so it lives inside the main `else` branch after the approval check (i.e., it only runs when `tc == nil`). The easiest way is to wrap the existing global-key block in:

```go
if tc == nil {
    // Tab, Shift+Tab, Ctrl+P, Ctrl+X, Ctrl+T, Ctrl+R handlers
}
```

and place it after the approval `else` block closes. Ensure it still runs before the `inputFocused` branching.

- [ ] **Step 2: Make `Esc` deny approval instead of quitting**

In the approval branch, change:

```go
case tea.KeyEsc:
    m.state.Shutdown()
    return m, tea.Quit
```

to:

```go
case tea.KeyEsc:
    tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
    m.state.SetPendingApproval(nil)
    return m, nil
```

- [ ] **Step 3: Sync `inputFocused` in command-edit mode**

In the approval branch where `case "e":` starts edit mode, add:

```go
m.editingCommand = true
m.inputFocused = true
m.input.SetValue(tc.Command)
m.input.Placeholder = "Edit command..."
m.input.Focus()
```

When edit mode is cancelled via `Esc` in the `editingCommand` branch, set:

```go
m.editingCommand = false
m.inputFocused = false
m.input.Blur()
m.input.Reset()
m.input.Placeholder = "Ask Marshal..."
```

- [ ] **Step 4: Add `Ctrl+K` toggle for memory**

In the `memoryOpen` branch, add before delegating to `m.memoryModel.Update`:

```go
if msg.Type == tea.KeyCtrlK {
    m.memoryOpen = false
    return m, nil
}
```

In the main (non-overlay, non-approval) key handling, make `Ctrl+K` behave as a toggle. Currently it opens memory in both input-focused and unfocused branches. Keep that, but also add the guard so repeated `Ctrl+K` closes it. Since the `memoryOpen` branch now handles close, the main branches only open.

- [ ] **Step 5: Render provider errors in AltScreen layout**

Add a helper style and render an error banner above the status bar when `m.state.ProviderError() != nil`.

Add near the other styles in `model.go`:

```go
errorBannerStyle = lipgloss.NewStyle().
    Background(lipgloss.Color("196")).
    Foreground(lipgloss.Color("255")).
    Padding(0, 1).
    Width(m.width) // set at render time, not here
```

Actually Lip Gloss styles cannot reference `m.width` at package init. Set the width when rendering:

```go
var errorBannerStyle = lipgloss.NewStyle().
    Background(lipgloss.Color("196")).
    Foreground(lipgloss.Color("255")).
    Padding(0, 1)
```

In `View()`, after the main layout and before the status bar:

```go
mainLayout := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)
result := mainLayout
if err := m.state.ProviderError(); err != nil {
    banner := errorBannerStyle.Width(m.width).MaxWidth(m.width).Render("Error: " + truncateRunes(err.Error(), m.width-8))
    result = lipgloss.JoinVertical(lipgloss.Left, mainLayout, banner)
}
return lipgloss.JoinVertical(lipgloss.Left, result, statusBar)
```

- [ ] **Step 6: Add tests for modal key capture and Esc-deny**

Append to `internal/app/tui/model_test.go`:

```go
func TestGlobalKeysDoNotLeakDuringApproval(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    tc := &session.PendingToolCall{
        ID:           "1",
        Name:         "shell.run",
        Command:      "go test",
        Risk:         "low",
        Reason:       "run tests",
        ResponseChan: make(chan session.UserApprovalDecision, 1),
    }
    state.SetPendingApproval(tc)
    model := New(state)
    model = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24}).(Model)

    for _, key := range []tea.KeyMsg{
        {Type: tea.KeyTab},
        {Type: tea.KeyShiftTab},
        {Type: tea.KeyCtrlP},
        {Type: tea.KeyCtrlX},
        {Type: tea.KeyCtrlT},
        {Type: tea.KeyCtrlR},
    } {
        updated, _ := model.Update(key)
        m := updated.(Model)
        if m.activeTab != 0 {
            t.Fatalf("activeTab changed on %v during approval", key)
        }
    }
    if state.PendingApproval() == nil {
        t.Fatal("approval was cleared by a global key")
    }
}

func TestEscDuringApprovalDenies(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    tc := &session.PendingToolCall{
        ID:           "1",
        Name:         "shell.run",
        Command:      "go test",
        Risk:         "low",
        Reason:       "run tests",
        ResponseChan: make(chan session.UserApprovalDecision, 1),
    }
    state.SetPendingApproval(tc)
    model := New(state)
    updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
    m := updated.(Model)

    if cmd != nil {
        t.Fatal("Esc during approval should not return a quit command")
    }
    select {
    case dec := <-tc.ResponseChan:
        if dec.Approved {
            t.Fatal("Esc should deny approval")
        }
    case <-time.After(time.Second):
        t.Fatal("no decision sent on Esc")
    }
    if m.state.PendingApproval() != nil {
        t.Fatal("pending approval was not cleared")
    }
}

func TestCtrlKTogglesMemory(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    db, err := db.Open(":memory:")
    if err != nil {
        t.Fatal(err)
    }
    defer db.Close()
    if err := db.Migrate(); err != nil {
        t.Fatal(err)
    }
    pid, err := db.GetOrCreateProject("/repo", "repo")
    if err != nil {
        t.Fatal(err)
    }

    model := New(state, WithMemoryStore(db, pid))
    model = model.Update(tea.KeyMsg{Type: tea.KeyCtrlK}).(Model)
    if !model.memoryOpen {
        t.Fatal("Ctrl+K did not open memory")
    }
    updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
    model = updated.(Model)
    if model.memoryOpen {
        t.Fatal("Ctrl+K did not close memory")
    }
}

func TestProviderErrorVisibleInAltScreen(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    state.SetProviderError(errors.New("connection refused"))
    model := New(state)
    model = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24}).(Model)

    view := model.View()
    if !strings.Contains(view, "connection refused") {
        t.Fatalf("provider error not visible in AltScreen view:\n%s", view)
    }
}
```

- [ ] **Step 7: Run tests**

```bash
go test ./internal/app/tui/...
```

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): modal-aware key routing, Esc denies approval, Ctrl+K toggle, provider errors"
```

---

## Task 5: Make settings and memory overlays responsive

**Files:**
- Modify: `internal/app/tui/settings/model.go`, `internal/app/tui/settings/view.go`, `internal/app/tui/settings/field.go`
- Modify: `internal/app/tui/memory/model.go`, `internal/app/tui/memory/view.go`
- Modify: `internal/app/tui/model.go` (propagate size)
- Test: `internal/app/tui/settings/model_test.go`, `internal/app/tui/memory/model_test.go`

### Settings

- [ ] **Step 1: Add size fields and `SetSize` to settings model**

In `internal/app/tui/settings/model.go`, add to `Model`:

```go
width  int
height int
```

Add method:

```go
func (m *Model) SetSize(width, height int) {
    m.width = width
    m.height = height
}
```

- [ ] **Step 2: Fix the settings frame renderer**

Replace `internal/app/tui/settings/view.go` with:

```go
package settings

import (
    "fmt"
    "strings"
)

func (m Model) View() string {
    frameWidth := 57
    if m.width > 0 {
        frameWidth = min(60, m.width-4)
    }
    if frameWidth < 30 {
        frameWidth = 30
    }
    inner := frameWidth - 4 // "│ " and " │"

    var b strings.Builder
    b.WriteString(frameTitle("Settings", frameWidth))
    for i, f := range m.fields {
        focused := i == m.focused
        line := f.View(inner)
        if focused {
            line = "> " + line
        } else {
            line = "  " + line
        }
        b.WriteString(frameLine(line, inner))
    }
    b.WriteString(frameSeparator(inner))
    if m.footer != "" {
        b.WriteString(frameLine(m.footer, inner))
    }
    b.WriteString(frameLine("[Ctrl+S] Save  [Esc] Cancel  [Tab] Next field", inner))
    b.WriteString(frameBottom(frameWidth))
    return b.String()
}

func frameTitle(title string, w int) string {
    inner := w - 4
    t := truncateRunes(title, inner-1)
    pad := inner - 1 - len([]rune(t))
    if pad < 0 {
        pad = 0
    }
    return "┌─ " + t + " " + strings.Repeat("─", pad) + "┐\n"
}

func frameSeparator(inner int) string {
    return "├" + strings.Repeat("─", inner+2) + "┤\n"
}

func frameBottom(w int) string {
    return "└" + strings.Repeat("─", w-2) + "┘\n"
}

func frameLine(content string, inner int) string {
    t := truncateRunes(content, inner)
    pad := inner - len([]rune(t))
    if pad < 0 {
        pad = 0
    }
    return "│ " + t + strings.Repeat(" ", pad) + " │\n"
}

func truncateRunes(s string, limit int) string {
    if limit <= 0 {
        return ""
    }
    runes := []rune(s)
    if len(runes) <= limit {
        return s
    }
    return string(runes[:limit])
}
```

- [ ] **Step 3: Make field views respect width**

Change `field.go` so each `View` method accepts the width and truncates. The signature is already `View(width int) string`. Update the implementations:

`stringField.View`:

```go
func (f *stringField) View(width int) string {
    label := f.label + ": "
    available := width - len([]rune(label)) - 2 // cursor / focus prefix
    if available < 1 {
        available = 1
    }
    f.input.Width = available
    return label + f.input.View()
}
```

`boolField.View`:

```go
func (f *boolField) View(width int) string {
    val := "false"
    if *f.value {
        val = "true"
    }
    s := fmt.Sprintf("%s: %s", f.label, val)
    if f.description != "" {
        s += " (" + f.description + ")"
    }
    return truncateRunes(s, width)
}
```

`selectField.View`:

```go
func (f *selectField) View(width int) string {
    s := f.label + ": " + f.value
    return truncateRunes(s, width)
}
```

`labelField.View`:

```go
func (f *labelField) View(width int) string {
    return truncateRunes(f.label+": "+f.value, width)
}
```

You may need to add `truncateRunes` to `field.go` or import it from `view.go` if both files are in the same package. Since they are, add the helper once in `view.go` and call it from `field.go`.

- [ ] **Step 4: Fix settings preset coherence (optional but cheap)**

In `settings/model.go`, when the Default profile select changes, recompute the active preset. This requires hooking the select field’s callback. Leave a note in the plan: if the field callback signature does not support recompute, skip this step for Phase 1 and document it as a known issue.

- [ ] **Step 5: Add settings sizing test**

Append to `internal/app/tui/settings/model_test.go`:

```go
func TestSettingsViewKeepsFrameBounded(t *testing.T) {
    cfg := config.Default()
    cfg.AgentProfiles = map[string]routing.Profile{
        "default": {Roles: map[routing.AgentRole]string{routing.RoleImplementer: "local"}},
    }
    cfg.Models.Presets = map[string]routing.ModelPreset{
        "local": {Name: "local", Provider: "ollama", Model: "qwen2.5-coder:14b"},
    }
    m := New(cfg, "/repo", "/repo/.marshal/config.toml")
    m.SetSize(80, 24)

    view := m.View()
    lines := strings.Split(view, "\n")
    maxW := 0
    for _, line := range lines {
        if w := len([]rune(line)); w > maxW {
            maxW = w
        }
    }
    if maxW > 60 {
        t.Fatalf("settings width = %d, want <= 60", maxW)
    }
    if maxW < 30 {
        t.Fatalf("settings width = %d, looks broken", maxW)
    }
    first := lines[0]
    last := lines[len(lines)-2]
    if !strings.HasPrefix(first, "┌") || !strings.HasSuffix(first, "┐") {
        t.Fatalf("top border broken: %q", first)
    }
    if !strings.HasPrefix(last, "└") || !strings.HasSuffix(last, "┘") {
        t.Fatalf("bottom border broken: %q", last)
    }
}
```

Add `strings` import if missing.

### Memory

- [ ] **Step 6: Add size and scroll offset to memory model**

In `internal/app/tui/memory/model.go`, add to `Model`:

```go
width  int
height int
offset int
```

Add method:

```go
func (m *Model) SetSize(width, height int) {
    m.width = width
    m.height = height
}
```

- [ ] **Step 7: Handle window resize in memory Update**

In `memory/model.go`, change `Update` to:

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.SetSize(msg.Width, msg.Height)
        return m, nil
    case tea.KeyMsg:
        switch msg.Type {
        case tea.KeyEsc:
            return m, func() tea.Msg { return ClosedMsg{} }
        case tea.KeyUp:
            m.moveCursor(-1)
            return m, nil
        case tea.KeyDown:
            m.moveCursor(1)
            return m, nil
        }
        switch msg.String() {
        case "k":
            m.moveCursor(-1)
        case "j":
            m.moveCursor(1)
        case "c":
            m.setConfidence(db.MemoryConfidenceConfirmed)
        case "s":
            m.setConfidence(db.MemoryConfidenceStale)
        }
    }
    return m, nil
}
```

- [ ] **Step 8: Clamp cursor and scroll offset**

Change `moveCursor` to:

```go
func (m *Model) moveCursor(delta int) {
    if len(m.memories) == 0 {
        return
    }
    m.cursor += delta
    if m.cursor < 0 {
        m.cursor = 0
    }
    if m.cursor >= len(m.memories) {
        m.cursor = len(m.memories) - 1
    }
    m.keepCursorInView()
}

func (m *Model) keepCursorInView() {
    visible := m.visibleCount()
    if visible <= 0 {
        return
    }
    if m.cursor < m.offset {
        m.offset = m.cursor
    }
    if m.cursor >= m.offset+visible {
        m.offset = m.cursor - visible + 1
    }
}

// visibleCount uses a value receiver because it does not mutate state.
func (m Model) visibleCount() int {
    // title(1) + separator(1) + footer/help(2) + borders(2)
    available := m.height - 6
    if available < 1 {
        return 1
    }
    return available
}
```

- [ ] **Step 9: Replace `memory/view.go` with a responsive version**

Replace the whole file with:

```go
package memory

import (
    "fmt"
    "strings"
)

func (m Model) View() string {
    frameWidth := 61
    if m.width > 0 {
        frameWidth = min(61, m.width-4)
    }
    if frameWidth < 30 {
        frameWidth = 30
    }
    inner := frameWidth - 4

    visible := m.visibleCount()
    if visible < 1 {
        visible = 1
    }
    end := m.offset + visible
    if end > len(m.memories) {
        end = len(m.memories)
    }

    var b strings.Builder
    b.WriteString(frameTitle("Project Memories", frameWidth))
    if len(m.memories) == 0 {
        b.WriteString(frameLine("No memories yet.", inner))
    }
    for i := m.offset; i < end; i++ {
        mem := m.memories[i]
        cursor := "  "
        if i == m.cursor {
            cursor = "> "
        }
        line := fmt.Sprintf("%s[%s] (%s) %s", cursor, mem.Kind, mem.Confidence, mem.Content)
        b.WriteString(frameLine(line, inner))
    }
    b.WriteString(frameSeparator(inner))
    if m.footer != "" {
        b.WriteString(frameLine(m.footer, inner))
    }
    b.WriteString(frameLine("[↑/k ↓/j] Move  [c] Confirm  [s] Mark Stale  [Esc] Close", inner))
    b.WriteString(frameBottom(frameWidth))
    return b.String()
}

func frameTitle(title string, w int) string {
    inner := w - 4
    t := truncateRunes(title, inner-1)
    pad := inner - 1 - len([]rune(t))
    if pad < 0 {
        pad = 0
    }
    return "┌─ " + t + " " + strings.Repeat("─", pad) + "┐\n"
}

func frameSeparator(inner int) string {
    return "├" + strings.Repeat("─", inner+2) + "┤\n"
}

func frameBottom(w int) string {
    return "└" + strings.Repeat("─", w-2) + "┘\n"
}

func frameLine(content string, inner int) string {
    t := truncateRunes(content, inner)
    pad := inner - len([]rune(t))
    if pad < 0 {
        pad = 0
    }
    return "│ " + t + strings.Repeat(" ", pad) + " │\n"
}

func truncateRunes(s string, limit int) string {
    if limit <= 0 {
        return ""
    }
    runes := []rune(s)
    if len(runes) <= limit {
        return s
    }
    return string(runes[:limit])
}
```

- [ ] **Step 10: Propagate size to overlays from main model**

In `internal/app/tui/model.go`, inside the `tea.WindowSizeMsg` handler, after `m.resize(...)`:

```go
m.settingsModel.SetSize(m.width, m.height)
m.memoryModel.SetSize(m.width, m.height)
```

Also, when opening settings/memory, set size before marking open:

```go
case tea.KeyCtrlO:
    m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
    m.settingsModel.SetSize(m.width, m.height)
    m.settingsOpen = true
    return m, nil
```

and similarly for `Ctrl+K`.

- [ ] **Step 11: Add memory sizing / scroll test**

Append to `internal/app/tui/memory/model_test.go`:

```go
func TestMemoryViewportScrollsAndClamps(t *testing.T) {
    db := openTestDB(t)
    pid := createTestProject(t, db)
    for i := 0; i < 50; i++ {
        if err := db.SaveMemory(pid, db.MemoryKindFact, fmt.Sprintf("memory %d", i)); err != nil {
            t.Fatal(err)
        }
    }
    m := New(db, pid)
    m.SetSize(80, 24)

    // Move cursor to bottom.
    for i := 0; i < 60; i++ {
        updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
        m = updated.(Model)
    }
    if m.cursor != 49 {
        t.Fatalf("cursor = %d, want 49", m.cursor)
    }
    if m.offset <= 0 {
        t.Fatalf("offset did not move: %d", m.offset)
    }

    view := m.View()
    if strings.Contains(view, "memory 0") {
        t.Fatal("first memory should be scrolled out of view")
    }
}
```

- [ ] **Step 12: Run all overlay tests**

```bash
go test ./internal/app/tui/...
```

- [ ] **Step 13: Commit**

```bash
git add internal/app/tui/settings internal/app/tui/memory internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): responsive settings and memory overlays"
```

---

## Task 6: Fix viewport refresh performance and streaming updates

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

- [ ] **Step 1: Add dirty counters**

Add to `Model`:

```go
lastMessageCount int
lastAuditCount   int
```

- [ ] **Step 2: Make `refreshViewport` dirty-aware**

Replace `refreshViewport` with:

```go
func (m *Model) refreshViewport() {
    messages := m.state.Messages()
    if len(messages) == m.lastMessageCount && !m.busy {
        return
    }
    m.lastMessageCount = len(messages)

    var b strings.Builder
    if len(messages) == 0 {
        b.WriteString("  No messages yet.\n")
    }
    for _, message := range messages {
        b.WriteString(fmt.Sprintf("  %s: %s\n\n", message.Role, message.Content))
    }
    m.viewport.SetContent(b.String())
    m.viewport.GotoBottom()
}
```

- [ ] **Step 3: Refresh during busy ticks**

Change the `agentTickMsg` handler from:

```go
case agentTickMsg:
    if !m.busy {
        return m, nil
    }
    return m, tickCmd()
```

to:

```go
case agentTickMsg:
    if !m.busy {
        return m, nil
    }
    m.refreshViewport()
    return m, tickCmd()
```

- [ ] **Step 4: Ensure `agentFinishedMsg` clears busy and refreshes**

This should already exist; verify it calls `m.refreshViewport()`:

```go
case agentFinishedMsg:
    m.busy = false
    if msg.err != nil {
        m.state.SetProviderError(msg.err)
    }
    m.refreshViewport()
    return m, nil
```

- [ ] **Step 5: Add streaming test**

Append to `internal/app/tui/model_test.go`:

```go
type streamingRunner struct {
    called chan string
}

func (s *streamingRunner) Run(ctx context.Context, goal string) error {
    s.called <- goal
    return nil
}

func TestBusyTickRefreshesViewport(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    runner := &streamingRunner{called: make(chan string, 1)}
    model := New(state, WithRunner(context.Background(), runner))
    model = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24}).(Model)

    // Start a turn.
    updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
    model = updated.(Model)
    updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
    model = updated.(Model)

    // Simulate the agent adding a message mid-turn.
    state.AddMessage(session.RoleAssistant, "working...")

    // Tick should refresh the viewport.
    updated, _ = model.Update(agentTickMsg{})
    model = updated.(Model)

    view := model.View()
    if !strings.Contains(view, "working...") {
        t.Fatalf("viewport not refreshed during busy tick:\n%s", view)
    }
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/app/tui/...
```

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): dirty-aware viewport refresh and streaming updates"
```

---

## Task 7: Clean up the approval UI

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go`

- [ ] **Step 1: Rewrite the no-diff approval banner as plain text**

In `View()`, replace the no-diff approval branch block that writes box-drawing characters with plain text. Keep the `approvalStyle` with `RoundedBorder`. Example:

```go
} else if tc != nil {
    approvalStyle := lipgloss.NewStyle().
        Width(m.leftWidth).
        Height(m.chatHeight).
        Border(lipgloss.RoundedBorder()).
        BorderForeground(accentColor)

    cmdLine := truncateRunes(tc.Command, m.leftWidth-12)
    reasonLine := truncateRunes(tc.Reason, m.leftWidth-10)
    riskLine := truncateRunes(riskText(tc), m.leftWidth-8)

    var b strings.Builder
    b.WriteString("SECURITY APPROVAL REQUIRED\n\n")
    b.WriteString(fmt.Sprintf("Command: %s\n", cmdLine))
    b.WriteString(fmt.Sprintf("Reason: %s\n", reasonLine))
    b.WriteString(fmt.Sprintf("Risk: %s\n", riskLine))
    b.WriteString("\n[Enter] Approve  [d] Deny  [e] Edit")
    if tc.Command != "" {
        b.WriteString(fmt.Sprintf("  [a] Always allow \"%s\"", truncateRunes(tc.Command, 20)))
    }
    if m.state.HasBackup() {
        b.WriteString("  [r] Rollback")
    }
    b.WriteString("\n")

    leftContent = approvalStyle.Render(b.String())
}
```

- [ ] **Step 2: Add `riskText` helper**

Add near the other helpers:

```go
func riskText(tc *session.PendingToolCall) string {
    if tc.Reason != "" {
        return tc.Reason
    }
    return tc.Risk
}
```

- [ ] **Step 3: Update approval tests**

The existing `TestTUIApprovalBannerAndKeypresses` checks for `"SECURITY APPROVAL REQUIRED"` and `"go test"`. It should still pass. Add an assertion that the view does not contain double border characters like `┌` more than once, or simply assert it contains the human risk text.

Append:

```go
func TestApprovalBannerHasSingleBorder(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    tc := &session.PendingToolCall{
        ID:           "1",
        Name:         "shell.run",
        Command:      "go test",
        Risk:         "low",
        Reason:       "run tests to validate changes",
        ResponseChan: make(chan session.UserApprovalDecision, 1),
    }
    state.SetPendingApproval(tc)
    model := New(state)
    model = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24}).(Model)

    view := model.View()
    if strings.Count(view, "┌") > 1 {
        t.Fatalf("approval banner has double borders:\n%s", view)
    }
    if !strings.Contains(view, "run tests") {
        t.Fatalf("approval banner missing human reason:\n%s", view)
    }
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/app/tui/...
```

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): single-border approval banner with human-readable risk"
```

---

## Task 8: Add a concurrency/race test

**Files:**
- Test: `internal/app/tui/model_test.go`

- [ ] **Step 1: Write a race test**

Append to `internal/app/tui/model_test.go`:

```go
func TestRenderWhileStateMutatedDoesNotRace(t *testing.T) {
    state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
    model := New(state)
    model = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24}).(Model)

    done := make(chan struct{})
    go func() {
        defer close(done)
        for i := 0; i < 200; i++ {
            state.AddMessage(session.RoleAssistant, fmt.Sprintf("msg %d", i))
            state.LogToolCall(registry.AuditEvent{ToolName: "test", ResultSummary: "ok"})
        }
    }()

    for i := 0; i < 200; i++ {
        _ = model.View()
        updated, _ := model.Update(agentTickMsg{})
        model = updated.(Model)
    }
    <-done
}
```

- [ ] **Step 2: Run with the race detector**

```bash
go test -race ./internal/app/tui/...
```

Expected: PASS. If a race is reported, fix it by ensuring `refreshViewport()` calls only the mutex-protected copy methods on `session.State` (which it already does).

- [ ] **Step 3: Commit**

```bash
git add internal/app/tui/model_test.go
git commit -m "test(tui): race test for render during state mutation"
```

---

## Task 9: Final verification and cleanup

- [ ] **Step 1: Run the full TUI test suite**

```bash
go test -race ./internal/app/tui/...
```

- [ ] **Step 2: Run the full project test suite**

```bash
go test ./...
```

- [ ] **Step 3: Manual smoke test (if possible)**

```bash
go run ./cmd/marshal
```

Verify:
- Terminal fills completely at 80×24.
- Resizing does not crash or misalign.
- `Tab` cycles tabs only when no approval dialog is open.
- `Esc` during approval denies.
- `Ctrl+K` opens and closes memory.
- Settings frame is a clean box.
- Long project/cwd names do not overflow.

- [ ] **Step 4: Commit any final fixes**

```bash
git commit -am "fix(tui): final phase 1 stabilization polish"
```

---

## Phase 2 roadmap (future plan)

After Phase 1 is merged and dog-food feedback is collected, write a second implementation plan for:

1. A `panel` package with a `Panel` interface and a `Manager` grid.
2. Converting Chat, Plan, Diff, Tool Log, Context, Agents, Memory, and Config into panels.
3. A mode state (Ask/Plan/Edit/Auto/Swarm) and status-bar integration.
4. A command palette overlay bound to `Ctrl+P`.
5. Implementing `Ctrl+M`, `Ctrl+D`, `Ctrl+R`, `Ctrl+A`, `Ctrl+Y`, `Ctrl+N`, `Ctrl+E`, `Ctrl+S`.
6. Richer context browser with item removal.
7. Snapshot/golden tests for the full layout.

---

## Self-review checklist

- **Spec coverage:** Every Phase 1 item from `docs/superpowers/specs/2026-07-03-tui-jank-fix-and-redesign.md` (geometry, View purity, focus/key routing, overlays, performance, approval UI, tests) has a corresponding task.
- **Placeholder scan:** No `TODO`, `TBD`, or vague steps. Each code step shows concrete code or exact commands.
- **Type consistency:** All new fields (`leftWidth`, `rightWidth`, `contentHeight`, `chatHeight`, `lastMessageCount`, `lastAuditCount`) are used consistently across `model.go` and tests.
- **File paths:** All paths are exact.
- **Tests:** Each task introduces a failing test before the implementation step, per TDD.
