import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/connect_repo_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// Connecting a repository from the sidebar (F9 B1): the plus beside
/// *Projects* registers one, and on success offers the discovery that makes
/// its issues appear.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// Lets the register's real round trip run: a pump advances the widget-side
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

  testWidgets('a repository is connected from the sidebar', (tester) async {
    tester.view.physicalSize = const Size(1200, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final fixture = await tester.runAsync(
      () => ModelFixture.start([
        FixtureHost(name: 'local', projects: [project('/home/u/marshal')]),
      ], now: now),
    );
    final bridge = fixture!.bridges['local']!;
    await tester.runAsync(() => fixture.refresh());
    bridge.reply(
      'POST',
      '/api/repos',
      const FakeReply(
        201,
        body:
            '{"id":"acme-marshal","url":"https://github.com/acme/marshal.git",'
            '"ownerId":"owner-1","forge":"github"}',
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
          connectRepoFor: (host) => ConnectRepoModel(fixture.clientFor(host)),
        ),
      ),
    );
    await settleIo(tester);

    await tester.tap(find.byTooltip('Connect a repository'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.text('Connect a repository'), findsOneWidget);

    // The sidebar has a field of its own, so the sheet's is named by its
    // sheet: the URL first, then the id it suggests.
    await tester.enterText(
      find
          .descendant(
            of: find.byType(Sheet),
            matching: find.byType(EditableText),
          )
          .first,
      'https://github.com/acme/marshal.git',
    );
    await tester.pump();
    await tester.tap(find.text('Connect the repository'));
    await settleIo(tester);

    // The bridge's answer is what the sheet shows, with the discovery that
    // makes its issues appear.
    expect(find.text('https://github.com/acme/marshal.git'), findsOneWidget);
    expect(find.text('Discover its issues'), findsOneWidget);
  });
}
