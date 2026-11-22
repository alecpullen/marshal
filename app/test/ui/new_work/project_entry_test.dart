import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/model/nav_model.dart' show projectName;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/project_model.dart';
import 'package:marshal_app/src/model/route_translation.dart';
import 'package:marshal_app/src/model/selection.dart' show ProjectMemory;
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/new_work/project_composer.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';
import 'package:marshal_app/src/ui/project/project_page.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';
import 'package:marshal_app/src/ui/shell/sidebar.dart' show Sidebar;
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// The project entry (agent-first Studio, task 2): a sidebar project selection
/// opens that project's *conversation* — the thread read there last when the
/// fleet still lists it, and a focused draft otherwise — while an explicit
/// board link keeps opening the board.
///
/// Each transcript carries a step named for its session, so a test can say
/// which thread the screen is showing from what is on it.
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
          'a-local' => 'Step in the local project',
          'a-studio' => 'Step in the studio project',
          _ => 'Step in $sessionId',
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

  /// A memory over a map this test can read back: the preference store's own
  /// seam, without a file.
  ({ProjectMemory memory, Map<String, String> kept}) memoryStore() {
    final kept = <String, String>{};
    return (
      memory: ProjectMemory(
        read: (key) => kept[key],
        save: (key, value) => kept[key] = value,
      ),
      kept: kept,
    );
  }

  /// One project's page model: the board the explicit links still open.
  ProjectModel board(String root) => ProjectModel(
    root: root,
    name: projectName(root),
    now: () => now,
    read: () async => ProjectSettingsVersion(
      settings: ProjectSettings.fromJson({
        'routing': {'profile': 'balanced'},
        'mode': 'act',
      }),
      etag: 'v1',
    ),
    write: (next, etag) async =>
        ProjectSettingsVersion(settings: next, etag: 'v2'),
    readHealth: () async => ProjectHealth.fromJson({
      'gateRunnable': 'yes',
      'mirrorFresh': <Object?>[],
      'orphanWorktrees': <Object?>[],
      'trust': 'trusted',
      'verify': {'build': 'go build ./...', 'test': 'go test ./...'},
    }),
    readRuns: () async => const <RunListEntry>[],
    discover: (repoID) async => DiscoveryReport.fromJson({
      'repoId': repoID,
      'forge': 'github',
      'status': 'ok',
      'branch': 'main',
      'revision': 'abc1234f',
      'capabilities': <Object?>[],
      'facts': <Object?>[],
      'recommended': <Object?>[],
      'gaps': <Object?>[],
    }),
  );

  /// The Studio over [fixture], with [memory] and the composer wired.
  Future<void> pumpStudio(
    WidgetTester tester,
    ModelFixture fixture, {
    ProjectMemory? memory,
    StartPlace? startPage,
    Place? initialPlace,
  }) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

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
          memory: memory,
          startPage: startPage,
          initialPlace: initialPlace ?? const Place.overview(),
          threadStore: (thread) => StackStore.seeded(_snapshot(thread.id)),
          projectFor: (host, root) => board(root),
          newWorkFor: (host, root) => NewWorkModel.forProject(
            project(root),
            fixture.clientFor(host),
            host: host,
          ),
        ),
      ),
    );
    await settleIo(tester);
  }

  /// Every agent start the fleet's bridges were asked for.
  List<String> creates(ModelFixture fixture) => [
    for (final host in fixture.bridges.keys)
      for (final request in fixture.requests(host))
        if (request.method == 'POST' && request.uri == '/api/agents')
          request.body,
  ];

  /// A project's row in the sidebar, by the name it draws.
  ///
  /// It is scoped to the sidebar because the Overview lists the same projects:
  /// a bare text finder would tap whichever the tree drew first.
  Finder sidebarRow(String name) =>
      find.descendant(of: find.byType(Sidebar), matching: find.text(name));

  testWidgets('two same-named projects on different hosts open their own', (
    tester,
  ) async {
    const localRoot = '/home/u/a/marshal';
    const studioRoot = '/home/u/b/marshal';
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project(localRoot)],
          agents: [
            agent(
              id: 'a-local',
              project: localRoot,
              name: 'Local thread',
              status: AgentState.agentRunning,
            ),
          ],
        ),
        FixtureHost(
          name: 'studio',
          projects: [project(studioRoot)],
          agents: [
            agent(
              id: 'a-studio',
              project: studioRoot,
              name: 'Studio thread',
              status: AgentState.agentRunning,
            ),
          ],
        ),
      ], now: now),
    );
    await tester.runAsync(() => fixture!.refresh());

    // The local project was read last, so selecting it resumes that thread —
    // and selecting the studio project, which has the same directory name,
    // must not: a remembered thread belongs to one host and one project.
    final store = memoryStore();
    store.memory
      ..rememberProject('local', localRoot)
      ..rememberThread('local', localRoot, 'a-local');

    await pumpStudio(tester, fixture!, memory: store.memory);

    // Both sidebar rows say "marshal"; the local one is first (rows sort by
    // name, then by id).
    expect(sidebarRow('marshal'), findsNWidgets(2));

    await tester.tap(sidebarRow('marshal').at(1));
    await settleIo(tester);

    expect(
      find.byType(ProjectComposer),
      findsOneWidget,
      reason: 'the studio project has no remembered thread, so it drafts',
    );
    expect(find.text(studioRoot), findsOneWidget);
    expect(find.text(localRoot), findsNothing);
    expect(find.text('Step in the local project'), findsNothing);
    expect(store.memory.lastProject, (
      host: 'studio',
      project: studioRoot,
    ), reason: 'the project selected last is remembered with its host');

    await tester.tap(sidebarRow('marshal').at(0));
    await settleIo(tester);

    expect(
      find.text('Step in the local project'),
      findsOneWidget,
      reason: 'the local project resumes the thread read there last',
    );
    expect(find.text('Step in the studio project'), findsNothing);
    expect(find.byType(ProjectComposer), findsNothing);
  });

  testWidgets('a closed remembered thread opens a focused draft', (
    tester,
  ) async {
    const root = '/home/u/a/marshal';
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project(root)],
          agents: [agent(id: 'a-local', project: root, name: 'Local thread')],
        ),
      ], now: now),
    );
    // A session the index has closed: the fleet lists it, so it is not
    // missing — it is not a conversation to resume either.
    fixture!.serve('local', '/api/sessions/all', {
      'projects': [
        {
          'root': root,
          'sessions': [
            {
              'sessionId': 'sess_1',
              'title': 'The closed thread',
              'updatedAt': now
                  .subtract(const Duration(days: 2))
                  .millisecondsSinceEpoch,
              'open': false,
              'viewers': 0,
              'busy': false,
            },
          ],
        },
      ],
    });
    await tester.runAsync(() => fixture.refresh());

    final store = memoryStore();
    store.memory
      ..rememberProject('local', root)
      ..rememberThread('local', root, 'sess_1');

    await pumpStudio(tester, fixture, memory: store.memory);
    await tester.tap(sidebarRow('marshal'));
    await settleIo(tester);

    expect(find.byType(ProjectComposer), findsOneWidget);
    expect(find.text(root), findsOneWidget);
    expect(find.byType(ThreadScreen), findsNothing);
    expect(
      store.memory.threadFor('local', root),
      isNull,
      reason: 'a closed thread is forgotten, so it is not resumed again',
    );
  });

  testWidgets('a first visit focuses a draft and creates nothing', (
    tester,
  ) async {
    const root = '/home/u/a/marshal';
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project(root)],
          // One thread, so the fleet draws its places rather than the state
          // of a fleet with nothing in it.
          agents: [
            agent(id: 'a-quiet', project: root, name: 'Something quiet'),
          ],
        ),
      ], now: now),
    );
    await tester.runAsync(() => fixture!.refresh());

    final store = memoryStore();
    await pumpStudio(tester, fixture!, memory: store.memory);
    await tester.tap(sidebarRow('marshal'));
    await settleIo(tester);

    expect(find.byType(ProjectComposer), findsOneWidget);
    expect(
      find.text(root),
      findsOneWidget,
      reason: 'the project the draft belongs to is above the input',
    );
    expect(
      tester.getTopLeft(find.text(root)).dy,
      lessThan(
        tester.getTopLeft(find.text('What should it do? (optional)')).dy,
      ),
      reason: 'the identity sits above the input, not beside it',
    );
    expect(store.memory.lastProject, (
      host: 'local',
      project: root,
    ), reason: 'selecting a project remembers it for next time');
    // Nothing is created before Send.
    expect(creates(fixture), isEmpty);
  });

  testWidgets('Back returns to the place the address names', (tester) async {
    const root = '/home/u/a/marshal';
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project(root)],
          agents: [
            agent(id: 'a-quiet', project: root, name: 'Something quiet'),
          ],
        ),
      ], now: now),
    );
    await tester.runAsync(() => fixture!.refresh());

    final store = memoryStore();
    await pumpStudio(
      tester,
      fixture!,
      memory: store.memory,
      startPage: const StartPlace(Place.overview()),
    );
    expect(find.byType(Overview), findsOneWidget);

    await tester.tap(sidebarRow('marshal'));
    await settleIo(tester);
    expect(find.byType(ProjectComposer), findsOneWidget);
    expect(find.byType(Overview), findsNothing);

    // Back delivers the address of the place before the conversation.
    await tester.binding.handlePushRoute('/overview');
    await tester.pump();
    // The app around the Studio answers a named route first and has none for
    // a Studio path; the screen still opens the place the address names.
    expect(tester.takeException(), isA<FlutterError>());

    expect(find.byType(Overview), findsOneWidget);
    expect(find.byType(ProjectComposer), findsNothing);
  });

  testWidgets('a board link still opens the board', (tester) async {
    const root = '/home/u/a/marshal';
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project(root)],
          agents: [agent(id: 'a-local', project: root, name: 'Local thread')],
        ),
      ], now: now),
    );
    await tester.runAsync(() => fixture!.refresh());

    // `/project/<root>` names the board: it is what an explicit board link, a
    // bookmark and the Overview's project row still open.
    final store = memoryStore();
    store.memory
      ..rememberProject('local', root)
      ..rememberThread('local', root, 'a-local');

    await pumpStudio(
      tester,
      fixture!,
      memory: store.memory,
      startPage: StartPlace(Place(PlaceKind.project, id: root)),
    );

    expect(
      find.byType(ProjectPage),
      findsOneWidget,
      reason: 'a board link opens the board, not the conversation',
    );
    expect(find.byType(ProjectComposer), findsNothing);
  });

  testWidgets('switching project keeps the draft and creates nothing', (
    tester,
  ) async {
    const alpha = '/home/u/a/marshal';
    const beta = '/home/u/b/studio';
    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project(alpha), project(beta)],
          agents: [agent(id: 'a-new', project: beta, name: 'The new thread')],
        ),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    await tester.runAsync(() => fixture.refresh());
    bridge
      ..reply(
        'GET',
        '/api/prompts/recent',
        const FakeReply(200, body: '{"prompts":[]}'),
      )
      ..reply('GET', '/api/recipes', const FakeReply(200, body: '[]'))
      ..reply(
        'POST',
        '/api/agents',
        const FakeReply(200, body: '{"agentId":"a-new"}'),
      );

    final store = memoryStore();
    await pumpStudio(tester, fixture, memory: store.memory);

    await tester.tap(sidebarRow('marshal'));
    await settleIo(tester);

    await tester.enterText(
      find.descendant(
        of: find.byType(ProjectComposer),
        matching: find.byType(EditableText),
      ),
      'Fix the login redirect',
    );
    await tester.pump();

    // The Project override under Options moves the conversation to the other
    // project, and the draft stays as it was typed.
    await tester.tap(find.text(ProjectComposer.optionsLabel));
    await tester.pump(const Duration(milliseconds: 400));
    await tester.tap(find.textContaining('Project:'));
    await tester.pump(const Duration(milliseconds: 400));
    // The menu lists both roots; the one this conversation is not in is the
    // choice.
    await tester.tap(find.text(beta).last);
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    expect(find.text('Fix the login redirect'), findsOneWidget);
    expect(
      creates(fixture),
      isEmpty,
      reason: 'switching project creates nothing before Send',
    );

    // Send starts the agent in the project the chip names now, not the one
    // the conversation opened on.
    await tester.tap(find.text('Send'));
    await settleIo(tester);
    await tester.pump(const Duration(milliseconds: 400));

    final posted = creates(fixture);
    expect(posted, hasLength(1), reason: 'one Send starts one agent');
    expect(posted.single, contains('"project":"$beta"'));
    expect(posted.single, contains('Fix the login redirect'));
    expect(find.text('Step in a-new'), findsOneWidget);
  });
}
