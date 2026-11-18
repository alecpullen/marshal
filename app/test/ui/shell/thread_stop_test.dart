import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// A one-step transcript, so the thread place draws its header rather than a
/// loading state.
StackSnapshot _snapshot(String sessionId) => StackSnapshot(
  sessionID: sessionId,
  rev: 1,
  roots: const ['turn:1'],
  nodes: [
    const WireNode(id: 'turn:1', kind: 'turn', children: ['msg:1']),
    const WireNode(
      id: 'msg:1',
      kind: 'message',
      parent: 'turn:1',
      message: WireMessage(role: 'user', content: 'Stop this turn.'),
    ),
  ],
);

/// A running thread whose header offers Stop, and not Hold: no bridge route
/// holds an agent (f9-parity.md decision 2).
///
/// The composer is the fixture's own client, so Stop is the request the
/// production composer makes, recorded by the bridge rather than counted by a
/// callback.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// Lets a request's real round trip run: a pump advances the widget-side
  /// futures, and the delay gives the in-process bridge its time.
  Future<void> settleIo(WidgetTester tester) async {
    for (var i = 0; i < 8; i++) {
      await tester.pump();
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 30)),
      );
    }
    await tester.pump();
  }

  /// The Studio, opened on a running agent's thread.
  Future<ModelFixture> pumpStudio(WidgetTester tester) async {
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/marshal')],
          agents: [
            agent(
              id: 'a-1',
              project: '/home/u/marshal',
              name: 'Reliable session recovery',
              status: AgentState.agentRunning,
              activity: 'Running the tests',
              lastActive: now,
            ),
          ],
        ),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    bridge
      ..reply(
        'GET',
        '/api/sessions/a-1/access',
        const FakeReply(
          200,
          body: '{"mode":"act","auto":false,"system":false,"revision":1}',
        ),
      )
      ..reply('POST', '/api/sessions/a-1/cancel', const FakeReply(200));
    await tester.runAsync(() => fixture.refresh());

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
          threadStore: (thread) => StackStore.seeded(_snapshot(thread.id)),
          composerFor: (thread, view) => ComposerModel.forSession(
            fixture.clientFor(thread.host),
            thread.id,
            view: view,
          ),
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
        ),
      ),
    );
    // The fleet store has a live stream and a clock, so nothing ever settles:
    // the Studio's own tests pump explicitly (test/ui/f2_flow_test).
    await settleIo(tester);
    return fixture;
  }

  Future<void> openMenu(WidgetTester tester) async {
    await tester.tap(find.byTooltip('More actions'));
    await tester.pump();
    await tester.pump();
  }

  testWidgets('the header offers Stop and not Hold', (tester) async {
    await pumpStudio(tester);
    await openMenu(tester);

    // The menu's own words, which only the popover draws: the composer names
    // its own Stop below the transcript.
    expect(
      find.text('End the turn now'),
      findsOneWidget,
      reason: 'the header menu offers Stop',
    );
    expect(
      find.text('Pause work and keep everything'),
      findsNothing,
      reason: 'no route holds an agent, so the menu offers no Hold',
    );
  });

  testWidgets('Stop cancels the turn', (tester) async {
    final fixture = await pumpStudio(tester);
    await openMenu(tester);

    await tester.tap(find.text('End the turn now'));
    await settleIo(tester);

    final posted = [
      for (final request in fixture.requests('local'))
        if (request.method == 'POST') request.uri,
    ];
    expect(posted, contains('/api/sessions/a-1/cancel'));
  });
}
