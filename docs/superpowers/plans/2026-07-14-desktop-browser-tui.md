# Desktop Browser Automation TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add visually distinct browser tool rendering, a persistent browser status bar, and a status line segment to Marshal's TUI for the desktop browser automation feature.

**Architecture:** A new `BrowserInfo` struct on `session.State` carries live browser state (URL, title, mode, active tool, session-open flag) from the desktop tool handlers to the TUI. The TUI reads it on each render tick — no event-driven re-rendering needed (the existing `agentTickMsg`/`spinnerTickMsg` periodic timers already drive re-renders). A new `browserbar.go` file renders a one-line bar below the transcript. Transcript rendering gains browser-aware glyph/prefix styling. The status line gets a new low-priority segment.

**Tech Stack:** Go, `charm.land/lipgloss/v2`, existing `marshal/internal/app/session`, `marshal/internal/app/tui`, `marshal/internal/tools/desktop`.

## Global Constraints

- This plan builds on top of the `feat/desktop-browser-automation` branch — the desktop package (`internal/tools/desktop/`) already exists there with six `browser.*` tools.
- No comments in code unless explicitly requested by the spec's design.
- Browser tools use `🌐` glyph in gold (`AccentTertiary`, same as `⏺`) plus `browser` prefix in violet (`AccentSecondary`).
- The browser bar is display-only — no keybindings, no interaction.
- The status line browser segment is the lowest-priority segment (priority 9 — after queued at 8), dropped first on narrow terminals.
- Existing tests must stay green — all changes are conditional additions.
- The `SessionState` field on `desktop.Options` must be nil-safe (no-ops when nil, so existing unit tests that pass nil continue to work).

---

## File Structure

```
Modified files:
  internal/app/session/session.go       — BrowserInfo struct, EventBrowserChanged constant, Browser field on Event, State.browser field, SetBrowserInfo/BrowserInfo methods
  internal/app/session/session_test.go   — BrowserInfo round-trip and default tests
  internal/app/tui/model.go              — browserGlyphStyle, browserPrefixStyle, browserBarStyle, urlStyle style vars + loadTheme() assignments + browserBarRows() helper
  internal/app/tui/transcript.go         — isBrowserTool() helper, browser-aware renderActiveToolCall/renderCompletedToolCall paths
  internal/app/tui/transcript_test.go     — browser glyph/prefix tests
  internal/app/tui/status.go             — browserStatusText() helper, statusLeftSegments() browser segment
  internal/app/tui/status_test.go         — browser segment visibility and drop-priority tests
  internal/app/tui/view.go               — renderBrowserBar() call in viewString() rows
  internal/tools/desktop/types.go        — SessionState field on Options
  internal/tools/desktop/tools.go        — sessionState field on toolSet, updateBrowserState() helper, injection points in getSession + each tool handler
  internal/app/app.go                     — SessionState: state in desktop.Options

New files:
  internal/app/tui/browserbar.go          — renderBrowserBar(), truncateURL(), dimSep() helpers
  internal/app/tui/browserbar_test.go     — browser bar rendering tests
```

---

### Task 1: Add BrowserInfo to session state

**Files:**
- Modify: `internal/app/session/session.go`
- Test: `internal/app/session/session_test.go`

**Interfaces:**
- Consumes: existing `State` struct, `publishEvent` method, `Event` struct.
- Produces: `BrowserInfo` struct, `EventBrowserChanged` constant, `Browser` field on `Event`, `State.browser` field, `SetBrowserInfo(BrowserInfo)`, `BrowserInfo() BrowserInfo` methods. Later tasks (2, 3, 4, 5, 6) consume these.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/session/session_test.go` (append after the last test function):

```go
func TestBrowserInfoDefault(t *testing.T) {
	state := newTestState()
	bi := state.BrowserInfo()
	if bi.SessionOpen {
		t.Fatal("new state should have SessionOpen=false")
	}
	if bi.Active {
		t.Fatal("new state should have Active=false")
	}
}

