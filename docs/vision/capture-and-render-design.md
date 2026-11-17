# Vision follow-up tools — desktop capture and rendered-page review

**Status:** approved design, awaiting plan.
**Follows:** `docs/vision/design.md` (vision capability) and
`docs/vision/plans/2026-11-17-images-plan.md` (image plumbing).

## Context

`docs/vision/design.md` decided that Marshal would see images before it could
produce them: the images plan gives the agent `image.view`, an
`internal/imagepayload` store, `schema.ImageRef`, `registry.ToolResult.Images`
and `ChatMessage.Images`. That leaves two vision workflows the user actually
asked for, and neither is covered:

1. **"Look at the app that's running."** There is no way to show the agent a
   window or the desktop. The agent can read text and drive a browser, but it
   cannot see what a process on this machine is displaying.
2. **"Look at what you just wrote."** Marshal already ships `browser.navigate`,
   `browser.read`, `browser.click`, `browser.fill`, `browser.submit` and
   `browser.screenshot` over Playwright (`internal/tools/desktop/`), but
   `browser.screenshot` throws the pixels away and returns metadata, and
   `file://` URLs are rejected before the scheme check by
   `urlAllowed` (`internal/tools/desktop/url_filter.go:11`), so local HTML
   cannot be opened at all. A model therefore cannot visually inspect the HTML
   or CSS it just wrote, and cannot see text overflow.

There is another browser already in the binary. Building a second renderer
(headless Chromium wrapper, or a Go HTML→image library) would duplicate a
session, a lifecycle, a timeout model and a hardening story that already exist
and are already tested. So the render half of this design adds **no new tool
and no new dependency**: it makes `browser.*` able to open a workspace file and
to return the pixels it already knows how to take.

## 1. Scope and architecture

### 1.1 What ships

**A. `image.capture` — one new tool.** Captures the desktop and returns an
image ref, so "look at the app that's running" is one call.

| | |
|---|---|
| Registration | only when `[capture] enabled = true` **and** the image store is wired (same rule as `image.view`) |
| Risk | `read_only` |
| Workspace effect | `WorkspaceEffectSessionOnly` |
| Gate | a default `{permission: "image.capture", pattern: "*", action: "ask"}` rule, so it prompts in Act mode (§3) |
| Params | `target: "active_window" \| "screen"` (default `active_window`), `label` |
| Returns | `ToolResult.Images = [ref]` plus a text `Content` and `Summary` naming target, backend, dimensions and hash |

**B. `browser.*` upgrade — no new render tool.**

1. `urlAllowed` learns `file:` URLs confined to the workspace root.
2. `PageHandle` gains `SetViewport`; `PageOptions` gains `Viewport`.
3. `ScreenshotOpts` gains `Selector`, `Quality`, `SettleMS`; a settle waits for
   `document.fonts.ready` plus a bounded pause before capture.
4. `browser.screenshot` returns `ToolResult.Images` instead of discarding the
   bytes, and its schema gains `selector`, `viewport`, `device_scale_factor`,
   `settle_ms`, `format`, `label`.

Rendering therefore becomes: `file.write` a scratch `.html` → `browser.navigate
file:///…/scratch/ui.html` → `browser.screenshot {viewport:{width:390,
height:844}, full_page:true}` → the model receives pixels. No renderer, no
second browser engine, no new Go module for either tool.

### 1.2 Placement

```
internal/capture/                     NEW leaf package: display detection + helper commands
  capture.go                          Backend interface, Target, Image, Env, Detect
  wlroots.go                          sway (swaymsg -t get_tree) + Hyprland (hyprctl -j activewindow), grim
  darwin.go                           screencapture -x / -l<id>, window id via JXA CGWindowList
  capture_test.go                     fake Runner; no real display touched
internal/tools/native/capture.go      NEW tool next to the images plan's image.view
internal/tools/desktop/browser/       viewport + settle + selector clip contracts
internal/tools/desktop/tools.go       screenshot returns images; navigate allows workspace file URLs
internal/tools/desktop/url_filter.go  file: branch scoped to the workspace root
```

`internal/capture` takes an injected command runner, so every backend is
testable without a compositor. It imports only the standard library.

### 1.3 Deliberately not building

- No `image.render` tool and no renderer package (see Context).
- No window-by-title/app picker and no region capture: `swaymsg`/`hyprctl`
  answer "which window is focused" without a picker, and a title match is a
  fuzzy guess across apps.
- No terminal image rendering: unchanged non-goal from the images spec; the TUI
  shows a chip.
