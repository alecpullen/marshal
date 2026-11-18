# F9.1 — reachable surfaces

The plan for Batch A of [F9](../f9-parity.md): the inspector, share links and
stop are reachable from a shipped thread. Nothing here is a new design;
every screen already exists and already has its tests.

- **Spec**: [../f9-parity.md](../f9-parity.md), Batch A and decisions 2, 4, 8.
- **Authored against**: `walk-main` at `b8bfefd8`.
- **Executed as**: one commit per task, `<plan-slug>: task N — <task title>`.
- **Verification runs on Linux**: `cd app && flutter test …`.

## Non-goals

- Work-item and plan-run inspector tab sets (F6 gave each its own screen;
  F9 decision 8).
- Holding an agent, which has no route (F9 decision 2).
- Any change to what the tab widgets draw: this plan only feeds them.

## Assumptions

- A thread's `id` is both its agent id and its ACP session id, which is what
  `main.dart:132` already assumes when it builds `StackStore` from
  `thread.id`.
- A host's client is reachable in `main.dart` as
  `hosts.hosts.firstWhere((h) => h.name == host).client`, as `threadStore`
  and `composerFor` already do at `main.dart:132-140`.
- `ComposerModel.stop()` (`app/lib/src/model/composer_model.dart:147`)
  posts the cancel the header's Stop means.

## Contracts

Declarations only; every task below uses these and nothing else from another
task.

```dart
// app/lib/src/ui/shell/shell.dart

/// One open thread's inspector: the view state the frame animates, what each
/// tab draws, and how to release both.
class ThreadInspector {
  const ThreadInspector({
    required this.state,
    required this.body,
    this.dispose,
  });

  final InspectorState state;
  final Widget Function(InspectorTab tab) body;

  /// Releases the tab models and the state. Called when the thread place
  /// closes.
  final VoidCallback? dispose;
}

/// Builds a thread's inspector, from the host's client. Null leaves a thread
/// without the panel, which is what every thread had before F9.
typedef InspectorBuilder =
    ThreadInspector Function(String host, model.ThreadRef thread);

// On Shell and _ThreadPlace, beside `newWorkFor` and `pulls`:
//   final InspectorBuilder? inspectorFor;
//   final ShareModel Function(String host, String agentId)? shareFor;
```

```dart
// app/lib/src/model/share_model.dart

/// The status link a thread's Share mints: the one URL the bridge gives out,
/// and the revoke that stops it working.
///
/// It is a model rather than a widget's state so the two rules it has — one
/// link in flight at a time, and a refusal is shown in the bridge's words —
/// are plain tests.
class ShareModel extends ChangeNotifier {
  ShareModel(BridgeClient client, {required this.agentId, DateTime Function()? now});

  final String agentId;

  /// True while a link is being minted or revoked.
  bool get busy;

  /// The link made in this window, and its URL — shown once.
  StatusLinkCreated? get made;

  /// What went wrong, in the bridge's words. Null when nothing has.
  String? get failure;

  Future<void> mint({int hours = 24});
  Future<void> revoke();
}
```

```dart
// app/lib/src/model/thread_inspector_models.dart

/// The five models an agent thread's inspector tabs read, owned by one object
/// so a rebuild does not rebuild them and closing the thread releases them.
class ThreadInspectorModels {
  ThreadInspectorModels(
    BridgeClient client, {
    required String agentId,
    required String sessionId,
    String root = '',
    int subagentId = 0,
  });

  final ChangesModel changes;
  final FilesModel files;
  final TerminalSession terminal;
  final PreviewModel preview;
  final InspectModel inspect;

  void dispose();
}
```

```dart
// app/lib/src/ui/inspector/host.dart

/// What an inspector tab draws for an agent thread: the tab widget, given the
/// model it reads. `ui/` may name these; only `main.dart` may build them.
Widget inspectorBody(
  ThreadInspectorModels models,
  InspectorTab tab, {
  StateKind? state,
  String? shipped,
});
```

## Task 1: A thread's tab set comes from the model

**Goal.** `ThreadScreen` and `tabsForThread` cannot disagree about which tabs
a thread kind has, because the screen reads the model's answer.

**Files.**
- `app/lib/src/ui/thread/thread_screen.dart`
- `app/test/ui/thread/inspector_tabs_test.dart` (new)

**Steps.**

