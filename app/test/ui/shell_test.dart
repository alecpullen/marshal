import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/hosts_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/sections/unsaved_guard.dart';
import 'package:marshal_app/src/ui/shell/host_switcher.dart';
import 'package:marshal_app/src/ui/shell/placeholder.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../support/screens.dart';

/// The shell's rules (F2 decision 1 and §4): a row opens its place, the
/// narrow layout reaches the same navigation through the app bar, a place a
/// later phase brings says so, and a host that is away is listed with the
/// time it was last seen.
void main() {
  /// The shell with a recorder for the place it opened.
  (Widget, List<Place>) shellAt(Place place, {StateKind? state}) {
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
}
