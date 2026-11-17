# P7 plan 2: sessions as threads in the Studio — implementation plan

Date: 2026-11-18. Status: ready for review.

Base: `main` at `bdd20ddc`. Every anchor below was checked there.
**It runs after [plan 1](2026-11-18-p7-1-bridge-plan.md)** is on `main`.
Re-check the names plan 1's "What plan 2 relies on" declares before starting:
the generated contract types, `GET /api/sessions/all`, `load`'s 409, and the
`sessions` delta.

Approved design: [p7-studio-sessions.md](../p7-studio-sessions.md): D7,
D9, D11's app half, D12–D14; Error handling's app half; Testing's app list
and the acceptance run. Rules:
[design-system.md](../../studio-flutter/design-system.md), normative for
everything under `app/lib/src/ui/`.
Previous plans: [F2, the shell](../../studio-flutter/plans/2026-11-17-f2-shell-plan.md),
[F3, the agent thread](../../studio-flutter/plans/2026-11-17-f3-agent-thread-plan.md)
and [F6 plan 2, the project page](../../studio-flutter/plans/2026-11-17-f6-2-planning-runs-project-plan.md),
whose sidebar, thread screen and project page this plan extends.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A session is a thread** of a fourth kind, keyed by host and session id,
  opened at `/thread/<host>/<sessionId>`.
- **Open sessions are current threads** in their project's tree. Closed ones
  count toward its Earlier. The project page lists both.
- **Opening a closed session reopens it** first. A session the idle sweep
  closed says so and offers Reopen.
- **A session's parked ask** is the same ask card an agent's is, answerable
  from the Inbox, the Overview and the thread.
- **The acceptance run** in the spec passes by hand.

## Non-goals

- **A Sessions place.** There is none (spec D13).
- **Sessions on the Overview's Running now.** The Overview shows a session's
  ask through the Inbox model, and nothing else about sessions.
- **Steering a terminal's turn from the composer.** A prompt during it
  answers 409, which the composer reports, as it does for an agent.
- **Phase 2:** host discovery beyond today's, the relay.

## Assumptions

Each was checked at the base.

- **Hosts.** `HostRegistry` and `Host` (`app/lib/src/client/host.dart`).
  `FleetStore` (`app/lib/src/model/fleet_store.dart`) keeps per-host state in
  `_HostState` (`:512`).
  - It reads a snapshot in `refresh` (`:93`), with a tolerant side read in
    `_budgets` (`:167`).
  - It applies stream deltas in `_applyDelta` (`:246`), whose `default` case
    hands an unknown kind to `_applyFleetDelta`, which ignores it.
  - It re-reads with one read in flight per host and a coalesced follow-up in
    `_refreshAgents` (`:409`).
- **Threads.** `ThreadRef` and `ThreadKind` (`app/lib/src/model/thread_ref.dart:8`,
  `:16`). `threadFor` (`:125`) maps an agent. `threadOfItem`
  (`work_item_model.dart`) is the model for a mapping function.
- **The tree.** `isEarlier` (`nav_model.dart:105`) decides Earlier.
  `buildNavModel` gathers each host's threads and groups them by project,
  keeping a work item whose project the host did not list. The sidebar maps
  the model's kind to the kit's in `_kind` (`sidebar.dart:224`).
- **Switches over the model's `ThreadKind`:**
  - `sidebar.dart:224`
  - `pull_facts.dart:83`
  - `thread_screen.dart:343` (`_inspectorTabs`)
  - `thread_header.dart:139`
  - `pulls_store.dart:81` (`_belongsTo`)
  - `inspector_state.dart:42` (`tabsForThread`)

  Dart's exhaustive switches fail to compile until each handles the new kind.
- **The kit's kinds.** `ThreadKind` in `app/lib/src/ui/kit/vocabulary.dart:34`:
  agent, work item and plan run, each with a word and a Cupertino icon.
  `CupertinoIcons.desktopcomputer` exists in the locked `cupertino_ui` 1.1.6.
  The gallery's thread rows are `_threads()` (`ui/gallery/gallery.dart:797`).
- **A thread place.** The shell resolves one in `_threadOn`
  (`shell.dart:770`), and `_ThreadPlace` (`:849`) builds its store with
  `widget.buildStore` and starts it (`:900-907`).
  - `main.dart:132` builds `StackStore(client, thread.id)` and
    `ComposerModel.forSession(client, thread.id)` on the thread's host.
  - Every route either calls (`/stack`, `/api/events?sessionId=`, `/prompt`,
    `/cancel`, `/mode`, `/access`, `/roster`, `/nodes`, `/last-request`,
    `/step-diffs`) is per-session, so both work for a session id unchanged.
