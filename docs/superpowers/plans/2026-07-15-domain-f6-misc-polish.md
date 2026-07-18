# Domain F6 — Theme, Encoding & Pub/Sub Polish Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in a
> dedicated worktree (suggested branch `feature/domain-f6-misc-polish`).
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** Resolve seven low-severity findings from
`docs/14-codebase-improvement-audit-2026-07-14.md` (Domain F) that
cover pub/sub delivery, theme reload, rune-aware truncation,
markdown-renderer caching, dead imports, and stale comments:

- **F-BUG-157** (MEDIUM) — `pubsub.Broker` drops events on a full
  buffer; the TUI's job-count subscription relies on exact counts
  and shows stale values.
- **F-POL-166** (LOW) — `activeTheme` is reloaded by `loadTheme`
  but `lipgloss.Style` instances held by other packages (e.g.
  `picker`, `memory`) still use the old theme because they
  captured it at import time.
- **F-POL-167** (LOW) — `huhtheme.WarmSunset` calls `theme.Load()`
  at package init. Changes to `$NO_COLOR` after startup are not
  reflected.
- **F-POL-171** (LOW) — `truncateURL` (browserbar.go:44-60) uses
  byte length instead of rune/visible width.
- **F-POL-172** (LOW) — `transcript.go` glamour-renderer cache
  never evicts entries; the map grows on every resize.
- **F-POL-173** (LOW) — `model.go` imports the `patch` package
  only for `patternForApproval`. Belongs in `internal/permissions`.
- **F-POL-175** (LOW) — `runtime.go:263` comment "closeFns (log
  file)" is misleading; the slice holds arbitrary closers.

**Architecture:** Localised edits. The pub/sub change (Task 1)
introduces a new `WithTerminal` option to opt in to must-deliver
semantics. The theme work (Tasks 2, 3) is a small refactor of
when `theme.Load()` is called. Tasks 4-7 are mechanical.

**Tech Stack:** Go 1.22+.

---

## Global Constraints

- Every code change MUST compile: `CGO_ENABLED=1 go build ./...`
  after each task.
- Every test change MUST pass: `CGO_ENABLED=1 go test ./...`
  after each task.
- Commit per task with the exact message shown.
- The pub/sub API (`pubsub.New`, `Subscribe`) MUST stay
  backwards-compatible: existing callers that do not pass
  `WithTerminal` keep the current best-effort behaviour.

---

## File Structure

Files modified by this plan:

- `internal/pubsub/broker.go` — Task 1
- `internal/pubsub/broker_test.go` — Task 1 (add test)
- `internal/app/tui/model.go` — Task 1 (use `WithTerminal` for
  job events)
- `internal/app/tui/theme/...` — Tasks 2, 3
- `internal/app/tui/theme/theme_test.go` — Tasks 2, 3 (add tests)
- `internal/app/tui/browserbar.go` — Task 4
- `internal/app/tui/browserbar_test.go` — Task 4 (add test)
- `internal/app/tui/transcript.go` — Task 5
- `internal/app/tui/transcript_test.go` — Task 5 (add test)
- `internal/app/tui/model.go` — Task 6
- `internal/app/tui/model_test.go` — Task 6 (build sentinel)
- `internal/permissions/...` — Task 6 (move `patternForApproval`)
- `internal/permissions/permissions_test.go` — Task 6 (add test)
- `internal/app/runtime.go` — Task 7 (rename `closeFns`)

---

### Task 1: F-BUG-157 — Terminal pub/sub subscription for job counts

**Files:**
- Modify: `internal/pubsub/broker.go` (add `WithTerminal`,
  per-subscription flag, blocking-send path)
- Add tests: `internal/pubsub/broker_test.go`
- Modify: `internal/app/tui/model.go` (subscribe to job events
  with `WithTerminal`)
- Modify: `internal/app/tui/model_test.go`

**Problem:** When the broker's channel buffer is full, the
`Publish` path drops the oldest event (`drop-head` semantics). The
TUI's job-count subscription is the only consumer that needs every
event; with drops, the status line can show stale counts.

**Fix:** Add a `WithTerminal()` subscription option. When a
subscription is terminal, the publisher blocks (with a short
deadline) on send instead of dropping. If the consumer is too
slow, log a warning and drop, but never silently.

**Implementation steps:**

- [ ] **Step 1: Add the option**

