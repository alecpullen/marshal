# Command Picker Modals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Interactive centered-modal pickers for `/model`, `/rewind`, `/branches`, and a new `/mode` command, per `docs/superpowers/specs/2026-07-11-command-picker-modals-design.md`.

**Architecture:** Promote three helpers built by the settings redesign (`renderPanel`, `clipLines`, fuzzy matching) into shared `internal/app/tui/chrome` and `internal/app/tui/fuzzy` packages, add a line-splicing `chrome.Overlay` compositor, and build one `internal/app/tui/picker` component on top. The TUI opens pickers from `dispatchCommand`; a picked value re-enters the existing command path, so all command semantics stay in one place.

**Tech Stack:** Go, Bubble Tea v2 (`charm.land/bubbletea/v2`), `charm.land/bubbles/v2/textinput`, `charm.land/lipgloss/v2`, `github.com/charmbracelet/x/ansi`, `internal/app/tui/theme`.

## Global Constraints

- **Prerequisite:** the settings redesign plan (`docs/superpowers/plans/2026-07-11-settings-tui-redesign.md`) must be fully executed first. Tasks 1–2 move code that plan creates (`internal/app/tui/settings/chrome.go`, `fieldlist.go` `clipLines`, `search.go` `fuzzyFilter`/`isSubsequence`). Before starting, verify those exist: `grep -n "func renderPanel" internal/app/tui/settings/chrome.go`.
- **As-built beats plan snippets:** the settings code shown here is what its plan specified. If the as-built code differs (names, params), adapt mechanically — keep the as-built behavior, move it, and keep the settings tests green.
- Colors ONLY via `internal/app/tui/theme` slots. Imports use `charm.land/...` v2 paths. Never bind `Ctrl+C`/`Ctrl+Z`.
- Deviation from spec §4 (recorded): no `pickerAction func(value string) tea.Cmd` closure. `Model.Update` is a value receiver, so a closure built inside `dispatchCommand` would capture a `*Model` whose copy is stale by the time `PickedMsg` arrives. Instead the Model stores `pickerCommand string` and the `PickedMsg` handler re-enters `dispatchCommand` (or calls `switchModelPreset` for `/model`, whose preset names may contain spaces that `strings.Fields` would split).
- `/model` stays session-only; command registry contract (`Handler func(state, args) string`) unchanged; `/ask`/`/edit`/`/auto` remain registered and untouched.
- Tests: `go test ./internal/app/tui/...` green at the end of every task; `gofmt -w .` before each commit. Build needs `CGO_ENABLED=1`.

---

### Task 1: `internal/app/tui/fuzzy` — extract the matcher

**Files:**
- Create: `internal/app/tui/fuzzy/fuzzy.go`
- Create: `internal/app/tui/fuzzy/fuzzy_test.go`
- Modify: `internal/app/tui/settings/search.go` (replace `fuzzyFilter` internals, delete `isSubsequence`)

**Interfaces:**
- Consumes: nothing (leaf package).
- Produces: `fuzzy.Rank(query string, haystacks []string) []int` — indices of matching haystacks, best first: case-insensitive substring hits, then subsequence hits. Empty/whitespace query returns all indices in original order.

- [ ] **Step 1: Write the failing test**

`internal/app/tui/fuzzy/fuzzy_test.go`:

```go
package fuzzy

import (
	"slices"
	"testing"
)

func TestRankEmptyQueryReturnsAllInOrder(t *testing.T) {
	got := Rank("  ", []string{"b", "a", "c"})
	if !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("empty query should keep order, got %v", got)
	}
}

func TestRankSubstringBeforeSubsequence(t *testing.T) {
	hay := []string{
		"Max output bytes", // no match for "allow ne"
		"All own kettle",   // subsequence match
		"Allow network",    // substring match
	}
	got := Rank("allow ne", hay)
	if !slices.Equal(got, []int{2, 1}) {
		t.Fatalf("want [2 1] (substring first, no-match dropped), got %v", got)
	}
}

func TestRankCaseInsensitive(t *testing.T) {
	got := Rank("ALLOW", []string{"allow network"})
	if !slices.Equal(got, []int{0}) {
		t.Fatalf("matching must be case-insensitive, got %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/fuzzy/ -v`
Expected: FAIL — package doesn't exist / `undefined: Rank`

- [ ] **Step 3: Write the implementation**

`internal/app/tui/fuzzy/fuzzy.go` (logic lifted from `settings/search.go` `fuzzyFilter`/`isSubsequence` — if the as-built settings versions differ, port the as-built logic):

```go
// Package fuzzy provides the shared filter-as-you-type matcher used by the
// settings search overlay and command picker modals.
package fuzzy

import "strings"

// Rank returns the indices of haystacks matching query, best first:
// case-insensitive substring matches rank before subsequence matches.
// An empty (or whitespace) query matches everything in original order.
func Rank(query string, haystacks []string) []int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		out := make([]int, len(haystacks))
		for i := range out {
			out[i] = i
		}
		return out
	}
	var sub, seq []int
	for i, h := range haystacks {
		hay := strings.ToLower(h)
		if strings.Contains(hay, q) {
			sub = append(sub, i)
		} else if isSubsequence(q, hay) {
			seq = append(seq, i)
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
```

- [ ] **Step 4: Point settings at the shared package**