- No image generation, OCR, video, PDF, or visual-regression diffing.
- No new Go module. Capture shells out to `grim` / `swaymsg` / `hyprctl` /
  `screencapture`; render reuses the `playwright-go` already in `go.mod`.
- No `save_path`: the store is the destination. Persisting a screenshot into the
  repo for a PR is a separate feature.

### 1.4 Sequencing

This design executes **after** `docs/vision/plans/2026-11-17-images-plan.md`
has landed. It depends on `internal/imagepayload.Store`, `schema.ImageRef`,
`registry.ToolResult.Images`, `ChatMessage.Images` and the `[images]` limits,
none of which exist in the tree yet. If the images plan has not landed, this
work is not executable — capture would have nowhere to put its bytes.

## 2. Components and data flow

### 2.1 `internal/capture` — detection and backends

```go
type Target string
const (
    TargetActiveWindow Target = "active_window"
    TargetScreen       Target = "screen"
)

// Runner runs one helper command. Stdout is the result; stderr folds into the
// error so a missing grim or a declined TCC prompt reads as itself.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

type Image struct {
    PNG     []byte // always PNG; the store re-encodes if it must
    Label   string // "window: kitty — ~/src/marshal" | "entire screen"
    Backend string // "sway" | "hyprland" | "darwin"
}

type Env struct {
    GOOS   string
    Getenv func(string) string
}

// Detect picks a backend for this session, or says why none can run.
func Detect(env Env, forced string) (Backend, error)

type Backend interface {
    Capture(ctx context.Context, t Target) (Image, error)
}
```

**Detection.** `forced` (from config) wins; otherwise `GOOS=darwin` → darwin,
and on linux `SWAYSOCK` set → sway, `HYPRLAND_INSTANCE_SIGNATURE` set →
hyprland. Anything else returns `ErrNoBackend` naming the supported sessions
and the variables it looked for. No guessing and no X11 fallback.

**Window targeting, per backend:**

| | active window | whole screen |
|---|---|---|
| sway | `swaymsg -t get_tree -r`, walk to the node with `"focused": true` that is a view (`window` or `pid` set), take its `rect` — sway emits absolute coordinates, so no ancestor origin is added → `grim -g "<x>,<y> <w>x<h>" -` | `grim -` |
| Hyprland | `hyprctl -j activewindow` → `at:[x,y]`, `size:[w,h]`, `class`, `title` → `grim -g "<x>,<y> <w>x<h>" -` | `grim -` |
| macOS | frontmost app's pid via `osascript -l JavaScript` (`CGWindowListCopyWindowInfo`, layer 0) → `screencapture -x -l<windowid> -t png <tmp>` | `screencapture -x -t png <tmp>` |

Both wlroots sessions target by **geometry**, which is why one shape covers
them: grim has no window-id capture, but the focused container's rect is exactly
the window. `screencapture` is the only helper that writes a file rather than
stdout, so the darwin backend owns a temp file and removes it. macOS Screen
Recording (TCC) refusal maps to `ErrPermission` naming the System Settings path,
not a raw exit status.

Width and height are **not** computed here: the payload store's normalize step
already decodes dimensions, and the tool reads them back from the `ImageRef` it
receives. One decode path, not two.

### 2.2 `internal/tools/native/capture.go` — the tool

Registered only when `[capture] enabled = true` **and** the payload store is
wired. A disabled capture is an absent tool, not a stubbed one, so no prompt can
offer it.

```
Name:            image.capture
Risk:            read_only
WorkspaceEffect: session_only
Schema: {"type":"object","properties":{
  "target":{"type":"string","enum":["active_window","screen"],"default":"active_window",
            "description":"active_window captures the focused window; screen captures every display"},
  "label":{"type":"string","description":"Short caption to identify this capture later"}},
  "additionalProperties":false}
```

`image.capture` registers the way `image.view` does in the images plan: through
`native.Options` and `RegisterAll`, not through a registration call of its own.
`native.Options` gains `Capture config.CaptureConfig` and
`CaptureRunner capture.Runner` (nil = the exec-backed runner), and `RegisterAll`
appends the tool beside the images gate:

```go
if tools.capture.Enabled && tools.images != nil {
    all = append(all, tools.captureTool())
}
```

That keeps one registration idiom and one "images off" story for both image
producers, and it is why the tool needs no `Options` struct of its own: the
toolset already holds the workspace root, the session state and the store.

