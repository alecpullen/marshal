# Desktop Browser Automation TUI Design

**Date:** 2026-07-14
**Status:** Approved
**Scope:** `internal/app/tui/`, `internal/app/session/`, `internal/tools/desktop/`, `internal/app/app.go`

## Problem

The desktop browser automation feature (spec: `2026-07-13-desktop-browser-automation-design.md`, implemented on branch `feat/desktop-browser-automation`) adds six `browser.*` tools. The TUI currently renders all tool calls identically — `⏺` glyph in gold for active/completed, `⎿` for continuation. Browser tools need to be visually distinct so the user can immediately tell "the agent is driving a browser" from "the agent is reading a file" or "running a shell command." A live browser status bar must show the current page URL/title/mode and a spinner when browser tools are active — so the user can watch the agent work (Cowork-style), even when the transcript has scrolled past the last browser action.

## Approach

**Approach C (approved):** Transcript + persistent browser bar + status line segment.

- Browser tool calls in the transcript use a `🌐` glyph in gold (`AccentTertiary`, same as `⏺`) plus a `browser` prefix in violet (`AccentSecondary`). Glyph is the scannable signal; prefix makes the namespace explicit.
- A persistent one-line browser bar appears below the transcript (between transcript and input area, same slot as the swarm panel) once a browser session is created. Shows URL · title · mode, plus a spinner + tool name when a browser tool is actively running.
- The status line gets a `🌐 shortURL` segment in the left cluster, shown only when a browser session is open. Lowest-priority segment — dropped first on narrow terminals.

## Decisions (from brainstorming)

1. **Browser activity placement:** Transcript + live browser panel (option B from visual companion).
2. **Visual distinctiveness:** Glyph + colored prefix (option C) — `🌐` in gold + `browser` prefix in violet.
3. **Browser bar visibility:** Persistent while session open + action indicator (option C) — bar stays for the whole session, shows spinner when tools are active.
4. **Status line:** New `browser` segment in left cluster (recommended) — shown only when `SessionOpen`.

## Current state (grounding)

- `internal/app/tui/transcript.go:384` — `renderActiveToolCall` renders `⏺ toolname · elapsed` with `toolBulletStyle` (gold).
- `internal/app/tui/transcript.go:408` — `renderCompletedToolCall` renders `✔ toolname done` with `statusOkStyle`.
- `internal/app/tui/view.go:63-69` — `viewString()` assembles rows: transcript frame, swarm panel (conditional), input area, help footer, status line.
- `internal/app/tui/status.go:33-55` — `renderStatusLine` builds left/right clusters with priority-based segment dropping.
- `internal/app/tui/model.go:1779-1849` — style variables (`coralColor`, `goldColor`, `mauveColor`, etc.) set from `activeTheme` in `loadTheme()`.
- `internal/app/session/session.go:237-273` — `SandboxInfo` struct + `SetSandboxInfo`/`SandboxInfo` methods (the template for `BrowserInfo`).
- `internal/tools/desktop/tools.go` — `toolSet` holds `cfg`, `backendFactory`, `session`. Handlers call `getSession()` then `sess.Page()`.
- `internal/app/app.go:451-466` — `desktop.RegisterAll` call in `buildAgentRunner` (Task 9 wiring).

## Design

### 1. Session state — `BrowserInfo`

New struct in `internal/app/session/session.go`, mirroring `SandboxInfo`:

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

New field on `State` struct: `browser BrowserInfo`.

New event type constant (session.go:35-41):
```go
EventBrowserChanged = "browser_changed"
```

New field on `Event` struct (session.go:48-56):
```go
Browser *BrowserInfo
```

Two new methods, mirroring `SetSandboxInfo`/`SandboxInfo` (session.go:562-577):

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

The `publishEvent` call follows the exact pattern of `SetActivity` (session.go:1254-1262): copy the value before publishing so the event carries a snapshot, not a pointer to mutable state.

### 2. Desktop tool handler wiring

`internal/tools/desktop/tools.go` — `Options` and `toolSet` get a `SessionState` field:

```go
type Options struct {
    Config         config.DesktopConfig
    BackendFactory func() (browser.BrowserBackend, error)
    SessionState   *session.State  // nil in unit tests
}
```

New helper on `toolSet`:

```go
func (ts *toolSet) updateBrowserState(info session.BrowserInfo) {
    if ts.sessionState != nil {
        ts.sessionState.SetBrowserInfo(info)
    }
}
```

