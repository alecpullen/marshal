# TUI Usability & Resilience Overhaul Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the breakage, jank, and poor discoverability diagnosed in the `internal/app/tui/` package review by adding a keybinding footer + `?` help to the main view, a terminal-size gate, semantic theming with `NO_COLOR` support, a mode indicator, spinner cadence/delay fixes, and several smaller hardening fixes to overlays and completions.

**Architecture:** All work is confined to `internal/app/tui/` and its subpackages (`settings`, `memory`, `huhtheme`). The central change is a new `theme` subpackage that defines semantic color slots with 256-color and 16-no-color fallbacks, consumed by every renderer in place of raw `lipgloss.Color("209")` literals. A new `help` companion renders the footer bar and the `?` overlay. Layout constants gain a `footerRows` term so the transcript viewport shrinks to make room for the persistent footer. The main `model.go` gains `helpOpen` state and a min-size gate that short-circuits `View()`. Each task is independently testable and commits on green.

**Tech Stack:** Go 1.24, Bubble Tea v2 (`charm.land/bubbletea/v2`), lipgloss v2 (`charm.land/lipgloss/v2`), glamour v2, huh v2, x/ansi widths. Tests are standard `testing` + table tests, run via `go test ./internal/app/tui/...`. Lint: `gofmt -w .` and `go vet ./...`.

## Global Constraints

- Go module paths use the `marshal` prefix (e.g. `marshal/internal/app/tui`).
- Bubble Tea v2: `View()` returns `tea.View`; existing tests access `m.View().Content` (see `view_test.go:29`).
- The TUI owns rendering only — NO routing, policy, or prompt logic lives here (per CLAUDE.md design constraints). Do not pull agent/llm packages into this package.
- Never add comments to code unless requested by the user — follow existing code style (the codebase has dense doc comments; match that style for exported items, none for locals).
- Build requires `CGO_ENABLED=1` (tree-sitter), but TUI tests do not exercise CGO paths — `go test ./internal/app/tui/...` runs without it.
- Commit messages follow repo style: lowercase subject, e.g. `tui: add persistent keybinding footer`.
- Every task ends with `gofmt -w .` and `go test ./internal/app/tui/...` passing before commit.

---

## File Structure

**New files:**
- `internal/app/tui/theme/theme.go` — semantic `Theme` struct with slot fields (`FG.Default`, `BG.Base`, `Accent.Primary`, `Status.Error`, …) and a `Load()` factory that detects `$NO_COLOR` / `$TERM` and returns a 256-color, 16-color, or monochrome instance. All renderers import this instead of raw color literals.
- `internal/app/tui/help/help.go` — `Footer(m.ModeHints) string` and `Overlay(width, height int) string` for the main-chat `?` help overlay.

**Modified files:**
- `internal/app/tui/model.go` — wire theme into `New`, add `helpOpen`/`modeHint` state, `?` key handling, min-size gate, spinner cadence/delay, replace raw color vars with theme slots, add footer row to layout.
- `internal/app/tui/view.go` — add `renderHelpFooter`, append to `viewString`, min-size gate branch in `viewString`, expose a `ModeHint()` helper.
- `internal/app/tui/transcript.go` — replace raw color literals with theme slot references; the `mdRenderers` cache stays (width-keyed).
- `internal/app/tui/status.go` — priority-collapse status segments by mode; render mode indicator segment first when in a non-default mode.
- `internal/app/tui/approval.go` — add "▸ submit" indicator on the submit action; document Enter behavior in the option label.
- `internal/app/tui/completions.go` — highlight matched runes in `acceptedText` path; (scoring unchanged).
- `internal/app/tui/view.go` (`renderCompletionPopup`) — bold the matched subsequence runes per row.
- `internal/app/tui/memory/view.go` — replace hand-drawn border frame with a lipgloss-bordered block (fixes wide-rune/ANSI misalignment).
- `internal/app/tui/settings/model.go` — adaptive sidebar collapse below breakpoint; pull colors from theme.
- `internal/app/tui/huhtheme/theme.go` — derive colors from the shared theme instead of re-declaring consts.

---

## Task 1: Semantic theme package with NO_COLOR detection

**Files:**
- Create: `internal/app/tui/theme/theme.go`
- Create: `internal/app/tui/theme/theme_test.go`

**Interfaces:**
- Consumes: nothing (leaf package; imports only `os`, `strings`, `charm.land/lipgloss/v2`).
- Produces: `type Theme struct` with fields `FGDefault, FGMuted, FGEmphasis, BGBase, BGSurface, BGSelection, AccentPrimary, AccentSecondary, StatusError, StatusWarning, StatusSuccess, StatusInfo lipgloss.Color`, plus `func Load() Theme`, `func LoadFor(noColor bool, term string) Theme`. Later tasks replace `coralColor`/`dimColor`/etc. with `theme.AccentPrimary`/`theme.FGMuted`.

- [ ] **Step 1: Write the failing test**

```go
package theme

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLoadReturnsColors(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	th := LoadFor(false, "xterm-256color")
	if th.AccentPrimary == "" || th.FGDefault == "" {
		t.Fatalf("expected non-zero colors, got %#v", th)
	}
	if th.AccentPrimary != lipgloss.Color("209") {
		t.Fatalf("AccentPrimary = %q, want 209 in 256 mode", th.AccentPrimary)
	}
}

func TestNoColorYieldsMonochrome(t *testing.T) {
	th := LoadFor(true, "xterm-256color")
	for name, c := range map[string]lipgloss.Color{
		"AccentPrimary": th.AccentPrimary,
		"FGMuted":       th.FGMuted,
		"StatusError":   th.StatusError,
	} {
		if c != "" {
			t.Fatalf("%s = %q in NO_COLOR mode, want empty (no SGR)", name, c)
		}
	}
}

func Test16ColorFallback(t *testing.T) {
	th := LoadFor(false, "xterm")
	if th.AccentPrimary != lipgloss.Color("5") {
		t.Fatalf("AccentPrimary = %q, want 16-color magenta 5", th.AccentPrimary)
	}
	if th.StatusSuccess != lipgloss.Color("2") {
		t.Fatalf("StatusSuccess = %q, want 16-color green 2", th.StatusSuccess)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/theme/...`
Expected: FAIL — package/`LoadFor` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// Package theme defines marshal's semantic color slots and resolves them to
// the terminal's color tier (256-color, 16-ANSI, or monochrome when
// $NO_COLOR is set). Every TUI renderer references these slots rather than
// raw color codes, so a single Load() call at startup retunes the whole
// interface. See the TUI design system: "Never hardcode hex values in
// widget code. Always reference semantic slots."
package theme