Handler: detect (cached on the tool set; a failed capture drops the cache and
re-detects, so starting a compositor mid-session recovers), capture, then put:

```go
ref, err := opts.Store.Put(ctx, bytes.NewReader(img.PNG), imagepayload.Options{
    Label:  args.Label, // "" = the backend's label ("window: kitty — ~/src/marshal")
    Format: "png",
})
```

`Put` returns the `schema.ImageRef` the tool reports, so `Width`, `Height`,
`Bytes`, `Hash` and `Label` in the result are the store's own figures — capture
computes none of them. The handler then returns:

```go
ToolResult{
    Summary: `image.capture: window: kitty — ~/src/marshal → 1440×900 png (214 KB) hash 3f9a1c2e`,
    Content: `{"target":"active_window","backend":"sway","width":1440,"height":900,"bytes":219123}`,
    Images:  []schema.ImageRef{ref},
}
```

`Content` stays text deliberately: a model with no image capability still learns
what was captured and where, exactly as `image.view` behaves.

Config, beside `[images]`:

```toml
[capture]
enabled = false   # default off: this surface is privacy-sensitive
backend = ""      # "" = detect; "sway" | "hyprland" | "darwin"
timeout = "10s"   # per capture, not per command; grim on a large layout is not instant
```

### 2.3 `browser.*` — the render path

**Contracts** (`internal/tools/desktop/browser/backend.go`):

```go
type Viewport struct {
    Width, Height     int
    DeviceScaleFactor float64 // 0 = 1
}

type PageOptions struct {
    Viewport *Viewport // nil = backend default
}

type ScreenshotOpts struct {
    FullPage bool
    Format   string // "png" | "jpeg"
    Quality  int    // jpeg only, 0 = playwright default
    Selector string // clip to this element's bounding box
    SettleMS int    // extra settle after fonts.ready; default 150
}

// PageHandle: the seven existing methods (Navigate, Title, URL, Text,
// ReadableText, Click, Fill, PressKey, Submit, Close) are unchanged; these two
// are new or widened.
type PageHandle interface {
    …
    Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error)
    SetViewport(ctx context.Context, v Viewport) error
}

// BrowserBackend: Close and Mode are unchanged.
type BrowserBackend interface {
    NewPage(ctx context.Context, opts PageOptions) (PageHandle, error)
    …
}
```

`browser.Session` records `lastURL string`, set by a successful `Navigate`, so a
`DeviceScaleFactor` change can recreate the page and re-navigate to where the
session actually was rather than to what a tool last recorded.

**Why `SetViewport` and not "recreate the page with a viewport":** the render
flow is navigate → screenshot. If setting the viewport recreated the context,
the screenshot would capture a blank page and the document would have to be
re-fetched. `SetViewport` is `page.SetViewportSize` in the standalone backend
(Playwright's emulation override — DOM, scroll state and loaded document
survive and reflow) and the same call in `AttachBackend` for a CDP-attached
page. `DeviceScaleFactor` is a **context** option, so it is honoured only when
the session creates the page; when a screenshot asks for a DSF the existing page
cannot provide, the session recreates the page and re-navigates to the URL that
page is actually showing — read from the live page before it is closed, falling
back to `LastURL`. The tool summary reports the requested viewport; it does not
claim whether the page was resized in place or recreated, because `Session.Page`
does not report that.

**Settle.** `Screenshot` evaluates `document.fonts.ready`, sleeps `SettleMS`
(default 150 ms), then captures. This is the webfont / one-frame-layout fix;
`full_page` height is measured after the settle, not before. When `Selector` is
set it wins and `FullPage` is ignored; the clip box is the element's bounding
box and the schema description says so.

**`browser.screenshot`** gains `selector`, `viewport{width,height}`,
`device_scale_factor`, `settle_ms`, `format`, `label`. The `format` argument
overrides `[desktop] screenshot_format` (which stays the default), and `quality`
applies to JPEG only. Its bytes go to

```go
ref, err := store.Put(ctx, bytes.NewReader(png), imagepayload.Options{
    Format:  format,  // args.format, else cfg.ScreenshotFormat
    Quality: quality, // args.quality, else 0 (store default)
    Label:   label,   // args.label, else "screenshot of <url>"
})
```

and come back as `ToolResult.Images`. With no store wired
(`[images] enabled = false`) it keeps today's metadata-only result and the
summary says `(images disabled)`. Its description loses the stale "in a future
vision milestone, returns image bytes" sentence.

**`urlAllowed`** (`internal/tools/desktop/url_filter.go`) grows a workspace
root:

