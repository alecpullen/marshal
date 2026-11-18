# Studio Fleet attention and recovery — implementation plan

Date: 2026-11-18. Status: ready to execute. Base: `main` at `bdd20ddc`
(2026-11-18), verified in this worktree.

Specification: [studio-fleet-attention-ux-spec.md](../studio-fleet-attention-ux-spec.md).
Ported from PR 115's [Flutter port plan](2026-11-17-studio-fleet-attention-ux-plan.md),
which is committed here unchanged for reference. Design system:
[`docs/studio-flutter/design-system.md`](../../studio-flutter/design-system.md)
is normative under `app/lib/src/ui/`.

## Goal and non-goals

**Goal:** every entry in Overview and Inbox names the source and project it
came from, says whether it blocks work, and leads to the place it is acted on;
each source says independently how fresh it is and what failed, so one dead
feed cannot hide a healthy one or pass for an empty queue.

**Non-goals:** no new poller, no new external notification channel, no
automatic action, no inferred owner, no new wire type or Go change, and no
change to the Svelte Studio (retired at `603f6930`). Counts stay counts of
real items; nothing gains a percentage, a duration or a completion claim it
cannot support.

**Tasks:** three, each one reviewable unit. Tasks 1 and 2 share no file and
are **parallel-safe**; Task 3 consumes both and runs after them.

## What the tree actually does today (verified at `bdd20ddc`)

These facts decide the tasks. They were read, not assumed.

1. **The screens' data-state machinery never runs.** `Overview(` and
   `Inbox(` are constructed in `shell.dart:574` and `shell.dart:585` without
   `state:`. `state: _state(store)` is passed only to the automations,
   library and settings places (`shell.dart:536`). A fleet that never
   connected therefore renders the Inbox's "Nothing is waiting on you." — an
   empty successful-looking queue for a failed feed, exactly what the spec
   forbids.
2. **The Overview under-reports its own count.** `buildOverview`
   (`overview_model.dart:169`) sets `needsYou: inbox.asks.take(overviewAsks)`
   with `overviewAsks = 3` (`overview_model.dart:10`); `Overview._subtitle`
   (`overview.dart:412`) and `Label('Needs you', count: asks.length)`
   (`overview.dart:158`) report that capped number as the total.
3. **An `Ask` names no source, project or scope.** `class Ask`
   (`inbox_model.dart:162`) carries host, id, kind, thread, title, body,
   actions, since, step, questions, pull, state, error and network. Its thread
   is null for intake, review-draft and pull asks, and `Inbox._source`
   (`inbox.dart:462`) returns null for those, so those cards have no source
   line at all.
4. **The Factory asks already exist and are merged.** `InboxModel._rebuild`
   (`inbox_model.dart:594`) folds `asksOf(host, item)`
   (`work_item_model.dart:241`) into the same queue as the fleet's, producing
   `approvalDraft`, `decision`, `recovery` and `completion` asks. This slice
   refines them; it does not add them.
5. **The Overview offers dead buttons for factory asks.** `InboxModel.answer`
   returns early for those four kinds (`inbox_model.dart:329`), and
   `Inbox._threadOnly` (`inbox.dart:255`) draws no action row for them — but
   `Overview._needsYou` draws `ask.actions` for every ask, so "Approve and
   start" and "Complete" appear on the Overview and post nothing.
6. **The merge order is deterministic but not source-ordered.**
   `InboxModel._rebuild` sorts by `since` then `id` (`inbox_model.dart:594`),
   which drops the fleet queue's own (authoritative) order when sources mix.
   Within a source, the store's order is kept.
7. **Freshness exists in the stores but reaches no screen.** `FleetStore`
   exposes `reach(host)`, `lastContact(host)` and `failure(host)`;
   `FactoryStore` exposes `loaded(host)` and `failure(host)` but records no
   time, so "how old is what I hold" cannot be said for the factory.
