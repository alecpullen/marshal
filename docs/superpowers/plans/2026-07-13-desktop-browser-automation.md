# Desktop Browser Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Cowork-style browser automation tool set to Marshal, letting the agent drive a visible Chromium via Playwright-go (standalone or attach-to-running modes), reusing the existing tool registry, approval, and risk system.

**Architecture:** A new `internal/tools/desktop` package registers six `browser.*` tools through the existing `registry.Registry`. A `BrowserBackend` interface (two impls: standalone Playwright launch, CDP attach) is the seam for both the future vision milestone and OS-level input. Config lives under a new `[desktop]` block mirroring `[web]`. Lifecycle is tied to `session.State` via the `Runtime.closeFns` cleanup hook.

**Tech Stack:** Go, `github.com/playwright-community/playwright-go`, existing `marshal/internal/tools/registry`, `marshal/internal/tools/policy`, `marshal/internal/app/config`.

## Global Constraints

- Pure Go dependency only: `github.com/playwright-community/playwright-go` (no CGO — must not affect the existing CGO/tree-sitter build).
- Tool risk levels reuse existing `registry.RiskLevel` values only — no new tiers.
- All browser tools are gated by `config.Desktop.Enabled` (default `false`).
- `headless` defaults to `false` (visible browser, Cowork-style) but is toggleable in config.
- URL allow/deny enforcement happens at the tool layer, not the policy layer.
- Browser backend is created lazily on first tool call, not at startup.
- Existing tests in `internal/tools/native`, `internal/tools/registry`, `internal/tools/policy`, `internal/sandbox` must stay green — the new package is purely additive.
- No comments in code unless explicitly requested by the spec's design.

---

## File Structure

```
internal/tools/desktop/
  types.go              — Options, BrowserOptions, ScreenshotOpts, shared types
  tools.go              — RegisterAll + toolSet struct, holds browser session
  url_filter.go         — URL allow/deny list matching logic
  tools_test.go         — Tool registration, schema, risk, arg validation, URL filter tests (fake backend)
  url_filter_test.go    — URL allow/deny matching unit tests (no browser)
  browser/
    backend.go           — BrowserBackend + PageHandle interfaces
    standalone.go        — Playwright-go launch implementation
    standalone_test.go   — Integration test (skipped with testing.Short())
    attach.go            — CDP attach implementation
    attach_test.go       — Integration test (skipped with testing.Short())
    session.go           — desktopSession: lazy creation, page handle, lifecycle
    fake_backend_test.go — Fake BrowserBackend + PageHandle for tool unit tests
```

Modified files:
- `internal/app/config/config.go` — add `DesktopConfig` struct, `fileDesktop` TOML struct, field in `Config`, default in `Default()`, merge in `mergeFile`.
- `internal/app/config/save.go` — add desktop section save logic.
- `internal/app/config/save_test.go` — add desktop to `fullEditedConfig` and round-trip assertion.
- `internal/app/app.go` — call `desktop.RegisterAll` after `native.RegisterAll`, return desktop closer from `buildAgentRunner`.
- `internal/app/runtime.go` — add `DesktopCloser` field to `Runtime`, close it in `Close()`.
- `go.mod` / `go.sum` — add playwright-go dependency.
- `docs/09-configuration-examples.md` — add `[desktop]` config example.

---

### Task 1: Add playwright-go dependency

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: `github.com/playwright-community/playwright-go` available for import in later tasks.

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get github.com/playwright-community/playwright-go
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./...`
Expected: PASS (no compilation errors — the dependency is imported but not yet used)

- [ ] **Step 3: Install browser binaries (local dev only, not committed)**

Run: `go run github.com/playwright-community/playwright-go/cmd/playwright install chromium`

This downloads Chromium into `~/Library/Caches/ms-playwright/`. It is not part of the build — it's a one-time local setup step for integration tests and actual usage.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add playwright-go for browser automation"
```

---

### Task 2: Add DesktopConfig to config layer

**Files:**
- Modify: `internal/app/config/config.go` (Config struct field, DesktopConfig struct, fileDesktop struct, Default(), mergeFile)
- Modify: `internal/app/config/save.go` (save desktop section)
- Modify: `internal/app/config/save_test.go` (fullEditedConfig + round-trip assertion)
- Test: `internal/app/config/save_test.go`

**Interfaces:**
- Produces: `config.DesktopConfig` struct with fields `Enabled bool`, `Mode string`, `Headless bool`, `CDPURL string`, `URLAllowlist []string`, `URLDenylist []string`, `DefaultTimeout time.Duration`, `ScreenshotFormat string`. Available as `cfg.Desktop` on `config.Config`.

- [ ] **Step 1: Write the failing config test**

Add to `internal/app/config/save_test.go`. First, extend `fullEditedConfig()` to include a desktop block. Find the existing line (around line 302):

```go
	cfg.Web = WebConfig{Enabled: true, FetchTimeout: 45 * time.Second, SearchProvider: "searx", SearchURL: "http://localhost:8888", SearchKey: "sk-live-1234"}
```

Add immediately after it:

```go
	cfg.Desktop = DesktopConfig{Enabled: true, Mode: "attach", Headless: true, CDPURL: "http://localhost:9222", URLAllowlist: []string{"example.com"}, URLDenylist: []string{"evil.com/admin"}, DefaultTimeout: 60 * time.Second, ScreenshotFormat: "png"}
```

Then add an assertion in `TestSaveProjectConfigFullSurfaceRoundTrip` after the web assertion (around line 344):

```go
	if loaded.Desktop != cfg.Desktop {
		t.Errorf("desktop: got %+v want %+v", loaded.Desktop, cfg.Desktop)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/... -run TestSaveProjectConfigFullSurfaceRoundTrip -v`
Expected: FAIL — `cfg.Desktop` undefined, `loaded.Desktop` undefined.

- [ ] **Step 3: Add DesktopConfig struct to config.go**

In `internal/app/config/config.go`, add the struct after the `WebConfig` definition (search for the existing `type WebConfig struct` and add after it). First, find `WebConfig`:

```go
type WebConfig struct {
	Enabled        bool
	FetchTimeout   time.Duration
	SearchProvider string
	SearchURL      string
	SearchKey      string
}
```

Add immediately after the closing brace of `WebConfig`:

```go
type DesktopConfig struct {
	Enabled          bool
	Mode             string
	Headless         bool
	CDPURL           string
	URLAllowlist     []string
	URLDenylist      []string
	DefaultTimeout   time.Duration
	ScreenshotFormat string
}
```

- [ ] **Step 4: Add Desktop field to Config struct**

In `internal/app/config/config.go`, the `Config` struct has a `Web` field at line 26:

```go
	Web           WebConfig                             `toml:"web"`
```

Add immediately after it:

```go
	Desktop       DesktopConfig                        `toml:"desktop"`
```

- [ ] **Step 5: Add fileDesktop TOML struct**

In `internal/app/config/config.go`, after the `fileWeb` struct (around line 355):

```go
type fileWeb struct {
	Enabled        *bool   `toml:"enabled"`
	FetchTimeout   *string `toml:"fetch_timeout"`
	SearchProvider *string `toml:"search_provider"`
	SearchURL      *string `toml:"search_url"`
	SearchKey      *string `toml:"search_key"`
}
```

Add immediately after it:

```go
type fileDesktop struct {
	Enabled          *bool    `toml:"enabled"`
	Mode            *string  `toml:"mode"`
	Headless        *bool    `toml:"headless"`
	CDPURL           *string  `toml:"cdp_url"`
	URLAllowlist    []string `toml:"url_allowlist"`
	URLDenylist     []string `toml:"url_denylist"`
	DefaultTimeout  *string  `toml:"default_timeout"`
	ScreenshotFormat *string `toml:"screenshot_format"`
}
```