```go
func urlAllowed(rawURL string, allowlist, denylist []string, workspaceRoot string) error
```

Today `file://` can never pass: `parsed.Host == ""` rejects it before the scheme
switch even though `file` is listed. The file branch resolves the path, requires
it to exist and to resolve **inside** `workspaceRoot` (symlinks followed — a
link out of the tree is outside), rejects any `file://host/…` URL outright, and
applies denylist entries to the resolved path as path patterns. The allowlist
does not apply to file URLs: it governs network destinations.

`desktop.Options` gains `WorkspaceRoot string` and `ImageStore` (nil when images
are disabled).

**End-to-end:**

```
file.write  scratch/marshal-card.html
browser.navigate  file:///home/…/scratch/marshal-card.html     ← the one approval
browser.screenshot {viewport:{width:390,height:844}, full_page:true}
  → SetViewport in place → fonts.ready + settle → PNG
  → Store.Put → schema.ImageRef → ToolResult.Images → ChatMessage.Images → the model
```

One approval per session covers both calls (§3.4), so "write HTML, look at it,
fix the overflow" is a loop that does not re-prompt on every iteration.

**Fakes.** `browser/fake.go` grows the new signatures so desktop tool tests keep
recording calls.

## 3. Gating

### 3.1 Why `read_only` risk cannot carry the gate

`evaluateNonShell` consults permission rules **before** its risk fallback
(`internal/tools/policy/evaluation.go:74-77`), and an unruled `read_only` tool
falls through to `DecisionAllow`. A read-only screen grab would therefore never
prompt. The gate is a default rule, not a risk level — in `config.Default()`:

```go
Permissions: PermissionsConfig{
    Rules: []PermissionRule{
        {Permission: "image.capture", Pattern: "*", Action: "ask"},
    },
},
```

It is inert until `[capture] enabled = true`, and it exists so that opting into
capture does not silently also mean opting out of prompts. The risk level stays
`read_only`: the tool writes nothing to the workspace, and mislabelling it a
write would corrupt the Ask ceiling and the snapshot preflight.

### 3.2 The target is the subject, so grants stay narrow

- `subjectsForTool` (`internal/tools/policy/policy.go:320`) gains
  `case "image.capture": return []string{targetOf(args)}`, where `targetOf`
  reads `args["target"]` and defaults to `active_window`, matching the schema.
- `permissions.PatternForApproval` (`internal/permissions/pattern.go:18`) gains
  the same case.

Consequences: approving for the session on `active_window` does **not** grant
whole-screen capture, and config can keep the broad one shut —
`{permission = "image.capture", pattern = "screen", action = "deny"}`.
`evaluateSubjects` already resolves most-restrictive-wins per subject, and
`Matches` globs `*` over both targets for the default rule.

### 3.3 The three modes

| Mode | capture |
|---|---|
| **Act**, Auto off | prompts every call. `once`, `session` and `always` are all available: capture sets no `ExactCommandApproval`, so `forcedApproval` (`shell.sudo` / `mode.request` / `model-cost-consent` / exact / `mode-elevation:`) leaves it answerable. |
| **Act**, Auto on | no prompt: `applyExecutionMode` resolves `Confirm → Allow` under Auto and skips the `ReasonExplicitConfirm` hold. Auto is the user's standing grant for every tool; the guard is that capture ships off and must be switched on deliberately. |
| **Ask** (read-only mode) | no prompt: `read_only` passes the Ask ceiling, and the tool is not registered unless capture is on. |

No "sensitive read" risk class is introduced. Making capture prompt in Ask mode
is a new ceiling concept and a different piece of work.

### 3.4 Session-wide grant: one new typed list

Session approvals today are shell command strings: `State.AddSessionRule(prefix)`
appends to `sessionRules` and `matchSessionRule` parses them with
`mvdan.cc/sh`. A tool grant has no command to parse, so capture adds a parallel,
typed list. The answer path routes on the call's permission rather than on an
empty command: `requestApproval` fills `Command` with the tool's own name when
the tool supplies none, so a command-shaped rule can never match a command-less
tool:

- **`internal/app/session`** — the grant type is session-owned, because
  `internal/permissions` already imports `internal/app/session`
  (`permissions/pattern.go:9`), so the reverse import would be a cycle:

  ```go
  // PermissionGrant is a session-scoped allow for one tool permission and one
  // pattern. It carries no action: a session can only grant, never deny.
  type PermissionGrant struct {
      Permission string
      Pattern    string
  }

  func (s *State) AddSessionPermissionRule(g PermissionGrant)
  func (s *State) SessionPermissionRules() []PermissionGrant
  ```

  Both take `accessMu` and bump `access.Revision`, exactly as `AddSessionRule`
  does, so an authorization minted before the grant cannot cross it. Only
  `allow` is expressible because the type has no action field — a whole class of
  bug removed rather than validated against.
