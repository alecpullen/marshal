# Desktop Browser Automation (Cowork-style, Approach 1)

**Date:** 2026-07-13
**Status:** Approved
**Scope:** `internal/tools/desktop/`, `internal/app/config/`, `internal/app/app.go`

## Problem

Marshal is a local-first TUI coding agent that works with local (Ollama) and remote (OpenAI-compatible) models. Today it operates on files, shell, git, and repo knowledge — but cannot interact with desktop applications or web browsers.

Users want a Claude Cowork-style agent that can drive a browser: navigate pages, read content, fill forms, click elements, submit — while the user watches a visible browser window. The agent should work with any provider (local or remote) through the existing provider abstraction, reuse the existing tool registry and approval system, and leave clean seams for a future vision milestone (screenshot + VLM grounding) and an OS-level input milestone (mouse/keyboard driving via cliclick/Accessibility).

## Approach

**Approach A (approved):** Playwright-go browser backend, config-driven standalone/attach mode, DOM-first tool set (no vision in this milestone), reuse existing approval + risk system. macOS first; cross-platform abstraction deferred to the OS-input milestone.

## Phasing

**Milestone 1 (this spec):** Browser automation via Playwright-go. Six `browser.*` tools. Standalone (visible bundled Chromium) + attach (CDP to user's browser) modes. Reuses existing approval/risk/audit plumbing.

**Future — Vision milestone (designed as a seam, not implemented here):** `browser.screenshot` returns image bytes; `desktop.vision_describe` tool routes screenshots to a vision-capable provider. DOM-first, vision as fallback. Additive — zero changes to milestone 1 code.

**Future — OS-level input milestone (separate spec):** `internal/tools/desktop/oscontrol/` package with `DesktopBackend` interface, macOS impl via cliclick + Accessibility API. Coordinate-based mouse/keyboard for non-browser apps. Sibling package, not a modification to browser tools.

## Current state (grounding)

- `internal/tools/registry/types.go` defines `Tool`, `ToolHandler`, `ToolResult`, `RiskLevel` (read_only, workspace_write, command, network, destructive).
- `internal/tools/native/native.go` shows the registration pattern: `RegisterAll(reg, opts)` registers tools conditionally based on config (web tools at lines 134-139).
- `internal/app/config/config.go` has a `[web]` config block (`WebConfig`) with enabled/timeout/search fields — this is the template for the `[desktop]` block.
- `internal/tools/policy/policy.go` implements the approval engine; risk levels flow through it.
- `internal/llm/provider/provider.go` defines the `Provider` interface with `Chat`, `Capabilities`, etc.
- `internal/sandbox/` handles command execution backends; browser tools don't use the sandbox (they use Playwright directly).
- `session.State` is the shared mutable state; the job manager is wired into it with a shutdown cleanup pattern (`native.go:193-202`).

## Milestone 1 design

### 1. Package layout

```
internal/tools/desktop/
  browser/
    backend.go         — BrowserBackend + PageHandle interfaces
    standalone.go      — Playwright-go launch (bundled Chromium), headless toggle
    attach.go          — Connect via CDP URL to user's running browser
    session.go         — desktopSession: lazy backend creation, page handle pool, lifecycle
  tools.go             — RegisterAll(reg, opts): registers browser.* tools
  types.go             — Options, BrowserOptions, shared types
  tools_test.go        — Tool registration + schema + arg validation tests (fake backend)
  config_test.go       — [desktop] config round-trip tests
```

### 2. Browser backend interfaces

```go
// internal/tools/desktop/browser/backend.go

type BrowserBackend interface {
    NewPage(ctx context.Context) (PageHandle, error)
    Close() error
    Mode() string  // "standalone" | "attach"
}

type PageHandle interface {
    Navigate(ctx context.Context, url string) error
    Title(ctx context.Context) (string, error)
    URL(ctx context.Context) (string, error)
    Text(ctx context.Context, selector string) (string, error)
    HTML(ctx context.Context, selector string) (string, error)
    ReadableText(ctx context.Context) (string, error)
    Click(ctx context.Context, selector string) error
    Fill(ctx context.Context, selector, value string) error
    PressKey(ctx context.Context, key string) error
    Submit(ctx context.Context, selector string) error
    WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error
    WaitForLoadState(ctx context.Context, state string) error
    Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error)
    Close() error
}

type ScreenshotOpts struct {
    FullPage bool
    Format   string  // "png" (milestone 1); future: "jpeg"
}
```

Both `standalone.go` and `attach.go` implement `BrowserBackend`. The tools layer never knows which is running.

### 3. Standalone backend

Uses `playwright-go` to launch a bundled Chromium instance. The `headless` config flag (default `false` — visible browser, Cowork-style) controls visibility. Playwright manages the browser process lifecycle; we tie it to `session.State` shutdown so the browser closes when the session ends.

Requires a one-time `playwright install chromium` setup step (documented in config examples and help text, not automated by the binary).

### 4. Attach backend

Connects to an existing browser via CDP URL (`config.Desktop.CDPURL`). No browser process management — Marshal is a client. User must launch Chrome with `--remote-debugging-port=9222`. Uses the user's real browser session/profile/cookies.

Connection health check wraps all calls: if the websocket is dead, returns a clear error ("browser connection lost: reconnect by calling browser.navigate") and the model can retry. No silent failures.

### 5. Config block

New `[desktop]` section in `config.toml`, paralleling `[web]`:

```toml
[desktop]
enabled = true
mode = "standalone"          # "standalone" | "attach"
headless = false             # default false (visible browser, Cowork-style)
cdp_url = ""                 # used when mode = "attach" (e.g. "http://localhost:9222")
url_allowlist = []           # optional navigation restrictions (hostname + optional path prefix)
url_denylist = []            # optional navigation restrictions
default_timeout = "30s"
screenshot_format = "png"    # metadata-only in milestone 1; image bytes in vision milestone
```

Go config struct:

```go
type DesktopConfig struct {
    Enabled          bool
    Mode             string        // "standalone" | "attach"
    Headless         bool
    CDPURL           string
    URLAllowlist     []string
    URLDenylist      []string
    DefaultTimeout   time.Duration
    ScreenshotFormat string
}
```

Defaults: `Enabled = false`, `Mode = "standalone"`, `Headless = false`, `DefaultTimeout = 30s`, `ScreenshotFormat = "png"`.

Config loading follows the existing merge order (defaults → `~/.config/marshal/config.toml` → `.marshal/config.toml`). The `fileDesktop` TOML struct mirrors `fileWeb` with pointer fields for optional override semantics.

### 6. Tools

Six tools registered under the `browser.*` namespace, following the existing `registry.Tool` shape.

| Tool | Risk | Purpose | Key args |
|------|------|---------|----------|
| `browser.navigate` | `RiskNetwork` | Go to a URL | `url` (required) |
| `browser.read` | `RiskReadOnly` | Get simplified readable text of current page | `selector` (optional, defaults to full page) |
| `browser.click` | `RiskNetwork` | Click an element by CSS selector | `selector` (required) |
| `browser.fill` | `RiskNetwork` | Type into an input/textarea | `selector` (required), `value` (required), `clear` (optional, default true) |
| `browser.submit` | `RiskNetwork` | Submit a form | `selector` (optional, defaults to focused element) |
| `browser.screenshot` | `RiskReadOnly` | Capture screenshot — milestone 1 returns metadata; vision milestone returns image bytes | `full_page` (optional, default false) |

**Tool result shape** — each returns `registry.ToolResult`:
- `Summary`: one-line human-readable (e.g. "Navigated to https://example.com", "Clicked button#submit", "Read page: 2,340 chars").
- `Content`: full output for the model. For `browser.read` this is the simplified page text. For others, a JSON blob with relevant data (current URL, page title, element text if applicable). For `browser.screenshot` in milestone 1: JSON with dimensions and timestamp (no image bytes).

**URL allow/deny enforcement** happens at the tool layer (before backend call), not the policy layer. If `url_allowlist` is non-empty, the target URL must match at least one entry. `url_denylist` entries always block. Matches on hostname with optional path prefix. Violation returns an error result the model can see: "navigation blocked by policy: <url>".

**Auto-wait behavior**: `browser.click` and `browser.fill` use Playwright's built-in auto-waiting (waits for element to be visible, stable, enabled before acting). `browser.navigate` implicitly waits for `domcontentloaded`. The `default_timeout` config bounds all waits. No explicit `browser.wait_for` tool in milestone 1 — Playwright auto-waiting covers common cases; the model can poll with `browser.read` for async content.

### 7. Registration and wiring

Registration happens in `app.Run()` alongside `native.RegisterAll`, gated by `config.Desktop.Enabled` — mirroring how web tools are conditionally registered at `native.go:134-139`.

```go
// in app.Run(), after native.RegisterAll:
if cfg.Desktop.Enabled {
    if err := desktop.RegisterAll(reg, desktop.Options{
        Config:       cfg.Desktop,
        SessionState: state,
    }); err != nil {
        return err
    }
}
```

### 8. Session lifecycle

`desktopSession` struct holds the `BrowserBackend` + active `PageHandle`:
- Created lazily on first browser tool call (not at startup — avoids launching Chromium if the user never uses browser tools).
- Cleanup registered via a shutdown hook in `app.Run()`, same pattern as the job manager (`native.go:193-202`). On session end: `PageHandle.Close()` then `BrowserBackend.Close()`.
- If the browser crashes or the CDP connection drops, the next tool call detects the stale handle, returns an error, and lazily re-creates on the following call.

Standalone process management: Playwright-go manages the Chromium subprocess. We pass the session shutdown context through to `BrowserBackend.Close()`. If Marshal exits uncleanly, Playwright's cleanup hooks fire on context cancellation.

### 9. Safety and approval

All six tools map to existing `RiskLevel` values — no new tier. `RiskNetwork` actions (navigate, click, fill, submit) flow through the existing approval system in `internal/tools/policy`. `RiskReadOnly` actions (read, screenshot metadata) skip approval, same as file read. The approval prompt in the TUI already shows tool name + args; browser tool args (URL, selector, value) are short and readable, so no TUI changes are needed.

URL allow/deny enforcement at the tool layer (not policy layer) — browser-specific logic belongs with the browser tool. Returns a structured error the model can reason about.

### 10. Explicitly NOT handled in milestone 1

- No cookie/session persistence between Marshal sessions (standalone starts fresh; attach inherits user's browser session).
- No proxy configuration (future config field).
- No multi-tab support (single page handle per session; multi-tab is a later enhancement).
- No file downloads/uploads (form fill only).
- No cross-platform abstraction (macOS first; the `BrowserBackend` interface is the seam for future OS-specific backends).

## Vision milestone (future seam, not implemented here)

Plugs into two existing seams: `BrowserBackend.Screenshot()` and the `Provider` abstraction.

1. **`browser.screenshot` enriched**: returns image bytes (PNG, base64) in `ToolResult.Content`. Tool schema stays the same — the model receives richer output. `screenshot_format` config already controls this.

2. **DOM-first, vision as fallback**: the agent's default loop is `browser.read` (DOM text) → reason → act. Vision enters when the model explicitly calls `browser.screenshot` because DOM text is insufficient (canvas, images, complex layout), then a future `desktop.vision_describe` tool routes the screenshot to a vision-capable provider.

3. **`desktop.vision_describe` tool** (new, added in vision milestone): takes a screenshot + a question, routes to the configured provider's `Chat` with an image content block. `internal/llm/schema` needs image content block support (schema addition, not a rewrite). `ProviderCapabilities` gains a `Vision bool` field so the tool can check before attempting.

4. **Provider routing for vision**: existing role-based routing in `internal/llm/routing` can route vision calls to a different model. A user could run a local text model for reasoning (Ollama) and a remote vision model (OpenAI-compatible endpoint) for screenshots. The routing layer already supports per-role model selection.

5. **Coordinate-based input** (the "mouse driving" future milestone): `internal/tools/desktop/oscontrol/` package with `DesktopBackend` interface, macOS impl via `cliclick` + Accessibility API. Vision loop: screenshot → VLM describes UI at coordinates → `desktop.click_at(x, y)` → screenshot → verify. Sibling package, not a modification to browser tools — browser interactions stay selector-based (more reliable), OS-level input is the fallback for non-browser apps.

**What milestone 1 does to prepare:**
- `BrowserBackend.Screenshot()` returns `[]byte` — milestone 1 callers just don't use the bytes.
- `[desktop]` config block is forward-compatible (no rework needed to add vision fields).
- `ProviderCapabilities` already exists as the extension point for capability flags.
- Registry + approval flow already handle image data in `ToolResult.Content` (it's a string — base64 images work today).

The vision milestone is additive: new tool, schema addition for image blocks, capability flag, routing config. Zero changes to milestone 1 code.

## Testing and dependencies

**New dependency:** `github.com/playwright-community/playwright-go` (pure Go, no CGO — does not affect the existing CGO/tree-sitter build). Added to `go.mod`. One-time setup: `playwright install chromium` (documented in config examples and help text, not automated by the binary).

**Test strategy**, mirroring existing `native_test.go` patterns:

- **Backend tests** (`browser/standalone_test.go`, `browser/attach_test.go`): integration tests against a real Playwright-launched Chromium. Tagged with a build constraint or `testing.Short()` skip so `go test ./...` stays fast. Validate navigation, click, fill, read, screenshot lifecycle.
- **Tool tests** (`tools_test.go`): unit tests with a fake `BrowserBackend` (satisfies the interface, records calls, returns canned data). Validate tool arg parsing, schema, risk levels, URL allow/deny enforcement, result formatting. No real browser needed — majority of coverage lives here.
- **Config tests**: mirror the `save_test.go` web config round-trip pattern — ensure `[desktop]` block loads, saves, and merges correctly with defaults.
- **Lifecycle test**: verify lazy creation (no browser launched until first tool call), cleanup on session shutdown, stale-handle recovery.

**Existing tests stay green:** no changes to `internal/tools/native`, `internal/tools/registry`, `internal/tools/policy`, or `internal/sandbox`. The new package is purely additive.