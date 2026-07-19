# Domain F5 — View Rendering Polish Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in a
> dedicated worktree (suggested branch `feature/domain-f5-view-polish`).
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** Resolve ten findings from
`docs/14-codebase-improvement-audit-2026-07-14.md` (Domain F) that
concern TUI layout correctness and dead/duplicated code in the
view layer:

- **F-UIUX-142** (MEDIUM) — when the settings diff overlay blocks
  save, the user sees only a transient footer message; the save
  control itself looks identical to a clickable button.
- **F-UIUX-143** (LOW) — the memory browser does not refresh the
  list after the user marks an item stale or confirmed.
- **F-UIUX-144** (LOW) — the browser bar and the right-side
  status segment both show the same URL/tool, duplicating status.
- **F-UIUX-145** (LOW) — the SDD panel reserves 10 rows even when
  no tasks are visible, leaving dead space.
- **F-UIUX-146** (LOW) — after the user accepts a command from
  the completion popup, the popup disappears but the inserted
  value lacks a visible `/` indicator (the popup also fails to
  remain open for argument completion).
- **F-BUG-160** (LOW) — `m.pickerModel.Update(msg)` returns only a
  command, not a model. The comment is misleading.
- **F-POL-162** (LOW) — `forceMode` is set but never rendered in
  the help overlay; the comment claims it is reserved for a
  future status-bar display.
- **F-POL-164** (LOW) — `inputAreaRows` counts raw newlines in
  wrapped content, which underestimates wrapped multi-line text.
- **F-POL-165** (LOW) — `renderCompletionPopup` recomputes
  `max`/`offset` independently of `completionPopup.reconcileOffset`,
  risking drift.
- **F-POL-170** (LOW) — `renderSwarmPanel` always prints 5 blank
  lines, regardless of how many roles are active.

**Architecture:** All changes are localised to `internal/app/tui/`
and `internal/app/tui/`. No new packages. The TUI design skill's
"semantic color encodes meaning, not decoration" principle guides
the disabled save button (Task 1); "deduplicate status
information" guides Task 4; "design in layers" guides the SDD
panel collapse (Task 5).

**Tech Stack:** Go 1.22+, Bubble Tea v2, `charm.land/lipgloss/v2`.

---

## Global Constraints

- Every code change MUST compile: `CGO_ENABLED=1 go build ./...`
  after each task.
- Every test change MUST pass:
  `CGO_ENABLED=1 go test ./internal/app/tui/...` after each task.
- At the end, `CGO_ENABLED=1 go test ./...` must pass.
- Commit per task with the exact message shown.
- Preserve the public view function signatures; only the
  *bodies* (and internal helpers) change.
- Disabled save (Task 1) must use semantic color slots, never
  hardcoded hex.

---

## File Structure

Files modified by this plan:

- `internal/app/tui/settings/model.go` — Task 1
- `internal/app/tui/settings/model_test.go` — Task 1 (add test)
- `internal/app/tui/memory/model.go` — Task 2
- `internal/app/tui/memory/model_test.go` — Task 2 (add test)
- `internal/app/tui/browserbar.go` — Task 3
- `internal/app/tui/browserbar_test.go` — Task 3 (add test)
- `internal/app/tui/view.go` — Tasks 3, 5, 6, 8, 9
- `internal/app/tui/view_test.go` — Tasks 3, 5, 6, 9 (add tests)
- `internal/app/tui/sdd_panel.go` — Task 5
- `internal/app/tui/sdd_panel_test.go` — Task 5 (add test)
- `internal/app/tui/completions.go` — Tasks 6, 9
- `internal/app/tui/completions_test.go` — Task 6 (add test)
- `internal/app/tui/model.go` — Tasks 7, 8 (comment + dead code)
- `internal/app/tui/swarm_panel.go` — Task 10
- `internal/app/tui/swarm_panel_test.go` — Task 10 (add test)

---

### Task 1: F-UIUX-142 — Disabled save control renders inline reason

**Files:**
- Modify: `internal/app/tui/settings/model.go`
- Add tests: `internal/app/tui/settings/model_test.go`

**Problem:** The diff overlay's "Save" button is rendered the same
whether save is blocked or allowed. `SetSaveBlocked` only stores a
message that the footer can show; the button itself is not
visually dimmed.

**Fix:** When `m.saveBlocked != ""`, render the save button as a
dimmed `lipgloss` style with the reason appended inline. Use the
existing `theme.Muted` slot, never a hardcoded color.

**Implementation steps:**

- [ ] **Step 1: Locate the save control renderer**

```bash
grep -n 'Save\|save' internal/app/tui/settings/model.go | head -40
```

- [ ] **Step 2: Branch the render**