- **`internal/tools/policy`** — the grant rides the existing config-source seam
  rather than a one-shot setter. The source gains a fourth value
  (`internal/tools/policy` may import `internal/app/session` directly; the
  reverse is what would cycle):

  ```go
  type ConfigSource func() (cfg config.Config, sessionRules []string,
      grants []session.PermissionGrant, revision uint64)

  // SetSessionPermissions seeds the list for an engine with no config source
  // (tests, standalone registration). With a source installed, the source wins.
  func (pe *PolicyEngine) SetSessionPermissions(grants []session.PermissionGrant)
  ```

  `EvaluateInput` reads the source once and passes the grants to both evaluation
  paths (`evaluateNonShell` and `evaluateShellInput`), so a grant applies to the
  very next call, at the revision `AddSessionPermissionRule` bumped. The six
  `SetConfigSource` call sites in `app.go` keep their current shape because they
  pass the method value; the two three-value destructurings
  (`internal/agent/execute.go:149`, `internal/tools/native/command.go:202`) take
  a fourth `_`.

- **Precedence is explicit, because last-match-wins is not enough.**
  `permissions.Evaluate` returns the *last* matching rule's action, so simply
  appending session rules would let a session `allow` override a config `deny`.
  Instead `evaluateSubjects` takes both lists and combines them per subject:

  | config | session | result |
  |---|---|---|
  | `deny` | anything | **deny** |
  | `ask` | `allow` | **allow** (the grant) |
  | `ask` | none | **confirm** |
  | `allow` | anything | **allow** |
  | none | `allow` | **allow** |
  | none | none | no match — the risk fallback decides |

  Config rules therefore stay authoritative; a session grant can only lift an
  `ask`, never a `deny`. Neither `permissions.Evaluate` nor any config rule's
  meaning changes, which is why this stays a policy-package change.
- **`internal/app/app.go`** — no new wiring beyond the signature.
  `state.PolicySnapshot` (`internal/app/session/authority.go:64`) returns the
  grants with the rest of the owner tuple, and the six
  `pol.SetConfigSource(x.PolicySnapshot)` sites already pass it through.
  `RebaseInteractionConfig` keeps the config half: session and config rules stay
  separate lists, so a config edit cannot drop a grant and a new session cannot
  inherit one.
- **`internal/acp/service/answers.go`** — `sessionRule` adds a
  `session.PermissionGrant` for every call whose `PermissionForTool` is not
  `shell` (a shell call keeps the command-prefix behaviour); `alwaysRule` already saves
  `{PermissionForTool, PatternForApproval, allow}` to the user config, which for
  capture is exactly `image.capture` / `active_window`. Only its save-failure
  fallback changes, from `AddSessionRule(call.Command)` to
  `AddSessionPermissionRule` for a command-less tool.
- **Visibility** — `internal/acp/service/sheet.go`'s `Rules:` stays the shell
  list; `acpwire.SessionSheet` gains `Permissions []SheetPermissionRule`
  (`Permission`, `Pattern`, `Scope`) so the sheet shows
  `image.capture active_window — allowed for this session`, and
  `buildTelemetry` (`internal/acp/turn.go:1615`) adds `"permissionRules"` beside
  `"rules"`. Both are additive. `web/bridge/contract` does not mirror
  `SessionSheet` (verified: no `SessionSheet` in `web/bridge/contract` or
  `contract.ts`), so no TypeScript regeneration is involved.

### 3.5 The prompt and the audit trail

No TUI change. The generic approval prompt renders the tool name, its
description and its args: *Capture the desktop and return the image…
active_window captures the focused window* / `image.capture
{"target":"active_window"}`. Open windows are deliberately **not** enumerated in
the prompt — enumerating titles is itself a privileged query, and it would show
what is on screen before the user consents to look.

After the fact, `registry.NewAuditEvent` records every call with its approval
outcome, and the tool's `Summary` names target, backend, window title,
dimensions and hash. The pixels live in the image store, referenced by hash —
not in the audit log.

### 3.6 Failure behaviour

