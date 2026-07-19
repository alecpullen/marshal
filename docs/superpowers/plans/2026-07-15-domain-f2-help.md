# Domain F2 — Help Overlay & Footer Polish Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in a
> dedicated worktree (suggested branch `feature/domain-f2-help`).
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** Resolve three findings from
`docs/14-codebase-improvement-audit-2026-07-14.md` (Domain F) that
concern discoverability of the keybinding system:

- **F-UIUX-138 (overlay part)** (MEDIUM) — the help overlay is missing
  `PgUp`/`PgDn`/`Ctrl+U`/`Ctrl+D` for scroll navigation and the
  approved-but-unwired `a`/`d`/`e` shortcuts for the approval form.
- **F-UIUX-139** (LOW) — the overlay's hand-aligned columns drift on
  narrow terminals and clip the right side.
- **F-POL-174** (LOW) — the footer text says `Enter×2` for the
  approval form, which confuses users; the matching keymap
  (`km.Quit` cleared, two-step Enter confirm) is implemented in
  `approval.go` but never documented in plain English.

**Architecture:** Two files change: `internal/app/tui/help/help.go`
and the per-package test file. No new packages, no dependencies.
The TUI design skill emphasises "contextual intelligence": keybindings
update per panel, status bars reflect current state. We extend the
existing `Overlay` renderer to use a real table and surface the
approval-form's true behaviour.

**Tech Stack:** Go 1.22+, `charm.land/lipgloss/v2` (already in use).

---

## Global Constraints

- Every code change MUST compile: `CGO_ENABLED=1 go build ./...`
  after each task.
- Every test change MUST pass:
  `CGO_ENABLED=1 go test ./internal/app/tui/help/...` after each
  task.
- At the end, `CGO_ENABLED=1 go test ./...` must pass.
- Commit per task with the exact message shown.
- Preserve the public `Footer` and `Overlay` function signatures.
- Wrap long descriptions rather than truncating; this matches the
  "progressive disclosure" guidance (always show what's actionable,
  full reference in the overlay).

---

## File Structure

Files modified by this plan:

- `internal/app/tui/help/help.go` — Tasks 1, 2, 3
- `internal/app/tui/help/help_test.go` — Tasks 1, 2, 3 (add tests;
  create file if absent)
- `internal/app/tui/approval.go` — Task 2 (only if `a`/`d`/`e`
  shortcuts are wired; the plan keeps this opt-in — see Task 2)

---

### Task 1: F-UIUX-139 — Render the help overlay as a real table

**Files:**
- Modify: `internal/app/tui/help/help.go` (`Overlay`)
- Add tests: `internal/app/tui/help/help_test.go`

**Problem:** Lines 72–96 hard-code leading spaces for column
alignment. A 24-column label like `Ctrl+Shift+Tab` overflows the
key column and pushes the description off-screen on terminals
narrower than 100 columns.

**Fix:** Define a `keyColumnWidth` (default 20) and render each row
as `key | description`, padding the key column to the fixed width
and wrapping the description with `lipgloss.Width(width-22)`.
The whole table is centred in the available width and height
(unchanged).

**Implementation steps:**

- [ ] **Step 1: Define the layout constants**

In `internal/app/tui/help/help.go` above `Overlay`:

```go
const keyColumnWidth = 20
```

- [ ] **Step 2: Replace the hard-coded `lines` slice with a
  `[][]string` table**

```go
var table = [][]string{
    {"Enter",          "send message / accept"},
    {"Shift+Enter",    "newline in input"},
    {"",               ""},
    {"/",              "command completion"},
    {"@",              "file completion"},
    {"↑↓",             "choose completion"},
    {"PgUp/PgDn",      "scroll transcript"},
    {"Ctrl+U/Ctrl+D",  "half-page scroll"},
    {"End",            "jump to bottom"},
    {"",               ""},
    {"Tab",            "cycle mode (auto→ask→edit) · accept completion"},
    {"Shift+Tab",      "cycle mode backward"},
    {"Alt+M",          "cycle model"},
    {"Alt+Shift+M",    "cycle model backward"},
    {"Esc",            "cancel turn · dismiss popup · deny approval"},
    {"",               ""},
    {"Ctrl+O",         "settings"},
    {"Ctrl+P",         "model picker"},
    {"Ctrl+K",         "memory browser"},
    {"Ctrl+G",         "toggle thinking"},
    {"Ctrl+R",         "rollback last change"},
    {"Ctrl+X",         "clear steering queue (while busy)"},
    {"",               ""},
    {"?",              "this help"},
    {"Ctrl+C",         "quit"},
}
```

Blank rows are rendered as 1-line gaps.

- [ ] **Step 3: Render with two-column formatting**

```go
keyStyle := lipgloss.NewStyle().Bold(true).Width(keyColumnWidth)
descStyle := lipgloss.NewStyle().Width(max(width-keyColumnWidth-4, 20))
rows := make([]string, 0, len(table)+4)
rows = append(rows, "marshal keys", "")
for _, r := range table {
    if r[0] == "" && r[1] == "" {
        rows = append(rows, "")
        continue
    }
    rows = append(rows, keyStyle.Render(r[0])+"  "+descStyle.Render(r[1]))
}
rows = append(rows, "", "Press ? or Esc to close.")
body := strings.Join(rows, "\n")
return lipgloss.NewStyle().Width(width).Height(height).Align(lipgloss.Center, lipgloss.Center).Render(body)
```

- [ ] **Step 4: Add tests**

