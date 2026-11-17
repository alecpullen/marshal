# F3 plan: the agent thread — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: `main` at `11957d83`, which contains F0. Runs after
[the F2 plan](2026-11-17-f2-shell-plan.md). Re-check the names it and
[the F1 plan](2026-11-17-f1-client-plan.md) declare before starting.

Approved design: [f3-agent-thread.md](../f3-agent-thread.md), including
"What F0 found". Finding:
[transcript-selection.md](../findings/transcript-selection.md), whose spike
is `spikes/transcript_selection/`. Rules:
[design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only. The mock's `thread.dart` and `composer.dart` are the
visual reference.

## Goal

- An agent thread shows its transcript live; the user prompts, steers,
  stops, changes mode and answers asks in place.
- Conversation text can be selected across rows while it streams, and
  copied.
- The new-work dialog starts an agent.

## Non-goals

- The inspector. Work items, plan runs, "Plan first", "Work item" (drawn
  planned). Voice and attachments (drawn disabled).

## Assumptions

- The stack types are hand-written in `web/ui/src/lib/stack.ts`; the bridge
  serves `GET /api/sessions/{id}/stack` and relays patches on
  `GET /api/events?sessionId=`; `internal/viewmodel` defines the tree and
  its JSON projection.
- Session routes exist for `prompt`, `steer`, `cancel`, `mode`, `load`,
  `access`, `roster`, `nodes/{nodeId}`; spawn is `POST /api/agents` with
  `contract.SpawnRequest`.
- Geist lacks ⌘ ⌥ ⏎ ✓ ✗ ▸ ▾ ⋯.

## Decisions this plan makes beyond the spec

- Selection is model-backed, as the finding recommends. Live tool output
  streams from the existing tool-call updates, as the Svelte client does.
- The symbol fallback is Noto Sans Symbols 2, bundled under its OFL licence
  from Google's `notofonts/symbols` release, as a fallback family in
  `tokens.dart`.
- Markdown in narration uses the `markdown` package for parsing only; this
  plan renders the parsed nodes with kit text roles, so no third-party
  widget sets a style.
- The performance budget: scrolling a 5,000-node transcript in the
  performance test builds no frame over 32 ms on the CI runner.

## Execution

- Branch `f3-agent-thread`. Commit each task as
  `f3-agent-thread: task N — <title>`, explicit paths only.
- After any change under `web/bridge/contract`, regenerate both clients
  with the two commands in `AGENTS.md`.

## Contracts

`web/bridge/contract/stack.go` (Task 1) declares, field for field and with
the same JSON keys, the wire types `web/ui/src/lib/stack.ts` declares today:
`WireThought`, `WireStep`, `WireTodoLabel`, `WireCall`, `WireRunning`,
`WireTool`, `WireCompletion`, `WirePlan`, `WireMessage`, `WireThinking`,
`WireRunEvent`, `WireBand`, `WireTask`, `WireBandEntity`, `WireReceipt`,
`WireNode`, `StackSnapshot` and `StackPatch`. The bridge relays these as
raw JSON from the agent (`internal/acp/stack.go`, `internal/viewmodel`), so
the Go declarations are checked by a fixture recorded from a real session,
not by a shared type.

`web/bridge/contract/session.go` declares `SessionSummary`,
`PromptRequest`, `SteerRequest`, `ModeRequest`, `SessionAccess` and
`Roster`, each with the JSON the matching handler in `web/bridge/http.go`
reads or writes today.

```dart
// model/stack_store.dart
class StackStore extends ChangeNotifier {
  StackStore(BridgeClient client, String sessionId);
  int get rev;  List<String> get roots;  WireNode? node(String id);
  String plainText(String nodeId);            // the projection selection copies from
  void start(); void stop();
}

// model/thread_view_state.dart
enum Density { outline, comfortable, full }
class ThreadViewState extends ChangeNotifier {
  Density density; String? cursor; bool following; String draft;
  bool folded(String nodeId); void toggleFold(String nodeId);
}

// model/selection.dart
class TextAnchor { String nodeId; int offset; }
class TranscriptSelection extends ChangeNotifier {
  TextAnchor? base; TextAnchor? extent;
  String copy(StackStore store, List<String> order);
  void selectWord(TextAnchor at); void selectLine(TextAnchor at); void selectAll(List<String> order);
}

// model/composer_model.dart
class ComposerModel extends ChangeNotifier {
  bool get running; bool get readOnly; String? get readOnlyReason;
  Future<void> send(String text);   // steers when running, prompts otherwise
  Future<void> stop();  Future<void> setMode(String mode);
}

// additions to BridgeClient
Future<StackSnapshot> stack(String sessionId);
EventStream sessionEvents(String sessionId);
Future<void> prompt(String sessionId, PromptRequest r);
Future<void> steer(String sessionId, SteerRequest r);
Future<void> cancel(String sessionId);
Future<void> setMode(String sessionId, ModeRequest r);
Future<SessionAccess> access(String sessionId);
Future<SpawnResult> spawn(SpawnRequest r);
```

Kit additions (Task 3): `TurnPrompt`, `StepHeader`, `ToolRow`, `Narration`,
`OutputBlock`, `LiveRow`, `Composer`.

## Task 1 — stack and session types into the contract

**Files:** `web/bridge/contract/stack.go`, `session.go` (new), the bridge
files that declare these shapes today, `web/ui/src/lib/stack.ts`,
`test/contract/stack.json`, `session.json`, both generated files.

1. Declare the types named in Contracts so their JSON equals what is sent
   now. Where the bridge has its own declaration of one, alias it to the
   contract type, as `web/bridge/registry.go` aliases `Decision`.
2. Record `test/contract/stack.json` from a real session on the fixture
   bridge: one snapshot and three patches covering a tool call, a
   completion and a removal.
3. Regenerate. `stack.ts` re-exports the generated types and keeps its
   functions.
4. Add the fixtures to F1's two round-trip tests.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd web/ui && npm
run check && npm test`; `cd app && flutter test test/client/`.

## Task 2 — stack store and view state

**Files:** `app/lib/src/model/stack_store.dart`,
`thread_view_state.dart`, `app/lib/src/client/bridge_client.dart`,
`app/test/model/stack_store_test.dart`, `thread_view_state_test.dart`,
`app/test/fixtures/stack/*.json` (recorded sequences).

1. Apply a patch only when its base revision equals the store's; otherwise
   refetch the snapshot and discard the patch. Refetch on a stream gap and
   after a reconnect.
2. Record two real snapshot-and-patch sequences from a session on the
   fixture bridge and check them in.
3. Tests: `snapshot loads`, `in-order patch applies`,
   `out-of-order patch refetches`, `removal removes`,
   `reconnect refetches`, `recorded sequence ends equal to its final
   snapshot`, `fold override survives a patch`.

**Verify:** `cd app && flutter test test/model/`.

## Task 3 — transcript rows and the symbol font

**Files:** `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/` (seven new files, `kit.dart`),
`app/lib/src/ui/tokens.dart`, `app/assets/fonts/`, `app/pubspec.yaml`,
`app/lib/src/ui/gallery/gallery.dart`, `app/test/kit/`, goldens.

1. Add the seven components to design-system.md §5, each with its one job
   and its rules, and the symbol fallback to §3.
2. Build them from tokens; add each to the gallery in every state; add a
   kit test each; regenerate goldens on Linux.
3. Extend `app/web/flutter_bootstrap.js` handling if needed so the web
   build still fetches nothing from another host.

**Verify:** `cd app && flutter test`; `flutter build web -t
lib/gallery_main.dart` and confirm, as F0's record describes, that the page
requests only its own host.

## Task 4 — the thread screen

**Files:** `app/lib/src/ui/thread/thread_screen.dart`, `thread_header.dart`,
`transcript.dart`, `app/lib/src/ui/shell/placeholder.dart`,
`app/test/ui/thread/transcript_test.dart`, `following_test.dart`,
`browse_keys_test.dart`, `screens.dart`.

1. A lazily built list over `StackStore`, drawing each node with the row
   for its kind at the current density.
2. Following: at the end, stay at the end; scrolling up shows one "Jump to
   live" control with the live headline.
3. Browse keys from the spec, read from one key map that tooltips also use.
4. The header as the spec describes; Share is disabled with a tooltip.
5. Tests: `renders each node kind`, `follows while at the end`,
   `stops following on scroll up`, `jump to live resumes`, one test per
   browse key, `density cycles`, `fold hides children`.

**Verify:** `cd app && flutter test`.

## Task 5 — selection

**Files:** `app/lib/src/model/selection.dart`,
`app/lib/src/ui/thread/selection_layer.dart`,
`app/test/model/selection_test.dart`,
`app/test/ui/thread/selection_test.dart`.

1. Port the spike's approach C: pointer hit-testing to a `TextAnchor`,
   highlight spans per row, auto-scroll at the edges, copy from
   `StackStore.plainText` in list order with a newline between rows.
2. Add double-click word, triple-click line, shift-click extend, select
   all, the copy shortcut and a context `Menu` with Copy.
3. Selection passes through `Narration`, `OutputBlock`, `ToolRow` and
   `TurnPrompt`.
4. Tests: `drag across rows copies in order`,
   `selection survives rows arriving`,
   `selection spans rows that are not built`, `word`, `line`,
   `shift extend`, `select all copies every row`, and, in Chrome,
   `drag selects in a browser`.

**Verify:** `cd app && flutter test && flutter test --platform chrome
test/ui/thread/selection_test.dart`.

## Task 6 — composer and asks in place

**Files:** `app/lib/src/model/composer_model.dart`,
`app/lib/src/ui/thread/thread_composer.dart`,
`app/test/model/composer_model_test.dart`,
`app/test/ui/thread/composer_test.dart`, `asks_in_place_test.dart`.

1. `send` steers when a turn is running and prompts otherwise; the draft is
   kept per thread in `ThreadViewState`.
2. A read-only session replaces the composer with its reason.
3. An ask from F2's `InboxModel` for this thread is drawn as `AskCard` at
   the step that raised it.
4. Tests: `prompts when idle`, `steers when running`, `stop cancels`,
   `mode chip posts the mode`, `draft survives a reconnect`,
   `read-only shows the reason`, `answering in the thread clears the inbox`.

**Verify:** `cd app && flutter test`.

## Task 7 — the new-work dialog

**Files:** `app/lib/src/model/new_work_model.dart`,
`app/lib/src/ui/new_work/new_work_dialog.dart`, tests, `screens.dart`.

1. Prompt first; chips defaulted from the project, each a `Menu`; Issues,
   Recent prompts and Recipes fill the prompt. "Plan first" and "Work item"
   are disabled with a tooltip naming F6.
2. Start posts `SpawnRequest` and opens the new thread. `⌘N` opens it.
3. Tests: `defaults come from the project`, `an issue fills the prompt`,
   `start posts the request and opens the thread`,
   `planned kinds are disabled`.

**Verify:** `cd app && flutter test`.

## Task 8 — performance, end to end, record

**Files:** `app/test/ui/thread/performance_test.dart`,
`app/integration_test/f3_test.dart`, the spec.

1. The performance test under Decisions.
2. End to end on the fixture bridge: start an agent from the dialog, steer
   it, answer one approval, let it finish; the app's transcript text equals
   the session's plain-text projection.
3. Implementation record.

**Verify:** `cd app && flutter analyze && flutter test && flutter test
integration_test -d linux && dart format --output=none --set-exit-if-changed
lib test`; `CGO_ENABLED=1 go test ./web/... -count=1`.

## Done when

- A full session is driven from the app with the TUI attached to the same
  session and both showing the same steps.
- Cutting the connection mid-turn loses and duplicates nothing.
- Text selected across steps while output streams copies readably, on Linux
  and in Chrome.

## Needs a Mac

- Select with a trackpad: drag, double and triple click, shift-click, the
  context menu.

## What later plans rely on

- `StackStore`, `ThreadViewState`, `TranscriptSelection`, the seven rows,
  and `thread_screen.dart` as an embeddable widget (F6 embeds it).