- [ ] **Step 6: Add Desktop field to fileConfig struct**

In `internal/app/config/config.go`, the `fileConfig` struct has (around line 427):

```go
	Web         *fileWeb         `toml:"web"`
```

Add immediately after it:

```go
	Desktop     *fileDesktop     `toml:"desktop"`
```

- [ ] **Step 7: Add default value to Default()**

In `internal/app/config/config.go`, in the `Default()` function, after the `Web` default (around line 527-530):

```go
		Web: WebConfig{
			Enabled:      false,
			FetchTimeout: 30 * time.Second,
		},
```

Add immediately after it:

```go
		Desktop: DesktopConfig{
			Enabled:          false,
			Mode:             "standalone",
			Headless:         false,
			DefaultTimeout:   30 * time.Second,
			ScreenshotFormat: "png",
		},
```

- [ ] **Step 8: Add merge logic in mergeFile**

In `internal/app/config/config.go`, after the `file.Web` merge block (around line 949-969, which ends with the closing `}` of `if file.Web != nil`):

```go
	if file.Web != nil {
		if file.Web.Enabled != nil {
			cfg.Web.Enabled = *file.Web.Enabled
		}
		if file.Web.FetchTimeout != nil && *file.Web.FetchTimeout != "" {
			d, err := time.ParseDuration(*file.Web.FetchTimeout)
			if err != nil {
				return fmt.Errorf("parse web.fetch_timeout: %w", err)
			}
			cfg.Web.FetchTimeout = d
		}
		if file.Web.SearchProvider != nil {
			cfg.Web.SearchProvider = *file.Web.SearchProvider
		}
		if file.Web.SearchURL != nil {
			cfg.Web.SearchURL = *file.Web.SearchURL
		}
		if file.Web.SearchKey != nil {
			cfg.Web.SearchKey = *file.Web.SearchKey
		}
	}
```

Add immediately after that block:

```go
	if file.Desktop != nil {
		if file.Desktop.Enabled != nil {
			cfg.Desktop.Enabled = *file.Desktop.Enabled
		}
		if file.Desktop.Mode != nil {
			cfg.Desktop.Mode = *file.Desktop.Mode
		}
		if file.Desktop.Headless != nil {
			cfg.Desktop.Headless = *file.Desktop.Headless
		}
		if file.Desktop.CDPURL != nil {
			cfg.Desktop.CDPURL = *file.Desktop.CDPURL
		}
		if file.Desktop.URLAllowlist != nil {
			cfg.Desktop.URLAllowlist = file.Desktop.URLAllowlist
		}
		if file.Desktop.URLDenylist != nil {
			cfg.Desktop.URLDenylist = file.Desktop.URLDenylist
		}
		if file.Desktop.DefaultTimeout != nil && *file.Desktop.DefaultTimeout != "" {
			d, err := time.ParseDuration(*file.Desktop.DefaultTimeout)
			if err != nil {
				return fmt.Errorf("parse desktop.default_timeout: %w", err)
			}
			cfg.Desktop.DefaultTimeout = d
		}
		if file.Desktop.ScreenshotFormat != nil {
			cfg.Desktop.ScreenshotFormat = *file.Desktop.ScreenshotFormat
		}
	}
```

- [ ] **Step 9: Add save logic to save.go**

In `internal/app/config/save.go`, after the `file.Web` save block (around line 116-124):

```go
	if file.Web != nil || cfg.Web != def.Web {
		file.Web = &fileWeb{
			Enabled:        ptr(cfg.Web.Enabled),
			FetchTimeout:   ptr(cfg.Web.FetchTimeout.String()),
			SearchProvider: ptr(cfg.Web.SearchProvider),
			SearchURL:      ptr(cfg.Web.SearchURL),
			SearchKey:      ptr(cfg.Web.SearchKey),
		}
	}
```

Add immediately after it:

```go
	if file.Desktop != nil || cfg.Desktop != def.Desktop {
		file.Desktop = &fileDesktop{
			Enabled:           ptr(cfg.Desktop.Enabled),
			Mode:              ptr(cfg.Desktop.Mode),
			Headless:          ptr(cfg.Desktop.Headless),
			CDPURL:            ptr(cfg.Desktop.CDPURL),
			URLAllowlist:      cfg.Desktop.URLAllowlist,
			URLDenylist:       cfg.Desktop.URLDenylist,
			DefaultTimeout:    ptr(cfg.Desktop.DefaultTimeout.String()),
			ScreenshotFormat:  ptr(cfg.Desktop.ScreenshotFormat),
		}
	}
```

- [ ] **Step 10: Run config tests to verify they pass**

Run: `go test ./internal/app/config/... -v`
Expected: PASS — all config tests including the full round-trip.

- [ ] **Step 11: Run vet and format**

Run: `gofmt -w internal/app/config/config.go internal/app/config/save.go internal/app/config/save_test.go && go vet ./internal/app/config/...`
Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add internal/app/config/config.go internal/app/config/save.go internal/app/config/save_test.go
git commit -m "feat(config): add [desktop] config block for browser automation"
```

---

### Task 3: Define BrowserBackend and PageHandle interfaces

**Files:**
- Create: `internal/tools/desktop/browser/backend.go`
- Test: `internal/tools/desktop/browser/fake_backend_test.go`

**Interfaces:**
- Produces: `BrowserBackend` interface (NewPage, Close, Mode) and `PageHandle` interface (Navigate, Title, URL, Text, HTML, ReadableText, Click, Fill, PressKey, Submit, WaitForSelector, WaitForLoadState, Screenshot, Close). Produces `ScreenshotOpts` struct. Produces `FakeBackend` and `FakePage` for use in tool unit tests.

- [ ] **Step 1: Write the fake backend test**

Create `internal/tools/desktop/browser/fake_backend_test.go`:

```go
package browser

import (
	"context"
	"errors"
	"sync"
	"time"
)

type FakePage struct {
	mu               sync.Mutex
	NavigatedTo      string
	ClickedSelectors []string
	FilledInputs     map[string]string
	SubmittedSel     string
	ScreenshotCalls  int
	TitleVal         string
	URLVal           string
	ReadableTextVal  string
	Closed           bool
	NavigateErr      error
}

func (p *FakePage) Navigate(ctx context.Context, url string) error {
	if p.NavigateErr != nil {
		return p.NavigateErr
	}
	p.mu.Lock()
	p.NavigatedTo = url
	p.mu.Unlock()
	return nil
}

func (p *FakePage) Title(ctx context.Context) (string, error) {
	return p.TitleVal, nil
}

func (p *FakePage) URL(ctx context.Context) (string, error) {
	return p.URLVal, nil
}

func (p *FakePage) Text(ctx context.Context, selector string) (string, error) {
	return p.ReadableTextVal, nil
}

func (p *FakePage) HTML(ctx context.Context, selector string) (string, error) {
	return "<html>" + selector + "</html>", nil
}

func (p *FakePage) ReadableText(ctx context.Context) (string, error) {
	return p.ReadableTextVal, nil
}

func (p *FakePage) Click(ctx context.Context, selector string) error {
	p.mu.Lock()
	p.ClickedSelectors = append(p.ClickedSelectors, selector)
	p.mu.Unlock()
	return nil
}

func (p *FakePage) Fill(ctx context.Context, selector, value string) error {
	p.mu.Lock()
	if p.FilledInputs == nil {
		p.FilledInputs = map[string]string{}
	}
	p.FilledInputs[selector] = value
	p.mu.Unlock()
	return nil
}

func (p *FakePage) PressKey(ctx context.Context, key string) error {
	return nil
}

func (p *FakePage) Submit(ctx context.Context, selector string) error {
	p.mu.Lock()
	p.SubmittedSel = selector
	p.mu.Unlock()
	return nil
}

func (p *FakePage) WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	return nil
}