- **The stack store.** `StackStore` (`model/stack_store.dart:52`):
  - `start` (`:133`) opens the stream, then refreshes;
  - `_fetchOnce` (`:182`) turns a 501 into `unsupported` and anything else
    into `error`;
  - the thread screen draws `loading`, `unsupported` and `error` in `_thread`
    (`thread_screen.dart:305-320`).
- **The composer** keeps a failed prompt's draft and shows the bridge's words
  (`composer_model.dart:106-121`). A 409 needs nothing new.
- **The project page.**
  - The Work tab lists its agents under `Label('Agents', …)` with `Rows` of
    `RowItem`, or one sentence when there are none
    (`ui/project/project_page.dart:174-194`).
  - The shell builds its `agents` from the store (`shell.dart:1409-1416`).
- **Asks.**
  - `InboxModel._rebuild` (`inbox_model.dart:559`) projects the asks.
  - `_askForPending` (`:700`) builds an approval or question ask from an
    agent and a `PendingRequest`.
  - Answers post `answerPermission` / `answerQuestion` (`:460-465`).
  - The thread screen pins the asks whose thread is its own.
- **Route parity.** `route_parity_test.dart` fails on a client route with no
  row, or a `dropped:` row the client issues. Plan 1 adds
  `GET /api/sessions/all` as `dropped:`. `POST /api/sessions/X/load` is
  `dropped:` today.
- **Screens.** They are registered in `app/test/support/screens.dart` as
  `ScreenCase`s. `'thread'` (`:1392`) and `'project · work'` (`:1203`) are the
  models.

## Decisions this plan makes beyond the spec

1. **The store holds rows, the thread is derived.** `FleetStore` keeps each
   host's `ProjectSessions` groups. `threadOfSession` derives a `ThreadRef` on
   read, as `threadFor` does for agents.
2. **A session's title** is the row's trimmed title. With none, it is
   `Session ` and the first eight characters of the id after a leading
   `sess_`.
3. **A session's headline** is "A terminal is attached" when its viewers are
   above zero, and empty otherwise.
4. **`ThreadRef.closed`** marks a closed session. `isEarlier` is true for a
   closed thread whatever its age, so a closed session never crowds the tree.
5. **The stack store opens before it follows.** A store given an `opener`
   awaits it before it opens the stream and fetches, so the first event
   stream does not race the reopening. `reopen()` runs the opener again,
   then refreshes.
6. **`gone` is a session's state only.** A 404 from the stack fetch is `gone`
   in a store that has an opener. An agent's store keeps today's `error`.
7. **A failed open's words are shown.** The store keeps the bridge's message
   as `failure`. The thread screen's error state shows it in place of the
   generic sentence, so a 409 names the session's owner.
8. **The project page's Sessions group** comes after Agents. It lists open
   sessions, then closed ones, newest first. Each row's description is
   "Open" or "Closed", the relative age and the message count. A group with
   no rows shows one sentence: "Sessions started with marshal in this project
   appear here." A per-root `error` shows as `StateMessage.error` with Try
   again, above the rows it still has.

## Execution

- After plan 1 is on `main`: `git switch -c p7-2-studio` from `main`, or
  create a worktree on that branch.
- Run each task's Verify and commit it as `p7-2-studio: task N — <title>`.
- Stage explicit paths only.
- Run `dart format` on every Dart file a task touches before its Verify.
- Each test this plan adds is proved by mutation: revert the line it covers
  and watch it fail, then restore with `git checkout`.
- Goldens are regenerated on macOS only:
  `cd app && flutter test --update-goldens test/gallery_test.dart`.

## Contracts

### Client (`app/lib/src/client/bridge_client.dart`, Task 2)

```dart
/// GET /api/sessions/all: every project's sessions on this host, or [root]'s.
Future<SessionIndex> sessionIndex({String? root});

/// POST /api/sessions/{id}/load: opens a closed session on its project's
/// child; an open one is answered as it is.
Future<void> openSession(String sessionId);
```

### Store (`app/lib/src/model/fleet_store.dart`, Task 2)

```dart
/// One session row and where it lives.
@immutable
class HostedSession {
  const HostedSession(this.host, this.root, this.row);
  final String host;
  final String root;
  final SessionRow row;
}

// FleetStore gains:
List<HostedSession> sessions({String? host});      // every host's, or one's
HostedSession? session(String host, String sessionId);
String? sessionsError(String host, String root);   // the root's listing error
Future<void> refreshSessions(String host, {String? root});
```