import (
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Theme is the set of semantic color slots consumed by every renderer.
// Each slot maps a meaning (primary accent, muted text, error) to a
// lipgloss.Color, which may be "" (no SGR emitted) in monochrome mode.
type Theme struct {
	FGDefault     lipgloss.Color
	FGMuted       lipgloss.Color
	FGEmphasis    lipgloss.Color
	BGBase        lipgloss.Color
	BGSurface     lipgloss.Color
	BGSelection   lipgloss.Color
	AccentPrimary lipgloss.Color
	AccentSecondary lipgloss.Color
	StatusError   lipgloss.Color
	StatusWarning lipgloss.Color
	StatusSuccess  lipgloss.Color
	StatusInfo    lipgloss.Color
}

// warmSunset256 is the default dark-theme palette (Warm Sunset).
var warmSunset256 = Theme{
	FGDefault:       lipgloss.Color("252"),
	FGMuted:         lipgloss.Color("244"),
	FGEmphasis:      lipgloss.Color("255"),
	BGBase:          lipgloss.Color("235"),
	BGSurface:        lipgloss.Color("237"),
	BGSelection:     lipgloss.Color("60"),
	AccentPrimary:   lipgloss.Color("209"),
	AccentSecondary: lipgloss.Color("175"),
	StatusError:     lipgloss.Color("203"),
	StatusWarning:   lipgloss.Color("172"),
	StatusSuccess:   lipgloss.Color("43"),
	StatusInfo:      lipgloss.Color("43"),
}

// warmSunset16 maps the Warm Sunset palette onto the 16-ANSI relative set
// so the terminal theme controls the actual appearance.
var warmSunset16 = Theme{
	FGDefault:       lipgloss.Color("7"),
	FGMuted:         lipgloss.Color("8"),
	FGEmphasis:      lipgloss.Color("15"),
	AccentPrimary:   lipgloss.Color("5"),
	AccentSecondary: lipgloss.Color("5"),
	StatusError:     lipgloss.Color("1"),
	StatusWarning:   lipgloss.Color("3"),
	StatusSuccess:   lipgloss.Color("2"),
	StatusInfo:     lipgloss.Color("6"),
}

// monochromeTheme returns a zero Theme (all slots "") so lipgloss emits no
// color SGR sequences — the interface stays usable through layout and
// symbols, exactly as the design system requires ("If you removed all
// color, the interface should still be usable").
func monochromeTheme() Theme { return Theme{} }

// LoadFor resolves the color tier from explicit flags. noColor true
// forces monochrome; term drives 256 vs 16 fallback. Tests use this
// directly; production code calls Load().
func LoadFor(noColor bool, term string) Theme {
	if noColor {
		return monochromeTheme()
	}
	if strings.Contains(term, "256color") {
		return warmSunset256
	}
	return warmSunset16
}

// Load reads the environment and returns the active Theme.
func Load() Theme {
	return LoadFor(os.Getenv("NO_COLOR") != "", os.Getenv("TERM"))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/tui/theme/...`
Expected: PASS.

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/app/tui/theme/
git add internal/app/tui/theme/
git commit -m "tui: add semantic theme package with NO_COLOR detection"
```

---

## Task 2: Wire theme into the TUI package, replacing raw color literals

**Files:**
- Modify: `internal/app/tui/model.go` (the `var (...)` color block at lines 1450-1498 and `New` construction)
- Modify: `internal/app/tui/transcript.go` (inline `lipgloss.Color("…")` usages and the `var` style blocks)
- Modify: `internal/app/tui/status.go` (the `var (...)` style block at lines 107-115)
- Modify: `internal/app/tui/huhtheme/theme.go` (derive from shared theme)
- Modify: `internal/app/tui/model_test.go` only if any test asserts on specific color literals — search first.

**Interfaces:**
- Consumes: `marshal/internal/app/tui/theme` `Load()` → `Theme`.
- Produces: package-level `var activeTheme = theme.Load()` (named `activeTheme`) consumed by all `lipgloss.NewStyle()...Foreground(activeTheme.AccentPrimary)` sites and `huhtheme`.

- [ ] **Step 1: Write the failing test**

Add `theme` import and assert that `activeTheme.AccentPrimary` matches the value used by `accentColor`.

```go
import "marshal/internal/app/tui/theme"

func TestActiveThemeMirrorsLegacyColors(t *testing.T) {
	if activeTheme.AccentPrimary != accentColor {
		t.Fatalf("activeTheme.AccentPrimary = %q, accentColor = %q", activeTheme.AccentPrimary, accentColor)
	}
	if activeTheme.StatusSuccess != successColor {
		t.Fatalf("StatusSuccess = %q, successColor = %q", activeTheme.StatusSuccess, successColor)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestActiveThemeMirrorsLegacyColors`
Expected: FAIL — `activeTheme` undefined.

- [ ] **Step 3: Replace the color var block**

In `internal/app/tui/model.go`, replace the `var (...)` block beginning around line 1450 with a single source-of-truth block that re-aliases the theme slots, keeping the legacy names as backwards-compat shims (so the dozens of `coralColor`/`dimColor`/`accentColor` reference sites compile unchanged):

```go
var activeTheme = theme.Load()

var (
	coralColor  = activeTheme.AccentPrimary
	goldColor   = activeTheme.StatusWarning
	tealColor   = activeTheme.StatusSuccess
	orangeColor = activeTheme.StatusWarning
	mauveColor  = activeTheme.FGMuted
	userColor   = activeTheme.FGDefault

	accentColor  = activeTheme.AccentPrimary
	violetColor  = activeTheme.AccentSecondary
	dimColor     = activeTheme.FGMuted
	successColor = activeTheme.StatusSuccess
	warningColor = activeTheme.StatusWarning
	errorColor   = activeTheme.StatusError

	mutedStyle = lipgloss.NewStyle().Foreground(activeTheme.FGMuted)
	panelTitleStyle = lipgloss.NewStyle().
		Foreground(activeTheme.FGEmphasis).
		Bold(true)
	thinkingLineStyle = lipgloss.NewStyle().
		Foreground(activeTheme.FGMuted).
		Italic(true)

	codeBorderStyle = lipgloss.NewStyle().
		Foreground(activeTheme.FGMuted).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(activeTheme.FGMuted)
	toolNameStyle = lipgloss.NewStyle().Foreground(activeTheme.StatusWarning)
	keyHintStyle = lipgloss.NewStyle().
		Foreground(activeTheme.AccentPrimary).
		Bold(true)
	riskLabelStyle = lipgloss.NewStyle().
		Foreground(activeTheme.StatusWarning).
		Bold(true)
	dimSeparator = " · "

	inputBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(activeTheme.AccentPrimary).
		Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
		Foreground(activeTheme.FGDefault)
)
```

Add the import `"marshal/internal/app/tui/theme"` to model.go.

- [ ] **Step 4: Update inline literals in transcript.go**

Replace these inline `lipgloss.Color("255")`/`"245"`/`"252"` literals inside `transcript.go` style declarations (e.g. the `panelTitleStyle`, `statusBarStyle`, and any `lipgloss.NewStyle().Foreground(lipgloss.Color(…))` call sites) with the corresponding `activeTheme.*` slot. Concretely, change:
- `lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true)` → `activeTheme.FGEmphasis` (panel titles).
- `renderWelcomeBanner` `lipgloss.NewStyle().Foreground(coralColor)` → `activeTheme.AccentPrimary` (already covered by `coralColor`, no change needed — verify).

There are no other raw literals in `transcript.go` beyond the ones already sourced from the package vars; grep to confirm:

Run: `grep -n 'lipgloss.Color("2' internal/app/tui/transcript.go`
Expected: no remaining hardcoded `"2xx"` literals (only references via `coralColor`/`dimColor`/etc vars).

If any remain, replace them with the matching slot and note the line in the commit.

- [ ] **Step 5: Update huhtheme to derive from the shared theme**

In `internal/app/tui/huhtheme/theme.go`, replace the `const ( Coral = "209" … )` block with imports of the shared theme. Add the import and initialize a package-level `var t = theme.Load()`:

```go
import "marshal/internal/app/tui/theme"
```

Then change the `WarmSunset()` func body to read color values from `t` (e.g. `coral := t.AccentPrimary`, `gold := t.StatusWarning`, etc.) instead of the literals `"209"`/`"214"`/`"43"`/`"244"`/`"245"`/`"172"`/`"252"`/`"203"`. Keep the function signature (`huh.ThemeFunc`) unchanged. The existing const block can stay as a deprecated alias mapping to `string(t.AccentPrimary)` if any external test references it — check first:

Run: `grep -rn 'huhtheme.Coral' .`
If no usages outside the package, delete the const block entirely; else keep as `const Coral = string(theme.Load().AccentPrimary)`.

- [ ] **Step 6: Run the full TUI test suite**

Run: `go test ./internal/app/tui/...`
Expected: PASS (the legacy alias vars keep all reference sites compiling; tests that assert on rendered content typically match runes via `stripANSI`, so color-code changes don't break them — verify by re-running).

- [ ] **Step 7: Format and commit**

```bash
gofmt -w internal/app/tui/ internal/app/tui/huhtheme/
git add internal/app/tui/
git commit -m "tui: derive colors from semantic theme slots"
```

---

## Task 3: Terminal too-small gate

**Files:**
- Modify: `internal/app/tui/model.go` (constants at lines 43-48, `resize` at 354)
- Modify: `internal/app/tui/view.go` (`viewString` at 45, `fallbackView` at 185)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: raises `minTerminalWidth` to 80, `minTerminalHeight` to 24; `viewString()` returns a centered resize message when below the gate instead of the crushed layout. `resize()` still clamps so internal geometry math never divides by zero, but the gate branch short-circuits rendering.

- [ ] **Step 1: Write the failing test**

```go
func TestMinSizeGateShowsResizeMessage(t *testing.T) {
	m := newViewTestModel(t, 60, 15)
	view := m.View().Content
	// Must not contain the normal chrome and must mention resizing.
	if strings.Contains(view, "❯") {
		t.Fatalf("_below-min view should not render input, got:\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "resize") {
		t.Fatalf("below-min view missing resize guidance:\n%s", view)
	}
}

func TestNormalSizeBypassesGate(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	view := m.View().Content
	if !strings.Contains(view, "❯") {
		t.Fatalf("80x24 view should render input prompt, got:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestMinSizeGate|TestNormalSizeBypasses'`
Expected: FAIL — current min is 40×10, so 60×15 renders the crushed layout (contains `❯`).

- [ ] **Step 3: Raise the minimums and add the gate branch**

In `model.go` constants block change:

```go
minTerminalWidth  = 80
minTerminalHeight = 24
```

In `view.go`, change `viewString()`:

```go
func (m Model) viewString() string {
	if m.width < minTerminalWidth || m.height < minTerminalHeight {
		return m.tooSmallView()
	}
	if m.settingsOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModel.View())
	}
	// … existing body unchanged
```

Add the helper:

```go
func (m Model) tooSmallView() string {
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		return ""
	}
	msg := "Terminal too small\nResize to at least 80×24"
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
		mutedStyle.Render(msg))
}
```

Note: `resize()` keeps its existing clamp at `minTerminalWidth`/`Height` so geometry stays sane while the gate message draws; the gate message is computed from the raw `m.width`/`m.height` (the clamped value equals the min when below, which is ≥ the raw — acceptable since `lipgloss.Place` centers within whatever box it's given). To avoid the gate message overflowing a *very* small terminal (smaller than the message), `lipgloss.Place` clips to the given box, so nothing wraps improperly.

- [ ] **Step 4: Adjust existing tests that assume the old minimums**

Search for tests constructing models below 80×24:

Run: `grep -rn 'resize(4[0-9]\|resize([0-9],\|resize([0-9][0-9],' internal/app/tui/*_test.go`

Bump any `resize(<80, …)` or `resize(…, <24)` test calls to at least `80, 24` (e.g. the `newViewTestModel(t, 60,…)` helpers). Keep the intent: tests that validate narrow-terminal truncation should now use e.g. `90, 24`. Update expectations (e.g. `TestViewContainsStatusLine` already uses 100×30 — fine).

- [ ] **Step 5: Run full suite**

Run: `go test ./internal/app/tui/...`
Expected: PASS.

- [ ] **Step 6: Format and commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/
git commit -m "tui: gate rendering behind 80x24 minimum with resize message"
```

---

## Task 4: Persistent keybinding footer in the main view

**Files:**
- Create: `internal/app/tui/help/help.go`
- Create: `internal/app/tui/help/help_test.go`
- Modify: `internal/app/tui/view.go` (append footer row, add `footerRows` constant)
- Modify: `internal/app/tui/model.go` (subtract `footerRows` in viewport height math at lines 375 and 951)
- Modify: `internal/app/tui/view.go` constants (line 21-27)

**Interfaces:**
- Consumes: from the model the hints that are *currently actionable*: `m.busy`, `m.editingCommand`, `m.state.PendingApproval() != nil`, `m.state.PendingQuestion() != nil`, `m.activeCompletionPopup() != nil`. These are accessed via a small callback struct so the help package has no import cycle into the main tui package.
- Produces: `help.Footer(hints FooterHints) string` where `FooterHints` is a plain struct; `help.Overlay(width, height int) string`. Also `const Rows = 1` exported as `help.Rows`.

- [ ] **Step 1: Write the failing test in help package**

```go
package help

import (
	"strings"
	"testing"
)

func TestFooterIdle(t *testing.T) {
	out := Footer(FooterHints{})
	if !strings.Contains(out, "Enter") || !strings.Contains(out, "?") || !strings.Contains(out, "/") {
		t.Fatalf("idle footer missing core hints: %q", out)
	}
	if strings.Contains(out, "cancel queued") {
		t.Fatalf("idle footer should not show busy-only hints: %q", out)
	}
}

func TestFooterBusyShowsCancelAndQueue(t *testing.T) {
	out := Footer(FooterHints{Busy: true})
	if !strings.Contains(out, "Esc cancel") || !strings.Contains(out, "Ctrl+X clear queue") {
		t.Fatalf("busy footer missing cancel/queue hints: %q", out)
	}
}

func TestFooterQuestionShowsAnswer(t *testing.T) {
	out := Footer(FooterHints{QuestionPending: true})
	if !strings.Contains(out, "Enter answer") {
		t.Fatalf("question footer missing answer hint: %q", out)
	}
}

func TestOverlayEnumeratesAllBindings(t *testing.T) {
	out := Overlay(80, 24)
	for _, want := range []string{"Enter", "Shift+Enter", "/", "@", "Esc", "?", "Ctrl+O", "Ctrl+K", "Ctrl+G", "Ctrl+R", "Ctrl+X", "PgUp", "PgDn", "End"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help overlay missing %q:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/help/...`
Expected: FAIL — package undefined.

- [ ] **Step 3: Implement the help package**

```go
// Package help renders the persistent keybinding footer and the ? help
// overlay for the main marshal chat view. The footer always shows the 3-5
// most actionable shortcuts for the current mode (progressive disclosure L0);
// the overlay (triggered by ?) lists every binding (L1/L2).
package help

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Rows is the vertical budget the persistent footer occupies in the main
// layout; the transcript viewport shrinks by this amount.
const Rows = 1

// FooterHints describes which mode-driven hints are currently actionable.
// The footer shows the union of always-on hints plus the mode-specific ones
// so a user never sees a hint they can't act on right now.
type FooterHints struct {
	Busy            bool
	EditingCommand  bool
	ApprovalPending bool
	QuestionPending bool
	PopupOpen       bool
}

var keyStyle = lipgloss.NewStyle().Bold(true)
var sep = lipgloss.NewStyle().Faint(true).SetString(" · ")

func pair(k, label string) string { return keyStyle.Render(k) + " " + label }

// Footer returns the single-row keybinding bar.
func Footer(h FooterHints) string {
	var segs []string
	if h.QuestionPending {
		segs = append(segs, pair("Enter", "answer"), pair("Esc", "skip"))
	} else if h.ApprovalPending && !h.EditingCommand {
		segs = append(segs, pair("Enter", "approve"), pair("d", "deny"), pair("e", "edit"), pair("a", "always"), pair("Esc", "deny"))
	} else if h.EditingCommand {
		segs = append(segs, pair("Enter", "save"), pair("Esc", "cancel edit"))
	} else if h.PopupOpen {
		segs = append(segs, pair("↑↓", "choose"), pair("Tab/Enter", "accept"), pair("Esc", "dismiss"))
	} else {
		segs = append(segs, pair("Enter", "send"), pair("Shift+Enter", "newline"))
		if h.Busy {
			segs = append(segs, pair("Esc", "cancel"), pair("Ctrl+X", "clear queue"))
		} else {
			segs = append(segs, pair("/", "command"), pair("@", "file"))
		}
	}
	segs = append(segs, pair("?", "help"))
	sepStr := sep.Render("")
	return strings.Join(segs, sepStr)
}

// Overlay returns the full-screen help panel shown when ? is pressed.
func Overlay(width, height int) string {
	lines := []string{
		"marshal keys",
		"",
		"  Enter          send message / accept",
		"  Shift+Enter     newline in input",
		"  /              command completion",
		"  @              file completion",
		"  ↑↓             choose completion · PgUp/PgDn/Ctrl-U/Ctrl-D/End scroll",
		"  Tab            accept completion",
		"  Esc            cancel turn · dismiss popup · deny approval",
		"  Ctrl+O         settings",
		"  Ctrl+K         memory browser",
		"  Ctrl+G         toggle thinking",
		"  Ctrl+R         rollback last change",
		"  Ctrl+X         clear steering queue (while busy)",
		"  ?              this help",
		"  Ctrl+C         quit",
		"",
		"Press ? or Esc to close.",
	}
	body := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Align(lipgloss.Center, lipgloss.Center).Render(body)
}
```

- [ ] **Step 4: Run the help package tests**

Run: `go test ./internal/app/tui/help/...`
Expected: PASS.

- [ ] **Step 5: Wire the footer into the main layout**

In `view.go`, import `marshal/internal/app/tui/help` and add to the constants block:

```go
footerRows = 1
```

In `viewString()`, after `m.renderInputArea()`, insert the footer row before the status line:

```go
rows := []string{m.renderTranscriptFrame()}
if panel := renderSwarmPanel(m.state.SwarmProgress(), m.spinnerFrame, m.width); panel != "" {
	rows = append(rows, panel)
}
rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
```

Add the renderer:

```go
func (m Model) renderHelpFooter() string {
	hints := help.FooterHints{
		Busy:            m.busy,
		EditingCommand:  m.editingCommand,
		ApprovalPending: m.state.PendingApproval() != nil,
		QuestionPending: m.state.PendingQuestion() != nil,
		PopupOpen:       m.activeCompletionPopup() != nil,
	}
	return mutedStyle.Width(max(m.width, 1)).Render(help.Footer(hints))
}
```

In `model.go`, add `footerRows` to the two viewport height expressions so the transcript shrinks by one row:

Line 375 (`resize`):
```go
m.viewport.SetHeight(max(height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-footerRows-statusLineRows, 1))
```

Line 951 (`updateViewportHeight`):
```go
newViewportHeight := max(m.height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-footerRows-statusLineRows, 1)
```

- [ ] **Step 6: Guard the footer when an overlay is open**

Overlays (settings, memory) replace the whole screen via `lipgloss.Place`, so the footer is naturally absent — no change needed. But when `m.helpOpen` is true (added in Task 5), `viewString()` returns the overlay instead; the footer is also naturally absent then.

- [ ] **Step 7: Add a test that the footer renders in the default view**

In `view_test.go`:

```go
func TestViewContainsFooter(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	view := m.View().Content
	if !strings.Contains(view, "send") || !strings.Contains(view, "help") {
		t.Fatalf("view missing keybinding footer:\n%s", view)
	}
}
```

- [ ] **Step 8: Run full suite and fix any height-counted assertions**

Several tests assert on exact row counts (e.g. `TestTranscriptFrameDoesNotMoveWhenActivityStarts` checks `len(busyLines) == 30`). The footer adds one row borrowed from the transcript viewport, so the total height stays the terminal height — those tests should still pass. Re-run:

Run: `go test ./internal/app/tui/...`
Expected: PASS. If a test that asserts viewport height off-by-one appears, adjust its expected value by `-footerRows` with a comment referencing the footer.

- [ ] **Step 7: Format and commit**

```bash
gofmt -w internal/app/tui/ internal/app/tui/help/
git add internal/app/tui/
git commit -m "tui: add persistent keybinding footer to main view"
```

---

## Task 5: `?` help overlay in the main view

**Files:**
- Modify: `internal/app/tui/model.go` (add `helpOpen bool` field, `?` key handling, View routing)
- Modify: `internal/app/tui/view.go` (`viewString` branch for overlay)

**Interfaces:**
- Consumes: `help.Overlay`.
- Produces: `m.helpOpen` toggled by `?`; while open, `viewString()` returns the overlay (centered), and `?`/`Esc` close it.

- [ ] **Step 1: Write the failing test**

```go
func TestHelpOverlayOpensAndCloses(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	updated, _ := m.Update(tea.KeyPressMsg{Code: '?'})
	m = updated.(Model)
	if !m.helpOpen {
		t.Fatal("? did not open help overlay")
	}
	view := m.View().Content
	if !strings.Contains(view, "marshal keys") {
		t.Fatalf("help overlay not rendered:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.helpOpen {
		t.Fatal("Esc did not close help overlay")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestHelpOverlayOpensAndCloses`
Expected: FAIL — `helpOpen` undefined.

- [ ] **Step 3: Add state and key handling**

In `model.go` `Model` struct, add a field near `settingsOpen`:

```go
helpOpen bool
```

In the main keypress switch (`case tea.KeyPressMsg:`), add a `?` case **before** the existing local keys (so it's global, not consumed by the textarea) — place it right after the `"ctrl+r"` case, before `"up"`:

```go
case "?":
	m.helpOpen = !m.helpOpen
	return m, nil
```

But this alone routes `?` to the textarea afterward. To make `?` open the overlay *and* not insert a char, handle it earlier: insert a guard right after the scroll-key block (before approval/question routing at line ~544) but only when no input editing context is active. Simpler approach: handle `?` at the very top of the `case tea.KeyPressMsg:` switch and `return` immediately when toggled open, and only allow closing:

Replace the `switch msg.String()` so that the very first case is:

```go
case "?":
	// Only toggle when the textarea is empty or not actively editing
	// (so ? inside a query is still a literal char). When the overlay
	// is already open, ? or Esc close it.
	if m.helpOpen {
		m.helpOpen = false
		return m, nil
	}
	if m.input.Value() == "" && !m.editingCommand && m.state.PendingQuestion() == nil && m.state.PendingApproval() == nil {
		m.helpOpen = true
		return m, nil
	}
	// Otherwise fall through (insert ? as a normal char).
```

Then in `viewString()`, add the branch near the top (after the too-small gate, before `settingsOpen`):

```go
if m.helpOpen {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, help.Overlay(m.width, m.height))
}
```

- [ ] **Step 4: Handle Esc closing the overlay**

Esc is already handled at the top: `case "esc"` dismisses a popup then cancels the turn. Add an overlay check **before** that:

```go
case "esc":
	if m.helpOpen {
		m.helpOpen = false
		return m, nil
	}
	if m.activeCompletionPopup() != nil {
		…
```

- [ ] **Step 5: Run the test**

Run: `go test ./internal/app/tui/ -run TestHelpOverlayOpensAndCloses`
Expected: PASS.

- [ ] **Step 6: Run full suite**

Run: `go test ./internal/app/tui/...`
Expected: PASS. Verify none of the existing tests that send `?` as a literal keystroke into the input break — they should now be unaffected when the input is non-empty (the guard requires empty input to open the overlay).

- [ ] **Step 7: Format and commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/
git commit -m "tui: add ? keybinding help overlay for the main view"
```

---

## Task 6: Spinner cadence (80ms) and 200ms delay before showing

**Files:**
- Modify: `internal/app/tui/model.go` (`tickCmd` at 1255, `agentTickMsg` handler at 475, `renderActivityStrip` in view.go)
- Modify: `internal/app/tui/status.go` (`statusRightSegment`)
- Modify: `internal/app/tui/transcript.go` (`renderThinkingBox` uses spinner frame only after delay)

**Interfaces:**
- Consumes: nothing new.
- Produces: a dedicated fast spinner tick (`spinnerTickMsg`) decoupled from the 150ms layout tick. Spinner frames shown only after the activity has been active >200ms.

- [ ] **Step 1: Write the failing test**

```go
func TestSpinnerHiddenBefore200ms(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.busy = true
	start := time.Unix(100, 0)
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, StartedAt: start, Label: "running"})
	m.now = func() time.Time { return start.Add(100 * time.Millisecond) }
	m.spinnerFrame = ""
	view := m.View().Content
	// Active strip should show the label but NOT the spinner glyph yet.
	if strings.ContainsAny(view, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("spinner shown before 200ms delay:\n%s", view)
	}
}

func TestSpinnerShownAfter200ms(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.busy = true
	start := time.Unix(100, 0)
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, StartedAt: start, Label: "running"})
	m.now = func() time.Time { return start.Add(300 * time.Millisecond) }
	m.spinnerFrame = "⠙"
	view := m.View().Content
	if !strings.Contains(view, "⠙") {
		t.Fatalf("spinner not shown after 200ms:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestSpinner'`
Expected: FAIL — the activity strip renders the spinner frame unconditionally.

- [ ] **Step 3: Add the spinnerTick mechanism**

In `model.go`, add a message and command:

```go
type spinnerTickMsg struct{}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}
```

Wire it alongside the existing tick: when `m.busy` becomes true (in the `"enter"` handler and `/swarm` handler), batch `spinnerTickCmd()` with the existing `tickCmd()`:

```go
return m, tea.Batch(runAgentCmd(agentCtx, m.runner, value), tickCmd(), spinnerTickCmd())
```

In `Update`, add a case for `spinnerTickMsg` mirroring the activity-spin update but re-arming itself:

```go
case spinnerTickMsg:
	if !m.busy {
		return m, nil
	}
	m.spinnerFrame = m.spinner.Next()
	m.refreshViewport()
	return m, spinnerTickCmd()
```

The existing `agentTickMsg` keeps the 150ms layout/elapsed refresh but should no longer advance the spinner frame itself — remove `m.spinnerFrame = m.spinner.Next()` from the `agentTickMsg` handler (leave the activity-label caching).

- [ ] **Step 4: Gate the spinner glyph behind 200ms**

In `view.go` `renderActivityStrip()`, compute the elapsed time and substitute an empty frame if <200ms. The function currently uses `m.spinnerFrame` directly; introduce a helper:

```go
func (m Model) activeSpinnerFrame(kind session.ActivityKind) string {
	if kind == session.ActivityIdle {
		return ""
	}
	a := m.state.Activity()
	if m.now().Sub(a.StartedAt) < 200*time.Millisecond {
		return ""
	}
	return m.spinnerFrame
}
```

Replace `m.spinnerFrame` in `renderActivityStrip()` with `m.activeSpinnerFrame(activity.Kind)`. Likewise in `status.go` `statusRightSegment()` and `transcript.go` `renderThinkingBox()`, use the helper. For `renderActiveToolCall`, the spinner frame is passed in from `refreshViewport`; compute it there:

```go
frame := m.activeSpinnerFrame(atc.Kind) // needs atc kind; since ActiveToolCall has no Kind, use m.state.Activity().Kind
if frame == "" {
	frame = " " // reserve the cell so the line doesn't jitter
}
b.WriteString(renderActiveToolCall(atc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, frame, m.now(), m.viewport.Width()))
```

(The active-tool line width is fixed by the spinner reserved cell, preventing jitter.)

- [ ] **Step 5: Re-arm the spinner chain on each agent run**

The `/swarm` branch in `dispatchCommand` and the `"enter"` submit branch both already call `tea.Batch(runAgentCmd(...), tickCmd())` — append `, spinnerTickCmd()` to each:

```go
return m, tea.Batch(runAgentCmd(agentCtx, m.swarmRunner, goal), tickCmd(), spinnerTickCmd())
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/app/tui/...`
Expected: PASS. Existing spinner-frame tests (e.g. ones that set `m.spinnerFrame = "⠋"` and check rendering) still pass because they set the frame explicitly; the 200ms gate uses real time via `m.now`, and those tests use a fixed `time.Unix(100,0)` start so ~0ms elapsed — confirm the gate shows the frame only if the test sets `m.now` to ≥200ms ahead. If a test breaks, update its `m.now` to `start.Add(300*time.Millisecond)`.

- [ ] **Step 7: Format and commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/
git commit -m "tui: 80ms spinner cadence with 200ms show delay"
```

---

## Task 7: Mode indicator in the status line (predictable Esc)

**Files:**
- Modify: `internal/app/tui/status.go` (`statusLeftSegments`, new `modeSegment`)
- Modify: `internal/app/tui/model.go` (expose helpers the status wants)

**Interfaces:**
- Consumes: `m.editingCommand`, `m.activeCompletionPopup() != nil`, `m.helpOpen`.
- Produces: the leftmost status segment changes from `auto`/`ask`/`edit` to a rich mode label like `edit cmd`, `completing`, `help open` when in a non-default mode, so the user can predict Esc.

- [ ] **Step 1: Write the failing test**

```go
func TestStatusLineShowsEditingMode(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.editingCommand = true
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "edit cmd") {
		t.Fatalf("status line missing edit mode indicator:\n%s", line)
	}
}

func TestStatusLineShowsHelpMode(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.helpOpen = true
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "help open") {
		t.Fatalf("status line missing help mode:\n%s", line)
	}
}

func TestStatusLineShowsCompletingMode(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.cmdPopup = nil
	m.cmdPopup = newCompletionPopup([]completionItem{{Text: "/plan", Kind: completionCommand}})
	m.cmdPopup.update("pl")
	line := m.renderStatusLine(100)
	if !strings.Contains(line, "completing") {
		t.Fatalf("status line missing completing mode:\n%s", line)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestStatusLineShows'`
Expected: FAIL — the status shows `auto` regardless.

- [ ] **Step 3: Add the mode segment**

In `status.go`, add a method on Model:

```go
func (m Model) modeSegment() string {
	switch {
	case m.helpOpen:
		return "help open"
	case m.editingCommand:
		return "edit cmd"
	case m.activeCompletionPopup() != nil:
		return "completing"
	case m.state.PendingApproval() != nil:
		return "approval"
	case m.state.PendingQuestion() != nil:
		return "answering"
	default:
		mode := m.forceMode
		if mode == "" {
			mode = "auto"
		}
		return mode
	}
}
```

In `statusLeftSegments`, replace the `mode := m.forceMode; …` block with:

```go
segments := []string{m.modeSegment()}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/tui/...`
Expected: PASS. Check `status_test.go` tests that assert specific segment counts — the default case unchanged keeps them green.

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/
git commit -m "tui: surface active mode in the status line"
```

---

## Task 8: Memory overlay border via lipgloss (fix wide-rune/ANSI misalignment)

**Files:**
- Modify: `internal/app/tui/memory/view.go`
- Modify: `internal/app/tui/memory/model_test.go`

**Interfaces:**
- Consumes: `charm.land/lipgloss/v2`.
- Produces: the memory overlay uses a lipgloss-bordered block with proper width-based padding; `frameLine`/`frameTitle`/etc. helpers deleted.

- [ ] **Step 1: Write the failing test asserting alignment with a wide rune**

```go
func TestMemoryViewAlignsWithWideRune(t *testing.T) {
	db, project := memoryTestDB(t) // reuse the existing helper if present, else construct
	m := memory.New(db, project)
	m.SetSize(80, 24)
	view := m.View()
	// Count │ chars per line — each visible row must have exactly two.
	for i, line := range strings.Split(strings.TrimSpace(view), "\n") {
		if strings.HasPrefix(line, "│") {
			left := strings.Count(line, "│")
			if left != 2 {
				t.Fatalf("line %d has %d │, want 2: %q", i, left, line)
			}
		}
	}
}
```

If there's an existing memory test DB helper, reuse it; otherwise insert a memory containing a wide rune (`鸭`) into the db to ensure the misalignment reproduces. The point is the test asserts on `│` count, which the old rune-count padding breaks.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/memory/...`
Expected: FAIL — old frame pads by rune length, so wide runes misalign the right `│`.

- [ ] **Step 3: Rewrite view.go with a lipgloss block**

```go
package memory

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

const minFrameWidth = 30

func (m Model) View() string {
	frameWidth := 61
	if m.width > 0 {
		frameWidth = min(61, m.width-4)
	}
	if frameWidth < minFrameWidth {
		frameWidth = minFrameWidth
	}

	visible := m.visibleCount()
	if visible < 1 {
		visible = 1
	}
	end := m.offset + visible
	if end > len(m.memories) {
		end = len(m.memories)
	}

	var b strings.Builder
	if len(m.memories) == 0 {
		b.WriteString("No memories yet.\n")
	}
	for i := m.offset; i < end; i++ {
		mem := m.memories[i]
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("%s[%s] (%s) %s\n", cursor, mem.Kind, mem.Confidence, mem.Content))
	}

	var footer string
	if m.footer != "" {
		footer = m.footer + "\n"
	}
	footer += "[↑/k ↓/j] Move  [c] Confirm  [s] Mark Stale  [Esc] Close"

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("245")).
		Width(frameWidth).
		Padding(0, 1)

	title := lipgloss.NewStyle().Bold(true).Render("Project Memories")
	content := title + "\n\n" + b.String() + footer
	return box.Render(content)
}
```

Delete `frameTitle`, `frameSeparator`, `frameBottom`, `frameLine`, and `truncateRunes` (the `truncateRunes` here was local — verify no other file references it; the tui package has its own).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/tui/memory/...`
Expected: PASS. Update any test asserting on exact `┌─` glyphs — those change to lipgloss rounded corners `╭`; fix expectations.

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/app/tui/memory/
git add internal/app/tui/memory/
git commit -m "tui: use lipgloss border for memory overlay, fix wide-rune alignment"
```

---

## Task 9: Settings overlay adaptive sidebar collapse

**Files:**
- Modify: `internal/app/tui/settings/model.go` (`SetSize`, `View`, add `sidebarHidden bool`)
- Modify: `internal/app/tui/settings/model_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: below `sidebarBreakpoint` (= 70 cols), the sidebar collapses to a compact stack and the pane gets the full width; no overflow.

- [ ] **Step 1: Write the failing test**

```go
func TestNarrowTerminalHidesSidebar(t *testing.T) {
	m := settings.New(testConfig(), "/repo", "/repo/.marshal/config.toml")
	m.SetSize(50, 24)
	view := m.View()
	if strings.Contains(view, "▸") && getViewWidth(view) > 50 {
		t.Fatalf("settings overflow at width 50:\n%s", view)
	}
}
```

(Exact assertion helper depends on existing test helpers — if none, assert that the rendered view's widest line ≤ 50 runes via `ansi.StringWidth`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/settings/... -run TestNarrow`
Expected: FAIL or panic on overflow.

- [ ] **Step 3: Add the breakpoint logic**

In `settings/model.go`, add a constant and a `sidebarHidden` field computed in `SetSize`:

```go
const sidebarBreakpoint = 70
```

```go
type Model struct {
	// existing fields…
	sidebarHidden bool
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.sidebarHidden = width < sidebarBreakpoint
	paneW := width
	if !m.sidebarHidden {
		paneW = width - sidebarWidth - 6
	}
	if paneW < 30 {
		paneW = 30
	}
	for _, p := range m.panes {
		p.SetWidth(paneW)
	}
}
```

In `View()`, when `m.sidebarHidden`, render only the pane (no sidebar); show the active section name in the pane header (which it already does via `paneTitleStyle.Render(m.sections[m.cursor].title)`). Add a tiny section switcher note to the footer: `←/→ sections` so the user can move between sections via h/l/Tab even without the visible list.

Add key handling in `Update` — when `sidebarHidden` and not pane-focused, `left`/`h` and `right`/`l` cycle sections directly (wrap-around):

```go
if m.sidebarHidden && !m.paneFocused {
	switch k.String() {
	case "left", "h":
		m.cursor = (m.cursor - 1 + len(m.sections)) % len(m.sections)
		return *m, nil
	case "right", "l":
		m.cursor = (m.cursor + 1) % len(m.sections)
		return *m, nil
	}
}
```

- [ ] **Step 4: Run tests and fix expectations**

Run: `go test ./internal/app/tui/settings/...`
Expected: PASS. Tests that assert on the sidebar existing may need their width bumped ≥70.

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/app/tui/settings/
git add internal/app/tui/settings/
git commit -m "tui: collapse settings sidebar below 70-col breakpoint"
```

---

## Task 10: Approval option labels — separate submit and "▸ submit" indicator

**Files:**
- Modify: `internal/app/tui/approval.go` (`opts` list, `newApprovalModel`)
- Modify: `internal/app/tui/approval_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: the submit action is explicit and labeled so Enter on the last option unambiguously submits; navigation uses `j`/`k`/`Tab`/arrow documented in the option list.

- [ ] **Step 1: Write the failing test**

```go
func TestApprovalShowsSubmitLabel(t *testing.T) {
	am := newApprovalModel(pendingShellCall(), session.SandboxInfo{}, false, false, 80)
	view := am.View()
	if !strings.Contains(view, "Approve") || !strings.Contains(view, "submit") {
		t.Fatalf("approval missing Approve/submit labels:\n%s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestApprovalShowsSubmitLabel`
Expected: FAIL — labels are "Approve" etc. with no "submit".

- [ ] **Step 3: Adjust labels and add j/k navigation hint**

In `approval.go` `newApprovalModel`, change the options to include the action verb in each label, and add an explicit submit option so Enter-on-last submits only when on the submit row:

```go
opts := []huh.Option[approvalChoice]{
	huh.NewOption("Approve", choiceApprove),
	huh.NewOption("Deny", choiceDeny),
	huh.NewOption("Edit command/args", choiceEdit),
	huh.NewOption("Always allow (save to config)", choiceAlways),
	huh.NewOption("Allow this session", choiceSessionAllow),
}
if hasBackup {
	opts = append(opts, huh.NewOption("Rollback last change", choiceRollback))
}
opts = append(opts, huh.NewOption("▸ Submit selection", choiceApprove))
```

Since "Submit" and "Approve" both map to `choiceApprove`, the behavior is unchanged (Enter on either approves), but the visible "Submit" label signals that pressing Enter on the last row is the explicit submit. Add j/k/arrow navigation so the user doesn't need Enter to move between options — the huh `Select.Submit` keybinding stays Enter, but the keymap's `Select.Next`/`Prev` already bind j/k/arrows by default; verify with the existing `km` and assert in the test that j/k moves selection without submitting.

Add to the title summary in `approvalSummary` a one-line hint:

```go
b.WriteString(mutedStyle.Render("↑↓/j/k to select · Enter to submit"))
b.WriteString("\n\n")
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/tui/...`
Expected: PASS.

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/
git commit -m "tui: clarify approval submit with explicit label and nav hint"
```

---

## Task 11: Completion popup highlights matched characters

**Files:**
- Modify: `internal/app/tui/completions.go` (track matched indices in `completionItem`)
- Modify: `internal/app/tui/view.go` (`renderCompletionPopup`)
- Test: `internal/app/tui/completions_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `completionPopup.update` records the matched rune indices in a per-row field; `renderCompletionPopup` bolds those runes.

- [ ] **Step 1: Write the failing test**

```go
func TestFuzzyScoreRecordsMatchIndices(t *testing.T) {
	idxs, ok := fuzzyMatchIndices("pl", "/plan")
	if !ok {
		t.Fatal("expected match")
	}
	if len(idxs) != 2 || idxs[0] != 1 || idxs[1] != 2 { // positions of p,l in "/plan"
		t.Fatalf("match indices = %v, want [1 2]", idxs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestFuzzyScoreRecordsMatchIndices`
Expected: FAIL — `fuzzyMatchIndices` undefined.

- [ ] **Step 3: Add index recording**

In `completions.go`, add a pure helper that returns matched rune indices (mirrors `fuzzyScore` but collects positions):

```go
func fuzzyMatchIndices(query, target string) ([]int, bool) {
	q := strings.ToLower(query)
	tt := strings.ToLower(target)
	if len(q) == 0 {
		return nil, true
	}
	if len(q) > len(tt) {
		return nil, false
	}
	idxs := make([]int, 0, len(q))
	ti := 0
	for qi := 0; qi < len(q); qi++ {
		matched := false
		for ; ti < len(tt); ti++ {
			if q[qi] == tt[ti] {
				idxs = append(idxs, ti)
				matched = true
				ti++
				break
			}
		}
		if !matched {
			return nil, false
		}
	}
	return idxs, true
}
```

Add a `matchIdxs []int` field to `completionItem` (or a parallel slice in the popup). Simpler: add `matchedIdxs []int` to `completionItem` and set it in the `update` loop:

```go
type completionItem struct {
	Text        string
	Description string
	Kind        completionKind
	matchedIdxs []int
}
```

In `update`, when scoring a hit:

```go
idxs, okMatch := fuzzyMatchIndices(query, it.Text)
if okMatch {
	hit := it
	hit.matchedIdxs = idxs
	hits = append(hits, scored{hit, s})
}
```

- [ ] **Step 4: Highlight in the popup renderer**

In `view.go` `renderCompletionPopup`, replace `row := marker + matches[i].Text` with a version that bolds matched runes (using `activeTheme.AccentPrimary` for emphasis — visible in 256 and degrades to bold in monochrome):

```go
row = marker + highlightMatches(matches[i].Text, matches[i].matchedIdxs)
```

```go
func highlightMatches(text string, idxs []int) string {
	if len(idxs) == 0 {
		return text
	}
	var b strings.Builder
	iSet := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		iSet[i] = true
	}
	for i, r := range []rune(text) {
		if iSet[i] {
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(activeTheme.AccentPrimary).Render(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
```

Note the indices from `fuzzyMatchIndices` are byte positions in the lowercased target; since `Text` is ASCII-dominated (paths/commands), byte positions line up with rune positions for ASCII. For safety, `fuzzyMatchIndices` should index by bytes and `highlightMatches` iterate by byte — simplest: make `highlightMatches` operate on the ASCII prefix only and skip highlighting for non-ASCII text (rare for file paths). Keep it simple and correct rather than clever.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/app/tui/...`
Expected: PASS.

- [ ] **Step 6: Format and commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/
git commit -m "tui: highlight matched chars in completion popup"
```

---

## Task 12: Status line priority collapse (drop segments instead of truncating)

**Files:**
- Modify: `internal/app/tui/status.go` (`renderStatusLine`, `statusLeftSegments`)
- Modify: `internal/app/tui/status_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: when the status line doesn't fit, the lowest-priority left segments are dropped entirely (not mid-string truncated) so the right activity cluster stays anchored.

- [ ] **Step 1: Write the failing test**

```go
func TestStatusLineDropsLowPrioritySegment(t *testing.T) {
	m := newViewTestModel(t, 70, 24)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder-7b", Provider: "ollama", LocalOnly: true})
	m.state.SetContextPack(contextPackWith(1000, 8000)) // assumes a test helper exists
	line := m.renderStatusLine(70)
	// mode + route must remain; ctx segment may drop, but never mid-truncated
	if !strings.Contains(line, "qwen") || !strings.Contains(line, "ollama") {
		t.Fatalf("route dropped on narrow line:\n%s", line)
	}
	// Nothing mid-truncated with a dangling partial token:
	if strings.Contains(line, "ol") && !strings.Contains(line, "ollama") {
		t.Fatalf("route was mid-truncated:\n%s", line)
	}
}
```

(Adapt the context-pack helper to whatever exists; if none, build the pack state directly.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestStatusLineDropsLowPrioritySegment`
Expected: FAIL — current code truncates mid-string.

- [ ] **Step 3: Reimplement with priority collapse**

In `status.go`, change `statusLeftSegments` to return `[]statusSeg` (value + priority) where priority is an int (lower = higher priority). Then `renderStatusLine` drops the lowest-priority segment until the line fits:

```go
type statusSeg struct {
	text     string
	priority int
}

func (m Model) statusLeftSegments() []statusSeg {
	// mode = 0 (always), route = 1, local = 2, ctx = 3, turn = 4, branch = 5, swarm tokens = 6, jobs = 7, queued = 8
	// …build as before, wrapping each entry in statusSeg
}
```

In `renderStatusLine`:

```go
func (m Model) renderStatusLine(width int) string {
	segs := m.statusLeftSegments()
	left := joinSegs(segs)
	right := m.statusRightSegment()
	for len(segs) > 0 && visibleRunes(left)+visibleRunes(right)+statusHorizontalPadding+statusMinGap > width {
		// drop the lowest-priority segment (highest priority number)
		worst := 0
		for i := 1; i < len(segs); i++ {
			if segs[i].priority > segs[worst].priority {
				worst = i
			}
		}
		segs = append(segs[:worst], segs[worst+1:]...)
		left = joinSegs(segs)
	}
	gap := width - visibleRunes(left) - visibleRunes(right) - statusHorizontalPadding
	if gap < statusMinGap {
		gap = statusMinGap
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	return statusBarStyle.Width(max(width, 1)).MaxWidth(max(width, 1)).Render(ansi.Cut(line, 0, width))
}

func joinSegs(segs []statusSeg) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, s.text)
	}
	return strings.Join(parts, dimSeparator)
}
```

- [ ] **Step 4: Update existing status tests**

Tests asserting specific truncation behavior (search `truncate` in `status_test.go`) now expect full-segment drops; update expected strings accordingly.

Run: `go test ./internal/app/tui/...`
Expected: PASS.

- [ ] **Step 5: Format and commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/
git commit -m "tui: drop low-priority status segments instead of mid-truncating"
```

---

## Task 13: Final verification and docs

**Files:**
- Modify: `CLAUDE.md` (update the architecture note to mention `theme` and `help` subpackages)

- [ ] **Step 1: Run the full build and test suite**

```bash
CGO_ENABLED=1 go build ./cmd/marshal
go test ./internal/app/tui/...
go vet ./internal/app/tui/...
gofmt -l internal/app/tui/
```
Expected: build succeeds, all tests pass, no vet warnings, no unformatted files.

- [ ] **Step 2: Update CLAUDE.md package layout**

Add two lines to the architecture block:

```
internal/app/tui/theme/                — semantic color slots with NO_COLOR/16/256 detection
internal/app/tui/help/                 — persistent keybinding footer and ? help overlay
```

- [ ] **Step 3: Commit**

```bash
gofmt -w .
git add CLAUDE.md
git commit -m "docs: note new tui theme and help subpackages"
```

---

## Self-Review

**1. Spec coverage (each diagnosed issue → task):**
- A1 min-size gate → Task 3 ✓
- A2 memory border misalignment → Task 8 ✓
- A3 settings overflow → Task 9 ✓
- A4 fragile dirty hash → deliberately NOT changed (deferred — the hash is a perf optimization, not a correctness bug; replacing it risks regressions and the framework's diff renderer handles atomicity). Acknowledged as out of scope; if breakage persists post-overhaul, revisit. Documented here for honesty.
- B1 spinner cadence → Task 6 ✓
- B2 200ms delay → Task 6 ✓
- B3 status-line truncation → Task 12 ✓
- C1 footer + help → Tasks 4, 5 ✓
- C2 mode indicator → Task 7 ✓
- C3 approval Enter → Task 10 ✓
- C4 matched-char highlight → Task 11 ✓
- C5 NO_COLOR → Task 1 + 2 ✓
- C6 semantic slots → Task 1 + 2 ✓

**2. Placeholder scan:** no TBD/TODO/"similar to". Each step has runnable code or explicit grep-verify commands. Tasks 10-12 reference existing test-helpers with a note to adapt if absent — acceptable since the test helper surface is knowable at execution time.

**3. Type consistency:** `help.FooterHints`, `help.Overlay`, `help.Rows` consistent across Tasks 4-5. `statusSeg` introduced in Task 12 and not referenced elsewhere. `activeTheme` defined in Task 2 and consumed in Tasks 6, 11. `fuzzyMatchIndices` defined in Task 11. Mode helpers (`modeSegment`) Task 7 only. No cross-task naming drift.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-07-11-tui-usability-overhaul.md`. Two execution options:**

**1. Subagent-Driven (recommended)** - dispatch a fresh subagent per task with two-stage review between tasks.

**2. Inline Execution** - execute tasks in this session using executing-plans with batched checkpoints.

**Which approach?**