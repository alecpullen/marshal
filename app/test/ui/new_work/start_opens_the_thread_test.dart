import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/new_work/project_composer.dart';
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

  /// Opens the project composer with `⌘N` on the thread the Studio opened
  /// from, and types [text] into it.
  Future<void> openComposer(WidgetTester tester, String text) async {
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyN);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump(const Duration(milliseconds: 400));
    await tester.enterText(
      find.descendant(
        of: find.byType(ProjectComposer),
        matching: find.byType(EditableText),
      ),
      text,
    );
    await tester.pump();
  }

  /// Every POST /api/agents the bridge received, decoded.
  List<Map<String, Object?>> spawns(FakeBridge bridge) => [
    for (final request in bridge.requests)
      if (request.method == 'POST' && request.uri == '/api/agents')
        jsonDecode(request.body) as Map<String, Object?>,
  ];

  testWidgets('two presses of Send make one request', (tester) async {
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
    bridge
      ..reply('GET', '/api/prompts/recent', const FakeReply(200, body: '{}'))
      ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'))
      ..reply(
        'POST',
        '/api/agents',
        // Held open so the second press lands while the first is in flight.
        const FakeReply(
          200,
          body: '{"agentId":"a-2"}',
          delay: Duration(milliseconds: 200),
        ),
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
    await settleIo(tester);
    await openComposer(tester, 'Fix the login redirect');

    // A second press before the first answers is not a second ask: the button
    // is disabled, and the model refuses it as well.
    await tester.tap(find.text('Send'));
    await tester.tap(find.text('Send'), warnIfMissed: false);
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    expect(spawns(bridge), hasLength(1));
    expect(find.text('Step in the new thread'), findsOneWidget);
  });

  testWidgets(
    'a lost acknowledgment keeps the draft and is resolved on retry',
    (tester) async {
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
      bridge
        ..reply('GET', '/api/prompts/recent', const FakeReply(200, body: '{}'))
        ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'))
        // The first answer never arrives: a 503 is an outcome the Studio does
        // not know, which is exactly what a lost answer looks like.
        ..replyOnce(
          'POST',
          '/api/agents',
          const FakeReply(503, body: '{"error":"the bridge went away"}'),
        )
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
            initialPlace: const Place(
              PlaceKind.thread,
              host: 'local',
              id: 'a-1',
            ),
          ),
        ),
      );
      await settleIo(tester);
      await openComposer(tester, 'Fix the login redirect');
      await tester.tap(find.text('Send'));
      await settleIo(tester);

      // The draft survives the uncertainty, and the composer says so.
      expect(find.text('Start a new agent'), findsOneWidget);
      expect(find.text('Fix the login redirect'), findsOneWidget);
      expect(find.textContaining('did not confirm'), findsOneWidget);

      // Sending it again resolves the original request, so both requests carry
      // one ID — and the thread it started opens.
      await tester.tap(find.text('Send'));
      await settleIo(tester);
      await tester.pump(const Duration(milliseconds: 400));
      final sent = spawns(bridge);
      expect(sent, hasLength(2));
      expect(sent.first['requestId'], isNotNull);
      expect(sent.last['requestId'], sent.first['requestId']);
      expect(find.text('Step in the new thread'), findsOneWidget);
    },
  );

  testWidgets('an edited draft is new work under a new request id', (
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
    bridge
      ..reply('GET', '/api/prompts/recent', const FakeReply(200, body: '{}'))
      ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'))
      ..replyOnce(
        'POST',
        '/api/agents',
        const FakeReply(
          409,
          body: '{"error":"the request id names other work"}',
        ),
      )
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
    await settleIo(tester);
    await openComposer(tester, 'Fix the login redirect');
    await tester.tap(find.text('Send'));
    await settleIo(tester);

    // A definite rejection lets the draft be edited: the text stays, and the
    // composer does not claim the outcome is unknown.
    expect(find.text('Fix the login redirect'), findsOneWidget);
    expect(find.textContaining('did not confirm'), findsNothing);

    await tester.enterText(
      find.descendant(
        of: find.byType(ProjectComposer),
        matching: find.byType(EditableText),
      ),
      'Fix the signup redirect',
    );
    await tester.pump();
    await tester.tap(find.text('Send'));
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    final sent = spawns(bridge);
    expect(sent, hasLength(2));
    expect(sent.first['requestId'], isNotNull);
    expect(sent.last['requestId'], isNot(sent.first['requestId']));
  });

  testWidgets('a request to plan goes to discussion, not to Act', (
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
    bridge
      ..reply('GET', '/api/prompts/recent', const FakeReply(200, body: '{}'))
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
    await settleIo(tester);
    // Natural language, not the explicit "Plan first" kind: the agent is asked
    // to discuss before it changes anything.
    await openComposer(tester, 'Plan how to fix the login redirect first');
    await tester.tap(find.text('Send'));
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    final sent = spawns(bridge);
    expect(sent, hasLength(1));
    expect(sent.single['mode'], 'ask');
    expect(sent.single['prompt'], contains('Plan how'));
  });

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

    // ⌘N opens the project composer (task 2's one new-work entry).
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyN);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.text('Start a new agent'), findsOneWidget);

    // The sidebar has a field of its own, so the composer's is named by its
    // screen.
    await tester.enterText(
      find.descendant(
        of: find.byType(ProjectComposer),
        matching: find.byType(EditableText),
      ),
      'Fix the login redirect',
    );
    await tester.pump();
    await tester.tap(find.text('Send'));
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    // The request went to the bridge, with the prompt that was typed…
    final posted = bridge.requests.lastWhere(
      (request) => request.method == 'POST',
    );
    expect(posted.uri, '/api/agents');
    expect(posted.body, contains('Fix the login redirect'));
    expect(posted.body, contains('"project":"/home/u/marshal"'));

    // …and the composer closed onto the thread it started
    // (`onStarted` in ui/shell/shell.dart), not the one it opened from.
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

    // ⌘N opens the composer, and the Host chip under Options moves it to the
    // other host.
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyN);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump(const Duration(milliseconds: 400));
    await tester.tap(find.text('Options'));
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
        of: find.byType(ProjectComposer),
        matching: find.byType(EditableText),
      ),
      'Fix the studio redirect',
    );
    await tester.pump();
    await tester.tap(find.text('Send'));
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
      reason: 'the host the composer left posts nothing',
    );

    // …and the composer closed onto the thread it started there, not onto the
    // thread on the host it opened from.
    expect(find.text('Start a new agent'), findsNothing);
    expect(find.text('Step in the studio thread'), findsOneWidget);
    expect(find.text('Step in the first thread'), findsNothing);
  });
}
