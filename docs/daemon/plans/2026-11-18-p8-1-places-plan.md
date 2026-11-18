# P8 plan 1: every host's places — implementation plan

Date: 2026-11-18. Status: ready for review.

Base: `main` at `bdd20ddc`. Every anchor below was checked there. If P7
plan 2 lands first, re-check the `app/lib/main.dart` and `shell.dart`
anchors: it edits the thread wiring near them, not the sections.

Approved design: [p8-studio-federation.md](../p8-studio-federation.md), D1–D5,
with the Testing section's plan 1 list. Rules:
[design-system.md](../../studio-flutter/design-system.md), normative for
everything under `app/lib/src/ui/`.
Previous plans: [F2, the shell](../../studio-flutter/plans/2026-11-17-f2-shell-plan.md),
[F7 plan 1](../../studio-flutter/plans/2026-11-17-f7-1-automations-library-plan.md)
and [F7 plan 2](../../studio-flutter/plans/2026-11-17-f7-2-settings-plan.md),
whose shell and sections this plan extends.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **Automations, Library and Settings read the host the switcher names.**
  Under All hosts they read the first live host, and their top line says
  which.
- **Each host's section models are built once and kept,** and changing the
  scope over an unsaved edit asks first.
- **The Overview's Automations card lists every host's tripped watches,** and
  the Inbox shows every host's blocked requests and held review drafts.

## Non-goals

- Merging any section's rows across hosts (spec D2).
- Adding or removing hosts, and new work's host chip: plan 2.
- Any bridge or contract change. Every route this plan reads exists.

## Assumptions

Each was checked at the base.

- **The scope.** `_ShellState._host` (`app/lib/src/ui/shell/shell.dart:107`)
  is the switcher's host, or null for All hosts. `_sidebar` (`:136`) passes
  it to `Sidebar`, whose `HostSwitcher` (`sidebar.dart:76`) calls `onHost`,
  which sets it without the guard (`shell.dart:141-144`).
- **The guard.** `_leave` (`shell.dart:163`) consults `widget.unsaved` and
  asks before it calls `go`.
- **The place builders.** `Shell` declares `automationsFor`, `libraryFor` and
  `settingsFor` as `(int section, StateKind? state)` (`shell.dart:427-433`),
  and `_content` calls them (`:614-631`).
- **The sections.** `_Sections` (`app/lib/main.dart:291`):
  - holds one `_Automations`, `_Library` and `_Settings` (`:302-304`);
  - reads the first host (`_host`, `:308`) and its first project
    (`_project`, `:312`);
  - builds the three places (`:319`, `:348`, `:379`).

  `_Automations` (`:429`), `_Library` (`:457`) and `_Settings` (`:484`) build
  and load their models in their constructors.
- **Hosts as screens see them.** `HostView` (`fleet_store.dart:611`) has
  `name`, `kind`, `reach`, `lastContact` and `live`. `FleetStore.hosts`
  lists them in discovery order.
- **The switcher.** `HostSwitcher` (`ui/shell/host_switcher.dart:12`) takes
  `hosts`, `selected`, `onSelect` and `now`, and is built from the kit's
  `Menu` and `Btn`.
- **Tripped watches.** `TrippedWatch` (`watches_model.dart:23`) has `name`,
  `agent`, `action` and `age`. `WatchesModel.tripped` (`:95`) derives them
  from the rows. `Overview` takes `automations` (`overview.dart:43`), and the
  shell passes none (`shell.dart:574-582`).
- **The Inbox's feeds.**
  - `InboxModel.showPendingBlocks` and `showHeldDrafts`
    (`inbox_model.dart:298`, `:309`) take a host and a list.
  - Its answer posts a network decision or a draft action on the ask's host
    (`:472-500`).
  - `main.dart:93` builds it.
- **The routes.** `BridgeClient.watches()` (`bridge_client.dart:353`),
  `networkPending()` (`:829`) and `reviewDrafts({project, status})`
  (`:394`). With no project, `reviewDrafts` lists every project's.
- **The section writes.**
  - `BotsModel`'s draft actions, post, send to the author and discard
    (`bots_model.dart:115-137`);
  - `WatchesModel.create` (`watches_model.dart:129`).
- **A store over the registry.** `FactoryStore` (`model/factory_store.dart`)
  follows `HostRegistry` with `addListener` (`:28`). It polls on a timer
  (`_schedule`, `:372`) with intervals given to its constructor (`:24-25`),
  and is the model to copy.
- **Model tests.** `ModelFixture.start([FixtureHost(…)])`
  (`test/model/support.dart:217`) builds a registry of `FakeBridge`s, and
  `fixture.bridges[name].reply(method, path, …)` scripts a route
  (`test/support/fake_bridge.dart`).