```go
type SubscriptionOption func(*subscription)

func WithTerminal() SubscriptionOption {
    return func(s *subscription) { s.terminal = true }
}
```

- [ ] **Step 2: Update `Subscribe` to accept options**

```go
func (b *Broker) Subscribe(topic string, opts ...SubscriptionOption) Subscription { ... }
```

- [ ] **Step 3: Branch the send path in `Publish`**

```go
if s.terminal {
    select {
    case s.ch <- evt:
    case <-time.After(500*time.Millisecond):
        slog.Default().Warn("terminal subscription slow; dropping", "topic", topic)
    }
    continue
}
// existing drop-head path
```

- [ ] **Step 4: Use the option in the TUI**

In `internal/app/tui/model.go`, find the job-event subscription
and pass `pubsub.WithTerminal()`.

- [ ] **Step 5: Test**

```go
func TestTerminalSubscriptionBlocksUntilConsumed(t *testing.T) {
    b := pubsub.New()
    sub := b.Subscribe("jobs", pubsub.WithTerminal())
    // Saturate the buffer.
    b.Publish("jobs", "a")
    b.Publish("jobs", "b")
    b.Publish("jobs", "c")
    // Now publish a fourth event; the publisher must not silently
    // drop — it should block until the consumer drains.
    done := make(chan struct{})
    go func() {
        b.Publish("jobs", "d")
        close(done)
    }()
    select {
    case <-done:
        t.Fatal("terminal publish should block on full buffer")
    case <-time.After(50*time.Millisecond):
    }
    <-sub.C()
    <-sub.C()
    <-sub.C()
    <-sub.C()
    <-done
}
```

- [ ] **Step 6: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/pubsub/... ./internal/app/tui -v
git add internal/pubsub/broker.go internal/pubsub/broker_test.go internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(pubsub): add WithTerminal for must-deliver subscriptions (F-BUG-157)"
```

---

### Task 2: F-POL-166 — Theme reload propagates to dependent packages

**Files:**
- Modify: `internal/app/tui/theme/loader.go` (or wherever
  `Load()` lives)
- Add tests: `internal/app/tui/theme/theme_test.go`

**Problem:** `activeTheme` is reloaded by `loadTheme` in
`model.go`, but `lipgloss.Style` instances held by other packages
(`picker`, `memory`, etc.) were built at import time from the old
theme.

**Fix:** Publish a `theme.ChangedMsg` via the existing
`pubsub.Broker` (or a new dedicated topic). Packages that build
styles lazily re-build on `ChangedMsg`. Import-time captures are
replaced with calls to `theme.Current()` inside `View`.

**Implementation steps:**

- [ ] **Step 1: Add the event**

In `internal/app/tui/theme/loader.go`:

```go
type ChangedMsg struct{ Theme *Theme }

func (l *Loader) ChangedMsg() ChangedMsg { return ChangedMsg{Theme: l.current} }
```

- [ ] **Step 2: Publish on reload**

In `model.go`'s `loadTheme` (or `applyTheme`), publish the message
via the existing broker.

- [ ] **Step 3: Audit dependent packages**

```bash
grep -rn 'lipgloss.NewStyle' internal/app/tui/picker internal/app/tui/memory
```

Replace any package-level `var style = lipgloss.NewStyle()...`
with a function `func style() lipgloss.Style { return
lipgloss.NewStyle().Foreground(theme.Current().Fg.Muted) }`.

- [ ] **Step 4: Test**

```go
func TestThemeReloadPropagates(t *testing.T) {
    l := theme.NewLoader(theme.Default)
    var got *theme.Theme
    l.Subscribe(func(t *theme.Theme) { got = t })
    l.Reload(theme.Light)
    if got == nil || got.Name != "light" {
        t.Fatal("subscriber not notified")
    }
}
```

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -v
git add internal/app/tui/theme internal/app/tui/picker internal/app/tui/memory internal/app/tui/model.go
git commit -m "refactor(tui/theme): publish ChangedMsg on reload (F-POL-166)"
```

---

### Task 3: F-POL-167 — `huhtheme.WarmSunset` reads theme lazily

**Files:**
- Modify: `internal/app/tui/huhtheme/theme.go`
- Add tests: `internal/app/tui/huhtheme/theme_test.go`