In `internal/app/tui/settings/search.go`: delete `isSubsequence` and rewrite `fuzzyFilter` as a thin adapter (keep its signature so settings tests don't change):

```go
// fuzzyFilter matches query against "section field keywords" via the shared
// fuzzy.Rank matcher.
func fuzzyFilter(hits []searchHit, query string) []searchHit {
	hay := make([]string, len(hits))
	for i, h := range hits {
		hay[i] = h.sectionTitle + " " + h.fieldTitle + " " + strings.Join(h.keywords, " ")
	}
	idx := fuzzy.Rank(query, hay)
	out := make([]searchHit, 0, len(idx))
	for _, i := range idx {
		out = append(out, hits[i])
	}
	return out
}
```

Add `"marshal/internal/app/tui/fuzzy"` to the import block.

- [ ] **Step 5: Run tests to verify pass (both packages)**

Run: `go test ./internal/app/tui/fuzzy/ ./internal/app/tui/settings/ -v -run 'TestRank|TestFuzzy'`
Expected: PASS — including the settings package's existing fuzzy tests.

- [ ] **Step 6: Commit**

```bash
gofmt -w .
git add internal/app/tui/fuzzy/ internal/app/tui/settings/search.go
git commit -m "refactor(tui): extract shared fuzzy.Rank matcher from settings search"
```

---

### Task 2: `internal/app/tui/chrome` — extract `Panel` and `ClipLines`

**Files:**
- Create: `internal/app/tui/chrome/chrome.go`
- Create: `internal/app/tui/chrome/chrome_test.go`
- Modify: `internal/app/tui/settings/chrome.go` (becomes a thin wrapper)
- Modify: `internal/app/tui/settings/fieldlist.go` (`clipLines` becomes a wrapper)

**Interfaces:**
- Consumes: `internal/app/tui/theme`.
- Produces:
  - `chrome.Panel(title, content string, w, h int, focused bool, th theme.Theme) string` — the settings `renderPanel` with an explicit theme param.
  - `chrome.ClipLines(lines []string, focusLine, height int, th theme.Theme) string` — the settings `clipLines` with themed ↑/↓ indicators.

- [ ] **Step 1: Write the failing test**

`internal/app/tui/chrome/chrome_test.go`:

```go
package chrome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func TestPanelEmbedsTitleAndSizes(t *testing.T) {
	out := Panel("Shell", "hello", 30, 6, true, testTheme)
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
}

func TestClipLinesWindowsAroundFocus(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "row"
	}
	out := ClipLines(lines, 29, 8, testTheme)
	got := strings.Split(out, "\n")
	if len(got) > 8 {
		t.Fatalf("must not exceed height 8, got %d lines", len(got))
	}
	if !strings.Contains(out, "↑") {
		t.Fatalf("expected ↑ more indicator when scrolled, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/chrome/ -v`
Expected: FAIL — package doesn't exist

- [ ] **Step 3: Write the implementation**

`internal/app/tui/chrome/chrome.go` — move the bodies of `settings.renderPanel` (from `internal/app/tui/settings/chrome.go`) and `settings.clipLines` (from `internal/app/tui/settings/fieldlist.go`) verbatim, with these mechanical changes: exported names, `th theme.Theme` parameter replacing the `settingsTheme` package var, and the `flMoreStyle` reference replaced by a local style. Expected result (adapt to as-built bodies if they drifted):

```go
// Package chrome provides shared TUI dressing: bordered panels with
// embedded titles, focus-aware border colors, line windowing, and overlay
// compositing. Extracted from the settings TUI so pickers and overlays
// render consistently.
package chrome

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// Panel draws a rounded-border box with the title embedded in the top
// border. The border uses accent.primary when focused, border.muted when
// not.
func Panel(title, content string, w, h int, focused bool, th theme.Theme) string {
	borderColor := th.BorderMuted
	titleStyle := lipgloss.NewStyle().Foreground(th.FGMuted)
	if focused {
		borderColor = th.AccentPrimary
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(th.AccentPrimary)
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

// ClipLines windows lines to at most height rows, keeping focusLine
// visible, with ↑/↓ more indicators occupying the first/last row when
// clipped.
func ClipLines(lines []string, focusLine, height int, th theme.Theme) string {
	if height <= 0 || len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	more := lipgloss.NewStyle().Foreground(th.FGMuted)
	inner := height - 2
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
		out = append(out, more.Render("  ↑ more"))
	} else {
		out = append(out, "")
	}
	out = append(out, lines[start:start+inner]...)
	if start+inner < len(lines) {
		out = append(out, more.Render("  ↓ more"))
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Turn the settings copies into wrappers**

`internal/app/tui/settings/chrome.go` becomes exactly:

```go
package settings

import "marshal/internal/app/tui/chrome"

// renderPanel draws a bordered panel via the shared chrome package with
// this package's theme.
func renderPanel(title, content string, w, h int, focused bool) string {
	return chrome.Panel(title, content, w, h, focused, settingsTheme)
}
```

In `internal/app/tui/settings/fieldlist.go`: delete the `clipLines` function body and replace with:

```go
func clipLines(lines []string, focusLine, height int) string {
	return chrome.ClipLines(lines, focusLine, height, settingsTheme)
}
```

Add the `chrome` import; remove now-unused imports (`flMoreStyle` stays only if other code uses it — delete it if orphaned). Move any tests that covered `renderPanel`/`clipLines` behavior in `settings/chrome_test.go` to assert through the wrapper (they should pass unchanged since the wrapper preserves behavior).

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./internal/app/tui/chrome/ ./internal/app/tui/settings/`
Expected: PASS — settings visual tests unchanged.

- [ ] **Step 6: Commit**

```bash
gofmt -w .
git add internal/app/tui/chrome/ internal/app/tui/settings/
git commit -m "refactor(tui): extract chrome.Panel and chrome.ClipLines from settings"
```

---

### Task 3: `chrome.Overlay` — line-splicing compositor

**Files:**
- Modify: `internal/app/tui/chrome/chrome.go`
- Modify: `internal/app/tui/chrome/chrome_test.go`

**Interfaces:**
- Produces: `chrome.Overlay(bg, panel string, width, height int) string` — splices `panel` into `bg`, centered; background remains visible around it. Styled (SGR-containing) lines survive the cut.

- [ ] **Step 1: Write the failing test**

Append to `chrome_test.go`:

```go
func TestOverlayCentersPanelOverBackground(t *testing.T) {
	bgLine := strings.Repeat("x", 40)
	bg := strings.Join([]string{bgLine, bgLine, bgLine, bgLine, bgLine}, "\n")
	panel := "PAN\nEL!"
	out := Overlay(bg, panel, 40, 5)
	lines := strings.Split(ansi.Strip(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
	// panel rows land centered: y = (5-2)/2 = 1, x = (40-3)/2 = 18
	if !strings.Contains(lines[1], "PAN") || !strings.Contains(lines[2], "EL!") {
		t.Fatalf("panel not spliced in:\n%s", ansi.Strip(out))
	}
	if !strings.HasPrefix(lines[1], strings.Repeat("x", 18)) {
		t.Fatalf("background left of panel should survive, got %q", lines[1])
	}
	if !strings.HasSuffix(lines[1], strings.Repeat("x", 19)) {
		t.Fatalf("background right of panel should survive, got %q", lines[1])
	}
	if lines[0] != bgLine || lines[4] != bgLine {
		t.Fatalf("rows outside the panel must be untouched")
	}
}

func TestOverlaySurvivesStyledBackground(t *testing.T) {
	styled := "\x1b[31m" + strings.Repeat("r", 40) + "\x1b[0m"
	bg := strings.Join([]string{styled, styled, styled}, "\n")
	out := Overlay(bg, "OK", 40, 3)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "OK") {
		t.Fatalf("panel missing over styled bg:\n%s", plain)
	}
	for i, l := range strings.Split(plain, "\n") {
		if w := len([]rune(l)); w != 40 {
			t.Fatalf("line %d width %d, want 40: %q", i, w, l)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/chrome/ -run 'TestOverlay' -v`
Expected: FAIL — `undefined: Overlay`

- [ ] **Step 3: Write the implementation**

Append to `chrome.go`:

```go
// Overlay splices panel into bg, centered horizontally and vertically.
// Both strings are full rendered views and may contain SGR sequences;
// background lines are cut at column boundaries with ansi.Cut so styling
// survives on both sides of the panel.
func Overlay(bg, panel string, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	pLines := strings.Split(panel, "\n")
	pw := 0
	for _, l := range pLines {
		if w := ansi.StringWidth(l); w > pw {
			pw = w
		}
	}
	x := max((width-pw)/2, 0)
	y := max((height-len(pLines))/2, 0)

	n := max(len(bgLines), y+len(pLines))
	out := make([]string, n)
	for i := 0; i < n; i++ {
		line := ""
		if i < len(bgLines) {
			line = bgLines[i]
		}
		pi := i - y
		if pi >= 0 && pi < len(pLines) {
			p := pLines[pi]
			if pad := pw - ansi.StringWidth(p); pad > 0 {
				p += strings.Repeat(" ", pad)
			}
			left := ansi.Cut(line, 0, x)
			if lw := ansi.StringWidth(left); lw < x {
				left += strings.Repeat(" ", x-lw)
			}
			right := ansi.Cut(line, x+pw, width)
			line = left + p + right
		}
		out[i] = line
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/app/tui/chrome/ -v`
Expected: PASS. If `ansi.Cut`'s bounds semantics differ (it is already used as `ansi.Cut(footer, 0, width)` in `settings/model.go`), adjust the right-side call to match its actual `(s, start, end)` column contract.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/chrome/
git add internal/app/tui/chrome/
git commit -m "feat(tui): chrome.Overlay line-splicing modal compositor"
```

---

### Task 4: picker component

**Files:**
- Create: `internal/app/tui/picker/picker.go`
- Create: `internal/app/tui/picker/picker_test.go`

**Interfaces:**
- Consumes: `chrome.Panel`, `chrome.ClipLines`, `fuzzy.Rank`, `theme`.
- Produces (consumed by Tasks 5–8):
  - `type Item struct { Label, Detail, Badge, Group, Value string }`
  - `type PickedMsg struct{ Value string }`, `type CancelledMsg struct{}`
  - `func New(title, footer string, items []Item) *Model` — cursor starts on the first item whose `Badge` starts with `●`, else the first item. Callers pre-sort items (group order = first appearance).
  - `(*Model) SetFilter(q string)`, `(*Model) Update(tea.Msg) tea.Cmd`, `(*Model) View(maxW, maxH int) string`
  - Test-visible internals (same package): `matches []int`, `cursor int`.

- [ ] **Step 1: Write the failing tests**

`internal/app/tui/picker/picker_test.go`:

```go
package picker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testItems() []Item {
	return []Item{
		{Group: "anthropic", Label: "sonnet", Detail: "anthropic/sonnet-5", Value: "sonnet"},
		{Group: "ollama", Label: "llama-local", Detail: "ollama/llama3.3", Badge: "local", Value: "llama-local"},
		{Group: "ollama", Label: "qwen-coder", Detail: "ollama/qwen2.5", Badge: "● now local", Value: "qwen-coder"},
	}
}

func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestNewStartsOnCurrentBadge(t *testing.T) {
	m := New("Switch model", "", testItems())
	if got := m.items[m.matches[m.cursor]].Value; got != "qwen-coder" {
		t.Fatalf("cursor should start on the ● item, got %q", got)
	}
}

func TestFilterNarrowsAndEnterPicks(t *testing.T) {
	m := New("Switch model", "", testItems())
	for _, r := range "llama" {
		m.Update(key(r))
	}
	if len(m.matches) != 1 {
		t.Fatalf("filter should narrow to 1, got %d", len(m.matches))
	}
	cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should emit a command")
	}
	picked, ok := cmd().(PickedMsg)
	if !ok || picked.Value != "llama-local" {
		t.Fatalf("want PickedMsg{llama-local}, got %#v", cmd())
	}
}

