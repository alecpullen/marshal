import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
// The contract's CheckState is a CI check's wire state; the kit's is a check
// drawn in the interface. This test reads the kit's.
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/inspector_state.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_inspector_models.dart';
import 'package:marshal_app/src/ui/inspector/host.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// A one-step transcript, so the thread place draws its header and the
/// inspector beside a conversation rather than a loading state.
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
      message: WireMessage(role: 'user', content: 'Inspect this turn.'),
    ),
  ],
);

/// The inspector on an agent thread (F9 decision 4): the five tabs beside the
/// transcript, the change set behind Changes, `⌘\` cycling the width, and the
/// sheet below the medium breakpoint.
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

  /// The Studio, opened on a running agent's thread, wide enough for the
  /// docked inspector unless [size] says otherwise.
  Future<void> pumpStudio(
    WidgetTester tester, {
    Size size = const Size(1440, 900),
  }) async {
    tester.view.physicalSize = size;
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    tester.platformDispatcher.accessibilityFeaturesTestValue =
        const FakeAccessibilityFeatures(disableAnimations: true);
    addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);

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
    final bridge = fixture!.bridges['local']!;
    bridge
      ..reply(
        'GET',
        '/api/agents/a-1/diff',
        FakeReply(
          200,
          headers: const {'content-type': 'application/json'},
          body: jsonEncode({
            'files': [
              {'path': 'internal/acp/stack.go', 'added': 12, 'removed': 3},
            ],
            'diff': 'diff --git a/internal/acp/stack.go b/internal/acp/stack.go\n',
          }),
        ),
      )
      ..reply(
        'GET',
        '/api/agents/a-1/gate',
        FakeReply(
          200,
          headers: const {'content-type': 'application/json'},
          body: jsonEncode({
            'at': '2026-11-17T04:08:00Z',
            'result': {'ok': true, 'skipped': false},
          }),
        ),
      )
      ..reply(
        'GET',
        '/api/agents/a-1/commit-draft',
        const FakeReply(200, body: '{"message":"acp: split the stack"}'),
      )
      ..reply(
        'GET',
        '/api/sessions/a-1/step-diffs',
        const FakeReply(200, body: '{"steps":[]}'),
      )
      ..reply(
        'GET',
        '/api/agents/a-1/review/comments',
        const FakeReply(200, body: '{"comments":[]}'),
      )
      ..reply('GET', '/api/agents/a-1/files', const FakeReply(200, body: '[]'));
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
          inspectorFor: (host, thread) {
            final models = ThreadInspectorModels(
              fixture.clientFor(host),
              agentId: thread.id,
              sessionId: thread.id,
              root: thread.project,
            );
            return ThreadInspector(
              state: InspectorState(),
              body: (tab) => inspectorBody(models, tab),
              start: models.start,
              dispose: models.dispose,
            );
          },
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
        ),
      ),
    );
    // The fleet store has a live stream and a clock, so nothing ever settles:
    // the Studio's own tests pump explicitly (test/ui/f2_flow_test).
    await settleIo(tester);
  }

  testWidgets('an open thread draws the inspector', (tester) async {
    await pumpStudio(tester);

    for (final label in const [
      'Changes',
      'Files',
      'Terminal',
      'Preview',
      'Inspect',
    ]) {
      expect(find.text(label), findsOneWidget, reason: '$label is a tab');
    }
  });

  testWidgets('the Changes tab draws the change set', (tester) async {
    await pumpStudio(tester);

    await tester.tap(find.text('Changes'));
    await settleIo(tester);

    // The gate the fixture answered with, and the file it changed.
    expect(find.text('Build and test'), findsOneWidget);
    expect(find.text('internal/acp/stack.go'), findsWidgets);
  });

  testWidgets('⌘\\ collapses the panel to a rail', (tester) async {
    await pumpStudio(tester);

    // Docked, then expanded, then the rail: one press per width.
    for (var i = 0; i < 2; i++) {
      await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
      await tester.sendKeyEvent(LogicalKeyboardKey.backslash);
      await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
      await tester.pump();
    }

    expect(find.byTooltip('Show the inspector'), findsOneWidget);
  });

  testWidgets('below the medium width the panel opens as a sheet', (
    tester,
  ) async {
    await pumpStudio(tester, size: const Size(600, 900));

    // No docked column at this width: the header's own control opens it.
    expect(find.byTooltip('Show the inspector'), findsNothing);
    await tester.tap(find.byTooltip('Inspector'));
    await settleIo(tester);

    expect(find.text('Inspector'), findsOneWidget);
  });
}
