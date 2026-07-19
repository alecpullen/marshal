# Hairline Gutter TUI — Phase 1 (Chrome & Dock) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce Marshal's idle chrome from 7 rows to 2 by deleting the title bar, footer rule, and hint row; merging hints into the status line; replacing the input border box with a `▍` state bar; and rewriting `chrome.Panel` as a gutter-style frame (settings/picker/memory/connect inherit it).

**Architecture:** Pure render-layer change per the spec `docs/superpowers/specs/2026-07-19-hairline-gutter-tui-design.md` ("Phasing" → phase 1). No logic, keybinding, or session-state changes. All work is in `internal/app/tui/` (view.go, status.go, model.go, help/, chrome/) plus the three dock panels that pass content into `chrome.Panel` (settings/browser.go, picker/picker.go, memory/panel.go).

**Tech Stack:** Go, Bubble Tea v2 (`charm.land/bubbletea/v2`), lipgloss v2, `github.com/charmbracelet/x/ansi`. Build needs `CGO_ENABLED=1` (tree-sitter dep elsewhere in the module).

## Global Constraints

- All colors must come from theme slots (`theme.Theme` fields / the named color vars in model.go). Never hardcode color values.
- Meaning must survive NO_COLOR: the `▍`/`❯` glyphs themselves are the markers; color is secondary (spec "Degradation and accessibility").
- `go vet ./...` has two PRE-EXISTING failures in `internal/app/session/session.go` (lock-copy). Do not fix them in this plan; do not add new vet failures. Vet the tui packages only: `go vet ./internal/app/tui/...`.
- Run tests with `go test ./internal/app/tui/...` per task and `go test ./...` in the final task.
- Commit after every task. Do not push.
- The spec is the authority on visuals; if a rendering question is not answered here, check the spec's "Per-widget mapping" table.

---

### Task 1: Trim the idle hint set in help.Footer

The footer's idle hints shrink to `Tab mode · / cmd · ? help` (+ `Ctrl+R rollback` when eligible) per the spec's status-line row. Mode-specific sets (busy/approval/question/popup/edit) are unchanged. `help.Rows` will be deleted in Task 3 — leave it for now.

**Files:**
- Modify: `internal/app/tui/help/help.go` (idle branch, ~lines 66-81)
- Test: `internal/app/tui/help/help_test.go`

**Interfaces:**
- Produces: `help.Footer(FooterHints) string` — unchanged signature, trimmed idle output. Task 2 calls it from the status line.

- [ ] **Step 1: Update the idle tests to the trimmed set**

Replace `TestFooterIdle` and delete `TestFooterIdleShowsThinkingToggle` in `internal/app/tui/help/help_test.go`:

```go
func TestFooterIdle(t *testing.T) {
	out := stripANSI(Footer(FooterHints{}))
	for _, want := range []string{"Tab mode", "/ cmd", "? help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("idle footer missing %q: %q", want, out)
		}
	}
	// The idle set is deliberately minimal; the full cheatsheet lives
	// behind ? (/help). These must NOT appear:
	for _, gone := range []string{"Alt+M", "@", "Ctrl+G"} {
		if strings.Contains(out, gone) {
			t.Fatalf("idle footer still shows %q (moved behind ?): %q", gone, out)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/app/tui/help/ -run TestFooterIdle -v`
Expected: FAIL — output still contains "Alt+M".

- [ ] **Step 3: Trim the idle branch in help.go**

