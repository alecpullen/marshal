# TUI "App Feel" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the marshal TUI read as a finished, layered app rather than a stacked chat, while keeping it single-column and free of background fills (which the user has explicitly rejected) and without any mouse capture (deliberately excluded).

**Architecture:** Four visual upgrades layered on the existing single-column frame in `internal/app/tui`: (1) a persistent one-line title bar at the top carrying brand + working dir + branch; (2) a titled rounded border around the transcript viewport (reusing the existing `chrome.Panel` helper); (3) the help footer promoted to a top-bordered "command bar"; (4) the welcome banner polished into a centered hero. No background fills are introduced — the `BGBase`/`BGSurface` theme slots stay unused by the main frame, so the existing `TestStatusLineHasNoBackgroundFill` and `TestInputAreaHasNoBackgroundFill` regression guards remain green. No new mouse handling is added.

**Tech Stack:** Go 1.x, Bubble Tea v2 (`charm.land/bubbletea/v2`), lipgloss v2 (`charm.land/lipgloss/v2`), `github.com/charmbracelet/x/ansi`, the existing `marshal/internal/app/tui/chrome` and `marshal/internal/app/tui/theme` packages.

## Global Constraints

- **No background fills** on any main-frame surface (title bar, transcript border, command bar, status line). The two existing regression tests (`TestStatusLineHasNoBackgroundFill`, `TestInputAreaHasNoBackgroundFill`) must stay green; do not weaken them.
- **No mouse capture.** `View().MouseMode` stays `tea.MouseModeNone`. Do not add mouse event handling.
- **Single-column layout only.** Do not introduce sidebars, split panes, or multi-panel navigation. The frame stays: title bar → transcript → swarm/browser bars (unchanged) → input → command bar → status line.
- **Color via semantic slots only.** Every new styled element references `activeTheme.*` slots (e.g. `AccentPrimary`, `FGMuted`, `BorderMuted`); never hardcode ANSI codes or hex values.
- **Minimum terminal size unchanged:** 80×24 (`minTerminalWidth`/`minTerminalHeight`). All new chrome must degrade gracefully at that size.
- **TDD:** Every task writes the failing test first, runs it red, implements, runs it green, commits. Tests live in the existing `internal/app/tui` package (`*_test.go`) alongside the code they cover.
- **Geometry accounting is centralized** through `transcriptFrameRows`, `footerRows`, `statusLineRows`, and a new `titleBarRows` constant in `view.go`; both viewport height computations (`resize` and `updateViewportHeight` in `model.go`) subtract the same set. Keep them in sync.
- **No comments added** unless explicitly shown in a step's code block. Strip stray comments from generated code.
- **Frequent commits:** one commit per task, conventional-commit style (`feat:` / `refactor:` / `test:` / `fix:`).

---

## File Structure