- **Screens.** The `'overview'` ScreenCase (`test/support/screens.dart:1166`)
  draws the Overview, and the section ScreenCases (`:1242-1376`) draw the
  places directly, not through the shell.

## Decisions this plan makes beyond the spec

1. **The shell resolves the host.** A model function, `placeHost`, picks the
   host a place reads from the hosts and the scope (spec D2). The shell calls
   it and hands the builders a host that is never null. A place has nothing
   to show until discovery has answered, and is then built with no host.
2. **The shell draws the top line.** `_content` wraps each of the three
   places in a column: the `HostSwitcher` showing the resolved host, then the
   place. Its `onSelect` sets the scope through the same path as the
   sidebar's.
3. **Every scope change is guarded.** The sidebar's `onHost` and the place's
   switcher both go through `_leave`, so an unsaved edit asks before the
   scope changes.
4. **`PerHost<T>` keeps the models.** It is a small model-side cache:
   - `of(host)` builds on first use with the factory it was given, and
     returns the same value after;
   - `forget(host)` disposes one;
   - `dispose` disposes all.

   `_Sections` holds three of them. Plan 2's forget calls `forget`.
5. **`TrippedWatch` gains `host`.** The card writes the host before the name,
   as "build · nightly-bench", only when the store knows more than one host.
6. **`SignalsStore` intervals.** It reads every 15 s, and the constructor
   takes the interval so a test can shorten it. A re-read asked while one is
   in flight runs once after it, as `FleetStore._refreshAgents`
   (`fleet_store.dart:409`) coalesces its reads.
7. **The Inbox asks for re-reads through a callback.** `InboxModel` gains an
   optional `onAnswered(host)`. It is called after a network or draft answer
   posts successfully, and `main.dart` points it at `signals.refresh`.

## Execution

- `git switch -c p8-1-places` from `main`, or create a worktree on that
  branch.
- Run each task's Verify and commit it as `p8-1-places: task N — <title>`.
- Stage explicit paths only.
- Run `dart format` on every Dart file a task touches before its Verify.
- Each test this plan adds is proved by mutation: revert the line it covers
  and watch it fail, then restore with `git checkout`.
- No golden changes: no kit piece is added, and the gallery is untouched.

## Contracts

### Scope (`app/lib/src/model/place_host.dart`, Task 1)

```dart
/// The host a per-host place reads (spec D2): the scope when it names a known
/// host; else the first live host; else the first host; null when none is
/// known.
String? placeHost(List<HostView> hosts, String? scope);
```

### Shell (`app/lib/src/ui/shell/shell.dart`, Task 1)

```dart
/// Builds a per-host place for [host] (spec D1).
typedef HostPlaceBuilder =
    Widget? Function(String host, int section, StateKind? state);

// Shell's three fields become:
final HostPlaceBuilder? automationsFor;
final HostPlaceBuilder? libraryFor;
final HostPlaceBuilder? settingsFor;
```

### Per-host cache (`app/lib/src/model/per_host.dart`, Task 2)

```dart
/// One value per host, built on first use and kept (decision 4).
class PerHost<T> {
  PerHost(T Function(String host) build, {void Function(T value)? dispose});
  T of(String host);
  bool holds(String host);
  void forget(String host);
  void dispose();
}
```

### Signals (`app/lib/src/model/signals_store.dart`, Task 3)

```dart
/// Every host's tripped watches, blocked requests and held drafts (spec D4).
class SignalsStore extends ChangeNotifier {
  SignalsStore(
    HostRegistry hosts, {
    required InboxModel inbox,
    required DateTime Function() now,
    Duration interval = const Duration(seconds: 15),
  });

  /// Every host's tripped watches, newest first.
  List<TrippedWatch> get tripped;

  /// Whether more than one host is known, so a row names its host.
  bool get manyHosts;

  void start();
  void stop();

  /// Re-reads [host] now, or once after the read in flight.
  Future<void> refresh(String host);
}
```

### Tripped watch (`app/lib/src/model/watches_model.dart`, Task 3)

```dart
// TrippedWatch gains:
  /// The host whose watch tripped.
  final String host; // required
```

### Inbox (`app/lib/src/model/inbox_model.dart`, Task 3)

```dart
// InboxModel's constructor gains:
  void Function(String host)? onAnswered,
```

### Section writes (`bots_model.dart`, `watches_model.dart`, Task 4)

```dart
// BotsModel's and WatchesModel's constructors gain:
  void Function()? onWrote,
```

## Task 1 — the scope reaches the places

**Files:** `app/lib/src/model/place_host.dart` (new),
`app/lib/src/ui/shell/shell.dart`, `app/lib/main.dart`,
`app/test/model/place_host_test.dart` (new), `app/test/ui/shell_test.dart`.