func TestEscCancels(t *testing.T) {
	m := New("x", "", testItems())
	cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc should emit a command")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatalf("want CancelledMsg, got %#v", cmd())
	}
}

func TestEnterOnNoMatchesIsNoop(t *testing.T) {
	m := New("x", "", testItems())
	for _, r := range "zzzzzz" {
		m.Update(key(r))
	}
	if cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter with no matches must be a no-op")
	}
	if !strings.Contains(ansi.Strip(m.View(80, 24)), "no matches") {
		t.Fatal("view should say no matches")
	}
}

func TestViewGroupsWhenUnfilteredFlatWhenFiltered(t *testing.T) {
	m := New("Switch model", "session only", testItems())
	v := ansi.Strip(m.View(80, 24))
	if !strings.Contains(v, "ollama") || !strings.Contains(v, "anthropic") {
		t.Fatalf("unfiltered view should show group headers:\n%s", v)
	}
	if !strings.Contains(v, "session only") {
		t.Fatalf("footer text missing:\n%s", v)
	}
	m.Update(key('q'))
	v = ansi.Strip(m.View(80, 24))
	if strings.Contains(v, "anthropic\n") {
		t.Fatalf("filtered view should be flat (no headers):\n%s", v)
	}
	if !strings.Contains(v, "qwen-coder") {
		t.Fatalf("filtered view should keep matches:\n%s", v)
	}
}

