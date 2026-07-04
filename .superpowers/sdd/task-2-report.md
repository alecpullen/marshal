What you implemented

- Added `renderModeStrip(active string, width int) string` to render the temporary Ask/Plan/Auto/Swarm mode strip with `Auto` active by default.
- Added `renderStatusBar(width int, state *session.State, busy bool) string` to render the polished status inventory using the Task 1 status styles and active route data.
- Reworked `Model.View()` to compose the shell from a top bar, left chat/input column, right inspector column, and bottom status bar while preserving the early settings and memory overlay returns.
- Added the temporary compile-time stubs required by the brief: `renderChatPanel`, `renderInputArea`, `renderRightInfoPanel`, and `renderProviderError`.
- Added `TestPolishedStatusBarShowsRouteWhenActive` exactly as specified in the brief.

What you tested and test results

- `go test ./internal/app/tui -run TestPolishedStatusBarShowsRouteWhenActive -v`
  - PASS after implementation.
- `go test ./internal/app/tui -run 'TestPolishedViewContainsCurrentLayoutChrome|TestPolishedStatusBarShowsRouteWhenActive|TestPolishedViewFitsCommonTerminalSizes' -v`
  - `TestPolishedViewContainsCurrentLayoutChrome`: PASS
  - `TestPolishedStatusBarShowsRouteWhenActive`: PASS
  - `TestPolishedViewFitsCommonTerminalSizes`: FAIL at 80x24, 100x30, and 120x40 because the recomposed shell is 2 lines taller than the current terminal budget. This matches the brief's note that bounds may still fail until Task 3 tightens vertical budgeting.
- `go test ./internal/app/tui`
  - FAIL. In addition to the terminal-fit failures above, pre-existing tests that assert the earlier AltScreen/status/approval layout now fail because Task 2 intentionally swaps the shell over to the temporary polished stubs before later tasks replace them.

TDD Evidence: RED and GREEN commands/output summaries

- RED: `go test ./internal/app/tui -run TestPolishedStatusBarShowsRouteWhenActive -v`
  - Failed as expected. `View()` was missing `"Auto"` and still rendered the old `project=... cwd=... local-only=...` status bar.
- GREEN: `go test ./internal/app/tui -run TestPolishedStatusBarShowsRouteWhenActive -v`
  - Passed after adding `renderModeStrip`, `renderStatusBar`, and recomposing `View()`.

Files changed

- `internal/app/tui/model.go`
- `internal/app/tui/model_test.go`
- `.superpowers/sdd/task-2-report.md`

Self-review findings

- The implementation matches the helper names and verbatim values from the Task 2 brief.
- The change is intentionally stub-driven: chat, inspector, and provider-error rendering are temporary placeholders so Task 2 compiles independently.
- `renderStatusBar` correctly reflects active route role/model/provider/locality and falls back to `local` when remote providers are disabled.
- `View()` now routes shell composition through the new helpers and no longer assembles the old two-panel/status layout inline.

Concerns, if any

- `TestPolishedViewFitsCommonTerminalSizes` still fails because the recomposed shell currently exceeds the height budget by two lines at the tested sizes.
- The broader `./internal/app/tui` test suite still contains expectations for the older shell and approval rendering, so package tests remain red until the later tasks update those renderers and associated tests.

Task 2 fit-fix follow-up

- Root cause investigation:
  - The first extra line came from the top bar: `"● ● ●"` was rendered inside `Width(3)`, which forced a wrap.
  - The second extra line came from the left-column vertical budget: `chatBelowViewportRows` still assumed a 3-row area below the chat viewport, but the temporary Task 2 `renderInputArea()` stub renders a 3-row bordered input box plus a 1-row help line.
- Fix implemented:
  - Increased the top-bar traffic-light segment width from `3` to `5` and adjusted the centered title width from `m.width-28` to `m.width-25` so the top bar stays on one line.
  - Updated `chatBelowViewportRows` from `3` to `4` to match the temporary bordered input stub used in Task 2.
- Fresh test results:
  - `go test ./internal/app/tui -run 'TestPolishedViewContainsCurrentLayoutChrome|TestPolishedStatusBarShowsRouteWhenActive|TestPolishedViewFitsCommonTerminalSizes' -v`
    - PASS
    - `TestPolishedViewContainsCurrentLayoutChrome`: PASS
    - `TestPolishedStatusBarShowsRouteWhenActive`: PASS
    - `TestPolishedViewFitsCommonTerminalSizes`:
      - `80x24`: PASS
      - `100x30`: PASS
      - `120x40`: PASS
