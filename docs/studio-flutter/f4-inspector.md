# F4 — the inspector

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`, ahead of F0–F3. Part of [the Studio programme](programme.md).

Read first: [design.md](design.md) §2, [design-system.md](design-system.md),
`findings/terminal.md`, `docs/web-studio/design.md` §8.5 and §8.6,
`web/bridge/contract/finish.go`, `web/bridge/exit.go`, and in the Svelte
client: `lib/dock/`, `lib/review/`, `lib/ExitPanel.svelte`,
`lib/GateResult.svelte`, `lib/DiffView.svelte`, `lib/diff.ts`, `lib/exit.ts`,
`views/Review.svelte`, with their tests.

**Before starting.** F3 has landed. Re-read the files above at the current
`main`.

## What F0 found

From [findings/terminal.md](findings/terminal.md):

- Use the `xterm` package at exactly 4.0.0. It passed automated tests on
  macOS and in Chrome for keys, resize, colours, cursor movement, wrap,
  scrollback and the alternate screen. It was last published in February
  2024; pin it, and vendor it under its MIT licence if it becomes a
  problem.
- It takes strings, so decode the stream with a streaming UTF-8 decoder: a
  character split across two chunks is otherwise corrupted.
- Ember has no blue, magenta or cyan. The terminal-only set of ANSI colours
  exists in design-system.md §2 ("Terminal colours") and `tokens.dart`; use
  it only in `terminal_view.dart`.
- Not verified: mouse selection and copy, paste, input methods, heavy
  output, and a live stream from a bridge.

## Context

The inspector is the right-hand column of a thread. It holds what the
Svelte Studio spreads across the session dock and the Review page: what the
agent changed, whether it passes the gate, how to ship it, the files, a
terminal, a preview, and the detail of one step.

The routes it uses, all under `/api/agents/{id}/`: `diff`, `gate`, `verify`,
`commit-draft`, `exit`, `merge`, `discard`, `patch`, `review/comments` and
`review/comments/{cid}/resolve`, `files`, `file`, `terminal` with
`terminal/{tid}/events`, `input`, `resize`, `release` and its delete, and
`preview/{port}`. Under `/api/sessions/{id}/`: `step-diffs`,
`nodes/{nodeId}` and `last-request`.

`web/bridge/contract/finish.go` already holds the diff, gate, commit-draft,
exit and merge types. The review-comment, file-tree, terminal, preview,
step-diff, node-detail and last-request types are hand-written TypeScript.

The gate is a hard build-and-test check. Overriding it requires a reason,
which is recorded. Git credentials never enter the agent's container: the
bridge commits, pushes and opens the pull request.

## Goal

- An agent's work can be read, checked, commented on and shipped as a pull
  request without leaving its thread.
- A step selected in the conversation can be inspected.
- The user can open a terminal in the agent's workspace and see its
  running app.

## Non-goals

- Pull-request and CI state from the forge. That is F5; here a shipped
  thread shows the pull request's link and nothing about its checks.
- The work-item and plan-run tab sets. F6.
- Editing files. The Files tab is read-only.

## Decisions

1. **Contract first.** Move the remaining types named above into
   `web/bridge/contract`, switch the bridge to them, regenerate both
   clients, add fixtures.
2. **Tabs for an agent thread**, in this order: Changes, Files, Terminal,
   Preview, Inspect. The tab and the inspector's width state are part of
   the place, so a link restores them.
3. **Three width states.** Collapsed, docked at 400, and expanded. Expanded
   gives the inspector the window and narrows the conversation to an
   outline of step headlines, with the composer still at its foot. `⌘\`
   cycles them. Below the medium breakpoint the inspector is a `Sheet`
   opened from the thread header.
4. **Following.** With nothing selected the inspector follows the agent:
   Changes shows the file being edited, Terminal tails the running command.
   Selecting a node in the conversation switches it to that node; a small
   line at the top says which, with "Back to live". A pinned tab ignores
   selection changes.
5. **Changes.**
   - Gate first: each check with its result and when it ran, and a Run gate
     action.
   - Then the file list with added and removed counts, and a switch between
     all changes and grouped by step.
   - Then the diff of the selected file. Unified by default, split when the
     inspector is expanded and wide enough. Files can be marked viewed;
     that mark is client state.
   - A comment on a line is sent to the agent as steering. The thread it
     starts shows the agent's reply and can be resolved.
   - The kit gains `DiffView` and `GateList` by the design system's §10.
6. **Shipping** lives in the Changes tab's footer and is one flow in a
   `Sheet`: the drafted commit message, editable; whether to open a pull
   request; then Ship. If the gate has failed, Ship is replaced by Override
   and ship, which requires a typed reason. Discard and Export patch are
   plain actions beside it; Discard confirms and names what is lost. Local
   merge is offered only where the project's ship target allows it.
7. **Every ship outcome is shown as it is.** Pushed with a pull request,
   pushed without one, refused by the gate, refused by the forge, and a
   patch for a read-only source are five distinct results, each with what
   to do next.
8. **Files.** A lazily loaded tree confined to the workspace, with a marker
   on files this thread changed, and a read-only viewer with syntax
   highlighting from a package chosen and pinned in this phase.
9. **Terminal.** Follow `findings/terminal.md`. Opening it starts a shell in
   the agent's workspace. **Typing pauses the agent**; the tab shows that
   and a Hand back action, and the agent resumes on hand back or after two
   minutes without input. A selected command step shows that command's
   replay above the live shell.
10. **Preview.** The agent's declared ports, each loaded through the
    bridge's preview route. In the browser it is a frame. On desktop it is an
    embedded web view if this phase finds one that works on macOS and
    Linux, recording the finding in `findings/preview.md`; otherwise the
    tab lists the ports with Open in browser, and says why.
11. **Inspect.** For the selected node: actor, why, timing, sandbox,
    approval, call id; then tabs for output, arguments, thinking and the
    last model request.
12. **Nothing here fetches from a widget.** `model/changes_model.dart`,
    `files_model.dart`, `terminal_session.dart`, `preview_model.dart` and
    `inspect_model.dart` own the data; the tabs draw them.

## Work

Suggested pull requests, in order.

1. **Contract.**
2. **Inspector frame.** The column, its three states, the sheet, tabs,
   following and pinning, with placeholder tab bodies.
3. **Changes: read.** Gate, file list, diff, by-step grouping, viewed marks.
4. **Changes: comment and ship.** Line comments, the ship sheet, every
   outcome, discard, patch.
5. **Files.**
6. **Terminal.**
7. **Preview.**
8. **Inspect.**
9. **End to end.** An agent makes a change; the app runs the gate, comments
   on a line and sees the agent respond, then ships to the fixture forge.

## Tests

- `web/bridge/contract`: fixtures for each moved type.
- `app/test/model/`: one test file per model named in decision 12, covering
  the gate-override rule, each ship outcome, the pause-on-typing rule and
  its two-minute release, and by-step grouping.
- `app/test/ui/inspector/`: `frame_test.dart` (states, sheet, following,
  pinning), `changes_test.dart`, `ship_test.dart`, `files_test.dart`,
  `terminal_test.dart`, `preview_test.dart`, `inspect_test.dart`.
- Every tab registers with the tooltip, states and overflow tests.

## Done when

- An agent's change is reviewed, commented on, corrected by the agent and
  shipped as a pull request from the app.
- A failing gate cannot be shipped without a reason, and the reason is in
  the audit log.
- Typing in the terminal pauses the agent and handing back resumes it.
- Selecting a step shows its detail and that step's part of the diff.
- All of it works at the narrow width through the sheet, and in the browser
  at `/next/`.

## Open questions

- Whether split diff is worth building in this phase or should wait for
  evidence it is wanted.
- Whether an embedded preview is achievable on Linux desktop.

## Implementation record

Status: **implemented** on `studio/f4-inspector`, tasks 1–9, one commit each.
Every Verify command was run; the environment-blocked ones are listed at the
end, separately from real failures.

### What landed, task by task

1. **Contract.** `web/bridge/contract/{review,files,terminal,inspect}.go`
   declare the eleven types the plan names, plus the supporting ones the wire
   needs to be expressed at all: `FileList`, `StepDiffs`,
   `NodeDetailResponse`, `ModelRequest` and its six request types,
   `CallDetail`'s five detail types. The bridge aliases `ReviewComment` and
   `ReviewCommentRequest` and uses the terminal's types directly; fixtures
   live in `test/contract/{review,files,terminal,inspect}.json` and both
   clients are regenerated and current. The Svelte client is re-pointed at
   the generated types, and two latent bugs fell out of that: `getLastRequest`
   and `getStepDiffs` returned the route's wrapper (`{request}`, `{steps}`) as
   if it were the value.
2. **The frame.** `InspectorState` (three widths, five tabs for an agent, a
   pin, the followed node) and `Inspector`, laid out by `ThreadScreen` at 400
   docked, as an outline plus the window when expanded, and as a `Sheet` below
   1080. `⌘\` cycles the width; `i` closes and opens the column. Width and tab
   travel in `Place`'s query (`w`, `t`), so a link restores them.
3. **`DiffView` and `GateList`** in the kit, documented in §5, with gallery
   specimens in every state, kit tests, and goldens regenerated on Linux.
   `DiffSide` is `before`/`after` (`new` is a Dart keyword) and carries the
   wire's `old`/`new`.
4. **Changes, read.** `ChangesModel` reads the diff, the gate, the draft, the
   steps and the comments; splits the diff per file; groups by step from
   `StepDiff`; holds the viewed marks; and follows the file the agent is
   editing. `ChangesTab` draws the gate, the files and the diff, with the
   five §7 states.
5. **Changes, comment and ship.** A line comment posts through the model and
   its thread is listed with Resolve; `shipSheet` carries the message, the
   pull-request choice, the override reason a failed gate requires, and one
   view per `ShipOutcome`; Discard confirms and names what is lost.
6. **Files.** `FilesModel` reads a directory at a time and refuses a path
   outside the workspace before it is sent; `FilesTab` draws the tree with a
   tooltip'd marker on changed files and a read-only viewer tokenised by
   `re_highlight`, coloured from a fixed class map in `tokens.dart`.
7. **Terminal.** `TerminalSession` opens the shell, decodes its stream through
   one long-lived UTF-8 decoder, holds the agent on the first keystroke, hands
   back on demand or after the injected idle wait, and posts keystrokes and
   resizes. `terminal_view.dart` is the one file that imports `xterm` 4.0.0
   and the one that uses `termBlue`/`termMagenta`/`termCyan`.
8. **Preview and Inspect.** `PreviewModel` issues a URL per declared port;
   the tab lists them and shows the selected one as its URL with Copy link,
   `InspectModel` reads a node's detail and, on demand, the newest model
   request; `InspectTab` shows actor, why, timing, sandbox, approval and call
   id, then Output, Arguments, Thinking and Model request.
9. **End to end.** `app/integration_test/f4_test.dart` drives the whole flow
   against the fixture bridge; this record.

### Decisions the plan left open

- **`ExitRequest.noPr` (a contract change).** The bridge already had
  `ExitOptions.NoPR`, withheld from the wire, so "open a pull request" had no
  field to travel in. It is added as `noPr` with `omitempty`: absent means
  open one, so today's JSON and older clients are unchanged.
- **`ReviewComment.resolvedAt` is `*time.Time` with `omitempty`.** The wire's
  `omitzero` cannot be expressed by either generator; the JSON is identical.
- **The gate's checks.** The record carries one outcome, so the gate is one
  row today; `GateList` is built for the many F5 will bring.
- **Kit additions beyond the plan's list**, each documented in §5:
  `RowItem.mono`, `RowItem.selected` (the file list's mono, selectable rows)
  and `NavRow.mono` (the file tree's path labels).
- **Export patch shows and copies the patch** rather than writing a file: the
  app's lanes have no `dart:io` in `ui/` or `model/`, so saving needs a new
  seam this phase does not add.
- **The override's actor.** The app sends `by: ""` for a gate override,
  exactly as the web client's `{reason}` does; neither fills the actor.
- **Preview ports come from the caller**, because the bridge has no route
  that lists them. On a desktop the tab shows the URL with Copy link and says
  why it is not framed; there is no URL launcher dependency in this phase.
- **`FilesModel.open` returns the model's `FileText`**, and `InspectModel`
  exposes `InspectFacts`, so `ui/` never imports `client/` (the boundary test
  is the reason).
- **Keystrokes are sent base64-encoded but unchunked.** The wire's cap is
  64 KiB per body, and a keystroke string is far below it.

### Not built in this phase

- The selected command's replay above the live shell (decision 9); the
  Terminal tab's `planned` row says so.
- A side-by-side diff (the plan's decision), and a web view for the preview:
  an iframe needs `dart:ui_web` and `package:web`, which this app's lanes keep
  in `client/*_web.dart`, and putting a widget factory there would put a
  widget in the client lane. The preview shows its URL, with Copy link, and
  says why. Extending the lane is a decision this plan did not make.
- Split diff, file editing, forge check state (F5) and the work-item tab
  sets (F6).

### Environment-blocked, not failures

- `flutter test --platform chrome test/ui/inspector/terminal_test.dart`: no
  working browser here (Flutter's Chrome host stalls on CanvasKit). The
  terminal's behaviour is covered by `test/model/terminal_session_test.dart`.
- `flutter test integration_test -d linux`: no display and no CMake/GTK3. The
  same flow is driven from `test/ui/inspector/{changes,ship}_test.dart`.
- `flutter build linux --debug`: the same missing toolchain.
- `cd web/ui && npm run check`: five errors and five warnings that predate
  this branch (`fleet.test.ts`, `AgentConversation.svelte`, `Run.svelte`,
  `Review.test.ts`, `Runs.test.ts`) and none added by it.