Nil-safe — when `SessionState` is nil (unit tests with fake backends), calls are no-ops.

Three injection points:

1. **Session creation** (`getSession`): after creating the session, set `SessionOpen: true` + `Mode: ts.cfg.Mode`.
2. **Before tool execution**: at the start of each browser tool handler, set `Active: true, ToolName: <tool name>`.
3. **After tool completion**: update `URL` and `Title` from `page.URL()` and `page.Title()`, clear `Active: false`.

What each tool updates:

| Tool | Active before | URL/Title after | Active after |
|------|---------------|-----------------|-------------|
| `browser.navigate` | `true` + `browser.navigate` | yes | `false` |
| `browser.read` | `true` + `browser.read` | no change | `false` |
| `browser.click` | `true` + `browser.click` | yes | `false` |
| `browser.fill` | `true` + `browser.fill` | no change | `false` |
| `browser.submit` | `true` + `browser.submit` | yes | `false` |
| `browser.screenshot` | `true` + `browser.screenshot` | no change | `false` |

Navigate, click, and submit re-fetch URL/title after completion because navigation may have changed them. Read, fill, and screenshot don't change the page location.

`app.go` wiring update — the existing `desktop.RegisterAll` call (Task 9) adds `SessionState: state`:

```go
desktopOpts := desktop.Options{
    Config:         cfg.Desktop,
    BackendFactory: func() (browser.BrowserBackend, error) {
        return newDesktopBackend(cfg.Desktop)
    },
    SessionState: state,
}
```

### 3. Style variables

New styles in `model.go` alongside the existing `toolBulletStyle` (model.go:281):

```go
browserGlyphStyle = lipgloss.NewStyle().
    Foreground(activeTheme.AccentTertiary)   // gold (same as ⏺)
browserPrefixStyle = lipgloss.NewStyle().
    Foreground(activeTheme.AccentSecondary)  // violet
browserBarStyle = lipgloss.NewStyle().
    Background(activeTheme.BGSurface)
urlStyle = lipgloss.NewStyle().
    Foreground(activeTheme.FGDefault)
```

Set in `loadTheme()` (model.go:1806) alongside the other style assignments.

### 4. Transcript rendering

`internal/app/tui/transcript.go` — two functions need browser-aware paths:

**Helper:**
```go
func isBrowserTool(name string) bool {
    return strings.HasPrefix(name, "browser.")
}
```

**`renderActiveToolCall` (transcript.go:384):** currently renders `⏺ toolname · elapsed`. For browser tools, renders `🌐 browser.navigate · url · elapsed` — glyph is `🌐` in gold via `browserGlyphStyle`, `browser` prefix in violet via `browserPrefixStyle`, tool suffix (`.navigate`) in default color. The args line stays `⎿` muted as before.

**`renderCompletedToolCall` (transcript.go:408):** currently renders `✔ toolname done`. For browser tools, renders `✔ browser.navigate done` with the same glyph/prefix treatment. The summary line is unchanged.

The `browser.read` content (page text) renders via the existing `renderToolResultLine` — no special handling needed. The existing truncation + wrapping handles large page content. Only the header line gets the browser glyph/prefix.

**Approval rendering** — `approval.go:approvalSummary` already handles non-shell tools generically: it renders `Agent wants to call tool: browser.navigate` with the URL in args. Browser tools with `RiskNetwork` already trigger the approval flow. No changes needed.

### 5. Browser bar

New file `internal/app/tui/browserbar.go` — one render function:

```go
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
        BorderTop(true).
        BorderForeground(activeTheme.BorderMuted).
        Width(max(m.width, 1)).
        Render(ansi.Cut(" "+line+" ", 0, m.width))
}
```

**`truncateURL`** — shared helper (used by both bar and status segment), strips `https://`/`http://`, truncates from the middle if too long: `https://developer.example.com/very/long/path` → `developer.example.com/…/path`. Keeps hostname visible.

**`dimSep`** — renders ` · ` in `FGMuted`, reusing the existing `dimSeparator` pattern.

**Spinner** — uses `m.activeSpinnerFrame(session.ActivityTool)`, the same call the activity strip and swarm panel use. Only appears when `bi.Active` is true.

**When the bar appears/disappears:**
- `SessionOpen = false` initially → bar hidden
- First browser tool call creates the session → `SessionOpen = true` → bar appears
- Bar persists for the entire session (even during file/shell work)
- Session closes on `Runtime.Close()` (via the desktop closer from Task 9) → `SessionOpen = false` → bar disappears

