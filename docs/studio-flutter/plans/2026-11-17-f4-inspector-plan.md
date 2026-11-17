# F4 plan: the inspector — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: `main` at `11957d83`, which contains F0. Runs after
[the F3 plan](2026-11-17-f3-agent-thread-plan.md). Re-check the names the
F1–F3 plans declare before starting.

Approved design: [f4-inspector.md](../f4-inspector.md), including "What F0
found". Finding: [terminal.md](../findings/terminal.md), whose spike is
`spikes/terminal/`. Rules: [design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only. The mock's `inspector.dart` is the visual reference.

## Goal

- An agent's work is read, gate-checked, commented on and shipped as a pull
  request from its thread.
- A selected step is inspected; a terminal and a preview open in the
  agent's workspace.

## Non-goals

- Forge check state on pull requests (F5). Work-item and plan-run tabs
  (F6). Editing files.

## Assumptions

- `web/bridge/contract/finish.go` already declares `DiffFile`, `DiffResult`,
  `MergeResult`, `CommitDraft`, `GateResult`, `GateRecord`, `ExitResult`,
  `ExitRequest`, `MergeRequest`.
- The routes under `/api/agents/{id}/` exist for `diff`, `gate`, `verify`,
  `commit-draft`, `exit`, `merge`, `discard`, `patch`, `review/comments`
  (and `…/{cid}/resolve`), `files`, `file`, `terminal` (with
  `{tid}/events`, `input`, `resize`, `release`, delete) and
  `preview/{port}`; and under `/api/sessions/{id}/` for `step-diffs`,
  `nodes/{nodeId}`, `last-request`.
- The terminal event stream carries base64 output chunks, as the finding
  records.
- `termBlue`, `termMagenta`, `termCyan` exist in `tokens.dart` and may be
  referenced only from a file named `terminal_view.dart`.

## Decisions this plan makes beyond the spec

- Split diff is not built in this phase; unified only.
- Preview on desktop lists the ports with Open in browser; in the browser
  it is a frame. An embedded desktop web view is not attempted here.
- Syntax highlighting uses the `re_highlight` package for tokenising only
  (confirm it resolves on the pinned Flutter in Task 6; if not, use
  `highlight`, which has the same token classes);
  colours come from a fixed map of token classes to Ember tokens (`fg`,
  `muted`, `ok`, `warn`, `accent`) declared in `tokens.dart`.
- The terminal uses `xterm` pinned to exactly 4.0.0, fed through a
  streaming UTF-8 decoder.

## Execution

- Branch `f4-inspector`. Commit each task as
  `f4-inspector: task N — <title>`, explicit paths only.
- Regenerate both clients after any contract change.

## Contracts

`web/bridge/contract/review.go`, `files.go`, `terminal.go`, `inspect.go`
(Task 1) declare, with today's JSON: `ReviewComment`,
`ReviewCommentRequest`, `FileEntry`, `FileContent`, `TerminalOpened`,
`TerminalChunk`, `TerminalInput`, `TerminalResize`, `StepDiff`,
`NodeDetail`, `LastRequest`.

```dart
// model/inspector_state.dart
enum InspectorWidth { collapsed, docked, expanded }
enum InspectorTab { changes, files, terminal, preview, inspect }
class InspectorState extends ChangeNotifier {
  InspectorWidth width; InspectorTab tab; bool pinned; String? selectedNode;
  void cycleWidth(); void select(String? nodeId); void backToLive();
}

// model/changes_model.dart
enum ShipOutcome { pullRequest, pushed, gateRefused, forgeRefused, patch }
class ShipResult { ShipOutcome outcome; String? url; String message; }
class ChangesModel extends ChangeNotifier {
  GateResult? gate; List<DiffFile> files; bool byStep; String? selectedFile;
  Set<String> viewed; List<ReviewComment> comments; CommitDraft? draft;
  Future<void> runGate();
  Future<void> comment(String path, int line, String body);
  Future<void> resolve(String commentId);
  Future<ShipResult> ship({required String message, required bool openPr, String? overrideReason});
  Future<void> discard();  Future<String> exportPatch();
}

// model/files_model.dart
class FilesModel extends ChangeNotifier {
  List<FileEntry> children(String path); Future<void> expand(String path);
  Future<FileContent> open(String path); bool changed(String path);
}

// model/terminal_session.dart
class TerminalSession extends ChangeNotifier {
  Stream<String> get output; bool get agentPaused;
  Future<void> open(); void input(String data); void resize(int cols, int rows);
  Future<void> handBack(); Future<void> close();
}

// model/inspect_model.dart
class InspectModel extends ChangeNotifier { NodeDetail? detail; LastRequest? lastRequest; }
```

Kit additions (Task 3): `DiffView`, `GateList`. `terminal_view.dart`
(Task 7) lives under `ui/inspector/` and is the one file that imports
`xterm`.

## Task 1 — remaining types into the contract

**Files:** `web/bridge/contract/review.go`, `files.go`, `terminal.go`,
`inspect.go` (new), the bridge files that declare these shapes, fixtures
under `test/contract/`, the Svelte files that hand-write them, both
generated files.

1. Declare the types in Contracts with today's JSON; alias the bridge's own
   declarations; re-point the Svelte client; add fixtures and regenerate.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd web/ui && npm
run check && npm test`; `cd app && flutter test test/client/`.

## Task 2 — the inspector frame

**Files:** `app/lib/src/model/inspector_state.dart`,
`app/lib/src/ui/inspector/inspector.dart`,
`app/lib/src/ui/thread/thread_screen.dart`,
`app/test/ui/inspector/frame_test.dart`, `screens.dart`.

1. The column at 400 docked; expanded takes the window and narrows the
   conversation to step headlines with the composer; `⌘\` cycles. Below
   1080 it is a `Sheet` opened from the header.
2. Selecting a node in the transcript calls `select`; a line at the top
   names the node with "Back to live". A pinned tab ignores selection.
3. Width and tab are part of `Place`'s query so a link restores them.
4. Tests: `cycles three widths`, `sheet below 1080`,
   `selection switches to the node`, `pinned tab stays`,
   `place restores tab and width`.

**Verify:** `cd app && flutter test`.

## Task 3 — `DiffView` and `GateList`

**Files:** `docs/studio-flutter/design-system.md`, `app/lib/src/ui/kit/
diff_view.dart`, `gate_list.dart`, `kit.dart`, gallery, kit tests, goldens.

1. Add both to design-system.md §5. `DiffView`: unified lines in `mono`,
   removed on `diffRemoved`, added on `diffAdded`, a gutter that takes a
   comment. `GateList`: one row per check with a status icon, name and
   detail.
2. Gallery specimens in every state; goldens regenerated on Linux.

**Verify:** `cd app && flutter test`.

## Task 4 — Changes: read

**Files:** `app/lib/src/model/changes_model.dart`,
`app/lib/src/ui/inspector/changes_tab.dart`,
`app/test/model/changes_model_test.dart`,
`app/test/ui/inspector/changes_test.dart`.

1. Gate first with Run gate; file list with counts and the all / by step
   switch (by step groups by `StepDiff`); the selected file's diff; viewed
   marks kept in the model.
2. With nothing selected, the file the agent is editing is selected.
3. Tests: `gate shows each check`, `run gate posts verify`,
   `by step groups hunks`, `viewed marks persist across tabs`,
   `follows the edited file`.

**Verify:** `cd app && flutter test`.

## Task 5 — Changes: comment and ship

**Files:** `changes_model.dart`, `app/lib/src/ui/inspector/ship_sheet.dart`,
`changes_tab.dart`, `app/test/ui/inspector/ship_test.dart`.

1. A line comment posts and shows its thread; the agent's reply appears;
   Resolve resolves.
2. The ship `Sheet`: editable drafted message, open a pull request, Ship.
   When the gate has failed, the action is "Override and ship" and requires
   a non-empty reason.
3. Each `ShipOutcome` has its own result view with the next step. Discard
   confirms and names what is lost. Export patch saves the patch.
4. Tests: `comment posts as steering`, `resolve resolves`,
   `ship posts message and pr flag`,
   `a failed gate requires a reason`, one test per `ShipOutcome`,
   `discard confirms`.

**Verify:** `cd app && flutter test`.

## Task 6 — Files

**Files:** `app/lib/src/model/files_model.dart`,
`app/lib/src/ui/inspector/files_tab.dart`, `tokens.dart` (the highlight
map), tests.

1. A lazy tree; a marker with a tooltip on changed files; a read-only
   viewer in `mono` with highlighting per Decisions.
2. Tests: `expands lazily`, `marks changed files`,
   `a path outside the workspace is refused`.

**Verify:** `cd app && flutter test`.

## Task 7 — Terminal

**Files:** `app/lib/src/model/terminal_session.dart`,
`app/lib/src/ui/inspector/terminal_view.dart`, `terminal_tab.dart`,
`app/pubspec.yaml`, tests.

1. Open posts the terminal route and follows its event stream, decoding
   chunks through a streaming UTF-8 decoder.
2. The first keystroke sets `agentPaused`; the tab shows it with Hand back;
   two minutes without input hands back automatically (time injected).
3. `terminal_view.dart` themes `xterm` from tokens: the eight ANSI colours
   as design-system.md §2 maps them, `mono`, `bg`.
4. Tests: `a character split across chunks is intact`,
   `typing pauses the agent`, `hand back resumes`,
   `two minutes idle hands back`, `resize posts the size`.

**Verify:** `cd app && flutter test && flutter test --platform chrome
test/ui/inspector/terminal_test.dart`.

## Task 8 — Preview and Inspect

**Files:** `app/lib/src/model/preview_model.dart`, `inspect_model.dart`,
`app/lib/src/ui/inspector/preview_tab.dart`, `preview_frame_web.dart`,
`inspect_tab.dart`, tests.

1. Preview per Decisions.
2. Inspect: actor, why, timing, sandbox, approval, call id; tabs Output,
   Arguments, Thinking, Model request.
3. Tests: `lists declared ports`, `inspect shows the node's facts`,
   `model request loads on demand`.

**Verify:** `cd app && flutter test && flutter build web`.

## Task 9 — end to end, record, full verification

**Files:** `app/integration_test/f4_test.dart`, the spec.

1. On the fixture bridge: an agent makes a change; the app runs the gate,
   comments on a line and sees the reply, then ships to the fixture forge.
2. Implementation record.

**Verify:** `cd app && flutter analyze && flutter test && flutter test
integration_test -d linux && dart format --output=none --set-exit-if-changed
lib test`; `CGO_ENABLED=1 go test ./web/... -count=1`.

## Done when

- A change is reviewed, corrected by the agent and shipped from the app.
- A failing gate cannot be shipped without a reason, and the reason is in
  the audit log.
- Typing in the terminal pauses the agent; handing back resumes it.

## Needs a Mac

- Terminal: paste, input methods, mouse selection and copy.

## What later plans rely on

- `InspectorState` takes its tab set from the thread kind (F6 adds two).
- `ChangesModel`, `DiffView`, `GateList`, and `changes_tab.dart` reused by
  work items.
