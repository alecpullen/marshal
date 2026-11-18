import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/sidebar.dart';
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

  testWidgets('New in the sidebar opens with the composer focused', (
    tester,
  ) async {
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

    // A tap does not take the keyboard from the composer (kit/Press), so the
    // sidebar's plus arrives while the field still holds it: the button opens
    // the dialog anyway, and only `⌘N` is guarded.
    await tester.tap(
      find.descendant(
        of: find.byType(ThreadComposer),
        matching: find.byType(EditableText),
      ),
    );
    await tester.pump();
    await tester.tap(
      find.descendant(
        of: find.byType(Sidebar),
        matching: find.byTooltip('New'),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));

    expect(find.text('Start a new agent'), findsOneWidget);
  });

  testWidgets("a thread's New opens with the composer focused", (tester) async {
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
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
          // An empty transcript is where the thread's own New control lives:
          // its empty-state action must open the dialog even while a composer
          // holds the keyboard. Only the thread's `⌘N` is guarded.
          threadStore: (thread) => StackStore.seeded(
            const StackSnapshot(sessionID: 'a-1', rev: 1, roots: [], nodes: []),
          ),
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

    await tester.tap(
      find.descendant(
        of: find.byType(ThreadComposer),
        matching: find.byType(EditableText),
      ),
    );
    await tester.pump();
    await tester.tap(find.text('Start an agent'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));

    expect(find.text('Start a new agent'), findsOneWidget);
  });

  testWidgets('⌘2 does nothing while a composer has focus', (tester) async {
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
        ),
      ),
    );
    await settleIo(tester);

    // `⌘2` typed into a focused composer belongs to the field, not to the
    // shell: the thread stays on screen instead of navigating to the Inbox
    // mid-sentence.
    await tester.tap(
      find.descendant(
        of: find.byType(ThreadComposer),
        matching: find.byType(EditableText),
      ),
    );
    await tester.pump();
    await sendMeta(tester, LogicalKeyboardKey.digit2);

    expect(find.widgetWithText(PageBody, 'Inbox'), findsNothing);
    expect(find.byType(ThreadComposer), findsOneWidget);
  });

  testWidgets('⌘K focuses search', (tester) async {
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

    // The search field holds no keyboard until it is asked for.
    final search = find.descendant(
      of: find.byType(Sidebar),
      matching: find.byType(EditableText),
    );
    expect(tester.widget<EditableText>(search).focusNode.hasFocus, isFalse);

    await sendMeta(tester, LogicalKeyboardKey.keyK);

    // `⌘K` is the search's own key wherever a person is (f9-parity decision 6).
    expect(tester.widget<EditableText>(search).focusNode.hasFocus, isTrue);
  });

  testWidgets('⌘K from the composer focuses search', (tester) async {
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
        ),
      ),
    );
    await settleIo(tester);

    final composer = find.descendant(
      of: find.byType(ThreadComposer),
      matching: find.byType(EditableText),
    );
    await tester.tap(composer);
    await tester.pump();
    await tester.enterText(composer, 'half a thought');
    await tester.pump();
    expect(tester.widget<EditableText>(composer).focusNode.hasFocus, isTrue);

    await sendMeta(tester, LogicalKeyboardKey.keyK);

    // The field's own keys are kept and `⌘K` is not one of them: unlike `⌘N`
    // and `⌘1/⌘2/⌘3`, it moves the keyboard to the search rather than
    // navigating away, and the draft is still there to come back to.
    final search = find.descendant(
      of: find.byType(Sidebar),
      matching: find.byType(EditableText),
    );
    expect(tester.widget<EditableText>(search).focusNode.hasFocus, isTrue);
    expect(tester.widget<EditableText>(composer).focusNode.hasFocus, isFalse);
    expect(find.text('half a thought'), findsOneWidget);
  });

  testWidgets('opening a thread reads its access', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final fixture = await fixtureWithProject(tester);
    final inboxModel = inbox.InboxModel(
      fixture.store,
      client: fixture.clientFor,
    );
    addTearDown(inboxModel.dispose);
    // The session committed auto, which only a read of the access route
    // reports: the chip names it.
    fixture.bridges['local']!.reply(
      'GET',
      '/api/sessions/a-1/access',
      const FakeReply(
        200,
        body:
            '{"mode":"act","auto":true,"system_access":false,'
            '"access_revision":1}',
      ),
    );

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(
          store: fixture.store,
          inbox: inboxModel,
          initialPlace: const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
          threadStore: (thread) => StackStore.seeded(_snapshot()),
          composerFor: (thread, view) => ComposerModel.forSession(
            fixture.clientFor(thread.host),
            thread.id,
            view: view,
          ),
        ),
      ),
    );
    await settleIo(tester);

    final reads = [
      for (final request in fixture.requests('local'))
        if (request.method == 'GET' &&
            request.uri == '/api/sessions/a-1/access')
          request.uri,
    ];
    expect(reads, hasLength(1), reason: 'the composer reads access once');
    // The chip names what the read reported.
    await settleIo(tester);
    expect(find.text('Act · Auto'), findsOneWidget);
  });
}
