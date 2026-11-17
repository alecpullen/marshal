# Capture and render — implementation plan

**Spec:** `docs/vision/capture-and-render-design.md` (approved).
**Authoring base:** `fix/subagent-concurrency-ceiling` at `f81b0044`
("subagents: raise the concurrency ceiling to 10"). Re-verify anchors if the
branch has moved.
**Hard dependency:** `docs/vision/plans/2026-11-17-images-plan.md` must have
executed. Tasks 4, 7, 9 and 10 assume `internal/imagepayload` exists,
`schema.ImageRef` and `registry.ToolResult.Images` are real,
`ChatMessage.Images` is real, `native.Options.Images` is wired, and
`image.view` registers. If any of that is missing, stop and land the images
plan first — do not stub it here.

**Goal.** The agent can photograph the desktop (`image.capture`, active window
or whole screen) and can look at HTML/CSS it just wrote (`browser.*` opens a
workspace file and returns the screenshot as an image), with one approval per
session for the render loop.

**Non-goals.** No renderer package, no new Go module, no window picker, no
region capture, no X11/GNOME/KDE backends, no terminal image rendering, no
`save_path`, no visual-regression diffing. See spec §1.3.

**Assumptions carried from the spec.** `[capture] enabled = false` by default;
`DefaultSettleMS = 150`; `quality` applies to JPEG only; image-store limits
apply unchanged; `selector` wins over `full_page`; a wlroots capture passes no
`-s`.

---

## Contracts

Every type or signature more than one task touches. Implement these exactly;
tasks below refer to them rather than restating them.

### `internal/capture` (new package — Tasks 1, 2, 4)

```go
package capture

type Target string

const (
	TargetActiveWindow Target = "active_window"
	TargetScreen       Target = "screen"
)

// Runner runs one helper command. Stdout is the result; stderr folds into the
// error so a missing grim or a declined TCC prompt reads as itself.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Env is the ambient session a backend detects from. Tests pass it explicitly;
// production passes runtime.GOOS and os.Getenv.
type Env struct {
	GOOS   string
	Getenv func(string) string
}

type Image struct {
	PNG     []byte // always PNG
	Label   string // "window: kitty — ~/src/marshal" | "entire screen"
	Backend string // "sway" | "hyprland" | "darwin"
}

type Backend interface {
	Capture(ctx context.Context, t Target) (Image, error)
}

var (
	ErrNoBackend     = errors.New("capture: no supported display session")
	ErrPermission    = errors.New("capture: screen recording is not permitted")
	ErrHelperMissing = errors.New("capture: a required helper command is missing")
	ErrNoFocused     = errors.New("capture: no focused window")
)

// Detect picks a backend for this session, or says why none can run.
func Detect(env Env, forced string) (Backend, error)
```

### `internal/app/config` (Task 3)

```go
// CaptureConfig is the desktop-capture surface. Off by default: a process that
// can photograph the screen is opt-in, never assumed.
type CaptureConfig struct {
	Enabled bool          `toml:"enabled"`
	Backend string        `toml:"backend"` // "" = detect; "sway" | "hyprland" | "darwin"
	Timeout time.Duration `toml:"timeout"`
}

// Config gains, after Desktop:
Capture CaptureConfig `toml:"capture"`
```

### `internal/tools/native` (Task 4)

```go
// Options gains:
Images        *imagepayload.Store // set by the images plan
Capture       config.CaptureConfig
CaptureRunner capture.Runner // nil = exec-backed
CaptureEnv    capture.Env    // zero value = runtime.GOOS + os.Getenv

// toolSet gains:
images        *imagepayload.Store
capture       config.CaptureConfig
captureRunner capture.Runner
captureEnv    capture.Env
captureMu     sync.Mutex
captureBack   capture.Backend // detected lazily, dropped on a failed capture
```

### `internal/tools/desktop/browser` (Task 5)

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
	Quality  int    // jpeg only, 0 = default
	Selector string // clip to this element's bounding box
	SettleMS int    // settle after fonts.ready; <=0 uses DefaultSettleMS
}

const DefaultSettleMS = 150

// ErrViewportNeedsRecreate reports that the requested viewport cannot be applied
// to the current page in place, because the device scale factor is a context
// option. The caller recreates the page and re-navigates.
var ErrViewportNeedsRecreate = errors.New("browser: viewport needs a new page")

// PageHandle: the ten existing methods (Navigate, Title, URL, Text,
// ReadableText, Click, Fill, PressKey, Submit, Close) are unchanged; these two
// are new or widened.
Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error)
SetViewport(ctx context.Context, v Viewport) error

// BrowserBackend: Close and Mode unchanged.
NewPage(ctx context.Context, opts PageOptions) (PageHandle, error)

// Session gains, plus an unexported pageViewport *Viewport recording what the
// current page was created with:
func (s *Session) Page(ctx context.Context, opts PageOptions) (PageHandle, error)
func (s *Session) Navigate(ctx context.Context, url string) error // records lastURL on success
func (s *Session) LastURL() string
```

### `internal/tools/desktop` (Tasks 6, 7)

```go
// Options gains:
WorkspaceRoot string               // navigate's file: root
ImageStore    *imagepayload.Store  // nil = images disabled

func urlAllowed(rawURL string, allowlist, denylist []string, workspaceRoot string) error
```

### `internal/app/session` (Task 8)

```go
// PermissionGrant is a session-scoped allow for one tool permission and one
// pattern. It carries no action: a session can only grant, never deny.
type PermissionGrant struct {
	Permission string
	Pattern    string
}

func (s *State) AddSessionPermissionRule(g PermissionGrant)
func (s *State) SessionPermissionRules() []PermissionGrant

