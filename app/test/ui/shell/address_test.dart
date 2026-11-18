import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/route_translation.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

import '../../model/support.dart';

/// The page's address (F8 decision 2): the place a person opens is the page's
/// address, so reload, Back, Forward and a copied link all land on it — and a
/// screen the page handed no place leaves the address alone.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// Lets an answer's real round trip run, and the fleet's reads settle: the
  /// store has a live stream and a clock, so nothing ever settles by itself
  /// (test/ui/f2_flow_test).
  Future<void> settleIo(WidgetTester tester) async {
    for (var i = 0; i < 8; i++) {
      await tester.pump();
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 30)),
      );
    }
    await tester.pump();
  }

  /// Records every method call the framework makes on the platform's
  /// navigation channel: what this build asked the page to name.
  ///
  /// The framework's own Navigator announces the route it opens as well, so a
  /// test clears what it recorded once the screen is up and reads only what
  /// the screen asked for afterwards.
  List<MethodCall> recordNavigation(WidgetTester tester) {
    final calls = <MethodCall>[];
    final messenger = tester.binding.defaultBinaryMessenger;
    messenger.setMockMethodCallHandler(SystemChannels.navigation, (call) async {
      calls.add(call);
      return null;
    });
    addTearDown(
      () => messenger.setMockMethodCallHandler(SystemChannels.navigation, null),
    );
    return calls;
  }

  /// A studio over one host listing one thread, which is what a thread's path
  /// has to resolve to.
  Future<ModelFixture> fixtureWithThread(WidgetTester tester) async {
    final started = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/marshal')],
          agents: [
            agent(
              id: 'a-1',
              project: '/home/u/marshal',
              name: 'Reliable session recovery',
            ),
          ],
        ),
      ], now: now),
    );
    final fixture = started!;
    await tester.runAsync(() => fixture.refresh());
    return fixture;
  }

  /// The Studio at [startPage], or with none when a test gives none: what the
  /// web build passes, and what a test and the tray's window pass.
  Future<void> pumpStudio(
    WidgetTester tester,
    ModelFixture fixture, {
    StartPlace? startPage,
  }) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

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
          startPage: startPage,
          threadStore: (thread) => StackStore.seeded(
            StackSnapshot(
              sessionID: thread.id,
              rev: 0,
              roots: const [],
              nodes: const [],
            ),
          ),
        ),
      ),
    );
    await settleIo(tester);
  }

  testWidgets('opening a place writes its address', (tester) async {
    final calls = recordNavigation(tester);
    final fixture = await fixtureWithThread(tester);
    await pumpStudio(
      tester,
      fixture,
      startPage: const StartPlace(Place.overview()),
    );
    // The framework's Navigator announced the route it opened; what this
    // screen asks for is what follows.
    calls.clear();

    await tester.tap(find.text('Inbox'));
    await tester.pump();

    final written = calls
        .where((call) => call.method == 'routeInformationUpdated')
        .toList();
    expect(written, hasLength(1), reason: '$calls');
    expect(written.single.arguments['uri'], '/inbox');
    expect(
      written.single.arguments['replace'],
      isFalse,
      reason: 'a place a person opens is one Back returns from, so a push',
    );
  });

  testWidgets('Back opens the place the address names', (tester) async {
    final calls = recordNavigation(tester);
    final fixture = await fixtureWithThread(tester);
    await pumpStudio(
      tester,
      fixture,
      startPage: const StartPlace(Place.overview()),
    );
    expect(find.byType(ThreadScreen), findsNothing);
    calls.clear();

    // What Back delivers: the address the place before this one had.
    await tester.binding.handlePushRoute('/thread/local/a-1');
    await tester.pump();

    // The app around the Studio is asked for a named route first, and has none
    // for a Studio path: the binding reports that (in release as well — the
    // app's own unknown-route path throws there too) and the screen still
    // opens the place the address names.
    expect(tester.takeException(), isA<FlutterError>());
    expect(find.byType(ThreadScreen), findsOneWidget);
    expect(
      calls.where((call) => call.method == 'routeInformationUpdated'),
      isEmpty,
      reason: 'the address already names the place Back arrived at',
    );
  });

  testWidgets('Back to the Overview opens it, not a second screen', (
    tester,
  ) async {
    final calls = recordNavigation(tester);
    final fixture = await fixtureWithThread(tester);
    await pumpStudio(
      tester,
      fixture,
      startPage: const StartPlace(Place.overview()),
    );
    // The Overview is on screen, and leaving it takes it off.
    expect(find.byType(Overview), findsOneWidget);
    await tester.tap(find.text('Inbox'));
    await tester.pump();
    calls.clear();
    expect(find.byType(Overview), findsNothing);

    // The Overview names itself `/overview`, not the root: the app around the
    // Studio answers a root `/` itself by pushing its home route, which would
    // stack a second Studio and leave this one unable to follow the address.
    await tester.binding.handlePushRoute('/overview');
    await tester.pump();

    expect(tester.takeException(), isA<FlutterError>());
    expect(
      find.byType(Studio),
      findsOneWidget,
      reason: 'the screen follows the address rather than being replaced',
    );
    expect(
      find.byType(Overview),
      findsOneWidget,
      reason: 'the Overview the address names is what is drawn',
    );
    expect(
      calls.where((call) => call.method == 'routeInformationUpdated'),
      isEmpty,
      reason: 'the address already names the place Back arrived at',
    );
  });

  testWidgets('a handed place writes nothing', (tester) async {
    final calls = recordNavigation(tester);
    final fixture = await fixtureWithThread(tester);
    // No start page: a test, and the tray's window, name their own place.
    await pumpStudio(tester, fixture);

    expect(
      calls.where((call) => call.method == 'selectMultiEntryHistory'),
      isEmpty,
      reason: 'a window with no address has no history to keep',
    );
    calls.clear();

    await tester.tap(find.text('Inbox'));
    await tester.pump();

    expect(calls, isEmpty);
  });
}
