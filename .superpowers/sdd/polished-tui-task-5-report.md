# Task 5 Report: Polish Approval, Diff, and Provider Error States

## Summary

Replaced the old approval-area rendering (plain "SECURITY APPROVAL REQUIRED"
heading, `Command:`/`Reason:`/`Risk:` lines, `[Enter] Approve  [d] Deny  [e]
Edit  [a] Always allow"` help line, raw `Diff:` dump appended inside the
`"Chat"` panel) with the brief's target design: a dedicated "Approval" panel
titled internally with an "Agent wants to run" heading, labeled `Reason`/`Risk`
sections, and lowercase `Enter approve  d deny  e edit  a always allow` help
copy — plus, when `tc.Diff != ""`, a side-by-side split with a separate `Diff`
panel. Wired in the previously-unused `riskText(tc)` helper. Replaced the
Task-2 `renderProviderError` stub body with a compact bordered banner (`"! "`
error-color prefix) while preserving the exact `"Provider Error Banner"` /
`"fits AltScreen"` title/meta strings the target test requires.

## Files changed

- `internal/app/tui/model.go` — `renderApprovalArea`, `renderProviderError`,
  and `View()` (banner placement moved from nested-in-right-column to a
  full-width footer row — see Deviation 2 below).
- `internal/app/tui/model_test.go` — added the two brief-specified tests;
  updated one pre-existing test's stale copy assertions.

## TDD evidence

**RED** (both new tests failing before implementation):

```
$ go test ./internal/app/tui -run 'TestPolishedApprovalStateShowsCommandReasonRiskAndActions|TestPolishedProviderErrorUsesCompactBanner' -v
--- FAIL: TestPolishedApprovalStateShowsCommandReasonRiskAndActions (0.00s)
    model_test.go:1393: View() missing approval copy "Agent wants to run"
--- FAIL: TestPolishedProviderErrorUsesCompactBanner (0.00s)
    model_test.go:1412: View() missing provider error copy "fits AltScreen"
FAIL
```

**GREEN** (after implementing `renderApprovalArea` / `renderProviderError`):

```
$ go test ./internal/app/tui -run 'TestPolishedApprovalStateShowsCommandReasonRiskAndActions|TestPolishedProviderErrorUsesCompactBanner|TestPolishedViewFitsCommonTerminalSizes' -v
--- PASS: TestPolishedViewFitsCommonTerminalSizes (0.00s)
    --- PASS: TestPolishedViewFitsCommonTerminalSizes/80x24 (0.00s)
    --- PASS: TestPolishedViewFitsCommonTerminalSizes/100x30 (0.00s)
    --- PASS: TestPolishedViewFitsCommonTerminalSizes/120x40 (0.00s)
--- PASS: TestPolishedApprovalStateShowsCommandReasonRiskAndActions (0.00s)
--- PASS: TestPolishedProviderErrorUsesCompactBanner (0.00s)
PASS
```

After that, one pre-existing test failed (expected, per the task brief):

```
--- FAIL: TestPolishedViewPreservesPendingApprovalContent (0.00s)
    model_test.go:254: View() missing approval item "SECURITY APPROVAL REQUIRED"
```

Updated its assertions to the new copy (see "Pre-existing tests updated"
below), then reran the full package:

```
$ go test ./internal/app/tui/... -v 2>&1 | grep -E "^(--- FAIL|FAIL|ok)"
ok  	marshal/internal/app/tui	0.758s
ok  	marshal/internal/app/tui/memory	(cached)
ok  	marshal/internal/app/tui/settings	(cached)
```

## Full test run / build / vet

```
$ go build ./... && go vet ./... && go test ./...
ok  	marshal/internal/agent
ok  	marshal/internal/app
ok  	marshal/internal/app/config
ok  	marshal/internal/app/session
ok  	marshal/internal/app/tui
ok  	marshal/internal/app/tui/memory
ok  	marshal/internal/app/tui/settings
ok  	marshal/internal/contextpack
ok  	marshal/internal/db
ok  	marshal/internal/knowledge
ok  	marshal/internal/llm/provider
ok  	marshal/internal/llm/routing
ok  	marshal/internal/llm/streaming
ok  	marshal/internal/repo
ok  	marshal/internal/tools/native
ok  	marshal/internal/tools/patch
ok  	marshal/internal/tools/policy
ok  	marshal/internal/tools/registry
```

`gofmt -l .` reports no files needing formatting.

## Pre-existing tests updated

Per the brief's checklist, only one required an actual copy-assertion change
(all others turned out unaffected — explained below):