func (p *FakePage) WaitForLoadState(ctx context.Context, state string) error {
	return nil
}

func (p *FakePage) Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error) {
	p.mu.Lock()
	p.ScreenshotCalls++
	p.mu.Unlock()
	return []byte("fake-screenshot-metadata"), nil
}

func (p *FakePage) Close() error {
	p.mu.Lock()
	p.Closed = true
	p.mu.Unlock()
	return nil
}

type FakeBackend struct {
	Page     *FakePage
	ModeVal  string
	CloseErr error
}

func (b *FakeBackend) NewPage(ctx context.Context) (PageHandle, error) {
	if b.Page == nil {
		b.Page = &FakePage{TitleVal: "Fake Title", URLVal: "https://example.com", ReadableTextVal: "fake page text"}
	}
	return b.Page, nil
}

func (b *FakeBackend) Close() error {
	return b.CloseErr
}

func (b *FakeBackend) Mode() string {
	if b.ModeVal != "" {
		return b.ModeVal
	}
	return "standalone"
}

func TestFakeBackendImplementsBrowserBackend(t *testing.T) {
	var _ BrowserBackend = (*FakeBackend)(nil)
	var _ PageHandle = (*FakePage)(nil)
}

func TestFakePageNavigate(t *testing.T) {
	p := &FakePage{}
	if err := p.Navigate(context.Background(), "https://test.com"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if p.NavigatedTo != "https://test.com" {
		t.Fatalf("navigated to %q, want https://test.com", p.NavigatedTo)
	}
}

func TestFakePageNavigateError(t *testing.T) {
	p := &FakePage{NavigateErr: errors.New("connection lost")}
	if err := p.Navigate(context.Background(), "https://test.com"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFakePageClickAndFill(t *testing.T) {
	p := &FakePage{}
	_ = p.Click(context.Background(), "#btn")
	_ = p.Fill(context.Background(), "#input", "hello")
	if len(p.ClickedSelectors) != 1 || p.ClickedSelectors[0] != "#btn" {
		t.Fatalf("clicked %v", p.ClickedSelectors)
	}
	if p.FilledInputs["#input"] != "hello" {
		t.Fatalf("filled %v", p.FilledInputs)
	}
}

func TestFakePageScreenshot(t *testing.T) {
	p := &FakePage{}
	data, err := p.Screenshot(context.Background(), ScreenshotOpts{Format: "png"})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if p.ScreenshotCalls != 1 {
		t.Fatalf("screenshot calls: %d", p.ScreenshotCalls)
	}
	if len(data) == 0 {
		t.Fatal("no screenshot data")
	}
}

func TestFakeBackendClose(t *testing.T) {
	b := &FakeBackend{CloseErr: errors.New("close failed")}
	if err := b.Close(); err == nil {
		t.Fatal("expected close error")
	}
}
```

Add the necessary import to the test file header (place at top of file with other imports):

```go
import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/desktop/browser/ -v`
Expected: FAIL — `BrowserBackend`, `PageHandle`, `ScreenshotOpts` undefined.

- [ ] **Step 3: Write backend.go**

Create `internal/tools/desktop/browser/backend.go`:

```go
package browser

import (
	"context"
	"time"
)

type BrowserBackend interface {
	NewPage(ctx context.Context) (PageHandle, error)
	Close() error
	Mode() string
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
	Format   string
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/desktop/browser/ -v`
Expected: PASS — all fake backend tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/desktop/browser/backend.go internal/tools/desktop/browser/fake_backend_test.go
git commit -m "feat(desktop): define BrowserBackend + PageHandle interfaces with fake for tests"
```

---

### Task 4: URL allow/deny filter

**Files:**
- Create: `internal/tools/desktop/url_filter.go`
- Test: `internal/tools/desktop/url_filter_test.go`

**Interfaces:**
- Consumes: `config.DesktopConfig` (URLAllowlist, URLDenylist fields from Task 2).
- Produces: `func urlAllowed(rawURL string, allowlist, denylist []string) error` — returns nil if allowed, error if blocked.

- [ ] **Step 1: Write the failing test**

Create `internal/tools/desktop/url_filter_test.go`:

```go
package desktop

import (
	"strings"
	"testing"
)

func TestURLAllowedEmptyLists(t *testing.T) {
	if err := urlAllowed("https://anything.com", nil, nil); err != nil {
		t.Fatalf("empty lists should allow: %v", err)
	}
}

func TestURLAllowedDenylistBlocks(t *testing.T) {
	err := urlAllowed("https://evil.com/admin", nil, []string{"evil.com"})
	if err == nil {
		t.Fatal("denylist should block evil.com")
	}
	if !strings.Contains(err.Error(), "evil.com") {
		t.Fatalf("error should mention url, got: %v", err)
	}
}

func TestURLAllowedDenylistPathPrefix(t *testing.T) {
	if err := urlAllowed("https://example.com/safe", nil, []string{"example.com/admin"}); err != nil {
		t.Fatalf("denylist prefix should not block /safe: %v", err)
	}
	if err := urlAllowed("https://example.com/admin/users", nil, []string{"example.com/admin"}); err == nil {
		t.Fatal("denylist prefix should block /admin/users")
	}
}

func TestURLAllowedAllowlistPermits(t *testing.T) {
	if err := urlAllowed("https://example.com/page", []string{"example.com"}, nil); err != nil {
		t.Fatalf("allowlist should permit example.com: %v", err)
	}
}

func TestURLAllowedAllowlistBlocksUnlisted(t *testing.T) {
	if err := urlAllowed("https://other.com", []string{"example.com"}, nil); err == nil {
		t.Fatal("allowlist should block unlisted host")
	}
}

func TestURLAllowedAllowlistPathPrefix(t *testing.T) {
	if err := urlAllowed("https://example.com/docs/page", []string{"example.com/docs"}, nil); err != nil {
		t.Fatalf("allowlist prefix should permit /docs/page: %v", err)
	}
	if err := urlAllowed("https://example.com/blog", []string{"example.com/docs"}, nil); err == nil {
		t.Fatal("allowlist prefix should block /blog")
	}
}

func TestURLAllowedDenylistWinsOverAllowlist(t *testing.T) {
	if err := urlAllowed("https://example.com/admin", []string{"example.com"}, []string{"example.com/admin"}); err == nil {
		t.Fatal("denylist should win over allowlist")
	}
}

func TestURLAllowedInvalidURL(t *testing.T) {
	if err := urlAllowed("not a url", nil, nil); err == nil {
		t.Fatal("invalid URL should be blocked")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/desktop/ -run TestURLAllowed -v`
Expected: FAIL — `urlAllowed` undefined.

- [ ] **Step 3: Write url_filter.go**

Create `internal/tools/desktop/url_filter.go`:

```go
package desktop

import (
	"fmt"
	"net/url"
	"strings"
)

func urlAllowed(rawURL string, allowlist, denylist []string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("navigation blocked: invalid URL %q", rawURL)
	}
	host := parsed.Hostname()
	path := parsed.Path

	for _, entry := range denylist {
		if matchHostPath(host, path, entry) {
			return fmt.Errorf("navigation blocked by policy (denylist): %s", rawURL)
		}
	}

	if len(allowlist) == 0 {
		return nil
	}

	for _, entry := range allowlist {
		if matchHostPath(host, path, entry) {
			return nil
		}
	}
	return fmt.Errorf("navigation blocked by policy (not in allowlist): %s", rawURL)
}

func matchHostPath(host, path, entry string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	parts := strings.SplitN(entry, "/", 2)
	entryHost := parts[0]
	if !hostMatches(host, entryHost) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	entryPath := "/" + strings.TrimPrefix(parts[1], "/")
	return strings.HasPrefix(path, entryPath)
}

func hostMatches(host, pattern string) bool {
	if pattern == host {
		return true
	}
	return strings.HasSuffix(host, "."+pattern)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/desktop/ -run TestURLAllowed -v`
Expected: PASS — all 8 URL filter tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/desktop/url_filter.go internal/tools/desktop/url_filter_test.go
git commit -m "feat(desktop): URL allow/deny list filter for navigation"
```

---

### Task 5: Desktop session manager (lazy creation + lifecycle)

**Files:**
- Create: `internal/tools/desktop/browser/session.go`
- Test: `internal/tools/desktop/browser/session_test.go`

**Interfaces:**
- Consumes: `BrowserBackend` interface (Task 3), `FakeBackend` (Task 3).
- Produces: `type Session struct` with methods `Page(ctx) (PageHandle, error)` (lazy creation, stale-handle recovery) and `Close() error`. Constructor `func NewSession(backend BrowserBackend) *Session`.

- [ ] **Step 1: Write the failing test**

Create `internal/tools/desktop/browser/session_test.go`:

```go
package browser

import (
	"context"
	"errors"
	"testing"
)

func TestSessionLazyCreation(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)

	if s.Page(context.Background()) == nil {
	}
	p, err := s.Page(context.Background())
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if p == nil {
		t.Fatal("page should be non-nil after first call")
	}

	p2, err := s.Page(context.Background())
	if err != nil {
		t.Fatalf("Page second call: %v", err)
	}
	if p != p2 {
		t.Fatal("session should return the same page handle on subsequent calls")
	}
}

func TestSessionClose(t *testing.T) {
	backend := &FakeBackend{CloseErr: errors.New("close failed")}
	s := NewSession(backend)
	_, _ = s.Page(context.Background())

	if err := s.Close(); err == nil {
		t.Fatal("expected close error from backend")
	}
}

func TestSessionCloseWithoutPage(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)
	if err := s.Close(); err != nil {
		t.Fatalf("close without page: %v", err)
	}
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)
	_ = s.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/desktop/browser/ -run TestSession -v`
Expected: FAIL — `NewSession`, `Session` undefined.

- [ ] **Step 3: Write session.go**

Create `internal/tools/desktop/browser/session.go`:

```go
package browser

import (
	"context"
	"sync"
)

type Session struct {
	backend BrowserBackend
	page    PageHandle
	mu      sync.Mutex
	closed  bool
}

func NewSession(backend BrowserBackend) *Session {
	return &Session{backend: backend}
}

func (s *Session) Page(ctx context.Context) (PageHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errSessionClosed
	}
	if s.page != nil {
		return s.page, nil
	}
	page, err := s.backend.NewPage(ctx)
	if err != nil {
		return nil, err
	}
	s.page = page
	return page, nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var pageErr, backendErr error
	if s.page != nil {
		pageErr = s.page.Close()
	}
	backendErr = s.backend.Close()
	if pageErr != nil {
		return pageErr
	}
	return backendErr
}

var errSessionClosed = errors.New("desktop session is closed")
```

Add the `errors` import to the import block:

```go
import (
	"context"
	"errors"
	"sync"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/desktop/browser/ -v`
Expected: PASS — all session and fake backend tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/desktop/browser/session.go internal/tools/desktop/browser/session_test.go
git commit -m "feat(desktop): lazy browser session with lifecycle management"
```

---

### Task 6: Tool registration and tool set

**Files:**
- Create: `internal/tools/desktop/types.go`
- Create: `internal/tools/desktop/tools.go`
- Test: `internal/tools/desktop/tools_test.go`

**Interfaces:**
- Consumes: `config.DesktopConfig` (Task 2), `BrowserBackend`/`PageHandle`/`Session` (Tasks 3, 5), `registry.Registry`/`registry.Tool`/`registry.ToolResult`/`registry.RiskLevel` (existing), `urlAllowed` (Task 4), `FakeBackend`/`FakePage` (Task 3).
- Produces: `type Options struct { Config config.DesktopConfig; BackendFactory func() (browser.BrowserBackend, error) }`, `func RegisterAll(reg *registry.Registry, opts Options) (func(), error)`. Registers six tools: `browser.navigate`, `browser.read`, `browser.click`, `browser.fill`, `browser.submit`, `browser.screenshot`. Returns a closer function that closes the browser session.

- [ ] **Step 1: Write the failing tool test**

Create `internal/tools/desktop/tools_test.go`:

```go
package desktop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/registry"
)

func newTestToolSet(t *testing.T) (*registry.Registry, *browser.FakeBackend, *browser.FakePage) {
	t.Helper()
	reg := registry.New()
	backend := &browser.FakeBackend{}
	opts := Options{
		Config: config.DesktopConfig{
			Enabled:          true,
			Mode:             "standalone",
			Headless:         false,
			DefaultTimeout:   30_000_000_000,
			ScreenshotFormat: "png",
		},
		BackendFactory: func() (browser.BrowserBackend, error) {
			return backend, nil
		},
	}
	if closer, err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	} else {
		_ = closer
	}
	page := &browser.FakePage{TitleVal: "Test", URLVal: "https://example.com", ReadableTextVal: "page content here"}
	backend.Page = page
	return reg, backend, page
}

func toolByName(t *testing.T, reg *registry.Registry, name string) registry.Tool {
	t.Helper()
	tools := reg.List()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not registered", name)
	return registry.Tool{}
}

func TestRegisterAllRegistersSixTools(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	expected := []string{
		"browser.navigate", "browser.read", "browser.click",
		"browser.fill", "browser.submit", "browser.screenshot",
	}
	tools := reg.List()
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, exp := range expected {
		if !names[exp] {
			t.Errorf("tool %q not registered", exp)
		}
	}
}

func TestBrowserNavigateRiskNetwork(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.navigate")
	if tool.Risk != registry.RiskNetwork {
		t.Errorf("browser.navigate risk = %s, want network", tool.Risk)
	}
}

func TestBrowserReadRiskReadOnly(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.read")
	if tool.Risk != registry.RiskReadOnly {
		t.Errorf("browser.read risk = %s, want read_only", tool.Risk)
	}
}

func TestBrowserScreenshotRiskReadOnly(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.screenshot")
	if tool.Risk != registry.RiskReadOnly {
		t.Errorf("browser.screenshot risk = %s, want read_only", tool.Risk)
	}
}

func TestBrowserNavigateExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.navigate")
	args, _ := json.Marshal(map[string]any{"url": "https://example.com"})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if page.NavigatedTo != "https://example.com" {
		t.Errorf("navigated to %q, want https://example.com", page.NavigatedTo)
	}
	if !strings.Contains(res.Summary, "https://example.com") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestBrowserNavigateBlockedByDenylist(t *testing.T) {
	reg := registry.New()
	backend := &browser.FakeBackend{}
	opts := Options{
		Config: config.DesktopConfig{
			Enabled:      true,
			URLDenylist:  []string{"blocked.com"},
			DefaultTimeout: 30_000_000_000,
		},
		BackendFactory: func() (browser.BrowserBackend, error) {
			return backend, nil
		},
	}
	if closer, err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	} else {
		_ = closer
	}
	tool := toolByName(t, reg, "browser.navigate")
	args, _ := json.Marshal(map[string]any{"url": "https://blocked.com"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected denylist block")
	}
	if !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("error should mention policy: %v", err)
	}
}

func TestBrowserReadReturnsContent(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.read")
	args, _ := json.Marshal(map[string]any{})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, "page content here") {
		t.Errorf("content = %q", res.Content)
	}
}

func TestBrowserClickExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.click")
	args, _ := json.Marshal(map[string]any{"selector": "#submit-btn"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(page.ClickedSelectors) != 1 || page.ClickedSelectors[0] != "#submit-btn" {
		t.Errorf("clicked %v, want [#submit-btn]", page.ClickedSelectors)
	}
}

func TestBrowserFillExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.fill")
	args, _ := json.Marshal(map[string]any{"selector": "#name", "value": "hello world"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if page.FilledInputs["#name"] != "hello world" {
		t.Errorf("filled %v", page.FilledInputs)
	}
}

func TestBrowserSubmitExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.submit")
	args, _ := json.Marshal(map[string]any{"selector": "form#login"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if page.SubmittedSel != "form#login" {
		t.Errorf("submitted %q, want form#login", page.SubmittedSel)
	}
}

func TestBrowserScreenshotReturnsMetadata(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.screenshot")
	args, _ := json.Marshal(map[string]any{})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Summary, "screenshot") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestBrowserFillRequiresSelector(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.fill")
	args, _ := json.Marshal(map[string]any{"value": "no selector"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for missing selector")
	}
}

func TestBrowserNavigateRequiresURL(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.navigate")
	args, _ := json.Marshal(map[string]any{})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for missing url")
	}
}

func TestRegisterAllDisabledDoesNotRegister(t *testing.T) {
	reg := registry.New()
	opts := Options{
		Config: config.DesktopConfig{Enabled: false},
		BackendFactory: func() (browser.BrowserBackend, error) {
			return &browser.FakeBackend{}, nil
		},
	}
	if closer, err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	} else {
		_ = closer
	}
	if len(reg.List()) != 0 {
		t.Fatalf("expected 0 tools when disabled, got %d", len(reg.List()))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/desktop/ -v`
Expected: FAIL — `Options`, `RegisterAll` undefined.

- [ ] **Step 3: Write types.go**

Create `internal/tools/desktop/types.go`:

```go
package desktop

import (
	"marshal/internal/app/config"
	"marshal/internal/tools/desktop/browser"
)

type Options struct {
	Config         config.DesktopConfig
	BackendFactory func() (browser.BrowserBackend, error)
}
```

- [ ] **Step 4: Write tools.go**

Create `internal/tools/desktop/tools.go`:

```go
package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/registry"
)

type toolSet struct {
	cfg            config.DesktopConfig
	backendFactory func() (browser.BrowserBackend, error)
	session        *browser.Session
	sessionMu      sync.Mutex
}

func (ts *toolSet) getSession(ctx context.Context) (*browser.Session, error) {
	ts.sessionMu.Lock()
	defer ts.sessionMu.Unlock()
	if ts.session != nil {
		return ts.session, nil
	}
	backend, err := ts.backendFactory()
	if err != nil {
		return nil, fmt.Errorf("create browser backend: %w", err)
	}
	ts.session = browser.NewSession(backend)
	return ts.session, nil
}

func RegisterAll(reg *registry.Registry, opts Options) (func(), error) {
	if !opts.Config.Enabled {
		return nil, nil
	}
	ts := &toolSet{
		cfg:            opts.Config,
		backendFactory: opts.BackendFactory,
	}
	tools := []registry.Tool{
		ts.navigateTool(),
		ts.readTool(),
		ts.clickTool(),
		ts.fillTool(),
		ts.submitTool(),
		ts.screenshotTool(),
	}
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return nil, fmt.Errorf("register %s: %w", tool.Name, err)
		}
	}
	closer := func() {
		ts.sessionMu.Lock()
		sess := ts.session
		ts.session = nil
		ts.sessionMu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
	}
	return closer, nil
}

func (ts *toolSet) navigateTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.navigate",
		Description:  "Navigate the browser to a URL. Requires approval (network risk). Subject to URL allow/deny list policy.",
		Schema:       json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The URL to navigate to"}},"required":["url"]}`),
		Risk:         registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			URL string `json:"url"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		if args.URL == "" {
			return registry.ToolResult{}, fmt.Errorf("url is required")
		}
		if err := urlAllowed(args.URL, ts.cfg.URLAllowlist, ts.cfg.URLDenylist); err != nil {
			return registry.ToolResult{}, err
		}
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
	}
	return tool
}

func (ts *toolSet) readTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.read",
		Description:  "Read simplified readable text from the current page. Optional selector targets a specific element.",
		Schema:       json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","description":"CSS selector for a specific element. Omit for full page."}}}`),
		Risk:         registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		sess, err := ts.getSession(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		var text string
		if args.Selector != "" {
			text, err = page.Text(ctx, args.Selector)
		} else {
			text, err = page.ReadableText(ctx)
		}
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("read: %w", err)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Read page: %d chars", len(text)),
			Content: text,
		}, nil
	}
	return tool
}