// PolicySnapshot gains a third return:
func (s *State) PolicySnapshot() (config.Config, []string, []PermissionGrant, uint64)
```

### `internal/tools/policy` (Task 8)

```go
type ConfigSource func() (config.Config, []string, []session.PermissionGrant, uint64)

func (pe *PolicyEngine) SetConfigSource(src ConfigSource)

// SetSessionPermissions seeds grants for an engine with no config source.
// With a source installed, the source wins.
func (pe *PolicyEngine) SetSessionPermissions(grants []session.PermissionGrant)

func evaluateSubjects(rules []permissions.Rule, grants []session.PermissionGrant, permissionName string, subjects []string) (Decision, bool)
```

### `internal/permissions` (Task 8)

```go
// PermissionMatches reports whether a rule's permission name governs a tool's
// permission name (the same relation permissionMatches implements, exported for
// session grants).
func PermissionMatches(rulePermission, toolPermission string) bool
```

### `internal/acpwire` (Task 9)

```go
// SheetPermissionRule is one session-scoped permission grant.
type SheetPermissionRule struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Scope      string `json:"scope"` // "session"
}

// SessionSheet gains, after Rules:
Permissions []SheetPermissionRule `json:"permissions,omitempty"`
```

---

## Task 1: `internal/capture` — types, detection, wlroots backends

**Goal.** A compositor-backed capture backend exists for sway and Hyprland, and
`Detect` chooses one from the environment or explains why it cannot.

**Files.**
- `internal/capture/capture.go` (new)
- `internal/capture/wlroots.go` (new)
- `internal/capture/capture_test.go` (new)

**Steps.**

1. `capture.go`: the Contracts block above verbatim, plus `Detect`:

```go
func Detect(env Env, forced string) (Backend, error) {
	switch forced {
	case "sway":
		return &wlrootsBackend{kind: "sway", run: defaultRunner, env: env}, nil
	case "hyprland":
		return &wlrootsBackend{kind: "hyprland", run: defaultRunner, env: env}, nil
	case "":
	default:
		return nil, fmt.Errorf("%w: unknown backend %q", ErrNoBackend, forced)
	}
	if env.Getenv("SWAYSOCK") != "" {
		return &wlrootsBackend{kind: "sway", run: defaultRunner, env: env}, nil
	}
	if env.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		return &wlrootsBackend{kind: "hyprland", run: defaultRunner, env: env}, nil
	}
	return nil, fmt.Errorf("%w: looked for SWAYSOCK and HYPRLAND_INSTANCE_SIGNATURE", ErrNoBackend)
}
```

**Task 2 extends this** with `case "darwin"`, the `env.GOOS == "darwin"` branch
and the final message naming `GOOS=darwin` — do not reference `darwinBackend`
before Task 2 creates it.

`defaultRunner` is an `exec.CommandContext` runner that returns stdout and folds
stderr into the error (`fmt.Errorf("%s %s: %w: %s", name, …)`), so a missing
binary surfaces as an `*exec.Error` the caller can map. Keep it in
`capture.go`.

2. `wlroots.go` — one type, two dialects:

```go
type wlrootsBackend struct {
	kind string // "sway" | "hyprland"
	run  Runner
	env  Env
}
```

- `sway` + `TargetActiveWindow`: `run(ctx, "swaymsg", "-t", "get_tree", "-r")`,
  decode the tree, walk `nodes` and `floating_nodes` recursively accumulating
  `rect.x`/`rect.y` from the parents, and return the first node with
  `"focused": true` that has `window != 0`. Its absolute rect is the window.
  `app_id` is the class; `name` is the title. No focused window →
  `ErrNoFocused`.
- `hyprland` + `TargetActiveWindow`: `run(ctx, "hyprctl", "-j",
  "activewindow")`, decode `{class, title, at [x,y], size [w,h]}`. An empty
  object or zero `size` → `ErrNoFocused`.
- Both + `TargetScreen`: `run(ctx, "grim", "-")`.
- Both + active window: `run(ctx, "grim", "-g", fmt.Sprintf("%d,%d %dx%d", x, y, w, h), "-")`.
- Label: `fmt.Sprintf("window: %s — %s", class, title)`; screen:
  `"entire screen"`. `Image.Backend` is `kind`.
- A `grim` failure whose error wraps `*exec.Error` returns
  `fmt.Errorf("%w: grim (install grim, or set [capture] enabled = false): %v", ErrHelperMissing, err)`.

3. `capture_test.go`: a fake runner keyed by command name that records argv and
   returns canned output. Cases (one test function each):
   - `TestSwayActiveWindowGeometry` — nested tree two containers deep; asserts
     `swaymsg -t get_tree -r` then `grim -g "1440,0 1440x900" -` and the label.
   - `TestHyprlandActiveWindowGeometry` — `grim -g "0,32 1280x720" -`.
   - `TestScreenCaptureUsesNoGeometry` — both kinds produce exactly
     `grim -`.
   - `TestSwayNoFocusedWindow` / `TestHyprlandNoFocusedWindow` —
     `errors.Is(err, ErrNoFocused)`.
   - `TestGrimMissingMapsToErrHelperMissing`.
   - `TestDetect` — table: `SWAYSOCK` only → sway; `HYPRLAND_INSTANCE_SIGNATURE`
     only → hyprland; neither → `ErrNoBackend`; forced overrides a sway
     environment; unknown forced value → error. (Task 2 adds the darwin rows.)

**Verify.**

```bash
go test ./internal/capture -run 'TestSway|TestHyprland|TestScreen|TestGrim|TestDetect' -count=1
go vet ./internal/capture
```

---

## Task 2: `internal/capture` — macOS backend

**Goal.** `screencapture` covers whole screen and active window on macOS, with
TCC refusal reported as `ErrPermission`.

**Files.**
- `internal/capture/darwin.go` (new)
- `internal/capture/darwin_test.go` (new)

**Steps.**

1. Extend `Detect` in `capture.go` — add the darwin branch to the `forced`
   switch, the `GOOS` branch, and widen the final message:

```go
	case "darwin":
		return &darwinBackend{run: defaultRunner}, nil
	…
	if env.GOOS == "darwin" {
		return &darwinBackend{run: defaultRunner}, nil
	}
	…
	return nil, fmt.Errorf("%w: looked for SWAYSOCK, HYPRLAND_INSTANCE_SIGNATURE and GOOS=darwin; screen capture needs sway, Hyprland or macOS", ErrNoBackend)
