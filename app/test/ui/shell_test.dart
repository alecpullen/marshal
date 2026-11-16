import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
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
    final (shell, _) = shellAt(const Place(PlaceKind.pulls));
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: shell),
    );

    final planned = tester
        .widgetList<StateMessage>(find.byType(StateMessage))
        .where((message) => message.kind == StateKind.planned);
    expect(planned, isNotEmpty);
    expect(find.text('Pull requests'), findsWidgets);
    expect(find.textContaining('later phase'), findsOneWidget);
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
}