func (ts *toolSet) clickTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.click",
		Description:  "Click an element by CSS selector. Uses Playwright auto-waiting.",
		Schema:       json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","description":"CSS selector for the element to click"}},"required":["selector"]}`),
		Risk:         registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		if args.Selector == "" {
			return registry.ToolResult{}, fmt.Errorf("selector is required")
		}
		sess, err := ts.getSession(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if err := page.Click(ctx, args.Selector); err != nil {
			return registry.ToolResult{}, fmt.Errorf("click %s: %w", args.Selector, err)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Clicked %s", args.Selector),
		}, nil
	}
	return tool
}

func (ts *toolSet) fillTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.fill",
		Description:  "Type text into an input or textarea element. Clears existing content by default.",
		Schema:       json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","description":"CSS selector for the input element"},"value":{"type":"string","description":"Text to type"},"clear":{"type":"boolean","description":"Clear field before filling (default true)"}},"required":["selector","value"]}`),
		Risk:         registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
			Value    string `json:"value"`
			Clear    *bool  `json:"clear"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		if args.Selector == "" {
			return registry.ToolResult{}, fmt.Errorf("selector is required")
		}
		if args.Value == "" && args.Clear == nil {
			return registry.ToolResult{}, fmt.Errorf("value is required")
		}
		sess, err := ts.getSession(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if err := page.Fill(ctx, args.Selector, args.Value); err != nil {
			return registry.ToolResult{}, fmt.Errorf("fill %s: %w", args.Selector, err)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Filled %s with %q", args.Selector, args.Value),
		}, nil
	}
	return tool
}

func (ts *toolSet) submitTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.submit",
		Description:  "Submit a form by clicking a submit button selector, or pressing Enter on the focused element if no selector given.",
		Schema:       json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","description":"CSS selector for submit button. Omit to press Enter on focused element."}}}`),
		Risk:         registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		sess, err := ts.getSession(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Selector != "" {
			if err := page.Submit(ctx, args.Selector); err != nil {
				return registry.ToolResult{}, fmt.Errorf("submit %s: %w", args.Selector, err)
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("Submitted %s", args.Selector),
			}, nil
		}
		if err := page.PressKey(ctx, "Enter"); err != nil {
			return registry.ToolResult{}, fmt.Errorf("press Enter: %w", err)
		}
		return registry.ToolResult{
			Summary: "Submitted (pressed Enter)",
		}, nil
	}
	return tool
}