### Threads (`app/lib/src/model/thread_ref.dart`, Task 3)

```dart
enum ThreadKind { agent, workItem, planRun, session }

// ThreadRef gains, with == and hashCode updated:
  /// A closed session: stored, with no runtime on its daemon.
  final bool closed; // default false

/// A session's status: needs you when it has a pending request, running when
/// busy, idle otherwise.
ThreadStatus statusOfSession(SessionRow row);

/// The thread a session row is (decisions 2–4).
ThreadRef threadOfSession(String host, String root, SessionRow row);
```

### Kit (`app/lib/src/ui/kit/vocabulary.dart`, Task 1)

```dart
enum ThreadKind {
  agent('Agent', CupertinoIcons.chat_bubble),
  workItem('Work item', CupertinoIcons.square_stack_3d_up),
  planRun('Plan run', CupertinoIcons.arrow_branch),
  session('Session', CupertinoIcons.desktopcomputer);
  // …
}
```

### Stack store (`app/lib/src/model/stack_store.dart`, Task 4)

```dart
enum StackStatus { loading, ready, unsupported, error, gone }

// StackStore's constructor gains:
  Future<void> Function()? opener,

// StackStore gains:
  /// The bridge's words for the last failure, or null.
  String? get failure;

  /// Runs the opener again, then refetches (decision 5).
  Future<void> reopen();
```

### Project page (`app/lib/src/ui/project/project_page.dart`, Task 5)

```dart
// ProjectPage gains:
  final List<threads.ThreadRef> sessions;            // open first, then closed
  final String? sessionsError;
  final void Function(threads.ThreadRef session)? onOpenSession;
  final VoidCallback? onRetrySessions;
```

## Task 1 — the session kind in the design system

**Files:** `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/vocabulary.dart`, `app/lib/src/ui/gallery/gallery.dart`,
`app/test/kit/thread_row_test.dart`, `app/test/goldens/gallery_light.png`,
`app/test/goldens/gallery_dark.png`.

1. **design-system.md §6.** It says four kinds of thread: agent, work item,
   plan run and session. Add one sentence: a session is a conversation a
   daemon's project holds that no agent owns, usually one a terminal started,
   and its icon is the desktop computer.
2. Add `session` to the kit's `ThreadKind` as Contracts gives it.
3. Add a sixth row to the gallery's `_threads()`: status idle, title
   "Terminal session", kind `session`.
4. In `thread_row_test.dart`, add `a session row's kind has its tooltip`: the
   row's icon carries the tooltip "Session".
5. Regenerate the gallery goldens on macOS.

**Verify:** `cd app && flutter test test/kit/thread_row_test.dart test/gallery_test.dart test/tooltips_test.dart`.

## Task 2 — the client and the store read the index

**Files:** `app/lib/src/client/bridge_client.dart`,
`app/lib/src/model/fleet_store.dart`, `docs/studio-flutter/route-parity.md`,
`app/test/client/bridge_client_test.dart`,
`app/test/model/fleet_store_test.dart`, `app/test/support/fake_bridge.dart`
(only if a route helper is missing).

1. **Client.** `sessionIndex` reads `/api/sessions/all` with `root` as a
   query when set. `openSession` posts `/api/sessions/{id}/load` with no
   body. Both are written as the neighbouring methods are.
2. **Route parity.** The `GET /api/sessions/all` row names `sessionIndex`,
   and the `POST /api/sessions/X/load` row names `openSession`. Both drop
   their `dropped:` notes.
3. **Store state.** `_HostState` gains the host's groups, empty until read.
4. **The snapshot.** `refresh` reads the index beside the budgets, through a
   tolerant helper written as `_budgets` is:
   - a 404 or 501 (an older bridge) leaves the groups empty;
   - any other failure keeps the groups held.

   The index never fails the snapshot.
5. **The delta.** `_applyDelta` gains a `sessions` case that decodes
   `SessionsDelta` and calls `refreshSessions(host, root: delta.project)`
   unawaited.
6. **Re-reads.** `refreshSessions`:
   - It keeps one read in flight per host and coalesces the roots asked for
     meanwhile into one follow-up, as `_refreshAgents` does.
   - A read of one root replaces that root's group, adds it, or removes it
     when the answer has no group for it.
   - A read with no root replaces them all.
   - A failed read keeps the groups.
7. **Accessors.**
   - `sessions` flattens the groups, with their host and root.
   - `session` finds one by host and id.
   - `sessionsError` is a root's `error`.
