import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/model/new_project_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// Adding a project from the sidebar (docs/studio-flutter/new-project.md):
/// the plus beside *Projects* opens the sheet, a clone is followed to its end,
/// and the new project's conversation opens.
void main() {
  final now = DateTime.utc(2026, 11, 25, 12);

  /// Lets the real round trips run: a pump advances the widget-side futures,
  /// and the delay gives the in-process bridge its time.
  Future<void> settleIo(WidgetTester tester) async {
    for (var i = 0; i < 12; i++) {
      // A clone's next read waits on a timer, which runs on the test's clock.
      await tester.pump(const Duration(milliseconds: 10));
      await tester.runAsync(
        () => Future<void>.delayed(const Duration(milliseconds: 30)),
      );
    }
    await tester.pump();
  }

  FakeReply json(Object body, [int status = 200]) => FakeReply(
    status,
    headers: const {'content-type': 'application/json'},
    body: jsonEncode(body),
  );

  Finder sheetField(int index) => find
      .descendant(of: find.byType(Sheet), matching: find.byType(EditableText))
      .at(index);

  testWidgets('a repository is cloned into a new project, which opens', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    await tester.runAsync(() => fixture.refresh());
    const root = '/home/u/projects/marshal';
    final creation = {
      'id': 'np-1',
      'kind': 'clone',
      'root': root,
      'url': 'https://github.com/acme/marshal.git',
      'startedAt': 1764000000000,
    };
    bridge
      ..reply(
        'GET',
        '/api/projects/new',
        json({'projectsDir': '/home/u/projects', 'creations': []}),
      )
      ..reply(
        'POST',
        '/api/projects/new',
        json({...creation, 'state': 'cloning'}, 202),
      )
      ..reply(
        'GET',
        '/api/projects/new/np-1',
        json({...creation, 'state': 'ready'}),
      );
    // Once the clone is registered, the host lists it.
    fixture.serve('local', '/api/projects', [
      project('/home/u/alpha').toJson(),
      project(root).toJson(),
    ]);

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
          newProjectFor: (host) => NewProjectModel(
            clients: {'local': fixture.clientFor('local')},
            host: host,
            poll: Duration.zero,
          ),
        ),
      ),
    );
    await settleIo(tester);

    await tester.tap(find.byTooltip('Add a project'));
    await settleIo(tester);
    expect(find.text('Add a project'), findsOneWidget);

    await tester.tap(find.widgetWithText(Btn, 'Clone').first);
    await tester.pump();
    await tester.enterText(
      sheetField(0),
      'https://github.com/acme/marshal.git',
    );
    await tester.pump();
    // The name follows the URL, and the folder follows the name, under the
    // host's projects folder.
    String fieldText(int index) =>
        tester.widget<EditableText>(sheetField(index)).controller.text;
    expect(fieldText(1), 'marshal');
    expect(fieldText(2), root);

    await tester.tap(find.widgetWithText(Btn, 'Clone').last);
    await settleIo(tester);
    // The sheet's exit transition.
    await tester.pump(const Duration(milliseconds: 400));
    await settleIo(tester);

    final post = bridge.requests.singleWhere(
      (r) => r.method == 'POST' && r.uri == '/api/projects/new',
    );
    expect(jsonDecode(post.body), {
      'kind': 'clone',
      'root': root,
      'url': 'https://github.com/acme/marshal.git',
    });
    // The sheet has closed, and the new project is listed and open.
    expect(find.byType(Sheet), findsNothing);
    expect(fixture.store.projects().map((p) => p.root), contains(root));
    expect(
      tester.widget<NavRow>(find.widgetWithText(NavRow, 'marshal')).selected,
      isTrue,
      reason: 'the new project is the one open',
    );
  });

  testWidgets('a refusal stays on the sheet, in the bridge’s words', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    await tester.runAsync(() => fixture.refresh());
    bridge
      ..reply(
        'GET',
        '/api/projects/new',
        json({'projectsDir': '/home/u/projects', 'creations': []}),
      )
      ..reply(
        'POST',
        '/api/projects/new',
        json({
          'error':
              '/home/u/projects/alpha already has files in it: add it as an '
              'existing folder instead',
        }, 400),
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
          newProjectFor: (host) => NewProjectModel(
            clients: {'local': fixture.clientFor('local')},
            host: host,
            poll: Duration.zero,
          ),
        ),
      ),
    );
    await settleIo(tester);
    await tester.tap(find.byTooltip('Add a project'));
    await settleIo(tester);

    await tester.enterText(sheetField(0), 'alpha');
    await tester.pump();
    await tester.tap(find.widgetWithText(Btn, 'Create'));
    await settleIo(tester);

    expect(find.byType(Sheet), findsOneWidget);
    expect(find.textContaining('already has files in it'), findsOneWidget);
  });
}