func (ts *toolSet) screenshotTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.screenshot",
		Description:  "Capture a screenshot of the current page. Returns metadata (dimensions, timestamp). In a future vision milestone, returns image bytes.",
		Schema:       json.RawMessage(`{"type":"object","properties":{"full_page":{"type":"boolean","description":"Capture full scrollable page (default false)"}}}`),
		Risk:         registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			FullPage bool `json:"full_page"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		sess, err := ts.getSession(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		page, err := sess.Page(ctx)
		if err != nil {
			return registry.ToolResult{}, err
		}
		_, err = page.Screenshot(ctx, browser.ScreenshotOpts{FullPage: args.FullPage, Format: ts.cfg.ScreenshotFormat})
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("screenshot: %w", err)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Screenshot captured (full_page=%v, format=%s)", args.FullPage, ts.cfg.ScreenshotFormat),
			Content: fmt.Sprintf(`{"full_page":%v,"format":%q}`, args.FullPage, ts.cfg.ScreenshotFormat),
		}, nil
	}
	return tool
}

func decodeArgs(tool registry.Tool, raw json.RawMessage, target any) error {
	if err := registry.ValidateArgs(tool, raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s arguments: %w", tool.Name, err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tools/desktop/ -v`
Expected: PASS — all tool tests pass.