**Layout integration** — in `view.go:viewString()`, insert the browser bar between the swarm panel and the input area:

```go
rows := []string{m.renderTranscriptFrame()}
if panel := renderSwarmPanel(...); panel != "" {
    rows = append(rows, panel)
}
if bar := m.renderBrowserBar(); bar != "" {
    rows = append(rows, bar)
}
rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
```

The bar takes 1 row (plus 1 for the top border). The viewport height calculation must account for this — the existing `view.go` already handles dynamic rows via the `rows` slice. The browser bar follows the same conditional-append pattern as the swarm panel.

### 6. Status line segment

`internal/app/tui/status.go` — in `statusLeftSegments()`, after the existing segments, append a browser segment:

```go
if bi := m.state.BrowserInfo(); bi.SessionOpen {
    segs = append(segs, statusSeg{
        text:     browserStatusText(bi),
        priority: 4,
    })
}
```

Helper:
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

Priority 4 means the browser segment is dropped first when the terminal is too narrow — before mode (0), model/provider (1), and ctx usage (3). On narrow terminals, the browser bar below the transcript still shows full info.

No right-cluster change — the right cluster already shows the live activity ("thinking", "browser.navigate · 3.2s") via the existing `Activity` system. Browser tools set `Activity{Kind: ActivityTool, Label: "browser.navigate"}` and the activity strip renders it. No change needed.

### 7. Explicitly NOT handled

- No screenshot preview in the TUI (that's the vision milestone).
- No multi-tab indicator (single page handle per session, per the browser automation spec).
- No browser bar keyboard interaction (the bar is display-only — no keybindings to navigate browser history, open/close tabs, etc.).
- No live page content preview in the bar (the transcript already shows `browser.read` results inline).
- No changes to the approval UI (it already handles browser tools via the generic non-shell tool path).
- No changes to the help footer (no new keybindings for browser tools in this milestone).

## Testing

All tests use existing fake/stub patterns — no real browser, no Playwright, no network.

**`browserbar_test.go`:**
- `TestRenderBrowserBarHiddenWhenNoSession` — `BrowserInfo{SessionOpen: false}` → returns `""`
- `TestRenderBrowserBarShowsURLTitleMode` — `SessionOpen: true` with URL/title/mode → output contains stripped URL, title, mode
- `TestRenderBrowserBarShowsSpinnerWhenActive` — `Active: true, ToolName: "browser.click"` → output contains spinner frame + tool name
- `TestRenderBrowserBarHidesSpinnerWhenIdle` — `Active: false` → no spinner text
- `TestRenderBrowserBarTruncatesLongURL` — long URL → output truncated, fits width
- `TestRenderBrowserBarNarrowWidth` — width < 30 → output cut, no panic

**`status_test.go` additions:**
- `TestStatusLineShowsBrowserSegmentWhenSessionOpen` — left cluster contains `🌐` + short URL
- `TestStatusLineHidesBrowserSegmentWhenNoSession` — left cluster does not contain `🌐`
- `TestStatusLineDropsBrowserSegmentFirst` — narrow width → browser segment dropped before model/provider

**`transcript_test.go` additions:**
- `TestRenderActiveToolCallBrowserGlyph` — `ActiveToolCall{Name: "browser.navigate"}` → output contains `🌐` and violet `browser` prefix
- `TestRenderActiveToolCallNonBrowserGlyph` — `ActiveToolCall{Name: "file.read"}` → output contains `⏺` (unchanged)
- `TestRenderCompletedToolCallBrowserGlyph` — `AuditEvent{ToolName: "browser.navigate"}` → output contains `✔ browser.navigate done` with browser glyph/prefix

**`session_test.go` additions:**
- `TestSetBrowserInfo` — `SetBrowserInfo` then `BrowserInfo()` returns same values
- `TestBrowserInfoDefault` — new `State` → `BrowserInfo()` returns zero-value

**Testing approach:** `renderBrowserBar` tests construct a `Model` with `session.State` that has `SetBrowserInfo` called. Transcript tests call render functions directly with constructed values. No real browser. The `SessionState` nil-safety path is exercised by the existing Task 6 fake tests (which pass `nil` for `SessionState`).

**Existing tests stay green:** no changes to `renderActiveToolCall`'s non-browser path, no changes to `statusLeftSegments` existing segments, no changes to `view.go`'s existing row assembly for non-browser sessions. All changes are conditional additions.