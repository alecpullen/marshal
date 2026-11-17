import 'dart:convert';

import 'package:flutter/services.dart' show LogicalKeyboardKey;
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/project_model.dart';
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
ProjectModel _model({String root = '/home/u/alpha', String name = 'alpha'}) =>
    ProjectModel(
      root: root,
      name: name,
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
          {
            'key': 'forge_metadata',
            'state': 'supported',
            'detail': 'It answered',
          },
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
      final identity = find.byKey(const ValueKey('identity'));
      expect(identity, findsOneWidget, reason: narrow ? 'narrow' : 'wide');
      expect(
        find.descendant(of: identity, matching: find.text('alpha')),
        findsOneWidget,
        reason: 'the project\'s name, ${narrow ? 'narrow' : 'wide'}',
      );
      expect(
        find.descendant(of: identity, matching: find.text('/home/u/alpha')),
        findsOneWidget,
        reason: 'the root tells two same-named projects apart',
      );
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

    // The identity line names it, machine path and all.
    final identity = find.byKey(const ValueKey('identity'));
    expect(
      find.descendant(of: identity, matching: find.text('old one #2')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: identity, matching: find.text(root)),
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

    // One call-out, above the sections; the Work tab beneath draws its
    // content, not a second copy.
    expect(find.text('alpha is unreachable'), findsOneWidget);
    expect(find.text('Last seen now ago'), findsOneWidget);
    expect(
      find.text('No agent is running in this project.'),
      findsOneWidget,
      reason: 'the Work tab keeps its content beside the call-out',
    );

    // The same one call-out stands on a section that is not Work.
    await tester.tap(find.text('Automations'));
    await tester.pump();
    expect(find.text('alpha is unreachable'), findsOneWidget);
    expect(
      find.text('No forge automation is configured for this project.'),
      findsOneWidget,
      reason: 'the Automations tab keeps its content too',
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
}