func TestSetBrowserInfo(t *testing.T) {
	state := newTestState()
	info := BrowserInfo{
		SessionOpen: true,
		Active:      true,
		ToolName:    "browser.navigate",
		URL:         "https://example.com/docs",
		Title:       "Example Docs",
		Mode:        "standalone",
	}
	state.SetBrowserInfo(info)
	got := state.BrowserInfo()
	if !got.SessionOpen {
		t.Error("SessionOpen not set")
	}
	if got.URL != "https://example.com/docs" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.Title != "Example Docs" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Mode != "standalone" {
		t.Errorf("Mode = %q", got.Mode)
	}
	if !got.Active {
		t.Error("Active not set")
	}
	if got.ToolName != "browser.navigate" {
		t.Errorf("ToolName = %q", got.ToolName)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/session/ -run TestBrowserInfo -v`
Expected: FAIL — `BrowserInfo` and `SetBrowserInfo` undefined.

- [ ] **Step 3: Add BrowserInfo struct and event constant**

In `internal/app/session/session.go`, after the `SandboxInfo` struct (around line 245, after the struct's closing brace), add:

```go
type BrowserInfo struct {
	Active      bool
	ToolName    string
	URL         string
	Title       string
	Mode        string
	SessionOpen bool
	UpdatedAt   time.Time
}
```

In the event constants block (around line 35-41), after `EventPendingQuestionChanged`, add:

```go
	EventBrowserChanged          = "browser_changed"
```

In the `Event` struct (around line 48-56), after `PendingQuestion *PendingQuestion`, add:

```go
	Browser         *BrowserInfo
```

- [ ] **Step 4: Add State.browser field**

In `internal/app/session/session.go`, in the `State` struct's private fields block, after `sandbox SandboxInfo` (around line 282), add:

```go
	browser         BrowserInfo
```

- [ ] **Step 5: Add SetBrowserInfo and BrowserInfo methods**

In `internal/app/session/session.go`, after the `SandboxInfo()` method (around line 577), add:

```go
func (s *State) SetBrowserInfo(info BrowserInfo) {
	s.mu.Lock()
	s.browser = info
	s.mu.Unlock()
	bi := info
	s.publishEvent(EventBrowserChanged, Event{Browser: &bi})
}

func (s *State) BrowserInfo() BrowserInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browser
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/app/session/ -run TestBrowserInfo -v`
Expected: PASS — both tests pass.

- [ ] **Step 7: Run full session test suite**

Run: `go test ./internal/app/session/ -v 2>&1 | tail -5`
Expected: PASS — all existing tests still pass.

- [ ] **Step 8: Format and vet**

Run: `gofmt -w internal/app/session/session.go internal/app/session/session_test.go && go vet ./internal/app/session/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): add BrowserInfo state for browser automation TUI"
```

---

### Task 2: Add browser styles to TUI model

**Files:**
- Modify: `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go` (existing test file — just verify build)

**Interfaces:**
- Consumes: `activeTheme` (existing global, set by `loadTheme()`).
- Produces: `browserGlyphStyle`, `browserPrefixStyle`, `browserBarStyle`, `urlStyle` package-level `lipgloss.Style` variables. Also `browserBarRows` constant. Tasks 3, 4, 5, 6 consume these.

- [ ] **Step 1: Add style variables to the var block**

In `internal/app/tui/model.go`, in the package-level `var` block (around line 1794-1804), after `statusBarStyle lipgloss.Style`, add:

```go
	browserGlyphStyle  lipgloss.Style
	browserPrefixStyle lipgloss.Style
	browserBarStyle    lipgloss.Style
	urlStyle           lipgloss.Style
```

- [ ] **Step 2: Add browserBarRows constant**

In `internal/app/tui/model.go`, in the `const` block (around line 24-31, after `statusLineRows`), add:

```go
	browserBarRows = 1
```

- [ ] **Step 3: Add style assignments to loadTheme()**

In `internal/app/tui/model.go`, in `loadTheme()` (around line 1847, after `statusBarStyle = ...`), add:

```go
	browserGlyphStyle = lipgloss.NewStyle().
		Foreground(activeTheme.AccentTertiary)
	browserPrefixStyle = lipgloss.NewStyle().
		Foreground(activeTheme.AccentSecondary)
	browserBarStyle = lipgloss.NewStyle().
		Background(activeTheme.BGSurface).
		BorderTop(true).
		BorderForeground(activeTheme.BorderMuted)
	urlStyle = lipgloss.NewStyle().
		Foreground(activeTheme.FGDefault)
```

- [ ] **Step 4: Add browserBarRows() helper method**

In `internal/app/tui/model.go`, after `swarmPanelRows()` (around line 931), add:

```go
func (m Model) browserBarRows() int {
	if m.state.BrowserInfo().SessionOpen {
		return browserBarRows
	}
	return 0
}
```

- [ ] **Step 5: Update viewport height calculations**

In `internal/app/tui/model.go`, in `resize()` (around line 401), the current line is:

```go
	m.viewport.SetHeight(max(height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-footerRows-statusLineRows, 1))
```

Change to:

```go
	m.viewport.SetHeight(max(height-transcriptFrameRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-footerRows-statusLineRows, 1))
```

In `updateViewportHeight()` (around line 934), the current line is:

```go
	newViewportHeight := max(m.height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-footerRows-statusLineRows, 1)
```

Change to:

```go
	newViewportHeight := max(m.height-transcriptFrameRows-m.swarmPanelRows()-m.browserBarRows()-m.inputAreaRows()-footerRows-statusLineRows, 1)
```

- [ ] **Step 6: Build and verify**

Run: `go build ./internal/app/tui/...`
Expected: PASS — compiles cleanly.

- [ ] **Step 7: Run existing TUI tests**

Run: `go test ./internal/app/tui/ -short -v 2>&1 | tail -10`
Expected: PASS — all existing tests still pass (browserBarRows returns 0 when no browser session).

- [ ] **Step 8: Format and vet**

Run: `gofmt -w internal/app/tui/model.go && go vet ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/app/tui/model.go
git commit -m "feat(tui): add browser styles and browserBarRows viewport accounting"
```

---

### Task 3: Browser-aware transcript rendering

**Files:**
- Modify: `internal/app/tui/transcript.go`
- Test: `internal/app/tui/transcript_test.go`

**Interfaces:**
- Consumes: `browserGlyphStyle`, `browserPrefixStyle` (Task 2), `session.ActiveToolCall`, `registry.AuditEvent` (existing).
- Produces: `isBrowserTool(name string) bool` helper. Modified `renderActiveToolCall` and `renderCompletedToolCall` that render browser tools with `🌐` glyph + violet `browser` prefix.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/tui/transcript_test.go` (append after the last test function):

```go
func TestRenderActiveToolCallBrowserGlyph(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "browser.navigate",
		Args:      "https://example.com",
		StartedAt: time.Unix(100, 0),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), 80)
	stripped := stripANSI(out)
	if !strings.Contains(stripped, "🌐") {
		t.Fatalf("browser active tool call missing 🌐 glyph:\n%s", out)
	}
	if !strings.Contains(stripped, "browser.navigate") {
		t.Fatalf("missing tool name:\n%s", out)
	}
}

func TestRenderActiveToolCallNonBrowserGlyph(t *testing.T) {
	atc := session.ActiveToolCall{
		Name:      "file.read",
		Args:      "src/main.go",
		StartedAt: time.Unix(100, 0),
	}
	out := renderActiveToolCall(atc, session.SandboxInfo{}, false, "⠋", time.Unix(103, 0), 80)
	stripped := stripANSI(out)
	if !strings.Contains(stripped, "⏺") {
		t.Fatalf("non-browser active tool call missing ⏺ glyph:\n%s", out)
	}
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("non-browser tool should not have 🌐:\n%s", out)
	}
}

func TestRenderCompletedToolCallBrowserGlyph(t *testing.T) {
	event := registry.AuditEvent{
		ToolName:      "browser.navigate",
		ResultSummary: "Navigated to https://example.com",
	}
	out := renderCompletedToolCall(event, 80)
	stripped := stripANSI(out)
	if !strings.Contains(stripped, "browser.navigate") {
		t.Fatalf("missing tool name:\n%s", out)
	}
	if !strings.Contains(stripped, "done") {
		t.Fatalf("missing 'done':\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run "TestRenderActiveToolCallBrowser|TestRenderCompletedToolCallBrowser" -v`
Expected: FAIL — the existing `renderActiveToolCall` always uses `⏺`, so `TestRenderActiveToolCallBrowserGlyph` fails (no `🌐` in output). `TestRenderActiveToolCallNonBrowserGlyph` and `TestRenderCompletedToolCallBrowserGlyph` may pass already since they don't check for glyph absence in the completed case — but the test is still valid.

- [ ] **Step 3: Add isBrowserTool helper**

In `internal/app/tui/transcript.go`, after the import block (around line 17), add:

```go
func isBrowserTool(name string) bool {
	return strings.HasPrefix(name, "browser.")
}
```

- [ ] **Step 4: Modify renderActiveToolCall for browser tools**

In `internal/app/tui/transcript.go`, in `renderActiveToolCall` (around line 384-406), the current function starts with:

```go
func renderActiveToolCall(atc session.ActiveToolCall, sb session.SandboxInfo, allowNetwork bool, spinnerFrame string, now time.Time, width int) string {
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	head := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", atc.Name, formatElapsed(elapsed)))
	var b strings.Builder
	b.WriteString(toolBulletStyle.Render(truncateRunes(head, max(width-2, 1))))
```

Replace the `head` and first `b.WriteString` lines with browser-aware rendering:

```go
func renderActiveToolCall(atc session.ActiveToolCall, sb session.SandboxInfo, allowNetwork bool, spinnerFrame string, now time.Time, width int) string {
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	head := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", atc.Name, formatElapsed(elapsed)))
	var b strings.Builder
	if isBrowserTool(atc.Name) {
		b.WriteString(browserGlyphStyle.Render("🌐"))
		b.WriteString(" ")
		prefixed := browserPrefixStyle.Render("browser") + "." + strings.TrimPrefix(atc.Name, "browser.")
		full := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", prefixed, formatElapsed(elapsed)))
		b.WriteString(truncateRunes(full, max(width-4, 1)))
	} else {
		b.WriteString(toolBulletStyle.Render(truncateRunes(head, max(width-2, 1))))
	}
```

The rest of the function (the `if atc.Name == "shell.run"` block and the continuation lines) stays unchanged.

- [ ] **Step 5: Modify renderCompletedToolCall for browser tools**

In `internal/app/tui/transcript.go`, in `renderCompletedToolCall` (around line 408-433), the current function starts with:

```go
func renderCompletedToolCall(event registry.AuditEvent, width int) string {
	glyph := "✔"
	style := statusOkStyle
	state := "done"
	if event.Error != "" {
		glyph = "✘"
		style = statusErrStyle
		state = "failed"
	}
	head := fmt.Sprintf("%s %s %s", glyph, event.ToolName, state)
```

Replace the `head` line with browser-aware rendering:

```go
func renderCompletedToolCall(event registry.AuditEvent, width int) string {
	glyph := "✔"
	style := statusOkStyle
	state := "done"
	if event.Error != "" {
		glyph = "✘"
		style = statusErrStyle
		state = "failed"
	}
	var head string
	if isBrowserTool(event.ToolName) {
		head = fmt.Sprintf("%s %s %s", glyph, browserPrefixStyle.Render("browser")+"."+strings.TrimPrefix(event.ToolName, "browser."), state)
	} else {
		head = fmt.Sprintf("%s %s %s", glyph, event.ToolName, state)
	}
```

The rest of the function stays unchanged.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run "TestRenderActiveToolCallBrowser|TestRenderActiveToolCallNonBrowser|TestRenderCompletedToolCallBrowser" -v`
Expected: PASS — all three tests pass.

- [ ] **Step 7: Run full TUI test suite**

Run: `go test ./internal/app/tui/ -short -v 2>&1 | tail -10`
Expected: PASS — all existing tests still pass.

- [ ] **Step 8: Format and vet**

Run: `gofmt -w internal/app/tui/transcript.go internal/app/tui/transcript_test.go && go vet ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/app/tui/transcript.go internal/app/tui/transcript_test.go
git commit -m "feat(tui): browser-aware glyph and prefix in transcript rendering"
```

---

### Task 4: Browser bar rendering

**Files:**
- Create: `internal/app/tui/browserbar.go`
- Test: `internal/app/tui/browserbar_test.go`

**Interfaces:**
- Consumes: `session.BrowserInfo` (Task 1), `browserGlyphStyle`, `browserBarStyle`, `urlStyle` (Task 2), `m.activeSpinnerFrame` (existing), `m.state.BrowserInfo()` (Task 1), `truncateRunes` (existing).
- Produces: `renderBrowserBar() string` method on `Model`, `truncateURL(url string, max int) string` helper, `dimSep(text string) string` helper. Task 5 and 6 consume `truncateURL` and `dimSep`.

- [ ] **Step 1: Write the failing tests**

Create `internal/app/tui/browserbar_test.go`:

```go
package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func newBrowserBarTestModel(t *testing.T) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	return m
}

