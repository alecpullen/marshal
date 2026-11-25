import 'dart:async';

import 'package:flutter/services.dart' show LogicalKeyboardKey, TextInputAction;
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart'
    show HostKind, HostReach, HostRegistry;
import 'package:marshal_app/src/model/fleet_store.dart'
    show FleetStore, HostView;
import 'package:marshal_app/src/model/general_model.dart';
import 'package:marshal_app/src/model/hosts_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart' show Density;
import 'package:marshal_app/src/ui/thread/thread_screen.dart';
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/sections/unsaved_guard.dart';
import 'package:marshal_app/src/ui/settings/settings_place.dart';
import 'package:marshal_app/src/ui/shell/host_switcher.dart';
import 'package:marshal_app/src/ui/shell/placeholder.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';
import 'package:marshal_app/src/ui/shell/sidebar.dart';

import '../model/support.dart';
import '../support/fake_bridge.dart';
import '../support/kit.dart';
import '../support/screens.dart';

/// The shell's rules (F2 decision 1 and §4): a row opens its place, the
/// narrow layout reaches the same navigation through the app bar, a place a
/// later phase brings says so, and a host that is away is listed with the
/// time it was last seen.
/// The session a thread place opens: one row of the fleet's index, with the
/// title the thread is called by.
SessionRow _sessionRow({bool open = true}) => SessionRow(
  sessionID: 'sess_1',
  title: 'Fix the login bug',
  updatedAt: 1763300000000,
  messageCount: 3,
  open: open,
  viewers: 0,
  busy: false,
);