1. **`placeHost`** answers by spec D2:
   - the scope when a host of that name is known;
   - else the first host whose `live` is true;
   - else the first host;
   - null when the list is empty.

   A scope naming a host that is not known reads as All hosts.
2. **The builders.** `Shell`'s three fields take a `HostPlaceBuilder`.
   `_content` resolves the host with `placeHost(widget.store.hosts, _host)`.
   With no host it draws the planned place, as a null build does today, and
   with one it calls the builder.
3. **The top line.** `_content` puts a `HostSwitcher` above each of the three
   built places (decision 2), with `selected` set to the resolved host, then
   the place, filling the rest. Its spacing comes from `tokens.dart`, as the
   sidebar's does (`sidebar.dart:67-72`).
4. **The guard.** The sidebar's `onHost` and the top line's `onSelect` both
   set `_host` inside `_leave` (decision 3).
5. **The composition root.** `_Sections`' three methods take the host and,
   for this task only, still build from its one set of models: the host is
   ignored. Task 2 makes it per host.
6. Tests:
   - `place_host_test.dart`:
     - `the scope wins when it names a known host`;
     - `all hosts reads the first live host`;
     - `with no live host the first host is read`;
     - `an unknown scope reads as all hosts`;
     - `no hosts, no host`.
   - `shell_test.dart`:
     - `the settings builder receives the switcher's host`;
     - `under all hosts the builder receives the first live host`;
     - `a per-host place shows the switcher naming its host`;
     - `changing the scope over an unsaved edit asks first`.

**Verify:** `cd app && flutter test test/model/place_host_test.dart test/ui/shell_test.dart && flutter analyze`.

**Mutation check:** make `placeHost` return the first host whatever its
reach. `all hosts reads the first live host` must fail. Restore.

## Task 2 — one set of models per host

**Files:** `app/lib/src/model/per_host.dart` (new), `app/lib/main.dart`,
`app/test/model/per_host_test.dart` (new).

1. **`PerHost<T>`** as decision 4 says:
   - `of` builds through the factory once per host;
   - `holds` says whether it has;
   - `forget` disposes and drops one;
   - `dispose` disposes every value.
2. **`_Sections`:**
   - Hold a `PerHost` each for `_Automations`, `_Library` and `_Settings`.
     Each value is built from that host's client, found in the registry by
     name as `main.dart:96` finds an inbox client.
   - The host's section project is that host's first project, as `_project`
     reads the first host's today.
   - `_host` and `_project` go.
   - The three model sets gain a `dispose` that disposes their models, which
     `PerHost` calls.
3. **What stays app-wide.** Settings › Hosts' model reads the store's rows,
   and General's preferences are the window's. Both are still built per host,
   because `_Settings` builds them, and read the same everywhere (spec D2).
4. Tests, in `per_host_test.dart`:
   - `a value is built once per host`;
   - `two hosts get two values`;
   - `forget disposes one host's value only`;
   - `dispose disposes every value`.

The wiring in `main.dart` is checked by hand in Task 5's run.

**Verify:** `cd app && flutter test test/model/per_host_test.dart && flutter analyze`.

**Mutation check:** make `of` build a new value on every call. `a value is
built once per host` must fail. Restore.

## Task 3 — every host's signals

**Files:** `app/lib/src/model/signals_store.dart` (new),
`app/lib/src/model/watches_model.dart`, `app/lib/src/model/inbox_model.dart`,
`app/lib/src/ui/overview/overview.dart`, `app/lib/src/ui/shell/shell.dart`,
`app/lib/main.dart`, `app/test/model/signals_store_test.dart` (new),
`app/test/ui/overview_test.dart`, `app/test/model/inbox_model_test.dart`,
`app/test/support/screens.dart`, `app/test/ui/watches_test.dart`.

1. **`TrippedWatch.host`.** It is required. `WatchesModel` gains a `host`
   that it puts on every tripped watch; the section's own rows are unchanged.
   Update every constructor call the analyzer reports.
2. **`SignalsStore`:**
   - It follows the registry as `FactoryStore` does. A host added is read at
     once, then every `interval`.
   - Each read fetches the three routes from the assumptions, through the
     host's client, with `reviewDrafts(status: 'draft')`. Each route's
     failure is kept apart: a failed watches read keeps that host's last
     tripped watches and still hands over its blocks.
   - **Hand-over.** After each read it hands the host's blocks to
     `inbox.showPendingBlocks` and its drafts to `inbox.showHeldDrafts`. It
     derives the host's tripped watches as `WatchesModel.tripped` does,
     reusing that derivation rather than copying it: move it to a top-level
     function both call.
   - **Reads in flight.** One read is in flight per host, and a `refresh`
     asked meanwhile runs once after it (decision 6).
   - **Lifecycle.** `stop` cancels the timers, and `dispose` stops and stops
     following the registry.
