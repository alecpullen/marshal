import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../model/support.dart';
import '../support/fake_bridge.dart';

/// The phase's own promise, driven through the real screens: with an agent
/// parked on an approval, answering it in the Inbox posts the decision, and
/// the agent leaves the needs-you state when the stream confirms it.
///
/// The bridge is F1's in-process fake, so this needs no daemon, no socket and
/// no display: it is the same flow `integration_test/f2_test.dart` drives on a
/// real device.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// Lets an answer's real round trip run: a pump advances the widget-side
  /// futures, and the delay gives the socket in the fake bridge its time.
  Future<void> settleIo(WidgetTester tester) async {
    for (var i = 0; i < 8; i++) {
      await tester.pump();
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 30)),
      );
    }
    await tester.pump();
  }

  testWidgets('an approval answered in the app unblocks its agent', (
    tester,
  ) async {
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
              status: AgentState.agentAwaitingApproval,
              pending: const PendingRequest(
                kind: PendingKind.pendingApproval,
                id: 'tc-1',
                params: {
                  'command': 'git push',
                  'reason': 'The branch is ready',
                },
              ),
              lastActive: now.subtract(const Duration(minutes: 3)),
            ),
          ],
          queue: const [
            InboxItem(
              kind: InboxItemKind.inboxItemAgent,
              agentID: 'a-1',
              at: '2026-11-17T11:57:00Z',
            ),
          ],
        ),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    await tester.runAsync(() => fixture.refresh());

    final inbox = model.InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);
    expect([for (final ask in inbox.asks) ask.id], ['tc-1']);

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(
          store: fixture.store,
          inbox: inbox,
          initialPlace: const Place(PlaceKind.inbox),
        ),
      ),
    );
    await tester.pump();

    // The Inbox shows the ask with the decision the route can carry.
    expect(find.text('git push'), findsOneWidget);
    bridge.reply('POST', '/api/permissions/tc-1', const FakeReply(200));

    await tester.tap(find.text('Approve once'));
    // The answer posts for real: the test drives real sockets for it.
    await settleIo(tester);

    final posted = bridge.requests.lastWhere(
      (request) => request.method == 'POST',
    );
    expect(posted.uri, '/api/permissions/tc-1');
    expect(posted.body, '{"approved":true}');

    // The bridge's snapshot now has the agent running, with nothing pending.
    await tester.runAsync(() async {
      fixture.serve('local', '/api/agents', [
        agent(
          id: 'a-1',
          project: '/home/u/marshal',
          name: 'Reliable session recovery',
          status: AgentState.agentRunning,
          activity: 'Running the tests',
        ).toJson(),
      ]);
      fixture.serve('local', '/api/inbox', {
        'groups': {
          'needsYou': <String>[],
          'running': ['a-1'],
          'ready': <String>[],
          'earlier': <String>[],
        },
        'queue': <Object>[],
      });
      await fixture.refresh();
    });
    await tester.pump();

    // The ask is gone, and the Inbox says so rather than pretending work.
    expect(inbox.asks, isEmpty);
    expect(find.text('Nothing is waiting on you.'), findsWidgets);
    expect(find.byType(AskCard), findsNothing);
  });
}