8. **There is no viewer identity, so "Mine" cannot be built.**
   `AgentStatus.ownerId` exists (`web/bridge/contract/agents.go:47`) but
   nothing in `app/` resolves "me": there is no `/api/me` route, and
   `web/bridge/contract/intake.go:18` records that today's bridge never sets
   an intake owner. The Flutter tree has no Mine/Everyone filter to preserve.
9. **Ready is source-reported; unknown checks never read as passed.**
   `PullGroup.ready` comes only from `PullState.pullReady`
   (`pulls_model.dart:357`), and unknown or pending checks map to
   `PullCheckState.pending` (`pulls_model.dart:376`). A ready pull with no
   checks reads "Ready for review" plus "No checks on this head yet"
   (`pulls_model.dart:338`), which is true; this plan pins that with a test
   instead of changing it.
10. **Two hardcoded ages.** `inbox.dart:507` says `age: '4m'` and the stale
    banner at `inbox.dart:133` says `age: 'now'`; neither comes from a source.

## Contracts

Only Task 3 uses these across files; Tasks 1 and 2 introduce them. Import
paths are relative to `app/lib/src/model/`.

### `attention.dart` (new, Task 1)

```dart
import 'package:flutter/foundation.dart' show immutable;

import 'nav_model.dart' show projectName;

/// Where an attention entry came from.
///
/// The Inbox merges several feeds into one queue. Each entry says which feed
/// reported it, so a factory draft, a review bot's held draft and an agent's
/// own question are never read as one source speaking for another.
enum AskSource {
  /// The fleet's own stream: an agent's parked request, an intake submission,
  /// a pull request whose fix failed.
  fleet,

  /// The review bot's held drafts, handed over by the Bots section.
  review,

  /// The egress proxy's blocked requests, handed over by the Network section.
  network,

  /// A work item the factory owns, read over its own route.
  factory,
}

extension AskSourceWords on AskSource {
  /// The source's name in a card's source line.
  String get label => switch (this) {
    AskSource.fleet => 'Fleet',
    AskSource.review => 'Review bot',
    AskSource.network => 'Network',
    AskSource.factory => 'Factory',
  };

  /// What this source's reports cover, for a tooltip that says more than the
  /// label.
  String get note => switch (this) {
    AskSource.fleet =>
      "Reported by the daemon's own fleet stream, so it goes when the daemon "
          'stops reporting it.',
    AskSource.review =>
      'Reported by the review bot, which holds the draft until a person '
          'posts, sends or discards it.',
    AskSource.network =>
      'Reported by the egress proxy, which holds the request until a person '
          'decides it.',
    AskSource.factory =>
      "Read from the factory's work items, which are polled apart from the "
          'fleet stream.',
  };
}

/// Who an entry belongs to.
///
/// There is no `mine` value: the bridge has no viewer identity to compare a
/// record's `ownerId` with (no `/api/me` route, and
/// `web/bridge/contract/intake.go` records that today's bridge never sets an
/// intake owner), so an entry cannot honestly claim to be this person's.
enum AskScope {
  /// A source the daemon reports for the whole bridge.
  everyone,

  /// A source whose owner the contract does not name: the entry is stated as
  /// the project's rather than guessing a person.
  projectWide,
}

extension AskScopeWords on AskScope {
  /// The scope's name in a card's source line.
  String get label => switch (this) {
    AskScope.everyone => 'Everyone',
    AskScope.projectWide => 'Project-wide',
  };

  /// What the scope means for the person, for a tooltip.
  String get note => switch (this) {
    AskScope.everyone =>
      'The daemon reports this for the whole bridge, not for one person.',
    AskScope.projectWide =>
      'The factory record names no owner, so this is stated as the project’s '
          'rather than guessing who it belongs to.',
  };
}

/// Where an ask came from, as a card's source line: its source, its project
/// when the source names one, and its scope.
///
/// The project is omitted rather than guessed when the source does not name
/// one (an intake submission and a review draft carry a repository id, not a
/// project root). [projectName] is the same helper the Overview's project
/// table uses.
String provenanceOf(AskSource source, String project, AskScope scope) => [
  source.label,
  if (project.isNotEmpty) projectName(project),
  scope.label,
].join(' · ');
```