In `Footer`, replace the final `else` branch (currently appending Tab/Alt+M///@ then Ctrl+R and Ctrl+G):

```go
	} else {
		segs = append(segs,
			pair("Tab", "mode"),
			pair("/", "cmd"),
		)
		if h.IdleRollbackEligible {
			segs = append(segs, pair("Ctrl+R", "rollback"))
		}
	}
```

Delete the `ThinkingVisible` Ctrl+G segment entirely. Keep the `showHelpHint` block that appends `pair("?", "help")`. Remove the now-unused `ThinkingVisible` field from `FooterHints` and its doc comment.

- [ ] **Step 4: Fix the one FooterHints construction site**

`renderHelpFooter` in `internal/app/tui/view.go` (~line 198) sets `ThinkingVisible: m.thinkingExpanded` — delete that field from the literal. (The whole function dies in Task 3; this just keeps the build green.)

- [ ] **Step 5: Run package tests**

Run: `go test ./internal/app/tui/... 2>&1 | tail -5`
Expected: all PASS (view tests only assert "Tab"/"model"/"help" substrings — "mode" still matches).
If `TestViewContainsFooter` fails on "Alt+M": that assertion is deleted in Task 3; for now change its `want` list to `{"Tab", "mode", "help"}`.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/help/ internal/app/tui/view.go internal/app/tui/view_test.go
git commit -m "feat(tui): trim idle hints to Tab/slash/help"
```

---

### Task 2: Status line right cluster shows contextual hints when idle

The status line's right segment currently returns `""` when nothing is running. It becomes the home of the hints: activity/approval/error/done-flash keep priority; otherwise render `help.Footer` for the current mode.

**Files:**
- Modify: `internal/app/tui/status.go` (`statusRightSegment`, imports)
- Test: `internal/app/tui/status_test.go`

**Interfaces:**
- Consumes: `help.Footer(help.FooterHints) string` from Task 1.
- Produces: `Model.footerHints() help.FooterHints` (method on Model, defined in status.go) — Task 3 reuses it if needed; `statusRightSegment` now never returns `""` when idle.

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/tui/status_test.go`:

```go
func TestStatusLineShowsIdleHints(t *testing.T) {
	m := newStatusTestModel(t)
	line := stripANSI(m.renderStatusLine(100))
	for _, want := range []string{"Tab mode", "/ cmd", "? help"} {
		if !strings.Contains(line, want) {
			t.Fatalf("idle status line missing hint %q:\n%s", want, line)
		}
	}
}

func TestStatusLineHintsYieldToActivity(t *testing.T) {
	m := newStatusTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(104, 0) }
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(100, 0)})
	line := stripANSI(m.renderStatusLine(100))
	if strings.Contains(line, "Tab mode") {
		t.Fatalf("hints must yield to activity in the right cluster:\n%s", line)
	}
	if !strings.Contains(line, "shell.run: go test") {
		t.Fatalf("activity missing from status line:\n%s", line)
	}
}
```

- [ ] **Step 2: Run to verify the first fails**

Run: `go test ./internal/app/tui/ -run 'TestStatusLineShowsIdleHints|TestStatusLineHintsYieldToActivity' -v`
Expected: `TestStatusLineShowsIdleHints` FAILS (right cluster empty); the yield test passes already.

- [ ] **Step 3: Implement**

In `internal/app/tui/status.go`, add the import `"marshal/internal/app/tui/help"` and change the tail of `statusRightSegment` — replace the final `return ""` with:

```go
	return help.Footer(m.footerHints())
```

(Do NOT wrap the Footer output in another style: it already carries bold
keys and faint separators, and an outer `Render` would stop at the first
inner reset sequence.)

and add below the function:

```go
// footerHints snapshots the mode flags the hint cluster needs. This is
// the FooterHints construction that used to live in the dedicated
// footer row (deleted in the hairline-gutter redesign).
func (m Model) footerHints() help.FooterHints {
	return help.FooterHints{
		Busy:                 m.busy,
		EditingCommand:       m.editingCommand,
		ApprovalPending:      m.state.PendingApproval() != nil,
		QuestionPending:      m.state.PendingQuestion() != nil,
		PopupOpen:            m.activeCompletionPopup() != nil,
		IdleRollbackEligible: !m.busy && m.state.HasBackup(),
	}
}
```

Leave the `⚠ approval`, activity, `✘ error`, and `✔ done` branches above untouched — they still win over hints (spec: "activity/approval state when busy (unchanged)").

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/tui/ -run TestStatusLine -v 2>&1 | tail -15`
Expected: all PASS, including the pre-existing width tests (`TestStatusLineFitsWidth`, `TestStatusLineFitsVeryNarrowWidths` — the left-cluster drop logic already handles a wide right cluster).

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/status.go internal/app/tui/status_test.go
git commit -m "feat(tui): status line right cluster shows contextual hints when idle"
```

---

### Task 3: Delete the footer row and rule

With hints living in the status line, the dedicated footer row and its full-width `─` rule are dead chrome.

**Files:**
- Modify: `internal/app/tui/view.go` (constants, `viewString`, delete `renderHelpFooter`)
- Modify: `internal/app/tui/model.go:564` and `model.go:1243` (height formulas)
- Modify: `internal/app/tui/help/help.go` (delete `Rows`)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Produces: frame rows are now `[transcript, panels..., dock, input, status]`. `footerRows` and `commandBarRows` no longer exist; height formulas subtract only `statusLineRows`.

- [ ] **Step 1: Rewrite the footer tests**

In `internal/app/tui/view_test.go`, replace `TestViewContainsFooter` (line ~54) and `TestCommandBarHasTopBorder` (line ~459) with:

```go
func TestViewShowsIdleHintsInStatusLine(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	view := stripANSI(m.View().Content)
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	last := lines[len(lines)-1]
	for _, want := range []string{"Tab mode", "? help"} {
		if !strings.Contains(last, want) {
			t.Fatalf("status line (last row) missing hint %q:\n%s", want, last)
		}
	}
}

func TestViewHasNoFooterRule(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	view := stripANSI(m.View().Content)
	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 20 && strings.Count(trimmed, "─") == len([]rune(trimmed)) {
			t.Fatalf("view still contains a full-width rule:\n%q", line)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestViewShowsIdleHintsInStatusLine|TestViewHasNoFooterRule' -v`
Expected: both FAIL (footer row is the last-but-one row; rule exists).

- [ ] **Step 3: Delete the footer**

In `internal/app/tui/view.go`:
- Constants block: delete `footerRows = help.Rows` and `commandBarRows = footerRows + 1`.
- `viewString`: change the final append to `rows = append(rows, m.renderInputArea(), m.renderStatusLine(m.width))`.
- Delete the whole `renderHelpFooter` function (and its comment).
- Remove the now-unused `"marshal/internal/app/tui/help"` import from view.go.

In `internal/app/tui/model.go`, both height formulas (line ~564 in `resize` and ~1243 in `updateViewportHeight`) drop `-commandBarRows`:

```go
	m.viewport.SetHeight(max(height-titleBarRows-transcriptFrameRows-m.swarmPanelRows()-m.sddPanelRows()-m.browserBarRows()-m.dockRows()-m.inputAreaRows()-statusLineRows, 1))
```

(and the same expression assigned to `newViewportHeight` in `updateViewportHeight`).

In `internal/app/tui/help/help.go`: delete the `Rows` constant and its comment.

- [ ] **Step 4: Fix the geometry test**

`TestResizeComputesSingleColumnGeometry` (view_test.go ~line 224): update the expectation:

```go
	wantHeight := 30 - titleBarRows - m.inputAreaRows() - statusLineRows
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/app/tui/... 2>&1 | tail -5`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/view.go internal/app/tui/model.go internal/app/tui/help/help.go internal/app/tui/view_test.go
git commit -m "feat(tui): delete footer row and rule; status line is the only bottom chrome"
```

---

### Task 4: Delete the title bar; working dir joins the status segments

**Files:**
- Modify: `internal/app/tui/view.go` (constants, `viewString`, delete `renderTitleBar`)
- Modify: `internal/app/tui/status.go` (`statusLeftSegments`)
- Modify: `internal/app/tui/model.go` (height formulas: drop `titleBarRows`)
- Test: `internal/app/tui/view_test.go`, `internal/app/tui/status_test.go`

**Interfaces:**
- Produces: `statusLeftSegments` gains a working-dir segment at priority 5. Frame's first row is the transcript.

- [ ] **Step 1: Write the failing tests**

In `internal/app/tui/view_test.go`, delete `TestViewHasTitleBar` and `TestTitleBarShowsWorkingDir`, add to `internal/app/tui/status_test.go`:

```go
func TestStatusLineShowsWorkingDir(t *testing.T) {
	dir := t.TempDir()
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	line := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(line, filepath.Base(dir)) {
		t.Fatalf("status line missing working dir base %q:\n%s", filepath.Base(dir), line)
	}
}
```

Add `"path/filepath"` to status_test.go imports.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/app/tui/ -run TestStatusLineShowsWorkingDir -v`
Expected: FAIL — dir not in status line.

- [ ] **Step 3: Implement**

In `internal/app/tui/status.go` `statusLeftSegments`, insert after the branch segment block (the `if leaves := m.state.Branches(); len(leaves) > 1 { ... }` block):

```go
	if wd := m.state.WorkingDir; wd != "" {
		segs = append(segs, statusSeg{text: filepath.Base(wd), priority: 5})
	}
```

Add `"path/filepath"` to status.go imports and update the priority doc comment above `statusLeftSegments` to read `... turn=4, branch=5, dir=5, swarm tokens=6, ...`.

In `internal/app/tui/view.go`:
- Delete `titleBarRows` from the constants block.
- `viewString`: change `rows := []string{m.renderTitleBar(m.width), m.renderTranscriptFrame()}` to `rows := []string{m.renderTranscriptFrame()}`.
- Delete the `renderTitleBar` function and its comment. If `"path/filepath"` and the `dot`/`brand` helpers become unused imports in view.go, remove them.

In `internal/app/tui/model.go`: remove `-titleBarRows` from both height formulas (resize ~564 and `updateViewportHeight` ~1243).

In `internal/app/tui/view_test.go` `TestResizeComputesSingleColumnGeometry`: `wantHeight := 30 - m.inputAreaRows() - statusLineRows`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/tui/... 2>&1 | tail -5`
Expected: all PASS. If `TestViewIsSingleColumn` fails on the `"❯"` assertion, the input prompt still provides it — investigate before touching the test.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/view.go internal/app/tui/status.go internal/app/tui/model.go internal/app/tui/view_test.go internal/app/tui/status_test.go
git commit -m "feat(tui): delete title bar; working dir joins status segments"
```

---

### Task 5: De-box the input area — `▍` state bar replaces the border

The rounded input box (3 rows for 1 line of text) becomes a flat `▍`-barred textarea. The bar color carries the old border-color state. The in-box activity strip is deleted (activity already lives in the status line's right cluster).

**Files:**
- Modify: `internal/app/tui/view.go` (`renderInputArea`, delete `renderActivityStrip`, constants)
- Modify: `internal/app/tui/model.go` (`inputAreaRows`, `resize` SetWidth, delete `inputBoxStyle`)
- Test: `internal/app/tui/view_test.go`, `internal/app/tui/model_test.go:4707`

**Interfaces:**
- Produces: `Model.inputBarColor() color.Color` and `Model.gutteredInput() string` in view.go. `inputAreaRows()` returns content rows only (no border overhead). Input text width is `terminal width − 2`.

- [ ] **Step 1: Rewrite the input-state tests**

In `internal/app/tui/view_test.go` replace `TestInputBorderPulsesTealOnSuccess` and `TestInputBorderColorReflectsFocus` with:

```go
func TestInputBarPulsesTealOnSuccess(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.successPulse = true
	teal := m.renderInputArea()
	m.successPulse = false
	neutral := m.renderInputArea()
	if teal == neutral {
		t.Fatal("success pulse bar should differ from the default focused bar")
	}
	if !strings.Contains(stripANSI(teal), "▍") {
		t.Fatalf("input area missing state bar:\n%s", stripANSI(teal))
	}
}

func TestInputBarColorReflectsFocus(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	focused := m.renderInputArea()
	m.input.Blur()
	blurred := m.renderInputArea()
	if focused == blurred {
		t.Fatal("focused and blurred input should have different bar colors")
	}
	if focused == stripANSI(focused) {
		t.Fatalf("focused input area has no ANSI styling:\n%q", focused)
	}
}

func TestInputAreaHasNoBorderBox(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	out := stripANSI(m.renderInputArea())
	for _, glyph := range []string{"╭", "╰", "│"} {
		if strings.Contains(out, glyph) {
			t.Fatalf("input area still box-bordered (%q):\n%s", glyph, out)
		}
	}
	if !strings.Contains(out, "▍❯") {
		t.Fatalf("input area missing ▍❯ prompt:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify the box test fails**

Run: `go test ./internal/app/tui/ -run 'TestInputBar|TestInputAreaHasNoBorderBox' -v`
Expected: `TestInputAreaHasNoBorderBox` FAILS (box glyphs present, no `▍❯`).

- [ ] **Step 3: Implement the flat input area**

In `internal/app/tui/view.go`:

Delete the constants `inputBorderRows` and `activityStripRows`. Delete the `renderActivityStrip` function. Replace `renderInputArea` with:

```go
func (m Model) renderInputArea() string {
	inputInnerWidth := max(m.width-4, 1)
	rows := make([]string, 0, 4)

	if q := m.state.PendingQuestion(); q != nil {
		if m.questionModel != nil {
			rows = append(rows, m.questionModel.View())
		} else {
			rows = append(rows, renderQuestionPanel(q, inputInnerWidth))
			rows = append(rows, m.gutteredInput())
		}
	} else if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			rows = append(rows, m.gutteredInput())
		} else if m.approvalModel != nil {
			rows = append(rows, m.approvalModel.View())
		} else {
			rows = append(rows, renderApprovalPanel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, inputInnerWidth))
		}
	} else {
		if m.state.SDDProgress().Active {
			rows = append(rows, mutedStyle().Render("SDD running — /stop to cancel, wait for completion to resume typing"))
		}
		if popup := m.renderCompletionPopup(); popup != "" {
			rows = append(rows, popup)
		}
		rows = append(rows, m.gutteredInput())
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// inputBarColor picks the ▍ state-bar color. This is the input box's old
// border-color semantics compressed into one cell (spec: "state moves to
// the ▍❯ prompt").
func (m Model) inputBarColor() color.Color {
	switch {
	case m.successPulse:
		return tealColor
	case m.state.PendingQuestion() != nil:
		return violetColor
	case m.state.PendingApproval() != nil:
		return warningColor
	case m.state.SDDProgress().Active, !m.input.Focused():
		return dimColor
	default:
		return coralColor
	}
}

// gutteredInput renders the textarea with the ▍ state bar prepended to
// every display line.
func (m Model) gutteredInput() string {
	bar := lipgloss.NewStyle().Foreground(m.inputBarColor()).Render("▍")
	lines := strings.Split(m.input.View(), "\n")
	for i := range lines {
		lines[i] = bar + lines[i]
	}
	return strings.Join(lines, "\n")
}
```

Add `"image/color"` to view.go imports.

In `internal/app/tui/model.go`:
- Delete the `inputBoxStyle` function (~line 2566).
- `inputAreaRows` (~line 1163): change `rows := inputBorderRows` to `rows := 0` and delete the activity-strip branch (`if m.state.Activity().Kind != session.ActivityIdle { rows += activityStripRows }`).
- `resize` (~line 560): change `m.input.SetWidth(max(width-8, 1))` to `m.input.SetWidth(max(width-2, 1))` and rewrite the comment block above it: the reserved cells are now 1 for the `▍` bar plus 1 right margin; the textarea's own SetWidth still handles the 2-cell `❯ ` prompt internally.

- [ ] **Step 4: Fix dependent tests**

- `internal/app/tui/model_test.go:4707`: change `if rows < inputBorderRows+1 {` to `if rows < 1 {` (the input area's floor is now the textarea's single row).
- `TestMultilineInputAlignsContinuationLines` (view_test.go ~line 296): update the width-math comment from "(80 - 4 box frame - 2 prompt = 74 text columns)" to "(80 - 2 reserved - 2 prompt = 76 text columns)". The structural assertions (prefix `❯ `, 2-space continuations on the raw textarea view) are unchanged — the bar is added outside the textarea.
- `TestInputWrapsBeforeBoxContentWidth` (view_test.go ~line 401): rename to `TestInputWrapsBeforeTerminalWidth` and update any `width-8` arithmetic inside to `width-2`. The invariant being tested (no rendered input line exceeds terminal width) is unchanged.
- `TestResizeComputesSingleColumnGeometry`: no change needed beyond Task 4's version — `inputAreaRows()` shrank on both sides of the equation.

- [ ] **Step 5: Run the full tui test set**

Run: `go test ./internal/app/tui/... 2>&1 | tail -8`
Expected: all PASS. Likely stragglers: any test grepping the input area for `╭` (check `internal/app/tui/approval_test.go`, `smoke_pickers_test.go`, `model_test.go` — run `grep -n '╭' internal/app/tui/*_test.go` and update those that assert the *input box* to expect `▍` instead; leave chrome.Panel assertions alone, they change in Task 6).

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/view.go internal/app/tui/model.go internal/app/tui/view_test.go internal/app/tui/model_test.go
git commit -m "feat(tui): replace input border box with ▍ state bar; delete activity strip"
```

---

### Task 6: Rewrite chrome.Panel as a gutter frame (dock panels inherit)

`chrome.Panel` keeps its signature; a new `PanelWithHints` adds right-aligned key hints on the header line. Settings/picker/memory drop their internal `─` rules and move key hints to the header. Connect inherits with no changes.

**Files:**
- Modify: `internal/app/tui/chrome/chrome.go`
- Modify: `internal/app/tui/settings/browser.go` (`View`, ~lines 447-479)
- Modify: `internal/app/tui/picker/picker.go` (`View`, ~lines 156-226)
- Modify: `internal/app/tui/memory/panel.go` (`View`, ~lines 195-223)
- Test: `internal/app/tui/chrome/chrome_test.go`, `internal/app/tui/view_test.go` (docked-panel assertions), `internal/app/tui/settings/nocolor_test.go`

**Interfaces:**
- Produces: `chrome.PanelWithHints(title, hints, content string, w, h int, focused bool, th theme.Theme) string`. `chrome.Panel(title, content, w, h, focused, th)` delegates with empty hints. `chrome.ClipLines` unchanged. Panel output has NO box glyphs; every row starts with `" ▍ "` (space, bar, space); the header row exists only when `title != ""`; output is at most `h` rows and is not padded to `h`.

- [ ] **Step 1: Rewrite the chrome test**

Replace `TestPanelEmbedsTitleAndSizes` in `internal/app/tui/chrome/chrome_test.go`. The file already has `testTheme = theme.LoadFor(false, "xterm-256color")` and uses `ansi.Strip` — keep both, and keep `TestClipLinesWindowsAroundFocus` untouched:

```go
func TestPanelRendersGutterFrame(t *testing.T) {
	out := Panel("Settings", "line one\nline two", 40, 10, true, testTheme)
	plain := ansi.Strip(out)
	for _, glyph := range []string{"╭", "╰", "│", "─"} {
		if strings.Contains(plain, glyph) {
			t.Fatalf("panel still uses box glyph %q:\n%s", glyph, plain)
		}
	}
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 content rows = 3, got %d:\n%s", len(lines), plain)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, " ▍ ") {
			t.Fatalf("row %d missing gutter prefix: %q", i, l)
		}
	}
	if !strings.Contains(lines[0], "Settings") {
		t.Fatalf("header missing title: %q", lines[0])
	}
}

func TestPanelWithHintsRightAligns(t *testing.T) {
	out := ansi.Strip(PanelWithHints("Memory", "Esc close", "body", 40, 10, true, testTheme))
	header := strings.Split(out, "\n")[0]
	if !strings.Contains(header, "Memory") || !strings.Contains(header, "Esc close") {
		t.Fatalf("header missing title or hints: %q", header)
	}
	if strings.Index(header, "Esc close") < strings.Index(header, "Memory") {
		t.Fatalf("hints should be right of the title: %q", header)
	}
}

func TestPanelTruncatesToHeightBudget(t *testing.T) {
	out := ansi.Strip(Panel("T", "a\nb\nc\nd\ne", 40, 3, true, testTheme))
	if got := len(strings.Split(out, "\n")); got != 3 {
		t.Fatalf("panel must clamp to h rows, got %d:\n%s", got, out)
	}
}
```

No new imports needed — the file already imports `ansi`, `strings`, `testing`, and `theme`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/app/tui/chrome/ -v`
Expected: FAIL (box glyphs, no PanelWithHints symbol → compile error is the expected first failure).

- [ ] **Step 3: Rewrite chrome.go**

Replace the `isMonochrome` helper and `Panel` function in `internal/app/tui/chrome/chrome.go` with (keep `ClipLines` untouched; update the package doc comment to say "gutter-framed panels" instead of "bordered panels"):

```go
// Panel draws a gutter-framed panel: a ▍ column down the left with the
// title as a bold header line. The gutter uses accent.secondary when
// focused, fg.muted when not. Output is at most h rows and is not
// padded to h. In monochrome mode the ▍ glyph alone marks the panel.
func Panel(title, content string, w, h int, focused bool, th theme.Theme) string {
	return PanelWithHints(title, "", content, w, h, focused, th)
}

// PanelWithHints is Panel with dim key hints right-aligned on the
// header line (e.g. "↵ edit · Esc back"). Hints are dropped when the
// header has no room for them.
func PanelWithHints(title, hints, content string, w, h int, focused bool, th theme.Theme) string {
	gutterColor := th.FGMuted
	titleStyle := lipgloss.NewStyle().Foreground(th.FGMuted)
	if focused {
		gutterColor = th.AccentSecondary
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(th.AccentSecondary)
	}
	gutter := " " + lipgloss.NewStyle().Foreground(gutterColor).Render("▍") + " "
	inner := w - 3
	if inner < 1 {
		inner = 1
	}

	out := make([]string, 0, h)
	if title != "" {
		label := ansi.Truncate(title, inner, "…")
		head := titleStyle.Render(label)
		if hints != "" {
			gap := inner - ansi.StringWidth(label) - ansi.StringWidth(hints)
			if gap >= 2 {
				head += strings.Repeat(" ", gap) + lipgloss.NewStyle().Foreground(th.FGMuted).Render(hints)
			}
		}
		out = append(out, gutter+head)
	}
	budget := h - len(out)
	if budget < 0 {
		budget = 0
	}
	lines := strings.Split(content, "\n")
	if len(lines) > budget {
		lines = lines[:budget]
	}
	for _, l := range lines {
		out = append(out, gutter+ansi.Truncate(l, inner, "…"))
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run chrome tests**

Run: `go test ./internal/app/tui/chrome/ -v`
Expected: PASS.

- [ ] **Step 5: Update the three dock panels**

`internal/app/tui/settings/browser.go` `View` — delete the separator rule, move hints to the header, account for the 1-row header instead of 2 border rows:

```go
	panelWidth := min(72, max(width-2, 30))
	innerWidth := panelWidth - 3

	title := "Settings"
	hints := "↵ edit · Esc close"
	var body string
	var footer string
	if b.stack != nil {
		rootTitle := b.stack.stack[0].title
		title += " › " + b.stack.breadcrumb(rootTitle)
		hints = "↵ edit · Esc back"
		b.stack.SetSize(innerWidth, max(maxHeight-3, 1))
		body = b.stack.top().list.View()
		footer = fmt.Sprintf("%d settings", len(b.stack.top().list.Rows()))
	} else {
		b.list.SetSize(innerWidth, max(maxHeight-4, 1))
		body = "/ " + b.filter.View() + "\n" + b.list.View()
		footer = fmt.Sprintf("%d settings", len(b.list.Rows()))
	}
	content := body + "\n" + flDescStyle().Render(footer)
	panelHeight := min(lipgloss.Height(content)+1, maxHeight)
	return chrome.PanelWithHints(title, hints, content, panelWidth, panelHeight, true, settingsTheme())
```

Also relax the guard at the top of `View` from `if maxHeight < 3` to `if maxHeight < 2` and update its comment (the panel needs a header row plus one content row, not three border rows).

`internal/app/tui/picker/picker.go` `View` — same pattern:

```go
	if maxH < 2 {
		return ""
	}
	pw := min(64, maxW-8)
	if pw < 30 {
		pw = max(maxW-2, 30)
	}
	inner := pw - 3
```

then replace the footer/separator assembly at the bottom:

```go
	listH := maxH - 3 // header + filter + margin
	if listH < 3 {
		listH = 3
	}
	body := chrome.ClipLines(rows, focusLine, listH, theme.Current())
	content := "/ " + m.filter.View() + "\n" + body
	if m.footer != "" {
		content += "\n" + mutedStyle().Render(m.footer)
	}
	ph := min(lipgloss.Height(content)+1, maxH)
	return chrome.PanelWithHints(m.title, "↵ pick · Esc cancel", content, pw, ph, true, theme.Current())
```

(The old `[↑↓] move [↵] pick [Esc] cancel` footer line and the `─` separator are deleted; arrow-key movement is discoverable, `m.footer` keeps carrying picker-specific context as a trailing dim line.)

`internal/app/tui/memory/panel.go` `View` — delete the `─` separator; default hints to the header; keep the delete-confirm and load-error lines in the body (they are alerts, not hints):

```go
	listH := maxHeight - 3
	if listH < 1 {
		listH = 1
	}
	body := chrome.ClipLines(rows, focusLine, listH, theme.Current())

	content := "/ " + p.filter.View() + "\n" + body
	if p.deleteArmed {
		content += "\n" + mutedStyle().Render("press ctrl+d again to confirm delete · esc cancel")
	}
	if p.loadErr != nil {
		content += "\n" + mutedStyle().Render("load failed: "+p.loadErr.Error())
	}
	ph := min(lipgloss.Height(content)+1, maxHeight)
	return chrome.PanelWithHints("Memory", "⏎ show · ctrl+d delete · esc close", content, pw, ph, true, theme.Current())
```

Update `inner := pw - 2` to `inner := pw - 3` in memory's View as well.

- [ ] **Step 6: Update docked-panel assertions**

In `internal/app/tui/view_test.go`:
- `TestPickerRendersDockedAboveInput` (~line 87): change the left-alignment check from `strings.HasPrefix(..., "╭")` to `strings.HasPrefix(lines[panelLine], " ▍")`.
- `TestConnectRendersDockedAboveInput` (~line 107): change the border-row search `strings.Contains(line, "╭") && strings.Contains(line, "connect")` to `strings.Contains(line, "▍") && strings.Contains(line, "connect")`, and the same `HasPrefix` fix; update the stale comment about the border title.

Then sweep for remaining box-glyph assertions against panels:

Run: `grep -rn '╭\|╰' internal/app/tui --include='*_test.go'`
Update each hit that asserts chrome.Panel output (known files: `smoke_pickers_test.go`, `settings/nocolor_test.go`, possibly `model_test.go`) to the gutter equivalents: presence of `▍` instead of `╭`, absence of box glyphs. `settings/nocolor_test.go` asserts the mono border — its new invariant: NO_COLOR output still contains `▍` and zero SGR sequences. Do NOT touch assertions about the *transcript's* code-block borders (`transcript_test.go`, `approval_test.go` diff blocks) — those are phase 2.

- [ ] **Step 7: Run the full tui tree**

Run: `go test ./internal/app/tui/... 2>&1 | tail -8`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/chrome/ internal/app/tui/settings/ internal/app/tui/picker/ internal/app/tui/memory/ internal/app/tui/view_test.go
git commit -m "feat(tui): chrome.Panel renders gutter frame; dock panels drop rules and move hints to header"
```

---

### Task 7: De-box the welcome banner

**Files:**
- Modify: `internal/app/tui/transcript.go` (`renderWelcomeBanner`, ~line 607)
- Test: `internal/app/tui/transcript_test.go` (~lines 244, 395)

**Interfaces:**
- Consumes: nothing new. After this task `transcript.go` may no longer import `chrome` — remove the import if unused.

- [ ] **Step 1: Update the banner tests**

`TestWelcomeBannerIsCenteredHero` (transcript_test.go ~line 395) asserts a bordered card. Replace both banner tests with:

```go
func TestWelcomeBannerIsPlainLines(t *testing.T) {
	out := renderWelcomeBanner(60)
	plain := stripANSI(out)
	if !strings.Contains(plain, "marshal") || !strings.Contains(plain, "●") {
		t.Fatalf("welcome banner missing brand:\n%s", plain)
	}
	for _, glyph := range []string{"╭", "╰", "│"} {
		if strings.Contains(plain, glyph) {
			t.Fatalf("welcome banner should be borderless (%q):\n%s", glyph, plain)
		}
	}
	if !strings.Contains(plain, "/") {
		t.Fatalf("welcome banner missing call-to-action:\n%s", plain)
	}
}
```

(Also delete `TestWelcomeBannerHasCoralDotAndName` at ~line 244 — the new test covers both.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/app/tui/ -run TestWelcomeBanner -v`
Expected: FAIL — box glyphs present.

- [ ] **Step 3: Implement**

Replace `renderWelcomeBanner` in `internal/app/tui/transcript.go`:

```go
// renderWelcomeBanner prints the one-time startup identity as plain
// transcript lines — brand chrome pays rent once, at startup, not as a
// persistent title bar.
func renderWelcomeBanner(width int) string {
	_ = width
	dot := lipgloss.NewStyle().Foreground(coralColor).Render("●")
	brand := lipgloss.NewStyle().Foreground(coralColor).Bold(true).Render("marshal")
	tagline := mutedStyle().Render("local-first coding agent")
	cta := mutedStyle().Render("Type a question, or " + lipgloss.NewStyle().Bold(true).Render("/") + " for commands.")
	return "  " + dot + " " + brand + dimSeparator + tagline + "\n\n  " + cta + "\n\n"
}
```

Remove the `chrome` import from transcript.go if this was its last use (check: `grep -n chrome internal/app/tui/transcript.go`).

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/app/tui/... 2>&1 | tail -5`
Expected: all PASS.

```bash
git add internal/app/tui/transcript.go internal/app/tui/transcript_test.go
git commit -m "feat(tui): welcome banner is plain lines, not a boxed card"
```

---

### Task 8: Full-tree verification sweep

**Files:**
- Modify: none expected; fix-ups only.

- [ ] **Step 1: Format and vet**

Run: `gofmt -l internal/ && go vet ./internal/app/tui/...`
Expected: gofmt lists nothing; vet reports nothing (the two session.go lock-copy failures live outside this path and stay).

- [ ] **Step 2: Build and full test suite**

Run: `go build ./cmd/marshal && go test ./... 2>&1 | tail -15`
Expected: build OK; all packages PASS (the `probe` package exercises full frames — if it fails, its assertions reference deleted chrome; update them to the new frame shape, same substitutions as Tasks 3-5).

- [ ] **Step 3: Eyeball the real TUI**

Run `go run ./cmd/marshal` in a real terminal (requires a TTY — ask the user to do this if running headless). Verify: no title bar; `▍❯` prompt; single status/hint line at the bottom; `/settings` opens a gutter-framed dock; Alt+M picker likewise.

- [ ] **Step 4: Commit any straggler fixes**

```bash
git add -A ':!/.openrouter-api'
git commit -m "test(tui): phase-1 gutter redesign fixups" || echo "nothing to commit"
```