func TestArrowsMoveCursorAndSkipHeaders(t *testing.T) {
	m := New("x", "", testItems())
	m.cursor = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.cursor != 1 {
		t.Fatalf("down should move to next item, got %d", m.cursor)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.cursor != 0 {
		t.Fatalf("up should clamp at 0, got %d", m.cursor)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/picker/ -v`
Expected: FAIL — package doesn't exist

- [ ] **Step 3: Write the implementation**

`internal/app/tui/picker/picker.go`:

```go
// Package picker renders a centered modal selection list with
// filter-as-you-type, used by slash commands (/model, /rewind, /branches,
// /mode). Keys are fzf-style: printable characters edit the filter, ↑/↓
// move, Enter picks, Esc cancels — j/k belong to the filter, not movement.
package picker

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/theme"
)

var th = theme.Load()

var (
	groupStyle  = lipgloss.NewStyle().Foreground(th.AccentPrimary)
	detailStyle = lipgloss.NewStyle().Foreground(th.FGMuted)
	nowStyle    = lipgloss.NewStyle().Foreground(th.AccentPrimary)
	badgeStyle  = lipgloss.NewStyle().Foreground(th.StatusInfo)
	cursorStyle = lipgloss.NewStyle().Bold(true).Background(th.BGSelection)
	mutedStyle  = lipgloss.NewStyle().Foreground(th.FGMuted)
)

// Item is one pickable row.
type Item struct {
	Label  string // primary text, left-aligned
	Detail string // secondary text, right-aligned, muted
	Badge  string // optional tag; "●" prefix marks the current item
	Group  string // optional group header (unfiltered view only)
	Value  string // opaque result delivered in PickedMsg
}

type PickedMsg struct{ Value string }
type CancelledMsg struct{}

type Model struct {
	title   string
	footer  string
	items   []Item
	filter  textinput.Model
	matches []int // indices into items, rank order
	cursor  int   // index into matches
}

func New(title, footer string, items []Item) *Model {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	m := &Model{title: title, footer: footer, items: items, filter: ti}
	m.refilter()
	for pos, idx := range m.matches {
		if strings.HasPrefix(items[idx].Badge, "●") {
			m.cursor = pos
			break
		}
	}
	return m
}

// SetFilter pre-fills the filter (e.g. "/model qw" with no exact match).
func (m *Model) SetFilter(q string) {
	m.filter.SetValue(q)
	m.filter.CursorEnd()
	m.refilter()
}

func (m *Model) refilter() {
	hay := make([]string, len(m.items))
	for i, it := range m.items {
		hay[i] = it.Group + " " + it.Label + " " + it.Detail
	}
	m.matches = fuzzy.Rank(m.filter.Value(), hay)
	if m.cursor >= len(m.matches) {
		m.cursor = max(len(m.matches)-1, 0)
	}
}

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc":
		return func() tea.Msg { return CancelledMsg{} }
	case "enter":
		if m.cursor < len(m.matches) && len(m.matches) > 0 {
			v := m.items[m.matches[m.cursor]].Value
			return func() tea.Msg { return PickedMsg{Value: v} }
		}
		return nil
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		return nil
	case "down":
		if m.cursor < len(m.matches)-1 {
			m.cursor++
		}
		return nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(k)
	m.refilter()
	m.cursor = 0
	return cmd
}

func (m *Model) View(maxW, maxH int) string {
	pw := min(64, maxW-8)
	if pw < 30 {
		pw = max(maxW-2, 30)
	}
	inner := pw - 2

	filtering := strings.TrimSpace(m.filter.Value()) != ""
	var rows []string
	focusLine := 0
	lastGroup := ""
	for pos, idx := range m.matches {
		it := m.items[idx]
		if !filtering && it.Group != "" && it.Group != lastGroup {
			rows = append(rows, groupStyle.Render(it.Group))
			lastGroup = it.Group
		}
		marker := "  "
		if pos == m.cursor {
			marker = "▸ "
			focusLine = len(rows)
		}
		right := detailStyle.Render(it.Detail)
		if it.Badge != "" {
			bs := badgeStyle
			if strings.HasPrefix(it.Badge, "●") {
				bs = nowStyle
			}
			right += " " + bs.Render(it.Badge)
		}
		gap := inner - lipgloss.Width(marker) - lipgloss.Width(it.Label) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		label := it.Label
		if pos == m.cursor {
			label = cursorStyle.Render(label)
		}
		rows = append(rows, marker+label+strings.Repeat(" ", gap)+right)
	}
	if len(m.matches) == 0 {
		rows = append(rows, mutedStyle.Render("  no matches"))
	}

	// panel = filter line + separator + windowed rows + footer
	listH := maxH - 7 // borders(2) + filter + separator + footer + margin(2)
	if listH < 3 {
		listH = 3
	}
	body := chrome.ClipLines(rows, focusLine, listH, th)
	footer := mutedStyle.Render("[↑↓] move [↵] pick [Esc] cancel")
	if m.footer != "" {
		footer += mutedStyle.Render(" · " + m.footer)
	}
	content := "/ " + m.filter.View() + "\n" +
		mutedStyle.Render(strings.Repeat("─", inner)) + "\n" +
		body + "\n" + footer
	ph := min(lipgloss.Height(content)+2, maxH)
	return chrome.Panel(m.title, content, pw, ph, true, th)
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/app/tui/picker/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/picker/
git add internal/app/tui/picker/
git commit -m "feat(tui): picker modal component with fuzzy filter and grouped items"
```

---

### Task 5: TUI plumbing — open/route/close pickers

**Files:**
- Modify: `internal/app/tui/model.go` (struct fields, `Update` routing, `viewString`, `openPicker` helper)
- Test: `internal/app/tui/model_test.go` (append)

**Interfaces:**
- Consumes: `picker.Model`, `picker.PickedMsg`, `picker.CancelledMsg`, `chrome.Overlay`.
- Produces (used by Tasks 6–8):
  - Model fields: `pickerModel *picker.Model`, `pickerCommand string`.
  - `(m *Model) openPicker(cmdName, title, footer string, items []picker.Item, prefilter string)`.
  - `PickedMsg` handling: `/model` value → `m.switchModelPreset(value)` (Task 6 defines it; this task wires the other path), `mode` → `dispatchCommand("/" + value)`, anything else → `dispatchCommand("/" + cmdName + " " + value)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/model_test.go` (follow the existing construction pattern used by `TestSlashCommandsShowSuggestionsAndTabCompletes`):

```go
func TestPickerRoutingOpenPickCancel(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	// open a picker directly via the helper
	m.openPicker("branches", "Switch branch", "", []picker.Item{
		{Label: "branch 1", Value: "1"},
		{Label: "branch 2", Value: "2", Badge: "● now"},
	}, "")
	if m.pickerModel == nil || m.pickerCommand != "branches" {
		t.Fatal("openPicker should set the modal state")
	}

	// the modal renders composited over the normal view
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Switch branch") || !strings.Contains(view, "branch 1") {
		t.Fatalf("picker should render over the view:\n%s", view)
	}

	// keys route to the picker: esc → CancelledMsg → closes
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("esc should produce the picker's cancel command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.pickerModel != nil || m.pickerCommand != "" {
		t.Fatal("CancelledMsg should close the picker")
	}
}
```

Add `"marshal/internal/app/tui/picker"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestPickerRouting' -v`
Expected: FAIL — `undefined: m.openPicker` / no `pickerModel` field

- [ ] **Step 3: Implement the plumbing**

In `internal/app/tui/model.go`:

(a) Add to the `Model` struct (near `settingsModel`):

```go
	pickerModel   *picker.Model
	pickerCommand string // which command opened the modal: "model", "rewind", "branches", "mode"
```

(b) Add the helper (near `dispatchCommand`):

```go
// openPicker opens a command modal. The picked value is delivered as
// picker.PickedMsg and re-enters dispatchCommand for pickerCommand, so
// command semantics stay in one place.
func (m *Model) openPicker(cmdName, title, footer string, items []picker.Item, prefilter string) {
	p := picker.New(title, footer, items)
	if prefilter != "" {
		p.SetFilter(prefilter)
	}
	m.pickerModel = p
	m.pickerCommand = cmdName
}
```

(c) In `Update`, immediately after the `if m.memoryOpen { ... }` block, add:

```go
	switch pm := msg.(type) {
	case picker.PickedMsg:
		cmdName := m.pickerCommand
		m.pickerModel = nil
		m.pickerCommand = ""
		switch {
		case cmdName == "" || pm.Value == "":
			m.refreshViewport()
			return m, nil
		case cmdName == "model":
			// preset names may contain spaces; apply directly instead of
			// round-tripping through the arg splitter
			m.switchModelPreset(pm.Value)
			m.refreshViewport()
			return m, nil
		case cmdName == "mode":
			return m.dispatchCommand("/" + pm.Value)
		default:
			return m.dispatchCommand("/" + cmdName + " " + pm.Value)
		}
	case picker.CancelledMsg:
		m.pickerModel = nil
		m.pickerCommand = ""
		m.refreshViewport()
		return m, nil
	}
	if m.pickerModel != nil {
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return m, m.pickerModel.Update(msg)
		}
		// non-key messages (ticks, agent events) keep flowing to the
		// normal handlers below so background work continues.
	}
```

For this task only, add a temporary `switchModelPreset` stub so the package compiles (Task 6 replaces it with the real extraction):

```go
func (m *Model) switchModelPreset(presetName string) {}
```

(d) In `viewString()` (in `view.go`), change the final return of the normal path:

```go
	rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
	out := lipgloss.JoinVertical(lipgloss.Left, rows...)
	if m.pickerModel != nil {
		return chrome.Overlay(out, m.pickerModel.View(m.width, m.height), m.width, m.height)
	}
	return out
```

Add `"marshal/internal/app/tui/chrome"` and `"marshal/internal/app/tui/picker"` to the respective import blocks.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/app/tui/ -run 'TestPickerRouting' -v && go test ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/model.go internal/app/tui/view.go internal/app/tui/model_test.go
git commit -m "feat(tui): picker modal plumbing — open, route keys, composite over view"
```

---

### Task 6: `/model` picker

**Files:**
- Modify: `internal/app/tui/model.go` (extract `switchModelPreset`, rewrite `case "model"`, add `modelPickerItems`)
- Test: `internal/app/tui/model_test.go` (append)

**Interfaces:**
- Consumes: Task 5 plumbing; existing `case "model"` logic at `internal/app/tui/model.go:1375-1408` (pre-settings-plan line numbers — locate by `case "model":`).
- Produces: `(m *Model) switchModelPreset(presetName string)` (replaces Task 5's stub), `(m *Model) modelPickerItems() []picker.Item`.

- [ ] **Step 1: Write the failing test**

```go
func modelTestState(t *testing.T) *session.State {
	t.Helper()
	cfg := config.Default()
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	cfg.Models.Presets["test-a"] = routing.ModelPreset{Name: "test-a", Provider: "ollama", Model: "qwen2.5", LocalOnly: true}
	cfg.Models.Presets["test-b"] = routing.ModelPreset{Name: "test-b", Provider: "anthropic", Model: "sonnet-5"}
	return session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestModelBareOpensPicker(t *testing.T) {
	m := New(modelTestState(t))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model")
	m = updated.(Model)
	if m.pickerModel == nil || m.pickerCommand != "model" {
		t.Fatal("bare /model should open the picker")
	}
	view := stripANSI(m.View().Content)
	for _, want := range []string{"test-a", "test-b", "ollama/qwen2.5", "local", "session only"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}
}

func TestModelExactArgBypassesPicker(t *testing.T) {
	var reloaded *config.Config
	m := New(modelTestState(t), WithConfigReloader(func(c config.Config) error { reloaded = &c; return nil }))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model test-b")
	m = updated.(Model)
	if m.pickerModel != nil {
		t.Fatal("exact preset arg must switch directly, no picker")
	}
	if reloaded == nil || reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer] != "test-b" {
		t.Fatalf("direct switch should reload with test-b, got %+v", reloaded)
	}
}

func TestModelUnknownArgOpensPrefilteredPicker(t *testing.T) {
	m := New(modelTestState(t))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model test-a-typo-b")
	m = updated.(Model)
	if m.pickerModel == nil {
		t.Fatal("unknown arg should open the picker instead of erroring")
	}
}

func TestModelPickAppliesSessionSwitch(t *testing.T) {
	var reloaded *config.Config
	m := New(modelTestState(t), WithConfigReloader(func(c config.Config) error { reloaded = &c; return nil }))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model")
	m = updated.(Model)
	updated, _ = m.Update(picker.PickedMsg{Value: "test-a"})
	m = updated.(Model)
	if m.pickerModel != nil {
		t.Fatal("pick should close the modal")
	}
	if reloaded == nil || reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer] != "test-a" {
		t.Fatalf("pick should reload with test-a, got %+v", reloaded)
	}
}

