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
        headline: switch (sessionId) {
          'a-2' => 'Step in the new thread',
          'a-3' => 'Step in the studio thread',
          _ => 'Step in the first thread',
        },
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

    // …and the dialog closed onto the thread it started
    // (`onThreadStarted` in ui/shell/shell.dart), not the one it opened from.
    expect(find.text('Start a new agent'), findsNothing);
    expect(find.text('Step in the new thread'), findsOneWidget);
    expect(find.text('Step in the first thread'), findsNothing);
  });

  testWidgets('a thread started on another host opens there', (tester) async {
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
          ],
        ),
        FixtureHost(
          name: 'studio',
          projects: [project('/home/u/studio')],
          agents: [
            agent(
              id: 'a-3',
              project: '/home/u/studio',
              name: 'The studio thread',
            ),
          ],
        ),
      ], now: now),
    );
    final local = fixture!.bridges['local']!;
    final studio = fixture.bridges['studio']!;
    await tester.runAsync(() => fixture.refresh());

    // The dialog's own lists on both hosts, and the spawn's answer on the one
    // it will start on.
    for (final bridge in [local, studio]) {
      bridge
        ..reply(
          'GET',
          '/api/prompts/recent',
          const FakeReply(200, body: '{"prompts":["Clean up the fixtures"]}'),
        )
        ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'));
    }
    studio.reply(
      'POST',
      '/api/agents',
      const FakeReply(200, body: '{"agentId":"a-3"}'),
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
          // Both hosts answer with a project, so the dialog may start on
          // either (spec D10, P8 plan 2 decision 6).
          newWorkFor: (host, root) => NewWorkModel.forHosts(
            host: host,
            routes: {
              'local': NewWorkRoutes.forProject(
                project('/home/u/marshal'),
                fixture.clientFor('local'),
              ),
              'studio': NewWorkRoutes.forProject(
                project('/home/u/studio'),
                fixture.clientFor('studio'),
              ),
            },
            project: host == 'studio'
                ? project('/home/u/studio')
                : project('/home/u/marshal'),
          ),
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
        ),
      ),
    );
    await settleIo(tester);
    expect(find.text('Step in the first thread'), findsOneWidget);

    // ⌘N opens the dialog, and its Host chip moves it to the other host.
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyN);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump(const Duration(milliseconds: 400));
    await tester.tap(find.textContaining('Host: local'));
    await tester.pump(const Duration(milliseconds: 400));
    // The sidebar's switcher lists the hosts too, so the menu's entry is the
    // last one drawn.
    await tester.tap(find.text('studio').last);
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.textContaining('Host: studio'), findsOneWidget);

    await tester.enterText(
      find.descendant(
        of: find.byType(Sheet),
        matching: find.byType(EditableText),
      ),
      'Fix the studio redirect',
    );
    await tester.pump();
    await tester.tap(find.text('Start'));
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    // The request went to the host the chip named…
    final posted = studio.requests.lastWhere(
      (request) => request.method == 'POST',
    );
    expect(posted.uri, '/api/agents');
    expect(posted.body, contains('"project":"/home/u/studio"'));
    expect(
      local.requests.where(
        (request) => request.method == 'POST' && request.uri == '/api/agents',
      ),
      isEmpty,
      reason: 'the host the dialog left posts nothing',
    );

    // …and the dialog closed onto the thread it started there, not onto the
    // thread on the host it opened from.
    expect(find.text('Start a new agent'), findsNothing);
    expect(find.text('Step in the studio thread'), findsOneWidget);
    expect(find.text('Step in the first thread'), findsNothing);
  });
}