8. Tests:
   - `bridge_client_test.dart`: `sessionIndex reads the index`,
     `sessionIndex narrows to a root`, `openSession posts load`.
   - `fleet_store_test.dart`:
     - `the snapshot reads the session index`;
     - `an older bridge's 404 leaves no sessions and fails nothing`;
     - `a sessions delta re-reads its root only`;
     - `a burst of sessions deltas costs at most two reads`;
     - `a root missing from the answer is removed`;
     - `a failed re-read keeps the rows`;
     - `a root's error is reported`.

**Verify:** `cd app && flutter test test/client/bridge_client_test.dart test/model/fleet_store_test.dart test/route_parity_test.dart`.

**Mutation check:** in `refreshSessions`, replace every group on a rooted
read; `a sessions delta re-reads its root only` must fail. Restore.

## Task 3 — sessions are threads in the tree

**Files:** `app/lib/src/model/thread_ref.dart`,
`app/lib/src/model/nav_model.dart`, `app/lib/src/model/inspector_state.dart`,
`app/lib/src/model/pulls_store.dart`, `app/lib/src/ui/shell/sidebar.dart`,
`app/lib/src/ui/pulls/pull_facts.dart`,
`app/lib/src/ui/thread/thread_header.dart`,
`app/lib/src/ui/thread/thread_screen.dart`,
`app/test/model/thread_ref_test.dart`, `app/test/model/nav_model_test.dart`.

1. **The model.** Add `session` to the model's `ThreadKind` and `closed` to
   `ThreadRef`, as Contracts gives them.
2. **The mapping.** `statusOfSession` and `threadOfSession` follow decisions
   2–4:
   - the row's `updatedAt` is `lastActive`, in UTC;
   - `closed` is `!row.open`;
   - the project is the root.
3. **Earlier.** `isEarlier` is true for a closed thread, and otherwise keeps
   its rule.
4. **The tree.** `buildNavModel` adds each host's
   `store.sessions(host: …)` as threads beside its agents and items. A
   session whose project the host has not listed still gets its project row,
   as a work item does.
5. **The kind switches.**
   - `sidebar.dart` and `pull_facts.dart` map `session` to the kit's
     `session`.
   - `thread_header.dart` words it "Session".
   - `pulls_store.dart`'s `_belongsTo` gives a session no id, so no pull
     request belongs to one.
   - `tabsForThread` and the thread screen's `_inspectorTabs` give a session
     `[InspectorTab.inspect]`.
6. Tests:
   - `thread_ref_test.dart`:
     - `a session with a pending request needs you`;
     - `a busy session is running`;
     - `an idle open session is idle`;
     - `an untitled session is named by its short id`;
     - `an attached terminal is the headline`;
     - `a closed row is a closed thread`.
   - `nav_model_test.dart`:
     - `an open session is a current thread under its project`;
     - `a closed session counts toward earlier`;
     - `a session on an unlisted project still has its project row`;
     - `sessions on two hosts share one project row`.

**Verify:** `cd app && flutter test test/model/ && flutter analyze`.

**Mutation check:** drop the `closed` clause from `isEarlier`;
`a closed session counts toward earlier` must fail. Restore.

## Task 4 — opening a session

**Files:** `app/lib/src/model/stack_store.dart`, `app/lib/main.dart`,
`app/lib/src/ui/shell/shell.dart`, `app/lib/src/ui/thread/thread_screen.dart`,
`app/test/model/stack_store_test.dart`, `app/test/ui/shell_test.dart`,
`app/test/support/screens.dart`.

1. **The stack store** (decisions 5–7):
   - When it has an `opener`, `start` sets `loading` and awaits it before it
     opens the stream and refreshes.
   - A failed opener sets `error` with the failure's message as `failure`,
     and opens nothing.
   - In a store with an opener, a 404 from the fetch sets `gone`. Without
     one, a 404 is `error`, as today.
   - `reopen` sets `loading`, runs the opener, and on success opens the
     stream if it is not open and refreshes.
2. **`main.dart`'s `threadStore`** passes
   `opener: () => client.openSession(thread.id)` for a thread whose kind is
   `session`, and nothing otherwise.
3. **The shell.** `_threadOn` answers a session on that host through
   `store.session(host, id)` and `threadOfSession`, after the work items and
   before the agents. An id that is both cannot happen: agents and sessions
   come from disjoint ids, because the bridge excludes agents' sessions from
   the index.
4. **The thread screen's `_thread`:**
   - for `gone`: `StateMessage.error` with the message "This session was
     closed while idle." and the action "Reopen", which calls
     `store.reopen`;
   - for `error`: `store.failure` as the message when it is set, else
     today's sentence.
