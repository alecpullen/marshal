# Task 5 fix report — three code-review findings

Base commit: `cec60640e72b1c6931501bfa88be0b8fa12ab2cd` (fix(tui): retry unsaved browser settings)

## Finding 1: failed-save retry semantics in the docked settings browser

### What was wrong

`BrowserPanel.flushChanges` (`internal/app/tui/settings/browser.go:319` at base
commit) diffed `b.baseline` against `b.reg.Config()` and returned early with
no save attempt whenever the diff was empty:

```go
func (b *BrowserPanel) flushChanges(inner tea.Cmd) tea.Cmd {
	lines := configDiff(b.baseline, b.reg.Config())
	if len(lines) == 0 {
		b.pendingKey = ""
		return inner
	}
	...
	saveErr := config.SaveProjectConfig(b.cfgPath, b.reg.Config())
	...
	b.baseline = cloneConfig(b.reg.Config())  // rolled forward even on failure
```

`b.baseline` is rolled forward to the in-memory (unsaved) config
unconditionally, even when `saveErr != nil` (this is intentional — it keeps
the edit visible/editable, matching `/set`'s retry-friendly behavior). But
because baseline now equals the current config, the *next* identical commit
gesture (re-confirm the same inline edit, re-toggle to the same value,
re-pick the same picker value) diffs empty against that rolled-forward
baseline and `flushChanges` bails out **before ever calling
`config.SaveProjectConfig` again**. There was no way to retry a failed
browser save short of making an unrelated change first — unlike `/set`,
which has a `configSavePending` flag (`internal/app/tui/model.go:344-359`)
that forces a retry on the next no-op `/set` of the same value.

### What changed

- `internal/app/tui/settings/browser.go`: added a `savePending bool` field
  on `BrowserPanel` (self-contained, since `BrowserPanel` has no access to
  `Model`), mirroring `configSavePending`. `flushChanges` gained a
  `commitAttempted bool` parameter; when the diff is empty but
  `commitAttempted && b.savePending`, it now retries `SaveProjectConfig`
  instead of returning early. On any real attempt (fresh diff or retry),
  `b.savePending` is set from the resulting `saveErr`. A successful retry
  emits a receipt (`"<pendingKey> persisted"`, matching `/set`'s
  `"✓ %s persisted · %s"` phrasing for its own no-diff-retry path) since
  there's no new diff to describe.
- `internal/app/tui/settings/browser.go`: `Update`'s dispatch switch now
  tracks `committed := list.Committed()` after each branch that forwards a
  key to `fieldList`/`paneStack`, and passes it into `flushChanges`.
  `handlePickerPicked` always passes `true` (picking a value is inherently
  a commit gesture).
- `internal/app/tui/settings/fieldlist.go`: added a `committed bool` field
  to `fieldList`, reset to `false` at the top of every `Update` call and
  set to `true` only at the point a field setter *succeeds*: toggle
  (`space` key and `Enter`-on-toggle via `openRow`), enum cycle
  (`cycleEnum`, left/right), scalar edit confirm (`updateEdit`'s `enter`),
  enum inline-dropdown pick (`updatePick`'s `enter`), collection add
  (`updateAdd`'s `enter` and the no-prompt `a` path), and paste (`p`).
  Pure navigation (`up`/`down`/`g`/`G`) and filter typing never set it.
  Exposed via `fieldList.Committed()`.

This precisely distinguishes "an explicit commit gesture was attempted"
from "the cursor moved" or "a character was typed into the filter box",
which is what lets the retry fire only on repeated commits and never on
navigation.

### Tests

- **`TestBrowserRetriesFailedSaveOnRepeatedCommit`** (new,
  `internal/app/tui/settings/browser_test.go`): opens the inline edit on
  `agent.max_retries`, sets it to `"9"`, confirms — save fails (parent path
  is a file, not a directory). Fixes the underlying problem (removes the
  blocking file, creates a real directory), then **re-confirms the same
  inline edit with the same value `"9"`** (which the in-memory config
  already holds from the failed attempt, so it diffs empty against the
  rolled-forward baseline). Asserts the retry emits `ChangedMsg{SaveErr:
  nil}` with a non-empty `Receipts`, and that the file now exists.
  - **Verified failing first**: stashed `browser.go`/`fieldlist.go`,
    confirmed the test failed with `confirming an inline edit must emit a
    command` (the second commit produced a nil `cmd`, i.e. the early-return
    no-op), restored the fix, confirmed pass.
- **`TestBrowserPendingSaveDoesNotRetryOnNavigationOrFilterTyping`** (new):
  after a failed toggle save leaves `savePending` set, fixes the blocking
  path, then sends `Down`, `Up`, and a filter keystroke. Asserts none of
  these produce a `ChangedMsg` (nav keys must return a nil `cmd`; filter
  typing may still return textinput's own cursor-blink `tea.Cmd`, so the
  assertion there is specifically "not a `ChangedMsg`") and that
  `savePending` stays `true` and no file is written — confirming the
  explicit "no retry on pure navigation" constraint holds.

Both pass; full `internal/app/tui/settings` suite (98 tests) passes.

## Finding 2: reload error synchronization in `handleSetCommand`

### What was wrong

`internal/app/tui/model.go:360-368` (base commit), the `handleSetCommand`
default case's reload-error path:

```go
m.setReg = nil
if err := m.configReloader(reg.Config()); err != nil {
	sys(fmt.Sprintf("✗ %s saved, but live reload failed: %v", key, err))
	return
}
```

versus the `settings.ChangedMsg` handler's equivalent branch
(`internal/app/tui/model.go:583-593`):

```go
m.setReg = nil
if err := m.configReloader(msg.Cfg); err != nil {
	// The runtime has already swapped cfg before cleanup can fail.
	// Keep all TUI-derived state aligned with that live config.
	m.applyNewConfig(msg.Cfg)
	...
```

`config.SaveProjectConfig` had already succeeded by this point in both
paths, and — per the `ChangedMsg` handler's own comment — the reloader
swaps the new config into the live runtime *before* a later cleanup step
can fail. `handleSetCommand` invalidated `m.setReg` (so the next `/set`
would rebuild its registry) but left `m.state.Config` pointing at the
*old*, pre-`/set` config. The next `/set` (or `/settings` open) would
rebuild `settingsRegistry()` from that stale `m.state.Config`, and a save
from that rebuilt registry would silently revert the change that had
already been persisted to disk and swapped into the runtime.

### What changed

`internal/app/tui/model.go`: added `m.applyNewConfig(reg.Config())` on the
reload-error path, exactly matching the `ChangedMsg` handler's fix and
comment:

```go
m.setReg = nil
if err := m.configReloader(reg.Config()); err != nil {
	// The runtime has already swapped cfg before cleanup can
	// fail (same contract as the settings.ChangedMsg handler).
	// Keep all TUI-derived state aligned with that live config.
	m.applyNewConfig(reg.Config())
	sys(fmt.Sprintf("✗ %s saved, but live reload failed: %v", key, err))
	return
}
```

`applyNewConfig` sets `m.state.Config`, clears `m.setReg`/`m.setPopup`, and
reloads the theme — the same invalidation the success path already
performs.

### Tests

- **`TestSetCommandReloadFailureSyncsStateConfig`** (new,
  `internal/app/tui/model_test.go`): a fake `configReloader` that always
  errors; runs `/set shell.allow_network on`; asserts
  `m.state.Config.Tools.Shell.AllowNetwork` is `true` after the failure,
  and cross-checks it against a fresh `config.Load` of the on-disk project
  config to confirm they agree (the save itself succeeded before the
  reloader ran).
  - **Verified failing first**: ran the test before the fix — failed with
    `m.state.Config must reflect the saved value after a reload failure`.
    Applied the fix, confirmed pass.
- **`TestSetCommandReloadFailureDoesNotReportSuccess`** (pre-existing,
  updated): this test's own assertion encoded the *old*, buggy contract
  (`if m.state.Config....AllowNetwork { t.Fatal(...) }` — asserting the
  config must **not** have been applied). That's precisely the behavior
  this finding says is wrong, so I flipped the assertion to require the
  config *is* applied, added a comment pointing at the new focused test,
  and left its other assertions (`m.setReg` invalidated, no `✓` in the
  message, "live reload failed" text present) unchanged since those were
  already correct and unaffected by the fix.

## Finding 3: dock height-contract — diagnosis and outcome

### Investigation

Traced the height-budget chain: `dock.MaxRows(frameHeight) = max(frameHeight*2/5, 6)`
(floor 6, always) → `dock.Host.View` passes that as `maxHeight` to
`Panel.View(width, maxHeight)` → the two `dock.Panel` implementations are
`settings.BrowserPanel.View` and, when a picker is opened directly as the
dock's panel (`model.go:2144-2145` `openPicker`, `model.go:2247-2248`
`openSDDPlanPicker`), `picker.Model.View` itself.

`chrome.Panel(title, content, w, h, ...)` (`internal/app/tui/chrome/chrome.go:19`)
always emits **exactly `h` rows when `h >= 2`** (1 top-border row + `h-2`
body rows + 1 bottom-border row, clamped/truncated as needed) — but when
`h < 2`, `innerH` clamps to `0` and it still emits the top and bottom
border rows unconditionally, i.e. **2 rows regardless of `h`**. So any
caller that lets `h` (or the `maxHeight` fed into a `min(natural+2,
maxHeight)` computation) drop below 2 gets an output that exceeds the
requested budget.

`BrowserPanel.View` already guards this (added in the base commit,
cec6064): `if maxHeight < 3 { return "" }`, applied *before* the
`b.pickerModel != nil` forwarding branch, so it protects both the
browser's own body and any picker nested inside it. I confirmed
mathematically and with `TestBrowserViewHonorsMaxHeight` (pre-existing,
covers maxHeight ∈ {0,1,2,3,4,6}, both with and without a nested picker)
that once `maxHeight >= 3`, `panelHeight = min(contentHeight+2, maxHeight)`
is always ≥ 3, so `chrome.Panel` is never invoked with `h < 2` from this
path. **This part of the base commit's fix is correct and sufficient.**

`picker.Model.View`, however, is used **directly** as a `dock.Panel` (not
only wrapped by `BrowserPanel`) at `model.go:2145` and `model.go:2248`, and
it had **no equivalent guard**. I wrote a test calling it directly at the
same maxHeight values `TestBrowserViewHonorsMaxHeight` uses, before making
any fix:

```
=== RUN   TestViewHonorsMaxHeight
    picker_test.go:157: View(80, 0) rendered 3 rows
    picker_test.go:157: View(80, 1) rendered 3 rows
    picker_test.go:157: View(80, 2) rendered 3 rows
--- FAIL: TestViewHonorsMaxHeight (0.00s)
```

This **is** a genuine, reproducible violation of the `dock.Panel` contract
("renders at most maxHeight rows"). It is not reachable through the
*current* production call sites, because `dock.MaxRows` always returns
≥ 6 (the floor applies unconditionally, independent of `frameHeight`) and
`BrowserPanel` already guards its own forwarding at `< 3`. But
`picker.Model` is itself a first-class, directly-registered `dock.Panel`
implementation with no defense of its own — any future direct caller
(or a change to `MaxRows`'s floor) would silently regress into rendering 2–3
rows over budget. Given the interface's doc comment is an explicit
per-implementation promise ("`View` renders at most maxHeight rows...at
width"), I fixed it rather than leaving it as a latent gap.

I also checked the second reviewer question — whether `dock.Host.Rows()`
is read stale relative to the panel about to be drawn. In
`internal/app/tui/view.go:69-70`, `dockView := m.dock.View(m.width,
m.height)` runs *before* `m.updateViewportHeight()`, and `dock.Host.View`
updates `h.rows` as part of that same call — so `dockRows()` is **fresh**
(this render's actual dock height) by the time the viewport is sized in
the same `viewString()` call, and `dockView` is later appended to `rows`
verbatim. This is *more* correct than `sddPanelRows()`'s pattern (which is
read at `view.go:70`, one statement before `m.sddPanelBody,
m.sddPanelCachedRows = renderSDDPanel(...)` is computed at `view.go:84` —
a genuine one-render lag, but the plan explicitly treats that as accepted
existing behavior for Task 1 to mirror, not something to fix here). I
found no divergence in the dock's own resize/refresh wiring that causes an
over-budget render.

### What changed

`internal/app/tui/picker/picker.go`: added the same guard `BrowserPanel`
already has, at the top of `View`:

```go
func (m *Model) View(maxW, maxH int) string {
	// chrome.Panel always emits at least its top and bottom border rows, so
	// it cannot honor a height budget under 3 (mirrors the identical guard
	// in settings.BrowserPanel.View, the other dock.Panel implementation).
	if maxH < 3 {
		return ""
	}
	...
```

### Tests

- **`TestViewHonorsMaxHeight`** (new, `internal/app/tui/picker/picker_test.go`):
  calls `picker.Model.View(80, maxHeight)` directly for maxHeight ∈
  {0,1,2,3,4,6} (mirroring `settings.TestBrowserViewHonorsMaxHeight`).
  Failed at 0/1/2 before the fix (shown above); passes after.
- **`TestBrowserViewHonorsMaxHeightWhileDrilled`** (new,
  `internal/app/tui/settings/browser_test.go`): drills into the
  `providers` collection (`b.stack != nil` branch, previously uncovered by
  `TestBrowserViewHonorsMaxHeight`) and re-runs the same maxHeight sweep.
  This **passes without any code change** — confirming the drilled-in
  branch was already safe (the `panelHeight = min(contentHeight+2,
  maxHeight)` bound applies uniformly regardless of whether `body` comes
  from the flat list or `b.stack.top().list`).

**Conclusion**: cec6064 fully and correctly fixed `BrowserPanel.View`'s own
height contract, including its drilled-in and nested-picker branches (no
fix needed there, coverage added to prove it). The one real remaining gap
was `picker.Model.View`'s missing defensive floor when used as a
standalone `dock.Panel` — fixed with the same guard, proven by a
test that failed before the fix and passes after.

## Verification

```
gofmt -w .          # clean on all touched files (reverted an unrelated
                     # pre-existing gofmt drift in internal/app/runtime_test.go
                     # that gofmt -w . also touched, out of scope)
go vet ./...         # two pre-existing, unrelated warnings in
                     # internal/app/session/session.go (sync.Once copies),
                     # present on the base commit too — not introduced by
                     # this change
go test ./internal/... # all packages pass
```

Full `go test ./internal/...` output: all `ok`, no `FAIL`.

## Commits

1. `1c24707` — `fix(settings): retry failed browser saves on repeated commit gestures`
   — Finding 1 (`browser.go`, `fieldlist.go`, `browser_test.go`)
2. `a3dadf1` — `fix(tui): sync state.Config on /set reload failure` — Finding 2
   (`model.go`, `model_test.go`)
3. `78b9bf0` — `fix(picker): honor dock.Panel's maxHeight contract below 3 rows` —
   Finding 3 (`picker.go`, `picker_test.go`, `browser_test.go` coverage
   addition)

Base commit: `cec6064` (`fix(tui): retry unsaved browser settings`).