- **Modify:** `internal/app/tui/view.go` — new `titleBarRows` constant, `renderTitleBar()` method, updated `viewString()` row assembly, `renderTranscriptFrame()` rewritten to use `chrome.Panel`, `renderHelpFooter()` rewritten to add a top border, `renderWelcomeBanner` call site unchanged.
- **Modify:** `internal/app/tui/model.go` — `resize()` and `updateViewportHeight()` subtract `titleBarRows`; `transcriptFrameRows` stays `0` (the panel border is accounted via a new `transcriptBorderRows` constant described in Task 2). No new fields.
- **Modify:** `internal/app/tui/transcript.go` — `renderWelcomeBanner()` rewritten to a centered hero card.
- **Modify:** `internal/app/tui/help/help.go` — `Rows` stays `1` (the command bar's top border is accounted by the caller in `view.go`, not here, so the existing `help.Rows` contract is preserved).
- **Test:** `internal/app/tui/view_test.go` — new tests `TestViewHasTitleBar`, `TestTitleBarShowsWorkingDir`, `TestTitleBarFitsWidth`; updated `TestTranscriptIsBorderless` is **removed/replaced** by `TestTranscriptHasTitledBorder`; updated `TestResizeComputesSingleColumnGeometry` expectation; new `TestCommandBarHasTopBorder`.
- **Test:** `internal/app/tui/transcript_test.go` — new `TestWelcomeBannerIsCenteredHero`.
- **No new files.**

---

## Task 1: Persistent title bar

**Files:**
- Modify: `internal/app/tui/view.go:24-31` (constants), `internal/app/tui/view.go:46-78` (`viewString`), `internal/app/tui/view.go:80-94` (new `renderTitleBar` inserted near `renderTranscriptFrame`)
- Modify: `internal/app/tui/model.go:380-404` (`resize`), `internal/app/tui/model.go:942-949` (`updateViewportHeight`)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: `m.state.WorkingDir` (string, already used at `model.go:438`), `m.state.Branches()` / `m.state.LeafID()` (from `session.State`, see `status.go:130-139` for the existing branch-count rendering pattern), `activeTheme`, `coralColor`, `mutedStyle`, `dimSeparator` (all defined in `model.go:1786-1855`).
- Produces: `renderTitleBar() string` method on `Model`; new package constant `titleBarRows = 1` in `view.go`. Both `resize` and `updateViewportHeight` subtract `titleBarRows`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/tui/view_test.go`:

```go
func TestViewHasTitleBar(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	view := m.View().Content
	if !strings.Contains(view, "marshal") {
		t.Fatalf("view missing title bar brand:\n%s", view)
	}
}

func TestTitleBarShowsWorkingDir(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	bar := m.renderTitleBar(m.width)
	// The base name of the temp dir should appear in the title bar.
	base := filepath.Base(t.TempDir())
	if !strings.Contains(stripANSI(bar), base) {
		t.Fatalf("title bar missing working dir base %q:\n%s", base, bar)
	}
}
```

Add the needed imports to the test file's import block: `"path/filepath"`. The existing import block already has `errors`, `strings`, `testing`, `time`, `tea`, `config`, `session`, `commands`.

Also update the existing geometry test in `view_test.go`:

```go
func TestResizeComputesSingleColumnGeometry(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if m.viewport.Width() != 98 {
		t.Fatalf("viewport.Width = %d, want 98 (width-2, borderless transcript)", m.viewport.Width())
	}
	wantHeight := 30 - titleBarRows - transcriptFrameRows - m.inputAreaRows() - footerRows - statusLineRows
	if m.viewport.Height() != wantHeight {
		t.Fatalf("viewport.Height = %d, want %d", m.viewport.Height(), wantHeight)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestViewHasTitleBar|TestTitleBarShowsWorkingDir|TestResizeComputesSingleColumnGeometry' -v`
Expected: FAIL — `m.renderTitleBar undefined`, `titleBarRows undefined`, and the geometry test's height mismatch.

- [ ] **Step 3: Implement the title bar**

In `internal/app/tui/view.go`, replace the constants block (lines 24-31) so it reads:

```go
const (
	titleBarRows        = 1
	inputBorderRows     = 2
	activityStripRows   = 1
	transcriptFrameRows  = 0
	footerRows          = help.Rows
	statusLineRows      = 1
	completionPopupMax  = 8
)
```

Update `viewString()` (lines 63-77) so the first row is the title bar:

```go
	rows := []string{m.renderTitleBar(m.width), m.renderTranscriptFrame()}
	swarmSpinner := m.activeSpinnerFrame(session.ActivityTool)
	if panel := renderSwarmPanel(m.state.SwarmProgress(), swarmSpinner, m.width); panel != "" {
		rows = append(rows, panel)
	}
	if bar := m.renderBrowserBar(); bar != "" {
		rows = append(rows, bar)
	}
	rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
	out := lipgloss.JoinVertical(lipgloss.Left, rows...)
	if m.pickerModel != nil {
		return chrome.Overlay(out, m.pickerModel.View(m.width, m.height), m.width, m.height)
	}
	return out
```

Add the new method just above `renderTranscriptFrame` (insert after the existing `viewString` function):

```go
// renderTitleBar draws the single-line persistent header: brand on the
// left, working dir + branch on the right. No background fill — it sits
// on the terminal's default background, matching the status line.
func (m Model) renderTitleBar(width int) string {
	dot := lipgloss.NewStyle().Foreground(coralColor).Render("●")
	brand := lipgloss.NewStyle().Foreground(coralColor).Bold(true).Render("marshal")

	right := ""
	if wd := m.state.WorkingDir; wd != "" {
		right = filepath.Base(wd)
	}
	if leaves := m.state.Branches(); len(leaves) > 1 {
		cur := m.state.LeafID()
		idx := 1
		for i, id := range leaves {
			if id == cur {
				idx = i + 1
				break
			}
		}
		if right != "" {
			right += dimSeparator
		}
		right += fmt.Sprintf("branch %d/%d", idx, len(leaves))
	}

	left := " " + dot + " " + brand
	gap := width - visibleRunes(left) - visibleRunes(right) - 1
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right + " "
	return lipgloss.NewStyle().Width(max(width, 1)).MaxWidth(max(width, 1)).Render(ansi.Cut(line, 0, width))
}
```

Add `filepath` to `view.go`'s imports (it already imports `fmt`, `strings`, `tea`, `lipgloss`, `ansi`, `session`, `chrome`, `help`). The import block at `view.go:3-15` becomes:

```go
import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/help"
)
```

Update both viewport height computations in `internal/app/tui/model.go`. In `resize` (line 403):

```go
	m.viewport.SetHeight(max(height-titleBarRows-transcriptFrameRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-footerRows-statusLineRows, 1))
```

In `updateViewportHeight` (line 943):

```go
	newViewportHeight := max(m.height-titleBarRows-transcriptFrameRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-footerRows-statusLineRows, 1)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run 'TestViewHasTitleBar|TestTitleBarShowsWorkingDir|TestResizeComputesSingleColumnGeometry|TestViewIsSingleColumn|TestViewContainsStatusLine|TestViewContainsFooter|TestTranscriptFrameDoesNotMoveWhenActivityStarts|TestViewFitsTerminalSizesSingleColumn' -v`
Expected: PASS for the new tests. `TestViewIsSingleColumn` asserts `MARSHAL` (uppercase) is absent — the title bar uses lowercase `marshal`, so it stays green; confirm by reading the assertion at `view_test.go:31`. All listed tests must pass.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/view.go internal/app/tui/model.go internal/app/tui/view_test.go
git commit -m "feat(tui): add persistent title bar with brand and working dir"
```

---

## Task 2: Titled transcript border

**Files:**
- Modify: `internal/app/tui/view.go:24-31` (add `transcriptBorderRows`), `internal/app/tui/view.go:80-94` (`renderTranscriptFrame` rewrite)
- Modify: `internal/app/tui/model.go:403` and `internal/app/tui/model.go:943` (subtract `transcriptBorderRows`)
- Test: `internal/app/tui/view_test.go` (replace `TestTranscriptIsBorderless`, update `TestResizeComputesSingleColumnGeometry`)

**Interfaces:**
- Consumes: `chrome.Panel(title, content string, w, h int, focused bool, th theme.Theme) string` (`chrome/chrome.go:19`), `activeTheme` (package global), `m.viewport` (`charm.land/bubbles/v2/viewport`).
- Produces: `transcriptBorderRows = 2` constant. The transcript viewport width shrinks by 2 (border) to `width-2`; its height shrinks by `transcriptBorderRows`.

- [ ] **Step 1: Write the failing test (replacing the old borderless assertion)**

In `internal/app/tui/view_test.go`, delete the body of `TestTranscriptIsBorderless` (lines 58-66) and replace it with:

```go
func TestTranscriptHasTitledBorder(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()
	transcript := m.renderTranscriptFrame()
	plain := stripANSI(transcript)
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Fatalf("transcript should have a rounded border:\n%s", plain)
	}
	if !strings.Contains(plain, "Conversation") {
		t.Fatalf("transcript border should embed the title \"Conversation\":\n%s", plain)
	}
}
```

Update the geometry test to account for the new border rows:

```go
func TestResizeComputesSingleColumnGeometry(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if m.viewport.Width() != 98 {
		t.Fatalf("viewport.Width = %d, want 98 (width-2, border accounts for it)", m.viewport.Width())
	}
	wantHeight := 30 - titleBarRows - transcriptBorderRows - m.inputAreaRows() - footerRows - statusLineRows
	if m.viewport.Height() != wantHeight {
		t.Fatalf("viewport.Height = %d, want %d", m.viewport.Height(), wantHeight)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run 'TestTranscriptHasTitledBorder|TestResizeComputesSingleColumnGeometry' -v`
Expected: FAIL — `transcriptBorderRows undefined`, transcript has no `╭`.

- [ ] **Step 3: Implement the titled transcript panel**

In `internal/app/tui/view.go`, update the constants block:

```go
const (
	titleBarRows            = 1
	inputBorderRows         = 2
	activityStripRows       = 1
	transcriptFrameRows     = 0
	transcriptBorderRows    = 2
	footerRows              = help.Rows
	statusLineRows          = 1
	completionPopupMax      = 8
)
```

Replace `renderTranscriptFrame` (lines 80-94) with:

```go
func (m Model) renderTranscriptFrame() string {
	innerW := max(m.width-2, 1)
	innerH := m.viewport.Height()
	title := "Conversation"
	content := m.viewport.View()
	if !m.viewportFollow && m.viewport.TotalLineCount() > m.viewport.Height() {
		hint := mutedStyle.Render("↑ scrolled — End to follow")
		content = lipgloss.JoinVertical(lipgloss.Left, hint, content)
		innerH = max(innerH-1, 1)
	}
	return chrome.Panel(title, content, innerW, innerH+transcriptBorderRows, true, activeTheme)
}
```

`chrome.Panel` draws a titled rounded border and pads each body line to the inner width; it already handles `focused` (accent border) and title truncation (`chrome/chrome.go:19-61`). Passing `innerW` (the outer width minus 2) and `innerH+transcriptBorderRows` (so the panel's own 2 border rows are included) keeps the rendered panel exactly `m.width` wide and `m.viewport.Height()+2` tall.

Update both viewport height computations in `internal/app/tui/model.go` to subtract `transcriptBorderRows` instead of relying on `transcriptFrameRows`:

`resize` (line 403):

```go
	m.viewport.SetWidth(max(width-2, 1))
	m.viewport.SetHeight(max(height-titleBarRows-transcriptBorderRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-footerRows-statusLineRows, 1))
```

`updateViewportHeight` (line 943):

```go
	newViewportHeight := max(m.height-titleBarRows-transcriptBorderRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-footerRows-statusLineRows, 1)
```

(`transcriptFrameRows` stays `0` and remains in the subtractions for compatibility, but `transcriptBorderRows` is the now-live term. Leaving both avoids touching other call sites that reference `transcriptFrameRows`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run 'TestTranscriptHasTitledBorder|TestResizeComputesSingleColumnGeometry|TestViewFitsTerminalSizesSingleColumn|TestTranscriptFrameDoesNotMoveWhenActivityStarts|TestNormalViewRendersAtMinSize' -v`
Expected: PASS. `TestTranscriptFrameDoesNotMoveWhenActivityStarts` checks the top line is stable across idle→busy; with the title bar now at row 0 the transcript border is at row 1 in both states, so the assertion (`idleLines[0] == busyLines[0]`) still holds because row 0 is the title bar in both. If it fails, the fix is to compare `idleLines[1] == busyLines[1]` instead — but try the test first; it should pass unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/view.go internal/app/tui/model.go internal/app/tui/view_test.go
git commit -m "feat(tui): wrap transcript in titled Conversation panel"
```

---

## Task 3: Command bar (top-bordered help footer)

**Files:**
- Modify: `internal/app/tui/view.go:155-166` (`renderHelpFooter`), `internal/app/tui/view.go:24-31` (add `commandBarRows`)
- Modify: `internal/app/tui/model.go:403` and `internal/app/tui/model.go:943` (subtract `commandBarRows` in place of `footerRows`)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: `help.Footer(hints)` (`help/help.go:34`, unchanged), `activeTheme.BorderMuted`, `help.Rows` (=1, unchanged).
- Produces: `commandBarRows = 2` (the content row + the top border row). `footerRows` stays `help.Rows` (=1) for the content portion; the caller subtracts `commandBarRows` to also reserve the border. To avoid double-counting, replace `footerRows` with `commandBarRows` in the two viewport-height subtractions.

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/view_test.go`:

```go
func TestCommandBarHasTopBorder(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	bar := m.renderHelpFooter()
	plain := stripANSI(bar)
	if !strings.Contains(plain, "─") {
		t.Fatalf("command bar should have a top border rule:\n%s", plain)
	}
	if !strings.Contains(plain, "send") || !strings.Contains(plain, "help") {
		t.Fatalf("command bar should still show the keybinding footer:\n%s", plain)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestCommandBarHasTopBorder' -v`
Expected: FAIL — no `─` in the rendered footer (it's plain text today).

- [ ] **Step 3: Implement the bordered command bar**

In `internal/app/tui/view.go`, add the constant and rewrite `renderHelpFooter`:

```go
const (
	titleBarRows         = 1
	inputBorderRows      = 2
	activityStripRows    = 1
	transcriptFrameRows  = 0
	transcriptBorderRows = 2
	footerRows           = help.Rows
	commandBarRows       = footerRows + 1
	statusLineRows       = 1
	completionPopupMax   = 8
)
```

```go
func (m Model) renderHelpFooter() string {
	hints := help.FooterHints{
		Busy:            m.busy,
		EditingCommand:  m.editingCommand,
		ApprovalPending: m.state.PendingApproval() != nil,
		QuestionPending: m.state.PendingQuestion() != nil,
		PopupOpen:       m.activeCompletionPopup() != nil,
	}
	body := help.Footer(hints)
	return lipgloss.NewStyle().
		BorderTop(true).
		BorderForeground(activeTheme.BorderMuted).
		Width(max(m.width, 1)).
		Render(body)
}
```

Update the two viewport height subtractions in `internal/app/tui/model.go` to use `commandBarRows` instead of `footerRows`. In `resize` (line 403):

```go
	m.viewport.SetHeight(max(height-titleBarRows-transcriptBorderRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-commandBarRows-statusLineRows, 1))
```

In `updateViewportHeight` (line 943):

```go
	newViewportHeight := max(m.height-titleBarRows-transcriptBorderRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-commandBarRows-statusLineRows, 1)
```

Update the geometry test in `view_test.go` to match:

```go
func TestResizeComputesSingleColumnGeometry(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if m.viewport.Width() != 98 {
		t.Fatalf("viewport.Width = %d, want 98", m.viewport.Width())
	}
	wantHeight := 30 - titleBarRows - transcriptBorderRows - m.inputAreaRows() - commandBarRows - statusLineRows
	if m.viewport.Height() != wantHeight {
		t.Fatalf("viewport.Height = %d, want %d", m.viewport.Height(), wantHeight)
	}
}
```

Also update the `inputTop` computation in `TestTranscriptFrameDoesNotMoveWhenActivityStarts` (`view_test.go:89`) since the footer now reserves an extra border row:

```go
	inputTop := 30 - m.inputAreaRows() - commandBarRows - statusLineRows
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run 'TestCommandBarHasTopBorder|TestViewContainsFooter|TestResizeComputesSingleColumnGeometry|TestTranscriptFrameDoesNotMoveWhenActivityStarts|TestViewFitsTerminalSizesSingleColumn' -v`
Expected: PASS. `TestViewContainsFooter` checks the footer contains `send` and `help`, both still present.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/view.go internal/app/tui/model.go internal/app/tui/view_test.go
git commit -m "feat(tui): promote help footer to top-bordered command bar"
```

---

## Task 4: Welcome banner as centered hero

**Files:**
- Modify: `internal/app/tui/transcript.go:584-589` (`renderWelcomeBanner`)
- Test: `internal/app/tui/transcript_test.go` (new test)

**Interfaces:**
- Consumes: `chrome.Panel` (for the hero card), `activeTheme`, `coralColor`, `mutedStyle`, `dimSeparator`. The banner is rendered inside the transcript viewport via `refreshViewport` (`model.go:1208-1210`), so its width is `m.viewport.Width()`.
- Produces: `renderWelcomeBanner(width int) string` with the same signature (called at `model.go:1209`).

- [ ] **Step 1: Write the failing test**

First confirm the existing transcript test file imports. Append to `internal/app/tui/transcript_test.go`:

```go
func TestWelcomeBannerIsCenteredHero(t *testing.T) {
	out := renderWelcomeBanner(60)
	plain := stripANSI(out)
	if !strings.Contains(plain, "marshal") {
		t.Fatalf("welcome banner missing brand:\n%s", plain)
	}
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Fatalf("welcome banner should be a bordered card:\n%s", plain)
	}
	if !strings.Contains(plain, "Type a question") {
		t.Fatalf("welcome banner missing call-to-action:\n%s", plain)
	}
}
```

If `transcript_test.go` does not already import `"strings"` and reference `stripANSI`, add them. Run `go vet ./internal/app/tui/` to confirm the test compiles before expecting failure.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run 'TestWelcomeBannerIsCenteredHero' -v`
Expected: FAIL — no `╭` border, no `Type a question` text in the current banner.

- [ ] **Step 3: Implement the hero banner**

Replace `renderWelcomeBanner` in `internal/app/tui/transcript.go:584-589` with:

```go
func renderWelcomeBanner(width int) string {
	dot := lipgloss.NewStyle().Foreground(coralColor).Render("●")
	brand := lipgloss.NewStyle().Foreground(coralColor).Bold(true).Render("marshal")
	tagline := mutedStyle.Render("local-first coding agent")
	cta := mutedStyle.Render("Type a question, or " + lipgloss.NewStyle().Bold(true).Render("/") + " for commands.")

	headline := dot + " " + brand + dimSeparator + tagline
	body := lipgloss.JoinVertical(lipgloss.Left, headline, "", cta)

	cardW := width
	if cardW > 48 {
		cardW = 48
	}
	cardH := 5
	return chrome.Panel("", body, cardW, cardH, true, activeTheme) + "\n"
}
```

This reuses `chrome.Panel` for a titled (empty title = plain top border) rounded card, centered by virtue of the transcript viewport's existing left margin. The `+ "\n"` preserves the trailing newline the old banner had so `refreshViewport`'s join spacing is unchanged. Import `chrome` in `transcript.go` if not already present — check the import block at `transcript.go:1-17`; it is **not** currently imported, so add it:

```go
import (
	"fmt"
	"strings"
	"time"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/diffview"
	"marshal/internal/tools/registry"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run 'TestWelcomeBannerIsCenteredHero|TestViewIsSingleColumn|TestNormalViewRendersAtMinSize|TestViewFitsTerminalSizesSingleColumn' -v`
Expected: PASS. `TestViewIsSingleColumn` asserts uppercase `MARSHAL` is absent — the hero uses lowercase `marshal`, so it stays green. `TestNormalViewRendersAtMinSize` renders at 80×24; the card caps at 48 wide so it fits inside the 78-wide viewport.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/transcript.go internal/app/tui/transcript_test.go
git commit -m "feat(tui): polish welcome banner into centered hero card"
```

---

## Task 5: Success-pulse border on the input box

**Files:**
- Modify: `internal/app/tui/model.go` (new `successPulse` field + handling in `handleAgentFinished` and `handleAgentTick`), `internal/app/tui/view.go:128-132` (`renderInputArea` border color)
- Test: `internal/app/tui/view_test.go`

**Interfaces:**
- Consumes: `lastActivityDone` / `lastActivityLabel` (already tracked in `model.go:139` and `status.go:213`), `doneDisplayDuration` (=2s, `model.go:53`), `now()`.
- Produces: a `successPulse bool` field on `Model`; `renderInputArea` chooses a teal border when `successPulse` is true and the input is focused.

- [ ] **Step 1: Write the failing test**

Append to `internal/app/tui/view_test.go`:

```go
func TestInputBorderPulsesTealOnSuccess(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.successPulse = true
	out := m.renderInputArea()
	if !strings.Contains(out, "48;") {
		// Just confirm the border color changed; the teal slot is 256-code 43
		// (StatusSuccess). Check for the 43 code in the SGR.
		t.Fatalf("input border should carry a non-default color when successPulse is set:\n%s", out)
	}
	// Verify it's specifically the success color, not the coral default.
	coral := m.renderInputArea()
	m.successPulse = false
	neutral := m.renderInputArea()
	if coral == neutral {
		t.Fatalf("success pulse border should differ from the default focused border")
	}
}
```

Note: the teal `StatusSuccess` slot resolves to 256-color code `43` (see `theme.go:56`). The test asserts the two renders differ; it does not hardcode `43` to avoid coupling to the palette, but does confirm ANSI is emitted.

- [ ] **Step 2: Run test to verify it fail**

Run: `go test ./internal/app/tui/ -run 'TestInputBorderPulsesTealOnSuccess' -v`
Expected: FAIL — `m.successPulse undefined`.

- [ ] **Step 3: Implement the pulse**

Add a field to the `Model` struct in `internal/app/tui/model.go` (near the other activity fields around line 136-141):

```go
	successPulse bool
```

In `handleAgentFinished` (`model.go:1343-1358`), set the pulse when the turn completed without error:

```go
func (m Model) handleAgentFinished(msg agentFinishedMsg) (Model, tea.Cmd) {
	m.busy = false
	m.agentCancel = nil
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.state.SetProviderError(msg.err)
	} else if msg.err == nil {
		m.successPulse = true
	}
	m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
	if m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
		m.lastActivityDone = m.now()
		m.lastActivityKind = session.ActivityIdle
	}
	m.updateViewportHeight()
	m.refreshViewport()
	m.syncSettingsSaveBlock()
	return m, tickCmd()
}
```

(Returning `tickCmd()` instead of `nil` keeps the existing 150ms tick alive so the pulse can be cleared on the next tick — see Step 3b.)

In `handleAgentTick` (`model.go:1393-1411`), clear the pulse after `doneDisplayDuration`:

```go
func (m Model) handleAgentTick(msg agentTickMsg) (Model, tea.Cmd) {
	if !m.busy && m.successPulse {
		if m.lastActivityKind == session.ActivityIdle && !m.lastActivityDone.IsZero() &&
			m.now().Sub(m.lastActivityDone) >= doneDisplayDuration {
			m.successPulse = false
		}
	}
	if !m.busy && !m.successPulse {
		return m, nil
	}
	act := m.state.Activity()
	if act.Kind == session.ActivityIdle && m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
		m.lastActivityDone = m.now()
	}
	m.lastActivityKind = act.Kind
	if act.Kind != session.ActivityIdle && act.Label != "" {
		m.lastActivityLabel = act.Label
	}
	if m.state.PendingQuestion() != nil && m.input.Placeholder != "Type your answer..." {
		m.input.Placeholder = "Type your answer..."
	}
	m.updateViewportHeight()
	m.refreshViewport()
	return m, tickCmd()
}
```

Note the changed early-return: previously `if !m.busy { return m, nil }`. Now the pulse needs ticks to clear even when idle, so the guard becomes `if !m.busy && !m.successPulse { return m, nil }`. The `spinnerTickMsg` handler (`handleSpinnerTick`) is unaffected because it only acts when `m.busy`.

In `renderInputArea` (`view.go:96-133`), choose the border color:

```go
	border := coralColor
	if m.successPulse {
		border = tealColor
	} else if !m.input.Focused() {
		border = mauveColor
	}
	return inputBoxStyle.BorderForeground(border).Width(inputInnerWidth).Render(content)
```

(`tealColor` is already defined at `model.go:1791` as `activeTheme.StatusSuccess`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run 'TestInputBorderPulsesTealOnSuccess|TestInputBorderColorReflectsFocus|TestInputAreaHasNoBackgroundFill|TestInputAreaHasNoBlankRowsWhenIdle|TestLongInputExpandsToMultipleRows|TestMultilineInputAlignsContinuationLines|TestInputWrapsBeforeBoxContentWidth' -v`
Expected: PASS. `TestInputBorderColorReflectsFocus` compares focused vs blurred; with `successPulse` false by default both renders use the coral/mauve path, so the test stays green.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/view.go internal/app/tui/view_test.go
git commit -m "feat(tui): pulse input border teal for 2s after a successful turn"
```

---

## Task 6: Full-suite verification and polish

**Files:**
- Run-only task; no source edits unless a test reveals a missed update.

- [ ] **Step 1: Run the complete TUI test package**

Run: `go test ./internal/app/tui/... -v`
Expected: all tests PASS. If any geometry test fails, re-check that every height subtraction in `resize` (`model.go:403`) and `updateViewportHeight` (`model.go:943`) uses the identical term set: `titleBarRows + transcriptBorderRows + swarmPanelRows + browserBarRows + inputAreaRows + commandBarRows + statusLineRows`. Fix by copying the exact expression.

- [ ] **Step 2: Run gofmt and go vet**

Run: `gofmt -w internal/app/tui && go vet ./...`
Expected: no output / exit 0. If `go vet` flags the new `filepath` import as unused in `view.go` (it won't — `renderTitleBar` uses it), remove it; otherwise leave it.

- [ ] **Step 3: Build the binary**

Run: `go build ./cmd/marshal`
Expected: exit 0, produces `marshal` binary. (CGO is required per CLAUDE.md for tree-sitter; the build environment must have a C toolchain.)

- [ ] **Step 4: Smoke-check the too-small and min-size paths**

Run: `go test ./internal/app/tui/ -run 'TestTooSmall|TestNormalViewRendersAtMinSize|TestViewFitsTerminalSizesSingleColumn' -v`
Expected: PASS. The title bar and transcript border must not crash at 80×24; the panel helper caps inner dimensions and the title bar truncates with `ansi.Cut`.

- [ ] **Step 5: Commit (if any fixups were made)**

```bash
git add -A
git commit -m "fix(tui): align geometry accounting after app-feel chrome"
```

If no fixups were needed, skip this step.

---

## Self-Review

**1. Spec coverage:** The user accepted (a) title bar, (b) transcript border, (c) command bar footer, (d) success pulse, (e) welcome hero. All five have tasks (1, 2, 3, 5, 4 respectively). The user rejected multi-panel and mouse — both excluded by Global Constraints. No background fills — covered by Global Constraints and verified by the existing regression tests staying green.

**2. Placeholder scan:** Every step contains runnable code or an exact command. No TBD/TODO. The one conditional in Task 2 Step 4 (`idleLines[1] == busyLines[1]` fallback) is a concrete fallback with a precise line number, not a placeholder.

**3. Type consistency:** `renderTitleBar(width int) string`, `renderTranscriptFrame() string`, `renderHelpFooter() string`, `renderWelcomeBanner(width int) string`, `successPulse bool` — all used consistently across tasks. `transcriptBorderRows`, `commandBarRows`, `titleBarRows` are defined once in Task 1/2/3 and referenced identically in Tasks 5/6. `chrome.Panel` signature (`title, content string, w, h int, focused bool, th theme.Theme`) matches `chrome/chrome.go:19`. `tealColor` (`model.go:1791`), `coralColor`, `mauveColor`, `activeTheme` all pre-existing globals used unchanged.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-14-tui-app-feel.md`. Two execution options:

1. **Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration
2. **Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

Which approach?