func TestRenderBrowserBarHiddenWhenNoSession(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{SessionOpen: false})
	if bar := m.renderBrowserBar(); bar != "" {
		t.Fatalf("expected empty bar, got:\n%s", bar)
	}
}

func TestRenderBrowserBarShowsURLTitleMode(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/docs",
		Title:       "Example Docs",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	for _, want := range []string{"example.com/docs", "Example Docs", "standalone"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("bar missing %q:\n%s", want, bar)
		}
	}
}

func TestRenderBrowserBarStripsProtocol(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	if strings.Contains(stripped, "https://") {
		t.Fatalf("bar should strip protocol:\n%s", bar)
	}
	if !strings.Contains(stripped, "example.com") {
		t.Fatalf("bar should contain hostname:\n%s", bar)
	}
}

func TestRenderBrowserBarShowsSpinnerWhenActive(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(105, 0) }
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		Active:      true,
		ToolName:    "browser.click",
		URL:         "https://example.com",
		Mode:        "standalone",
		UpdatedAt:   time.Unix(100, 0),
	})
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "browser.click", StartedAt: time.Unix(100, 0)})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	if !strings.Contains(stripped, "browser.click") {
		t.Fatalf("bar should show active tool name:\n%s", bar)
	}
}

func TestRenderBrowserBarHidesSpinnerWhenIdle(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		Active:      false,
		URL:         "https://example.com",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	if strings.Contains(stripped, "browser.") {
		t.Fatalf("idle bar should not contain tool name:\n%s", bar)
	}
}

