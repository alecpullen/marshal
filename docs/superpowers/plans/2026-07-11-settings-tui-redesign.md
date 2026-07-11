# Settings TUI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework `internal/app/tui/settings/` into a bordered two-pane settings TUI with a single unified field widget, two-level focus, global `/` search, context-sensitive footer, and a `?` help overlay — per `docs/superpowers/specs/2026-07-11-settings-tui-redesign-design.md`.

**Architecture:** A new `fieldList` widget (typed rows: toggle/scalar/enum/drill) replaces `huh` forms and all hand-rolled editors. Sections become declarative `frame` builders. `Model` owns two focus levels (sidebar ↔ pane), a drill-down frame stack per section with breadcrumbs, and overlay routing for search/help. New machinery is built alongside the old panes (both compile), sections are converted, then the Model is switched over and legacy files deleted.

**Tech Stack:** Go, Bubble Tea v2 (`charm.land/bubbletea/v2`), `charm.land/bubbles/v2/textinput`, `charm.land/lipgloss/v2`, existing `internal/app/tui/theme` semantic slots. Build needs `CGO_ENABLED=1` (tree-sitter dep elsewhere in the module).

## Global Constraints

- Colors ONLY via `internal/app/tui/theme` slots (`settingsTheme` package var already exists). Never hardcode hex/ANSI values in widget code.
- Preserve the public surface consumed by `internal/app/tui/model.go` and tests: `New(cfg config.Config, workingDir, projectCfgPath string) Model`, `SetSize(width, height int)`, `Update(tea.Msg) (Model, tea.Cmd)`, `View() string`, `Init() tea.Cmd`, `SavedMsg{Cfg config.Config}`, `CancelledMsg{}`, `Footer() string`, `FocusedFieldTitle() string`, `BoolValue(title string) bool`.
- Keep `state`/`cloneConfig` (`state.go`), `warningsFor` (`validation.go`), `messages.go`, and `maskKey` unchanged.
- Imports use the `charm.land/...` v2 paths (this repo is post-Bubble Tea-v2 migration). Never import `charm.land/huh/v2` in new files.
- Key ownership: never bind `Ctrl+C`/`Ctrl+Z`. `Esc` always means "up one level".
- Tests: `go test ./internal/app/tui/settings/...` must pass at the end of every task. Format with `gofmt -w .` before each commit.
- Behavior deviation from spec §3 (agreed during planning): field edits inside drill-down frames apply to the working config immediately (the whole settings screen is already a transaction guarded by Ctrl+S / double-Esc). There is no per-sub-pane commit/discard.

---

### Task 1: `field` type + `fieldList` navigation and toggle rows

**Files:**
- Create: `internal/app/tui/settings/field.go`
- Create: `internal/app/tui/settings/fieldlist.go`
- Test: `internal/app/tui/settings/fieldlist_test.go`

**Interfaces:**
- Consumes: `settingsTheme` (package var in `model.go`), `warnStyle` (package var in `model.go`).
- Produces:
  - `type field struct` with fields `id, title, desc string`, `keywords []string`, `kind fieldKind`, `getBool func() bool`, `setBool func(bool)`, `getStr func() string`, `setStr func(string) error`, `masked bool`, `options func() []string`, `summary func() string`, `build func() *frame`, `del func()`.
  - `type fieldKind int` with `kindToggle`, `kindScalar`, `kindEnum`, `kindDrill`.
  - `newFieldList(fields func() []*field) *fieldList` and methods `SetSize(w, h int)`, `Refresh()`, `Rows() []*field`, `Cursor() int`, `SetCursor(int)`, `CursorRow() *field`, `Editing() bool` (true when inline edit/picker/add prompt open), `CancelEdit()`, `Update(tea.Msg) tea.Cmd`, `View() string`, `TakePushRequest() *frame`.
  - `frame` is only forward-declared here as `type frame struct { title string; list *fieldList; keyPrompt string; onAdd func(string) error }` (fleshed out in Task 4 — declare it in `field.go` now so `build func() *frame` compiles).

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func key(s string) tea.KeyPressMsg {
	// Single-rune helper for tests; special keys are constructed inline.
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func testToggleField(title string, val *bool) *field {
	return &field{
		id: "test." + title, title: title, kind: kindToggle,
		getBool: func() bool { return *val },
		setBool: func(b bool) { *val = b },
	}
}

func TestFieldListNavigationAndToggle(t *testing.T) {
	a, b := false, true
	fl := newFieldList(func() []*field {
		return []*field{testToggleField("Alpha", &a), testToggleField("Beta", &b)}
	})
	fl.SetSize(60, 20)

	if fl.CursorRow().title != "Alpha" {
		t.Fatalf("cursor should start on first row, got %q", fl.CursorRow().title)
	}
	fl.Update(key("j"))
	if fl.CursorRow().title != "Beta" {
		t.Fatalf("j should move to Beta, got %q", fl.CursorRow().title)
	}
	fl.Update(key("k"))
	fl.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !a {
		t.Fatal("space should toggle Alpha to true")
	}
	view := fl.View()
	if !strings.Contains(view, "Alpha") || !strings.Contains(view, "on ●") {
		t.Fatalf("view should show toggled value, got:\n%s", view)
	}
	if !strings.Contains(view, "off ○") {
		t.Fatalf("view should show Beta off, got:\n%s", view)
	}
	if !strings.Contains(view, "▸") {
		t.Fatalf("view should mark the cursor row, got:\n%s", view)
	}
}

func TestFieldListGandG(t *testing.T) {
	a, b, c := false, false, false
	fl := newFieldList(func() []*field {
		return []*field{testToggleField("A", &a), testToggleField("B", &b), testToggleField("C", &c)}
	})
	fl.SetSize(60, 20)
	fl.Update(key("G"))
	if fl.CursorRow().title != "C" {
		t.Fatalf("G should jump to last row, got %q", fl.CursorRow().title)
	}
	fl.Update(key("g"))
	if fl.CursorRow().title != "A" {
		t.Fatalf("g should jump to first row, got %q", fl.CursorRow().title)
	}
}

func TestFieldListDescriptionShownForCursorRow(t *testing.T) {
	a := false
	f := testToggleField("Alpha", &a)
	f.desc = "controls the alpha behavior"
	b := false
	g := testToggleField("Beta", &b)
	g.desc = "controls the beta behavior"
	fl := newFieldList(func() []*field { return []*field{f, g} })
	fl.SetSize(60, 20)
	view := fl.View()
	if !strings.Contains(view, "controls the alpha behavior") {
		t.Fatalf("cursor row description should render, got:\n%s", view)
	}
	if strings.Contains(view, "controls the beta behavior") {
		t.Fatalf("non-cursor description should NOT render, got:\n%s", view)
	}
}

func TestFieldListScrollsToKeepCursorVisible(t *testing.T) {
	vals := make([]bool, 30)
	fl := newFieldList(func() []*field {
		out := make([]*field, 30)
		for i := range out {
			out[i] = testToggleField(strings.Repeat("x", 3)+string(rune('a'+i%26)), &vals[i])
		}
		return out
	})
	fl.SetSize(60, 8)
	fl.Update(key("G"))
	view := fl.View()
	if len(strings.Split(view, "\n")) > 8 {
		t.Fatalf("view must not exceed height 8, got %d lines", len(strings.Split(view, "\n")))
	}
	if !strings.Contains(view, "▸") {
		t.Fatalf("cursor row must remain visible after G, got:\n%s", view)
	}
	if !strings.Contains(view, "↑") {
		t.Fatalf("expected ↑ more indicator when scrolled down, got:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestFieldList' -v`
Expected: FAIL — `undefined: newFieldList`, `undefined: field`

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/field.go`:

```go
package settings

// fieldKind discriminates the row types a fieldList can render and edit.
type fieldKind int

const (
	kindToggle fieldKind = iota // bool: Space flips
	kindScalar                  // string/number/duration: Enter opens inline edit
	kindEnum                    // one of options(): ←/→ cycle, Enter opens picker
	kindDrill                   // Enter pushes build() as a new frame
)

// field is one row in a fieldList. Exactly one kind-group of closures is
// set. Setters write straight into the working config (the settings screen
// is a single transaction guarded by Ctrl+S / double-Esc at the top).
type field struct {
	id       string   // stable id for the search registry ("shell.allow_network")
	title    string
	desc     string   // one-liner rendered under the row while the cursor is on it
	keywords []string // extra search terms beyond the title

	kind fieldKind

	// kindToggle
	getBool func() bool
	setBool func(bool)

	// kindScalar and kindEnum current-value access. setStr validates and
	// applies; a non-nil error renders under the row and blocks apply.
	// setStr == nil marks the row read-only (Enter does nothing).
	getStr func() string
	setStr func(string) error
	masked bool // render via maskKey; edits replace, empty input keeps

	// kindEnum
	options func() []string

	// kindDrill
	summary func() string // right-cell summary, e.g. "3 items"
	build   func() *frame

	// optional per-row delete: entry rows, list items, map keys, and
	// masked-secret clear all hang their removal behavior here (key: d).
	del func()
}

// frame is one level of a pane's drill-down stack: a titled fieldList plus
// optional add behavior for collection frames. (Stack management lives in
// pane.go — Task 4.)
type frame struct {
	title     string // breadcrumb segment, e.g. "github" in "MCP › github"
	list      *fieldList
	keyPrompt string             // add prompt label; "" with onAdd set = add without prompt
	onAdd     func(string) error // nil = 'a' disabled in this frame
}
```

`internal/app/tui/settings/fieldlist.go`:

```go
package settings

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	flCursorStyle = lipgloss.NewStyle().Bold(true).Background(settingsTheme.BGSelection)
	flTitleStyle  = lipgloss.NewStyle().Foreground(settingsTheme.FGDefault)
	flValueStyle  = lipgloss.NewStyle().Foreground(settingsTheme.AccentSecondary)
	flDescStyle   = lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
	flErrStyle    = lipgloss.NewStyle().Foreground(settingsTheme.StatusError)
	flOnStyle     = lipgloss.NewStyle().Foreground(settingsTheme.StatusSuccess)
	flOffStyle    = lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
	flMoreStyle   = lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
)

// fieldList renders and edits a vertical list of typed rows. It is the one
// widget behind every settings pane and drill-down frame.
type fieldList struct {
	fields func() []*field
	rows   []*field
	cursor int
	width  int
	height int

	// inline scalar edit
	editing bool
	input   textinput.Model
	errMsg  string

	// enum picker (inline dropdown under the row)
	picking bool
	pickIdx int

	// collection add prompt (wired by pane frames in Task 4)
	adding    bool
	keyPrompt string
	onAdd     func(string) error
	keyInput  textinput.Model

	// drill request picked up by the owning pane after Update
	pushRequest *frame
}

func newFieldList(fields func() []*field) *fieldList {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ki := textinput.New()
	ki.SetVirtualCursor(true)
	fl := &fieldList{fields: fields, input: ti, keyInput: ki}
	fl.Refresh()
	return fl
}

func (fl *fieldList) Refresh() {
	fl.rows = fl.fields()
	if fl.cursor >= len(fl.rows) {
		fl.cursor = len(fl.rows) - 1
	}
	if fl.cursor < 0 {
		fl.cursor = 0
	}
}

func (fl *fieldList) Rows() []*field { fl.Refresh(); return fl.rows }
func (fl *fieldList) Cursor() int    { return fl.cursor }

func (fl *fieldList) SetCursor(i int) {
	fl.Refresh()
	if i >= 0 && i < len(fl.rows) {
		fl.cursor = i
	}
}

func (fl *fieldList) CursorRow() *field {
	fl.Refresh()
	if len(fl.rows) == 0 {
		return nil
	}
	return fl.rows[fl.cursor]
}

func (fl *fieldList) SetSize(w, h int) { fl.width, fl.height = w, h }

func (fl *fieldList) Editing() bool { return fl.editing || fl.picking || fl.adding }

func (fl *fieldList) CancelEdit() {
	fl.editing = false
	fl.picking = false
	fl.adding = false
	fl.errMsg = ""
	fl.input.Blur()
	fl.keyInput.Blur()
}

// TakePushRequest returns and clears the frame a drill row asked to open.
func (fl *fieldList) TakePushRequest() *frame {
	f := fl.pushRequest
	fl.pushRequest = nil
	return f
}

func (fl *fieldList) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	fl.Refresh()
	if fl.adding {
		return fl.updateAdd(k)
	}
	if fl.editing {
		return fl.updateEdit(k)
	}
	if fl.picking {
		fl.updatePick(k)
		return nil
	}
	row := fl.CursorRow()
	switch k.String() {
	case "up", "k":
		if fl.cursor > 0 {
			fl.cursor--
		}
	case "down", "j":
		if fl.cursor < len(fl.rows)-1 {
			fl.cursor++
		}
	case "g":
		fl.cursor = 0
	case "G":
		fl.cursor = len(fl.rows) - 1
	case "space":
		if row != nil && row.kind == kindToggle {
			row.setBool(!row.getBool())
		}
	case "left", "right":
		if row != nil && row.kind == kindEnum {
			fl.cycleEnum(row, k.String() == "right")
		}
	case "enter", "e":
		fl.openRow(row)
	case "a":
		if fl.onAdd != nil {
			if fl.keyPrompt == "" {
				if err := fl.onAdd(""); err != nil {
					fl.errMsg = err.Error()
					return nil
				}
				fl.Refresh()
				fl.cursor = len(fl.rows) - 1
				return nil
			}
			fl.adding = true
			fl.errMsg = ""
			fl.keyInput.SetValue("")
			fl.keyInput.Focus()
		}
	case "d":
		if row != nil && row.del != nil {
			row.del()
			fl.Refresh()
		}
	}
	return nil
}

func (fl *fieldList) openRow(row *field) {
	if row == nil {
		return
	}
	switch row.kind {
	case kindToggle:
		row.setBool(!row.getBool())
	case kindScalar:
		if row.setStr == nil {
			return // read-only
		}
		fl.editing = true
		fl.errMsg = ""
		if row.masked {
			fl.input.SetValue("")
		} else {
			fl.input.SetValue(row.getStr())
			fl.input.CursorEnd()
		}
		fl.input.Focus()
	case kindEnum:
		fl.picking = true
		fl.pickIdx = indexOf(row.options(), row.getStr())
	case kindDrill:
		fl.pushRequest = row.build()
	}
}

func (fl *fieldList) cycleEnum(row *field, forward bool) {
	opts := row.options()
	if len(opts) == 0 {
		return
	}
	i := indexOf(opts, row.getStr())
	if forward {
		i = (i + 1) % len(opts)
	} else {
		i = (i - 1 + len(opts)) % len(opts)
	}
	if err := row.setStr(opts[i]); err != nil {
		fl.errMsg = err.Error()
	}
}

func (fl *fieldList) updateEdit(k tea.KeyPressMsg) tea.Cmd {
	row := fl.CursorRow()
	switch k.String() {
	case "enter":
		val := strings.TrimSpace(fl.input.Value())
		if row.masked && val == "" {
			fl.CancelEdit() // empty keeps the stored secret
			return nil
		}
		if err := row.setStr(val); err != nil {
			fl.errMsg = err.Error()
			return nil
		}
		fl.CancelEdit()
		return nil
	case "esc":
		fl.CancelEdit()
		return nil
	}
	var cmd tea.Cmd
	fl.input, cmd = fl.input.Update(k)
	return cmd
}

func (fl *fieldList) updatePick(k tea.KeyPressMsg) {
	row := fl.CursorRow()
	opts := row.options()
	switch k.String() {
	case "up", "k":
		if fl.pickIdx > 0 {
			fl.pickIdx--
		}
	case "down", "j":
		if fl.pickIdx < len(opts)-1 {
			fl.pickIdx++
		}
	case "enter":
		if fl.pickIdx >= 0 && fl.pickIdx < len(opts) {
			if err := row.setStr(opts[fl.pickIdx]); err != nil {
				fl.errMsg = err.Error()
				return
			}
		}
		fl.picking = false
	case "esc":
		fl.picking = false
	}
}

func (fl *fieldList) updateAdd(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		if err := fl.onAdd(strings.TrimSpace(fl.keyInput.Value())); err != nil {
			fl.errMsg = err.Error()
			return nil
		}
		fl.CancelEdit()
		fl.Refresh()
		fl.cursor = len(fl.rows) - 1
		return nil
	case "esc":
		fl.CancelEdit()
		return nil
	}
	var cmd tea.Cmd
	fl.keyInput, cmd = fl.keyInput.Update(k)
	return cmd
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return 0
}

// valueCell renders the right-hand value for a row.
func (fl *fieldList) valueCell(row *field, isCursor bool) string {
	switch row.kind {
	case kindToggle:
		if row.getBool() {
			return flOnStyle.Render("on ●")
		}
		return flOffStyle.Render("off ○")
	case kindScalar:
		if fl.editing && isCursor {
			return fl.input.View()
		}
		v := row.getStr()
		if row.masked {
			v = maskKey(v)
		}
		if v == "" {
			v = "—"
		}
		return flValueStyle.Render(v)
	case kindEnum:
		return flValueStyle.Render(row.getStr() + " ▾")
	case kindDrill:
		return flValueStyle.Render(row.summary() + " ›")
	}
	return ""
}

// View renders the list clipped to height, keeping the cursor row visible
// and adding ↑/↓ more indicators when clipped.
func (fl *fieldList) View() string {
	fl.Refresh()
	var lines []string
	cursorLine := 0
	if len(fl.rows) == 0 && !fl.adding {
		empty := "  (empty"
		if fl.onAdd != nil {
			empty += " — press a to add"
		}
		lines = append(lines, flDescStyle.Render(empty+")"))
	}
	for i, row := range fl.rows {
		isCursor := i == fl.cursor
		marker := "  "
		if isCursor {
			marker = "▸ "
		}
		val := fl.valueCell(row, isCursor)
		title := row.title
		gap := fl.width - lipgloss.Width(marker) - lipgloss.Width(title) - lipgloss.Width(val)
		if gap < 1 {
			gap = 1
		}
		line := marker + flTitleStyle.Render(title) + strings.Repeat(" ", gap) + val
		if isCursor {
			cursorLine = len(lines)
			line = flCursorStyle.Render(marker+title) + strings.Repeat(" ", gap) + val
		}
		lines = append(lines, line)
		if isCursor && fl.errMsg != "" {
			lines = append(lines, "    "+flErrStyle.Render("⚠ "+fl.errMsg))
		}
		if isCursor && row.desc != "" && !fl.Editing() {
			lines = append(lines, "    "+flDescStyle.Render(row.desc))
		}
		if isCursor && fl.picking {
			for j, opt := range row.options() {
				pm := "    "
				if j == fl.pickIdx {
					pm = "  ▸ "
				}
				lines = append(lines, pm+flValueStyle.Render(opt))
			}
		}
	}
	if fl.adding {
		lines = append(lines, "▸ "+fl.keyPrompt+": "+fl.keyInput.View())
		if fl.errMsg != "" {
			lines = append(lines, "    "+flErrStyle.Render("⚠ "+fl.errMsg))
		}
	}
	return clipLines(lines, cursorLine, fl.height)
}

// clipLines windows lines to at most height rows, keeping focusLine visible,
// with ↑/↓ more indicators occupying the first/last row when clipped.
func clipLines(lines []string, focusLine, height int) string {
	if height <= 0 || len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	inner := height - 2 // reserve rows for the two indicators
	if inner < 1 {
		inner = 1
	}
	start := focusLine - inner/2
	if start < 0 {
		start = 0
	}
	if start+inner > len(lines) {
		start = len(lines) - inner
	}
	out := make([]string, 0, height)
	if start > 0 {
		out = append(out, flMoreStyle.Render("  ↑ more"))
	} else {
		out = append(out, "")
	}
	out = append(out, lines[start:start+inner]...)
	if start+inner < len(lines) {
		out = append(out, flMoreStyle.Render("  ↓ more"))
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestFieldList' -v`
Expected: PASS (all four tests). If `tea.KeyPressMsg{Code: tea.KeySpace}` doesn't stringify to `"space"`, check with `k.String()` in a debug print and match the actual key-string constants used elsewhere in this package's tests (see `model_test.go` for the established way to construct key messages — reuse that helper style).

- [ ] **Step 5: Verify whole package still compiles (old code untouched)**

Run: `go test ./internal/app/tui/settings/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/field.go internal/app/tui/settings/fieldlist.go internal/app/tui/settings/fieldlist_test.go
git commit -m "feat(settings): fieldList widget core — navigation and toggle rows"
```

---

### Task 2: scalar rows — inline edit, validation, read-only, masked + setter helpers

**Files:**
- Create: `internal/app/tui/settings/setters.go`
- Modify: `internal/app/tui/settings/fieldlist_test.go` (append tests)
- Test: `internal/app/tui/settings/setters_test.go`

**Interfaces:**
- Consumes: `field`, `fieldList` from Task 1; `maskKey` from `masked.go`.
- Produces:
  - `intSetter(min int, apply func(int)) func(string) error`
  - `floatSetter(apply func(float64)) func(string) error`
  - `durationSetter(apply func(time.Duration)) func(string) error`
  - `scalarField(id, title string, get func() string, set func(string) error) *field`
  - `intField2(id, title string, get func() int, min int, apply func(int)) *field` (named to avoid clashing with legacy `numField` until Task 10 deletes it; renamed to `intField` in Task 10)
  - `secretRow(id, title string, get func() string, set func(string)) *field` (masked scalar with `del` = clear)

- [ ] **Step 1: Write the failing tests**

Append to `fieldlist_test.go`:

```go
func TestScalarInlineEditAppliesAndValidates(t *testing.T) {
	n := 5
	fl := newFieldList(func() []*field {
		return []*field{intField2("t.n", "Count", func() int { return n }, 1, func(v int) { n = v })}
	})
	fl.SetSize(60, 20)

	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open edit
	if !fl.Editing() {
		t.Fatal("enter should open inline edit")
	}
	for _, r := range "12" {
		fl.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n != 12 {
		t.Fatalf("edit should apply 12, got %d", n)
	}
	if fl.Editing() {
		t.Fatal("apply should close the edit")
	}

	// invalid input blocks apply and shows the error
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	fl.input.SetValue("abc")
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n != 12 {
		t.Fatalf("invalid input must not apply, got %d", n)
	}
	if !strings.Contains(fl.View(), "must be a number") {
		t.Fatalf("error should render, got:\n%s", fl.View())
	}
	// esc cancels without applying
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if fl.Editing() {
		t.Fatal("esc should cancel the edit")
	}
}

func TestScalarReadOnlyRowIgnoresEnter(t *testing.T) {
	fl := newFieldList(func() []*field {
		return []*field{{id: "t.ro", title: "Preset", kind: kindScalar, getStr: func() string { return "qwen" }}}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if fl.Editing() {
		t.Fatal("read-only row must not open an edit")
	}
}

func TestMaskedRowKeepsOnEmptyAndClearsOnD(t *testing.T) {
	secret := "sk-abcd1234"
	fl := newFieldList(func() []*field {
		return []*field{secretRow("t.key", "API key", func() string { return secret }, func(v string) { secret = v })}
	})
	fl.SetSize(60, 20)
	if !strings.Contains(fl.View(), "••••1234") {
		t.Fatalf("masked value should render last four, got:\n%s", fl.View())
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // apply empty = keep
	if secret != "sk-abcd1234" {
		t.Fatalf("empty apply must keep the secret, got %q", secret)
	}
	fl.Update(key("d"))
	if secret != "" {
		t.Fatalf("d must clear the secret, got %q", secret)
	}
}
```

`setters_test.go`:

```go
package settings

import (
	"testing"
	"time"
)

func TestIntSetterClampsToMin(t *testing.T) {
	got := 0
	set := intSetter(1, func(v int) { got = v })
	if err := set("0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected clamp to 1, got %d", got)
	}
	if err := set("x"); err == nil {
		t.Fatal("expected error for non-number")
	}
}

func TestDurationSetter(t *testing.T) {
	var got time.Duration
	set := durationSetter(func(d time.Duration) { got = d })
	if err := set("8h"); err != nil || got != 8*time.Hour {
		t.Fatalf("expected 8h, got %v err %v", got, err)
	}
	if err := set("nope"); err == nil {
		t.Fatal("expected error for bad duration")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestScalar|TestMasked|TestIntSetter|TestDurationSetter' -v`
Expected: FAIL — `undefined: intField2`, `undefined: secretRow`, `undefined: intSetter`

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/setters.go`:

```go
package settings

import (
	"fmt"
	"strconv"
	"time"
)

// intSetter parses an int, clamps to min (when min != 0), and applies it.
func intSetter(min int, apply func(int)) func(string) error {
	return func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		if min != 0 && v < min {
			v = min
		}
		apply(v)
		return nil
	}
}

func floatSetter(apply func(float64)) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		apply(v)
		return nil
	}
}