```

2. `darwinBackend` with `run Runner`; `Capture`:
   - `TargetScreen`: create a temp file with `os.CreateTemp("", "marshal-capture-*.png")`,
     close and remove it, run `screencapture -x -t png <path>`, read the file,
     remove it, return `Image{PNG: data, Label: "entire screen", Backend: "darwin"}`.
   - `TargetActiveWindow`: `windowID` from
     `run(ctx, "osascript", "-l", "JavaScript", "-e", script)` where `script`
     is a single-line JXA program returning the CGWindowID string of the
     frontmost application's layer-0 window:

     ```js
     ObjC.import('Cocoa'); ObjC.import('CoreGraphics');
     var app = $.NSWorkspace.sharedWorkspace.frontmostApplication;
     var list = ObjC.deepUnwrap($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly, $.kCGNullWindowID));
     var pid = app.processIdentifier;
     var win = list.find(w => w.kCGWindowLayer == 0 && w.kCGWindowOwnerPID == pid);
     win ? String(win.kCGWindowNumber) : "";
     ```

     Empty output → `ErrNoFocused`. Then `screencapture -x -l<id> -t png <path>`.
     Label: `"window id <id>"` (the id is all macOS gives without Screen
     Recording metadata; the JXA owner name is added when present).
   - A `screencapture`/`osascript` failure whose stderr mentions
     "not authorized", "screen recording" or exit status without a file
     returns `fmt.Errorf("%w: grant Screen Recording to the app running marshal "+
        "in System Settings → Privacy & Security → Screen Recording", ErrPermission)`.
     Otherwise wrap with the helper name.

3. `darwin_test.go` uses the same fake runner:
   - `TestDarwinScreenWritesThroughTempFile` — the fake writes a valid 1×1 PNG
     to the path that appears after `-t png`; assert the returned bytes decode
     and that the temp path no longer exists.
   - `TestDarwinActiveWindowUsesWindowID` — fake returns `"12345"` for the
     osascript call and asserts `screencapture -x -l12345 -t png <tmp>`.
   - `TestDarwinEmptyWindowIDIsNoFocused`.
   - `TestDarwinTCCDeniedMapsToErrPermission` — fake stderr contains
     "not authorized".
   These tests never execute on Linux: the backend is constructed directly and
   the runner is fake.

**Verify.**

```bash
go test ./internal/capture -run 'TestDarwin' -count=1
go test ./internal/capture -count=1
```

---

## Task 3: config — the `[capture]` table and the default ask rule

**Goal.** `[capture]` parses, merges and defaults, and `image.capture` prompts by
default in Act mode.

**Files.**
- `internal/app/config/types.go`
- `internal/app/config/file_types.go`
- `internal/app/config/defaults.go`
- `internal/app/config/merge.go`
- `internal/app/config/config_test.go` (add cases)

**Steps.**

1. `types.go` — after `DesktopConfig` (`internal/app/config/types.go:441`):

```go
// CaptureConfig is the desktop-capture surface. Off by default: a process that
// can photograph the screen is opt-in, never assumed.
type CaptureConfig struct {
	Enabled bool          `toml:"enabled"`
	Backend string        `toml:"backend"` // "" = detect; "sway" | "hyprland" | "darwin"
	Timeout time.Duration `toml:"timeout"`
}
```

and in `Config`, after the `Desktop` field (`types.go:26`):

```go
	Capture       CaptureConfig                         `toml:"capture"`
```

2. `file_types.go` — after `fileDesktop` (`file_types.go:160`):

```go
type fileCapture struct {
	Enabled *bool   `toml:"enabled"`
	Backend *string `toml:"backend"`
	Timeout *string `toml:"timeout"`
}
```

and in `configFile` after `Desktop` (`file_types.go:366`):

```go
	Capture     *fileCapture     `toml:"capture"`
```

3. `defaults.go` — in `Default()`, beside `Desktop:` (`defaults.go:133`):

```go
		Capture: CaptureConfig{
			Enabled: false,
			Backend: "",
			Timeout: 10 * time.Second,
		},
```

and replace `Permissions: PermissionsConfig{Rules: nil}` (`defaults.go:165-167`)
with:

```go
		Permissions: PermissionsConfig{
			// capture is a read-only tool, so risk alone cannot prompt it; the
			// rule is what makes "switched on" different from "unattended".
			Rules: []PermissionRule{
				{Permission: "image.capture", Pattern: "*", Action: "ask"},
			},
		},
```

4. `merge.go` — after the `file.Desktop` block (`merge.go:418-437`), mirror that
   block's structure exactly:

```go
	if file.Capture != nil {
		set(&cfg.Capture.Enabled, file.Capture.Enabled)
		set(&cfg.Capture.Backend, file.Capture.Backend)
		if file.Capture.Timeout != nil && *file.Capture.Timeout != "" {
			d, err := time.ParseDuration(*file.Capture.Timeout)
			if err != nil {
				return fmt.Errorf("capture.timeout: %w", err)
			}
			cfg.Capture.Timeout = d
		}
	}