func TestRenderBrowserBarNarrowWidth(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.resize(25, 30)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/some/very/long/path",
		Title:       "Very Long Title That Exceeds Width",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	if bar == "" {
		t.Fatal("bar should not be empty even on narrow width")
	}
}

func TestTruncateURL(t *testing.T) {
	cases := []struct {
		url  string
		max  int
		want string
	}{
		{"https://example.com", 30, "example.com"},
		{"http://example.com", 30, "example.com"},
		{"https://example.com", 0, "example.com"},
		{"https://developer.example.com/very/long/path/to/somewhere", 25, "developer.example.com/…/somewhere"},
		{"https://example.com/short", 30, "example.com/short"},
		{"", 30, ""},
	}
	for _, c := range cases {
		got := truncateURL(c.url, c.max)
		if c.want == "" && got == "" {
			continue
		}
		if got != c.want {
			t.Errorf("truncateURL(%q, %d) = %q, want %q", c.url, c.max, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run "TestRenderBrowserBar|TestTruncateURL" -v`
Expected: FAIL — `renderBrowserBar`, `truncateURL` undefined.

- [ ] **Step 3: Write browserbar.go**

Create `internal/app/tui/browserbar.go`:

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

func (m Model) renderBrowserBar() string {
	bi := m.state.BrowserInfo()
	if !bi.SessionOpen {
		return ""
	}

	available := max(m.width-4, 1)
	var b strings.Builder
	b.WriteString(browserGlyphStyle.Render("🌐"))
	b.WriteString(" ")
	b.WriteString(urlStyle.Render(truncateURL(bi.URL, available)))

	if bi.Title != "" {
		b.WriteString(dimSep(bi.Title))
	}
	b.WriteString(dimSep(bi.Mode))

	if bi.Active {
		spinner := m.activeSpinnerFrame(session.ActivityTool)
		b.WriteString(dimSep(spinnerLabel(spinner, bi.ToolName)))
	}

	line := b.String()
	return browserBarStyle.
		Width(max(m.width, 1)).
		MaxWidth(max(m.width, 1)).
		Render(ansi.Cut(" "+line+" ", 0, m.width))
}

func dimSep(text string) string {
	return mutedStyle.Render(" · ") + text
}

func truncateURL(raw string, max int) string {
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if max <= 0 || len(raw) <= max {
		return raw
	}
	if max < 8 {
		return raw[:max]
	}
	hostEnd := strings.Index(raw, "/")
	if hostEnd < 0 {
		hostEnd = len(raw)
	}
	host := raw[:hostEnd]
	suffix := raw[max-3:]
	return host + "/…" + suffix
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run "TestRenderBrowserBar|TestTruncateURL" -v`
Expected: PASS — all 7 tests pass.

- [ ] **Step 5: Run full TUI test suite**

Run: `go test ./internal/app/tui/ -short -v 2>&1 | tail -10`
Expected: PASS

- [ ] **Step 6: Format and vet**

Run: `gofmt -w internal/app/tui/browserbar.go internal/app/tui/browserbar_test.go && go vet ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/browserbar.go internal/app/tui/browserbar_test.go
git commit -m "feat(tui): browser bar rendering with URL truncation and spinner"
```

---

### Task 5: Wire browser bar into view assembly

**Files:**
- Modify: `internal/app/tui/view.go`
- Test: `internal/app/tui/view_test.go` (existing — just verify build)

**Interfaces:**
- Consumes: `renderBrowserBar()` (Task 4).
- Produces: browser bar rendered in the view between swarm panel and input area.

- [ ] **Step 1: Add browser bar to viewString()**

In `internal/app/tui/view.go`, in `viewString()` (around line 63-69), the current code is:

```go
	rows := []string{m.renderTranscriptFrame()}
	swarmSpinner := m.activeSpinnerFrame(session.ActivityTool)
	if panel := renderSwarmPanel(m.state.SwarmProgress(), swarmSpinner, m.width); panel != "" {
		rows = append(rows, panel)
	}
	rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
```

Change to insert the browser bar between the swarm panel and the input area:

```go
	rows := []string{m.renderTranscriptFrame()}
	swarmSpinner := m.activeSpinnerFrame(session.ActivityTool)
	if panel := renderSwarmPanel(m.state.SwarmProgress(), swarmSpinner, m.width); panel != "" {
		rows = append(rows, panel)
	}
	if bar := m.renderBrowserBar(); bar != "" {
		rows = append(rows, bar)
	}
	rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
```

- [ ] **Step 2: Build and verify**

Run: `go build ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 3: Run full TUI test suite**

Run: `go test ./internal/app/tui/ -short -v 2>&1 | tail -10`
Expected: PASS — all tests pass (browser bar returns "" when no session, so existing tests unaffected).

- [ ] **Step 4: Format and vet**

Run: `gofmt -w internal/app/tui/view.go && go vet ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/view.go
git commit -m "feat(tui): wire browser bar into view assembly"
```

---

### Task 6: Status line browser segment

**Files:**
- Modify: `internal/app/tui/status.go`
- Test: `internal/app/tui/status_test.go`

**Interfaces:**
- Consumes: `session.BrowserInfo` (Task 1), `browserGlyphStyle` (Task 2), `truncateURL` (Task 4), `m.state.BrowserInfo()` (Task 1).
- Produces: `browserStatusText(BrowserInfo) string` helper, browser segment in `statusLeftSegments()`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/tui/status_test.go` (append after the last test function):

```go
func TestStatusLineShowsBrowserSegmentWhenSessionOpen(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/docs",
		Mode:        "standalone",
	})
	line := m.renderStatusLine(100)
	stripped := stripANSI(line)
	if !strings.Contains(stripped, "🌐") {
		t.Fatalf("status line missing 🌐 when browser session open:\n%s", line)
	}
	if !strings.Contains(stripped, "example.com/docs") {
		t.Fatalf("status line missing browser URL:\n%s", line)
	}
}

func TestStatusLineHidesBrowserSegmentWhenNoSession(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{SessionOpen: false})
	line := m.renderStatusLine(100)
	stripped := stripANSI(line)
	if strings.Contains(stripped, "🌐") {
		t.Fatalf("status line should not show 🌐 when no browser session:\n%s", line)
	}
}

func TestStatusLineDropsBrowserSegmentFirst(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen2.5-coder:14b", Provider: "ollama", LocalOnly: true})
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com",
		Mode:        "standalone",
	})
	line := m.renderStatusLine(35)
	stripped := stripANSI(line)
	if !strings.Contains(stripped, "qwen2.5-coder:14b @ ollama") {
		t.Fatalf("model segment should survive on narrow width:\n%s", line)
	}
	if strings.Contains(stripped, "example.com") {
		t.Fatalf("browser segment should be dropped on narrow width:\n%s", line)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run "TestStatusLineShowsBrowser|TestStatusLineHidesBrowser|TestStatusLineDropsBrowser" -v`
Expected: FAIL — no `🌐` in status line (browser segment not yet added).

- [ ] **Step 3: Add browserStatusText helper**

In `internal/app/tui/status.go`, after the existing `statusLeftSegments` function (around line 159), add:

```go
func browserStatusText(bi session.BrowserInfo) string {
	glyph := browserGlyphStyle.Render("🌐")
	url := truncateURL(bi.URL, 20)
	if url == "" {
		url = bi.Mode
	}
	return glyph + " " + url
}
```

- [ ] **Step 4: Add browser segment to statusLeftSegments**

In `internal/app/tui/status.go`, in `statusLeftSegments()` (around line 158, after the queued segment), add before the final `return segs`:

```go
	if bi := m.state.BrowserInfo(); bi.SessionOpen {
		segs = append(segs, statusSeg{
			text:     browserStatusText(bi),
			priority: 9,
		})
	}
```

Note: priority 9 is used (after queued at priority 8) so the browser segment is dropped first on narrow terminals, matching the spec's requirement ("lowest-priority segment — dropped first"). The existing priorities go up to 8 (queued), so 9 is the next available.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -run "TestStatusLineShowsBrowser|TestStatusLineHidesBrowser|TestStatusLineDropsBrowser" -v`
Expected: PASS — all three tests pass.

- [ ] **Step 6: Run full TUI test suite**

Run: `go test ./internal/app/tui/ -short -v 2>&1 | tail -10`
Expected: PASS

- [ ] **Step 7: Format and vet**

Run: `gofmt -w internal/app/tui/status.go internal/app/tui/status_test.go && go vet ./internal/app/tui/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/status.go internal/app/tui/status_test.go
git commit -m "feat(tui): browser segment in status line with drop priority"
```

---

### Task 7: Wire SessionState into desktop tools

**Files:**
- Modify: `internal/tools/desktop/types.go`
- Modify: `internal/tools/desktop/tools.go`
- Modify: `internal/app/app.go`

**Interfaces:**
- Consumes: `session.BrowserInfo`, `session.SetBrowserInfo` (Task 1), `session.State` (existing).
- Produces: `Options.SessionState *session.State` field, `toolSet.sessionState` field, `toolSet.updateBrowserState(BrowserInfo)` method, browser state injection in `getSession()` and each tool handler. `app.go` passes `state` as `SessionState`.

- [ ] **Step 1: Add SessionState to Options**

In `internal/tools/desktop/types.go`, change:

```go
type Options struct {
	Config         config.DesktopConfig
	BackendFactory func() (browser.BrowserBackend, error)
}
```

To:

```go
type Options struct {
	Config         config.DesktopConfig
	BackendFactory func() (browser.BrowserBackend, error)
	SessionState   *session.State
}
```

Add the import:

```go
import (
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/desktop/browser"
)
```

- [ ] **Step 2: Add sessionState to toolSet and updateBrowserState helper**

In `internal/tools/desktop/tools.go`, add `session.State` to the imports:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/registry"
)
```

Change the `toolSet` struct:

```go
type toolSet struct {
	cfg            config.DesktopConfig
	backendFactory func() (browser.BrowserBackend, error)
	session        *browser.Session
	sessionMu      sync.Mutex
	sessionState   *session.State
}
```

Add the `updateBrowserState` helper after `getSession`:

```go
func (ts *toolSet) updateBrowserState(info session.BrowserInfo) {
	if ts.sessionState != nil {
		ts.sessionState.SetBrowserInfo(info)
	}
}
```

- [ ] **Step 3: Wire sessionState into RegisterAll**

In `internal/tools/desktop/tools.go`, in `RegisterAll`, the current toolSet construction is:

```go
	ts := &toolSet{
		cfg:            opts.Config,
		backendFactory: opts.BackendFactory,
	}
```

Change to:

```go
	ts := &toolSet{
		cfg:            opts.Config,
		backendFactory: opts.BackendFactory,
		sessionState:   opts.SessionState,
	}
```

- [ ] **Step 4: Inject SessionOpen + Mode in getSession**

In `internal/tools/desktop/tools.go`, in `getSession`, after `ts.session = browser.NewSession(backend)` and before `return ts.session, nil`, add:

```go
	ts.session = browser.NewSession(backend)
	ts.updateBrowserState(session.BrowserInfo{
		SessionOpen: true,
		Mode:        ts.cfg.Mode,
	})
	return ts.session, nil
```

- [ ] **Step 5: Inject Active state before each tool and URL/Title after**

In `internal/tools/desktop/tools.go`, in `navigateTool`, after `page, err := sess.Page(ctx)` and before `page.Navigate`, add the active state. After `page.Navigate` succeeds, update URL/title. The current handler body is:

```go
		sess, err := ts.getSession(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if err := page.Navigate(ctx, args.URL); err != nil {
			return registry.ToolResult{}, fmt.Errorf("navigate: %w", err)
		}
		title, _ := page.Title(ctx)
		return registry.ToolResult{
			Summary: fmt.Sprintf("Navigated to %s", args.URL),
			Content: fmt.Sprintf(`{"url":%q,"title":%q}`, args.URL, title),
		}, nil
```

Change to:

```go
		sess, err := ts.getSession(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      true,
			ToolName:    "browser.navigate",
			URL:         args.URL,
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
		if err := page.Navigate(ctx, args.URL); err != nil {
			ts.updateBrowserState(session.BrowserInfo{
				SessionOpen: true,
				Active:      false,
				URL:         args.URL,
				Mode:        ts.cfg.Mode,
				UpdatedAt:   time.Now(),
			})
			return registry.ToolResult{}, fmt.Errorf("navigate: %w", err)
		}
		title, _ := page.Title(ctx)
		currentURL, _ := page.URL(ctx)
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      false,
			URL:         currentURL,
			Title:       title,
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
		return registry.ToolResult{
			Summary: fmt.Sprintf("Navigated to %s", args.URL),
			Content: fmt.Sprintf(`{"url":%q,"title":%q}`, args.URL, title),
		}, nil
```

Add `"time"` to the imports in tools.go.

- [ ] **Step 6: Add Active state to readTool**

In `internal/tools/desktop/tools.go`, in `readTool`, after `page, err := sess.Page(ctx)` and before the text reading logic, add:

```go
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      true,
			ToolName:    "browser.read",
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
```

And after the text is read (before the return), clear active:

```go
		if err != nil {
			ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
			return registry.ToolResult{}, fmt.Errorf("read: %w", err)
		}
		ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
```

- [ ] **Step 7: Add Active state to clickTool**

In `internal/tools/desktop/tools.go`, in `clickTool`, after `page, err := sess.Page(ctx)` and before `page.Click`, add active state. After click succeeds, fetch URL/title:

```go
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      true,
			ToolName:    "browser.click",
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
		if err := page.Click(ctx, args.Selector); err != nil {
			ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
			return registry.ToolResult{}, fmt.Errorf("click %s: %w", args.Selector, err)
		}
		clickURL, _ := page.URL(ctx)
		clickTitle, _ := page.Title(ctx)
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      false,
			URL:         clickURL,
			Title:       clickTitle,
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
```

- [ ] **Step 8: Add Active state to fillTool**

In `internal/tools/desktop/tools.go`, in `fillTool`, after `page, err := sess.Page(ctx)` and before `page.Fill`, add active state. After fill, clear active (fill doesn't change URL/title):

```go
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      true,
			ToolName:    "browser.fill",
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
		if err := page.Fill(ctx, args.Selector, args.Value); err != nil {
			ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
			return registry.ToolResult{}, fmt.Errorf("fill %s: %w", args.Selector, err)
		}
		ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
```

- [ ] **Step 9: Add Active state to submitTool**

In `internal/tools/desktop/tools.go`, in `submitTool`, add active state before submit and update URL/title after (submit may cause navigation):

```go
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      true,
			ToolName:    "browser.submit",
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
```

After the submit action (whether selector or Enter), fetch URL/title:

```go
		if args.Selector != "" {
			if err := page.Submit(ctx, args.Selector); err != nil {
				ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
				return registry.ToolResult{}, fmt.Errorf("submit %s: %w", args.Selector, err)
			}
		} else {
			if err := page.PressKey(ctx, "Enter"); err != nil {
				ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
				return registry.ToolResult{}, fmt.Errorf("press Enter: %w", err)
			}
		}
		submitURL, _ := page.URL(ctx)
		submitTitle, _ := page.Title(ctx)
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      false,
			URL:         submitURL,
			Title:       submitTitle,
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
```

Note: the submitTool handler currently has two return paths (selector vs Enter). Both need the active-state-clearing. The above shows the unified pattern — restructure so both paths share the post-submit URL/title fetch.

- [ ] **Step 10: Add Active state to screenshotTool**

In `internal/tools/desktop/tools.go`, in `screenshotTool`, add active state before screenshot, clear after (screenshot doesn't change URL/title):

```go
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		ts.updateBrowserState(session.BrowserInfo{
			SessionOpen: true,
			Active:      true,
			ToolName:    "browser.screenshot",
			Mode:        ts.cfg.Mode,
			UpdatedAt:   time.Now(),
		})
		_, err = page.Screenshot(ctx, browser.ScreenshotOpts{FullPage: args.FullPage, Format: ts.cfg.ScreenshotFormat})
		if err != nil {
			ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
			return registry.ToolResult{}, fmt.Errorf("screenshot: %w", err)
		}
		ts.updateBrowserState(session.BrowserInfo{SessionOpen: true, Active: false, Mode: ts.cfg.Mode, UpdatedAt: time.Now()})
```

- [ ] **Step 11: Wire SessionState in app.go**

In `internal/app/app.go`, in `buildAgentRunner`, the current desktop block is:

```go
		desktopOpts := desktop.Options{
			Config: cfg.Desktop,
			BackendFactory: func() (browser.BrowserBackend, error) {
				return newDesktopBackend(cfg.Desktop)
			},
		}
```

Change to:

```go
		desktopOpts := desktop.Options{
			Config: cfg.Desktop,
			BackendFactory: func() (browser.BrowserBackend, error) {
				return newDesktopBackend(cfg.Desktop)
			},
			SessionState: state,
		}
```

- [ ] **Step 12: Build and verify**

Run: `go build ./...`
Expected: PASS

- [ ] **Step 13: Run desktop tests**

Run: `go test ./internal/tools/desktop/ -v 2>&1 | tail -10`
Expected: PASS — existing tests pass (SessionState is nil in those tests, so `updateBrowserState` is a no-op).

- [ ] **Step 14: Run full test suite**

Run: `go test ./... -short 2>&1 | tail -10`
Expected: PASS

- [ ] **Step 15: Format and vet**

Run: `gofmt -w internal/tools/desktop/types.go internal/tools/desktop/tools.go internal/app/app.go && go vet ./...`
Expected: PASS

- [ ] **Step 16: Commit**

```bash
git add internal/tools/desktop/types.go internal/tools/desktop/tools.go internal/app/app.go
git commit -m "feat(desktop): wire SessionState for live browser info updates to TUI"
```

---

### Task 8: Full integration verification

**Files:**
- None (verification only)

- [ ] **Step 1: Run complete test suite**

Run: `go test ./... -short`
Expected: PASS — all packages green.

- [ ] **Step 2: Run build**

Run: `go build ./cmd/marshal`
Expected: PASS

- [ ] **Step 3: Run vet**

Run: `go vet ./...`
Expected: PASS

- [ ] **Step 4: Run format check**

Run: `gofmt -l .`
Expected: no output (all files formatted).

- [ ] **Step 5: Verify no existing tests broke**

Run: `go test ./internal/app/tui/... ./internal/app/session/... ./internal/tools/desktop/... -short -v 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 6: Commit if any format changes needed**

```bash
gofmt -w .
git add -A
git commit -m "style: format desktop TUI code" || echo "nothing to commit"
```