import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/share_model.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

const _url = 'https://marshal.example/s/abc123';

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
      message: WireMessage(role: 'user', content: 'Share this thread.'),
    ),
  ],
);

/// A thread's Share (F7 decision 7): the header's button mints a read-only
/// status link and the sheet shows the one URL the bridge gives out.
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

  /// The Studio, opened on a running agent's thread with Share wired to the
  /// fixture's client.
  Future<void> pumpStudio(WidgetTester tester) async {
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
              lastActive: now,
            ),
          ],
        ),
      ], now: now),
    );
    fixture!.bridges['local']!.reply(
      'POST',
      '/api/status-links',
      FakeReply(
        200,
        headers: const {'content-type': 'application/json'},
        body: jsonEncode({
          'id': 'link-1',
          'url': _url,
          'expiresAt': '2026-11-19T12:00:00Z',
        }),
      ),
    );
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
          shareFor: (host, agentId) =>
              ShareModel(fixture.clientFor(host), agentId: agentId),
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
        ),
      ),
    );
    // The fleet store has a live stream and a clock, so nothing ever settles:
    // the Studio's own tests pump explicitly (test/ui/f2_flow_test).
    await settleIo(tester);
  }

  testWidgets('Share mints a link, shows the URL and copies it', (
    tester,
  ) async {
    await pumpStudio(tester);

    await tester.tap(find.byTooltip('Share a read-only status link'));
    await settleIo(tester);

    expect(find.text('Share this thread'), findsOneWidget);
    expect(find.text(_url), findsOneWidget);

    // The link is shown once; copying it is the sheet's one write.
    await tester.tap(find.text('Copy link'));
    await tester.pump();
    expect(tester.takeException(), isNull);
  });
}