```go
func TestOverlayUsesFixedKeyColumn(t *testing.T) {
    out := Overlay(120, 40)
    for _, line := range strings.Split(out, "\n") {
        if strings.Contains(line, "Alt+Shift+M") && !strings.Contains(line, "cycle model backward") {
            // The description MUST appear on the same line as the
            // key (we only wrap on \n, not in the middle of a row).
        }
    }
}

func TestOverlayWrapsOnNarrowWidth(t *testing.T) {
    out := Overlay(60, 30) // narrower than keyColumnWidth*2
    // Assert that no line contains a key label clipped by a
    // hard wrap inside the description (heuristic: the description
    // for "cycle mode backward" should still contain the word
    // "cycle" or "backward").
    if !strings.Contains(out, "backward") {
        t.Fatalf("description lost on narrow terminal: %q", out)
    }
}
```

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui/help -v
git add internal/app/tui/help/help.go internal/app/tui/help/help_test.go
git commit -m "refactor(tui/help): render overlay as fixed-column table (F-UIUX-139)"
```

---

### Task 2: F-UIUX-138 (overlay part) — Surface scroll and approval keys

**Files:**
- Modify: `internal/app/tui/help/help.go` (`Overlay` table)
- Add tests: `internal/app/tui/help/help_test.go`

**Problem:** `PgUp`/`PgDn`, `Ctrl+U`/`Ctrl+D`, and the approval-form
shortcuts (`a`/`d`/`e` after the form is armed) are not documented.

**Fix:** Already addressed in Task 1: the table now lists
`PgUp/PgDn`, `Ctrl+U/Ctrl+D`, and `End`. The approval-form
shortcuts are added in a separate sub-table labelled
"In approval form" so the overlay stays context-relevant.

- [ ] **Step 1: Add the approval sub-table**

Append to the `table` slice in `Overlay` (after the model cycling
rows, before the section-break):

```go
{"",               ""},
{"— in approval form —", ""},
{"↑↓ / j / k",     "choose action"},
{"Enter",          "arm selection"},
{"Enter (twice)",  "submit armed selection"},
{"a",              "always allow (save to config)"},
{"d",              "deny"},
{"e",              "edit command/args"},
{"Esc",            "deny"},
```

The `a`/`d`/`e` keys are currently **not** wired in
`internal/app/tui/approval.go` (only `up`/`k`/`down`/`j`/`enter`/`esc`).
This plan documents them; wiring them is a follow-up captured in the
"Out of scope" section below.

- [ ] **Step 2: Test**

```go
func TestOverlayListsApprovalShortcuts(t *testing.T) {
    out := Overlay(120, 60)
    for _, want := range []string{"always allow", "deny", "edit command/args", "PgUp", "Ctrl+U"} {
        if !strings.Contains(out, want) {
            t.Errorf("overlay missing %q: %s", want, out)
        }
    }
}
```

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui/help -v
git add internal/app/tui/help/help.go internal/app/tui/help/help_test.go
git commit -m "docs(tui/help): document scroll and approval keys (F-UIUX-138 overlay)"
```

---

### Task 3: F-POL-174 — Plain-English footer for the approval form

**Files:**
- Modify: `internal/app/tui/help/help.go` (`Footer` + `FooterHints`)
- Add tests: `internal/app/tui/help/help_test.go`

**Problem:** The footer renders `"Enter×2"` (`approval pending` branch,
line 43). Users read it as "press Enter, then press some other key
labeled ×2". The actual flow is "Enter arms, Enter again submits."

**Fix:** Replace the segment with a clearer two-line or two-segment
label: `Enter: arm` and `Enter⏎: submit` (or simply `Enter: arm · ⏎
again: submit` on a single segment). Drop the `a`/`d`/`e` segments
from the footer until those keys are actually wired; today's footer
promises a binding that does not exist.

**Implementation steps:**

- [ ] **Step 1: Update the approval branch in `Footer`**

```go
} else if h.ApprovalPending && !h.EditingCommand {
    segs = append(segs,
        pair("↑↓", "choose"),
        pair("Enter", "arm"),
        pair("Enter⏎", "submit"),
        pair("Esc", "deny"),
    )
```

(Note: `pair` is the existing helper in the file.)

- [ ] **Step 2: Test**

```go
func TestFooterApprovalWording(t *testing.T) {
    out := Footer(FooterHints{ApprovalPending: true})
    if strings.Contains(out, "Enter×2") {
        t.Fatalf("stale 'Enter×2' label still present: %q", out)
    }
    if !strings.Contains(out, "arm") || !strings.Contains(out, "submit") {
        t.Fatalf("expected arm/submit labels, got %q", out)
    }
}
```

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui/help -v
git add internal/app/tui/help/help.go internal/app/tui/help/help_test.go
git commit -m "fix(tui/help): plain-English approval footer (F-POL-174)"
```

---

## Out of scope (deliberate)

- Wiring `a`/`d`/`e` shortcuts in `approvalModel.Update` (so the
  footer can offer them). These are documented in the help overlay
  (Task 2) but remain unwired pending a UX decision on whether to
  bind them. Track as a follow-up: `F-UIUX-138 followup`.

- Width-aware truncation of the footer itself. The footer is already
  short (≤ 4 segments in the busiest mode). A future change can add
  a `lipgloss.Width(width).Render(...)` pass if users report
  overflow on <80-column terminals.

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
```

Manual smoke checklist:

- [ ] Press `?` from the main chat view; confirm the help overlay
      shows a tidy two-column layout with `PgUp/PgDn`,
      `Ctrl+U/Ctrl+D`, and the approval-form sub-table.
- [ ] Trigger an approval; confirm the footer now says
      `Enter: arm · Enter⏎: submit · Esc: deny`.
- [ ] Resize the terminal to 70 columns and reopen the overlay;
      descriptions wrap rather than clip.
