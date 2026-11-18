import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';
import 'package:marshal_app/src/ui/thread/thread_composer.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// The shell's keys (F9 decision 6): `⌘N` starts work from anywhere, `⌘2` opens
/// the Inbox, and neither fires while a composer has the keyboard.
StackSnapshot _snapshot() => const StackSnapshot(
  sessionID: 'a-1',
  rev: 1,
  roots: ['turn:1'],
  nodes: [
    WireNode(id: 'turn:1', kind: 'turn', children: ['msg:1']),
    WireNode(
      id: 'msg:1',
      kind: 'message',
      parent: 'turn:1',
      message: WireMessage(role: 'user', content: 'Start here.'),
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

  /// A studio over one host with one project, which is what a new-work dialog
  /// needs to belong to.
  Future<ModelFixture> fixtureWithProject(WidgetTester tester) async {
    final started = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/marshal')],
          agents: [
            agent(id: 'a-1', project: '/home/u/marshal', name: 'The thread'),
          ],
        ),
      ], now: now),
    );
    final fixture = started!;
    await tester.runAsync(() => fixture.refresh());
    return fixture;
  }

  Future<void> sendMeta(WidgetTester tester, LogicalKeyboardKey key) async {
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(key);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump(const Duration(milliseconds: 400));
  }

  testWidgets('⌘N opens the new-work dialog from the Overview', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final fixture = await fixtureWithProject(tester);
    fixture.bridges['local']!
      ..reply(
        'GET',
        '/api/prompts/recent',
        const FakeReply(200, body: '{"prompts":[]}'),
      )
      ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'));
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
          newWorkFor: (host, root) => NewWorkModel.forProject(
            project('/home/u/marshal'),
            fixture.clientFor('local'),
            host: 'local',
          ),
        ),
      ),
    );
    await settleIo(tester);

    await sendMeta(tester, LogicalKeyboardKey.keyN);

    // The dialog starts in the first project the fleet lists, and its Project
    // chip offers the rest.
    expect(find.text('Start a new agent'), findsOneWidget);
  });

  testWidgets('⌘2 opens the Inbox', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final fixture = await fixtureWithProject(tester);
    final inboxModel = inbox.InboxModel(
      fixture.store,
      client: fixture.clientFor,
    );
    addTearDown(inboxModel.dispose);

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(store: fixture.store, inbox: inboxModel),
      ),
    );
    await settleIo(tester);
    expect(find.widgetWithText(PageBody, 'Inbox'), findsNothing);

    await sendMeta(tester, LogicalKeyboardKey.digit2);

    expect(find.widgetWithText(PageBody, 'Inbox'), findsOneWidget);
  });

  testWidgets('⌘N does nothing while a composer has focus', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final fixture = await fixtureWithProject(tester);
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
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
          threadStore: (thread) => StackStore.seeded(_snapshot()),
          composerFor: (thread, view) => ComposerModel(
            prompt: (text) async {},
            steer: (text) async {},
            cancel: () async {},
            setMode: (mode) async {},
            access: () async => const AccessState(
              mode: 'act',
              auto: false,
              system: false,
              revision: 1,
            ),
            view: view,
          ),
          newWorkFor: (host, root) => NewWorkModel.forProject(
            project('/home/u/marshal'),
            fixture.clientFor('local'),
            host: 'local',
          ),
        ),
      ),
    );
    await settleIo(tester);

    // The composer's field takes the keyboard: a `⌘N` typed into it is text,
    // not a key press.
    await tester.tap(
      find.descendant(
        of: find.byType(ThreadComposer),
        matching: find.byType(EditableText),
      ),
    );
    await tester.pump();
    await sendMeta(tester, LogicalKeyboardKey.keyN);

    expect(find.text('Start a new agent'), findsNothing);
  });
}