**Problem:** `theme.Load()` is called at package init. The
`$NO_COLOR` and `$TERM` env vars are not re-read after startup.

**Fix:** Replace the package-level `var warmSunset = ...` with a
function `WarmSunset() huh.Theme` that calls `theme.Load()` on
each invocation.

**Implementation steps:**

- [ ] **Step 1: Convert to a function**

```go
func WarmSunset() huh.Theme {
    t := theme.Load()
    return huh.Theme{
        Base:     baseFromTheme(t),
        // ...
    }
}
```

(Adjust field names to match the existing constructor.)

- [ ] **Step 2: Update call sites**

```bash
grep -rn 'huhtheme.WarmSunset' --include='*.go'
```

If callers store the result in a package-level var, replace with a
function call inside `View()`.

- [ ] **Step 3: Test**

```go
func TestWarmSunsetReadsNoColor(t *testing.T) {
    t.Setenv("NO_COLOR", "1")
    th := huhtheme.WarmSunset()
    if th.Focused.Title.GetForeground() != nil {
        // NO_COLOR=1 forces the default foreground
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui/huhtheme -v
git add internal/app/tui/huhtheme/theme.go internal/app/tui/huhtheme/theme_test.go
git commit -m "refactor(tui/huhtheme): load theme on each WarmSunset call (F-POL-167)"
```

---

### Task 4: F-POL-171 — Rune-aware URL truncation

**Files:**
- Modify: `internal/app/tui/browserbar.go` (`truncateURL`)
- Add tests: `internal/app/tui/browserbar_test.go`

**Problem:** `truncateURL` uses `len(raw)` and `raw[:max]`, which
operate on bytes. Multibyte characters are split mid-rune.

**Fix:** Convert to `[]rune` and use `ansi.StringWidth` for
width-aware truncation. Use `ansi.Cut` to handle escape
sequences correctly.

**Implementation steps:**

- [ ] **Step 1: Rewrite `truncateURL`**

```go
func truncateURL(raw string, maxWidth int) string {
    if ansi.StringWidth(raw) <= maxWidth {
        return raw
    }
    out, _ := ansi.Cut(raw, 0, maxWidth-1)
    return out + "…"
}
```

- [ ] **Step 2: Test**

```go
func TestTruncateURLIsRuneAware(t *testing.T) {
    out := truncateURL("https://例え.com/path/very/long", 12)
    if !utf8.ValidString(out) {
        t.Fatal("output is not valid UTF-8")
    }
    if ansi.StringWidth(out) > 12 {
        t.Fatalf("output exceeds max width: %d", ansi.StringWidth(out))
    }
}
```

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestTruncate' -v
git add internal/app/tui/browserbar.go internal/app/tui/browserbar_test.go
git commit -m "fix(tui/browserbar): rune-aware URL truncation (F-POL-171)"
```

---

### Task 5: F-POL-172 — Bound the markdown renderer cache

**Files:**
- Modify: `internal/app/tui/transcript.go` (the `mdRenderers`
  cache)
- Add tests: `internal/app/tui/transcript_test.go`

**Problem:** `mdRenderers` is a `map[int]*glamour.TermRenderer`
keyed by width. On every resize a new entry is added; the map
grows unboundedly.

**Fix:** Replace with a small LRU (or, more simply, evict any
entry whose width differs by more than a threshold — e.g. 8
columns — from the current width).

**Implementation steps:**

- [ ] **Step 1: Replace the map with a sized cache**

```go
const maxRenderers = 4

var (
    mdMu       sync.Mutex
    mdRenderers = map[int]*glamour.TermRenderer{}
)

func getRenderer(width int) *glamour.TermRenderer {
    mdMu.Lock()
    defer mdMu.Unlock()
    if r, ok := mdRenderers[width]; ok {
        return r
    }
    // Evict the entry whose width is farthest from `width`.
    if len(mdRenderers) >= maxRenderers {
        var evictKey int
        var evictDist int
        first := true
        for k := range mdRenderers {
            d := abs(k - width)
            if first || d > evictDist {
                evictKey, evictDist, first = k, d, false
            }
        }
        delete(mdRenderers, evictKey)
    }
    r, _ := glamour.NewTermRenderer(
        glamour.WithStandardStyle("notty"),
        glamour.WithWordWrap(width),
    )
    mdRenderers[width] = r
    return r
}
```

- [ ] **Step 2: Test**

```go
func TestRendererCacheEvicts(t *testing.T) {
    for i := 0; i < 20; i++ {
        _ = getRenderer(60 + i*7)
    }
    mdMu.Lock()
    size := len(mdRenderers)
    mdMu.Unlock()
    if size > maxRenderers {
        t.Fatalf("cache exceeded bound: %d", size)
    }
}
```

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestRenderer' -v
git add internal/app/tui/transcript.go internal/app/tui/transcript_test.go
git commit -m "perf(tui/transcript): bound the markdown renderer cache (F-POL-172)"
```

