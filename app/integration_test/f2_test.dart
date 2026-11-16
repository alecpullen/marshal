import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../test/model/support.dart';
import '../test/support/fake_bridge.dart';

/// F2's end-to-end flow on a device: the fixture bridge serves one agent
/// parked on an approval, the app answers it from the Inbox, and the agent
/// leaves the needs-you state when the snapshot says the request is gone.
///
/// The same flow runs as a widget test in `test/ui/f2_flow_test.dart`, which
/// is what CI can run; this one needs a display:
/// `flutter test integration_test -d linux`.
void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  final now = DateTime.utc(2026, 11, 17, 12);

  testWidgets('an approval answered in the app unblocks its agent', (
    tester,
  ) async {
    final fixture = await ModelFixture.start([
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
              params: {'command': 'git push', 'reason': 'The branch is ready'},
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
    ], now: now);
    final bridge = fixture.bridges['local']!;
    await fixture.refresh();

    final inbox = model.InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);

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
    await tester.pumpAndSettle();
    expect(find.text('git push'), findsOneWidget);

    bridge.reply('POST', '/api/permissions/tc-1', const FakeReply(200));
    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();

    final posted = bridge.requests.lastWhere(
      (request) => request.method == 'POST',
    );
    expect(posted.uri, '/api/permissions/tc-1');
    expect(posted.body, '{"approved":true}');

    // The agent's request is answered: the snapshot no longer reports it.
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
    await tester.pumpAndSettle();

    expect(inbox.asks, isEmpty);
    expect(find.byType(AskCard), findsNothing);
  });
}