- **Helper missing** — the first capture fails with `ErrHelperMissing` and the
  message names the helper: `capture: grim not found (install grim for
  sway/Hyprland); set [capture] enabled = false to turn capture off`. Detection
  is lazy by design: a registration-time probe would have to inspect PATH, and
  would refuse a session that could still capture once the helper is installed.
- **No compositor visible** — e.g. a systemd-managed daemon child without the
  session environment → `ErrNoBackend` naming exactly what was looked for:
  `SWAYSOCK`, `HYPRLAND_INSTANCE_SIGNATURE`, `GOOS=darwin`.
- **macOS TCC declined or revoked** → `ErrPermission` with the System Settings
  path and which app needs Screen Recording.
- **Over the image limit** → `imagepayload.ErrTooLarge` passes through with the
  limit and the actual size; capture never downscales, because the store's
  normalize owns re-encoding.

## 4. Testing

No display is touched: capture backends run against a fake runner, browser logic
against the existing fake page. Real-browser cases keep the discipline
`standalone_test.go` already sets (`testing.Short()` + `skipWithoutDriver`).

### 4.1 `internal/capture/capture_test.go`

A `fakeRunner` returning canned stdout per command and recording every call:

| case | asserts |
|---|---|
| sway tree, focused node nested two containers deep | `swaymsg -t get_tree -r` once, then `grim -g "1440,0 1440x900" -` — offsets accumulated through parents |
| Hyprland `hyprctl -j activewindow` | `grim -g "0,32 1280x720" -`; `class`/`title` land in `Image.Label` |
| `target=screen` on both wlroots backends | exactly `grim -`, never `-g` |
| darwin screen | `screencapture -x -t png <tmp>`; bytes read back and the temp file removed |
| darwin window | frontmost-pid lookup, then `screencapture -x -l<id> -t png <tmp>` |
| no focused node in the tree | error naming the condition, no blank capture |
| stderr from `grim` / TCC refusal from `screencapture` | `ErrNoBackend` / `ErrPermission` with an actionable message |
| detection table | `SWAYSOCK` only → sway; `HYPRLAND_INSTANCE_SIGNATURE` only → hyprland; `GOOS=darwin` → darwin; neither → `ErrNoBackend`; `forced` overrides all |

No `TestMain` and no `testenv.Run`: the package never reads ambient env; `Env`
is passed in.

### 4.2 `internal/tools/native/capture_test.go`

- **Registration gate:** enabled + store → present; enabled without store →
  absent; disabled with store → absent.
- **Schema:** compiles, rejects unknown properties (`additionalProperties:
  false`), rejects `target: "window"`.
- **Success shape:** `Store.Put` called once, one `ImageRef`, `Content` decodes
  with `target`/`backend`/`width`/`height`/`bytes`, `Summary` contains target,
  backend and dimensions.
- **Errors:** `ErrNoBackend` reaches the model as the tool error text, and no
  store write happens.

### 4.3 `internal/tools/desktop/browser`

- **`viewport_test.go`** (fake page): a width/height change applies
  `SetViewport` **without** recreating the page; a `DeviceScaleFactor` change
  recreates and re-navigates to `LastURL`; a `SetViewport` error falls back to
  recreate-and-renavigate; navigate → screenshot keeps the loaded document when
  the viewport is applied in place.
- **Settle and clip:** `ScreenshotOpts{Selector, SettleMS}` records
  `document.fonts.ready` evaluation and the clip box on the fake page.
- **Real browser** (`testing.Short()` + `skipWithoutDriver`): one case renders
  an `httptest`-served page with a delayed webfont and asserts the settle put
  glyph pixels where expected; one case clips a selector and asserts the PNG is
  the element's box, not the viewport's.

### 4.4 `internal/tools/desktop` — tool level

- `TestBrowserScreenshotReturnsImages` (store wired) and the existing
  `TestBrowserScreenshotReturnsMetadata` retained as the store-absent case, its
  summary asserting `(images disabled)`.
- `url_filter_test.go` file-URL table: inside the workspace → allowed; `../`
  escape → blocked; symlink out → blocked; `file://host/path` → blocked; a
  configured allowlist does not block a workspace file; a denylist path entry
  still blocks it.
- `TestRegisterAllRegistersSixTools` stays six; capture registers separately.
- `schemas_test.go` keeps asserting `additionalProperties: false` for the
  extended screenshot schema.

### 4.5 Policy and session — the grant, not just the prompt