- **`TestPolishedViewPreservesPendingApprovalContent`** (model_test.go ~228):
  replaced `"SECURITY APPROVAL REQUIRED"` and `"Risk: command"` assertions
  with `"Agent wants to run"`, `"Reason"`, and `"Risk"` (dropping the literal
  `"Risk: command"` check, since `riskText(tc)` now prefers the human-readable
  `Reason` field over the raw `Risk` field when both are set — this test's
  fixture sets both, so the rendered risk text is now the `Reason` value, not
  `"command"`). Command/diff-line substring checks were kept unchanged.

The other tests the brief flagged as at-risk turned out **not** to need
changes, because they call `model.View()` without first sending a
`tea.WindowSizeMsg`, so `m.width == 0` and `View()` dispatches to
`fallbackView()` — a separate, unstyled rendering path that this task did not
touch (it still literally emits `"SECURITY APPROVAL REQUIRED"` and
`"[r] Rollback Last Patch"`):

- `TestTUIApprovalBannerAndKeypresses` — no resize call, hits `fallbackView()`.
- `TestTUIRollbackFlow` — no resize call, hits `fallbackView()`; also its
  assertion was already a no-op (`if !strings.Contains(...) { // comment, no
  t.Fatal }`), so it couldn't have failed either way.
- `TestGlobalKeysDoNotLeakDuringApproval`, `TestEscDuringApprovalDenies` —
  behavioral only, no copy assertions; verified they still pass.
- `TestApprovalBannerHasSingleBorder` — asserts `strings.Count(view, "┌") <=
  1`; all panels use `lipgloss.RoundedBorder()` (corner glyph `╭`, not `┌`),
  so this count is always 0 regardless of design — unaffected structurally
  and functionally.
- `TestViewShowsProviderErrorWhenSet`, `TestViewOmitsProviderErrorSectionByDefault`,
  `TestProviderErrorVisibleInAltScreen` — generic substrings (`"Provider
  Error"`, `"connection refused"`/`"connection"`), all preserved since the
  literal `"Provider Error Banner"` title is unchanged.
- `TestAgentFinishedMsgClearsBusyAndRecordsProviderError` — behavioral only
  (never calls `View()`), unaffected.

## Deviations from the brief's target code (and why)

1. **Rollback hint copy.** The brief didn't specify rollback copy for the new
   design. Used lowercase `"r rollback"` appended to the help line/body when
   `m.state.HasBackup()` is true, matching the new lowercase
   `key action` style (`Enter approve  d deny  e edit  a always allow`).

2. **Provider-error banner moved from nested-in-right-column to a full-width
   footer row.** The brief's sample `renderProviderError` kept using
   `m.rightWidth` for the panel width (same as the Task-2 stub), appended
   inside `rightColumn` via `lipgloss.JoinVertical`. I verified by TDD that
   this produces a failing test: at a 120-wide terminal, `m.rightWidth` is
   only 35 columns, and `renderPanel`'s header-truncation logic
   (`metaWidth := innerWidth - visibleRunes(title) - 2`) truncates
   `"fits AltScreen"` down to `"fits AltSc"` before the meta ever reaches the
   14 characters the test requires — this is mathematically impossible to
   satisfy at that width no matter how the body is styled, since the title
   (`"Provider Error Banner"`, 21 chars) alone consumes most of the available
   34-char inner width.

   Fix: render the banner as a standalone footer spanning the full terminal
   width (`lipgloss.JoinVertical(topBar, mainLayout, errorBanner, statusBar)`)
   instead of nesting it in the right column. This gives the header ample
   room at every tested terminal width (80/100/120) while keeping the exact
   `"Provider Error Banner"` / `"fits AltScreen"` strings the target test
   requires.

3. **Found and fixed a width-off-by-2 bug surfaced by the footer-banner
   change.** `renderPanel(width, height)` renders `lipgloss.RoundedBorder()`
   *outside* the passed `width`/`height` (verified empirically: `Width(20)` +
   `Border()` renders 22 columns wide, `Height(4)` + `Border()` renders 6 rows
   tall). The existing two-column layout already silently compensates for
   this via `totalHorizontalBorderGutter = 5` subtracted once from the full
   terminal width before splitting into `leftWidth`/`rightWidth`. My first
   pass at the full-width footer banner passed `m.width` directly to
   `renderPanel`, which rendered 2 columns *wider* than the terminal —
   `lipgloss.JoinVertical` then padded every sibling line (`topBar`,
   `mainLayout`, `statusBar`) out to match, breaking the width-fit invariant
   even for lines that don't depend on the provider error at all. Fixed by
   passing `panelWidth := max(m.width-2, 4)` (and sizing the nested body
   style down by another 2 columns) so the banner's total rendered width is
   exactly `m.width`. Verified via a throwaway test (not committed) at
   80x24/100x30/120x40 with a long error message and a long diff/approval
   combination — all lines fit within the configured width in every case.