func durationSetter(apply func(time.Duration)) func(string) error {
	return func(s string) error {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("must be a duration like 30s or 8h")
		}
		apply(d)
		return nil
	}
}

func scalarField(id, title string, get func() string, set func(string) error) *field {
	return &field{id: id, title: title, kind: kindScalar, getStr: get, setStr: set}
}

// intField2 binds a scalar row to an int config value. Renamed to intField
// when the legacy numField is deleted (Task 10).
func intField2(id, title string, get func() int, min int, apply func(int)) *field {
	return &field{
		id: id, title: title, kind: kindScalar,
		getStr: func() string { return strconv.Itoa(get()) },
		setStr: intSetter(min, apply),
	}
}

// secretRow is a masked scalar: displays via maskKey, Enter replaces (empty
// keeps), d clears the stored value.
func secretRow(id, title string, get func() string, set func(string)) *field {
	return &field{
		id: id, title: title, kind: kindScalar, masked: true,
		desc:     "enter replaces · empty keeps · d clears · prefer the env-var field",
		keywords: []string{"secret", "api key", "token"},
		getStr:   get,
		setStr:   func(v string) error { set(v); return nil },
		del:      func() { set("") },
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestScalar|TestMasked|TestIntSetter|TestDurationSetter' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/setters.go internal/app/tui/settings/setters_test.go internal/app/tui/settings/fieldlist_test.go
git commit -m "feat(settings): scalar rows — inline edit, validation, masked secrets, setter helpers"
```

---

### Task 3: enum rows — ←/→ cycle and inline picker

**Files:**
- Modify: `internal/app/tui/settings/fieldlist_test.go` (append tests)
- Modify: `internal/app/tui/settings/setters.go` (add `enumField` helper)

**Interfaces:**
- Consumes: Task 1 `fieldList` (cycle/pick logic already implemented there).
- Produces: `enumField(id, title string, opts []string, get func() string, set func(string)) *field`

- [ ] **Step 1: Write the failing tests**

Append to `fieldlist_test.go`:

```go
func TestEnumCycleWithArrows(t *testing.T) {
	v := "deny"
	fl := newFieldList(func() []*field {
		return []*field{enumField("t.e", "Guardrail", []string{"deny", "confirm", "allow"},
			func() string { return v }, func(s string) { v = s })}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if v != "confirm" {
		t.Fatalf("right should cycle deny→confirm, got %q", v)
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if v != "deny" {
		t.Fatalf("left should cycle back to deny, got %q", v)
	}
}

func TestEnumPickerSelectsOption(t *testing.T) {
	v := "deny"
	fl := newFieldList(func() []*field {
		return []*field{enumField("t.e", "Guardrail", []string{"deny", "confirm", "allow"},
			func() string { return v }, func(s string) { v = s })}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open picker
	if !fl.Editing() {
		t.Fatal("enter should open the picker")
	}
	view := fl.View()
	if !strings.Contains(view, "confirm") || !strings.Contains(view, "allow") {
		t.Fatalf("picker should list all options, got:\n%s", view)
	}
	fl.Update(key("j"))
	fl.Update(key("j"))
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if v != "allow" {
		t.Fatalf("picker should apply allow, got %q", v)
	}
	if fl.Editing() {
		t.Fatal("picker should close after apply")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestEnum' -v`
Expected: FAIL — `undefined: enumField`

- [ ] **Step 3: Add the helper to `setters.go`**

```go
func enumField(id, title string, opts []string, get func() string, set func(string)) *field {
	return &field{
		id: id, title: title, kind: kindEnum,
		options: func() []string { return opts },
		getStr:  get,
		setStr:  func(v string) error { set(v); return nil },
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestEnum' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/setters.go internal/app/tui/settings/fieldlist_test.go
git commit -m "feat(settings): enum rows — arrow cycling and inline picker"
```

---

### Task 4: drill-down frames — pane stack, list/map/entries builders

**Files:**
- Create: `internal/app/tui/settings/panestack.go`
- Test: `internal/app/tui/settings/panestack_test.go`

**Interfaces:**
- Consumes: `field`, `frame`, `fieldList` from Tasks 1–3.
- Produces:
  - `type paneStack struct { stack []*frame }` with `newPaneStack(root *frame) *paneStack`, `top() *frame`, `push(*frame)`, `pop() bool` (false at root), `depth() int`, `breadcrumb(sectionTitle string) string`, `Update(tea.Msg) tea.Cmd` (routes to top frame, handles push requests), `atRoot() bool`.
  - `newFrame(title string, fields func() []*field) *frame`
  - `newCollectionFrame(title, keyPrompt string, fields func() []*field, onAdd func(string) error) *frame`
  - `listDrill(id, title string, items *[]string) *field` — drill row over a `[]string`
  - `mapStringDrill(id, title string, values *map[string]string) *field`
  - `mapIntDrill(id, title string, values *map[string]int) *field`
  - `entriesDrill(id, title, keyPrompt string, keys func() []string, rowTitle func(key string) string, add func(key string) error, buildEntry func(key string) *frame, del func(key string)) *field`

- [ ] **Step 1: Write the failing tests**

```go
package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestListDrillEditsSlice(t *testing.T) {
	items := []string{"rm -rf", "git push --force"}
	root := newFrame("Shell", func() []*field {
		return []*field{listDrill("shell.deny", "Deny patterns", &items)}
	})
	ps := newPaneStack(root)
	ps.top().list.SetSize(60, 20)

	// summary row shows the count
	if !strings.Contains(ps.top().list.View(), "2 items") {
		t.Fatalf("expected item count summary, got:\n%s", ps.top().list.View())
	}

	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill in
	if ps.depth() != 2 {
		t.Fatalf("enter should push the list frame, depth=%d", ps.depth())
	}
	if got := ps.breadcrumb("Shell"); got != "Shell › Deny patterns" {
		t.Fatalf("breadcrumb wrong: %q", got)
	}

	// add an item: a → typed value → enter
	ps.Update(key("a"))
	for _, r := range "curl" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(items) != 3 || items[2] != "curl" {
		t.Fatalf("add should append, got %v", items)
	}

	// delete the first item
	ps.top().list.SetCursor(0)
	ps.Update(key("d"))
	if len(items) != 2 || items[0] != "git push --force" {
		t.Fatalf("d should delete row 0, got %v", items)
	}

	// pop back to root
	if !ps.pop() {
		t.Fatal("pop should succeed above root")
	}
	if ps.pop() {
		t.Fatal("pop at root must return false")
	}
}

func TestMapIntDrillEditsValues(t *testing.T) {
	m := map[string]int{"reviewer": 4}
	root := newFrame("Swarm", func() []*field {
		return []*field{mapIntDrill("swarm.tool_iters", "Tool iters", &m)}
	})
	ps := newPaneStack(root)
	ps.top().list.SetSize(60, 20)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill

	// add key then edit its value
	ps.Update(key("a"))
	for _, r := range "planner" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m["planner"]; !ok {
		t.Fatalf("add should create the key, got %v", m)
	}
	// cursor lands on the new row; enter opens the value edit
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "7" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m["planner"] != 7 {
		t.Fatalf("value edit should apply 7, got %v", m)
	}
}

func TestEntriesDrillBuildsSubFrame(t *testing.T) {
	vals := map[string]string{"local": "ollama"}
	root := newFrame("Providers", func() []*field {
		return []*field{entriesDrill("providers", "Providers", "New provider name",
			func() []string { return sortedKeys(vals) },
			func(k string) string { return k },
			func(k string) error { vals[k] = ""; return nil },
			func(k string) *frame {
				return newFrame(k, func() []*field {
					v := k
					return []*field{scalarField("providers."+k+".type", "Type",
						func() string { return vals[v] },
						func(s string) error { vals[v] = s; return nil })}
				})
			},
			func(k string) { delete(vals, k) })}
	})
	ps := newPaneStack(root)
	ps.top().list.SetSize(60, 20)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill into collection
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill into "local"
	if ps.depth() != 3 {
		t.Fatalf("expected depth 3, got %d", ps.depth())
	}
	if got := ps.breadcrumb("Providers"); got != "Providers › Providers › local" {
		t.Fatalf("breadcrumb wrong: %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/settings/ -run 'TestListDrill|TestMapIntDrill|TestEntriesDrill' -v`
Expected: FAIL — `undefined: newPaneStack`, `undefined: listDrill`, …

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/panestack.go`:

```go
package settings

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func newFrame(title string, fields func() []*field) *frame {
	return &frame{title: title, list: newFieldList(fields)}
}

func newCollectionFrame(title, keyPrompt string, fields func() []*field, onAdd func(string) error) *frame {
	f := newFrame(title, fields)
	f.keyPrompt = keyPrompt
	f.onAdd = onAdd
	f.list.keyPrompt = keyPrompt
	f.list.onAdd = onAdd
	return f
}

// paneStack is one section's drill-down stack. stack[0] is the section root.
type paneStack struct {
	stack  []*frame
	width  int
	height int
}

func newPaneStack(root *frame) *paneStack { return &paneStack{stack: []*frame{root}} }

func (p *paneStack) top() *frame  { return p.stack[len(p.stack)-1] }
func (p *paneStack) depth() int   { return len(p.stack) }
func (p *paneStack) atRoot() bool { return len(p.stack) == 1 }

func (p *paneStack) push(f *frame) {
	f.list.SetSize(p.width, p.height)
	p.stack = append(p.stack, f)
}

func (p *paneStack) pop() bool {
	if len(p.stack) == 1 {
		return false
	}
	p.stack = p.stack[:len(p.stack)-1]
	p.top().list.Refresh()
	return true
}

func (p *paneStack) SetSize(w, h int) {
	p.width, p.height = w, h
	for _, f := range p.stack {
		f.list.SetSize(w, h)
	}
}

// breadcrumb joins the section title with every pushed frame title:
// "MCP › github › Env".
func (p *paneStack) breadcrumb(sectionTitle string) string {
	parts := []string{sectionTitle}
	for _, f := range p.stack[1:] {
		parts = append(parts, f.title)
	}
	return strings.Join(parts, " › ")
}

func (p *paneStack) Update(msg tea.Msg) tea.Cmd {
	cmd := p.top().list.Update(msg)
	if f := p.top().list.TakePushRequest(); f != nil {
		p.push(f)
	}
	return cmd
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// listDrill is a drill row over a []string: each item is an editable scalar
// row, a appends (typed value is the item), d deletes.
func listDrill(id, title string, items *[]string) *field {
	buildFields := func() []*field {
		out := make([]*field, len(*items))
		for i := range *items {
			i := i
			out[i] = &field{
				id: fmt.Sprintf("%s.%d", id, i), title: (*items)[i], kind: kindScalar,
				getStr: func() string { return (*items)[i] },
				setStr: func(v string) error {
					if v == "" {
						return fmt.Errorf("cannot be empty")
					}
					(*items)[i] = v
					return nil
				},
				del: func() { *items = append((*items)[:i], (*items)[i+1:]...) },
			}
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d items", len(*items)) },
		build: func() *frame {
			return newCollectionFrame(title, "New entry", buildFields, func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("cannot be empty")
				}
				*items = append(*items, v)
				return nil
			})
		},
	}
}

// mapDrill is the generic key/value drill; parse/format adapt the value type.
func mapDrill[T any](id, title string, values *map[string]T, parse func(string) (T, error), format func(T) string) *field {
	buildFields := func() []*field {
		keys := sortedKeys(*values)
		out := make([]*field, len(keys))
		for i, k := range keys {
			k := k
			out[i] = &field{
				id: id + "." + k, title: k, kind: kindScalar,
				getStr: func() string { return format((*values)[k]) },
				setStr: func(v string) error {
					pv, err := parse(v)
					if err != nil {
						return err
					}
					(*values)[k] = pv
					return nil
				},
				del: func() { delete(*values, k) },
			}
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d entries", len(*values)) },
		build: func() *frame {
			return newCollectionFrame(title, "New key", buildFields, func(k string) error {
				if k == "" {
					return fmt.Errorf("key cannot be empty")
				}
				if _, exists := (*values)[k]; exists {
					return fmt.Errorf("key already exists")
				}
				if *values == nil {
					*values = map[string]T{}
				}
				var zero T
				(*values)[k] = zero
				return nil
			})
		},
	}
}

func mapStringDrill(id, title string, values *map[string]string) *field {
	return mapDrill(id, title, values, func(s string) (string, error) { return s, nil }, func(s string) string { return s })
}

func mapIntDrill(id, title string, values *map[string]int) *field {
	return mapDrill(id, title, values,
		func(s string) (int, error) {
			v, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return 0, fmt.Errorf("must be a number")
			}
			return v, nil
		},
		strconv.Itoa)
}

// entriesDrill is a drill row over a named collection (providers, presets,
// MCP servers, hooks, permission rules). Each entry row drills again into
// buildEntry(key).
func entriesDrill(id, title, keyPrompt string, keys func() []string, rowTitle func(string) string,
	add func(string) error, buildEntry func(string) *frame, del func(string)) *field {
	buildFields := func() []*field {
		ks := keys()
		out := make([]*field, len(ks))
		for i, k := range ks {
			k := k
			out[i] = &field{
				id: id + "." + k, title: rowTitle(k), kind: kindDrill,
				summary: func() string { return "" },
				build:   func() *frame { return buildEntry(k) },
				del:     func() { del(k) },
			}
		}
		return out
	}
	return &field{
		id: id, title: title, kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d entries", len(keys())) },
		build: func() *frame {
			return newCollectionFrame(title, keyPrompt, buildFields, add)
		},
	}
}
```

Add `"strconv"` to the import block.

Note on `listDrill` row closures: the `del`/`setStr` closures capture the index `i`; because `fields()` is re-invoked by `Refresh()` after every mutation, stale indices are never used after a delete. Do not "optimize" by caching rows.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestListDrill|TestMapIntDrill|TestEntriesDrill' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/panestack.go internal/app/tui/settings/panestack_test.go
git commit -m "feat(settings): drill-down frames — pane stack, list/map/entries builders"
```

---

### Task 5: panel chrome — bordered panels with embedded titles and focus colors

**Files:**
- Create: `internal/app/tui/settings/chrome.go`
- Test: `internal/app/tui/settings/chrome_test.go`

**Interfaces:**
- Consumes: `settingsTheme`.
- Produces: `renderPanel(title, content string, w, h int, focused bool) string` — a rounded-border box, title embedded in the top border (`╭─ Title ─────╮`), border colored `AccentPrimary` when focused / `BorderMuted` when not, content padded to the inner size.

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderPanelEmbedsTitleAndSizes(t *testing.T) {
	out := renderPanel("Shell", "hello", 30, 6, true)
	plain := ansi.Strip(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d:\n%s", len(lines), plain)
	}
	if !strings.Contains(lines[0], "╭") || !strings.Contains(lines[0], " Shell ") {
		t.Fatalf("top border should embed the title, got %q", lines[0])
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 30 {
			t.Fatalf("line %d should be width 30, got %d: %q", i, w, l)
		}
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("content missing:\n%s", plain)
	}
}

func TestRenderPanelFocusChangesBorderColor(t *testing.T) {
	focused := renderPanel("S", "x", 20, 4, true)
	blurred := renderPanel("S", "x", 20, 4, false)
	if focused == blurred {
		t.Fatal("focused and unfocused panels should differ (border color)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestRenderPanel' -v`
Expected: FAIL — `undefined: renderPanel`

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/chrome.go`:

```go
package settings

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderPanel draws a rounded-border box with the title embedded in the top
// border. The border uses accent.primary when focused, border.muted when
// not — the primary focus signal of the settings UI.
func renderPanel(title, content string, w, h int, focused bool) string {
	borderColor := settingsTheme.BorderMuted
	titleStyle := lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
	if focused {
		borderColor = settingsTheme.AccentPrimary
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(settingsTheme.AccentPrimary)
	}
	bs := lipgloss.NewStyle().Foreground(borderColor)
	inner := w - 2
	innerH := h - 2
	if inner < 1 {
		inner = 1
	}
	if innerH < 0 {
		innerH = 0
	}

	// Top border with embedded title: ╭─ Title ────╮
	label := " " + title + " "
	fill := inner - 1 - ansi.StringWidth(label)
	if fill < 0 {
		label = ansi.Truncate(label, inner-1, "…")
		fill = inner - 1 - ansi.StringWidth(label)
	}
	top := bs.Render("╭─") + titleStyle.Render(label) + bs.Render(strings.Repeat("─", max(fill, 0))+"╮")

	lines := strings.Split(content, "\n")
	body := make([]string, 0, innerH)
	for i := 0; i < innerH; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		l = ansi.Truncate(l, inner, "…")
		pad := inner - ansi.StringWidth(l)
		if pad < 0 {
			pad = 0
		}
		body = append(body, bs.Render("│")+l+strings.Repeat(" ", pad)+bs.Render("│"))
	}
	bottom := bs.Render("╰" + strings.Repeat("─", inner) + "╯")
	return top + "\n" + strings.Join(body, "\n") + "\n" + bottom
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestRenderPanel' -v`
Expected: PASS. (If `ansi.Truncate`'s signature differs, check usage elsewhere in the repo: `grep -rn "ansi.Truncate\|ansi.Cut" internal/` and match it.)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/chrome.go internal/app/tui/settings/chrome_test.go
git commit -m "feat(settings): bordered panel chrome with embedded titles and focus colors"
```

---

### Task 6: section frames A — Privacy, Snapshots, Commands, Indexing, Web, Swarm, Diagnostics

**Files:**
- Create: `internal/app/tui/settings/frames_basic.go`
- Test: `internal/app/tui/settings/frames_basic_test.go`

These coexist with the legacy `section_*.go` panes until Task 10 switches the Model over and deletes them. Frame builders are named `xxxFrame` to avoid clashing with the legacy `newXxxPane` constructors.

**Interfaces:**
- Consumes: helpers from Tasks 1–4; `state`; `config.Config` field paths exactly as used by the legacy sections (see `section_privacy.go`, `section_snapshots.go`, `section_commands.go`, `section_indexing.go`, `section_web.go`, `section_swarm.go`, `section_diagnostics.go` before deleting them).
- Produces: `privacyFrame(s *state) *frame`, `snapshotsFrame`, `commandsFrame`, `indexingFrame`, `webFrame`, `swarmFrame`, `diagnosticsFrame` — all `func(s *state) *frame`.

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestPrivacyFrameTogglesRemoteProviders(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(privacyFrame(s))
	ps.SetSize(60, 20)
	if ps.top().list.CursorRow().title != "Remote providers allowed" {
		t.Fatalf("first row should be Remote providers allowed, got %q", ps.top().list.CursorRow().title)
	}
	before := s.cfg.Privacy.RemoteProvidersAllowed
	ps.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if s.cfg.Privacy.RemoteProvidersAllowed == before {
		t.Fatal("space should toggle the working copy")
	}
}

func TestWebFrameDurationValidation(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(webFrame(s))
	ps.SetSize(60, 20)
	// move to "Fetch timeout" row
	for ps.top().list.CursorRow().title != "Fetch timeout" {
		ps.Update(key("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ps.top().list.input.SetValue("45s")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Web.FetchTimeout.String() != "45s" {
		t.Fatalf("expected 45s, got %v", s.cfg.Web.FetchTimeout)
	}
}

func TestDiagnosticsFrameIsMapAtRoot(t *testing.T) {
	s := newState(config.Default())
	s.cfg.Diagnostics.Commands = map[string]string{"lint": "go vet ./..."}
	ps := newPaneStack(diagnosticsFrame(s))
	ps.SetSize(60, 20)
	view := ps.top().list.View()
	if !strings.Contains(view, "lint") {
		t.Fatalf("diagnostics root should list command keys directly, got:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestPrivacyFrame|TestWebFrame|TestDiagnosticsFrame' -v`
Expected: FAIL — `undefined: privacyFrame`, …

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/frames_basic.go`:

```go
package settings

func privacyFrame(s *state) *frame {
	return newFrame("Privacy", func() []*field {
		return []*field{
			{id: "privacy.remote_providers", title: "Remote providers allowed", kind: kindToggle,
				desc:    "allow remote providers globally",
				getBool: func() bool { return s.cfg.Privacy.RemoteProvidersAllowed },
				setBool: func(v bool) { s.cfg.Privacy.RemoteProvidersAllowed = v }},
			{id: "privacy.redact_secrets", title: "Redact secrets", kind: kindToggle,
				desc:    "scrub likely secrets from context sent to models",
				getBool: func() bool { return s.cfg.Privacy.RedactSecrets },
				setBool: func(v bool) { s.cfg.Privacy.RedactSecrets = v }},
			{id: "privacy.include_gitignored", title: "Include gitignored files", kind: kindToggle,
				desc:    "let indexing and context include gitignored paths",
				getBool: func() bool { return s.cfg.Privacy.IncludeGitignoredFiles },
				setBool: func(v bool) { s.cfg.Privacy.IncludeGitignoredFiles = v }},
		}
	})
}

func snapshotsFrame(s *state) *frame {
	return newFrame("Snapshots", func() []*field {
		return []*field{
			{id: "snapshots.enabled", title: "Enabled", kind: kindToggle,
				desc:    "capture before-write snapshots of changed files",
				getBool: func() bool { return s.cfg.Snapshots.Enabled },
				setBool: func(v bool) { s.cfg.Snapshots.Enabled = v }},
			intField2("snapshots.retention_days", "Retention days",
				func() int { return s.cfg.Snapshots.RetentionDays }, 0,
				func(v int) { s.cfg.Snapshots.RetentionDays = v }),
			intField2("snapshots.max_file_bytes", "Max file bytes",
				func() int { return s.cfg.Snapshots.MaxFileBytes }, 0,
				func(v int) { s.cfg.Snapshots.MaxFileBytes = v }),
		}
	})
}

func commandsFrame(s *state) *frame {
	return newFrame("Commands", func() []*field {
		return []*field{
			scalarField("commands.test", "Test command",
				func() string { return s.cfg.Commands.Test },
				func(v string) error { s.cfg.Commands.Test = v; return nil }),
			scalarField("commands.format", "Format command",
				func() string { return s.cfg.Commands.Format },
				func(v string) error { s.cfg.Commands.Format = v; return nil }),
			scalarField("commands.vet", "Vet command",
				func() string { return s.cfg.Commands.Vet },
				func(v string) error { s.cfg.Commands.Vet = v; return nil }),
			scalarField("project.name", "Project name",
				func() string { return s.cfg.Project.Name },
				func(v string) error { s.cfg.Project.Name = v; return nil }),
			listDrill("project.languages", "Languages", &s.cfg.Project.Languages),
		}
	})
}

func indexingFrame(s *state) *frame {
	return newFrame("Indexing", func() []*field {
		return []*field{
			{id: "indexing.treesitter", title: "Use treesitter", kind: kindToggle,
				getBool: func() bool { return s.cfg.Indexing.UseTreesitter },
				setBool: func(v bool) { s.cfg.Indexing.UseTreesitter = v }},
			{id: "indexing.embeddings", title: "Use embeddings", kind: kindToggle,
				getBool: func() bool { return s.cfg.Indexing.UseEmbeddings },
				setBool: func(v bool) { s.cfg.Indexing.UseEmbeddings = v }},
			{id: "indexing.summarise", title: "Summarise files", kind: kindToggle,
				getBool: func() bool { return s.cfg.Indexing.SummariseFiles },
				setBool: func(v bool) { s.cfg.Indexing.SummariseFiles = v }},
			listDrill("indexing.ignore", "Ignore patterns", &s.cfg.Indexing.Ignore),
		}
	})
}

func webFrame(s *state) *frame {
	return newFrame("Web", func() []*field {
		return []*field{
			{id: "web.enabled", title: "Enabled", kind: kindToggle,
				desc:    "allow web.fetch / web.search tools",
				getBool: func() bool { return s.cfg.Web.Enabled },
				setBool: func(v bool) { s.cfg.Web.Enabled = v }},
			scalarField("web.fetch_timeout", "Fetch timeout",
				func() string { return s.cfg.Web.FetchTimeout.String() },
				durationSetter(func(d time.Duration) { s.cfg.Web.FetchTimeout = d })),
			scalarField("web.search_provider", "Search provider",
				func() string { return s.cfg.Web.SearchProvider },
				func(v string) error { s.cfg.Web.SearchProvider = v; return nil }),
			scalarField("web.search_url", "Search URL",
				func() string { return s.cfg.Web.SearchURL },
				func(v string) error { s.cfg.Web.SearchURL = v; return nil }),
			secretRow("web.search_key", "Search key",
				func() string { return s.cfg.Web.SearchKey },
				func(v string) { s.cfg.Web.SearchKey = v }),
		}
	})
}

func swarmFrame(s *state) *frame {
	return newFrame("Swarm", func() []*field {
		return []*field{
			intField2("swarm.max_fix_rounds", "Max fix rounds",
				func() int { return s.cfg.Swarm.Budget.MaxFixRounds }, 0,
				func(v int) { s.cfg.Swarm.Budget.MaxFixRounds = v }),
			intField2("swarm.max_total_tokens", "Max total tokens",
				func() int { return s.cfg.Swarm.Budget.MaxTotalTokens }, 0,
				func(v int) { s.cfg.Swarm.Budget.MaxTotalTokens = v }),
			mapIntDrill("swarm.tool_iters", "Tool iters", &s.cfg.Swarm.Budget.ToolIters),
		}
	})
}

// diagnosticsFrame is nothing but the commands map, so the root frame IS the
// map frame (no pointless single-row drill).
func diagnosticsFrame(s *state) *frame {
	drill := mapStringDrill("diagnostics.commands", "Commands", &s.cfg.Diagnostics.Commands)
	f := drill.build()
	f.title = "Diagnostics"
	return f
}
```

Add `"time"` to imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestPrivacyFrame|TestWebFrame|TestDiagnosticsFrame' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/frames_basic.go internal/app/tui/settings/frames_basic_test.go
git commit -m "feat(settings): frame specs for privacy/snapshots/commands/indexing/web/swarm/diagnostics"
```

---

### Task 7: section frames B — Agent, Shell, Sandbox

**Files:**
- Create: `internal/app/tui/settings/frames_agent.go`
- Create: `internal/app/tui/settings/frames_shell.go`
- Test: `internal/app/tui/settings/frames_agent_test.go`

**Interfaces:**
- Consumes: Tasks 1–4 helpers; `activePresetNameFor` (currently in `model.go` — keep it there); `routing.RoleImplementer`.
- Produces: `agentFrame(s *state) *frame`, `shellFrame(s *state) *frame`, `sandboxFrame(s *state) *frame`.

The Agent section preserves the legacy preset-aware binding: Provider/Model/Local-only write into the active preset when one exists, else into `cfg.Agent.*` (see `section_agent.go` `Validate` closures — replicate exactly).

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestAgentFrameProviderWritesToActivePreset(t *testing.T) {
	cfg := config.Default()
	s := newState(cfg)
	preset := activePresetNameFor(s.cfg)
	if preset == "" {
		t.Skip("default config has no active preset; covered by direct-write test below")
	}
	ps := newPaneStack(agentFrame(s))
	ps.SetSize(80, 24)
	for ps.top().list.CursorRow().title != "Provider" {
		ps.Update(key("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ps.top().list.input.SetValue("vllm")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Models.Presets[preset].Provider != "vllm" {
		t.Fatalf("provider should write to preset %q, got %q", preset, s.cfg.Models.Presets[preset].Provider)
	}
}

func TestShellFrameHasEnumAndLists(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(shellFrame(s))
	ps.SetSize(80, 24)
	var titles []string
	for _, f := range ps.top().list.Rows() {
		titles = append(titles, f.title)
	}
	for _, want := range []string{"Allow network", "Dynamic argv0 guardrail", "Allow commands", "Confirm commands", "Deny patterns"} {
		found := false
		for _, ti := range titles {
			if ti == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("shell frame missing row %q; rows: %v", want, titles)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestAgentFrame|TestShellFrame' -v`
Expected: FAIL — `undefined: agentFrame`, `undefined: shellFrame`

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/frames_agent.go`:

```go
package settings

import (
	"marshal/internal/llm/routing"
)

func agentFrame(s *state) *frame {
	// Preset-aware setters: write into the active preset when one exists,
	// else into cfg.Agent — same rule as the legacy huh form.
	setProvider := func(v string) error {
		if name := activePresetNameFor(s.cfg); name != "" {
			if p, ok := s.cfg.Models.Presets[name]; ok {
				p.Provider = v
				s.cfg.Models.Presets[name] = p
				return nil
			}
		}
		s.cfg.Agent.Provider = v
		return nil
	}
	setModel := func(v string) error {
		if name := activePresetNameFor(s.cfg); name != "" {
			if p, ok := s.cfg.Models.Presets[name]; ok {
				p.Model = v
				s.cfg.Models.Presets[name] = p
				return nil
			}
		}
		s.cfg.Agent.Model = v
		return nil
	}
	getActive := func() routing.ModelPreset {
		if name := activePresetNameFor(s.cfg); name != "" {
			if p, ok := s.cfg.Models.Presets[name]; ok {
				return p
			}
		}
		return routing.ModelPreset{}
	}

	return newFrame("Agent", func() []*field {
		active := getActive()
		provider := active.Provider
		if provider == "" {
			provider = s.cfg.Agent.Provider
		}
		model := active.Model
		if model == "" {
			model = s.cfg.Agent.Model
		}
		presetTitle := active.Name
		if presetTitle == "" {
			presetTitle = "(none)"
		}
		return []*field{
			enumField("agent.default_profile", "Default profile", sortedKeys(s.cfg.AgentProfiles),
				func() string { return s.cfg.Profile.Default },
				func(v string) { s.cfg.Profile.Default = v }),
			// Read-only: shows which preset the profile resolves to.
			{id: "agent.preset", title: "Preset", kind: kindScalar,
				desc:   "resolved from the default profile's implementer role",
				getStr: func() string { return presetTitle }},
			scalarField("agent.provider", "Provider", func() string { return provider }, setProvider),
			scalarField("agent.model", "Model", func() string { return model }, setModel),
			{id: "agent.local_only", title: "Local only", kind: kindToggle,
				desc:    "block remote providers for this preset",
				getBool: func() bool { return getActive().LocalOnly },
				setBool: func(v bool) {
					if name := activePresetNameFor(s.cfg); name != "" {
						if p, ok := s.cfg.Models.Presets[name]; ok {
							p.LocalOnly = v
							s.cfg.Models.Presets[name] = p
						}
					}
				}},
			intField2("agent.max_tool_iterations", "Max tool iterations",
				func() int { return s.cfg.Agent.MaxToolIterations }, 1,
				func(v int) { s.cfg.Agent.MaxToolIterations = v }),
			intField2("agent.max_retries", "Max retries",
				func() int { return s.cfg.Agent.MaxRetries }, 0,
				func(v int) { s.cfg.Agent.MaxRetries = v }),
			intField2("agent.max_turn_context_tokens", "Max turn context tokens",
				func() int { return s.cfg.Agent.MaxTurnContextTokens }, 0,
				func(v int) { s.cfg.Agent.MaxTurnContextTokens = v }),
			intField2("agent.subtask_iterations", "Subtask iterations",
				func() int { return s.cfg.Agent.SubtaskIterations }, 0,
				func(v int) { s.cfg.Agent.SubtaskIterations = v }),
			{id: "agent.plan_first", title: "Plan first", kind: kindToggle,
				getBool: func() bool { return s.cfg.Agent.PlanFirst },
				setBool: func(v bool) { s.cfg.Agent.PlanFirst = v }},
		}
	})
}
```

`internal/app/tui/settings/frames_shell.go`:

```go
package settings

func shellFrame(s *state) *frame {
	return newFrame("Shell", func() []*field {
		return []*field{
			intField2("shell.timeout", "Default timeout (s)",
				func() int { return s.cfg.Tools.Shell.DefaultTimeoutSeconds }, 0,
				func(v int) { s.cfg.Tools.Shell.DefaultTimeoutSeconds = v }),
			intField2("shell.max_output", "Max output bytes",
				func() int { return s.cfg.Tools.Shell.MaxOutputBytes }, 0,
				func(v int) { s.cfg.Tools.Shell.MaxOutputBytes = v }),
			intField2("shell.max_jobs", "Max background jobs",
				func() int { return s.cfg.Tools.Shell.MaxBackgroundJobs }, 0,
				func(v int) { s.cfg.Tools.Shell.MaxBackgroundJobs = v }),
			scalarField("shell.retention", "Background retention",
				func() string { return s.cfg.Tools.Shell.BackgroundRetention.String() },
				durationSetter(func(d time.Duration) { s.cfg.Tools.Shell.BackgroundRetention = d })),
			{id: "shell.allow_network", title: "Allow network", kind: kindToggle,
				keywords: []string{"internet"},
				getBool:  func() bool { return s.cfg.Tools.Shell.AllowNetwork },
				setBool:  func(v bool) { s.cfg.Tools.Shell.AllowNetwork = v }},
			{id: "shell.allow_sudo", title: "Allow sudo", kind: kindToggle,
				getBool: func() bool { return s.cfg.Tools.Shell.AllowSudo },
				setBool: func(v bool) { s.cfg.Tools.Shell.AllowSudo = v }},
			{id: "shell.allow_destructive", title: "Allow destructive", kind: kindToggle,
				getBool: func() bool { return s.cfg.Tools.Shell.AllowDestructive },
				setBool: func(v bool) { s.cfg.Tools.Shell.AllowDestructive = v }},
			{id: "shell.auto_approve", title: "Auto-approve shell", kind: kindToggle,
				desc:    "run classified-safe commands without confirmation",
				getBool: func() bool { return s.cfg.Tools.Shell.AutoApprove },
				setBool: func(v bool) { s.cfg.Tools.Shell.AutoApprove = v }},
			enumField("shell.guardrail_argv0", "Dynamic argv0 guardrail",
				[]string{"deny", "confirm", "allow"},
				func() string { return s.cfg.Tools.Shell.GuardrailDynamicArgv0 },
				func(v string) { s.cfg.Tools.Shell.GuardrailDynamicArgv0 = v }),
			listDrill("shell.allow_commands", "Allow commands", &s.cfg.Tools.Shell.Allow.Commands),
			listDrill("shell.confirm_commands", "Confirm commands", &s.cfg.Tools.Shell.Confirm.Commands),
			listDrill("shell.deny_patterns", "Deny patterns", &s.cfg.Tools.Shell.Deny.Patterns),
		}
	})
}

func sandboxFrame(s *state) *frame {
	sb := &s.cfg.Tools.Shell.Sandbox
	return newFrame("Sandbox", func() []*field {
		return []*field{
			enumField("sandbox.backend", "Backend",
				[]string{"restricted", "container", "passthrough"},
				func() string { return sb.Backend },
				func(v string) { sb.Backend = v }),
			intField2("sandbox.memory_mb", "Memory limit (MB)",
				func() int { return sb.MemoryLimitMB }, 0, func(v int) { sb.MemoryLimitMB = v }),
			intField2("sandbox.cpu_seconds", "CPU seconds",
				func() int { return sb.CPUSeconds }, 0, func(v int) { sb.CPUSeconds = v }),
			intField2("sandbox.max_processes", "Max processes",
				func() int { return sb.MaxProcesses }, 0, func(v int) { sb.MaxProcesses = v }),
			intField2("sandbox.file_size_mb", "File size limit (MB)",
				func() int { return sb.FileSizeLimitMB }, 0, func(v int) { sb.FileSizeLimitMB = v }),
			scalarField("sandbox.container_runtime", "Container runtime",
				func() string { return sb.ContainerRuntime },
				func(v string) error { sb.ContainerRuntime = v; return nil }),
			scalarField("sandbox.container_image", "Container image",
				func() string { return sb.ContainerImage },
				func(v string) error { sb.ContainerImage = v; return nil }),
			{id: "sandbox.allow_fallback", title: "Allow fallback", kind: kindToggle,
				getBool: func() bool { return sb.AllowFallback },
				setBool: func(v bool) { sb.AllowFallback = v }},
			listDrill("sandbox.env_allowlist", "Env allowlist", &sb.EnvAllowlist),
			listDrill("sandbox.env_denylist", "Env denylist", &sb.EnvDenylist),
		}
	})
}
```

Add `"time"` to `frames_shell.go` imports. Note `sb := &s.cfg.Tools.Shell.Sandbox` is taken once outside the fields func — `state.cfg` is heap-allocated and stable (Model stores `*state`), so the pointer stays valid.

- [ ] **Step 4: Run tests, then run the full package**

Run: `go test ./internal/app/tui/settings/ -run 'TestAgentFrame|TestShellFrame' -v && go test ./internal/app/tui/settings/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/frames_agent.go internal/app/tui/settings/frames_shell.go internal/app/tui/settings/frames_agent_test.go
git commit -m "feat(settings): frame specs for agent/shell/sandbox"
```

---

### Task 8: section frames C — Providers, Presets, Hooks, Permissions

**Files:**
- Create: `internal/app/tui/settings/frames_collections.go`
- Test: `internal/app/tui/settings/frames_collections_test.go`

**Interfaces:**
- Consumes: `entriesDrill`, `secretRow`, `enumField`, setter helpers; `config.ProviderConfig`, `routing.ModelPreset`, `config.HookConfig`, `config.PermissionRule`.
- Produces: `providersFrame(s *state) *frame`, `presetsFrame`, `hooksFrame`, `permissionsFrame`.

Slice-backed collections (hooks, permissions) use index-keyed entries. Because edits apply immediately (Global Constraints), entry frames bind to `s.cfg.<slice>[idx]` directly.

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestProvidersAddAndEditType(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(providersFrame(s))
	ps.SetSize(80, 24)
	// providers root IS the collection frame; add an entry
	ps.Update(key("a"))
	for _, r := range "local" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	pc, ok := s.cfg.Providers["local"]
	if !ok {
		t.Fatalf("add should create provider, got %v", s.cfg.Providers)
	}
	if pc.Type != "openai_compatible" {
		t.Fatalf("new provider should default to openai_compatible, got %q", pc.Type)
	}
	// drill into it and edit Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ps.depth() != 2 {
		t.Fatalf("enter should drill into the provider, depth=%d", ps.depth())
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // edit first row (Type)
	ps.top().list.input.SetValue("anthropic")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Providers["local"].Type != "anthropic" {
		t.Fatalf("type edit should apply immediately, got %q", s.cfg.Providers["local"].Type)
	}
}

func TestHooksAddWithoutPromptAndDelete(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(hooksFrame(s))
	ps.SetSize(80, 24)
	ps.Update(key("a")) // no key prompt: adds immediately
	if len(s.cfg.Hooks.Entries) != 1 || s.cfg.Hooks.Entries[0].Event != "pre_tool" {
		t.Fatalf("a should append a pre_tool hook, got %v", s.cfg.Hooks.Entries)
	}
	ps.Update(key("d"))
	if len(s.cfg.Hooks.Entries) != 0 {
		t.Fatalf("d should delete the hook, got %v", s.cfg.Hooks.Entries)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestProvidersAdd|TestHooksAdd' -v`
Expected: FAIL — `undefined: providersFrame`, `undefined: hooksFrame`

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/frames_collections.go`:

```go
package settings

import (
	"fmt"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

// rootDrillFrame unwraps a single drill field into a section root frame, so
// sections that are nothing but one collection open directly on the list.
func rootDrillFrame(title string, drill *field) *frame {
	f := drill.build()
	f.title = title
	return f
}

func providersFrame(s *state) *frame {
	drill := entriesDrill("providers", "Providers", "New provider name",
		func() []string { return sortedKeys(s.cfg.Providers) },
		func(k string) string { return k + "  (" + maskKey(s.cfg.Providers[k].APIKey) + ")" },
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Providers[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			if s.cfg.Providers == nil {
				s.cfg.Providers = map[string]config.ProviderConfig{}
			}
			s.cfg.Providers[k] = config.ProviderConfig{Type: "openai_compatible"}
			return nil
		},
		func(k string) *frame {
			mut := func(f func(*config.ProviderConfig)) {
				pc := s.cfg.Providers[k]
				f(&pc)
				s.cfg.Providers[k] = pc
			}
			return newFrame(k, func() []*field {
				return []*field{
					scalarField("providers."+k+".type", "Type",
						func() string { return s.cfg.Providers[k].Type },
						func(v string) error { mut(func(p *config.ProviderConfig) { p.Type = v }); return nil }),
					scalarField("providers."+k+".base_url", "Base URL",
						func() string { return s.cfg.Providers[k].BaseURL },
						func(v string) error { mut(func(p *config.ProviderConfig) { p.BaseURL = v }); return nil }),
					{id: "providers." + k + ".api_key_env", title: "API key env", kind: kindScalar,
						desc:   "env var name resolved at provider construction — preferred over storing the key",
						getStr: func() string { return s.cfg.Providers[k].APIKeyEnv },
						setStr: func(v string) error { mut(func(p *config.ProviderConfig) { p.APIKeyEnv = v }); return nil }},
					secretRow("providers."+k+".api_key", "API key",
						func() string { return s.cfg.Providers[k].APIKey },
						func(v string) { mut(func(p *config.ProviderConfig) { p.APIKey = v }) }),
					{id: "providers." + k + ".tool_calling", title: "Tool calling", kind: kindToggle,
						desc:    "provider advertises native tool-calling support",
						getBool: func() bool { return s.cfg.Providers[k].ToolCalling },
						setBool: func(v bool) { mut(func(p *config.ProviderConfig) { p.ToolCalling = v }) }},
				}
			})
		},
		func(k string) { delete(s.cfg.Providers, k) })
	return rootDrillFrame("Providers", drill)
}

func presetsFrame(s *state) *frame {
	drill := entriesDrill("presets", "Model Presets", "New preset name",
		func() []string { return sortedKeys(s.cfg.Models.Presets) },
		func(k string) string {
			p := s.cfg.Models.Presets[k]
			return k + "  (" + p.Provider + "/" + p.Model + ")"
		},
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Models.Presets[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.Models.Presets[k] = routing.ModelPreset{Name: k}
			return nil
		},
		func(k string) *frame {
			mut := func(f func(*routing.ModelPreset)) {
				p := s.cfg.Models.Presets[k]
				f(&p)
				s.cfg.Models.Presets[k] = p
			}
			return newFrame(k, func() []*field {
				return []*field{
					scalarField("presets."+k+".provider", "Provider",
						func() string { return s.cfg.Models.Presets[k].Provider },
						func(v string) error { mut(func(p *routing.ModelPreset) { p.Provider = v }); return nil }),
					scalarField("presets."+k+".model", "Model",
						func() string { return s.cfg.Models.Presets[k].Model },
						func(v string) error { mut(func(p *routing.ModelPreset) { p.Model = v }); return nil }),
					intField2("presets."+k+".context_window", "Context window",
						func() int { return s.cfg.Models.Presets[k].ContextWindow }, 0,
						func(v int) { mut(func(p *routing.ModelPreset) { p.ContextWindow = v }) }),
					intField2("presets."+k+".max_output", "Max output tokens",
						func() int { return s.cfg.Models.Presets[k].MaxOutputTokens }, 0,
						func(v int) { mut(func(p *routing.ModelPreset) { p.MaxOutputTokens = v }) }),
					{id: "presets." + k + ".temperature", title: "Temperature", kind: kindScalar,
						getStr: func() string {
							return strconv.FormatFloat(s.cfg.Models.Presets[k].Temperature, 'f', -1, 64)
						},
						setStr: floatSetter(func(v float64) { mut(func(p *routing.ModelPreset) { p.Temperature = v }) })},
					{id: "presets." + k + ".top_p", title: "Top P", kind: kindScalar,
						getStr: func() string {
							return strconv.FormatFloat(s.cfg.Models.Presets[k].TopP, 'f', -1, 64)
						},
						setStr: floatSetter(func(v float64) { mut(func(p *routing.ModelPreset) { p.TopP = v }) })},
					enumField("presets."+k+".tool_calling", "Tool calling",
						[]string{"native", "simulated", "none"},
						func() string { return s.cfg.Models.Presets[k].ToolCalling },
						func(v string) { mut(func(p *routing.ModelPreset) { p.ToolCalling = v }) }),
					enumField("presets."+k+".reasoning", "Reasoning effort",
						[]string{"low", "medium", "high", "none"},
						func() string { return s.cfg.Models.Presets[k].ReasoningEffort },
						func(v string) { mut(func(p *routing.ModelPreset) { p.ReasoningEffort = v }) }),
					{id: "presets." + k + ".local_only", title: "Local only", kind: kindToggle,
						desc:    "block remote providers for this preset",
						getBool: func() bool { return s.cfg.Models.Presets[k].LocalOnly },
						setBool: func(v bool) { mut(func(p *routing.ModelPreset) { p.LocalOnly = v }) }},
				}
			})
		},
		func(k string) { delete(s.cfg.Models.Presets, k) })
	return rootDrillFrame("Model Presets", drill)
}

// sliceKeys returns "0".."n-1" index keys for slice-backed collections.
func sliceKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i)
	}
	return out
}

func hooksFrame(s *state) *frame {
	drill := entriesDrill("hooks", "Hooks", "",
		func() []string { return sliceKeys(len(s.cfg.Hooks.Entries)) },
		func(k string) string {
			i, _ := strconv.Atoi(k)
			h := s.cfg.Hooks.Entries[i]
			return fmt.Sprintf("%s %s → %s", h.Event, h.Matcher, h.Command)
		},
		func(string) error {
			s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries, config.HookConfig{Event: "pre_tool"})
			return nil
		},
		func(k string) *frame {
			i, _ := strconv.Atoi(k)
			return newFrame("hook "+k, func() []*field {
				if i >= len(s.cfg.Hooks.Entries) {
					return nil
				}
				h := &s.cfg.Hooks.Entries[i]
				return []*field{
					scalarField("hooks."+k+".event", "Event",
						func() string { return h.Event },
						func(v string) error { h.Event = v; return nil }),
					scalarField("hooks."+k+".matcher", "Matcher",
						func() string { return h.Matcher },
						func(v string) error { h.Matcher = v; return nil }),
					scalarField("hooks."+k+".command", "Command",
						func() string { return h.Command },
						func(v string) error { h.Command = v; return nil }),
					intField2("hooks."+k+".timeout_ms", "Timeout (ms)",
						func() int { return h.TimeoutMS }, 0, func(v int) { h.TimeoutMS = v }),
				}
			})
		},
		func(k string) {
			i, _ := strconv.Atoi(k)
			if i < len(s.cfg.Hooks.Entries) {
				s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries[:i], s.cfg.Hooks.Entries[i+1:]...)
			}
		})
	return rootDrillFrame("Hooks", drill)
}

func permissionsFrame(s *state) *frame {
	drill := entriesDrill("permissions", "Permissions", "",
		func() []string { return sliceKeys(len(s.cfg.Permissions.Rules)) },
		func(k string) string {
			i, _ := strconv.Atoi(k)
			r := s.cfg.Permissions.Rules[i]
			return fmt.Sprintf("%s %s → %s", r.Permission, r.Pattern, r.Action)
		},
		func(string) error {
			s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules, config.PermissionRule{
				Permission: "shell", Pattern: "*", Action: "confirm",
			})
			return nil
		},
		func(k string) *frame {
			i, _ := strconv.Atoi(k)
			return newFrame("rule "+k, func() []*field {
				if i >= len(s.cfg.Permissions.Rules) {
					return nil
				}
				r := &s.cfg.Permissions.Rules[i]
				return []*field{
					scalarField("permissions."+k+".permission", "Permission",
						func() string { return r.Permission },
						func(v string) error { r.Permission = v; return nil }),
					scalarField("permissions."+k+".pattern", "Pattern",
						func() string { return r.Pattern },
						func(v string) error { r.Pattern = v; return nil }),
					enumField("permissions."+k+".action", "Action",
						[]string{"allow", "confirm", "deny"},
						func() string { return r.Action },
						func(v string) { r.Action = v }),
				}
			})
		},
		func(k string) {
			i, _ := strconv.Atoi(k)
			if i < len(s.cfg.Permissions.Rules) {
				s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules[:i], s.cfg.Permissions.Rules[i+1:]...)
			}
		})
	return rootDrillFrame("Permissions", drill)
}
```

Add `"strconv"` to imports. Note: `&s.cfg.Hooks.Entries[i]` / `&s.cfg.Permissions.Rules[i]` pointers are re-derived inside the fields func on every Refresh, so append/delete reallocation cannot leave a stale pointer live — the pointer is taken fresh each render. The `if i >= len(...)` guard covers the frame that is still on the stack when its entry was deleted from underneath it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestProvidersAdd|TestHooksAdd' -v && go test ./internal/app/tui/settings/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/frames_collections.go internal/app/tui/settings/frames_collections_test.go
git commit -m "feat(settings): frame specs for providers/presets/hooks/permissions"
```

---

### Task 9: section frame — MCP (nested drill: servers → args/env)

**Files:**
- Create: `internal/app/tui/settings/frames_mcp.go`
- Test: `internal/app/tui/settings/frames_mcp_test.go`

**Interfaces:**
- Consumes: everything above; `config.MCPServerConfig`.
- Produces: `mcpFrame(s *state) *frame` — root has Disclosure threshold (scalar), Servers (entries drill), Policies (map drill); each server frame has Command (scalar), Args (list drill), Env (map drill).

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestMCPServerNestedDrill(t *testing.T) {
	s := newState(config.Default())
	s.cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Command: "gh-mcp", Args: []string{"--stdio"}, Env: map[string]string{}},
	}
	ps := newPaneStack(mcpFrame(s))
	ps.SetSize(80, 24)

	// root rows: Disclosure threshold, Servers, Policies
	rows := ps.top().list.Rows()
	if len(rows) != 3 {
		t.Fatalf("expected 3 root rows, got %d", len(rows))
	}

	// drill: Servers → github → Args
	ps.top().list.SetCursor(1)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Servers
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // github
	for ps.top().list.CursorRow().title != "Args" {
		ps.Update(key("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Args
	if got := ps.breadcrumb("MCP"); got != "MCP › Servers › github › Args" {
		t.Fatalf("breadcrumb wrong: %q", got)
	}
	// add an arg and confirm it lands in the working copy
	ps.Update(key("a"))
	for _, r := range "-v" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := s.cfg.MCP.Servers["github"].Args; len(got) != 2 || got[1] != "-v" {
		t.Fatalf("arg add should apply to working copy, got %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/ -run 'TestMCPServer' -v`
Expected: FAIL — `undefined: mcpFrame`

- [ ] **Step 3: Write the implementation**

`internal/app/tui/settings/frames_mcp.go`:

```go
package settings

import (
	"fmt"

	"marshal/internal/app/config"
)

func mcpFrame(s *state) *frame {
	serversDrill := entriesDrill("mcp.servers", "Servers", "New server name",
		func() []string { return sortedKeys(s.cfg.MCP.Servers) },
		func(k string) string { return k + "  (" + s.cfg.MCP.Servers[k].Command + ")" },
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.MCP.Servers[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			if s.cfg.MCP.Servers == nil {
				s.cfg.MCP.Servers = map[string]config.MCPServerConfig{}
			}
			s.cfg.MCP.Servers[k] = config.MCPServerConfig{Env: map[string]string{}}
			return nil
		},
		func(k string) *frame {
			// Args and Env need stable pointers for the drill builders; the
			// mutate helper writes the struct back after each change. We keep
			// a per-frame copy whose slices/maps alias the working config
			// only through explicit writeback.
			return newFrame(k, func() []*field {
				srv := s.cfg.MCP.Servers[k]
				writeback := func() { s.cfg.MCP.Servers[k] = srv }
				return []*field{
					scalarField("mcp.servers."+k+".command", "Command",
						func() string { return s.cfg.MCP.Servers[k].Command },
						func(v string) error {
							srv.Command = v
							writeback()
							return nil
						}),
					// Args/Env drills operate on fresh copies bound through
					// closures that write back on every mutation.
					mcpArgsDrill(s, k),
					mcpEnvDrill(s, k),
				}
			})
		},
		func(k string) { delete(s.cfg.MCP.Servers, k) })

	return newFrame("MCP", func() []*field {
		return []*field{
			intField2("mcp.disclosure_threshold", "Disclosure threshold tools",
				func() int { return s.cfg.MCP.DisclosureThresholdTools }, 0,
				func(v int) { s.cfg.MCP.DisclosureThresholdTools = v }),
			serversDrill,
			mapStringDrill("mcp.policies", "Policies", &s.cfg.MCP.Policies),
		}
	})
}

// mcpArgsDrill is listDrill's shape, but map-stored structs can't hand out
// a *[]string into the map value — every mutation must write the struct
// back. It reimplements the small list frame against getter/setter closures.
func mcpArgsDrill(s *state, server string) *field {
	get := func() []string { return s.cfg.MCP.Servers[server].Args }
	set := func(args []string) {
		srv := s.cfg.MCP.Servers[server]
		srv.Args = args
		s.cfg.MCP.Servers[server] = srv
	}
	buildFields := func() []*field {
		args := get()
		out := make([]*field, len(args))
		for i := range args {
			i := i
			out[i] = &field{
				id: fmt.Sprintf("mcp.servers.%s.args.%d", server, i), title: args[i], kind: kindScalar,
				getStr: func() string { return get()[i] },
				setStr: func(v string) error {
					if v == "" {
						return fmt.Errorf("cannot be empty")
					}
					a := get()
					a[i] = v
					set(a)
					return nil
				},
				del: func() {
					a := get()
					set(append(a[:i], a[i+1:]...))
				},
			}
		}
		return out
	}
	return &field{
		id: "mcp.servers." + server + ".args", title: "Args", kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d items", len(get())) },
		build: func() *frame {
			return newCollectionFrame("Args", "New entry", buildFields, func(v string) error {
				if v == "" {
					return fmt.Errorf("cannot be empty")
				}
				set(append(get(), v))
				return nil
			})
		},
	}
}

// mcpEnvDrill: Env maps inside the server struct are reference types, so
// mutating the map through the struct copy mutates the stored map directly;
// only nil-map creation needs a writeback.
func mcpEnvDrill(s *state, server string) *field {
	ensure := func() map[string]string {
		srv := s.cfg.MCP.Servers[server]
		if srv.Env == nil {
			srv.Env = map[string]string{}
			s.cfg.MCP.Servers[server] = srv
		}
		return srv.Env
	}
	buildFields := func() []*field {
		env := ensure()
		keys := sortedKeys(env)
		out := make([]*field, len(keys))
		for i, k := range keys {
			k := k
			out[i] = &field{
				id: "mcp.servers." + server + ".env." + k, title: k, kind: kindScalar,
				getStr: func() string { return ensure()[k] },
				setStr: func(v string) error { ensure()[k] = v; return nil },
				del:    func() { delete(ensure(), k) },
			}
		}
		return out
	}
	return &field{
		id: "mcp.servers." + server + ".env", title: "Env", kind: kindDrill,
		summary: func() string { return fmt.Sprintf("%d entries", len(ensure())) },
		build: func() *frame {
			return newCollectionFrame("Env", "New key", buildFields, func(k string) error {
				if k == "" {
					return fmt.Errorf("key cannot be empty")
				}
				env := ensure()
				if _, ok := env[k]; ok {
					return fmt.Errorf("key already exists")
				}
				env[k] = ""
				return nil
			})
		},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestMCPServer' -v && go test ./internal/app/tui/settings/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/frames_mcp.go internal/app/tui/settings/frames_mcp_test.go
git commit -m "feat(settings): MCP frame with nested server args/env drills"
```

---

### Task 10: Model rewrite — bordered frame, two-level focus, Esc rule, footer, narrow mode; delete legacy machinery

This is the switch-over task. The Model consumes frames, the old pane machinery is deleted, and old tests are rewritten. It is the largest task; its deliverable is "the settings screen works end-to-end on the new engine."

**Files:**
- Rewrite: `internal/app/tui/settings/model.go`
- Rewrite: `internal/app/tui/settings/sections.go`
- Rewrite: `internal/app/tui/settings/model_test.go`
- Delete: `internal/app/tui/settings/pane.go`, `mixed.go`, `mixed_test.go`, `composite.go`, `composite_test.go`, `collection.go`, `collection_test.go`, `scalar.go`, `liststrings.go`, `liststrings_test.go`, `mapeditor.go`, `mapeditor_test.go`, `section_agent.go`, `section_agent_test.go`, `section_commands.go`, `section_diagnostics.go`, `section_diagnostics_test.go`, `section_hooks.go`, `section_hooks_test.go`, `section_indexing.go`, `section_mcp.go`, `section_mcp_test.go`, `section_permissions.go`, `section_permissions_test.go`, `section_presets.go`, `section_presets_test.go`, `section_privacy.go`, `section_providers.go`, `section_providers_test.go`, `section_sandbox.go`, `section_shell.go`, `section_shell_test.go`, `section_snapshots.go`, `section_swarm.go`, `section_swarm_test.go`, `section_web.go`, `section_scalar_test.go`, `skeleton_test.go`, `integration_test.go`
- Modify: `internal/app/tui/settings/masked.go` (delete `secretField`, keep `maskKey`; drop the `huh` import)
- Modify: `internal/app/tui/settings/setters.go` (rename `intField2` → `intField`, update all call sites in `frames_*.go`)

**Interfaces:**
- Consumes: `paneStack`, frames from Tasks 6–9, `renderPanel`, `warningsFor`, `state`.
- Produces (consumed by Tasks 11–12 and the parent TUI):
  - `type sectionSpec struct { id, title string; root func(*state) *frame }` and `func sectionList() []sectionSpec` (same 15 sections, same order/titles as today).
  - `Model` fields: `state *state`, `specs []sectionSpec`, `panes []*paneStack`, `cursor int`, `paneFocused bool`, `overlay overlayKind` (`overlayNone`/`overlaySearch`/`overlayHelp`), `pendingCancel bool`, `savedFlash bool`, `footerMsg string`, `width, height int`, `sidebarHidden bool`, `workingDir, projectCfgPath string`.
  - Preserved public methods (Global Constraints list). `FocusedFieldTitle()` returns the active frame's cursor-row title when pane-focused, else the section title. `BoolValue` keeps its current body verbatim.
  - `activeSectionTitle()`, `activePane() *paneStack` helpers for Tasks 11–12.

- [ ] **Step 1: Write the failing tests (rewrite `model_test.go`)**

Replace `model_test.go` wholesale. Keep any existing helper for constructing `tea.KeyPressMsg` if one exists there (check before deleting; if the `key()` helper from Task 1 duplicates it, keep exactly one copy in `fieldlist_test.go`).

```go
package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func newTestModel(t *testing.T) Model {
	t.Helper()
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)
	return m
}

func press(m Model, keys ...tea.KeyPressMsg) Model {
	for _, k := range keys {
		m, _ = m.Update(k)
	}
	return m
}

func TestTwoLevelFocusAndEscRule(t *testing.T) {
	m := newTestModel(t)
	if m.paneFocused {
		t.Fatal("focus should start on the sidebar")
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.paneFocused {
		t.Fatal("enter should focus the pane")
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.paneFocused {
		t.Fatal("esc in pane at root should return to sidebar")
	}
}

func TestEscAtSidebarCleanEmitsCancelled(t *testing.T) {
	m := newTestModel(t)
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc with clean state should produce a command")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatal("expected CancelledMsg")
	}
}

func TestEscWithDirtyStateNeedsDoublePress(t *testing.T) {
	m := newTestModel(t)
	// dirty the config: enter Privacy pane and toggle the first row
	// (sidebar starts on Agent; j*3 = Privacy per sectionList order)
	m = press(m, key("j"), key("j"), key("j"), tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !m.dirty() {
		t.Fatal("toggle should dirty the working copy")
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape}) // back to sidebar
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("first esc with dirty state must not cancel")
	}
	if !strings.Contains(m.View(), "unsaved") {
		t.Fatalf("footer should warn about unsaved changes:\n%s", m.View())
	}
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("second esc should cancel")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatal("expected CancelledMsg on second esc")
	}
}

func TestViewHasBorderedPanelsAndFocusMarker(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	if !strings.Contains(v, "╭") || !strings.Contains(v, "╰") {
		t.Fatalf("view should render bordered panels:\n%s", v)
	}
	if !strings.Contains(v, " Settings ") {
		t.Fatalf("sidebar panel should be titled Settings:\n%s", v)
	}
	if !strings.Contains(v, " Agent ") {
		t.Fatalf("detail panel should be titled with the section:\n%s", v)
	}
}

func TestFooterIsContextSensitive(t *testing.T) {
	m := newTestModel(t)
	sidebar := m.View()
	if !strings.Contains(sidebar, "open") || !strings.Contains(sidebar, "search") {
		t.Fatalf("sidebar footer should show open/search hints:\n%s", sidebar)
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // into pane; Agent first rows include a toggle further down
	pane := m.View()
	if !strings.Contains(pane, "sidebar") && !strings.Contains(pane, "back") {
		t.Fatalf("pane footer should hint how to get back:\n%s", pane)
	}
}

func TestDirtyDotInSidebarTitle(t *testing.T) {
	m := newTestModel(t)
	m = press(m, key("j"), key("j"), key("j"), tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !strings.Contains(m.View(), "Settings ●") {
		t.Fatalf("dirty state should mark the sidebar title:\n%s", m.View())
	}
}

func TestNarrowModePagesSections(t *testing.T) {
	m := newTestModel(t)
	m.SetSize(60, 24)
	if !m.sidebarHidden {
		t.Fatal("width 60 should hide the sidebar")
	}
	if !strings.Contains(m.View(), "‹") {
		t.Fatalf("narrow mode should show paging chevrons:\n%s", m.View())
	}
	before := m.cursor
	m = press(m, key("l"))
	if m.cursor != before+1 {
		t.Fatalf("l should page to next section, cursor=%d", m.cursor)
	}
}

func TestCtrlSSavesAndFlashes(t *testing.T) {
	m := newTestModel(t)
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+s should produce a save command")
	}
	msg := cmd()
	if _, ok := msg.(SavedMsg); !ok {
		t.Fatalf("expected SavedMsg, got %T (footer: %q)", msg, m.Footer())
	}
}

func TestBoolValueStillReadsWorkingCopy(t *testing.T) {
	m := newTestModel(t)
	got := m.BoolValue("Remote providers allowed")
	if got != m.state.cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("BoolValue should read the working copy")
	}
}

func TestFocusedFieldTitleFollowsPaneCursor(t *testing.T) {
	m := newTestModel(t)
	if m.FocusedFieldTitle() != "Agent" {
		t.Fatalf("sidebar focus should report section title, got %q", m.FocusedFieldTitle())
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.FocusedFieldTitle() != "Default profile" {
		t.Fatalf("pane focus should report the cursor row, got %q", m.FocusedFieldTitle())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/tui/settings/ -run 'TestTwoLevel|TestEsc|TestViewHas|TestFooter|TestDirtyDot|TestNarrow|TestCtrlS|TestBoolValue|TestFocusedField' -v`
Expected: FAIL (old Model still huh-based; several assertions can't hold)

- [ ] **Step 3: Rewrite `sections.go`**

```go
package settings

// sectionSpec maps a sidebar entry to its root frame builder.
type sectionSpec struct {
	id    string
	title string
	root  func(s *state) *frame
}

func sectionList() []sectionSpec {
	return []sectionSpec{
		{id: "agent", title: "Agent", root: agentFrame},
		{id: "providers", title: "Providers", root: providersFrame},
		{id: "presets", title: "Model Presets", root: presetsFrame},
		{id: "privacy", title: "Privacy", root: privacyFrame},
		{id: "shell", title: "Shell", root: shellFrame},
		{id: "sandbox", title: "Sandbox", root: sandboxFrame},
		{id: "indexing", title: "Indexing", root: indexingFrame},
		{id: "web", title: "Web", root: webFrame},
		{id: "swarm", title: "Swarm", root: swarmFrame},
		{id: "mcp", title: "MCP", root: mcpFrame},
		{id: "snapshots", title: "Snapshots", root: snapshotsFrame},
		{id: "hooks", title: "Hooks", root: hooksFrame},
		{id: "permissions", title: "Permissions", root: permissionsFrame},
		{id: "diagnostics", title: "Diagnostics", root: diagnosticsFrame},
		{id: "commands", title: "Commands", root: commandsFrame},
	}
}
```

- [ ] **Step 4: Rewrite `model.go`**

```go
package settings

import (
	"fmt"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/routing"
)

var settingsTheme = theme.Load()

const (
	sidebarWidth      = 20
	sidebarBreakpoint = 70
	maxFrameWidth     = 100
	maxFrameHeight    = 32
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlaySearch
	overlayHelp
)

type Model struct {
	state          *state
	specs          []sectionSpec
	panes          []*paneStack
	cursor         int
	paneFocused    bool
	overlay        overlayKind
	search         searchState // zero value until Task 11 wires it
	pendingCancel  bool
	savedFlash     bool
	footerMsg      string // error/status text; cleared on next keypress
	workingDir     string
	projectCfgPath string
	width          int
	height         int
	sidebarHidden  bool
}

func New(cfg config.Config, workingDir, projectCfgPath string) Model {
	st := newState(cfg)
	specs := sectionList()
	panes := make([]*paneStack, len(specs))
	for i, sp := range specs {
		panes[i] = newPaneStack(sp.root(st))
	}
	return Model{
		state:          st,
		specs:          specs,
		panes:          panes,
		workingDir:     workingDir,
		projectCfgPath: projectCfgPath,
	}
}

func (m Model) Init() tea.Cmd { return nil }

// frameSize returns the outer settings frame dimensions.
func (m Model) frameSize() (int, int) {
	w := min(m.width-2, maxFrameWidth)
	h := min(m.height-1, maxFrameHeight)
	if w < 40 {
		w = max(m.width, 40)
	}
	if h < 10 {
		h = max(m.height, 10)
	}
	return w, h
}

func (m *Model) SetSize(width, height int) {
	m.width, m.height = width, height
	m.sidebarHidden = width > 0 && width < sidebarBreakpoint
	fw, fh := m.frameSize()
	pw := fw - 2 // detail panel interior width
	if !m.sidebarHidden {
		pw = fw - sidebarWidth - 2
	}
	ph := fh - 4 // borders + title/warning line + footer
	for _, p := range m.panes {
		p.SetSize(pw-2, ph)
	}
}

func (m Model) dirty() bool { return !reflect.DeepEqual(m.state.cfg, m.state.snapshot) }

func (m *Model) activePane() *paneStack     { return m.panes[m.cursor] }
func (m Model) activeSectionTitle() string  { return m.specs[m.cursor].title }

func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		return *m, nil
	}
	ks := k.String()
	if ks != "esc" {
		m.pendingCancel = false
	}
	m.savedFlash = false
	if m.footerMsg != "" && ks != "ctrl+s" {
		m.footerMsg = ""
	}

	// Overlays capture everything (Task 11 wires search; Task 12 help).
	if m.overlay == overlayHelp {
		if ks == "esc" || ks == "?" {
			m.overlay = overlayNone
		}
		return *m, nil
	}
	if m.overlay == overlaySearch {
		return *m, m.updateSearch(k)
	}

	editing := m.activePane().top().list.Editing()

	// Global keys (never while an inline edit wants the characters).
	switch ks {
	case "ctrl+s":
		return *m, m.saveCmd()
	case "ctrl+o": // parent toggle key behaves like Esc-at-top: close request
		return *m, m.requestClose()
	}
	if !editing {
		switch ks {
		case "/":
			m.openSearch()
			return *m, nil
		case "?":
			m.overlay = overlayHelp
			return *m, nil
		}
	}

	// Esc: up one level, always.
	if ks == "esc" {
		if editing {
			m.activePane().top().list.CancelEdit()
			return *m, nil
		}
		if m.activePane().pop() {
			return *m, nil
		}
		if m.paneFocused && !m.sidebarHidden {
			m.paneFocused = false
			return *m, nil
		}
		return *m, m.requestClose()
	}

	if m.sidebarHidden {
		// Narrow mode: pane always focused; h/l page sections at root.
		m.paneFocused = true
		if !editing && m.activePane().atRoot() {
			switch ks {
			case "l":
				m.cursor = (m.cursor + 1) % len(m.specs)
				return *m, nil
			case "h":
				m.cursor = (m.cursor - 1 + len(m.specs)) % len(m.specs)
				return *m, nil
			}
		}
		return *m, m.activePane().Update(msg)
	}

	if !m.paneFocused {
		switch ks {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.specs)-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.specs) - 1
		case "enter", "l", "right", "tab":
			m.paneFocused = true
		}
		return *m, nil
	}

	// Pane focused: h / shift+tab return to the sidebar unless the cursor
	// row is an enum (which consumes ←/→ but not h) or an edit is open.
	if !editing {
		switch ks {
		case "h", "shift+tab":
			m.paneFocused = false
			return *m, nil
		}
	}
	return *m, m.activePane().Update(msg)
}

// requestClose is the top-level Esc: confirm when dirty, else cancel out.
func (m *Model) requestClose() tea.Cmd {
	if m.dirty() && !m.pendingCancel {
		m.pendingCancel = true
		return nil
	}
	m.pendingCancel = false
	return func() tea.Msg { return CancelledMsg{} }
}

func (m *Model) saveCmd() tea.Cmd {
	if err := config.SaveProjectConfig(m.projectCfgPath, m.state.cfg); err != nil {
		m.footerMsg = fmt.Sprintf("Save failed: %v", err)
		return nil
	}
	loaded, err := config.Load(config.LoadOptions{WorkingDir: m.workingDir})
	if err != nil {
		m.footerMsg = fmt.Sprintf("Reload failed: %v", err)
		return nil
	}
	m.pendingCancel = false
	m.savedFlash = true
	return func() tea.Msg { return SavedMsg{Cfg: loaded} }
}

// openSearch / updateSearch are completed in Task 11; stubs keep this task
// compiling.
func (m *Model) openSearch()                       {}
func (m *Model) updateSearch(tea.KeyPressMsg) tea.Cmd { return nil }

type searchState struct{} // replaced in Task 11

var (
	sidebarItemStyle   = lipgloss.NewStyle().Foreground(settingsTheme.FGDefault)
	sidebarActiveStyle = lipgloss.NewStyle().Bold(true).Background(settingsTheme.BGSelection)
	warnStyle          = lipgloss.NewStyle().Foreground(settingsTheme.StatusWarning)
	successStyle       = lipgloss.NewStyle().Foreground(settingsTheme.StatusSuccess)
	errStyle           = lipgloss.NewStyle().Foreground(settingsTheme.StatusError)
	footerKeyStyle     = lipgloss.NewStyle().Foreground(settingsTheme.AccentPrimary)
	footerTextStyle    = lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
)

func (m Model) View() string {
	fw, fh := m.frameSize()
	body := m.renderBody(fw, fh-1)
	footer := m.renderFooter(fw)
	out := body + "\n" + footer
	if m.overlay == overlayHelp {
		return m.helpOverlay(fw, fh)
	}
	if m.overlay == overlaySearch {
		return m.searchOverlay(fw, fh)
	}
	return out
}

func (m Model) renderBody(fw, fh int) string {
	pane := m.activePane()
	title := pane.breadcrumb(m.activeSectionTitle())
	if m.sidebarHidden {
		title = "‹ " + title + " ›"
	}
	var content strings.Builder
	if warns := warningsFor(m.specs[m.cursor].id, m.state.cfg); len(warns) > 0 {
		content.WriteString(warnStyle.Render("⚠ "+strings.Join(warns, " · ")) + "\n")
	}
	content.WriteString(pane.top().list.View())

	if m.sidebarHidden {
		return renderPanel(title, content.String(), fw, fh, true)
	}

	sidebarTitle := "Settings"
	if m.dirty() {
		sidebarTitle = "Settings " + warnStyle.Render("●")
	}
	var sb strings.Builder
	for i, sp := range m.specs {
		label := "  " + sp.title
		if i == m.cursor {
			label = sidebarActiveStyle.Render("▸ " + sp.title)
		} else {
			label = sidebarItemStyle.Render(label)
		}
		sb.WriteString(label + "\n")
	}
	sidebar := renderPanel(sidebarTitle, strings.TrimRight(sb.String(), "\n"),
		sidebarWidth, fh, !m.paneFocused)
	detail := renderPanel(title, content.String(), fw-sidebarWidth, fh, m.paneFocused)
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, detail)
}

// renderFooter shows only what is actionable right now.
func (m Model) renderFooter(fw int) string {
	seg := func(k, label string) string {
		return footerKeyStyle.Render("["+k+"]") + footerTextStyle.Render(label)
	}
	var parts []string
	switch {
	case m.pendingCancel:
		return ansi.Cut(warnStyle.Render("⚠ unsaved changes — Esc again to discard, Ctrl+S to save"), 0, max(fw, 1))
	case m.footerMsg != "":
		return ansi.Cut(errStyle.Render(m.footerMsg), 0, max(fw, 1))
	case m.savedFlash:
		return ansi.Cut(successStyle.Render("✓ saved"), 0, max(fw, 1))
	}
	fl := m.activePane().top().list
	switch {
	case fl.adding || fl.editing:
		parts = []string{seg("↵", "apply"), seg("Esc", "cancel")}
	case fl.picking:
		parts = []string{seg("j/k", "choose"), seg("↵", "apply"), seg("Esc", "cancel")}
	case !m.paneFocused && !m.sidebarHidden:
		parts = []string{seg("j/k", "move"), seg("↵", "open"), seg("/", "search"), seg("^S", "save"), seg("Esc", "close"), seg("?", "help")}
	default:
		parts = []string{seg("j/k", "move")}
		if row := fl.CursorRow(); row != nil {
			switch row.kind {
			case kindToggle:
				parts = append(parts, seg("Space", "toggle"))
			case kindScalar:
				if row.setStr != nil {
					parts = append(parts, seg("↵", "edit"))
				}
				if row.masked {
					parts = append(parts, seg("d", "clear"))
				}
			case kindEnum:
				parts = append(parts, seg("←/→", "cycle"), seg("↵", "pick"))
			case kindDrill:
				parts = append(parts, seg("↵", "open"))
				if row.del != nil {
					parts = append(parts, seg("d", "delete"))
				}
			}
		}
		if fl.onAdd != nil {
			parts = append(parts, seg("a", "add"))
		}
		if m.sidebarHidden && m.activePane().atRoot() {
			parts = append(parts, seg("h/l", "section"))
		} else if !m.sidebarHidden {
			parts = append(parts, seg("h", "sidebar"))
		}
		parts = append(parts, seg("/", "search"), seg("^S", "save"), seg("?", "help"))
	}
	if m.dirty() {
		parts = append([]string{warnStyle.Render("● unsaved")}, parts...)
	}
	return ansi.Cut(" "+strings.Join(parts, " "), 0, max(fw, 1))
}

// helpOverlay / searchOverlay are completed in Tasks 11–12.
func (m Model) helpOverlay(fw, fh int) string   { return "" }
func (m Model) searchOverlay(fw, fh int) string { return "" }

func (m Model) FocusedFieldTitle() string {
	if m.paneFocused || m.sidebarHidden {
		if row := m.activePane().top().list.CursorRow(); row != nil {
			return row.title
		}
	}
	return m.activeSectionTitle()
}

func (m Model) Footer() string { return m.footerMsg }

// BoolValue returns the current value of a named boolean settings field,
// read straight from the working copy. Convenience for tests and the parent
// status line.
func (m Model) BoolValue(title string) bool {
	switch title {
	case "Local only":
		if p, ok := m.state.cfg.Models.Presets[activePresetNameFor(m.state.cfg)]; ok {
			return p.LocalOnly
		}
		return false
	case "Remote providers allowed":
		return m.state.cfg.Privacy.RemoteProvidersAllowed
	case "Allow network":
		return m.state.cfg.Tools.Shell.AllowNetwork
	case "Allow sudo":
		return m.state.cfg.Tools.Shell.AllowSudo
	case "Allow destructive":
		return m.state.cfg.Tools.Shell.AllowDestructive
	case "Auto-approve shell":
		return m.state.cfg.Tools.Shell.AutoApprove
	}
	return false
}

// activePresetNameFor resolves the implementer preset of the default profile
// (same rule as config.activePresetName, duplicated here because that helper
// is package-private to config).
func activePresetNameFor(cfg config.Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	return profile.Roles[routing.RoleImplementer]
}
```

Two behavior notes baked in above:
- `saveCmd` runs save/reload synchronously in Update (so `footerMsg` mutation lands on the model, not a stale copy) and returns the `SavedMsg` as a command. This differs from the legacy closure-over-pointer approach, which mutated a Model that Bubble Tea had already copied — a latent bug we're not preserving.
- Ctrl+O is handled here as a close request; Task 13 removes the parent's interception so unsaved changes get the confirm flow instead of being silently discarded.

- [ ] **Step 5: Delete the legacy files and fix `masked.go` / `intField2` rename**

```bash
cd internal/app/tui/settings
git rm pane.go mixed.go mixed_test.go composite.go composite_test.go collection.go collection_test.go \
  scalar.go liststrings.go liststrings_test.go mapeditor.go mapeditor_test.go \
  section_agent.go section_agent_test.go section_commands.go section_diagnostics.go section_diagnostics_test.go \
  section_hooks.go section_hooks_test.go section_indexing.go section_mcp.go section_mcp_test.go \
  section_permissions.go section_permissions_test.go section_presets.go section_presets_test.go \
  section_privacy.go section_providers.go section_providers_test.go section_sandbox.go section_shell.go \
  section_shell_test.go section_snapshots.go section_swarm.go section_swarm_test.go section_web.go \
  section_scalar_test.go skeleton_test.go integration_test.go
```

In `masked.go`: delete the `secretField` function and the `charm.land/huh/v2` import; keep `maskKey` and its doc comment. In `setters.go`: rename `intField2` → `intField` (now that legacy `numField` is gone) and update every call site: `grep -rn "intField2" internal/app/tui/settings/ | cut -d: -f1 | sort -u | xargs sed -i '' 's/intField2/intField/g'`.

Also delete `internal/app/tui/settings/masked_test.go` ONLY if it tests `secretField`; if it tests `maskKey`, keep it (check first — it currently tests `maskKey`, so keep it).

- [ ] **Step 6: Run the package tests**

Run: `go test ./internal/app/tui/settings/ -v 2>&1 | tail -40`
Expected: PASS. Iterate on compile errors — this is the switch-over commit, expect a few rounds.

- [ ] **Step 7: Run the full TUI tree and build**

Run: `go build ./cmd/marshal && go test ./internal/app/tui/...`
Expected: PASS. `internal/app/tui/model_test.go` / `view_test.go` may assert old settings strings (e.g. footer text) — update those assertions to the new UI, changing expectations only, not parent behavior.

- [ ] **Step 8: Commit**

```bash
gofmt -w .
git add -A internal/app/tui/
git commit -m "feat(settings): switch Model to bordered two-pane fieldList engine, drop huh and legacy editors"
```

---

### Task 11: global search — registry, overlay, jump

**Files:**
- Create: `internal/app/tui/settings/search.go`
- Test: `internal/app/tui/settings/search_test.go`
- Modify: `internal/app/tui/settings/model.go` (replace the `searchState` stub, `openSearch`, `updateSearch`, `searchOverlay`)

**Interfaces:**
- Consumes: `Model.specs`, `Model.panes` (root frames), `renderPanel`, `fieldList.SetCursor`.
- Produces:
  - `type searchHit struct { sectionIdx int; fieldID, sectionTitle, fieldTitle string; keywords []string }`
  - `buildRegistry(specs []sectionSpec, panes []*paneStack) []searchHit` — walks each root frame's top-level rows.
  - `fuzzyFilter(hits []searchHit, query string) []searchHit` — case-insensitive; substring matches rank before subsequence matches.
  - `searchState { input textinput.Model; registry, results []searchHit; cursor int }`

- [ ] **Step 1: Write the failing tests**

```go
package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestRegistryCoversEverySection(t *testing.T) {
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	hits := buildRegistry(m.specs, m.panes)
	seen := map[int]bool{}
	for _, h := range hits {
		seen[h.sectionIdx] = true
	}
	for i, sp := range m.specs {
		if !seen[i] {
			t.Errorf("section %q registered no searchable fields", sp.title)
		}
	}
}

func TestFuzzyFilterRanksSubstringFirst(t *testing.T) {
	hits := []searchHit{
		{fieldTitle: "Max output bytes"},
		{fieldTitle: "Allow network"},
		{fieldTitle: "All own kettle"}, // subsequence match for "allow ne"
	}
	got := fuzzyFilter(hits, "allow ne")
	if len(got) == 0 || got[0].fieldTitle != "Allow network" {
		t.Fatalf("substring match should rank first, got %v", got)
	}
}

func TestSearchJumpLandsOnField(t *testing.T) {
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if m.overlay != overlaySearch {
		t.Fatal("/ should open the search overlay")
	}
	for _, r := range "allow network" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.overlay != overlayNone {
		t.Fatal("enter should close the overlay")
	}
	if m.specs[m.cursor].id != "shell" {
		t.Fatalf("jump should land on shell section, got %q", m.specs[m.cursor].id)
	}
	if !m.paneFocused {
		t.Fatal("jump should focus the pane")
	}
	if got := m.activePane().top().list.CursorRow().title; got != "Allow network" {
		t.Fatalf("jump should land on the field, got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/tui/settings/ -run 'TestRegistry|TestFuzzy|TestSearchJump' -v`
Expected: FAIL — `undefined: buildRegistry`, `undefined: searchHit`

- [ ] **Step 3: Write `search.go` and wire the Model**

`internal/app/tui/settings/search.go`:

```go
package settings

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type searchHit struct {
	sectionIdx   int
	fieldID      string
	sectionTitle string
	fieldTitle   string
	keywords     []string
}

// buildRegistry walks every section's ROOT frame rows. Nested drill frames
// are intentionally not indexed — the row that leads to them is. Sections
// whose root is an empty collection (e.g. Providers/Hooks with a default
// config) register a section-level hit so they stay findable; its fieldID
// is empty and jumpTo just opens the section.
func buildRegistry(specs []sectionSpec, panes []*paneStack) []searchHit {
	var out []searchHit
	for i, sp := range specs {
		rows := panes[i].stack[0].list.Rows()
		if len(rows) == 0 {
			out = append(out, searchHit{sectionIdx: i, sectionTitle: sp.title, fieldTitle: sp.title})
			continue
		}
		for _, f := range rows {
			out = append(out, searchHit{
				sectionIdx: i, fieldID: f.id,
				sectionTitle: sp.title, fieldTitle: f.title,
				keywords: f.keywords,
			})
		}
	}
	return out
}

// fuzzyFilter matches query case-insensitively against "section field
// keywords". Substring hits rank before subsequence hits.
func fuzzyFilter(hits []searchHit, query string) []searchHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return hits
	}
	var sub, seq []searchHit
	for _, h := range hits {
		hay := strings.ToLower(h.sectionTitle + " " + h.fieldTitle + " " + strings.Join(h.keywords, " "))
		if strings.Contains(hay, q) {
			sub = append(sub, h)
		} else if isSubsequence(q, hay) {
			seq = append(seq, h)
		}
	}
	return append(sub, seq...)
}

func isSubsequence(needle, hay string) bool {
	i := 0
	for _, c := range hay {
		if i < len(needle) && rune(needle[i]) == c {
			i++
		}
	}
	return i == len(needle)
}

type searchState struct {
	input    textinput.Model
	registry []searchHit
	results  []searchHit
	cursor   int
}

func (m *Model) openSearch() {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	reg := buildRegistry(m.specs, m.panes)
	m.search = searchState{input: ti, registry: reg, results: reg}
	m.overlay = overlaySearch
}

func (m *Model) updateSearch(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.overlay = overlayNone
		return nil
	case "up", "ctrl+k":
		if m.search.cursor > 0 {
			m.search.cursor--
		}
		return nil
	case "down", "ctrl+j", "tab":
		if m.search.cursor < len(m.search.results)-1 {
			m.search.cursor++
		}
		return nil
	case "enter":
		if m.search.cursor < len(m.search.results) {
			m.jumpTo(m.search.results[m.search.cursor])
		}
		m.overlay = overlayNone
		return nil
	}
	var cmd tea.Cmd
	m.search.input, cmd = m.search.input.Update(k)
	m.search.results = fuzzyFilter(m.search.registry, m.search.input.Value())
	m.search.cursor = 0
	return cmd
}

func (m *Model) jumpTo(h searchHit) {
	m.cursor = h.sectionIdx
	pane := m.activePane()
	for pane.pop() {
	} // reset to root
	m.paneFocused = true
	if h.fieldID == "" {
		return // section-level hit: opening the section is the jump
	}
	for i, f := range pane.top().list.Rows() {
		if f.id == h.fieldID {
			pane.top().list.SetCursor(i)
			return
		}
	}
}

func (m Model) searchOverlay(fw, fh int) string {
	var b strings.Builder
	b.WriteString("/ " + m.search.input.View() + "\n")
	b.WriteString(footerTextStyle.Render(strings.Repeat("─", max(fw/2-2, 10))) + "\n")
	maxRows := min(len(m.search.results), fh-6)
	for i := 0; i < maxRows; i++ {
		h := m.search.results[i]
		marker := "  "
		line := h.sectionTitle + " › " + h.fieldTitle
		if i == m.search.cursor {
			marker = "▸ "
			line = sidebarActiveStyle.Render(line)
		}
		b.WriteString(marker + line + "\n")
	}
	if len(m.search.results) == 0 {
		b.WriteString(footerTextStyle.Render("  no matches") + "\n")
	}
	panel := renderPanel("Jump to setting", strings.TrimRight(b.String(), "\n"),
		max(fw/2, 40), min(fh, maxRows+5), true)
	return lipgloss.NewStyle().Width(fw).Height(fh).Align(lipgloss.Center, lipgloss.Center).Render(panel)
}
```

In `model.go`, delete the three stubs (`openSearch`, `updateSearch`, `type searchState struct{}`) and the `searchOverlay` stub.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestRegistry|TestFuzzy|TestSearchJump' -v && go test ./internal/app/tui/settings/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/search.go internal/app/tui/settings/search_test.go internal/app/tui/settings/model.go
git commit -m "feat(settings): global / search — field registry, fuzzy overlay, jump-to-field"
```

---

### Task 12: `?` help overlay

**Files:**
- Create: `internal/app/tui/settings/help.go`
- Test: `internal/app/tui/settings/help_test.go`
- Modify: `internal/app/tui/settings/model.go` (replace `helpOverlay` stub)

**Interfaces:**
- Consumes: `renderPanel`, `Model` focus state.
- Produces: `(m Model) helpOverlay(fw, fh int) string` — centered bordered panel listing the full keymap.

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestHelpOverlayOpensAndCloses(t *testing.T) {
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)
	m, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	v := m.View()
	for _, want := range []string{"Settings keys", "search", "toggle", "save"} {
		if !strings.Contains(v, want) {
			t.Fatalf("help overlay missing %q:\n%s", want, v)
		}
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.overlay != overlayNone {
		t.Fatal("esc should close help")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/tui/settings/ -run 'TestHelpOverlay' -v`
Expected: FAIL (stub returns "")

- [ ] **Step 3: Write `help.go`**

```go
package settings

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func (m Model) helpOverlay(fw, fh int) string {
	lines := []string{
		"Settings keys",
		"",
		"  j/k or ↑/↓     move",
		"  g / G          first / last",
		"  Enter          open · edit · drill in",
		"  Space          toggle on/off",
		"  ←/→            cycle enum values",
		"  a / d          add / delete entry",
		"  h / Shift+Tab  back to sidebar",
		"  Esc            up one level · discard edit",
		"  /              search all settings",
		"  Ctrl+S         save",
		"  ?              close this help",
	}
	if m.sidebarHidden {
		lines = append(lines, "", "  h / l          previous / next section")
	}
	panel := renderPanel("Help", strings.Join(lines, "\n"), max(fw/2, 44), min(fh, len(lines)+4), true)
	return lipgloss.NewStyle().Width(fw).Height(fh).Align(lipgloss.Center, lipgloss.Center).Render(panel)
}
```

Delete the `helpOverlay` stub from `model.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/settings/ -run 'TestHelpOverlay' -v && go test ./internal/app/tui/settings/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/help.go internal/app/tui/settings/help_test.go internal/app/tui/settings/model.go
git commit -m "feat(settings): ? help overlay panel"
```

---

### Task 13: parent integration, NO_COLOR check, full verification

**Files:**
- Modify: `internal/app/tui/model.go:439-448` (remove the Ctrl+O interception — settings now handles it with the unsaved-confirm flow)
- Test: `internal/app/tui/settings/nocolor_test.go`
- Modify (if needed): `internal/app/tui/model_test.go`, `internal/app/tui/view_test.go` assertions

- [ ] **Step 1: Remove the parent Ctrl+O interception**

In `internal/app/tui/model.go`, change:

```go
	if m.settingsOpen {
		// Ctrl+O toggles the overlay closed.
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+o" {
			m.settingsOpen = false
			return m, nil
		}
		var cmd tea.Cmd
		m.settingsModel, cmd = m.settingsModel.Update(msg)
		return m, cmd
	}
```

to:

```go
	if m.settingsOpen {
		// Ctrl+O is handled inside settings as a close request so unsaved
		// changes get the confirm flow; closure arrives via CancelledMsg.
		var cmd tea.Cmd
		m.settingsModel, cmd = m.settingsModel.Update(msg)
		return m, cmd
	}
```

Also update the now-stale comment above the switch ("huh's internal navigation messages…") to say the settings model consumes key messages only.

- [ ] **Step 2: Write the NO_COLOR regression test**

`internal/app/tui/settings/nocolor_test.go`:

```go
package settings

import (
	"strings"
	"testing"
)

// The design system requires the UI to stay usable with color stripped.
// settingsTheme is resolved at package init, so this test asserts structure
// survives ansi-stripping rather than re-resolving the theme: the cursor
// marker, borders, and toggle glyphs must all be plain text.
func TestViewUsableWhenColorStripped(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	plain := stripANSI(v)
	for _, want := range []string{"▸", "╭", "on", "off"} {
		if !strings.Contains(plain, want) && !strings.Contains(plain, "●") {
			t.Fatalf("structure marker %q must survive color stripping:\n%s", want, plain)
		}
	}
}
```

Use `ansi.Strip` from `github.com/charmbracelet/x/ansi` for `stripANSI` (alias it or call directly).

- [ ] **Step 3: Full verification**

```bash
gofmt -l . | tee /dev/stderr | wc -l   # expect 0
go vet ./...
go build ./cmd/marshal
go test ./...
```

Expected: all pass. Fix anything that fails before committing.

- [ ] **Step 4: Manual smoke test**

Run `go run ./cmd/marshal` in a scratch directory, press Ctrl+O, and walk the checklist:
- borders render; focused panel border is accent-colored
- Tab/Enter into pane, h back; Esc walks up levels; double-Esc confirm when dirty
- `/` search jumps to "Allow network"; `?` overlay opens/closes
- resize below 70 cols → paging mode; resize tiny → no crash
- Ctrl+S saves and flashes ✓; Ctrl+O with unsaved changes asks first

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(settings): parent ctrl+o routes through unsaved-confirm; NO_COLOR structure test"
```

---

## Self-Review (completed during planning)

- **Spec coverage:** §1 layout → Tasks 5, 10. §2 focus/keys → Tasks 1, 10. §3 unified widget → Tasks 1–4, 6–9. §4 search → Task 11. §5 footer/help/save → Tasks 10, 12. §6 architecture/deletions → Task 10. §7 testing → every task + Task 13. Spec's `search.go`/`fieldlist.go`/`field.go` file names kept; `panestack.go`, `chrome.go`, `setters.go`, `frames_*.go` refine the spec's "section_*.go stay" — section files are replaced by consolidated `frames_*.go` files (fewer, focused files; same declarative intent).
- **Deviations recorded:** immediate-apply in drill frames (Global Constraints, agreed); overlays render as centered panels rather than compositing over a dimmed frame (matches existing codebase overlay pattern); mapEditor's two-phase add becomes add-key-then-edit-value; legacy `saveCmd` pointer-mutation bug not preserved.
- **Type consistency:** `frame`/`fieldList`/`paneStack` names and signatures checked across tasks; `intField2`→`intField` rename is explicit in Task 10 Step 5.
