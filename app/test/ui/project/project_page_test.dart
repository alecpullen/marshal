import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/project_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as threads;
import 'package:marshal_app/src/model/work_item_model.dart'
    show Stage, WorkItemView;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/project/project_page.dart';
import 'package:marshal_app/src/ui/sections/unsaved_guard.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart'
    show FixtureHost, ModelFixture, agent, factoryItem, project;
import '../../support/fake_bridge.dart' show FakeReply;

/// The moment the page's fixtures are measured against.
final _now = DateTime.utc(2026, 11, 17, 4, 12);

/// A project's settings with a repository to discover and a factory default
/// block, and health and runs beside them.
ProjectModel _model() => ProjectModel(
  root: '/home/u/alpha',
  name: 'alpha',
  now: () => _now,
  read: () async => ProjectSettingsVersion(
    settings: contract.ProjectSettings.fromJson({
      'routing': {'profile': 'balanced'},
      'mode': 'act',
      'intake': {'repoId': 'repo-1'},
      'factoryDefaults': {'targetBranch': 'main', 'maxRounds': 3},
    }),
    etag: 'v1',
  ),
  write: (next, etag) async =>
      ProjectSettingsVersion(settings: next, etag: 'v2'),
  readHealth: () async => contract.ProjectHealth.fromJson({
    'gateRunnable': 'yes',
    'mirrorFresh': <Object?>[],
    'orphanWorktrees': <Object?>[],
    'trust': 'trusted',
    'verify': {'build': 'go build ./...', 'test': ''},
  }),
  readRuns: () async => const <contract.RunListEntry>[],
  discover: (repoID) async => contract.DiscoveryReport.fromJson({
    'repoId': repoID,
    'forge': 'github',
    'status': 'ok',
    'branch': 'main',
    'revision': 'abc1234f',
    'capabilities': [
      {'key': 'forge_metadata', 'state': 'supported', 'detail': 'It answered'},
    ],
    'facts': <Object?>[],
    'recommended': <Object?>[],
    'gaps': <Object?>[],
  }),
);

/// A page tall enough that its discovery action is on screen.
Future<void> _pumpTall(WidgetTester tester, Widget page) async {
  tester.view.physicalSize = const Size(1440, 2400);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(EmberApp(brightness: Brightness.light, home: page));
  await tester.pump();
}

/// One session as the page draws it: an index row read as a session's thread.
threads.ThreadRef _session(
  String id, {
  String? title,
  bool open = true,
  int? messageCount,
  Duration age = const Duration(minutes: 5),
}) => threads.threadOfSession(
  'local',
  '/home/u/alpha',
  contract.SessionRow(
    sessionID: id,
    title: title,
    updatedAt: _now.subtract(age).millisecondsSinceEpoch,
    messageCount: messageCount,
    open: open,
    viewers: 0,
    busy: false,
  ),
);