1. In `thread_screen.dart:355`, replace the body of `_inspectorTabs` — the
   inline `switch` over `model.ThreadKind` — with:

   ```dart
   List<InspectorTab> get _inspectorTabs =>
       model.tabsForThread(widget.thread.kind);
   ```

   Rewrite its doc comment to say the set is the thread kind's, declared by
   `tabsForThread` (F4 decision 2, F6 decision 6). `thread_screen.dart`
   imports the model as `model`, as `model.ThreadKind` at that line already
   shows.

2. Nothing else in the file changes: `_inspectorBelowBreakpoint` (`:367`),
   `_body` (`:376`) and `_panel` (`:565`) already read `_inspectorTabs`.

3. Add `app/test/ui/thread/inspector_tabs_test.dart`. It pumps `ThreadScreen`
   through `EmberApp` (as `app/test/ui/thread/browse_keys_test.dart` does),
   with `store` from `transcriptFixture(…)`, `view: ThreadViewState()`,
   `inspector: InspectorState()`, and `inspectorBody: (_) => const
   SizedBox()`; `thread` comes from `threadRef()` in
   `app/test/ui/thread/support.dart`, which builds an agent thread.
   - `'an agent thread shows the five inspector tabs'`: expect the tab labels
     `Changes`, `Files`, `Terminal`, `Preview`, `Inspect`.
   - `'a work item thread shows its own five tabs'`: build the thread with
     `threadRef()`'s fields and `kind: ThreadKind.workItem`; expect `Plan`,
     `Agents`, `Checks`, `Changes`, `Pull requests`, and no `Files`.

**Verify.** `cd app && flutter test test/ui/thread/inspector_tabs_test.dart test/ui/inspector/frame_test.dart test/ui/thread/browse_keys_test.dart`
— the new test passes, and the frame and keyboard tests still pass.

## Task 2: Stop is in the thread header

**Goal.** A running thread's header menu offers Stop, and choosing it cancels
the turn; Hold is absent, because nothing can hold an agent yet.

**Files.**
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/src/ui/thread/thread_header.dart`
- `app/test/ui/shell/thread_stop_test.dart` (new)

**Steps.**

1. In `_ThreadPlaceState.build` (`shell.dart:1085-1105`, the `ThreadScreen`
   constructor call), add, beside `onNewWork`:

   ```dart
   // Stop ends the turn through the composer's own route. Hold is not
   // passed: no bridge route holds an agent, so the header would offer an
   // item that cannot work (f9-parity.md decision 2).
   onStop: _composer == null ? null : () => unawaited(_composer!.stop()),
   ```

   `dart:async` is already imported for `unawaited` (`shell.dart:642`).

2. In `thread_header.dart`, extend the comment above the `More actions` menu
   (`:123`) with one sentence: `onHold` is null everywhere today because no
   route holds an agent (f9-parity.md decision 2), so Stop is the only item the
   menu can carry; the code already omits a null callback.

3. Add `app/test/ui/shell/thread_stop_test.dart`. Start a `ModelFixture` with
   one running agent on `/home/u/marshal`
   (`ModelFixture.start([FixtureHost(name: 'local', projects: [...], agents:
   [agent(id: 'a-1', status: AgentState.agentRunning, ...)])], now: now)`), as
   `app/test/ui/f2_flow_test.dart:41-72` does. Pump `EmberApp(home: Studio(store:
   fixture.store, inbox: inbox, initialPlace: Place(PlaceKind.thread, host:
   'local', id: 'a-1'), composerFor: ..., threadStore: ...))`.
   - `'the header offers Stop and not Hold'`: open the `More actions` menu and
     expect `find.text('Stop')` to find one, and `find.text('Hold')` none.
   - `'Stop cancels the turn'`: tap Stop and expect the fake bridge to have
     seen `POST /api/sessions/a-1/cancel` (the fixture records requests; assert
     on the recorded path).

**Verify.** `cd app && flutter test test/ui/shell/thread_stop_test.dart test/ui/thread/composer_test.dart`
— both pass, including the existing composer Stop path.

## Task 3: Share mints a status link

**Goal.** The thread header's Share button mints a read-only status link,
shows its URL once, copies it, and can revoke it.

**Files.**
- `app/lib/src/model/share_model.dart` (new)
- `app/lib/src/ui/thread/share_sheet.dart` (new)
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/src/ui/thread/thread_header.dart`
- `app/lib/main.dart`
- `app/test/model/share_model_test.dart` (new)
- `app/test/ui/shell/share_test.dart` (new)

