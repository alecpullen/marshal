import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/placeholder.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../model/support.dart';
import '../support/fake_bridge.dart';
import '../support/screens.dart';

/// The shell's rules (F2 decision 1 and §4): a row opens its place, the
/// narrow layout reaches the same navigation through the app bar, a place a
/// later phase brings says so, and a host that is away is listed with the
/// time it was last seen.
/// The session a thread place opens: one row of the fleet's index, with the
/// title the thread is called by.
SessionRow _sessionRow() => const SessionRow(
  sessionID: 'sess_1',
  title: 'Fix the login bug',
  updatedAt: 1763300000000,
  messageCount: 3,
  open: true,
  viewers: 0,
  busy: false,
);

/// One host listing [_sessionRow], read through the real client: the shell's
/// session place resolves against the fleet's index, not against a fixture it
/// is handed.
Future<ModelFixture> _fixtureListingSession(WidgetTester tester) async {
  final fixture = (await tester.runAsync(
    () => ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/marshal')],
        // One agent keeps the shell from reading the host as unreachable: the
        // session's own place does not depend on it.
        agents: [agent(id: 'a-1', project: '/home/u/marshal')],
      ),
    ]),
  ))!;
  fixture.serve(
    'local',
    '/api/sessions/all',
    SessionIndex(
      projects: [
        ProjectSessions(root: '/home/u/marshal', sessions: [_sessionRow()]),
      ],
    ),
  );
  await tester.runAsync(() => fixture.refresh());
  return fixture;
}

/// A store that already holds a transcript with nothing in it: the session
/// thread's chrome is what the test reads.
StackStore _emptyStore(String sessionId) => StackStore.seeded(
  StackSnapshot(sessionID: sessionId, rev: 0, roots: const [], nodes: const []),
);

/// A session stream that never connects: its transport fails at once and its
/// backoff never comes due, so the test leaves no timer pending.
EventStream _quietStream() => EventStream(
  _DeadTransport(),
  '/api/events',
  wait: (_) => Completer<void>().future,
  jitter: 0,
);

class _DeadTransport implements Transport {
  @override
  Future<Response> send(Request request) async =>
      throw StateError('the shell test does not send requests');

  @override
  Stream<String> lines(Request request) =>
      throw StateError('the shell test does not open streams');
}

/// The session thread's place, pumped through the Studio: the only widget that
/// resolves a thread place to a session (F2 decision 1).
Future<void> _pumpSessionPlace(
  WidgetTester tester,
  ModelFixture fixture, {
  required StackStore Function(String sessionId) storeFor,
}) async {
  final inbox = InboxModel(fixture.store, client: fixture.clientFor);
  addTearDown(inbox.dispose);
  await tester.pumpWidget(
    EmberApp(
      brightness: Brightness.light,
      home: Studio(
        store: fixture.store,
        inbox: inbox,
        threadStore: (thread) => storeFor(thread.id),
        initialPlace: const Place(
          PlaceKind.thread,
          host: 'local',
          id: 'sess_1',
        ),
      ),
    ),
  );
  // The fleet's read and the place's own store settle over two frames.
  await tester.pump();
  await tester.pump();
}

/// The shell's rules (F2 decision 1 and §4): a row opens its place, the
/// narrow layout reaches the same navigation through the app bar, a place a
/// later phase brings says so, and a host that is away is listed with the
/// time it was last seen.