### `Ask` additions (Task 1, in `inbox_model.dart`)

```dart
  /// Where it came from: the feed that reported it.
  final AskSource source;

  /// The project it belongs to, as the source names it; empty when the source
  /// names none, which the source line omits rather than guessing.
  final String project;

  /// Who it belongs to, as the source can say.
  final AskScope scope;

  /// Whether this ask stops work until it is answered.
  ///
  /// It is the source's own kind saying so, not a filter's guess: a draft
  /// that cannot start, a worker's question, a decision, a recovery, a failed
  /// fix, a held review and a blocked request all stop something. An intake
  /// has not started yet and a completion waits only after the work finished,
  /// so neither blocks.
  bool get blocking => switch (kind) {
    AskKind.approval ||
    AskKind.question ||
    AskKind.approvalDraft ||
    AskKind.decision ||
    AskKind.recovery ||
    AskKind.pull ||
    AskKind.reviewDraft ||
    AskKind.network => true,
    AskKind.intake || AskKind.completion => false,
  };

  /// Whether it is answered in its own thread rather than from a list: a work
  /// item's four asks carry the item's version and reviewed identity, so a
  /// list shows them and their source line opens the thread
  /// (`InboxModel.answer` posts nothing for them).
  bool get answeredInThread => switch (kind) {
    AskKind.approvalDraft ||
    AskKind.decision ||
    AskKind.recovery ||
    AskKind.completion => true,
    AskKind.approval ||
    AskKind.question ||
    AskKind.intake ||
    AskKind.pull ||
    AskKind.reviewDraft ||
    AskKind.network => false,
  };

  /// The card's source line.
  String get provenance => provenanceOf(source, project, scope);
```

### `source_state.dart` (new, Task 2)

```dart
import 'package:flutter/foundation.dart' show immutable;

import '../client/host.dart' show HostReach;
import 'age.dart' show relativeAge;
import 'factory_store.dart' show FactoryStore;
import 'fleet_store.dart' show FleetStore;

/// Which feed a freshness row reports.
enum SourceKind {
  /// The fleet's stream: agents, its queue, and the pull requests in it.
  fleet,

  /// The factory's work items, polled over their own route.
  factory;

  /// The name a person reads for this source.
  String get label => switch (this) {
    SourceKind.fleet => 'Fleet',
    SourceKind.factory => 'Factory',
  };
}

/// One source's freshness for one host: when it last answered, why its last
/// read failed, and whether what is held is a last trustworthy snapshot.
@immutable
class SourceState {
  const SourceState({
    required this.kind,
    required this.host,
    this.reach,
    this.lastRead,
    this.failure,
    this.loaded = false,
  });

  final SourceKind kind;
  final String host;

  /// What the fleet's stream can reach of [host]; null for a source read over
  /// HTTP that has no stream.
  final HostReach? reach;

  /// When this source last answered for [host], or null when it never has.
  final DateTime? lastRead;

  /// Why the last read failed, in the bridge's words; null when it worked.
  final String? failure;

  /// Whether anything was ever read for [host].
  final bool loaded;

  /// The last read worked.
  bool get healthy => loaded && failure == null;

  /// The last read failed and an earlier snapshot is what is held: its
  /// entries stay, labelled with their age.
  bool get stale => loaded && failure != null;

  /// Nothing was ever read, so there is no snapshot: the source says it is
  /// unavailable rather than empty.
  bool get unavailable => !loaded && failure != null;

  /// The source is still making its first attempt.
  bool get connecting => !loaded && failure == null;

  /// What this source's row says, in the design system's words (§8): what is
  /// held, how old it is, and what did not arrive.
  String words(DateTime now) {
    final age = lastRead == null ? 'unknown' : relativeAge(lastRead!, now);
    return switch (this) {
      _ when healthy => '${kind.label}: read $age ago',
      _ when stale =>
        '${kind.label}: $age old, and the last read failed — $failure',
      _ when unavailable =>
        '${kind.label}: unavailable — ${failure ?? 'no answer yet'}',
      _ => '${kind.label}: connecting',
    };
  }

  @override
  String toString() => 'SourceState($host/${kind.name}, '
      '${healthy ? 'healthy' : stale ? 'stale' : unavailable ? 'unavailable' : 'connecting'})';
}

/// Every source's freshness, in a stable order: the store's host order, then
/// the fleet before the factory, so the rows do not move between reads.
///
/// A source that is not wired (`factory` null) contributes no row rather than
/// an empty one: it has not failed, it is not there.
List<SourceState> sourceStates(FleetStore store, {FactoryStore? factory}) {
  final states = <SourceState>[];
  for (final host in store.hosts) {
    final contact = host.lastContact;
    states.add(
      SourceState(
        kind: SourceKind.fleet,
        host: host.name,
        reach: host.reach,
        lastRead: contact,
        failure: store.failure(host.name),
        // A host that has answered at least once holds a snapshot, even when
        // that snapshot names no agent.
        loaded: contact != null,
      ),
    );
    if (factory == null) continue;
    states.add(
      SourceState(
        kind: SourceKind.factory,
        host: host.name,
        lastRead: factory.lastReadAt(host.name),
        failure: factory.failure(host.name),
        loaded: factory.loaded(host.name),
      ),
    );
  }
  return List.unmodifiable(states);
}
```