```go
func (m *SettingsModel) saveView() string {
    if m.saveBlocked != "" {
        return mutedStyle.Render("Save (blocked: " + m.saveBlocked + ")")
    }
    return keyStyle.Render("Ctrl+S") + " save"
}
```

- [ ] **Step 3: Test**

```go
func TestSaveControlShowsBlockedReason(t *testing.T) {
    m := newTestSettingsModel()
    m.SetSaveBlocked("Resolve the pending tool approval to save.")
    out := m.saveView()
    if !strings.Contains(out, "Resolve the pending tool approval") {
        t.Fatalf("reason not inlined: %q", out)
    }
    if !strings.Contains(out, "Save") {
        t.Fatalf("button label missing: %q", out)
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui/settings -v
git add internal/app/tui/settings/model.go internal/app/tui/settings/model_test.go
git commit -m "feat(tui/settings): inline save-blocked reason on control (F-UIUX-142)"
```

---

### Task 2: F-UIUX-143 — Memory browser refreshes after staleness change

**Files:**
- Modify: `internal/app/tui/memory/model.go` (`setConfidence`)
- Add tests: `internal/app/tui/memory/model_test.go`

**Problem:** After the user toggles an item stale/confirmed, the
view does not re-render. The data is updated, but the user has
no immediate feedback.

**Fix:** In `setConfidence`, after the DB write, update the
in-memory `confidence` field of the affected item and re-publish
a `memory.UpdatedMsg` (or a simple local "set version + 1" that
the parent model reads on the next tick).

**Implementation steps:**

- [ ] **Step 1: Find `setConfidence`**

```bash
grep -n 'setConfidence\|confidence' internal/app/tui/memory/model.go
```

- [ ] **Step 2: Bump a version field**

Add `version int` to the `memory.Model` struct. In `setConfidence`,
after the DB write, set the item's confidence and increment
`version`. The parent reads `version` and calls `refreshViewport()`
when it changes.

- [ ] **Step 3: Surface a footer confirmation**

```go
m.footer = "Marked as " + newConfidence
m.version++
```

- [ ] **Step 4: Test**