- [ ] **Step 6: Run format and vet**

Run: `gofmt -w internal/tools/desktop/ && go vet ./internal/tools/desktop/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/tools/desktop/types.go internal/tools/desktop/tools.go internal/tools/desktop/tools_test.go
git commit -m "feat(desktop): register six browser.* tools with fake backend tests"
```

---

### Task 7: Standalone Playwright backend

**Files:**
- Create: `internal/tools/desktop/browser/standalone.go`
- Test: `internal/tools/desktop/browser/standalone_test.go`

**Interfaces:**
- Consumes: `BrowserBackend`, `PageHandle`, `ScreenshotOpts` (Task 3).
- Produces: `type StandaloneBackend struct` implementing `BrowserBackend`. Constructor `func NewStandaloneBackend(headless bool, timeout time.Duration) (*StandaloneBackend, error)`. The `NewPage` method launches Playwright + Chromium lazily if not already running.

- [ ] **Step 1: Write the integration test (skipped when short)**

Create `internal/tools/desktop/browser/standalone_test.go`:

```go
package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStandaloneBackendNavigateAndRead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	if backend.Mode() != "standalone" {
		t.Errorf("mode = %q, want standalone", backend.Mode())
	}

	page, err := backend.NewPage(context.Background())
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	target := "https://example.com"
	if err := page.Navigate(context.Background(), target); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	_ = page.WaitForLoadState(context.Background(), "domcontentloaded")

	title, err := page.Title(context.Background())
	if err != nil {
		t.Fatalf("Title: %v", err)
	}
	if !strings.Contains(title, "Example") {
		t.Errorf("title = %q, want something containing 'Example'", title)
	}

	text, err := page.ReadableText(context.Background())
	if err != nil {
		t.Fatalf("ReadableText: %v", err)
	}
	if !strings.Contains(text, "Example") {
		t.Errorf("text = %q, want something containing 'Example'", text)
	}
}

func TestStandaloneBackendScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	page, err := backend.NewPage(context.Background())
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	_ = page.Navigate(context.Background(), "https://example.com")
	_ = page.WaitForLoadState(context.Background(), "domcontentloaded")

	data, err := page.Screenshot(context.Background(), ScreenshotOpts{Format: "png"})
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(data) < 100 {
		t.Errorf("screenshot data too small: %d bytes", len(data))
	}
}
```

- [ ] **Step 2: Run test (short mode, should skip)**

Run: `go test ./internal/tools/desktop/browser/ -run TestStandalone -short -v`
Expected: PASS (skipped)

- [ ] **Step 3: Write standalone.go**

Create `internal/tools/desktop/browser/standalone.go`:

```go
package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

type StandaloneBackend struct {
	headless    bool
	timeout     time.Duration
	pw          *playwright.Playwright
	browser     playwright.Browser
	mu          sync.Mutex
	startedOnce bool
}

func NewStandaloneBackend(headless bool, timeout time.Duration) (*StandaloneBackend, error) {
	return &StandaloneBackend{
		headless: headless,
		timeout:  timeout,
	}, nil
}

func (b *StandaloneBackend) ensureStarted() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startedOnce {
		return nil
	}
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright: %w", err)
	}
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(b.headless),
	})
	if err != nil {
		pw.Stop()
		return fmt.Errorf("launch chromium: %w", err)
	}
	b.pw = pw
	b.browser = browser
	b.startedOnce = true
	return nil
}

func (b *StandaloneBackend) NewPage(ctx context.Context) (PageHandle, error) {
	if err := b.ensureStarted(); err != nil {
		return nil, err
	}
	pwCtx, err := b.browser.NewContext(playwright.BrowserNewContextOptions{})
	if err != nil {
		return nil, fmt.Errorf("new browser context: %w", err)
	}
	page, err := pwCtx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	if b.timeout > 0 {
		timeoutMs := float64(b.timeout / time.Millisecond)
		page.SetDefaultTimeout(timeoutMs)
		page.SetDefaultNavigationTimeout(timeoutMs)
	}
	return &standalonePage{page: page, ctx: pwCtx}, nil
}

func (b *StandaloneBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.startedOnce {
		return nil
	}
	var errs []error
	if b.browser != nil {
		if err := b.browser.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if b.pw != nil {
		if err := b.pw.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	b.startedOnce = false
	if len(errs) > 0 {
		return fmt.Errorf("standalone backend close: %v", errs)
	}
	return nil
}

func (b *StandaloneBackend) Mode() string {
	return "standalone"
}

type standalonePage struct {
	page playwright.Page
	ctx  playwright.BrowserContext
}

func (p *standalonePage) Navigate(ctx context.Context, url string) error {
	_, err := p.page.Goto(url)
	return err
}

func (p *standalonePage) Title(ctx context.Context) (string, error) {
	return p.page.Title()
}

func (p *standalonePage) URL(ctx context.Context) (string, error) {
	return p.page.URL(), nil
}

func (p *standalonePage) Text(ctx context.Context, selector string) (string, error) {
	el, err := p.page.Query(selector)
	if err != nil {
		return "", err
	}
	if el == nil {
		return "", fmt.Errorf("element %q not found", selector)
	}
	return el.InnerText()
}

func (p *standalonePage) HTML(ctx context.Context, selector string) (string, error) {
	if selector == "" {
		return p.page.Content()
	}
	el, err := p.page.Query(selector)
	if err != nil {
		return "", err
	}
	if el == nil {
		return "", fmt.Errorf("element %q not found", selector)
	}
	return el.InnerHTML()
}

func (p *standalonePage) ReadableText(ctx context.Context) (string, error) {
	return p.page.InnerHTML("body")
}

func (p *standalonePage) Click(ctx context.Context, selector string) error {
	return p.page.Click(selector)
}

func (p *standalonePage) Fill(ctx context.Context, selector, value string) error {
	return p.page.Fill(selector, value)
}

func (p *standalonePage) PressKey(ctx context.Context, key string) error {
	return p.page.Keyboard().Press(key)
}

func (p *standalonePage) Submit(ctx context.Context, selector string) error {
	return p.page.Click(selector)
}

func (p *standalonePage) WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	return p.page.WaitForSelector(selector, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(float64(timeout / time.Millisecond)),
	})
}

func (p *standalonePage) WaitForLoadState(ctx context.Context, state string) error {
	return p.page.WaitForLoadState(playwright.LoadState(state))
}

func (p *standalonePage) Screenshot(ctx context.Context, opts ScreenshotOpts) ([]byte, error) {
	pwOpts := playwright.PageScreenshotOptions{
		FullPage: playwright.Bool(opts.FullPage),
		Type:     playwright.ScreenshotTypePng,
	}
	if opts.Format == "jpeg" {
		pwOpts.Type = playwright.ScreenshotTypeJpeg
	}
	return p.page.Screenshot(pwOpts)
}

func (p *standalonePage) Close() error {
	var errs []error
	if err := p.page.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := p.ctx.Close(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("standalone page close: %v", errs)
	}
	return nil
}
```

- [ ] **Step 4: Run unit tests (short mode) to verify compilation**

Run: `go test ./internal/tools/desktop/browser/ -short -v`
Expected: PASS (integration tests skipped, unit tests pass)

- [ ] **Step 5: Run full build**

Run: `go build ./...`
Expected: PASS

- [ ] **Step 6: Run format and vet**