**Steps.**

1. `share_model.dart`: implement `ShareModel` as declared in Contracts.
   `mint` sets `busy`, clears `failure`, awaits
   `client.createStatusLink(agentId, ttlHours: hours)`, stores `made`,
   notifies; a thrown `Object` becomes `failure` through `'$e'` and leaves
   `made` null. `revoke` returns immediately when `made` is null; otherwise
   calls `client.revokeStatusLink(made!.id)`, clears `made`, notifies. Both
   settle `busy` in a `finally`.

2. `share_sheet.dart`: `Future<void> showShareSheet(BuildContext context,
   ShareModel model)` shows a `Sheet` (kit) titled `Share this thread`, with:
   - while `model.busy`, `StateMessage.loading(lines: 2)`;
   - when `model.failure != null`, `StateMessage.fault(message:
     model.failure!, action: StateAction('Try again', onTap: model.mint))`
     — use the same `StateKind`/`StateAction` names the kit exports;
   - when `model.made != null`, the URL in a `Field(readOnly: true,
     controller: …)`, a `Btn('Copy link', icon: Glyphs.copy, onTap: …)` that
     writes it with `Clipboard.setData`, one line naming `expiresAt` as a
     date, and `Btn('Revoke', onTap: model.revoke)`;
   - a footer that says the link shows status only and never the transcript.
   It rebuilds on `model` with `AnimatedBuilder` and closes on Revoke's
   success.

3. `shell.dart`: add `final ShareModel Function(String host, String agentId)?
   shareFor;` to `Shell` and to `_ThreadPlace`, as declared in Contracts. In
   `_ThreadPlaceState.initState`, build it once:
   `_share = widget.shareFor?.call(widget.thread.host, widget.thread.id);` and
   dispose it in `dispose`. Pass
   `onShare: _share == null ? null : () => unawaited(showShareSheet(context, _share!))`
   to `ThreadScreen`.

   Thread ids for a running agent are agent ids, which is what
   `createStatusLink` takes.

4. `thread_header.dart:118`: change the disabled tooltip from `'Share a
   read-only status link: arrives in F7'` to `'Share a read-only status
   link'`, and keep a distinct line for the null case only if the widget still
   needs one (the button is only drawn enabled when `onShare != null`).

5. `main.dart`, beside `newWorkFor` (`:146`), add:
   ```dart
   shareFor: (host, agentId) => ShareModel(
     hosts.hosts.firstWhere((h) => h.name == host).client,
     agentId: agentId,
   ),
   ```
   with the import of `share_model.dart`.

6. Tests.
   - `app/test/model/share_model_test.dart` against a fake
     `BridgeClient`/fake bridge: `'mint stores the one URL'` asserts
     `made!.url` and `busy == false`; `'a refusal is reported in the bridge's
     words'` asserts `failure`; `'revoke clears the link'` asserts the client
     saw `revokeStatusLink('link-1')` and `made` is null.
   - `app/test/ui/shell/share_test.dart` pumps `Studio` over a `ModelFixture`
     (as Task 2 does) with `shareFor`, taps the header's
     `find.byTooltip('Share a read-only status link')`, and expects the URL
     text to appear; then taps `Copy link` and expects no exception.

**Verify.** `cd app && flutter test test/model/share_model_test.dart test/ui/shell/share_test.dart test/settings_access_test.dart`
— the model, the sheet and the existing access screen (which shares the
client method) all pass.

## Task 4: The inspector reaches an agent thread

**Goal.** Opening an agent thread draws the inspector beside the transcript,
with Changes, Files, Terminal, Preview and Inspect drawing real data; `⌘\`
cycles its width and below the medium breakpoint it opens as a sheet.

**Files.**
- `app/lib/src/model/thread_inspector_models.dart` (new)
- `app/lib/src/ui/inspector/host.dart` (new)
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/main.dart`
- `app/test/model/thread_inspector_models_test.dart` (new)
- `app/test/ui/shell/inspector_test.dart` (new)

**Steps.**

1. `thread_inspector_models.dart`: construct, in order,
   `ChangesModel(client, agentId: agentId, sessionId: sessionId)`,
   `FilesModel(client, agentId: agentId, root: root)`,
   `TerminalSession(client, agentId: agentId)`,
   `PreviewModel(client, agentId: agentId)`,
   `InspectModel(client, sessionId: sessionId, subagentId: subagentId)`; keep
   them in the final fields Contracts declares; `dispose()` disposes each in
   the same order. These constructors are the ones the classes already have
   (`changes_model.dart:123`, `files_model.dart:35`,
   `terminal_session.dart:23`, `preview_model.dart:12`, `inspect_model.dart:46`).