/// One host listing [_sessionRow], read through the real client: the shell's
/// session place resolves against the fleet's index, not against a fixture it
/// is handed. A closed row is the project's earlier work.
Future<ModelFixture> _fixtureListingSession(
  WidgetTester tester, {
  bool open = true,
}) async {
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
        ProjectSessions(
          root: '/home/u/marshal',
          sessions: [_sessionRow(open: open)],
        ),
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
  Density density = Density.comfortable,
}) async {
  final inbox = InboxModel(fixture.store, client: fixture.clientFor);
  addTearDown(inbox.dispose);
  await tester.pumpWidget(
    EmberApp(
      brightness: Brightness.light,
      home: Studio(
        store: fixture.store,
        inbox: inbox,
        density: density,
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
    VoidCallback? onAddProject,
    UnsavedGuard? unsaved,
    HostPlaceBuilder? settingsFor,
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
        onAddProject: onAddProject,
        onLeaveSettings: () {},
        unsaved: unsaved,
        settingsFor: settingsFor,
        content: (_) => plannedPlace(place, nav),
      ),
      opened,
    );
  }

  /// The top line's switcher: the one above the place, drawn after the
  /// sidebar's.
  Finder topSwitcher() => find.byType(HostSwitcher).last;

  /// The shell at a per-host place, its three builders recording the host each
  /// was asked for, and the guard it consults when a test gives it one.
  (Widget, List<String>) hostShellAt(Place place, {UnsavedGuard? unsaved}) {
    final recorded = <String>[];
    final nav = shellNavFor(place);
    Widget? build(String host, int section, StateKind? state) {
      recorded.add(host);
      return Text('${placeTitle(place, nav)} at $host');
    }

    return (
      Shell(
        nav: nav,
        hosts: shellHosts,
        place: place,
        now: shellNow,
        onSelect: (_) {},
        unsaved: unsaved,
        automationsFor: build,
        libraryFor: build,
        settingsFor: build,
        content: (_) => plannedPlace(place, nav),
      ),
      recorded,
    );
  }

  /// Opens the top line's switcher and picks the host called [name].
  Future<void> chooseHost(WidgetTester tester, String name) async {
    await tester.tap(
      find.descendant(of: topSwitcher(), matching: find.byType(Btn)),
    );
    // The popover has no animation, but it builds over two frames.
    await tester.pump();
    await tester.pump();
    await tester.tap(find.text(name));
    await tester.pump();
  }

  testWidgets('the settings builder receives the switcher\'s host', (
    tester,
  ) async {
    final (shell, recorded) = hostShellAt(const Place(PlaceKind.settings));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    // The switcher's scope is All hosts until a host is chosen, and under it
    // the place reads the first host that answers.
    expect(recorded.last, 'local');
    expect(find.text('Settings at local'), findsOneWidget);

    await chooseHost(tester, 'studio');

    expect(recorded.last, 'studio');
    expect(find.text('Settings at studio'), findsOneWidget);
  });

  testWidgets('under all hosts the builder receives the first live host', (
    tester,
  ) async {
    final (shell, recorded) = hostShellAt(const Place(PlaceKind.library));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    expect(
      find.descendant(
        of: find.byType(HostSwitcher).first,
        matching: find.text('All hosts'),
      ),
      findsOneWidget,
    );
    // `shellHosts` lists an unreachable `studio` first and a live `local`
    // second: All hosts reaches past it to the host that is answering.
    expect(recorded, isNotEmpty);
    expect(
      recorded.every((host) => host == 'local'),
      isTrue,
      reason: '$recorded',
    );
  });

  testWidgets('a per-host place shows the switcher naming its host', (
    tester,
  ) async {
    final (shell, recording) = hostShellAt(const Place(PlaceKind.automations));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await chooseHost(tester, 'studio');

    expect(tester.widget<HostSwitcher>(topSwitcher()).selected, 'studio');
    expect(
      find.descendant(of: topSwitcher(), matching: find.text('studio')),
      findsOneWidget,
    );
    expect(recording.last, 'studio');
    expect(find.text('Automations at studio'), findsOneWidget);
  });

  testWidgets('changing the scope over an unsaved edit asks first', (
    tester,
  ) async {
    // A section holding an edit: the host changing under it would lose what
    // was typed, so the switcher asks as the sidebar does.
    final edits = HostsModel.fixture(shellHosts)
      ..edit(shellHosts.first)
      ..change(shellHosts.first);
    final guard = UnsavedGuard()..watch(edits);
    final (shell, recorded) = hostShellAt(
      const Place(PlaceKind.settings),
      unsaved: guard,
    );
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );
    expect(recorded.last, 'local');

    await chooseHost(tester, 'studio');
    await tester.pump();

    expect(find.text('Leave without saving?'), findsOneWidget);
    expect(recorded.contains('studio'), isFalse);

    await tester.tap(find.text('Discard the edit'));
    await tester.pump();
    await tester.pump();

    expect(recorded.last, 'studio');
    expect(edits.hasUnsavedEdits, isFalse);
    expect(find.text('Settings at studio'), findsOneWidget);
  });

  testWidgets('a per-host place keeps its switcher below 760', (tester) async {
    // The top line is drawn in the content area, which the narrow layout
    // fills: it must sit under the app bar, name its host, and not overflow.
    tester.view.physicalSize = const Size(390, 800);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final (shell, recorded) = hostShellAt(const Place(PlaceKind.automations));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );
    await tester.pump();

    // The sidebar is behind the app bar's button, so the one switcher on
    // screen is the place's own top line.
    expect(find.byType(HostSwitcher), findsOneWidget);
    expect(
      find.descendant(of: topSwitcher(), matching: find.text('local')),
      findsOneWidget,
    );
    expect(recorded.last, 'local');
    expect(find.text('Automations at local'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('in Settings the sidebar lists its sections in order', (
    tester,
  ) async {
    final (shell, _) = shellAt(const Place(PlaceKind.settings));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    // Settings' sidebar is its ten sections in the design's order (F7
    // decision 7), under the row that leaves the place.
    final labels = [
      for (final row in tester.widgetList<NavRow>(
        find.descendant(
          of: find.byType(Sidebar),
          matching: find.byType(NavRow),
        ),
      ))
        row.label,
    ];
    expect(labels.first, 'Back');
    expect(labels.skip(1), [
      'Hosts',
      'Environments',
      'Connections',
      'Models and routing',
      'Secrets',
      'Network',
      'Usage and audit',
      'Notifications',
      'Access',
      'General',
    ]);
  });

  testWidgets('a section opens through the guard', (tester) async {
    // A section holding an edit: opening another would lose what was typed, so
    // a section row asks the one guard every way out of a place asks.
    final edits = HostsModel.fixture(shellHosts)
      ..edit(shellHosts.first)
      ..change(shellHosts.first);
    final guard = UnsavedGuard()..watch(edits);
    final (shell, opened) = shellAt(
      const Place(PlaceKind.settings),
      unsaved: guard,
    );
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.tap(find.text('Secrets'));
    await tester.pump();

    expect(find.text('Leave without saving?'), findsOneWidget);
    expect(opened, isEmpty, reason: 'nothing opens until the person says so');

    await tester.tap(find.text('Discard the edit'));
    await tester.pump();
    await tester.pump();

    expect(opened, [const Place(PlaceKind.settings, section: 'secrets')]);
  });

  testWidgets('Settings draws no list of its own', (tester) async {
    // The sidebar is Settings' list, so the page beside it draws the open
    // section alone: one column of section rows, never two (F2).
    final (shell, _) = shellAt(
      const Place(PlaceKind.settings, section: 'secrets'),
      settingsFor: (host, section, state) => SettingsPlace(
        preferences: GeneralPreferences(),
        index: section,
        sections: {
          SettingsSectionKind.hosts: (state) => const Text('Hosts page'),
          SettingsSectionKind.secrets: (state) => const Text('Secrets page'),
        },
      ),
    );
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );
    await tester.pump();

    expect(find.text('Secrets page'), findsOneWidget);
    expect(find.text('Hosts page'), findsNothing);
    expect(
      find.descendant(
        of: find.byType(SettingsPlace),
        matching: find.byType(NavRow),
      ),
      findsNothing,
      reason: 'the sections are the sidebar, not the page',
    );
    // And the sidebar fills the section that is open.
    expect(
      tester
          .widgetList<NavRow>(
            find.descendant(
              of: find.byType(Sidebar),
              matching: find.byType(NavRow),
            ),
          )
          .where((row) => row.selected)
          .map((row) => row.label),
      ['Secrets'],
    );
  });

  testWidgets('Back returns to the place before Settings', (tester) async {
    // Settings is a place like any other, so its Back row leaves it for the
    // place the person came from (F2), here the Overview the Studio opened at.
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final store = FleetStore(HostRegistry(discovery: () async => []));
    addTearDown(store.dispose);
    final asks = InboxModel(
      store,
      client: (host) => throw StateError('no host answers'),
    );
    addTearDown(asks.dispose);

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(store: store, inbox: asks),
      ),
    );
    await tester.pump();

    await tester.tap(
      find.descendant(
        of: find.byType(Sidebar),
        matching: find.text('Settings'),
      ),
    );
    await tester.pump();
    expect(find.text('Back'), findsOneWidget);

    await tester.tap(find.text('Back'));
    await tester.pump();

    expect(find.text('Back'), findsNothing);
    expect(
      tester
          .widgetList<NavRow>(
            find.descendant(
              of: find.byType(Sidebar),
              matching: find.byType(NavRow),
            ),
          )
          .where((row) => row.selected)
          .map((row) => row.label),
      ['Overview'],
    );

    // Back returns wherever the person came from, not only where the Studio
    // opened: from Needs you, it goes back to Needs you.
    await tester.tap(
      find.descendant(
        of: find.byType(Sidebar),
        matching: find.text('Needs you'),
      ),
    );
    await tester.pump();
    await tester.tap(
      find.descendant(
        of: find.byType(Sidebar),
        matching: find.text('Settings'),
      ),
    );
    await tester.pump();
    await tester.tap(find.text('Back'));
    await tester.pump();

    expect(
      tester
          .widgetList<NavRow>(
            find.descendant(
              of: find.byType(Sidebar),
              matching: find.byType(NavRow),
            ),
          )
          .where((row) => row.selected)
          .map((row) => row.label),
      ['Needs you'],
    );
  });

  testWidgets('selecting a row changes the place', (tester) async {
    final (shell, opened) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.tap(find.text('Needs you'));
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

  testWidgets('Earlier opens its threads and a thread opens', (tester) async {
    final (shell, opened) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    // The earlier threads start closed: a project shows the work in flight.
    expect(find.text('Retire the old bridge'), findsNothing);

    await tester.tap(find.text('Earlier'));
    await tester.pump();

    await tester.tap(find.text('Retire the old bridge'));
    await tester.pump();

    expect(opened, [const Place(PlaceKind.thread, host: 'local', id: 'a-4')]);
  });

  testWidgets('an open earlier thread shows its group open', (tester) async {
    // The place is one of the project's earlier threads: its group is drawn
    // open without a tap, so the row being read is always visible.
    final fixture = await _fixtureListingSession(tester, open: false);
    await _pumpSessionPlace(tester, fixture, storeFor: _emptyStore);

    final earlier = tester
        .widgetList<NavRow>(find.byType(NavRow))
        .firstWhere((row) => row.label == 'Earlier');
    expect(earlier.expanded, isTrue);
    expect(earlier.count, 1);

    final row = tester.widget<ThreadRow>(
      find.widgetWithText(ThreadRow, 'Fix the login bug'),
    );
    expect(row.selected, isTrue);
  });

  testWidgets('a chosen host keeps its own earlier threads', (tester) async {
    // The host scope filters the earlier rows as it filters the current ones,
    // and a project whose only work on that host is earlier still has a row.
    tester.view.physicalSize = const Size(1200, 1400);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    model.ThreadRef thread(String host, String id, String title) =>
        model.ThreadRef(
          host: host,
          id: id,
          project: '/home/u/alpha',
          title: title,
          kind: model.ThreadKind.agent,
          status: model.ThreadStatus.idle,
          headline: '',
          lastActive: shellNow,
        );
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Sidebar(
          nav: NavModel(
            fixed: shellNavFor(const Place.overview()).fixed,
            projects: [
              NavProject(
                id: '/home/u/alpha',
                name: 'alpha',
                current: [
                  thread('local', 'a-1', 'Local work'),
                  thread('studio', 's-1', 'Studio work'),
                ],
                earlierThreads: [
                  thread('local', 'a-2', 'Local and quiet'),
                  thread('studio', 's-2', 'Studio and quiet'),
                ],
              ),
              NavProject(
                id: '/srv/beta',
                name: 'beta',
                current: [thread('local', 'b-1', 'Beta work')],
                earlierThreads: [thread('studio', 's-3', 'Beta and quiet')],
              ),
            ],
          ),
          hosts: shellHosts,
          place: const Place.overview(),
          host: 'studio',
          now: shellNow,
          onSelect: (_) {},
        ),
      ),
    );

    // The chosen host's project whose only threads there are earlier is
    // still listed, and every Earlier count is that host's alone.
    expect(find.text('alpha'), findsOneWidget);
    expect(find.text('beta'), findsOneWidget);
    expect(find.text('Studio work'), findsOneWidget);
    expect(find.text('Local work'), findsNothing);
    final rows = tester
        .widgetList<NavRow>(find.byType(NavRow))
        .where((row) => row.label == 'Earlier')
        .toList();
    expect([for (final row in rows) row.count], [1, 1]);

    await tester.tap(find.text('Earlier').first);
    await tester.pump();

    expect(find.text('Studio and quiet'), findsOneWidget);
    expect(find.text('Local and quiet'), findsNothing);

    await tester.tap(find.text('Earlier').last);
    await tester.pump();

    expect(find.text('Beta and quiet'), findsOneWidget);
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
    expect(find.text('Needs you'), findsNothing);

    await tester.tap(find.byTooltip('Projects and threads').first);
    await tester.pump();
    expect(find.text('PROJECTS'), findsOneWidget);

    // Choosing a place closes the panel and opens it.
    await tester.tap(find.text('Needs you'));
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
    // The search field is drawn here, above the tree. It has no label above
    // it: a magnifying glass names it, and its tooltip says what it finds.
    expect(find.byType(Field), findsOneWidget);
    expect(find.byIcon(Glyphs.search), findsOneWidget);
    expect(find.text('Search'), findsNothing);
    expect(
      find.byTooltip('Find a thread, project or place (⌘K)'),
      findsOneWidget,
    );
  });

  testWidgets('typing filters the sidebar to what matches', (tester) async {
    final (shell, _) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.enterText(find.byType(Field), 'recover');
    await tester.pump();

    // The tree is replaced by what matches: the thread whose title holds the
    // word, with no other thread of the project's under it.
    expect(find.text('THREADS'), findsOneWidget);
    expect(find.text('Reliable session recovery'), findsOneWidget);
    expect(find.text('Web studio contract'), findsNothing);
    expect(find.text('Bump the gate'), findsNothing);

    // The fixed places and the tree's own labels are gone with it, and a
    // group holding nothing — here the places and the projects — is not
    // drawn at all.
    expect(find.text('PROJECTS'), findsNothing);
    expect(find.text('PLACES'), findsNothing);
    expect(find.text('Needs you'), findsNothing);
  });

  testWidgets('Enter opens the first match', (tester) async {
    final (shell, opened) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.enterText(find.byType(Field), 'studio');
    await tester.pump();
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump();

    // A thread is the first thing found: Enter opens it.
    expect(opened, [const Place(PlaceKind.thread, host: 'local', id: 'a-2')]);

    // And the search is put back: the tree is drawn again in full.
    expect(find.text('THREADS'), findsNothing);
    expect(find.text('Bump the gate'), findsOneWidget);
    expect(find.text('PROJECTS'), findsOneWidget);
  });

  testWidgets('Escape restores the tree', (tester) async {
    final (shell, opened) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.enterText(find.byType(Field), 'recover');
    await tester.pump();
    expect(find.text('Web studio contract'), findsNothing);

    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pump();

    // The query is gone, so the tree is back, and nothing was opened.
    expect(opened, isEmpty);
    expect(find.text('PROJECTS'), findsOneWidget);
    expect(find.text('Reliable session recovery'), findsOneWidget);
    expect(find.text('Web studio contract'), findsOneWidget);

    // The field also let go of the keyboard: its text is empty and unfocused,
    // so the tree's own shortcuts are the ones the keyboard reaches.
    final field = tester.widget<Field>(find.byType(Field));
    expect(field.controller?.text, isEmpty);
    expect(
      tester.widget<EditableText>(find.byType(EditableText)).focusNode.hasFocus,
      isFalse,
    );
  });

  testWidgets('nothing matching says so', (tester) async {
    final (shell, _) = shellAt(const Place.overview());
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    await tester.enterText(find.byType(Field), 'zzz');
    await tester.pump();

    // One line, in the words of the thing searched for, and no group labels
    // standing over nothing.
    expect(find.text('Nothing is called “zzz”'), findsOneWidget);
    expect(find.text('THREADS'), findsNothing);
    expect(find.text('PROJECTS'), findsNothing);
    expect(find.text('Reliable session recovery'), findsNothing);
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

  testWidgets('one row is filled for the open thread', (tester) async {
    const place = Place(PlaceKind.thread, host: 'local', id: 'a-1');
    // The fill is the open place alone: the thread's row, never its project's.
    final fixture = shellNavFor(place);
    final nav = NavModel(
      fixed: fixture.fixed,
      projects: [
        for (final project in fixture.projects)
          NavProject(
            id: project.id,
            name: project.name,
            current: project.current,
            earlierThreads: project.earlierThreads,
          ),
      ],
    );
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Shell(
          nav: nav,
          hosts: shellHosts,
          place: place,
          now: shellNow,
          onRetry: () {},
          onSelect: (_) {},
          content: (_) => plannedPlace(place, nav),
        ),
      ),
    );

    final fill = tokens().selected;
    final filled = [
      for (final box in tester.widgetList<DecoratedBox>(
        find.descendant(
          of: find.byType(Sidebar),
          matching: find.byType(DecoratedBox),
        ),
      ))
        if (box.decoration is BoxDecoration &&
            (box.decoration as BoxDecoration).color == fill)
          box,
    ];
    expect(filled, hasLength(1));

    // And the row it fills is the thread's: no `NavRow` is selected, because
    // the project whose thread is open is not where the person is.
    final rows = tester.widgetList<NavRow>(find.byType(NavRow));
    expect(rows.where((row) => row.selected), isEmpty);
    expect(
      tester
          .widgetList<ThreadRow>(find.byType(ThreadRow))
          .where((row) => row.selected),
      hasLength(1),
    );
  });

  testWidgets('the sidebar rows share one edge', (tester) async {
    final (shell, _) = shellAt(
      const Place.overview(),
      onNew: () {},
      onAddProject: () {},
    );
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    double left(Finder finder) => tester.getTopLeft(finder).dx;
    double right(Finder finder) => tester.getTopRight(finder).dx;

    // One inset for everything the sidebar draws: the switcher, the search,
    // a place's icon and the `Projects` label all start at the same edge.
    expect(find.byType(HostSwitcher), findsOneWidget);
    final edge = left(find.byType(HostSwitcher));
    expect(left(find.byType(Field)), edge);
    expect(
      left(
        find.descendant(
          of: find.byType(Sidebar),
          matching: find.byIcon(Glyphs.square_grid_2x2),
        ),
      ),
      edge,
    );
    expect(left(find.text('PROJECTS')), edge);

    // And one inset on the right: the search's plus and the `Projects` plus
    // end at the same edge.
    Finder plus(String tooltip) => find.descendant(
      of: find.byTooltip(tooltip),
      matching: find.byIcon(Glyphs.plus),
    );
    expect(right(plus('New')), right(plus('Add a project')));
  });

  testWidgets('New starts work in the first project', (tester) async {
    tester.view.physicalSize = const Size(600, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final started = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/marshal')],
          // One thread, so the fleet draws its places rather than the state of
          // a fleet with nothing in it: New opens the project's conversation
          // inside the content area.
          agents: [agent(id: 'a-1', project: '/home/u/marshal')],
        ),
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

    test('a proxy refusal is unauthorized, never unreachable', () {
      expect(
        kindFor([host('local', HostReach.unauthorized)]),
        StateKind.unauthorized,
        reason:
            'a page the proxy refused must say so: reporting it as '
            'unreachability sends the reader to look at the wrong thing',
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

  testWidgets('a thread opens at the chosen density', (tester) async {
    // Finding 4: Settings' General section chose what a transcript starts as,
    // so every thread the Studio opens begins at it. A thread's own switch is
    // the only thing that overrides it, and it overrides it for that thread
    // alone.
    final fixture = await _fixtureListingSession(tester);
    await _pumpSessionPlace(
      tester,
      fixture,
      storeFor: _emptyStore,
      density: Density.full,
    );

    final screen = tester.widget<ThreadScreen>(find.byType(ThreadScreen));
    expect(screen.view.density, Density.full);
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