Run: `gofmt -w internal/tools/desktop/browser/standalone.go internal/tools/desktop/browser/standalone_test.go && go vet ./internal/tools/desktop/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/tools/desktop/browser/standalone.go internal/tools/desktop/browser/standalone_test.go
git commit -m "feat(desktop): standalone Playwright browser backend"
```

---

### Task 8: Attach (CDP) backend

**Files:**
- Create: `internal/tools/desktop/browser/attach.go`
- Test: `internal/tools/desktop/browser/attach_test.go`

**Interfaces:**
- Consumes: `BrowserBackend`, `PageHandle`, `ScreenshotOpts` (Task 3).
- Produces: `type AttachBackend struct` implementing `BrowserBackend`. Constructor `func NewAttachBackend(cdpURL string, timeout time.Duration) (*AttachBackend, error)`.

- [ ] **Step 1: Write the integration test (skipped when short)**

Create `internal/tools/desktop/browser/attach_test.go`:

```go
package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAttachBackendMode(t *testing.T) {
	b, err := NewAttachBackend("http://localhost:9222", 30*time.Second)
	if err != nil {
		t.Fatalf("NewAttachBackend: %v", err)
	}
	if b.Mode() != "attach" {
		t.Errorf("mode = %q, want attach", b.Mode())
	}
	_ = b.Close()
}

func TestAttachBackendNewPageRequiresRunningBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	b, err := NewAttachBackend("http://localhost:9999", 2*time.Second)
	if err != nil {
		t.Fatalf("NewAttachBackend: %v", err)
	}
	defer b.Close()
	_, err = b.NewPage(context.Background())
	if err == nil {
		t.Fatal("expected error connecting to non-existent CDP endpoint")
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "websocket") && !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "context") {
		t.Logf("error (acceptable): %v", err)
	}
}
```

- [ ] **Step 2: Run test (short mode, should skip or pass for Mode)**

Run: `go test ./internal/tools/desktop/browser/ -run TestAttach -short -v`
Expected: PASS (TestAttachBackendMode passes, integration test skipped)

- [ ] **Step 3: Write attach.go**

Create `internal/tools/desktop/browser/attach.go`:

```go
package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

type AttachBackend struct {
	cdpURL      string
	timeout     time.Duration
	pw          *playwright.Playwright
	browser     playwright.Browser
	mu          sync.Mutex
	startedOnce bool
}

func NewAttachBackend(cdpURL string, timeout time.Duration) (*AttachBackend, error) {
	if cdpURL == "" {
		return nil, fmt.Errorf("attach mode requires a cdp_url")
	}
	return &AttachBackend{
		cdpURL:  cdpURL,
		timeout: timeout,
	}, nil
}

func (b *AttachBackend) ensureConnected(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startedOnce {
		return nil
	}
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright: %w", err)
	}
	browser, err := pw.Chromium.ConnectOverCDP(b.cdpURL)
	if err != nil {
		pw.Stop()
		return fmt.Errorf("connect to browser at %s: %w", b.cdpURL, err)
	}
	b.pw = pw
	b.browser = browser
	b.startedOnce = true
	return nil
}

func (b *AttachBackend) NewPage(ctx context.Context) (PageHandle, error) {
	if err := b.ensureConnected(ctx); err != nil {
		return nil, err
	}
	contexts := b.browser.Contexts()
	var pwCtx playwright.BrowserContext
	if len(contexts) > 0 {
		pwCtx = contexts[0]
	} else {
		var err error
		pwCtx, err = b.browser.NewContext(playwright.BrowserNewContextOptions{})
		if err != nil {
			return nil, fmt.Errorf("new browser context: %w", err)
		}
	}
	page, err := pwCtx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	if b.timeout > 0 {
		timeoutMs := float64(b.timeout / time.Millisecond)
		page.SetDefaultTimeout(timeoutMs)
		page.SetDefaultNavigationTimeout(timeoutMs)
	}
	return &standalonePage{page: page, ctx: pwCtx}, nil
}

func (b *AttachBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.startedOnce {
		return nil
	}
	b.startedOnce = false
	if b.browser != nil {
		_ = b.browser.Close()
	}
	if b.pw != nil {
		_ = b.pw.Stop()
	}
	return nil
}

func (b *AttachBackend) Mode() string {
	return "attach"
}
```

- [ ] **Step 4: Run unit tests (short mode)**

Run: `go test ./internal/tools/desktop/browser/ -short -v`
Expected: PASS

- [ ] **Step 5: Run format and vet**

Run: `gofmt -w internal/tools/desktop/browser/attach.go internal/tools/desktop/browser/attach_test.go && go vet ./internal/tools/desktop/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tools/desktop/browser/attach.go internal/tools/desktop/browser/attach_test.go
git commit -m "feat(desktop): CDP attach browser backend"
```

---

### Task 9: Backend factory and wiring into app.Run

**Files:**
- Modify: `internal/app/app.go` (call desktop.RegisterAll after native.RegisterAll, return desktop closer)
- Modify: `internal/app/runtime.go` (add DesktopCloser field, close in Close())
- Test: `internal/app/app_test.go` (verify desktop tools register when enabled)

**Interfaces:**
- Consumes: `desktop.RegisterAll` (Task 6), `desktop.Options` (Task 6), `browser.NewStandaloneBackend` (Task 7), `browser.NewAttachBackend` (Task 8), `config.DesktopConfig` (Task 2), `Runtime` struct (existing).
- Produces: Desktop tools registered in the registry when `cfg.Desktop.Enabled` is true. Browser session closed on `Runtime.Close()`.

- [ ] **Step 1: Examine the existing buildAgentRunner return and Runtime struct**

Read `internal/app/app.go:299` — the `buildAgentRunner` function returns `(*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Service, *native.JobManager, error)`. We need to add a `func()` closer for the desktop session. Read `internal/app/runtime.go:58-89` — the `Runtime` struct needs a new `DesktopCloser func()` field, closed in `Close()` at `runtime.go:204`.

- [ ] **Step 2: Write the failing test**

Add to `internal/app/app_test.go`. First, check the existing test patterns for how app tests inject config. The test should verify that when `Desktop.Enabled = true`, browser tools appear in the registry.

Find an existing test that constructs a config and check for registered tools. Add this test near the end of the file:

```go
func TestRunRegistersDesktopToolsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `
[project]
name = "test"

[desktop]
enabled = true
mode = "standalone"
headless = true
`
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg, err := config.Load(config.LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Desktop.Enabled {
		t.Fatal("desktop should be enabled")
	}

	reg := registry.New()
	fakeFactory := func() (browser.BrowserBackend, error) {
		return &browser.FakeBackend{}, nil
	}
	if _, err := desktop.RegisterAll(reg, desktop.Options{Config: cfg.Desktop, BackendFactory: fakeFactory}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tools := reg.List()
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, expected := range []string{"browser.navigate", "browser.read", "browser.click", "browser.fill", "browser.submit", "browser.screenshot"} {
		if !names[expected] {
			t.Errorf("tool %q not registered", expected)
		}
	}
}
```

Add the necessary imports to the top of `app_test.go` if not already present:

```go
	"marshal/internal/tools/desktop"
	"marshal/internal/tools/desktop/browser"
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestRunRegistersDesktopToolsWhenEnabled -v`
Expected: FAIL — `desktop` or `browser` package not imported or not found in the test file's import block.

- [ ] **Step 4: Add DesktopCloser to Runtime struct**

In `internal/app/runtime.go`, add to the `Runtime` struct after `JobManager` (around line 77):

```go
	JobManager     *native.JobManager
	DesktopCloser   func()
	additionalDirs []string
```

- [ ] **Step 5: Close DesktopCloser in Runtime.Close**

In `internal/app/runtime.go`, in `Close()`, after the database close block and before the closeFns block (around line 253-255):