2. `host.dart`: `inspectorBody` switches on the tab and returns
   `ChangesTab(model: models.changes, state: state, shipped: shipped)`,
   `FilesTab(model: models.files, state: state)`,
   `TerminalTab(session: models.terminal, state: state)`,
   `PreviewTab(model: models.preview, state: state)`,
   `InspectTab(model: models.inspect, state: state)`, and for any other tab
   `const SizedBox.shrink()` — an agent thread's set never asks for one, and
   a shrink is honest where a placeholder would invent a task.

3. `shell.dart`: add `ThreadInspector` and `InspectorBuilder` exactly as
   Contracts declares, and `final InspectorBuilder? inspectorFor;` on `Shell`
   and `_ThreadPlace`. In `_ThreadPlaceState`:
   - `late final ThreadInspector? _inspector = widget.inspectorFor
     ?.call(widget.thread.host, widget.thread);` as a field initialiser, so it
     is built once;
   - pass `inspector: _inspector?.state` and
     `inspectorBody: _inspector?.body` to `ThreadScreen`;
   - in `dispose`, call `_inspector?.dispose?.call()` before disposing the
     store.

4. `main.dart`, beside `newWorkFor`:
   ```dart
   inspectorFor: (host, thread) {
     final models = ThreadInspectorModels(
       hosts.hosts.firstWhere((h) => h.name == host).client,
       agentId: thread.id,
       sessionId: thread.id,
       root: thread.project,
     );
     return ThreadInspector(
       state: InspectorState(),
       body: (tab) => inspectorBody(models, tab),
       dispose: () {
         models.dispose();
       },
     );
   },
   ```
   with the imports of `thread_inspector_models.dart`,
   `model/inspector_state.dart` and `ui/inspector/host.dart`.

5. Tests.
   - `app/test/model/thread_inspector_models_test.dart`: build the holder
     over a fake client and assert `changes.agentId == 'a-1'`,
     `files.root == '/home/u/marshal'`, `inspect` is not disposed before
     `dispose()`; after `dispose()` a second `dispose()` does not throw.
   - `app/test/ui/shell/inspector_test.dart`, over a `ModelFixture` and
     `Studio` with `inspectorFor`:
     - `'an open thread draws the inspector'`: expect the five tab labels
       beside the transcript;
     - `'the Changes tab draws the change set'`: tap `Changes`, expect the
       gate or the file list the fixture's diff answers with;
     - `'⌘\\ collapses the panel to a rail'`: send the shortcut
       (`LogicalKeyboardKey.backslash` with meta), expect
       `find.byTooltip('Show the inspector')`;
     - `'below the medium width the panel opens as a sheet'`: set the surface
       size to `Size(600, 900)`, expect no docked column, open the header's
       inspector button, expect the `Inspector` sheet title.

**Verify.** `cd app && flutter test test/model/thread_inspector_models_test.dart test/ui/shell/inspector_test.dart test/ui/inspector/`
— the new tests and every inspector widget test pass.

## Final verification

1. `cd app && flutter analyse` — no errors, no new warnings.
2. `cd app && flutter test` — the whole Dart suite, including
   `design_rules_test.dart`, `tooltips_test.dart`, `states_test.dart` and
   `boundary_test.dart`.
3. `cd app && flutter test test/route_parity_test.dart` — the table still
   matches the client; `createStatusLink` and `statusLinks` keep the callers
   they had.
4. If the Flutter build is refreshed for a manual look:
   `cd app && flutter build web --base-href / --no-web-resources-cdn --output ../web/bridge/static`
   then `scripts/dev-daemon.sh`. The daemon restart ends running turns, so it
   is the last step, never mid-run.

## Integration notes

- No Go, contract or bridge change: every route this plan reaches already
  exists, and `route-parity.md` already carries its rows.
- `AppModel`-side wiring is confined to `main.dart`'s `Studio` arguments;
  nothing under `ui/` gains a `client/` import, so `boundary_test.dart` is
  untouched.
- The inspector's bodies take `state:` and `shipped:` where the tab accepts
  them; this plan passes neither, which draws the ready state. A later task
  that knows the thread's data state can thread it through `ThreadInspector`
  without changing a tab.