5. **Screens.** Register `'session thread'`, built as `'thread'` is with a
   session `ThreadRef`, a seeded store, and the inspector on Inspect, with
   every required state.
6. Tests:
   - `stack_store_test.dart`:
     - `a store with an opener opens before it follows`: the opener
       completes before the first fetch and before the stream starts;
     - `a failed open is an error with the bridge's words`;
     - `a 404 for an opened session is gone`;
     - `a 404 for an agent is still an error`;
     - `reopen opens again and refetches`.
   - `shell_test.dart`:
     - `a session's thread place opens the session thread`: with a store
       holding a session row, `/thread/<host>/<id>` shows the thread screen
       with the session's title and the Session word;
     - `a gone session offers reopen`.

**Verify:** `cd app && flutter test`.

**Mutation check:** start the stream before awaiting the opener;
`a store with an opener opens before it follows` must fail. Restore.

## Task 5 — the project page, and a session's ask

**Files:** `app/lib/src/ui/project/project_page.dart`,
`app/lib/src/ui/shell/shell.dart`, `app/lib/src/model/inbox_model.dart`,
`app/test/ui/project/project_page_test.dart`,
`app/test/model/inbox_model_test.dart`, `app/test/support/screens.dart`.

1. **The Sessions group.** `ProjectPage` takes the Contracts' four fields and
   draws them after Agents, as decision 8 says, with `Label('Sessions',
   count: …)`, `Rows` and `RowItem` as the Agents group does. Choosing a row
   calls `onOpenSession`.
2. **The shell's project place** passes the root's sessions on that host,
   sorted open first and then by `lastActive` newest first. It also passes
   `store.sessionsError(host, root)`, the open callback as `onOpenAgent`'s
   navigation is, and `onRetrySessions` as
   `store.refreshSessions(host, root: root)`.
3. **The ask.** Split `_askForPending` into `_askForPendingOn(host, thread,
   pending, since)`. The agent's call passes `threadFor(host, agent)` and
   `since ?? lastActiveOf(agent)`. In `_rebuild`, after the agents' pending
   loop, add one ask per hosted session whose row has `pending`, with
   `threadOfSession`'s thread and its `lastActive` as `since`. Answers need
   nothing new: they post by the request's id.
4. **Screens.** `'project · work'`'s fixture gains one open and one closed
   session, so the shared tests reach the group.
5. Tests:
   - `project_page_test.dart`:
     - `sessions list open first, then closed`;
     - `a row says open or closed, its age and its messages`;
     - `no sessions shows the sentence`;
     - `a root's error shows with try again`;
     - `choosing a session opens it`.
   - `inbox_model_test.dart`:
     - `a session's parked approval is an ask on its thread`;
     - `answering it posts the permission`;
     - `a session without pending asks nothing`.

**Verify:** `cd app && flutter test`.

**Mutation check:** skip the sessions loop in `_rebuild`;
`a session's parked approval is an ask on its thread` must fail. Restore.

## Task 6 — acceptance, the record and full verification

**Files:** `docs/daemon/p7-studio-sessions.md`.

1. **The acceptance run,** by hand, as the spec's Testing section lists it:
   1. Build both binaries and the Studio, and restart the daemon and the
      Studio with `scripts/dev-studio.sh`.
   2. Run two `marshal` sessions in terminals on one project.
   3. Open the Studio with `marshal studio`.
   4. Record each of the six steps with what the Studio showed. A step that
      fails is recorded as failed, with what happened.
2. **The record.** Fill the spec's "Plan 2" record:
   - what landed;
   - any decision the executor had to make;
   - the mutation checks and their results;
   - the acceptance run.
3. **Full verification:**
   - `cd app && flutter analyze`;
   - `flutter test`;
   - `dart format --output=none --set-exit-if-changed lib test`;
   - on macOS, `flutter test test/gallery_test.dart` against the regenerated
     goldens;
   - `TMPDIR=/private/tmp/p7-2 go test ./web/bridge/... -count=1`, since the
     route-parity table is shared.

**Verify:** the full verification above.

## Done when

- A terminal's sessions appear under their project in the Studio, open ones
  in the tree and closed ones under Earlier and on the project page.
- A session's thread reads, streams and drives. A turn typed in the terminal
  shows as running, and a prompt during it reports the conflict.
- A closed session reopens on open. One the idle sweep closed says so and
  reopens on Reopen.
- A session's parked ask is answered from the Inbox.
- Every test named above passes, each mutation check fails as described, and
  the acceptance run is recorded.