```go
		// 7. database.
		if rt.DB != nil {
			if err := rt.DB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("db close: %w", err))
			}
		}

		// 7b. desktop browser session.
		if rt.DesktopCloser != nil {
			rt.DesktopCloser()
		}

		// 8. reverse-order closeFns (log file).
```

- [ ] **Step 6: Add desktop registration to buildAgentRunner**

In `internal/app/app.go`, modify `buildAgentRunner` to register desktop tools after `native.RegisterAll` (around line 367-370). Change the signature to return an additional `func()` for the desktop closer. The current signature:

```go
func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent]) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Service, *native.JobManager, error) {
```

Change to:

```go
func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent]) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Service, *native.JobManager, func(), error) {
```

Update every `return nil, nil, nil, nil, nil, nil, err` and `return nil, nil, nil, nil, nil, nil, fmt.Errorf(...)` in the function to add a `nil` before the error — they become `return nil, nil, nil, nil, nil, nil, nil, err`. Search for all such returns and update them.

Update the final successful return (around line 459):

```go
	return runner, reg, swarmRunner, mcpMgr, snapSvc, jobManager, nil
```

Change to:

```go
	return runner, reg, swarmRunner, mcpMgr, snapSvc, jobManager, desktopCloser, nil
```

And add the desktop registration block just before the final return, after the MCP registration block (after line 386). Insert:

```go
	var desktopCloser func()
	if cfg.Desktop.Enabled {
		desktopOpts := desktop.Options{
			Config: cfg.Desktop,
			BackendFactory: func() (browser.BrowserBackend, error) {
				return newDesktopBackend(cfg.Desktop)
			},
		}
		closer, err := desktop.RegisterAll(reg, desktopOpts)
		if err != nil {
			jmErr = err
			return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register desktop tools: %w", err)
		}
		desktopCloser = closer
	}
```

Note: `desktop.RegisterAll` returns `(func(), error)` — the closer function closes the browser session (see Task 6). This is assigned to `desktopCloser` and returned from `buildAgentRunner` so `Runtime.Close()` can call it during shutdown.

Add the `newDesktopBackend` helper function in `app.go` (at the bottom of the file, or near the other helper functions):

```go
func newDesktopBackend(cfg config.DesktopConfig) (browser.BrowserBackend, error) {
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	switch cfg.Mode {
	case "attach":
		return browser.NewAttachBackend(cfg.CDPURL, timeout)
	case "standalone", "":
		return browser.NewStandaloneBackend(cfg.Headless, timeout)
	default:
		return nil, fmt.Errorf("unknown desktop mode %q", cfg.Mode)
	}
}
```

Add the imports to `app.go`:

```go
	"marshal/internal/tools/desktop"
	"marshal/internal/tools/desktop/browser"
```

- [ ] **Step 7: Update StartRuntime to wire DesktopCloser**

In `internal/app/runtime.go`, the `buildAgentRunner` call (around line 418) now returns an extra value. Update:

```go
	runner, toolReg, swarmRunner, mcpMgr, snapSvc, jobMgr, desktopCloser, err := buildAgentRunner(workCtx, cfg, state, database, projectID, skillIndex, dataDir, runOpts.additionalDirs, jobBroker)
```

Then in the `Runtime` struct construction (around line 438), add after `JobManager: jobMgr,`:

```go
		JobManager:     jobMgr,
		DesktopCloser:   desktopCloser,
```

- [ ] **Step 8: Update reloadAgentRuntime to handle the new return value**

In `internal/app/app.go`, `reloadAgentRuntime` (around line 729) calls `buildAgentRunner`:

```go
	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, err := buildAgentRunner(rt.workCtx, cfg, rt.State, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb)
```

Change to:

```go
	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newDesktopCloser, err := buildAgentRunner(rt.workCtx, cfg, rt.State, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb)
```

Then after the swap section (around line 758), add the desktop closer swap. After `rt.JobManager = newJobMgr`:

```go
	rt.JobManager = newJobMgr
```

Add:

```go
	// Close the old desktop session and swap in the new one.
	if rt.DesktopCloser != nil {
		rt.DesktopCloser()
	}
	rt.DesktopCloser = newDesktopCloser
```

This goes inside the `rt.mu.Lock()` ... `rt.mu.Unlock()` block.

- [ ] **Step 9: Update the unit tests that call buildAgentRunner or mock its return**

Run: `go build ./internal/app/...`
Expected: If there are compilation errors from tests calling `buildAgentRunner` with the old signature, fix them by adding the `nil` or `desktopCloser` value.

Run: `go test ./internal/app/ -run TestRunRegistersDesktopToolsWhenEnabled -v`
Expected: PASS

- [ ] **Step 10: Run full test suite (short mode)**

Run: `go test ./... -short`
Expected: PASS — all existing tests stay green, new desktop tests pass.

- [ ] **Step 11: Run format and vet**

Run: `gofmt -w internal/app/app.go internal/app/runtime.go internal/app/app_test.go internal/tools/desktop/tools.go && go vet ./...`
Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add internal/app/app.go internal/app/runtime.go internal/app/app_test.go internal/tools/desktop/tools.go
git commit -m "feat(desktop): wire browser tools into app.Run with backend factory and lifecycle"
```

---

### Task 10: Configuration examples documentation

**Files:**
- Modify: `docs/09-configuration-examples.md`

**Interfaces:**
- Consumes: the `[desktop]` config block from Task 2.

- [ ] **Step 1: Read the existing config examples file**

Read `docs/09-configuration-examples.md` to understand the format and find where to add the desktop section.

- [ ] **Step 2: Add desktop config example**

Add a new section to `docs/09-configuration-examples.md` following the existing section format. Add at the end of the file:

```markdown
## Desktop browser automation

Marshal can drive a web browser (Chromium via Playwright) for Cowork-style automation. The browser is visible by default so you can watch the agent work.

### Standalone mode (default)

Launches a bundled Chromium instance managed by Marshal.

```toml
[desktop]
enabled = true
mode = "standalone"
headless = false
default_timeout = "30s"
```

One-time setup: run `go run github.com/playwright-community/playwright-go/cmd/playwright install chromium` to download the browser binary.

### Attach mode

Connect to your running Chrome browser (keeps your session, cookies, profile):

```toml
[desktop]
enabled = true
mode = "attach"
cdp_url = "http://localhost:9222"
default_timeout = "30s"
```

You must launch Chrome with `--remote-debugging-port=9222`.

### URL restrictions

Limit which sites the agent can navigate to:

```toml
[desktop]
enabled = true
url_allowlist = ["example.com", "docs.mycompany.com"]
url_denylist = ["example.com/admin"]
```

Entries match on hostname (with subdomain support) and optional path prefix.
```

- [ ] **Step 3: Commit**

```bash
git add docs/09-configuration-examples.md
git commit -m "docs(config): add desktop browser automation examples"
```

---

### Task 11: Full integration verification

**Files:**
- None (verification only)

- [ ] **Step 1: Run the complete test suite**

Run: `go test ./... -short`
Expected: PASS — all tests green, no regressions.

- [ ] **Step 2: Run the build**

Run: `go build ./cmd/marshal`
Expected: PASS — binary builds successfully.

- [ ] **Step 3: Run vet across the whole project**

Run: `go vet ./...`
Expected: PASS — no warnings.

- [ ] **Step 4: Run format check**

Run: `gofmt -l .`
Expected: no output (all files formatted).

- [ ] **Step 5: Verify no existing tests broke**

Run: `go test ./internal/tools/native/... ./internal/tools/registry/... ./internal/tools/policy/... ./internal/sandbox/... ./internal/app/config/... -short -v 2>&1 | tail -20`
Expected: PASS — all pre-existing tests pass unchanged.

- [ ] **Step 6: Commit if any formatting changes were needed**

```bash
gofmt -w .
git add -A
git commit -m "style: format desktop automation code" || echo "nothing to commit"
```