```

5. `config_test.go`: `TestCaptureDefaults` (off, empty backend, 10s) and
   `TestCaptureMerge` (a file layer setting `enabled = true`, `backend =
   "hyprland"`, `timeout = "2s"` overrides; an invalid duration is an error).
   Also assert `Default().Permissions.Rules` contains exactly the one
   `image.capture` ask rule — this is the first non-nil default rule set, so the
   assertion is the record of that decision.

**Verify.**

```bash
go test ./internal/app/config -run 'TestCapture|TestDefault' -count=1
go test ./internal/app/config -count=1
```

---

## Task 4: the `image.capture` tool

**Goal.** `image.capture` is registered only when capture is enabled and the
image store is wired, prompts by default, and returns an `ImageRef`.

**Files.**
- `internal/tools/native/capture.go` (new)
- `internal/tools/native/capture_test.go` (new)
- `internal/tools/native/native.go`

**Steps.**

1. `native.go`: add the three `Options`/`toolSet` fields from Contracts, set
   them in `newToolSet` (`native.go:409`) beside the existing `opts` copies, and
   in `RegisterAll` (`native.go:277`) add, next to the `tools.symbols != nil`
   gate at `native.go:309`:

```go
	// image.capture needs both an enabled capture surface and the image store
	// the images plan wires: without the store it could not return an image.
	if tools.capture.Enabled && tools.images != nil {
		all = append(all, tools.captureTool())
	}
```

2. `capture.go`:

```go
func (t *toolSet) captureTool() registry.Tool {
	return registry.Tool{
		Name:        "image.capture",
		Description: "Capture the desktop and return the image so you can look at it. target=active_window (default) captures the focused window; target=screen captures every display. Requires approval.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","enum":["active_window","screen"],"default":"active_window","description":"active_window captures the focused window; screen captures every display"},"label":{"type":"string","description":"Short caption to identify this capture later"}},"additionalProperties":false}`),
		Risk:            registry.RiskReadOnly,
		WorkspaceEffect: registry.WorkspaceEffectSessionOnly,
	}
}
```

with `Handler` that:

- decodes `{Target, Label}` (default `Target` to `"active_window"`),
- validates the target against the two constants (a bad value is an error, not a
  silent default),
- gets the backend under `captureMu`, calling `capture.Detect(t.captureEnv,
  t.capture.Backend)` on first use — `newToolSet` fills a zero `Env` with
  `runtime.GOOS`/`os.Getenv` and a nil `Runner` with an exec-backed one, so
  tests inject both without touching the ambient environment;
- on **any** capture error, drop the cached backend so the next call re-detects,
- runs the capture under `context.WithTimeout(ctx, t.capture.Timeout)` when
  `Timeout > 0`,
- puts the bytes:

```go
	ref, err := t.images.Put(ctx, bytes.NewReader(img.PNG), imagepayload.Options{
		Label:  label,
		Format: "png",
	})
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("capture: store image: %w", err)
	}
	label = ref.Label
	return registry.ToolResult{
		Summary: fmt.Sprintf("image.capture: %s → %d×%d %s (%s) hash %s",
			label, ref.Width, ref.Height, shortMIME(ref.MIME), humanBytes(ref.Bytes), ref.Hash),
		Content: fmt.Sprintf(`{"target":%q,"backend":%q,"width":%d,"height":%d,"bytes":%d}`,
			args.Target, img.Backend, ref.Width, ref.Height, ref.Bytes),
		Images: []schema.ImageRef{ref},
	}, nil
```

`shortMIME`/`humanBytes`: the images plan's Task 8 defines them as unexported
helpers in `internal/tools/native/image.go`; reuse them if present, otherwise
add them there rather than in `capture.go` — do not import `internal/agent`.

3. `capture_test.go`, using a `capture.Runner` fake and a real
   `imagepayload.Open(t.TempDir(), cfg)` store. Follow the images plan's
   `image_test.go` for how it builds a `native.Options` with a store.
   - `TestCaptureRegisteredOnlyWhenEnabledAndStored` — four combinations
     (`capture.Enabled` × store) through `RegisterAll`, asserting presence.
   - `TestCaptureIsReadOnlyAndSessionOnly` — risk and workspace effect.
   - `TestCaptureUnknownTargetRejected` — `target: "window"` is a schema error.
   - `TestCaptureReturnsOneRef` — one image ref, `Content` decodes with target,
     backend and dimensions, `Summary` contains the label and dimensions.
   - `TestCaptureNoBackendIsToolError` — a zero `Env` with no forced backend
     yields an error mentioning `SWAYSOCK`, and the store gains nothing.

**Verify.**

```bash
go test ./internal/tools/native -run 'TestCapture' -count=1
go test ./internal/tools/native -count=1
```

---

## Task 5: browser viewport, settle and clip

**Goal.** A page can be resized in place and screenshotted after fonts settle,
optionally clipped to one element — without losing the loaded document.

**Files.**
- `internal/tools/desktop/browser/backend.go`
- `internal/tools/desktop/browser/standalone.go`
- `internal/tools/desktop/browser/attach.go`
- `internal/tools/desktop/browser/fake.go`
- `internal/tools/desktop/browser/session.go`
- `internal/tools/desktop/browser/session_test.go` (extend)
- `internal/tools/desktop/browser/viewport_test.go` (new)
- `internal/tools/desktop/browser/standalone_test.go` (extend)
- `internal/tools/desktop/tools.go` (one line — keeps the tree compiling)

**Steps.**

1. `backend.go`: the Contracts types verbatim, replacing `ScreenshotOpts`.

