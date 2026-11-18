import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// A one-step transcript, distinct per session, so a test can say which
/// thread the app is showing from what is on screen.
StackSnapshot _snapshot(String sessionId) => StackSnapshot(
  sessionID: sessionId,
  rev: 1,
  roots: const ['turn:1'],
  nodes: [
    const WireNode(id: 'turn:1', kind: 'turn', children: ['msg:1', 'step:1']),
    const WireNode(
      id: 'msg:1',
      kind: 'message',
      parent: 'turn:1',
      message: WireMessage(role: 'user', content: 'Start here.'),
    ),
    WireNode(
      id: 'step:1',
      kind: 'step',
      parent: 'turn:1',
      step: WireStep(
        headline: sessionId == 'a-2'
            ? 'Step in the new thread'
            : 'Step in the first thread',
      ),
    ),
  ],
);

void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// Lets an answer's real round trip run: a pump advances the widget-side
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

  testWidgets('start posts the request and opens the thread', (tester) async {
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/marshal')],
          agents: [
            agent(
              id: 'a-1',
              project: '/home/u/marshal',
              name: 'The first thread',
              status: AgentState.agentRunning,
            ),
            agent(
              id: 'a-2',
              project: '/home/u/marshal',
              name: 'The new thread',
            ),
          ],
        ),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    await tester.runAsync(() => fixture.refresh());

    // The dialog's own lists, and the spawn's answer.
    bridge
      ..reply(
        'GET',
        '/api/prompts/recent',
        const FakeReply(200, body: '{"prompts":["Clean up the fixtures"]}'),
      )
      ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'))
      ..reply(
        'POST',
        '/api/agents',
        const FakeReply(200, body: '{"agentId":"a-2"}'),
      );

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
          newWorkFor: (host, root) => NewWorkModel.forProject(
            project('/home/u/marshal'),
            fixture.clientFor('local'),
            host: 'local',
          ),
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
        ),
      ),
    );
    // The fleet store has a live stream and a clock, so nothing ever
    // settles: the Studio's own tests pump explicitly (test/ui/f2_flow_test).
    await settleIo(tester);
    expect(find.text('Step in the first thread'), findsOneWidget);

    // ⌘N opens the dialog (task 7).
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyN);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.text('Start a new agent'), findsOneWidget);

    // The sidebar has a field of its own, so the dialog's is named by its
    // sheet.
    await tester.enterText(
      find.descendant(
        of: find.byType(Sheet),
        matching: find.byType(EditableText),
      ),
      'Fix the login redirect',
    );
    await tester.pump();
    await tester.tap(find.text('Start'));
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    // The request went to the bridge, with the prompt that was typed…
    final posted = bridge.requests.lastWhere(
      (request) => request.method == 'POST',
    );
    expect(posted.uri, '/api/agents');
    expect(posted.body, contains('Fix the login redirect'));
    expect(posted.body, contains('"project":"/home/u/marshal"'));

    // …and the dialog closed onto the thread it started (the Studio's
    // `_openNewWork` in ui/shell/shell.dart), not the one it opened from.
    expect(find.text('Start a new agent'), findsNothing);
    expect(find.text('Step in the new thread'), findsOneWidget);
    expect(find.text('Step in the first thread'), findsNothing);
  });

  testWidgets('the Overview’s New opens the same dialog', (tester) async {
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/marshal')],
          agents: [agent(id: 'a-1', project: '/home/u/marshal')],
        ),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    await tester.runAsync(() => fixture.refresh());
    bridge
      ..reply('GET', '/api/prompts/recent', const FakeReply(200, body: '{}'))
      ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'));

    final inboxModel = inbox.InboxModel(
      fixture.store,
      client: fixture.clientFor,
    );
    addTearDown(inboxModel.dispose);

    // The window opens on the Overview (the default place), and the page's
    // header carries the one primary action: start work (design.md, "Starting
    // work", and the mock's `Btn('New', primary: true, icon: plus)`).
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
    await settleIo(tester);

    final button = find.widgetWithText(Btn, 'New');
    expect(button, findsOneWidget);
    await tester.tap(button);
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.text('Start a new agent'), findsOneWidget);
  });
}