### `OverviewModel` additions (Task 2, in `overview_model.dart`)

```dart
  /// Every ask, including the ones past [needsYou]'s cap, so a count and a
  /// label agree with what exists rather than with what is drawn.
  final int needsYouTotal;

  /// Each source's freshness, by host then source.
  final List<SourceState> sources;
```

## Task 1: every attention entry names its source, project and scope

**Goal:** each `Ask` states which feed reported it, which project it belongs
to (or that the source names none) and who it belongs to; the blocking and
thread-only rules are the model's, not a screen's guess.

**Files:**

- `app/lib/src/model/attention.dart` (new — the whole file is the Contracts
  block above, verbatim)
- `app/lib/src/model/inbox_model.dart`
- `app/lib/src/model/work_item_model.dart`
- `app/test/model/attention_test.dart` (new)
- `app/test/model/inbox_model_test.dart`
- Fixture call sites of `model.Ask(` / `Ask(` that must compile:
  `app/test/support/screens.dart`, `app/test/ui/inbox_test.dart`,
  `app/test/ui/overview_test.dart`, `app/test/ui/sections_test.dart`,
  `app/test/ui/thread/asks_in_place_test.dart`,
  `app/test/ui/thread/e2e_test.dart`

**Parallel-safe with:** Task 2 (disjoint files; neither consumes the other).

**Steps:**

1. Create `attention.dart` from the Contracts block. Import it in
   `inbox_model.dart` (it is in the same directory, so
   `import 'attention.dart';`) and in `work_item_model.dart`.

2. In `class Ask` (`inbox_model.dart:162`) add the three fields and three
   getters from the Contracts block. Make `source` and `scope` **required**
   named parameters in the constructor (place them after `since`); add
   `this.project = ''` as an optional named parameter with the doc comment
   from the Contracts block. `withState` must copy all three:
   `source: source, project: project, scope: scope`.