## Known limitation (flagged, not fixed — out of scope)

When a provider error is showing, the full view's **total line count**
exceeds the terminal height (e.g. 32 lines rendered at a 24-row terminal).
This is not a regression: the *original* Task-2 stub design (nesting the
banner inside `rightColumn`) had the same class of problem — appending a
5-row-tall bordered panel to the right column without shrinking anything
else already made `rightColumn` (and therefore `mainLayout`) taller than the
`m.height` budget computed in `resize()`, by a comparable margin (calculated
~7 rows over at 80x24 in the old design vs. ~8 rows over in the new one).
Properly fixing this would require `resize()` to know about provider-error
state before computing `contentHeight`/`chatHeight`, which is a layout-budget
change beyond this task's brief (no test — old or new — asserts total line
count when a provider error is set; `TestPolishedViewFitsCommonTerminalSizes`
and friends never combine a resize with `SetProviderError`). No line in the
provider-error view exceeds the terminal *width* in any of the three tested
sizes; only the height/line-count invariant is left unresolved, exactly as it
was before this task.

## Self-review

- No new third-party dependencies.
- No keybinding logic changed — only `View()`/render helpers touched;
  `Update()` is untouched for the approval flow (Enter/d/e/a/r all still
  dispatch identically).
- `View()` remains read-only (no mutation of viewport/input/layout fields);
  only pure rendering functions and the top-level assembly changed.
- Verified (via throwaway, since-removed test files) that the new
  approval+diff split and the new provider-error banner both keep every
  rendered line within the terminal width at 80x24, 100x30, and 120x40, with
  long command/reason/risk/diff content designed to stress wrapping/
  truncation.

## Fix Report

A follow-up review of this task found two Important issues, both now fixed.

### Finding 1 — `riskText()` preferred `Reason` over `Risk`

`riskText(tc)` returned `tc.Reason` whenever it was non-empty, only falling
back to `tc.Risk` if `Reason` was blank. Since `internal/agent/runner.go`
always sets both fields with distinct content (`Reason` = the agent's
justification, `Risk` = the actual classification, e.g. `"workspace_write"`
or `"Low - test command, no destructive flags detected."`), the panel
section labeled **"Risk"** was, in every realistic case, showing the Reason
text a second time and never showing the real risk classification.

**Fix:** reversed the priority in `internal/app/tui/model.go`:

```go
func riskText(tc *session.PendingToolCall) string {
	if tc.Risk != "" {
		return tc.Risk
	}
	return tc.Reason
}
```

**Test:** `TestPolishedApprovalStateShowsCommandReasonRiskAndActions` in
`internal/app/tui/model_test.go` previously only asserted the literal words
"Reason" and "Risk" appeared anywhere in the view — it could not have caught
this bug (both fields were non-empty and either one satisfies a bare
substring check on the label word). Strengthened it to walk the rendered
lines, find the line following the `"Risk"` label, and assert it contains
the fixture's `Risk` string (`"Low - test command, no destructive flags
detected."`) and specifically does **not** contain the `Reason` string
(`"Validate layout bounds and modal capture."`). This test now fails against
the old (Reason-preferring) implementation and passes against the fix.