- Follow-up concerns:
  - I did not rerun the broader `go test ./internal/app/tui` package sweep in this follow-up because the user asked specifically to clear the focused Task 2 review gate. The earlier report still reflects that wider suite remained red against later-task expectations at that point in time.

Review-fix follow-up

- Findings addressed:
  - Restored pending approval rendering in the polished `View()` path by making `renderChatPanel(tc)` render approval details and diff content when a pending tool call exists.
  - Restored tab-dependent right-panel behavior by making `renderRightInfoPanel(tc)` switch on `activeTab` and render the prior Plan / Context / Log bodies again inside the Task 2 helper structure.
  - Added focused regression tests for pending approval visibility and tab switching.
- Implementation details:
  - `renderChatPanel(tc)` now uses `tc` to render:
    - `SECURITY APPROVAL REQUIRED`
    - command / reason / risk inventory
    - approval key hints
    - diff content when `tc.Diff` is present
  - `renderRightInfoPanel(tc)` now uses `m.activeTab`:
    - Plan tab shows current-plan copy and pending-approval/busy/idle state
    - Context tab shows empty-state or context-pack summary
    - Log tab shows audit-log entries or the empty-state
  - This also removes the temporary unused-parameter smell for `tc`.
- TDD evidence:
  - RED: `go test ./internal/app/tui -run 'TestPolishedViewPreservesPendingApprovalContent|TestPolishedRightPanelTracksActiveTab' -v`
    - Failed because the polished shell did not render approval content and the right panel ignored `activeTab`.
  - GREEN: `go test ./internal/app/tui -run 'TestPolishedViewPreservesPendingApprovalContent|TestPolishedRightPanelTracksActiveTab' -v`
    - PASS after restoring approval and tab-dependent rendering.
- Fresh test results:
  - `go test ./internal/app/tui -run 'TestPolishedViewContainsCurrentLayoutChrome|TestPolishedStatusBarShowsRouteWhenActive|TestPolishedViewFitsCommonTerminalSizes' -v`
    - PASS
  - `go test ./internal/app/tui -run 'TestPolishedViewPreservesPendingApprovalContent|TestPolishedRightPanelTracksActiveTab|TestPolishedViewContainsCurrentLayoutChrome|TestPolishedStatusBarShowsRouteWhenActive|TestPolishedViewFitsCommonTerminalSizes' -v`
    - PASS

Re-review follow-up

- Findings addressed:
  - Restored a real right-panel tab strip with visible active state in the polished shell.
  - Preserved multi-line diff visibility in pending approvals instead of collapsing `tc.Diff` into a single truncated segment.
  - Tightened the focused tests so they prove active tab chrome and multi-line diff visibility.
- Implementation details:
  - `renderRightInfoPanel(tc)` now renders a compact tab strip above the panel body.
    - Active tab is shown with an explicit `›` marker and accent-colored bold text.
    - Inactive tabs use the muted styling.
    - The strip wraps between tab items when the inspector width is narrow, so labels like `3 Log` remain readable as a unit.
  - `renderChatPanel(tc)` now splits `tc.Diff` on newlines and appends each diff line separately, truncating per line rather than truncating the entire diff blob as one string.
- Test updates:
  - `TestPolishedViewPreservesPendingApprovalContent` now verifies multiple diff lines are visible, including `--- a/app.go`, `+++ b/app.go`, and `+added line`.
  - `TestPolishedRightPanelTracksActiveTab` now verifies the active tab chrome appears for Plan, Context, and Log views.
- Fresh test results:
  - `env GOCACHE=/private/tmp/codex-gocache go test ./internal/app/tui -run 'TestPolishedViewPreservesPendingApprovalContent|TestPolishedRightPanelTracksActiveTab' -v`
    - PASS
  - `env GOCACHE=/private/tmp/codex-gocache go test ./internal/app/tui -run 'TestPolishedViewContainsCurrentLayoutChrome|TestPolishedStatusBarShowsRouteWhenActive|TestPolishedViewFitsCommonTerminalSizes|TestPolishedViewPreservesPendingApprovalContent|TestPolishedRightPanelTracksActiveTab' -v`
    - PASS