func TestModelNoPresetsPointsAtSettings(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model")
	m = updated.(Model)
	if m.pickerModel != nil {
		t.Fatal("no presets: picker must not open")
	}
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "/settings") {
		t.Fatal("should add a system message pointing at /settings")
	}
}
```

Add `"marshal/internal/llm/routing"` to the test imports if absent.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/tui/ -run 'TestModel(Bare|Exact|Unknown|Pick|NoPresets)' -v`
Expected: FAIL — bare `/model` prints usage instead of opening a picker

- [ ] **Step 3: Implement**

(a) Replace Task 5's `switchModelPreset` stub with the extraction of the existing `case "model"` body (move verbatim — this is today's logic, unchanged):

```go
// switchModelPreset applies a session-only model switch by routing every
// role of a synthetic "switched" profile at the preset. Nothing is written
// to config files; /settings owns persistence.
func (m *Model) switchModelPreset(presetName string) {
	if m.configReloader == nil {
		return
	}
	newCfg := m.state.Config
	preset, ok := newCfg.Models.Presets[presetName]
	if !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Unknown preset: %s", presetName), session.ContentTypePlain)
		return
	}
	newCfg.Profile.Default = "switched"
	newCfg.AgentProfiles = map[string]routing.AgentProfile{
		"switched": {
			Name: "switched",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: presetName,
				routing.RoleRepoScout:   presetName,
				routing.RoleKnowledge:   presetName,
			},
		},
	}
	if err := m.configReloader(newCfg); err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to switch model: %v", err), session.ContentTypePlain)
	} else {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Switched to model: %s (%s)", presetName, preset.Model), session.ContentTypePlain)
	}
}
```