---

### Task 6: F-POL-173 — Move `patternForApproval` into `internal/permissions`

**Files:**
- Modify: `internal/app/tui/model.go` (remove the function and
  its import of `patch`)
- New file: `internal/permissions/pattern.go` (or extend
  `internal/permissions/permissions.go`)
- Add tests: `internal/permissions/permissions_test.go`

**Problem:** `patternForApproval` lives in `model.go` and pulls
in the `patch` and `permissions` packages for one helper. The
function is policy logic, not view logic.

**Fix:** Move the function to `internal/permissions`. Replace
call sites with `permissions.PatternForApproval(tc)`.

**Implementation steps:**

- [ ] **Step 1: Move the function**

Create `internal/permissions/pattern.go`:

```go
package permissions

import (
    "path/filepath"
    "strings"
    "marshal/internal/app/session"
)

// PatternForApproval builds the glob-like pattern persisted in the
// user's config when the user picks "always allow" in the approval
// chooser. The result is matched against the full parsed command
// (post-A2), not just argv0.
func PatternForApproval(tc *session.PendingToolCall) string {
    // ... existing body ...
}
```

- [ ] **Step 2: Update call sites**

```bash
grep -rn 'patternForApproval' --include='*.go'
```

Replace each with `permissions.PatternForApproval(tc)`.

- [ ] **Step 3: Remove the dead import**

In `internal/app/tui/model.go`, drop the `patch` import if no
longer used.

- [ ] **Step 4: Test**

```go
func TestPatternForApprovalShellRun(t *testing.T) {
    tc := &session.PendingToolCall{Name: "shell.run", Command: "git status"}
    if got := permissions.PatternForApproval(tc); got != "git status" {
        t.Fatalf("expected full command, got %q", got)
    }
}
```

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/permissions ./internal/app/tui -v
git add internal/app/tui/model.go internal/permissions/pattern.go internal/permissions/permissions_test.go
git commit -m "refactor(tui): move patternForApproval to permissions package (F-POL-173)"
```

---

### Task 7: F-POL-175 — Rename `closeFns` to `resourceClosers`

**Files:**
- Modify: `internal/app/runtime.go` (rename the field and the
  comment)
- Add tests: `internal/app/runtime_test.go` (build sentinel)

**Problem:** The `closeFns` slice holds arbitrary closers, but the
comment calls out "log file" only. The name lies; the comment
underdocuments.

**Fix:** Rename the field to `resourceClosers`. Update the
comment to state "resources are appended in setup order and closed
in reverse."

- [ ] **Step 1: Find the field**

```bash
grep -n 'closeFns' internal/app/runtime.go
```

- [ ] **Step 2: Rename + comment**

```go
// resourceClosers holds cleanup functions (log file, DB handle,
// open file descriptors) appended in setup order. They are
// invoked in reverse during Close, so the most recently opened
// resource is released first.
resourceClosers []func()
```

- [ ] **Step 3: Update all references**

```bash
grep -rn 'closeFns' --include='*.go'
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app -v
git add internal/app/runtime.go
git commit -m "refactor(app): rename closeFns to resourceClosers (F-POL-175)"
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
```

Manual smoke checklist:

- [ ] Start a long-running tool with many background jobs; the
      job count in the status line stays accurate.
- [ ] Toggle light/dark theme via `/settings`; the picker and
      memory browser use the new theme without restart.
- [ ] Set `NO_COLOR=1` and restart; huh surfaces are uncolored.
- [ ] Type a URL with multibyte characters in the browser bar;
      truncation is on a rune boundary.
- [ ] Resize the terminal 20 times; transcript renders stay fast.

Update `docs/14-codebase-improvement-audit-2026-07-14.md` with the
new resolution table entries.