- **`internal/tools/policy`:** a session permission rule turns `image.capture`
  from `DecisionConfirm` to `DecisionAllow`; a config `deny` on `screen` still
  wins over a session `allow`; a session rule with action `ask`/`deny` is
  rejected at insertion; the default rule yields `DecisionConfirm` for both
  targets; Auto resolves to allow; Ask stays allowed — the §3.3 table as three
  assertions.
- **`internal/app/session`:** `AddSessionPermissionRule` bumps the access
  revision (a pre-grant authorization is then stale), and
  `SessionPermissionRules()` returns a copy.
- **`internal/acp/service`:** `ApprovalSession` adds a typed rule for every
  non-shell tool and keeps the command-prefix path for shell;
  `ApprovalAlways` writes `{image.capture, active_window, allow}` to the user
  config path, and its save-failure fallback lands on the typed session rule.
- **Sheet/telemetry:** `Rules` and `Permissions` both populate; if
  `web/bridge/contract` mirrors `SessionSheet`, extend it and run
  `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -update`
  plus `npm run check`.

### 4.6 Commands

```bash
CGO_ENABLED=1 go build ./...
go vet ./...
gofmt -l .
scripts/test.sh quick
scripts/test.sh --base origin/main
go test ./internal/capture/... ./internal/tools/desktop/... -count=1
go test ./internal/tools/policy/... ./internal/app/session/... ./internal/acp/service/... -count=1
```

### 4.7 Not automated, and why

No compositor, no display and no macOS Screen Recording grant exists in CI, so
the usability harness and the e2e runner do not drive `image.capture`. The
argv is what the fake runner pins; a manual checklist confirms the rest:
active-window capture on sway and Hyprland (including a floating window over
another), whole-screen on each, macOS window and screen, the TCC-denied path,
and the "no compositor in env" path on a systemd-started daemon child.

## 5. Rollout, compatibility, docs

### 5.1 Sequencing

1. **The images plan lands** — `image.view` registers and returns refs; the
   store decodes and reports dimensions.
2. **This work lands** — capture is the second producer of `ImageRef` and reuses
   the store's decode path for its summary rather than parsing PNG headers.

### 5.2 Wiring

`internal/app/app.go:1539` builds `desktop.Options` inside
`if cfg.Desktop.Enabled`. It gains two fields:

```go
desktopOpts := desktop.Options{
    Config:         cfg.Desktop,
    BackendFactory: newDesktopBackend, // unchanged
    SessionState:   state,
    WorkspaceRoot:  state.WorkingDir,  // the same source native.Options uses (app.go:1146)
    ImageStore:     imageStore,        // nil when [images] enabled = false
}
```

Capture needs no block of its own: `nativeOpts` (`app.go:1145`) already carries
`WorkspaceRoot: state.WorkingDir`, and the images plan adds
`Images: imageStore` to it. This design adds two more:

```go
nativeOpts := native.Options{
    WorkspaceRoot: state.WorkingDir,
    // …
    Images:  imageStore, // added by the images plan
    Capture: cfg.Capture,
}
```

`imageStore` is the store the images plan constructs (`imagepayload.Open`);
handing the same value to both registrations is what makes "images off" one
story rather than two. The local daemon's child inherits the session
environment, so `SWAYSOCK` / `HYPRLAND_INSTANCE_SIGNATURE` are visible to it; a
daemon started outside the graphical session is what `ErrNoBackend` reports.

### 5.3 Compatibility

- **`browser.screenshot` output changes** from metadata to an image. Its
  `Content` stays valid JSON with `url`, `format`, dimensions and (when clipped)
  the selector, so a text-only model still gets a usable answer; the images
  plan's downgrade policy applies unchanged, and the summary says
  `(images disabled)` when no store is wired.
- **`urlAllowed` gains a parameter.** Internal only (`navigateTool` is the sole
  caller); the change is a compile error at the call site, not a silent
  behaviour change. The `file:` branch is a widening, narrowed to the workspace
  root.
- **The default permission rule is a defaults change**, so it appears for every
  existing user on upgrade. It is inert until `[capture] enabled = true`, and
  `config.Default()`'s `Permissions.Rules` is currently `nil`, so this is the
  first default rule — call it out in the commit message.
- **Project-config capture.** A `.marshal/config.toml` may set `[capture]` like
  any other table and is read only on a trusted project. It cannot pre-authorize
  itself past the prompt except through the same rules boundary every other tool
  lives inside. The session grant stays per-user and per-session.
- **`acpwire.SessionSheet` gains `Permissions`** — additive; an older client
  ignores it. Regenerate `web/ui/src/lib/contract.ts` if the sheet is mirrored
  in `web/bridge/contract`.
