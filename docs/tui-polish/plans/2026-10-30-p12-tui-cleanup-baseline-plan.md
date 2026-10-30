# P12 TUI cleanup and test baseline — implementation plan

Date: 2026-10-30. Status: proposed; not executed.
Base: `b864c7e81507414c015ea5a9cf4e5c00054a7ee8` on
`p11-ctrl-x-guidance`. **Dependency:** P11 is complete on that branch but is
not yet integrated into `main` at authoring time. Execute this plan only on a
checkout that contains that commit; if the integration rebases or changes
the relevant code, recheck the anchors and baseline before editing.
Requirements: [triage items 13–14](../triage.md#p3--cleanup-and-baseline),
[browse Enter](../audit.md#browse-enter-contains-an-unreachable-subagent-card-branch),
[transcript click target](../audit.md#transcript-click-target-retains-an-unreachable-subagent-branch),
[plan review import](../audit.md#plan-review-has-an-unused-import-retained-by-a-dummy-reference),
and [verification observations](../audit.md#verification-observations).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** remove the remaining unreachable TUI branches, preserve the actual
keyboard and mouse behavior, and make the warning-color test verify the
semantic theme slot instead of an obsolete palette literal.

**Non-goals:** redesign navigation, remove subagent drill-in, change the
default palette, adjust application warning colors, change web UI, or fix
unrelated full-suite failures. This is phase 12 in the sequence and covers the
remaining P3 triage items.

**Verified behavior and decisions:**

- Browse Enter expands the selected transcript node; on a live-box stop it
  uses `enterBoxStop`. Browse `f` drills into the child from a completion
  event, dispatch row or box agent. Existing tests explicitly assert this
  behavior (`TestBrowseEnterExpandsACompletionEvent`,
  `TestBrowseFDrillsFromACompletionEvent`,
  `TestBrowseFDrillsFromTheDispatchRow`, and
  `TestBrowseFOpensAnAgentFromTheBox`). These tests establish the intended
  drill-in contract for the cleanup; no new navigation gesture is introduced.
- Transcript clicks toggle a node; live-box mouse actions remain in
  `handleBoxClick`. The `clickTarget.subagent` field has no producer and its
  handler branch is unreachable. Remove only that branch and field.
- The plan-review dummy-reference finding is obsolete: at the P11 base,
  `sddreview/review.go` has no `var _ = strings.TrimSpace`; `strings.Join`
  and `strings.Split` are live uses. Keep the import and implementation.
  Record this as already resolved, rather than deleting live code.
- The warning test fails in isolation on the current checkout: it expects
  ANSI 172 or 33 while the rendered default `StatusWarning` is ANSI 215.
  `untrustedStyle()` already uses `theme.Current().StatusWarning`. Test that
  the **untrusted text itself** receives the selected semantic warning style
  under multiple explicit themes and that monochrome retains the text, rather
  than checking any unrelated color somewhere in the status line.
- Use the repository's isolated test helpers, restore the global theme in
  `t.Cleanup`, and do not run theme-mutating subtests in parallel. Preserve
  unrelated untracked files. Tasks run sequentially and are independently
  verifiable; stage named paths only.

## Contracts

No exported or cross-task types are introduced. Task 1 changes the existing
private signature to:

```go
func (m *Model) subagentAtCursor() (session.SubagentView, bool)
```

Its existing non-card resolution remains unchanged. Task 2 needs no new API.

## Task 1: Remove unreachable transcript drill-in paths

**Goal:** dead branches disappear while Enter, `f`, transcript clicks and
live-box navigation retain their existing behavior.

**Files:** `internal/app/tui/browse.go`, `internal/app/tui/click.go`.

**Steps:**

1. In `handleBrowseKey`'s `enter` case, remove exactly this unreachable block:

   ```go
   if v, ok := m.subagentAtCursor(true); ok {
       m.drillIntoSubagent(v)
       m.leaveBrowse()
       return *m, nil, true
   }
   ```

   Keep the preceding live-box handling and following expansion/refresh path.
   Change the `f` case call from `m.subagentAtCursor(false)` to
   `m.subagentAtCursor()`. Remove the `onlyCard` parameter, its always-false
   guard, and its obsolete comment from the helper; keep resolution of step
   children, completion events, tool rows and the active transcript source.
2. In `click.go`, remove the unused `marshal/internal/app/session` import,
   `clickTarget.subagent` field, and its obsolete comment. Replace the
   `switch` in `handleTranscriptClick` with its current default body:

   ```go
   m.toggleExpanded(target.node)
   m.invalidateTranscript()
   m.refreshViewport()
   ```

   Retain browse-cursor movement, event consumption, live-region wheel
   routing, and the separate live-box click handler. Do not touch
   `sddreview/review.go`.

**Verify:**

```bash
go test ./internal/app/tui -run '^Test(BrowseFDrillsFromACompletionEvent|BrowseFDrillsFromTheDispatchRow|BrowseEnterExpandsACompletionEvent|BrowseFOpensAnAgentFromTheBox|BrowseEnterExpandsARunningCard|CompletionClickTogglesItsDisclosure|MouseClickOnABeatShowsItsReasoning|BoxClickTogglesAWorkCard|ClickBeatWhileDrilledIntoSubagent)$' -count=1 -timeout 90s
gofmt -l internal/app/tui/browse.go internal/app/tui/click.go
```

Expected: all named tests pass and formatting prints no paths. These existing
behavior tests cover the retained paths; do not add tests for deleted dead code.

## Task 2: Assert warning styling through the semantic theme

**Goal:** the untrusted-status test follows the selected warning slot and
passes across explicitly controlled themes, including monochrome.

**Files:** `internal/app/tui/status_test.go`.

**Steps:**

1. Replace the hard-coded ANSI checks in
   `TestStatusLineUntrustedIsWarningColored`. Save `theme.Current()` and
   restore it in `t.Cleanup`. Construct the model with `newStatusTestModel`,
   then reload each fixture after model construction so initialization cannot
   replace it. Use `theme.LoadFor(false, "xterm-256color")` for 256-color,
   `theme.LoadFor(false, "xterm")` for 16-color, and
   `theme.LoadFor(true, "xterm-256color")` for monochrome. Add a 256-color
   fixture with `StatusWarning` overridden to a distinct `lipgloss.Color`
   that differs from `FGDefault` and `StatusError`, proving the assertion
   follows the semantic slot rather than a particular built-in palette.
2. For each colored fixture, independently construct the expected token with
   `lipgloss.NewStyle().Foreground(th.StatusWarning).Bold(true).Render("untrusted")`
   and assert that exact styled token occurs in `m.renderStatusLine(100)`;
   do not merely search the complete line for a warning color, since another
   segment could produce it. Also assert visible `untrusted` text. For
   monochrome, assert the visible text remains and the token has no
   foreground-color SGR; bold without color is allowed. Do not use
   `untrustedStyle()` to generate the expectation. Avoid parallel subtests.
3. Keep product code and palette values unchanged. If the test renderer's
   detected color profile makes an explicit 16-color token indistinguishable,
   configure the test renderer deterministically using the existing lipgloss
   test setup; do not accept a literal list of palette colors as the assertion.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestStatusLineUntrustedIsWarningColored$' -count=1 -timeout 45s
go test ./internal/app/tui/... -count=1 -timeout 2m
gofmt -l internal/app/tui/status_test.go
```

Expected: the formerly failing isolated test and TUI suite pass, and formatting
prints no paths. The check must fail if `untrustedStyle()` is changed to use a
different color slot, including under the distinct override fixture.

## Final verification and integration

After both tasks pass, run from the repository root:

```bash
git merge-base --is-ancestor b864c7e81507414c015ea5a9cf4e5c00054a7ee8 HEAD
CGO_ENABLED=1 go build -o /private/tmp/marshal-p12-cleanup ./cmd/marshal
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui/browse.go internal/app/tui/click.go internal/app/tui/status_test.go
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p12-cleanup go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Formatting must print no paths. Compare full-suite failures against the latest
integrated P11 baseline and record actual results. The scripted usability suite
is help-only and does not verify drill-in or mouse actions; cite the focused
tests rather than claiming it does. If a controlling terminal is available,
manually exercise browse Enter, `f`, transcript and live-box clicks at 80×24,
including resize and a multiplexer/SSH session; otherwise record the missing
environments. After verification, update the two dead-branch findings, the
obsolete plan-review import finding, verification observations, and triage
items 13–14 with actual evidence. Commit the named plan and documentation with
the work. Do not mark the full audit clean if other baseline failures remain.

## Plan self-review

- The actual drill-in behavior was checked against current code and existing
  tests; cleanup removes only paths with no producers.
- The obsolete import finding is explicitly resolved without touching its live
  uses. The color regression tests the warning token under explicit themes,
  including a distinct override and monochrome.
- P11 is a declared prerequisite because its branch is not yet in `main`.
  No implementation occurred in planning.