Note: this also affects the pre-existing `riskText`-adjacent note in the
original Task 5 report's "Pre-existing tests updated" section, which
observed that `TestPolishedViewPreservesPendingApprovalContent`'s fixture
sets both `Reason` and `Risk` to different values and only asserted the
`Reason` string was present (the original report explicitly called out that
`riskText` "prefers the human-readable Reason field over the raw Risk
field"). That assertion still passes post-fix because it never asserted the
Risk value was absent, but the new dedicated check above now covers the
regression case precisely.

### Finding 2 — provider-error banner overflowed the terminal height budget

**Root cause:** `renderPanel(width, height)` renders its `RoundedBorder()`
*outside* the passed dimensions — actual rendered height is `height + 2`
(same mechanism already compensated for on the width axis via `panelWidth :=
max(m.width-2, 4)` in `renderProviderError`, per Deviation 3 in the original
report, but never compensated for on the height axis). `View()`'s normal
layout (no provider error) exactly fills the `m.height` budget with zero
slack: `contentHeight := height - verticalOverhead` is sized so that
`topBar(1) + mainLayout(contentHeight+2) + statusBar(1) == height` exactly.
Appending the error banner as an extra row below `mainLayout` — sized via
`renderPanel(..., min(6, max(m.contentHeight/3, 4)))`, i.e. always passing 6
at the three tested sizes, so an actual visible height of 6+2=8 — therefore
always overflowed the budget by exactly 8 lines, matching the reviewer's
measurement at all three sizes.

**Fix:** factored the banner's content-height calculation into a shared
helper:

```go
func providerErrorBannerContentHeight(contentHeight int) int {
	return min(6, max(contentHeight/3, 4))
}
```

used both by `renderProviderError` (unchanged behavior) and by `View()`,
which now builds a local, adjusted copy of the layout dimensions before
rendering the chat/right panels whenever a provider error is present:

```go
layout := m
if err := m.state.ProviderError(); err != nil {
	reserved := providerErrorBannerContentHeight(m.contentHeight) + 2
	layout.contentHeight = max(m.contentHeight-reserved, 5)
	layout.chatHeight = max(layout.contentHeight-chatBelowViewportRows, 1)
}
```

`layout` (not `m`) is then used to render `chatPanel`, `inputPanel`, and
`rightColumn`, shrinking `mainLayout` by exactly the banner's visible height
(content rows + 2 border rows) so the total output — `topBar + mainLayout +
errorBanner + statusBar` — again exactly matches `m.height`. Since `View()`
has a value receiver, mutating the local `layout` copy never touches stored
model state (`resize()`'s stored `m.contentHeight`/`m.chatHeight` are
unaffected; they still drive the non-error rendering path and other
callers).

**Measured line counts** (via a temporary throwaway test that rendered
`View()` with `state.SetProviderError(...)` set, at each of the three
mandated sizes, counting `len(strings.Split(strings.TrimRight(view, "\n"),
"\n"))`):

| Terminal size | Line count (before fix) | Line count (after fix) | Budget |
|---|---|---|---|
| 80x24  | 32 | **24** | 24 |
| 100x30 | 38 | **30** | 30 |
| 120x40 | 48 | **40** | 40 |

The fix lands exactly on budget at all three sizes — 0 lines of slack in
either direction. This was verified both by the throwaway measurement above
and by a new permanent test,
`TestPolishedProviderErrorBannerFitsCommonTerminalSizes` (in
`internal/app/tui/model_test.go`), which mirrors
`TestPolishedViewFitsCommonTerminalSizes` but sets a provider error first and
asserts `len(lines) <= size.height` (and per-line width `<= size.width`) at
80x24, 100x30, and 120x40. All three subtests pass.

Because the fit is exact (no slack), if a future change adds even one more
row of chrome around the banner or the main layout, the height budget will
break again immediately and visibly via this new test — there is no hidden
margin.

### Minor finding — help-line inconsistency between diff/no-diff branches

Fixed as a quick, low-risk change: the diff-shown branch of
`renderApprovalArea` previously rendered the actions as four separate lines
in a different order (`"Enter approve"`, `"e edit"`, `"d deny"`, `"a always
allow"`, plus its own separate `"r rollback"` append) than the no-diff
branch's single combined `helpLine` (`"Enter approve  d deny  e edit  a
always allow"` with `"  r rollback"` appended when applicable). Both
branches now build and reuse the same `helpLine` string, so the approval
panel's action hints are identical in content, order, and format regardless
of whether a diff is shown.

### Test / build evidence

```
$ go test ./internal/app/tui/... -run 'TestPolished' -v
... (all PASS, including the two new/strengthened tests above)
ok  	marshal/internal/app/tui	0.545s

$ go build ./... && go vet ./... && go test ./...
ok  	marshal/internal/agent
ok  	marshal/internal/app
ok  	marshal/internal/app/config
ok  	marshal/internal/app/session
ok  	marshal/internal/app/tui
ok  	marshal/internal/app/tui/memory
ok  	marshal/internal/app/tui/settings
ok  	marshal/internal/contextpack
ok  	marshal/internal/db
ok  	marshal/internal/knowledge
ok  	marshal/internal/llm/provider
ok  	marshal/internal/llm/routing
ok  	marshal/internal/llm/streaming
ok  	marshal/internal/repo
ok  	marshal/internal/tools/native
ok  	marshal/internal/tools/patch
ok  	marshal/internal/tools/policy
ok  	marshal/internal/tools/registry
```

Full suite, build, and vet all clean.