2. `standalone.go`:
   - `NewPage(ctx, opts PageOptions)`: build `playwright.BrowserNewContextOptions`
     with `Viewport: &playwright.ViewportSize{Width: float64(w), Height:
     float64(h)}` and `DeviceScaleFactor: playwright.Float(dsf)` when
     `opts.Viewport != nil` and `w > 0 && h > 0`; otherwise today's empty
     options. Record the applied viewport on `standalonePage`.
   - `SetViewport(ctx, v)`: `p.page.SetViewportSize(playwright.PageSetViewportSizeOptions{
     Width: playwright.Int(v.Width), Height: playwright.Int(v.Height)})`. Device
     scale factor is a context option and is **not** applied here — return
     `ErrViewportNeedsRecreate` (a new sentinel in `backend.go`) when
     `v.DeviceScaleFactor` differs from the page's context DSF, so the session
     can recreate instead.
   - `Screenshot`: when `opts.SettleMS` is used, first
     `p.page.Evaluate("() => document.fonts.ready", nil)` (ignore the error —
     a page without fonts is not a failure), then
     `time.Sleep(time.Duration(settle) * time.Millisecond)` with
     `settle = opts.SettleMS` or `DefaultSettleMS`. Then build
     `playwright.PageScreenshotOptions` as today, adding
     `Quality: playwright.Int(opts.Quality)` when `> 0`, and, when
     `opts.Selector != ""`, resolve the element and set
     `Clip: &playwright.PageScreenshotClip{X: b.X, Y: b.Y, Width: b.Width,
     Height: b.Height}` with a `Scale: playwright.Float(1)`; force
     `FullPage: playwright.Bool(false)` in that case. A missing element is a
     clear error naming the selector.

3. `attach.go`: `NewPage(ctx, PageOptions)` keeps its CDP connection logic and
   applies `opts.Viewport` best-effort through the same `page.SetViewportSize`
   call. `SetViewport` mirrors the standalone implementation; DSF unsupported
   there too → `ErrViewportNeedsRecreate`.

4. `fake.go`: `NewPage(ctx, opts PageOptions)` records the options;
   `FakePage` gains `SetViewportCalls []Viewport` and
   `LastScreenshotOpts ScreenshotOpts`, plus `SettleCalls int` so tests assert
   the settle ran.

5. `session.go`:

```go
func (s *Session) Page(ctx context.Context, opts PageOptions) (PageHandle, error)
func (s *Session) LastURL() string
```

   Behaviour: no page yet → `backend.NewPage(ctx, opts)`. A page exists and
   `opts.Viewport == nil` → return it. A page exists with a different
   `DeviceScaleFactor` than the page was created with → close and recreate with
   `opts`, then re-navigate to `lastURL` if it is non-empty. Otherwise call
   `SetViewport` and, on `ErrViewportNeedsRecreate`, recreate and re-navigate.
   `lastURL` is recorded by the session's own `Navigate(ctx, url string) error`,
   which calls the page's `Navigate` and stores the URL on success. Update
   `navigateTool` to call `sess.Navigate` instead of `page.Navigate`, so every
   navigation — including the one inside `browser.click`/`browser.submit`'s
   follow-ups that re-read `page.URL` — leaves `lastURL` truthful.

6. `internal/tools/desktop/tools.go`: `withPage`'s `sess.Page(ctx)` becomes
   `sess.Page(ctx, browser.PageOptions{})`.

7. Tests:
   - `viewport_test.go` (fake backend): `TestSetViewportInPlaceDoesNotRecreate`
     (width/height change → one `NewPage`, one `SetViewport`),
     `TestDeviceScaleFactorRecreatesAndRenavigates` (two `NewPage` calls, second
     `Navigate` to `lastURL`), `TestSetViewportErrorFallsBackToRecreate`.
   - `session_test.go`: `TestPageReturnsSamePageWithoutOptions`,
     `TestNavigateRecordsLastURL`.
   - `standalone_test.go` (behind `testing.Short()` +
     `skipWithoutDriver`): `TestScreenshotSettlesAfterFonts` renders an
     `httptest` page with a delayed webfont and asserts glyph pixels;
     `TestScreenshotClipsSelector` asserts the PNG dimensions equal the
     element's box, not the viewport's.

**Verify.**

```bash
go test ./internal/tools/desktop/... -count=1
go vet ./internal/tools/desktop/...
```

Because `PageOptions`/`SetViewport` change an interface, `go build ./...` must
pass with every implementation updated — the compiler enumerates them
(`standalonePage`, the CDP page in `attach.go`, `FakePage`).

---

## Task 6: workspace `file:` URLs for `browser.navigate`

**Goal.** `browser.navigate` can open a workspace HTML file and still refuses
everything outside the tree.

**Files.**
- `internal/tools/desktop/url_filter.go`
- `internal/tools/desktop/url_filter_test.go`
- `internal/tools/desktop/types.go`
- `internal/tools/desktop/tools.go` (`navigateTool` call site)
- `internal/app/app.go` (`desktopOpts.WorkspaceRoot`)

**Steps.**

1. `url_filter.go`: signature gains `workspaceRoot string`. Restructure the top
   so `file:` is handled before the host check (today `parsed.Host == ""`
   rejects it at line 11 despite `file` being in the scheme switch):

```go
func urlAllowed(rawURL string, allowlist, denylist []string, workspaceRoot string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("navigation blocked: invalid URL %q", rawURL)
	}
	switch parsed.Scheme {
	case "file":
		return fileAllowed(parsed, denylist, workspaceRoot)
	case "http", "https":
	default:
		return fmt.Errorf("navigation blocked: scheme %q not allowed for %q", parsed.Scheme, rawURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("navigation blocked: invalid URL %q", rawURL)
	}
	// … existing denylist/allowlist body unchanged …
}

// fileAllowed confines file: navigation to the workspace. The allowlist does
// not apply: it names network destinations, and a configured allowlist must
// not become a second way to deny local rendering.
func fileAllowed(u *url.URL, denylist []string, workspaceRoot string) error {
	if u.Host != "" {
		return fmt.Errorf("navigation blocked: remote file URL %q", u.String())
	}
	if workspaceRoot == "" {
		return fmt.Errorf("navigation blocked: no workspace root for %q", u.String())
	}
	path := filepath.FromSlash(u.Path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("navigation blocked: %q does not resolve: %w", u.String(), err)
	}
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return fmt.Errorf("navigation blocked: workspace root %q does not resolve: %w", workspaceRoot, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("navigation blocked: %q is outside the workspace", u.String())
	}
	for _, entry := range denylist {
		if permissions.Matches(entry, resolved) {
			return fmt.Errorf("navigation blocked by policy (denylist): %s", u.String())
		}
	}
	return nil
}
```