(b) Add the items builder (items sorted by provider then name, so group headers appear alphabetically):

```go
func (m *Model) modelPickerItems() []picker.Item {
	presets := m.state.Config.Models.Presets
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		pi, pj := presets[names[i]], presets[names[j]]
		if pi.Provider != pj.Provider {
			return pi.Provider < pj.Provider
		}
		return names[i] < names[j]
	})
	current := m.state.ActiveRoute().Preset
	items := make([]picker.Item, 0, len(names))
	for _, n := range names {
		p := presets[n]
		var badges []string
		if n == current {
			badges = append(badges, "● now")
		}
		if p.LocalOnly {
			badges = append(badges, "local")
		}
		items = append(items, picker.Item{
			Group:  p.Provider,
			Label:  n,
			Detail: p.Provider + "/" + p.Model,
			Badge:  strings.Join(badges, " "),
			Value:  n,
		})
	}
	return items
}
```

(c) Replace the entire `case "model":` body in `dispatchCommand`:

```go
	case "model":
		presets := m.state.Config.Models.Presets
		if len(presets) == 0 {
			m.state.AddMessage(session.RoleSystem, "No model presets configured. Add one in /settings → Model Presets.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		if len(args) > 0 {
			if _, ok := presets[args[0]]; ok {
				m.switchModelPreset(args[0])
				m.refreshViewport()
				return m, nil
			}
		}
		// bare, or an argument that doesn't resolve: open the picker,
		// pre-filtered with whatever was typed
		m.openPicker("model", "Switch model", "session only — /settings to persist",
			m.modelPickerItems(), strings.Join(args, " "))
		m.refreshViewport()
		return m, nil
```