```go
func TestSetConfidenceBumpsVersion(t *testing.T) {
    m := newTestMemoryModel()
    before := m.Version()
    m.setConfidence(0, "stale")
    if m.Version() <= before {
        t.Fatal("version did not advance")
    }
}
```

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui/memory -v
git add internal/app/tui/memory/model.go internal/app/tui/memory/model_test.go
git commit -m "fix(tui/memory): refresh after confidence change (F-UIUX-143)"
```

---

### Task 3: F-UIUX-144 — Dedupe browser bar and right-side status

**Files:**
- Modify: `internal/app/tui/browserbar.go` + `internal/app/tui/view.go`
- Add tests: `internal/app/tui/browserbar_test.go`,
  `internal/app/tui/view_test.go`

**Problem:** The browser bar (`browserbar.go:11-38`) shows
URL/tool; the right-side status segment in `view.go:181-188`
shows the same. The activity strip repeats the label.

**Fix:** Introduce a `ShouldShowStatusURL() bool` helper. When the
browser bar is visible, return `false`; the right-side status
segment then omits the URL line. The activity strip continues to
show the current tool label because that is the *activity*, not
the *URL* — they are semantically different.

**Implementation steps:**

- [ ] **Step 1: Add the helper**

In `internal/app/tui/model.go`:

```go
func (m Model) ShouldShowStatusURL() bool {
    // Browser bar already covers URL when active.
    return m.browserBar == nil
}
```

(`m.browserBar` is the existing pointer; if the field name is
different, search for the browser-bar constructor call site.)

- [ ] **Step 2: Branch the status renderer**

In `view.go`, gate the URL segment on `m.ShouldShowStatusURL()`.

- [ ] **Step 3: Test**

```go
func TestStatusURLHiddenWhenBrowserBarVisible(t *testing.T) {
    m := newTestModel()
    m.browserBar = newBrowserBar("http://example.com")
    if m.ShouldShowStatusURL() {
        t.Fatal("URL should be hidden when browser bar is shown")
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestStatus' -v
git add internal/app/tui/browserbar.go internal/app/tui/browserbar_test.go internal/app/tui/view.go internal/app/tui/view_test.go internal/app/tui/model.go
git commit -m "fix(tui): dedupe URL between browser bar and status (F-UIUX-144)"
```

---

### Task 5: F-UIUX-145 — SDD panel returns its actual rendered height

**Files:**
- Modify: `internal/app/tui/sdd_panel.go`
- Add tests: `internal/app/tui/sdd_panel_test.go`

**Problem:** The SDD panel reserves 10 rows via `sddPanelRows()`,
but `renderSDDPanel` clamps to ≤ 10 rows of content. Empty
panels still claim 10 rows.

**Fix:** Have `renderSDDPanel` return `(string, int)`. The second
value is the actual rendered row count. The caller uses that
value to size the layout instead of the constant.

**Implementation steps:**

- [ ] **Step 1: Update the signature**

```go
func (m *Model) renderSDDPanel(width int) (string, int) {
    if !m.state.SDDProgress().Active {
        return "", 0
    }
    body := m.renderSDDContent(width)
    return body, lipgloss.Height(body)
}
```

- [ ] **Step 2: Update callers**

Find every `m.sddPanelRows()` and `renderSDDPanel(...)` call site
and switch to the new return.

- [ ] **Step 3: Test**

```go
func TestSDDPanelHeightMatchesContent(t *testing.T) {
    m := newTestModel()
    m.state.SetSDDProgress(&session.SDDProgress{Active: true, Tasks: nil})
    _, rows := m.renderSDDPanel(80)
    if rows > 3 {
        t.Fatalf("empty SDD panel reports too many rows: %d", rows)
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestSDDPanel' -v
git add internal/app/tui/sdd_panel.go internal/app/tui/sdd_panel_test.go internal/app/tui/view.go
git commit -m "refactor(tui/sdd): report actual panel height (F-UIUX-145)"
```

---

### Task 6: F-UIUX-146 — Completion popup shows `/` for slash commands

**Files:**
- Modify: `internal/app/tui/completions.go`
- Add tests: `internal/app/tui/completions_test.go`

**Problem:** When the user accepts a `/`-command from the popup,
the popup disappears. If the user starts typing arguments, the
popup does not reappear, and the inserted text does not visibly
indicate the `/` prefix when the cursor is in the middle of the
word.

**Fix:** Two-part fix:

1. After the popup is dismissed (e.g. `Enter` on a command
   candidate), re-show the popup for argument completion when the
   user types a space after the accepted command.
2. Render the selected item in the popup with the leading `/`
   character so the user sees the full command word.

**Implementation steps:**

- [ ] **Step 1: Re-trigger on space**

In the completion controller, after a command is accepted and the
user types ` `, re-open the popup if there are sub-arguments
documented for that command.

- [ ] **Step 2: Render with prefix**

In `renderCompletionPopup`, change the candidate display to
`/command` (instead of just `command`) for slash commands.

- [ ] **Step 3: Test**

```go
func TestCompletionPopupShowsSlashPrefix(t *testing.T) {
    pop := newTestCompletionPopup()
    pop.SetCandidates([]candidate{{Display: "plan", Value: "plan"}})
    out := pop.View()
    if !strings.Contains(out, "/plan") {
        t.Fatalf("expected /plan in popup, got %q", out)
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestCompletion' -v
git add internal/app/tui/completions.go internal/app/tui/completions_test.go
git commit -m "feat(tui/completion): show / prefix and re-open on argument (F-UIUX-146)"
```

---

### Task 7: F-BUG-160 — Clarify `m.pickerModel.Update` semantics

**Files:**
- Modify: `internal/app/tui/model.go` (line 530-531)

**Problem:** The comment "route key messages to the picker" is
ambiguous because the picker mutates via pointer; the call site
discards the returned model.

**Fix:** Improve the comment to state "the picker is pointer-
updated; the returned model is the same pointer; we discard it
intentionally." No code change beyond the comment.

- [ ] **Step 1: Update the comment**

```go
// pickerModel is pointer-updated: m.pickerModel.Update(msg) mutates
// the picker in place via its embedded pointer, and returns the same
// *picker.Model. We forward the returned command but discard the
// model — assigning back is a no-op. The picker keeps its own
// state.
```

- [ ] **Step 2: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -v
git add internal/app/tui/model.go
git commit -m "docs(tui): clarify pickerModel.Update pointer semantics (F-BUG-160)"
```

---

### Task 8: F-POL-162 — Remove or render `forceMode`

**Files:**
- Modify: `internal/app/tui/model.go` (line 81, 1714-1715)

**Problem:** `forceMode` is set but the help overlay never shows
the current mode. The comment claims it is reserved for a future
status-bar display that has not arrived.

**Fix:** Render the current mode in the help overlay under a new
"— current mode —" sub-table. The header `forceMode` is no longer
needed.

- [ ] **Step 1: Pass the mode to the help overlay**

In `help.Overlay`, accept an `OverlayHints` struct with an
optional `Mode` field. Render it as the first sub-table.

- [ ] **Step 2: Wire the call**

In `view.go` (where `help.Overlay` is called), pass
`help.OverlayHints{Mode: m.state.Config.Mode}` (or however the
current mode is accessed).

- [ ] **Step 3: Test**

```go
func TestOverlayRendersMode(t *testing.T) {
    out := help.Overlay(120, 60, help.OverlayHints{Mode: "ask"})
    if !strings.Contains(out, "ask") {
        t.Fatalf("mode not rendered: %s", out)
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestOverlay' -v
git add internal/app/tui/help/help.go internal/app/tui/help/help_test.go internal/app/tui/model.go internal/app/tui/view.go
git commit -m "feat(tui/help): show current mode in overlay (F-POL-162)"
```

---

### Task 9: F-POL-164 + F-POL-165 — Use rendered height and single source of truth for popup math

**Files:**
- Modify: `internal/app/tui/model.go` (`inputAreaRows`),
  `internal/app/tui/view.go` (`renderCompletionPopup`),
  `internal/app/tui/completions.go` (`reconcileOffset`)

**Problem (F-POL-164):** `inputAreaRows` counts `len(strings.Split(
content, "\n"))`. Wrapped content (e.g. an approval form with a
long description) consumes more terminal rows than the line count.

**Problem (F-POL-165):** `renderCompletionPopup` re-derives
`max`/`offset` independently of `completionPopup.reconcileOffset`.
Drift is possible.

**Fix (F-POL-164):** Replace the line count with
`lipgloss.Height(content)` after a layout pass.

**Fix (F-POL-165):** Move all offset/index clamping into
`completionPopup.reconcileOffset`. `renderCompletionPopup`
consumes only the result.

- [ ] **Step 1: Update `inputAreaRows`**

```go
rendered := m.renderQuestionPanel(...) // or approval panel
rows := max(rows, lipgloss.Height(rendered))
```

(Use the actual rendering function; if it does not exist for
pending approval, render the placeholder and pass that through
`lipgloss.Height`.)

- [ ] **Step 2: Centralise popup math**

```go
func (p *completionPopup) reconcileOffset() {
    if p.offset >= len(p.candidates) {
        p.offset = len(p.candidates) - 1
    }
    if p.offset < 0 {
        p.offset = 0
    }
    // existing math
}
```

Remove the duplicate math in `renderCompletionPopup`.

- [ ] **Step 3: Test**

```go
func TestInputAreaRowsHandlesWrappedContent(t *testing.T) {
    m := newTestModel()
    m.state.SetPendingApproval(&session.PendingToolCall{Command: strings.Repeat("x ", 200)})
    rows := m.inputAreaRows()
    lineCount := len(strings.Split(m.approvalModel.View(), "\n"))
    if rows < lineCount {
        t.Fatalf("rows undercount: %d < %d", rows, lineCount)
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestInputArea|TestCompletion' -v
git add internal/app/tui/model.go internal/app/tui/view.go internal/app/tui/completions.go internal/app/tui/completions_test.go
git commit -m "refactor(tui): use lipgloss.Height + single popup math (F-POL-164, F-POL-165)"
```

---

### Task 10: F-POL-170 — `renderSwarmPanel` only emits lines for existing roles

**Files:**
- Modify: `internal/app/tui/swarm_panel.go`
- Add tests: `internal/app/tui/swarm_panel_test.go`

**Problem:** The loop always prints 5 newlines, leaving blank
rows when fewer roles are active.

**Fix:** Iterate over `m.state.SwarmRoles()` (or equivalent) and
emit one row per active role. If no roles, return an empty
string (or a single `— no swarm active —` placeholder).

- [ ] **Step 1: Iterate active roles**

```go
roles := m.state.SwarmRoles()
if len(roles) == 0 {
    return ""
}
var b strings.Builder
for _, r := range roles {
    fmt.Fprintf(&b, "%s: %s\n", r.Name, r.Status)
}
return b.String()
```

- [ ] **Step 2: Test**

```go
func TestSwarmPanelEmitsOneLinePerRole(t *testing.T) {
    m := newTestModel()
    m.state.SetSwarmRoles([]session.SwarmRole{{Name: "implementer"}, {Name: "tester"}})
    out := m.renderSwarmPanel()
    if got, want := strings.Count(out, "\n"), 2; got != want {
        t.Fatalf("expected %d lines, got %d (%q)", want, got, out)
    }
}
```

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestSwarmPanel' -v
git add internal/app/tui/swarm_panel.go internal/app/tui/swarm_panel_test.go
git commit -m "fix(tui/swarm): only emit lines for active roles (F-POL-170)"
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
```

Manual smoke checklist:

- [ ] Open settings during a pending approval; the save control
      dims and shows "Resolve the pending tool approval to save."
- [ ] Mark a memory item stale; the row updates and a footer
      message confirms.
- [ ] Activate the browser bar; the right-side status URL segment
      disappears.
- [ ] SDD panel with no tasks renders zero rows.
- [ ] Type `/pla`; accept "plan" from the popup; type a space;
      the popup re-opens with argument hints.

Update `docs/14-codebase-improvement-audit-2026-07-14.md` with the
new resolution table entries.