- **No persistence migration.** No DB schema change, no stored-format change, no
  snapshot change: capture declares `WorkspaceEffectSessionOnly`.
- **No CI change.** `internal/capture` is pure Go.

### 5.4 Known rough edges

- **Fractional scaling.** Capture passes no `-s`; the compositor's scale decides
  the pixel size, and the summary reports the dimensions the store decoded, so
  the tool never asserts a size it did not observe.
- **macOS TCC after an upgrade.** Revoking Screen Recording turns every capture
  into `ErrPermission` naming the setting, not a bare exit code.
- **Approval fatigue** is why the session grant exists; `docs/tools/README.md`
  must say so next to the rule.
- **`selector` with `full_page`** — `selector` wins and `full_page` is ignored;
  the schema description says so.

### 5.5 Docs to update with the work

- `docs/vision/design.md` — link this design for the follow-up sections it left
  open.
- `AGENTS.md` "Where things live" — `internal/capture/` under Tools & safety,
  plus the two image producers.
- `docs/ARCHITECTURE.md` layer table — the `internal/capture` row.
- `docs/tools/README.md` — the `browser.*` gate paragraph gains
  `browser.screenshot`'s image output, the `[capture]` table, the two targets
  and the session-grant note.
- The plan file for this work, written after this spec is approved.

## Decisions and trade-offs

1. **No renderer; extend `browser.*`.** One browser, one lifecycle, one
   hardening story, zero new dependencies — at the cost that rendering requires
   `[desktop] enabled` and a workspace file URL. Rejected: a second headless
   engine per render, which duplicates the session, timeout and hardening work.
2. **Two targets only, three backends.** `active_window` + `screen` on
   sway, Hyprland and macOS, done properly. Rejected: title/app pickers, region
   capture, X11, GNOME and KDE portals — each is a different discovery
   mechanism and a different helper.
3. **Geometry targeting on wlroots** rather than a window-id API, because grim
   has none and the focused container's rect is the window.
4. **Default `ask` rule rather than a risk bump.** Risk stays honest
   (`read_only`); the prompt comes from the rule, which is the only mechanism
   `evaluateNonShell` honours before its risk fallback.
5. **Target-as-subject.** Grants and denials are per target, so the session
   grant for `active_window` never becomes a blanket whole-screen grant.
6. **A typed session permission list instead of overloading session command
   rules.** The typed list keeps `mvdan.cc/sh` parsing meaning what it means, and
   it bumps the access revision like every other grant.
7. **`SetViewport` in place, recreate only for DSF.** Preserves the loaded
   document across a viewport change, which is the whole point of
   navigate → screenshot.
8. **Settle on `document.fonts.ready` + bounded delay.** Fixes the webfont and
   one-frame-layout class of wrong pixels without a heuristic full-page wait.

## Assumptions

- `[capture] enabled = false` by default. Capture is opt-in; nothing captures
  the screen on a fresh install.
- `DefaultSettleMS = 150` and the default `SettleMS` applies when the argument
  is omitted.
- `quality` is honoured for JPEG only; for PNG it is ignored, as Playwright
  does.
- The image store's limits (`[images]` max input bytes, per-image and per-turn
  caps) apply to capture and screenshot output unchanged; capture adds no limit
  of its own.
- `image.capture`'s `label` argument becomes `imagepayload.Options.Label` and so
  `schema.ImageRef.Label`; when it is empty the backend's own label
  (`window: kitty — ~/src/marshal`, `entire screen`) is used, and the summary
  always shows that label.
- `ErrNoBackend`, `ErrPermission` and `ErrHelperMissing` live in
  `internal/capture` as sentinel errors so tests can assert them and the tool
  can map them to model-facing text.
- The images plan's registration gate for `image.view` (store wired + config
  enabled) is the pattern `capture.Register` follows exactly.
- A wlroots `grim` capture uses the compositor's scale; no `-s` flag is passed.

## Explicit non-goals

- Window-by-title or app-name picking; region/rectangle capture.
- X11, GNOME (portal) and KDE (spectacle) backends.
- Terminal image rendering; the TUI shows a chip (unchanged from the images
  spec).
- Saving captures or renders into the repository (`save_path`).
- Image generation, OCR, PDF or video capture.
- Visual-regression diffing between two images.
- A "sensitive read" risk class or a policy ceiling that prompts in Ask mode.
- Any new Go module or third-party dependency.

## Open questions

None.