Add `path/filepath` and `marshal/internal/permissions` to the imports (no cycle:
`permissions` does not import `desktop`).

2. `types.go`: `Options` gains `WorkspaceRoot string` and
   `ImageStore *imagepayload.Store` (the second is used in Task 7; add both now
   so `types.go` changes once). Store both on `toolSet` in `RegisterAll`.

3. `tools.go`: `urlAllowed(args.URL, ts.cfg.URLAllowlist, ts.cfg.URLDenylist,
   ts.WorkspaceRoot)`.

4. `app.go`: in the `desktopOpts` literal (`app.go:1539`) add
   `WorkspaceRoot: state.WorkingDir` and `ImageStore: imageStore` (Task 7 needs
   the store; `imageStore` is the images plan's variable, already in scope on
   this path).

5. `url_filter_test.go`: add the workspace root argument to every existing call
   (`""` where the test is about host policy, which keeps those cases' meaning),
   then add `TestURLAllowedFileURL`:
   - a file inside the root → allowed;
   - `../escape.html` → blocked;
   - a symlink inside the root pointing outside → blocked;
   - `file://host/path` → blocked;
   - with `allowlist = ["example.com"]`, a workspace file → allowed;
   - denylist entry `scratch/**` … note the pattern is matched against the
     resolved absolute path, so use an entry equal to the resolved path.

**Verify.**

```bash
go test ./internal/tools/desktop -run 'TestURL' -count=1
go test ./internal/tools/desktop -count=1
```

---

## Task 7: `browser.screenshot` returns the image

**Goal.** A screenshot's bytes reach the model as an `ImageRef`, with viewport,
selector, settle and label controls.

**Files.**
- `internal/tools/desktop/tools.go` (`screenshotTool` at `tools.go:310`)
- `internal/tools/desktop/tools_test.go`
- `internal/tools/desktop/schemas_test.go` (no change expected — it asserts
  `additionalProperties: false` generically; confirm it passes)

**Steps.**

1. Replace the tool's `Description` with: "Capture a screenshot of the current
   page and return the image so you can look at it. Optional selector clips to
   one element; viewport resizes the page first." Delete the stale "In a future
   vision milestone, returns image bytes" sentence.

2. Schema:

```json
{"type":"object","properties":{
  "full_page":{"type":"boolean","description":"Capture full scrollable page (default false; ignored when selector is set)"},
  "selector":{"type":"string","description":"CSS selector to clip the capture to"},
  "viewport":{"type":"object","properties":{"width":{"type":"integer","minimum":1},"height":{"type":"integer","minimum":1}},"required":["width","height"],"additionalProperties":false},
  "device_scale_factor":{"type":"number","exclusiveMinimum":0},
  "settle_ms":{"type":"integer","minimum":0},
  "format":{"type":"string","enum":["png","jpeg"]},
  "quality":{"type":"integer","minimum":1,"maximum":100},
  "label":{"type":"string"}
},"additionalProperties":false}
```

3. Handler:
   - decode the args; `format := args.Format`, falling back to
     `ts.cfg.ScreenshotFormat` when empty;
   - `pageOpts := browser.PageOptions{}`; when `args.Viewport.Width > 0` set
     `pageOpts.Viewport = &browser.Viewport{Width, Height, DeviceScaleFactor: args.DeviceScaleFactor}`;
     call `sess.Page(ctx, pageOpts)` through `withPage` — extend `withPage` to
     take `browser.PageOptions` so the screenshot path can pass a viewport and
     the other tools pass the zero value;
   - `png, err := page.Screenshot(ctx, browser.ScreenshotOpts{FullPage: args.FullPage, Format: format, Quality: args.Quality, Selector: args.Selector, SettleMS: args.SettleMS})`;
   - when `ts.ImageStore == nil`, keep today's metadata-only result and append
     ` (images disabled)` to the Summary, returning `Content` with `url`,
     `format`, `full_page`, `selector`;
   - otherwise `ref, err := ts.ImageStore.Put(ctx, bytes.NewReader(png), imagepayload.Options{Format: format, Quality: args.Quality, Label: args.Label})`,
     with `label` defaulting to `"screenshot of <current url>"`, and return
     `Images: []schema.ImageRef{ref}` plus a Summary naming the label,
     dimensions and whether the viewport was applied in place or recreated.

4. `tools_test.go`:
   - rename/keep `TestBrowserScreenshotReturnsMetadata` as the store-absent case
     with its Summary asserting `(images disabled)`;
   - `TestBrowserScreenshotReturnsImages` (store wired, temp store) — one ref,
     Summary has the label and dimensions;
   - `TestBrowserScreenshotClipsSelectorAndSettles` — asserts the fake page
     received `Selector` and `SettleMS` unchanged;
   - `TestBrowserScreenshotViewportReachesPage` — asserts
     `FakeBackend.NewPage` options or `SetViewportCalls` carry 390×844.

**Verify.**

```bash
go test ./internal/tools/desktop -run 'TestBrowserScreenshot' -count=1
go test ./internal/tools/desktop -count=1
```

---

## Task 8: session permission grants and policy precedence

**Goal.** An approval answer of "session" for `image.capture active_window`
grants exactly that for the rest of the session, and a config `deny` still wins.

**Files.**
- `internal/app/session/session.go`
- `internal/app/session/authority.go`
- `internal/app/session/access_grant_test.go` (extend)
- `internal/permissions/rules.go` (export `PermissionMatches`)
- `internal/tools/policy/policy.go`
- `internal/tools/policy/evaluation.go`
- `internal/tools/policy/evaluation_test.go` (extend)
- `internal/agent/execute.go:149` (destructuring)
- `internal/tools/native/command.go:202` (destructuring)
- Any test that passes a `SetConfigSource` func literal (fix the arity)

**Steps.**

1. `internal/app/session/session.go`: add the field
   `sessionPermissionRules []PermissionGrant` beside `sessionRules`
   (`session.go:303`), the `PermissionGrant` type, and the two methods from
   Contracts, modelled on `AddSessionRule` (`session.go:3936`) — same
   `accessMu` → `mu` → `access.Revision++` → `signalInteractionLocked()`
   sequence.

2. `authority.go`: `PolicySnapshot` (`authority.go:64`) returns a third copy:

```go
	grants := append([]PermissionGrant(nil), owner.sessionPermissionRules...)
	return cfg, rules, grants, owner.access.Revision
```

3. `internal/permissions/rules.go`: add

```go
// PermissionMatches reports whether a rule's permission name governs a tool's
// permission name, using the same relation permissionMatches implements.
func PermissionMatches(rulePermission, toolPermission string) bool {
	return permissionMatches(rulePermission, toolPermission)
}
```

4. `internal/tools/policy/policy.go`:
   - `type ConfigSource func() (config.Config, []string, []session.PermissionGrant, uint64)`;
     `SetConfigSource` takes it.
   - `PolicyEngine` gains `sessionPerms []session.PermissionGrant` and
     `SetSessionPermissions`.
   - `evaluateSubjects` takes `grants` and implements the precedence table:

```go
func evaluateSubjects(rules []permissions.Rule, grants []session.PermissionGrant, permissionName string, subjects []string) (Decision, bool) {
	var matched, allow, confirm bool
	for _, subject := range subjects {
		action, found := permissions.Evaluate(rules, permissionName, subject)
		granted := grantAllows(grants, permissionName, subject)
		switch {
		case found && action == permissions.ActionDeny:
			return DecisionDeny, true
		case found && action == permissions.ActionAllow:
			matched, allow = true, true
		case found && action == permissions.ActionAsk && granted:
			matched, allow = true, true
		case found && action == permissions.ActionAsk:
			matched, confirm = true, true
		case granted:
			matched, allow = true, true
		}
	}
	if !matched {
		return "", false
	}
	if confirm && !allow {
		return DecisionConfirm, true
	}
	if allow {
		return DecisionAllow, true
	}
	return DecisionConfirm, true
}

func grantAllows(grants []session.PermissionGrant, permissionName, subject string) bool {
	for _, g := range grants {
		if permissions.PermissionMatches(g.Permission, permissionName) && permissions.Matches(g.Pattern, subject) {
			return true
		}
	}
	return false
}
```

   With `grants` empty this reproduces today's behaviour exactly: allow → allow,
   ask → confirm, no match → false.

5. `evaluation.go`:
   - `snapshot()` returns four values; the fallback path returns
     `pe.sessionPerms` when there is no config source.
   - `EvaluateInput` passes grants to `evaluateNonShell(in, cfg, grants)` and
     `evaluateShellInput(in, cfg, sessionRules, grants, rules)`.
   - Both call sites of `evaluateSubjects` (`evaluation.go:123` and
     `evaluation.go:277`) pass their grants argument. The shell path's grants
     never match a command subject, but passing them keeps one rule.

6. `internal/agent/execute.go:149` and `internal/tools/native/command.go:202`:
   `cfg, _, _ :=` becomes `cfg, _, _, _ :=`.

7. Tests:
   - `evaluation_test.go`:
     - `TestGrantLiftsConfiguredAsk` — config ask on `image.capture` + grant
       `{image.capture, active_window}` → `DecisionAllow` for
       `active_window`, `DecisionConfirm` for `screen`.
     - `TestConfigDenyBeatsGrant` — config deny on `screen` + grant on screen →
       `DecisionDeny`.
     - `TestGrantAloneAllows` — no config match + grant → allow.
     - `TestNoGrantKeepsConfirm` — config ask, no grant → confirm.
     - `TestDefaultCaptureRuleConfirmsBothTargets` — `config.Default()`'s rule
       with no grants yields confirm for `active_window` and `screen`.
   - `access_grant_test.go`: `TestAddSessionPermissionRuleBumpsRevision` (a
     snapshot taken before the grant has a lower revision, so an
     `Authorizer.Evaluate` with the old revision is `ErrStaleAdmission`/stale),
     `TestSessionPermissionRulesReturnsCopy`.

**Verify.**

```bash
go test ./internal/tools/policy -count=1
go test ./internal/app/session -run 'TestAddSessionPermissionRule|TestSessionPermissionRules' -count=1
go build ./... && go vet ./internal/tools/policy ./internal/app/session ./internal/agent ./internal/tools/native
```

---

## Task 9: the approval answer, the sheet and telemetry

**Goal.** Choosing "session" on a capture prompt records the grant; the sheet
and telemetry show it.

**Files.**
- `internal/acp/service/answers.go`
- `internal/acp/service/sheet.go`
- `internal/acpwire/sheet.go`
- `internal/acp/turn.go`
- `internal/acp/service/*_test.go` (extend)
- `internal/viewmodel` (only if `SheetToolsOf`-style helpers are the pattern for
  `Permissions`; otherwise fill in `activityOf` directly)

**Steps.**

1. `answers.go`: `sessionRule` (`answers.go:108`):

```go
func (s *Session) sessionRule(call *session.PendingToolCall) func() error {
	return func() error {
		if call.Command == "" {
			s.state.AddSessionPermissionRule(session.PermissionGrant{
				Permission: permissions.PermissionForTool(call.Name),
				Pattern:    permissions.PatternForApproval(call),
			})
			return nil
		}
		s.state.AddSessionRule(call.Command)
		return nil
	}
}
```

2. `answers.go`: `alwaysRule`'s save-failure fallback (`answers.go:111`) —
   replace `s.state.AddSessionRule(call.Command)` with the same command-less
   branch as above, so an "always" answer on a command-less tool that cannot
   write config still grants for the session instead of appending `""` to the
   shell rule list.

3. `internal/permissions/pattern.go`: add a case to `PatternForApproval`
   (`pattern.go:18`), before the final `return "*"`:

```go
	if tc.Name == "image.capture" {
		var m map[string]any
		if err := json.Unmarshal([]byte(tc.Args), &m); err != nil {
			return "active_window"
		}
		if target, ok := m["target"].(string); ok && target != "" {
			return target
		}
		return "active_window"
	}
```

and the same target extraction in `internal/tools/policy/policy.go`'s
`subjectsForTool` (`policy.go:320`), before `default:`:

```go
	case "image.capture":
		target := "active_window"
		if m, ok := args["target"].(string); ok && m != "" {
			target = m
		}
		return []string{target}
```

4. `internal/acpwire/sheet.go`: `SheetPermissionRule` from Contracts and
   `Permissions` on `SessionSheet` (`sheet.go:20`, after `Rules`).

5. `internal/acp/service/sheet.go`: in `activityOf` (`sheet.go:211`) populate it
   from `st.SessionPermissionRules()`:

```go
	perms := st.SessionPermissionRules()
	out := make([]acpwire.SheetPermissionRule, 0, len(perms))
	for _, g := range perms {
		out = append(out, acpwire.SheetPermissionRule{Permission: g.Permission, Pattern: g.Pattern, Scope: "session"})
	}
	// … set Permissions: nilIfEmpty(out) on the returned SessionSheet
```

6. `internal/acp/turn.go`: `buildTelemetry` (`turn.go:1611`) adds
   `"permissionRules"` beside `"rules"` (`turn.go:1615`), always present as a
   non-nil slice, shaped `{"permission":…,"pattern":…,"scope":"session"}`.

7. Tests (`internal/acp/service`): `TestSessionAnswerGrantsCaptureForSession`
   (an `ApprovalSession` answer on a command-less `image.capture` call adds a
   grant with pattern `active_window`, and a shell call still adds the command
   rule), `TestAlwaysAnswerFallbackGrantsSessionPermission` (config dir
   unwritable), and a sheet test asserting `Permissions` is populated and
   `Rules` is unchanged.

**Verify.**

```bash
go test ./internal/acp/... -count=1
go vet ./internal/acp/... ./internal/acpwire/...
```

---

## Task 10: docs and full verification

**Goal.** The repository's own maps match the new package and tools.

**Files.**
- `AGENTS.md`
- `docs/ARCHITECTURE.md`
- `docs/tools/README.md`
- `docs/vision/design.md`

**Steps.**

1. `AGENTS.md`, "Where things live" → Tools & safety block: add a line for
   `internal/capture/` ("display detection and the grim/screencapture backends
   behind `image.capture`"), and in the same block note that the image producers
   are `image.view`, `image.capture` and `browser.screenshot`.
2. `docs/ARCHITECTURE.md`: add an `internal/capture/` row to the layer table in
   the Runtime & orchestration or Tools & safety group, matching the table's
   existing description style.
3. `docs/tools/README.md`: extend the `browser.*` gating paragraph (line 48) to
   mention `browser.screenshot` returning images when `[images] enabled`, and
   add a `[capture]` subsection: the table, the two targets, the default ask
   rule, and the sentence "approving for the session covers that target for the
   rest of the session; config `deny` still wins".
4. `docs/vision/design.md`: add a link to
   `docs/vision/capture-and-render-design.md` wherever it lists follow-up work,
   without restating the design.

**Verify — full project.**

```bash
CGO_ENABLED=1 go build ./...
go vet ./...
gofmt -l .          # must print nothing
scripts/test.sh quick
scripts/test.sh --base origin/main
go test ./internal/capture/... ./internal/tools/desktop/... -count=1
go test ./internal/tools/policy/... ./internal/app/session/... ./internal/acp/service/... -count=1
```

Expected: build and vet clean, `gofmt -l` empty, the affected-package run green
(real-browser cases skipping on a machine without the Playwright driver, as they
already do).

**Manual checklist (not CI-runnable).** On sway: active-window capture of a
tiled window and of a floating window over another, then whole screen. On
Hyprland: the same two. On macOS: window and screen, then revoke Screen
Recording and confirm `ErrPermission` names the setting. Start the daemon from a
shell outside the graphical session and confirm `ErrNoBackend` names
`SWAYSOCK` / `HYPRLAND_INSTANCE_SIGNATURE`.

---

## Integration notes

- **Ordering.** Tasks 1–2 are independent of the images work. Tasks 4, 7 and 9
  require the images plan. Tasks 3, 5 and 6 touch disjoint files and may run
  concurrently; everything else is sequential, and every task must leave
  `go build ./...` green (Task 5's one-line `withPage` change exists for that
  reason).
- **The one risk to verify early.** Task 5's `SetViewport`-in-place is
  Playwright behaviour, not Marshal's: if `page.SetViewportSize` refuses for a
  page created inside a context on the installed driver, the
  `ErrViewportNeedsRecreate` → recreate-and-re-navigate path is what keeps the
  render loop working. Run the real-browser test on a machine with the driver
  immediately after Task 5, not at the end of the plan.
- **Commit per task**, message `<spec-slug>: task N — <task title>`, after that
  task's Verify commands pass.