void main() {
  /// The shell with a recorder for the place it opened.
  (Widget, List<Place>) shellAt(
    Place place, {
    StateKind? state,
    VoidCallback? onNew,
  }) {
    final opened = <Place>[];
    final nav = shellNavFor(place);
    return (
      Shell(
        nav: nav,
        hosts: shellHosts,
        place: place,
        state: state,
        now: shellNow,
        onRetry: () {},
        onSelect: opened.add,
        onNew: onNew,
        content: (_) => plannedPlace(place, nav),
      ),
      opened,
    );
  }

  testWidgets('selecting a row changes the place', (tester) async {
    final (shell, opened) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.tap(find.text('Inbox'));
    await tester.pump();

    expect(opened, [const Place(PlaceKind.inbox)]);
  });

  testWidgets('a thread row opens its thread', (tester) async {
    final (shell, opened) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.tap(find.text('Reliable session recovery'));
    await tester.pump();

    expect(opened, [const Place(PlaceKind.thread, host: 'local', id: 'a-1')]);
  });

  testWidgets('drawer below 760', (tester) async {
    tester.view.physicalSize = const Size(390, 800);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final (shell, opened) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    // The navigation is behind the app bar's button, not beside the content.
    expect(find.text('PROJECTS'), findsNothing);
    expect(find.text('Inbox'), findsNothing);

    await tester.tap(find.byTooltip('Projects and threads').first);
    await tester.pump();
    expect(find.text('PROJECTS'), findsOneWidget);

    // Choosing a place closes the panel and opens it.
    await tester.tap(find.text('Inbox'));
    await tester.pump();
    expect(opened, [const Place(PlaceKind.inbox)]);
    expect(find.text('PROJECTS'), findsNothing);
  });

  testWidgets('planned places say so', (tester) async {
    final (shell, _) = shellAt(const Place(PlaceKind.settings));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    final planned = tester
        .widgetList<StateMessage>(find.byType(StateMessage))
        .where((message) => message.kind == StateKind.planned);
    expect(planned, isNotEmpty);
    expect(find.text('Settings'), findsWidgets);
    expect(find.textContaining('later phase'), findsOneWidget);
  });

  testWidgets('a thread no host lists says so, not that a phase brings it', (
    tester,
  ) async {
    // A stale bookmark, or a legacy `#runs/<id>` for a thread that has since
    // gone: the thread screen shipped in F3, so promising it would be a lie.
    final (shell, _) = shellAt(
      const Place(PlaceKind.thread, host: 'local', id: 'gone'),
    );
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    expect(
      find.textContaining('No host in this fleet lists that thread'),
      findsOneWidget,
    );
    expect(find.textContaining('arrives in F3'), findsNothing);
  });

  testWidgets('a hostless thread link finds the host that lists it', (
    tester,
  ) async {
    // A link written for the one-bridge Studio names no host; the thread it
    // names is still shown, with its own title.
    final (shell, _) = shellAt(const Place(PlaceKind.thread, id: 'a-1'));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    expect(find.text('Reliable session recovery'), findsWidgets);
    expect(find.textContaining('is built'), findsOneWidget);
    expect(find.textContaining('No host in this fleet'), findsNothing);
    expect(find.textContaining('arrives in F3'), findsNothing);
  });

  testWidgets('places this phase builds say they are built', (tester) async {
    // Automations and Library are real screens registered with the gallery;
    // the shell simply has no per-host bridge to wire them yet, and a promise
    // of a later phase would be untrue.
    for (final kind in [PlaceKind.automations, PlaceKind.library]) {
      final (shell, _) = shellAt(Place(kind));
      await tester.pumpWidget(
        EmberApp(brightness: Brightness.light, home: shell),
      );
      await tester.pump();

      expect(find.textContaining('is built'), findsOneWidget);
      expect(find.textContaining('per-host bridge'), findsOneWidget);
      expect(find.textContaining('later phase'), findsNothing);
    }

    // Pull requests is built and wired: a shell handed no pull model is the
    // only one that falls back to the promise, and it says the screen exists.
    final (shell, _) = shellAt(const Place(PlaceKind.pulls));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );
    await tester.pump();
    expect(find.textContaining('is built'), findsOneWidget);
    expect(find.textContaining('later phase'), findsNothing);
  });

  testWidgets('an unreachable host is listed with its age', (tester) async {
    final (shell, _) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.tap(find.text('All hosts'));
    // The popover has no animation, but the anchor rebuilds over two frames;
    // settling would wait for the running dot's pulse forever.
    await tester.pump();
    await tester.pump();

    expect(find.text('studio'), findsOneWidget);
    expect(find.text('unreachable 4m'), findsOneWidget);
    // The host that is answering says so, in the same words.
    expect(find.text('live'), findsOneWidget);
  });

  testWidgets('the wide layout draws the sidebar and no app bar', (
    tester,
  ) async {
    final (shell, _) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    expect(find.text('PROJECTS'), findsOneWidget);
    expect(find.byTooltip('Projects and threads'), findsNothing);
    // The search field is drawn where it will live, disabled, and says why.
    expect(find.text('Search'), findsOneWidget);
    expect(find.byTooltip('Search arrives in a later phase'), findsOneWidget);
  });

  testWidgets('the narrow app bar has New', (tester) async {
    tester.view.physicalSize = const Size(600, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final (shell, _) = shellAt(const Place.overview(), onNew: () {});
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    expect(find.byTooltip('New'), findsOneWidget);
  });

  testWidgets('the sidebar has New beside search', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final (shell, _) = shellAt(const Place.overview(), onNew: () {});
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    // The wide layout draws the sidebar and no app bar, so the one plus is
    // the sidebar's.
    expect(find.byTooltip('New'), findsOneWidget);
    expect(find.byTooltip('Projects and threads'), findsNothing);
  });

  testWidgets('New starts work in the first project', (tester) async {
    tester.view.physicalSize = const Size(600, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final started = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(name: 'local', projects: [project('/home/u/marshal')]),
      ], now: DateTime.utc(2026, 11, 17, 12)),
    );
    final fixture = started!;
    await tester.runAsync(() => fixture.refresh());
    fixture.bridges['local']!
      ..reply(
        'GET',
        '/api/prompts/recent',
        const FakeReply(200, body: '{"prompts":[]}'),
      )
      ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'));
    final inboxModel = inbox.InboxModel(
      fixture.store,
      client: fixture.clientFor,
    );
    addTearDown(inboxModel.dispose);

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(
          store: fixture.store,
          inbox: inboxModel,
          newWorkFor: (host, root) => NewWorkModel.forProject(
            project('/home/u/marshal'),
            fixture.clientFor('local'),
            host: 'local',
          ),
        ),
      ),
    );
    // The fleet store has a live stream and a clock, so nothing ever settles:
    // the Studio's own tests pump explicitly (test/ui/f2_flow_test).
    for (var i = 0; i < 8; i++) {
      await tester.pump();
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 30)),
      );
    }
    await tester.pump();

    await tester.tap(find.byTooltip('New'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));

    expect(find.text('Start a new agent'), findsOneWidget);
  });

  group('the fleet state (§7)', () {
    HostView host(String name, HostReach reach) => HostView(
      name: name,
      kind: HostKind.local,
      reach: reach,
      lastContact: shellNow,
    );

    StateKind? kindFor(List<HostView> hosts, {bool anyAgents = false}) =>
        fleetStateKind(
          read: true,
          failed: false,
          anyAgents: anyAgents,
          hosts: hosts,
        );

    test('a refused token is unauthorized, never unreachable', () {
      expect(
        kindFor([host('local', HostReach.unauthorized)]),
        StateKind.unauthorized,
        reason:
            'a page whose token the bridge refused must say so: reporting it '
            'as unreachability sends the reader to look at the wrong thing',
      );
    });

    test('a host nothing answers on is unavailable', () {
      expect(
        kindFor([host('local', HostReach.unreachable)]),
        StateKind.unavailable,
      );
    });

    test('a refusal explains a fleet of refusals better than a silence', () {
      expect(
        kindFor([
          host('local', HostReach.unreachable),
          host('studio', HostReach.unauthorized),
        ]),
        StateKind.unauthorized,
      );
    });

    test('a fleet still showing agents is stale, not a state page', () {
      expect(
        kindFor([
          host('local', HostReach.live),
          host('studio', HostReach.unauthorized),
        ], anyAgents: true),
        StateKind.stale,
      );
    });

    test('a fleet with nothing to show and no answers is empty or error', () {
      expect(kindFor([]), StateKind.empty);
      expect(
        fleetStateKind(read: true, failed: true, anyAgents: false, hosts: []),
        StateKind.error,
      );
      expect(
        fleetStateKind(
          read: false,
          failed: false,
          anyAgents: false,
          hosts: [host('local', HostReach.live)],
        ),
        StateKind.loading,
      );
    });
  });

  testWidgets('a session\'s thread place opens the session thread', (
    tester,
  ) async {
    // The session's id is not an agent's: the place resolves through the
    // fleet's index, which lists the sessions the bridge holds (P7).
    final fixture = await _fixtureListingSession(tester);
    await _pumpSessionPlace(tester, fixture, storeFor: _emptyStore);

    final screen = find.byType(ThreadScreen);
    expect(screen, findsOneWidget, reason: 'a session is a thread of its own');
    expect(
      find.descendant(of: screen, matching: find.text('Fix the login bug')),
      findsWidgets,
      reason: 'the session is called by its own title',
    );
    expect(
      find.descendant(of: screen, matching: find.textContaining('Session')),
      findsWidgets,
      reason: 'the header names the thread\'s kind',
    );
  });

  testWidgets('a gone session offers reopen', (tester) async {
    // The session was closed while idle: the bridge answers the snapshot with
    // a 404, so the thread screen offers to open it again (P7 decisions 5–7).
    final fixture = await _fixtureListingSession(tester);
    var opens = 0;
    final store = StackStore(
      fixture.clientFor('local'),
      'sess_1',
      fetcher: (sessionId, subagentId) async =>
          throw BridgeFailure(404, '{"error":"no such session"}'),
      events: _quietStream,
      opener: () async {
        opens++;
      },
    );

    await _pumpSessionPlace(tester, fixture, storeFor: (_) => store);
    // The open, the fetch and the store's notification take more than the two
    // frames the place itself needs.
    await tester.pump();
    await tester.pump();
    expect(store.status, StackStatus.gone);
    expect(opens, 1, reason: 'the place opened the session before reading it');

    // The screen reads its store when it builds, and the store answered after
    // those frames: one more build of the same place draws its answer.
    await _pumpSessionPlace(tester, fixture, storeFor: (_) => store);

    expect(find.text('This session was closed while idle.'), findsOneWidget);
    expect(find.text('Reopen'), findsOneWidget);

    await tester.tap(find.text('Reopen'));
    await tester.pump();
    await tester.pump();

    expect(opens, 2, reason: 'Reopen opens the session again');
  });
}
