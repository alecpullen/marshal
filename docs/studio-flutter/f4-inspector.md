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
- Ember has no blue, magenta or cyan. Before the Terminal tab is built, add
  a terminal-only set of ANSI colours to design-system.md by its §10, or
  map them to existing tokens. This is undecided.
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