void main() {
  testWidgets('board columns follow stage', (tester) async {
    await _pumpTall(
      tester,
      ProjectPage(
        model: _model(),
        items: [
          for (final item in [
            factoryItem(
              id: 'item-1',
              title: 'Draft the intake',
              state: 'queued',
            ),
            factoryItem(
              id: 'item-2',
              title: 'Reliable session recovery',
              state: 'implementing',
            ),
            factoryItem(
              id: 'item-3',
              title: 'Review the inspector',
              state: 'reviewing',
            ),
            factoryItem(id: 'item-4', title: 'Ready to land', state: 'ready'),
          ])
            WorkItemView.of('local', item),
        ],
        now: _now,
      ),
    );

    for (final (stage, title) in [
      (Stage.plan, 'Draft the intake'),
      (Stage.implement, 'Reliable session recovery'),
      (Stage.review, 'Review the inspector'),
      (Stage.ready, 'Ready to land'),
    ]) {
      expect(
        find.descendant(
          of: find.byKey(ValueKey(stage)),
          matching: find.text(title),
        ),
        findsOneWidget,
        reason: '$title belongs in the ${stage.name} column',
      );
    }
    // A column holds only its own stage's work.
    expect(
      find.descendant(
        of: find.byKey(const ValueKey(Stage.plan)),
        matching: find.text('Ready to land'),
      ),
      findsNothing,
    );
  });

  testWidgets('discovery shows its report', (tester) async {
    final model = _model();
    await _pumpTall(
      tester,
      ProjectPage(model: model, initialTab: 3, now: _now),
    );
    await tester.pump();

    expect(find.text('Run discovery'), findsOneWidget);
    await tester.tap(find.text('Run discovery'));
    await tester.pump();
    await tester.pump();

    expect(find.text('DISCOVERY REPORT'), findsOneWidget);
    expect(find.text('forge_metadata'), findsOneWidget);
    expect(find.text('supported'), findsOneWidget);
    expect(model.discovery, isNotNull);
  });

  testWidgets('unsaved edits ask before leaving', (tester) async {
    final model = _model();
    final guard = UnsavedGuard();
    addTearDown(guard.dispose);
    bool? left;

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Builder(
          builder: (context) => Column(
            children: [
              Btn(
                'Leave',
                onTap: () async => left = await guard.confirm(context),
              ),
              Expanded(
                child: ProjectPage(
                  model: model,
                  guard: guard,
                  initialTab: 2,
                  now: _now,
                ),
              ),
            ],
          ),
        ),
      ),
    );
    await tester.pump();
    expect(guard.hasUnsavedEdits, isFalse);

    // The page watches the model while it is mounted, so an edit is the
    // guard's to ask about.
    model.setRounds('7');
    await tester.pump();
    expect(guard.hasUnsavedEdits, isTrue);

    await tester.tap(find.text('Leave'));
    await tester.pumpAndSettle();
    expect(find.text('Leave without saving?'), findsOneWidget);

    await tester.tap(find.text('Keep editing'));
    await tester.pumpAndSettle();
    expect(left, isFalse);
    expect(model.draft!.rounds, '7');
  });

  testWidgets('the project place opens from the shell', (tester) async {
    // The fixture bridge is real HTTP, so it is started and read outside the
    // test's fake-async zone (test/ui/f2_flow_test.dart does the same).
    final fixture = (await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/alpha')],
          // One agent keeps the shell from reading the host as unreachable;
          // the project's own page does not depend on it.
          agents: [agent(id: 'a-1', project: '/home/u/alpha')],
        ),
      ]),
    ))!;
    await tester.runAsync(() => fixture.refresh());
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(
          store: fixture.store,
          inbox: inbox,
          projectFor: (host, root) => _model(),
          initialPlace: const Place(PlaceKind.project, id: '/home/u/alpha'),
        ),
      ),
    );
    await tester.pump();
    await tester.pump();

    // The page's four tabs, not the placeholder a planned place draws. The
    // sidebar also names Automations, so the tabs are matched inside the page.
    final page = find.byType(ProjectPage);
    expect(page, findsOneWidget);
    for (final label in ['Work', 'Automations', 'Defaults', 'Health']) {
      expect(
        find.descendant(of: page, matching: find.text(label)),
        findsOneWidget,
        reason: 'the project page has a $label tab',
      );
    }
  });

  testWidgets('the discovery tap asks the bridge and draws its answer', (
    tester,
  ) async {
    // The page's whole loop: the button's callback asks the model, the model
    // asks the bridge through its client, and the bridge's answer is what the
    // tab then draws. It runs inside `runAsync` because the fixture bridge is
    // a real socket that a widget test's own clock never drives
    // (test/ui/pulls/fix_dispatch_test.dart does the same for a dispatch).
    final fixture = (await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/alpha')],
          agents: [agent(id: 'a-1', project: '/home/u/alpha')],
        ),
      ]),
    ))!;
    await tester.runAsync(() => fixture.refresh());

    final bridge = fixture.bridges['local']!;
    const json = {'content-type': 'application/json'};
    bridge
      ..reply(
        'GET',
        '/api/projects/settings',
        FakeReply(
          200,
          headers: const {...json, 'etag': '"v1"'},
          body: jsonEncode(
            contract.ProjectSettings.fromJson({
              'workspace': 'alpha',
              'intake': {'repoId': 'repo-1'},
            }).toJson(),
          ),
        ),
      )
      ..reply(
        'GET',
        '/api/projects/health',
        FakeReply(
          200,
          headers: json,
          body: jsonEncode(
            contract.ProjectHealth.fromJson({
              'gateRunnable': 'yes',
              'mirrorFresh': <Object?>[],
              'orphanWorktrees': <Object?>[],
              'trust': 'trusted',
              'verify': {'build': 'go build ./...', 'test': ''},
            }).toJson(),
          ),
        ),
      )
      ..reply(
        'POST',
        '/api/repos/repo-1/discover',
        FakeReply(
          200,
          headers: json,
          body: jsonEncode(
            contract.DiscoveryReport.fromJson({
              'repoId': 'repo-1',
              'forge': 'github',
              'status': 'ok',
              'branch': 'main',
              'revision': 'abc1234f',
              'capabilities': [
                {
                  'key': 'forge_metadata',
                  'state': 'supported',
                  'detail': 'It answered',
                },
              ],
              'facts': <Object?>[],
              'recommended': <Object?>[],
              'gaps': <Object?>[],
            }).toJson(),
          ),
        ),
      );

    // The page's own client over that bridge: the one the shell wires.
    final client = fixture.clientFor('local');
    final model = ProjectModel(
      root: '/home/u/alpha',
      name: 'alpha',
      now: () => _now,
      read: () => client.projectSettings('/home/u/alpha'),
      write: (settings, etag) =>
          client.putProjectSettings('/home/u/alpha', settings, etag: etag),
      readHealth: () => client.projectHealth('/home/u/alpha'),
      readRuns: () async => const <contract.RunListEntry>[],
      discover: (repoID) => client.discoverRepo(repoID),
    );
    addTearDown(model.dispose);

    await tester.runAsync(() async {
      await model.load();
      await model.loadHealth();
    });
    await _pumpTall(
      tester,
      ProjectPage(model: model, initialTab: 3, now: _now),
    );
    await tester.pump();
    expect(find.text('Run discovery'), findsOneWidget);

    // The tap posts a real request, so the test waits for its answer instead
    // of for a clock it controls.
    await tester.runAsync(() async {
      await tester.tap(find.text('Run discovery'));
      for (var i = 0; i < 200 && model.discovery == null; i++) {
        await Future<void>.delayed(const Duration(milliseconds: 5));
      }
    });
    await tester.pump();

    expect(model.discoveryError, isNull);
    expect(model.discovery, isNotNull);
    expect(
      bridge.requests.any(
        (request) =>
            request.method == 'POST' &&
            request.uri == '/api/repos/repo-1/discover',
      ),
      isTrue,
      reason: 'the tap reached the bridge',
    );
    expect(find.text('DISCOVERY REPORT'), findsOneWidget);
    expect(find.text('forge_metadata'), findsOneWidget);
    expect(find.text('supported'), findsOneWidget);
  });

  testWidgets('sessions list open first, then closed', (tester) async {
    // The order is the shell's: the page draws the list it is given, so the
    // test drives the place that sorts the index's rows.
    final fixture = (await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/alpha')],
          agents: [agent(id: 'a-1', project: '/home/u/alpha')],
        ),
      ], now: _now),
    ))!;
    fixture.serve('local', '/api/sessions/all', {
      'projects': [
        contract.ProjectSessions(
          root: '/home/u/alpha',
          sessions: [
            // The closed session is the newest of the three, and still comes
            // last.
            contract.SessionRow(
              sessionID: 'sess_3',
              title: 'Tidy the parser',
              updatedAt: _now
                  .subtract(const Duration(minutes: 1))
                  .millisecondsSinceEpoch,
              open: false,
              viewers: 0,
              busy: false,
            ),
            contract.SessionRow(
              sessionID: 'sess_1',
              title: 'Older open session',
              updatedAt: _now
                  .subtract(const Duration(hours: 2))
                  .millisecondsSinceEpoch,
              open: true,
              viewers: 0,
              busy: false,
            ),
            contract.SessionRow(
              sessionID: 'sess_2',
              title: 'Newer open session',
              updatedAt: _now
                  .subtract(const Duration(minutes: 5))
                  .millisecondsSinceEpoch,
              open: true,
              viewers: 0,
              busy: false,
            ),
          ],
        ).toJson(),
      ],
    });
    await tester.runAsync(() => fixture.refresh());

    tester.view.physicalSize = const Size(1440, 2400);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(
          store: fixture.store,
          inbox: inbox,
          projectFor: (host, root) => _model(),
          initialPlace: const Place(PlaceKind.project, id: '/home/u/alpha'),
        ),
      ),
    );
    await tester.pump();
    await tester.pump();

    final page = find.byType(ProjectPage);
    double topOf(String title) => tester
        .getTopLeft(find.descendant(of: page, matching: find.text(title)))
        .dy;
    expect(topOf('Newer open session'), lessThan(topOf('Older open session')));
    expect(topOf('Older open session'), lessThan(topOf('Tidy the parser')));
  });

  testWidgets('a row says open or closed, its age and its messages', (
    tester,
  ) async {
    await _pumpTall(
      tester,
      ProjectPage(
        model: _model(),
        sessions: [
          _session(
            'sess_1',
            title: 'Fix the login bug',
            messageCount: 18,
            age: const Duration(minutes: 8),
          ),
          _session(
            'sess_2',
            title: 'Tidy the parser',
            open: false,
            messageCount: 1,
            age: const Duration(hours: 3),
          ),
          _session(
            'sess_3',
            title: 'Sketch the importer',
            open: false,
            age: const Duration(days: 2, hours: 5),
          ),
        ],
        now: _now,
      ),
    );

    expect(find.text('Fix the login bug'), findsOneWidget);
    expect(find.text('Open · 8m · 18 messages'), findsOneWidget);
    expect(find.text('Closed · 3h · 1 message'), findsOneWidget);
    // A session whose index entry carried no count says nothing about
    // messages rather than "0 messages".
    expect(find.text('Closed · 2d'), findsOneWidget);
  });

  testWidgets('no sessions shows the sentence', (tester) async {
    await _pumpTall(tester, ProjectPage(model: _model(), now: _now));

    expect(
      find.text('Sessions started with marshal in this project appear here.'),
      findsOneWidget,
    );
  });

  testWidgets("a root's error shows with try again", (tester) async {
    var retries = 0;
    await _pumpTall(
      tester,
      ProjectPage(
        model: _model(),
        sessions: [_session('sess_1', title: 'Fix the login bug')],
        sessionsError: "This project's sessions could not be listed.",
        onRetrySessions: () => retries++,
        now: _now,
      ),
    );

    expect(
      find.text("This project's sessions could not be listed."),
      findsOneWidget,
    );
    // The rows it still has, under the error.
    expect(find.text('Fix the login bug'), findsOneWidget);

    await tester.tap(find.text('Try again'));
    await tester.pump();
    expect(retries, 1);
  });

  testWidgets('choosing a session opens it', (tester) async {
    threads.ThreadRef? opened;
    await _pumpTall(
      tester,
      ProjectPage(
        model: _model(),
        sessions: [_session('sess_1', title: 'Fix the login bug')],
        onOpenSession: (session) => opened = session,
        now: _now,
      ),
    );

    await tester.tap(find.text('Fix the login bug'));
    await tester.pump();

    expect(opened?.id, 'sess_1');
    expect(opened?.host, 'local');
  });
}
