import 'dart:convert';

import 'package:flutter/services.dart' show LogicalKeyboardKey;
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/expected_checks.dart';
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/project_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as threads;
import 'package:marshal_app/src/model/section_model.dart' show SaveState;
import 'package:marshal_app/src/model/work_item_model.dart'
    show Stage, WorkItemView;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/project/project_page.dart';
import 'package:marshal_app/src/ui/sections/unsaved_guard.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart'
    show
        FixtureHost,
        ModelFixture,
        agent,
        factoryCheck,
        factoryCI,
        factoryItem,
        factoryRound,
        project;
import '../../support/fake_bridge.dart' show FakeReply;

/// The moment the page's fixtures are measured against.
final _now = DateTime.utc(2026, 11, 17, 4, 12);

/// A project's settings with a repository to discover and a factory default
/// block, and health and runs beside them.
///
/// [factoryDefaults] replaces the whole factory block, so a test can store the
/// checks a project states; [onWrite] sees the document a save would send.
ProjectModel _model({
  String root = '/home/u/alpha',
  String name = 'alpha',
  Map<String, Object?>? factoryDefaults,
  void Function(contract.ProjectSettings next)? onWrite,
}) => ProjectModel(
  root: root,
  name: name,
  now: () => _now,
  read: () async => ProjectSettingsVersion(
    settings: contract.ProjectSettings.fromJson({
      'routing': {'profile': 'balanced'},
      'mode': 'act',
      'intake': {'repoId': 'repo-1'},
      'factoryDefaults':
          factoryDefaults ?? {'targetBranch': 'main', 'maxRounds': 3},
    }),
    etag: 'v1',
  ),
  write: (next, etag) async {
    onWrite?.call(next);
    return ProjectSettingsVersion(settings: next, etag: 'v2');
  },
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

/// The Defaults tab with its expected-checks sheet open.
Future<void> _openChecks(
  WidgetTester tester,
  ProjectModel model, {
  List<ObservedCheck> observedChecks = const <ObservedCheck>[],
}) async {
  await _pumpTall(
    tester,
    ProjectPage(
      model: model,
      initialTab: 2,
      now: _now,
      observedChecks: observedChecks,
    ),
  );
  await tester.pump();
  await tester.tap(find.text('Expected checks'));
  await tester.pumpAndSettle();
}

/// Types into the sheet's field labelled [label].
Future<void> _enter(WidgetTester tester, String label, String text) async {
  final field = find.ancestor(
    of: find.text(label),
    matching: find.byType(Field),
  );
  await tester.enterText(
    find.descendant(of: field, matching: find.byType(EditableText)),
    text,
  );
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

  testWidgets('the header New starts work in this project', (tester) async {
    var opened = 0;
    await _pumpTall(
      tester,
      ProjectPage(model: _model(), now: _now, onNewWork: () => opened++),
    );

    // The page's one primary action, in its header (design.md, "Starting
    // work"), on every section: the page is where a project's next agent is
    // started from.
    final button = find.widgetWithText(Btn, 'New');
    expect(button, findsOneWidget);
    await tester.tap(button);
    expect(opened, 1);
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

  testWidgets('suggestions come from the project\u2019s own repository', (
    tester,
  ) async {
    // The shell is what hands the store the project's intake repository, so
    // the evidence it offers comes from the same project and the same
    // repository (F6 decision 12), never from a neighbouring one.
    final fixture = (await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/alpha')],
          agents: [agent(id: 'a-1', project: '/home/u/alpha')],
          factoryItems: [
            factoryItem(
              id: 'item-1',
              repo: 'repo-1',
              rounds: [
                factoryRound(
                  ci: factoryCI(
                    checks: [factoryCheck(name: 'build', appID: 42)],
                  ),
                ),
              ],
            ),
            factoryItem(
              id: 'item-2',
              repo: 'repo-2',
              rounds: [
                factoryRound(
                  ci: factoryCI(
                    checks: [factoryCheck(name: 'other', appID: 7)],
                  ),
                ),
              ],
            ),
          ],
        ),
      ]),
    ))!;
    await tester.runAsync(() => fixture.refresh());
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await tester.runAsync(() => factory.refresh('local'));

    tester.view.physicalSize = const Size(1440, 2400);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Studio(
          store: fixture.store,
          inbox: InboxModel(fixture.store, client: fixture.clientFor),
          factory: factory,
          // The project's intake names repo-1; item-2 was recorded in
          // repo-2, so its check is not evidence for this project.
          projectFor: (host, root) => _model(),
          initialPlace: const Place(PlaceKind.project, id: '/home/u/alpha'),
        ),
      ),
    );
    await tester.pump();
    await tester.pump();

    final page = find.byType(ProjectPage);
    await tester.tap(
      find.descendant(of: page, matching: find.text('Defaults')),
    );
    await tester.pump();
    await tester.tap(
      find.descendant(of: page, matching: find.text('Expected checks')),
    );
    // The shell keeps a frame source of its own, so the sheet's opening
    // animation is pumped past rather than waited out.
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));
    await tester.tap(find.text('Show checks observed in this project'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));

    expect(find.text('build (App 42)'), findsOneWidget);
    expect(
      find.text('other (App 7)'),
      findsNothing,
      reason: 'a check recorded in another repository is not evidence here',
    );
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

  testWidgets('the page names the project and its section, wide and narrow', (
    tester,
  ) async {
    Future<void> pumpNarrow(Widget page) async {
      tester.view.physicalSize = const Size(420, 1600);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.reset);
      await tester.pumpWidget(
        EmberApp(brightness: Brightness.light, home: page),
      );
      await tester.pump();
    }

    final model = _model();
    for (final narrow in [true, false]) {
      final page = ProjectPage(model: model, now: _now);
      if (narrow) {
        await pumpNarrow(page);
      } else {
        await _pumpTall(tester, page);
      }
      final within = find.byType(ProjectPage);
      // The page's header is the one place the project is named: its title and
      // its subtitle, at every width (§1.4: quiet until it matters).
      final header = find.byType(PageBody);
      expect(
        tester.widget<PageBody>(header).title,
        'alpha',
        reason: 'the header carries the project\'s name',
      );
      expect(
        tester.widget<PageBody>(header).subtitle,
        '/home/u/alpha',
        reason: 'the root tells two same-named projects apart',
      );
      for (final text in ['alpha', '/home/u/alpha']) {
        expect(
          find.descendant(of: header, matching: find.text(text)),
          findsOneWidget,
          reason: '$text is drawn once, in the header',
        );
      }
      for (final label in ['Work', 'Automations', 'Defaults', 'Health']) {
        expect(
          find.descendant(of: within, matching: find.text(label)),
          findsOneWidget,
          reason: 'a $label tab, ${narrow ? 'narrow' : 'wide'}',
        );
      }
      expect(
        find.descendant(
          of: within,
          matching: find.text(
            'The project\'s work items, its agents and its plan runs',
          ),
        ),
        findsOneWidget,
        reason: 'the open section\'s line, ${narrow ? 'narrow' : 'wide'}',
      );
    }
  });

  testWidgets('a project whose id holds special characters opens its page', (
    tester,
  ) async {
    const root = '/srv/old one #2/';
    await _pumpTall(
      tester,
      ProjectPage(
        model: _model(root: root, name: 'old one #2'),
        now: _now,
      ),
    );
    await tester.pump();

    // The identity is the page's header: the name and the root, which is what
    // tells two same-named projects apart.
    final header = find.byType(PageBody);
    expect(tester.widget<PageBody>(header).title, 'old one #2');
    expect(tester.widget<PageBody>(header).subtitle, root);
    expect(
      find.descendant(of: header, matching: find.text('old one #2')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: header, matching: find.text(root)),
      findsOneWidget,
    );

    // All four sections open.
    for (final (label, mark) in const [
      ('Automations', 'No forge automation is configured for this project.'),
      ('Defaults', 'Model profile'),
      ('Health', 'Run discovery'),
      ('Work', 'No agent is running in this project.'),
    ]) {
      await tester.tap(find.text(label));
      await tester.pump();
      expect(
        find.descendant(
          of: find.byType(ProjectPage),
          matching: find.text(mark),
        ),
        findsOneWidget,
        reason: '$label opens',
      );
    }
  });

  testWidgets('an unsaved edit survives switching sections', (tester) async {
    final model = _model();
    await _pumpTall(
      tester,
      ProjectPage(model: model, initialTab: 2, now: _now),
    );
    await tester.pump();

    // The draft lives in the model, so the edit is the page's, not one
    // tab's.
    model.setRounds('7');
    await tester.pump();
    expect(find.text('7'), findsOneWidget, reason: 'the row shows the edit');

    await tester.tap(find.text('Health'));
    await tester.pump();
    await tester.tap(find.text('Defaults'));
    await tester.pump();

    expect(
      find.text('7'),
      findsOneWidget,
      reason: 'switching sections keeps the unsaved edit',
    );
    expect(model.draft!.rounds, '7');
    expect(model.dirty, isTrue);
  });

  testWidgets('an unreachable project is called out once, on any section', (
    tester,
  ) async {
    await _pumpTall(
      tester,
      ProjectPage(model: _model(), state: StateKind.unavailable, now: _now),
    );
    await tester.pump();

    // One call-out, above the sections. Unavailable replaces the content
    // (§7), so no section draws what it cannot read beneath it.
    expect(find.text('alpha is unreachable'), findsOneWidget);
    expect(find.text('Last seen now ago'), findsOneWidget);
    expect(
      find.text('No agent is running in this project.'),
      findsNothing,
      reason: 'unavailable replaces the Work tab content, not sits beside it',
    );

    // The same one call-out stands on a section that is not Work.
    await tester.tap(find.text('Automations'));
    await tester.pump();
    expect(find.text('alpha is unreachable'), findsOneWidget);
    expect(
      find.text('No forge automation is configured for this project.'),
      findsNothing,
      reason: 'unavailable replaces the Automations content too',
    );
  });

  testWidgets('the keyboard moves between sections', (tester) async {
    await _pumpTall(
      tester,
      ProjectPage(model: _model(), initialTab: 3, now: _now),
    );
    await tester.pump();

    // The first tab stop is the Work tab: Enter opens it, and the line
    // under the tabs follows.
    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    expect(
      find.text('The project\'s work items, its agents and its plan runs'),
      findsOneWidget,
      reason: 'the line names the section the keyboard opened',
    );

    // The next stop is Automations: Enter opens it too.
    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    expect(
      find.text('The forge automations configured for this project'),
      findsOneWidget,
      reason: 'the line follows the keyboard again',
    );
    expect(
      find.text('No forge automation is configured for this project.'),
      findsOneWidget,
      reason: 'Enter opened the section',
    );
  });

  testWidgets('the defaults tab says whether it follows the app or states '
      'checks', (tester) async {
    final model = _model();
    await _pumpTall(
      tester,
      ProjectPage(model: model, initialTab: 2, now: _now),
    );
    await tester.pump();

    expect(find.text('Expected checks'), findsOneWidget);
    expect(
      find.text('Following the defaults'),
      findsOneWidget,
      reason: 'stored settings that state none follow the app\'s',
    );
  });

  testWidgets('adding a check writes both checks and keeps the rest of the '
      'settings', (tester) async {
    final written = <Map<String, dynamic>>[];
    final model = _model(
      factoryDefaults: {
        'targetBranch': 'main',
        'maxRounds': 3,
        'expectedChecks': [
          {'kind': 'check_run', 'name': 'ci:build', 'appId': 42},
        ],
      },
      onWrite: (next) => written.add(next.toJson()),
    );
    await _pumpTall(
      tester,
      ProjectPage(model: model, initialTab: 2, now: _now),
    );
    await tester.pump();
    expect(find.text('1 check'), findsOneWidget);

    await _openChecks(tester, model);

    // The stored check is the sheet's row, its name and app kept whole.
    expect(find.text('ci:build (App 42)'), findsOneWidget);

    // A commit status whose context holds a colon, added from the form. The
    // kinds are chosen in plain words, and stored as the wire's spelling.
    await tester.tap(find.text('Check run'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Commit status'));
    await tester.pumpAndSettle();
    await _enter(tester, 'Name', 'ci:lint:repo');
    await tester.tap(find.text('Add the check'));
    await tester.pumpAndSettle();
    expect(find.text('ci:lint:repo'), findsOneWidget);

    await tester.tap(find.text('Done'));
    await tester.pumpAndSettle();
    expect(find.text('2 checks'), findsOneWidget);

    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    expect(written, hasLength(1));
    final document = written.single;
    expect(document['mode'], 'act', reason: 'unrelated settings survived');
    final factory = document['factoryDefaults'] as Map<String, dynamic>;
    expect(factory['targetBranch'], 'main');
    expect(factory['expectedChecks'], [
      {'kind': 'check_run', 'name': 'ci:build', 'appId': 42},
      {'kind': 'commit_status', 'name': 'ci:lint:repo', 'appId': 0},
    ]);
  });

  testWidgets('mode switching keeps invalid raw text and the page refuses it', (
    tester,
  ) async {
    final written = <Map<String, dynamic>>[];
    final model = _model(onWrite: (next) => written.add(next.toJson()));
    await _openChecks(tester, model);

    // Rows hands over to raw text, which is the draft's own text.
    await tester.tap(find.text('Rows'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Raw text'));
    await tester.pumpAndSettle();
    await _enter(tester, 'Checks', 'nonsense');
    expect(find.textContaining('"nonsense" is neither'), findsOneWidget);

    // Rows and back again: the text the person typed is still there, and so
    // is the line that says why it cannot be stored.
    await tester.tap(find.text('Raw text'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Rows'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Rows'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Raw text'));
    await tester.pumpAndSettle();

    expect(find.text('nonsense'), findsOneWidget);
    expect(find.textContaining('"nonsense" is neither'), findsOneWidget);

    await tester.tap(find.text('Done'));
    await tester.pumpAndSettle();
    expect(find.text('Not valid'), findsOneWidget);

    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    expect(model.saveState, SaveState.failed);
    expect(model.saveError, contains('nonsense'));
    expect(written, isEmpty, reason: 'nothing unparsed reached the bridge');
  });

  testWidgets('rows mode over text it cannot read offers no way to lose it', (
    tester,
  ) async {
    final model = _model();
    await _openChecks(tester, model);

    // Rows hands over to the raw lines, which is where a person types.
    await tester.tap(find.text('Rows'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Raw text'));
    await tester.pumpAndSettle();
    await _enter(tester, 'Checks', 'nonsense');
    final typed = model.draft!.expectedChecksText;
    final dirtyAfterTyping = model.dirty;

    // Back to rows: the text parses to no check, so the sheet says so in
    // the parser's own words rather than claiming nothing is stated. An add
    // form here would rewrite the text from the empty parse and lose it.
    await tester.tap(find.text('Raw text'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Rows'));
    await tester.pumpAndSettle();

    expect(
      model.draft!.expectedChecksText,
      typed,
      reason: 'switching to rows leaves the typed text byte for byte',
    );
    expect(
      model.dirty,
      dirtyAfterTyping,
      reason: 'merely viewing the unreadable text does not dirty the draft',
    );
    expect(find.textContaining('"nonsense" is neither'), findsOneWidget);
    expect(
      find.text('No check is stated.'),
      findsNothing,
      reason: 'the sheet does not claim the project states no check',
    );
    expect(
      find.text('Add the check'),
      findsNothing,
      reason: 'no add form over text that cannot be read',
    );
    expect(
      find.text('Show checks observed in this project'),
      findsNothing,
      reason: 'no suggestion list that would rewrite the text',
    );

    // The one way on is the text itself, still holding what was typed.
    await tester.tap(find.text('Edit the text as raw lines'));
    await tester.pumpAndSettle();
    expect(find.text('nonsense'), findsOneWidget);
    expect(model.draft!.expectedChecksText, typed);
  });

  testWidgets('observed checks are offered only when asked, and added one at '
      'a time', (tester) async {
    final written = <Map<String, dynamic>>[];
    final model = _model(onWrite: (next) => written.add(next.toJson()));
    const observed = [
      ObservedCheck(
        check: ExpectedCheck(kind: 'check_run', name: 'ci/build', appID: 11),
        observedAt: '2026-11-17T03:00:00Z',
        revision: 2,
      ),
      ObservedCheck(
        check: ExpectedCheck(kind: 'check_run', name: 'ci/build', appID: 12),
        observedAt: '2026-11-17T04:00:00Z',
        revision: 3,
      ),
    ];
    await _openChecks(tester, model, observedChecks: observed);

    // Nothing is offered until the person asks for it.
    expect(find.text('ci/build (App 11)'), findsNothing);
    await tester.tap(find.text('Show checks observed in this project'));
    await tester.pumpAndSettle();

    // Both are listed, told apart by their app, and each says where it came
    // from: the round, and how long ago (§8: never a raw timestamp).
    expect(find.text('ci/build (App 11)'), findsOneWidget);
    expect(find.text('ci/build (App 12)'), findsOneWidget);
    expect(find.text('seen in round 2, 1h 12m ago'), findsOneWidget);
    expect(find.text('seen in round 3, 12m ago'), findsOneWidget);
    expect(
      find.textContaining('2026-11-17T'),
      findsNothing,
      reason: 'no raw timestamp in person-facing copy',
    );
    expect(
      find.text(
        'These were recorded by earlier work; they are not a complete list '
        'of what the forge requires.',
      ),
      findsOneWidget,
    );

    // Looking is not choosing: the draft and the bridge are untouched.
    expect(model.dirty, isFalse);
    expect(written, isEmpty);

    await tester.tap(find.text('Add').first);
    await tester.pumpAndSettle();

    expect(model.draft!.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'ci/build', appID: 11),
    ]);
  });

  testWidgets('with nothing observed the editor says so and takes a check by '
      'hand', (tester) async {
    final model = _model();
    await _openChecks(tester, model);

    await tester.tap(find.text('Show checks observed in this project'));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'No check has been observed in this project yet, so add them by hand.',
      ),
      findsOneWidget,
    );

    // The add form is still there, and still the way in.
    await _enter(tester, 'Name', 'build');
    await _enter(tester, 'App id', '42');
    await tester.tap(find.text('Add the check'));
    await tester.pumpAndSettle();

    expect(model.draft!.expectedChecks, [
      const ExpectedCheck(kind: 'check_run', name: 'build', appID: 42),
    ]);
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