Ensure `"sort"` is imported.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/app/tui/ -run 'TestModel(Bare|Exact|Unknown|Pick|NoPresets)|TestPickerRouting' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): /model opens a grouped preset picker; exact args still switch directly"
```

---

### Task 7: `/rewind` and `/branches` pickers

**Files:**
- Modify: `internal/app/tui/model.go` (`dispatchCommand` pre-handler interception + item builders)
- Test: `internal/app/tui/model_test.go` (append)

**Interfaces:**
- Consumes: Task 5 plumbing (`PickedMsg` for these commands re-enters `dispatchCommand("/rewind <n>")` / `("/branches <n>")` — already wired).
- Produces: `(m *Model) rewindPickerItems() []picker.Item`, `(m *Model) branchesPickerItems() []picker.Item`, and a pre-handler interception block in `dispatchCommand`.

**Why pre-handler:** unlike `/model` (whose registry handler is a no-op), the `/rewind` and `/branches` handlers act immediately. Bare invocations must open the picker *instead of* running the handler, so the interception sits before `cmd.Handler(...)` is called. Argument-carrying invocations fall through to the handler unchanged.

- [ ] **Step 1: Write the failing tests**

```go
func pickerTestModel(t *testing.T, state *session.State) Model {
	t.Helper()
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	return m
}

func TestRewindBareOpensPickerNewestFirst(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "first question", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "second question about parsing", session.ContentTypePlain)
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/rewind")
	m = updated.(Model)
	if m.pickerModel == nil || m.pickerCommand != "rewind" {
		t.Fatal("bare /rewind should open the picker, not rewind immediately")
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "second question") {
		t.Fatalf("picker should preview turn content:\n%s", view)
	}
	// newest turn is first and carries the ● badge → default Enter target
	first := strings.Index(view, "turn 2")
	second := strings.Index(view, "turn 1")
	if first == -1 || second == -1 || first > second {
		t.Fatalf("turns should list newest first:\n%s", view)
	}
}

func TestRewindWithArgSkipsPicker(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "only turn", session.ContentTypePlain)
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/rewind 1")
	m = updated.(Model)
	if m.pickerModel != nil {
		t.Fatal("/rewind 1 must run directly")
	}
}

func TestBranchesBareOpensPickerWithCurrentBadge(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/branches")
	m = updated.(Model)
	if m.pickerModel == nil || m.pickerCommand != "branches" {
		t.Fatal("bare /branches should open the picker")
	}
	if !strings.Contains(stripANSI(m.View().Content), "● now") {
		t.Fatal("current branch should be badged")
	}
}

func TestRewindNoTurnsFallsThroughToHandler(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/rewind")
	m = updated.(Model)
	if m.pickerModel != nil {
		t.Fatal("no turns: picker must not open")
	}
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "No user turns") {
		t.Fatal("handler's 'No user turns to rewind to.' message should appear")
	}
}
```

All four tests share `pickerTestModel` because `dispatchCommand` requires a populated command registry before it reaches the interception block.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/tui/ -run 'TestRewind|TestBranches' -v`
Expected: FAIL — bare `/rewind` rewinds immediately; bare `/branches` prints the text list

- [ ] **Step 3: Implement**

(a) Item builders in `model.go`:

```go
func (m *Model) rewindPickerItems() []picker.Item {
	var turns []session.Message
	for _, msg := range m.state.Messages() {
		if msg.Role == session.RoleUser {
			turns = append(turns, msg)
		}
	}
	items := make([]picker.Item, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		badge := ""
		if i == len(turns)-1 {
			badge = "● last"
		}
		items = append(items, picker.Item{
			Label:  fmt.Sprintf("turn %d", i+1),
			Detail: truncateRunes(strings.ReplaceAll(turns[i].Content, "\n", " "), 50),
			Badge:  badge,
			Value:  strconv.Itoa(i + 1),
		})
	}
	return items
}

func (m *Model) branchesPickerItems() []picker.Item {
	leaves := m.state.Branches()
	cur := m.state.LeafID()
	items := make([]picker.Item, 0, len(leaves))
	for i, id := range leaves {
		badge := ""
		if id == cur {
			badge = "● now"
		}
		items = append(items, picker.Item{
			Label:  fmt.Sprintf("branch %d", i+1),
			Detail: fmt.Sprintf("leaf %d", id),
			Badge:  badge,
			Value:  strconv.Itoa(i + 1),
		})
	}
	return items
}
```

(b) In `dispatchCommand`, insert the interception between `cmd, ok := m.cmdRegistry.Lookup(name)` succeeding and `msg := cmd.Handler(m.state, args)`:

```go
	// Bare picker-backed commands open a modal instead of running the
	// handler; with arguments (or when there is nothing to pick) they fall
	// through to the handler unchanged.
	if len(args) == 0 {
		switch cmd.Name {
		case "rewind":
			if items := m.rewindPickerItems(); len(items) > 0 {
				m.openPicker("rewind", "Rewind to turn", "starts a new branch", items, "")
				m.refreshViewport()
				return m, nil
			}
		case "branches":
			if items := m.branchesPickerItems(); len(items) > 1 {
				m.openPicker("branches", "Switch branch", "", items, "")
				m.refreshViewport()
				return m, nil
			}
		}
	}
```