3. Set the values at every construction site in `inbox_model.dart`:

   | Site | `source` | `project` | `scope` |
   |---|---|---|---|
   | `_askForPending` (agent's parked request) | `AskSource.fleet` | `agent.project` | `AskScope.everyone` |
   | `_askForIntake` | `AskSource.fleet` | omit (`''`) | `AskScope.everyone` |
   | `_askForPull` | `AskSource.fleet` | omit (`''`) | `AskScope.everyone` |
   | `_askForDraft` (review bot) | `AskSource.review` | omit (`''`) | `AskScope.everyone` |
   | `_askForBlock` (egress proxy) | `AskSource.network` | `agent?.project ?? ''` | `AskScope.everyone` |

   For `_askForBlock`, hoist the existing `final agent = agents[block.agentID];`
   and use it for both the thread and the project.

4. In `asksOf` (`work_item_model.dart:241`) set every one of the four `Ask(`
   literals to `source: AskSource.factory`,
   `project: item.projectRoot`, `scope: AskScope.projectWide`. Import
   `attention.dart`.

5. Replace `Inbox._blocking` (`inbox.dart:103`) with `ask.blocking` and
   `Inbox._threadOnly` (`inbox.dart:255`) with `ask.answeredInThread`, and
   delete both private helpers. This is a screen-side call-site change only:
   no visual change in this task.

   **Note:** this edits `inbox.dart`, which Task 3 also edits. That is why
   Task 3 runs after Task 1 and not beside it.

6. Add the fixture arguments. Every `Ask(` in the six test files above needs
   `source:` and `scope:`. Use `AskSource.fleet` / `AskScope.everyone` in the
   inbox and overview fixtures, `AskSource.factory` / `AskScope.projectWide`
   in any factory fixture, and keep each fixture's meaning: a fixture that
   stands for a review draft names `AskSource.review`. `screens.dart`'s
   `_overviewAsk` (`screens.dart:281`) gains optional named parameters
   `AskSource source = AskSource.fleet` and
   `AskScope scope = AskScope.everyone` so its callers stay short.

7. Write `app/test/model/attention_test.dart` with exactly these tests:
   - `every source has a label and a note that says more than the label`
     (all four values; each note is non-empty and differs from its label).
   - `every scope has a label and a note` (both values).
   - `provenance names the source, the project and the scope` — a factory ask
     on `/home/u/marshal` reads
     `Factory · marshal · Project-wide`.
   - `provenance omits a project the source does not name` — `Fleet · Everyone`
     with no separator left dangling.
   - `blocking is the source kind's own claim` — the eight blocking kinds true,
     `intake` and `completion` false.
   - `answeredInThread is the four factory kinds and nothing else`.

8. Extend `app/test/model/inbox_model_test.dart` with exactly these tests,
   built from the existing helpers and a `FactoryStore` over the existing fake
   hosts:
   - `every ask names its source, project and scope` — one ask per source,
     asserting the three fields (factory ask: `factory`, the item's
     `projectRoot`, `projectWide`; agent request: `fleet`, the agent's
     project, `everyone`; intake: `fleet`, `''`, `everyone`).
   - `a factory decision and the same worker's question stay separate asks` —
     one factory item with a decision plus that worker's `pending` question;
     assert two asks, two different `id`s, `factory` and `fleet` sources, and
     that neither is removed from the list.
   - `a source that names no project says so rather than guessing` — the
     intake ask's `project` is empty and its `provenance` contains no ` · `
     run of separators.
   - `a draft blocks work and a completion does not` — `blocking` true for the
     item's `approvalDraft` ask, false for its `completion` ask.
   - `factory asks are answered in their thread, not in the list` — all four
     `answeredInThread`, and an agent approval false.

**Verify:** from `app/`:

```bash
flutter test test/model/attention_test.dart test/model/inbox_model_test.dart
flutter analyze
```

Expected: the two files pass with every new test named above, and `analyze`
reports no issues (it is what proves the six fixture files compile).

## Task 2: each source says how fresh it is, and a failed read keeps what it held

**Goal:** the stores expose when each source last answered and why its last
read failed, and the Overview model carries that per host and source plus the
true total of waiting asks — without a second poller and without overwriting a
last trustworthy snapshot.

**Files:**

- `app/lib/src/model/source_state.dart` (new — the Contracts block above,
  verbatim)
- `app/lib/src/model/factory_store.dart`
- `app/lib/src/model/overview_model.dart`
- `app/test/model/source_state_test.dart` (new)
- `app/test/model/factory_store_test.dart` (extend, or create if absent —
  check with `ls app/test/model/`)
- `app/test/model/overview_model_test.dart`

**Parallel-safe with:** Task 1 (disjoint files; neither consumes the other).

**Steps:**

1. Create `source_state.dart` from the Contracts block.

2. In `FactoryStore`, add `final _lastRead = <String, DateTime>{};` beside
   `_loaded`, the getter
   `DateTime? lastReadAt(String host) => _lastRead[host];` beside
   `loaded(String host)`, and record the time wherever the host answered:
   - in `refresh`, in the success branch right after `_loaded.add(name);`:
     `_lastRead[name] = _now();`
   - in `_apply(String host, FactoryItem item)`, after `_loaded.add(host);`:
     `_lastRead[host] = _now();` (a command's answer, a create and a
     drill-in read all prove the host answered).

   Do **not** touch `_lastRead` on a failure: the time of the last read that
   worked is what makes "3m old" true.

3. In `overview_model.dart`, add the two fields from the Contracts block to
   `OverviewModel` (after `running`, before `projects`, in both the
   constructor and the field list, both `required`). In `buildOverview`, import
   `source_state.dart` and set:

   ```dart
   needsYouTotal: inbox.asks.length,
   sources: sourceStates(store, factory: inbox.factory),
   ```

   `InboxModel.factory` is already public (`inbox_model.dart:269`), so no
   InboxModel change is needed and this task keeps off Task 1's files.

4. Write `app/test/model/source_state_test.dart` with exactly these tests,
   using `sourceStates` over a fake `FleetStore` and `FactoryStore` (reuse the
   fakes the existing model tests build; if a fake host does not allow setting
   `reach`, drive it through the fake `EventStream` those tests already use):
   - `a source that answered is healthy and says how long ago` — words
     contain `Fleet: read` and end in `ago`.
   - `a failed read over a snapshot is stale, not empty` — `loaded: true`,
     `failure: 'offline'` → `stale` true, `words` contains `old, and the last
     read failed — offline`.
   - `a source that never answered is unavailable, not empty` —
     `loaded: false`, `failure: 'offline'` → `unavailable` true, `words`
     contains `unavailable`.
   - `a source making its first attempt is connecting, not unavailable`.
   - `the rows are ordered by host then fleet before factory` — assert the
     exact `[host, kind]` sequence for two hosts.
   - `a factory that is not wired contributes no row` — `factory: null`
     yields one row per host.

5. Extend `factory_store_test.dart` with exactly these tests:
   - `a successful read records when it happened` — `lastReadAt` null before
     the first read, non-null after, and the injected `now` is used.
   - `a failed read keeps the time of the last one that worked` — read, then
     fail, and `lastReadAt` is unchanged while `failure` is set.
   - `a host that has not answered has no read time` — a second host stays
     null.

6. Extend `overview_model_test.dart` with exactly these tests:
   - `the waiting total counts every ask while the drawn list stays capped` —
     five asks give `needsYou.length == 3` and `needsYouTotal == 5`.
   - `the model carries one freshness row per host and source` — both kinds
     present, `SourceKind.fleet` first.
   - `a factory read failure leaves the fleet's project counts intact` — the
     factory fails, the agents still appear in `projects` and `capacity`, and
     `sources` reports the factory as stale/unavailable.

**Verify:** from `app/`:

```bash
flutter test test/model/source_state_test.dart test/model/factory_store_test.dart test/model/overview_model_test.dart
flutter analyze
```

Expected: all pass with the named tests, and `analyze` clean.

## Task 3: compose Overview and Inbox from those facts

**Goal:** both screens show the real per-source freshness, the true waiting
count, a source line on every entry, and no control that posts nothing; a dead
feed is visible beside a healthy one instead of replacing it.

**Files:**

- `app/lib/src/ui/overview/overview.dart`
- `app/lib/src/ui/inbox/inbox.dart`
- `app/lib/src/ui/shell/shell.dart`
- `app/test/ui/overview_test.dart`
- `app/test/ui/inbox_test.dart`
- `app/test/support/screens.dart` (state fixtures for the gallery registry)

**Depends on:** Tasks 1 and 2 (uses `Ask.provenance`,
`Ask.answeredInThread`, `OverviewModel.sources`, `OverviewModel.needsYouTotal`
and `sourceStates`). Runs after both.

**Steps:**

1. **Wire the shell** (`shell.dart:574` and `shell.dart:585`). Import
   `source_state.dart`. Pass `state: _state(widget.store)` to both screens.
   Pass the Inbox its sources:
   `sources: sourceStates(widget.store, factory: widget.factory)`.
   The Overview reads `overview.sources`, so it takes no new parameter.

2. **Full-page states yield to a loaded source.** In both `build` methods,
   replace the early return's condition so a global failure does not hide a
   source that answered:

   ```dart
   final anyLoaded = sources.any((source) => source.loaded);
   if (state != null && state != StateKind.stale && !anyLoaded) {
     return PageBody(title: '…', children: [_stateMessage(state)]);
   }
   ```

   `Inbox` gains `final List<SourceState> sources;` (default `const []`) and
   `Overview` uses `overview.sources`. Keep the existing `state` parameter.

3. **Draw one freshness line per unwell source.** Add a private helper to each
   screen that returns a `StateMessage.stale` for each source where
   `stale`, and a `StateMessage.unavailable` for each `unavailable`, each
   carrying `source.words(now)` as its detail and the existing retry action
   (`onRetry`). Skip `healthy` and `connecting`. Replace the Inbox's
   hardcoded `StateMessage.stale(age: 'now', …)` (`inbox.dart:133`) and the
   `age: '4m'` in `_stateMessage` (`inbox.dart:507`) with these rows; the
   whole-screen `_stateMessage` keeps only the states it can justify
   (`loading`, `empty`, `error`, `unavailable` with `lastSeen: 'unknown'` —
   never an invented age).

4. **Overview counts.** In `_needsYou` (`overview.dart:156`), stop re-capping
   and use the model's own list; set
   `Label('Needs you', count: overview.needsYouTotal, …)`. When
   `needsYouTotal > overview.needsYou.length`, draw one muted line under the
   cards: `'${overview.needsYouTotal - overview.needsYou.length} more in the inbox.'`
   In `_subtitle` (`overview.dart:412`) use `overview.needsYouTotal` for the
   waiting count.

5. **Overview: no dead controls.** In `_needsYou`, replace the
   `ask.questions.length > 1` action branch with:

   ```dart
   actions: ask.answeredInThread
       ? [
           AskAction(
             'Open in the factory',
             onTap: _thread(ask.thread!),
           ),
         ]
       : ask.questions.length > 1
       ? [AskAction('Open inbox', onTap: _open(const Place(PlaceKind.inbox)))]
       : [ …existing mapping… ],
   ```

   `_thread` returns null only when `onSelect` is null, which also makes the
   action inert — acceptable, as elsewhere on this screen.

6. **Source line on every card, both screens.** Overview: replace
   `source: ask.thread == null ? null : '…'` (`overview.dart:183`) with
   `source: ask.provenance`, and keep `onOpenSource` as it is. Inbox: replace
   `_source` (`inbox.dart:462`) with `ask.provenance` and delete the helper.

7. **Inbox filters say what they mean.** `_blocking` already became
   `ask.blocking` in Task 1. Add filtered-empty messages: when the filter is
   `blocking` and nothing matches, `StateMessage.empty(message: 'Nothing is
   blocking work.', action: …)` whose action switches the filter to `all`;
   when it is `lookAt`, `'Nothing is waiting to be looked at.'` with the same
   action. Keep the existing all-empty sentence.

8. **Drop the stale placeholder.** Remove
   `'Activity arrives with the thread screen in F3.'`
   (`overview.dart:377`) and the `Text` that draws it; F3 shipped.

9. **Gallery/state fixtures.** `app/test/support/screens.dart` builds
   `Overview`/`Inbox` with a `StateKind? state`; keep those fixtures and make
   them compile against the new `Inbox.sources` parameter by passing
   `sources: const []`. Add nothing else: no kit change is made by this plan,
   so no golden changes.

10. **Tests — `app/test/ui/overview_test.dart`**, exactly these additions:
    - `the needs-you count is the total, not the drawn three` — five asks:
      the label reads `5`, three cards are drawn, and
      `'2 more in the inbox.'` is present.
    - `a factory ask opens its item and offers nothing that posts` — a
      `completion` ask: `'Open in the factory'` present, `'Complete'` absent,
      tapping it yields the item's `Place`.
    - `every card names its source` — `'Factory · marshal · Project-wide'`
      for the factory ask and `'Fleet · Everyone'` for the fleet ask.
    - `a stale factory is named while the fleet's rows stay` — one
      `SourceState(kind: factory, host: 'local', loaded: true, failure: 'offline')`:
      the freshness row is present with `offline`, and the project row still
      draws its counts.
    - `a loaded factory is not hidden by an unreachable fleet` —
      `state: StateKind.unavailable` with a loaded factory source: the
      full-page message is absent and the factory card is drawn.
    - `the F3 placeholder is gone` — `'Activity arrives with the thread screen in F3.'`
      finds nothing.

11. **Tests — `app/test/ui/inbox_test.dart`**, exactly these additions:
    - `every ask has a source line, including the ones with no thread` —
      intake, review draft, network and pull fixtures each render their
      `provenance`; none renders an empty source line.
    - `the blocking filter counts what the model calls blocking` — a
      `completion` ask is not in `Blocking` and is in `To look at`, and the tab
      counts agree.
    - `an empty filter says which filter is empty` — `Blocking` with only an
      intake ask reads `'Nothing is blocking work.'`, and its action returns
      to `All`.
    - `a stale source is named with its own age` — a `SourceState` with
      `lastRead` four minutes before `now` and `failure: 'offline'` renders
      `4m old` and `offline`; no `'now'` or bare `'4m'` age appears.
    - `a ready pull with no checks never reads as passed` — one
      `PullView(group: PullGroup.ready)` with `checks` empty: the Ready tab
      shows the card and the screen contains no `checks passed`.

**Verify:** from `app/`:

```bash
flutter test test/ui/overview_test.dart test/ui/inbox_test.dart
flutter analyze
dart format --output=none --set-exit-if-changed lib test integration_test
```

Expected: all pass, `analyze` clean, format reports no changed file.

## Final verification and integration

From `app/`:

```bash
flutter analyze
flutter test
dart format --output=none --set-exit-if-changed lib test integration_test
```

All three must pass. No Go file, wire type or `contract.g.dart` changes are
made by this plan, so the bridge suite and the contract generator currentness
test are not required; if a task finds it needs one, it stops and reports
rather than editing the generated contract.

**Evidence split to record in the PR description:** the widget and model tests
are fixture evidence over in-process fakes; none of them is a live bridge,
multi-device or physical-phone claim. The gallery goldens are unchanged
because the kit is unchanged.

**Reconcile before merge:** PR #154 (`studio/planning-handoff`) touches
`planning_screen.dart`, `handoff_sheet.dart`, `drafts_model.dart` and the
shell's planning places — none of this plan's files. Rebase on `main` after it
lands and re-run the three commands.