3. **The Overview.** `Studio` (`shell.dart:331`) and `Shell` (`:52`) gain a
   `signals` field. `_content` passes `signals.tripped` to
   `Overview.automations`. The store joins the `Listenable.merge` that
   `Studio` rebuilds on (`shell.dart:521-522`), so a new trip redraws.
   - The card writes each row's host before its name when
     `signals.manyHosts` (decision 5).
   - Its tooltip and overflow rules are unchanged.
4. **The Inbox.** `onAnswered` (decision 7) is called with the ask's host
   after a network or draft answer posts successfully. A failed post calls
   nothing.
5. **The composition root.** `main.dart` builds the store after the inbox,
   points `onAnswered` at `signals.refresh`, passes the store to `Studio` and
   on to `Shell`, and starts it beside `factory.start()` (`:260`).
6. **Screens.** The `'overview'` ScreenCase gains two tripped watches on two
   hosts, so the states, overflow and tooltip tests draw the host prefix.
7. Tests:
   - `signals_store_test.dart`:
     - `every host's tripped watches are listed, newest first`;
     - `a second host's blocked request becomes an inbox ask`;
     - `a second host's held draft becomes an inbox ask`;
     - `a failed read keeps that host's last answer`;
     - `one route failing does not lose the others`;
     - `a host never answered contributes nothing`;
     - `a refresh during a read runs once after it`;
     - `the interval re-reads every host`.
   - `overview_test.dart`: `tripped watches name their host when there are
     several`, and `one host's watches carry no prefix`.
   - `inbox_model_test.dart`: `answering a network ask asks for a re-read of
     its host`, and `a failed answer asks for nothing`.

**Verify:** `cd app && flutter test test/model/signals_store_test.dart test/ui/overview_test.dart test/model/inbox_model_test.dart test/ui/watches_test.dart test/states_test.dart test/overflow_test.dart test/tooltips_test.dart && flutter analyze`.

**Mutation check:** in `SignalsStore`, hand the blocks to the inbox under the
first host's name for every host. `a second host's blocked request becomes
an inbox ask` must fail. Restore.

## Task 4 — a section's write re-reads its host's signals

**Files:** `app/lib/src/model/bots_model.dart`,
`app/lib/src/model/watches_model.dart`, `app/lib/main.dart`,
`app/test/ui/bots_test.dart`, `app/test/ui/watches_test.dart`.

1. **`onWrote`.** `BotsModel` calls it after a draft is posted, sent to its
   author or discarded successfully. `WatchesModel` calls it after `create`
   succeeds. A refusal calls nothing.
2. **The composition root.** `main.dart` builds each host's `BotsModel` and
   `WatchesModel` with `onWrote` re-reading that host's signals.
3. Tests, model-level `test()` cases beside the existing ones:
   - `bots_test.dart`: `posting a draft reports the write`, and `a refused
     post reports nothing`;
   - `watches_test.dart`: `starting a watch reports the write`.

**Verify:** `cd app && flutter test test/ui/bots_test.dart test/ui/watches_test.dart`.

**Mutation check:** call `onWrote` before the post's await rather than after
it. `a refused post reports nothing` must fail. Restore.

## Task 5 — the record and full verification

**Files:** `docs/daemon/p8-studio-federation.md`,
`docs/studio-flutter/f2-shell-overview-inbox.md`.

1. **F2's record.** Its decision "The host switcher narrows the sidebar's
   tree only" gains one sentence: P8 D1 widens it to Automations, Library and
   Settings.
2. **A run by hand** with two hosts: the local daemon, and
   `scripts/dev-studio.sh`'s server-profile Studio named in `fleet.toml`
   with its token stored by `marshal fleet login`.
   1. Open Settings › Models under each host.
   2. Leave an unsaved edit and switch hosts.
   3. Trip a watch on the second host.

   Record what each showed.
3. **The record.** Fill the spec's "Plan 1" record:
   - what landed;
   - any decision the executor had to make;
   - the mutation checks and their results;
   - the run.
4. **Full verification:**
   - `cd app && flutter analyze`;
   - `flutter test`;
   - `dart format --output=none --set-exit-if-changed lib test`;
   - on macOS, `flutter test test/gallery_test.dart`, which must pass
     unchanged.

**Verify:** the full verification above.

## Done when

- Settings, Library and Automations show the switcher's host, or the first
  live host under All hosts, and say which.
- Switching hosts keeps each host's models, and asks first over an unsaved
  edit.
- A watch tripped, a request blocked or a review held on any host reaches
  the Overview or the Inbox.
- Every test named above passes, each mutation check fails as described, and
  the run is recorded.