(`len(items) > 1` for branches: a single branch has nothing to switch to — fall through to the handler's text output.)

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/app/tui/ -run 'TestRewind|TestBranches' -v && go test ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): /rewind and /branches open pickers when bare; args stay direct"
```

---

### Task 8: `/mode` command + final verification

**Files:**
- Modify: `internal/commands/commands.go` (register `/mode`)
- Modify: `internal/app/tui/model.go` (`case "mode"` + `modePickerItems`)
- Test: `internal/app/tui/model_test.go`, `internal/commands/commands_test.go` (append)

**Interfaces:**
- Consumes: Task 5 plumbing (`PickedMsg` with `pickerCommand == "mode"` dispatches `/"ask"|"edit"|"auto"` — already wired).
- Produces: `/mode` registered command; `(m *Model) modePickerItems() []picker.Item`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/tui/model_test.go`:

```go
func TestModePickerMarksCurrentAndApplies(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.forceMode = "edit"

	updated, _ := m.dispatchCommand("/mode")
	m = updated.(Model)
	if m.pickerModel == nil || m.pickerCommand != "mode" {
		t.Fatal("/mode should open the picker")
	}
	view := stripANSI(m.View().Content)
	for _, want := range []string{"Ask", "Edit", "Auto", "● now"} {
		if !strings.Contains(view, want) {
			t.Fatalf("mode picker missing %q:\n%s", want, view)
		}
	}

	updated, _ = m.Update(picker.PickedMsg{Value: "ask"})
	m = updated.(Model)
	if m.forceMode != "ask" {
		t.Fatalf("picking Ask should set forceMode, got %q", m.forceMode)
	}
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "Ask mode") {
		t.Fatal("the /ask handler's confirmation message should appear")
	}
}

func TestModeWithArgDispatchesDirectly(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/mode edit")
	m = updated.(Model)
	if m.pickerModel != nil {
		t.Fatal("/mode edit must not open the picker")
	}
	if m.forceMode != "edit" {
		t.Fatalf("forceMode = %q, want edit", m.forceMode)
	}
}
```

Append to `internal/commands/commands_test.go` (follow its existing style):

```go
func TestModeCommandRegistered(t *testing.T) {
	reg := New()
	if err := RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if _, ok := reg.Lookup("mode"); !ok {
		t.Fatal("/mode should be registered")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/commands/ ./internal/app/tui/ -run 'TestMode' -v`
Expected: FAIL — `/mode` not registered

- [ ] **Step 3: Implement**

(a) Register in `internal/commands/commands.go`, next to the `auto` entry:

```go
		{
			Name:        "mode",
			Description: "Pick the interaction mode (Ask / Edit / Auto)",
			Args:        "[ask|edit|auto]",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
```

(b) In `dispatchCommand`, add the case (near `case "auto":`):

```go
	case "mode":
		if len(args) > 0 {
			switch v := strings.ToLower(args[0]); v {
			case "ask", "edit", "auto":
				return m.dispatchCommand("/" + v)
			}
		}
		m.openPicker("mode", "Interaction mode", "", m.modePickerItems(), "")
		m.refreshViewport()
		return m, nil
```

(c) Items builder:

```go
func (m *Model) modePickerItems() []picker.Item {
	current := m.forceMode // "ask", "edit", or "" (auto)
	badge := func(v string) string {
		if v == current || (v == "auto" && current == "") {
			return "● now"
		}
		return ""
	}
	return []picker.Item{
		{Label: "Ask", Detail: "read-only, no planning", Badge: badge("ask"), Value: "ask"},
		{Label: "Edit", Detail: "planning + full tools", Badge: badge("edit"), Value: "edit"},
		{Label: "Auto", Detail: "classify each turn", Badge: badge("auto"), Value: "auto"},
	}
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/commands/ ./internal/app/tui/ -run 'TestMode' -v`
Expected: PASS

- [ ] **Step 5: Full verification**

```bash
gofmt -l . | tee /dev/stderr | wc -l   # expect 0
go vet ./...
go build ./cmd/marshal
go test ./...
```

Expected: all pass.

- [ ] **Step 6: Manual smoke test**

Run `go run ./cmd/marshal` in a scratch project:
- `/model` → modal over the visible transcript, presets grouped by provider, active preset badged and pre-selected, footer says "session only"; typing filters; Enter switches; Esc leaves no message
- `/model <exact-name>` switches without a modal; `/model zz` opens pre-filtered
- `/rewind` after a couple of turns → newest-first list with previews; Enter-Enter rewinds to the last turn
- `/branches` with 2+ branches → picker with `● now`; single branch → plain text output
- `/mode` → Ask/Edit/Auto with the current mode badged; status line reflects the pick
- resize small → panel clamps, no crash

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(tui): /mode picker command; full verification of command modals"
```

---

## Self-Review (completed during planning)

- **Spec coverage:** §1 extraction → Tasks 1–2. §2 compositor → Task 3. §3 picker → Task 4 (incl. cursor-starts-on-● rule). §4 integration → Task 5. §5 command behavior → Tasks 6–8 (`/model` grouping/badges/session-only, pre-filter rule, `/rewind` newest-first with previews, `/branches` badge, `/mode`). §6 testing → per-task tests + Task 8 Step 5.
- **Deviations recorded:** `pickerCommand` discriminator + dispatch re-entry replaces the spec's `pickerAction` closure (stale-Model-pointer hazard; see Global Constraints). `/rewind`/`/branches` with an invalid numeric argument fall through to the handler's existing error text rather than opening a pre-filtered picker (a number pre-filter would match nothing useful); the pre-filter rule applies to `/model`, where it helps. `/branches` with a single branch keeps the text output.
- **Type consistency:** `picker.Item`/`PickedMsg`/`CancelledMsg`, `openPicker(cmdName, title, footer, items, prefilter)`, `chrome.Panel/ClipLines/Overlay`, `fuzzy.Rank` signatures checked across all tasks.
