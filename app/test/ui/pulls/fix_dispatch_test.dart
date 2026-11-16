import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/pulls_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/pulls/fix_control.dart';
import 'package:marshal_app/src/ui/pulls/pull_facts.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// The one click (F5 decision 12) and what the card says while it happens
/// (decision 15): a dispatch posts with no further input, shows as sending
/// until the bridge answers, and shows as accepted — being fixed — not as
/// fixed. The caret holds exactly four variations.
///
/// The dispatch runs inside `runAsync`: the fake bridge is a real socket, and
/// a widget test's own clock never drives one. What a tap does is pinned by
/// `test/kit/pr_card_test.dart`; here the card's own callbacks are what run.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  contract.PullRecord record({
    int number = 131,
    String title = 'Quiet capture mode',
    contract.PullState state = contract.PullState.pullFailing,
    List<contract.PullCheck> checks = const [
      contract.PullCheck(name: 'build', state: contract.CheckState.checkPassed),
      contract.PullCheck(name: 'unit', state: contract.CheckState.checkFailed),
    ],
    contract.PullFix? fix,
  }) => contract.PullRecord(
    id: 'acme/widgets#$number',
    repoID: 'acme/widgets',
    projectRoot: '/home/u/marshal',
    number: number,
    url: 'https://forge.example/acme/widgets/pull/$number',
    title: title,
    headRef: 'agent/quiet-capture',
    baseRef: 'main',
    headSHA: 'a1b2c3d4',
    draft: false,
    additions: 412,
    deletions: 96,
    origin: contract.PullOrigin.pullOriginAgent,
    agentID: 'a-1',
    state: state,
    checks: checks,
    reviews: '2 reviews passed',
    fix: fix,
    observedAt: 1000,
    updatedAt: 1000,
  );

  /// The body the bridge answers a dispatched fix with: the record, accepted
  /// and now being fixed.
  FakeReply accepted({Duration? delay}) => FakeReply(
    202,
    headers: const {'content-type': 'application/json'},
    body: jsonEncode(
      record(
        state: contract.PullState.pullBeingFixed,
        fix: const contract.PullFix(
          id: 'f-1',
          check: 'unit',
          headSHA: 'a1b2c3d4',
          mode: 'fix',
          startedAt: 2000,
        ),
      ).toJson(),
    ),
    delay: delay,
  );

  /// A model over one host serving [records], and the client it posts
  /// through.
  Future<(ModelFixture, PullsModel)> modelFor(
    WidgetTester tester,
    List<contract.PullRecord> records,
  ) async {
    final built = await tester.runAsync(() async {
      final fixture = await ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/marshal')],
          agents: [agent(id: 'a-1', name: 'Quiet capture mode')],
        ),
      ]);
      await fixture.refresh();
      fixture.serve('local', '/api/pulls', {
        'pulls': [for (final r in records) r.toJson()],
      });
      final store = PullsStore(fixture.registry, fixture.store);
      addTearDown(store.dispose);
      await eventually(() => store.all().isNotEmpty);
      final model = PullsModel(store, client: fixture.clientFor);
      addTearDown(model.dispose);
      await eventually(() => model.views.isNotEmpty);
      return (fixture, model);
    });
    return built!;
  }

  /// The views in one group, which the model always holds a list for.
  List<PullView> inGroup(PullsModel model, PullGroup group) =>
      model.groups[group] ?? const [];

  /// The card for the single pull request, wired the way a screen wires it.
  ///
  /// It follows the record as it moves between groups, so a card that is
  /// accepted is drawn from `beingFixed` and one that was stopped from
  /// `failing` — the same way a place re-draws it.
  Future<void> pumpCard(WidgetTester tester, PullsModel model) async {
    tester.view.physicalSize = const Size(900, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        home: Align(
          alignment: Alignment.topLeft,
          child: Padding(
            padding: const EdgeInsets.all(16),
            child: SizedBox(
              width: 820,
              child: AnimatedBuilder(
                animation: model,
                builder: (context, _) {
                  final pull = model.views.single;
                  return PrCard(
                    pull: pullFactsOf(pull, now: now),
                    actions: pullCardActions(context, pull: pull, model: model),
                  );
                },
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pump();
  }

  /// What the pumped card's buttons and caret actually run.
  PrActions actionsOf(WidgetTester tester) =>
      tester.widget<PrCard>(find.byType(PrCard)).actions;

  /// Runs [work] where the real socket can answer it, then renders what it
  /// changed.
  Future<void> settle(
    WidgetTester tester,
    Future<Object?> Function() work,
  ) async {
    await tester.runAsync(work);
    await tester.pump();
    await tester.pump();
  }

  testWidgets('one click dispatches with no further input', (tester) async {
    final (fixture, model) = await modelFor(tester, [record()]);
    final encoded = Uri.encodeComponent('acme/widgets#131');
    fixture.bridges['local']!.reply(
      'POST',
      '/api/pulls/$encoded/fix',
      accepted(),
    );
    await pumpCard(tester, model);

    // No sheet, no field: the button's own callback is the whole interaction.
    final fix = actionsOf(tester).fix;
    expect(fix, isNotNull);
    await settle(tester, () async {
      fix!();
      await eventually(
        () => inGroup(model, PullGroup.beingFixed).isNotEmpty,
        reason: 'the dispatch is accepted',
      );
      return null;
    });

    final posted = fixture
        .requests('local')
        .where((r) => r.method == 'POST' && r.uri.endsWith('/fix'))
        .toList();
    expect(posted, hasLength(1), reason: 'one click, one dispatch');
    expect(jsonDecode(posted.single.body), {'check': 'unit', 'mode': 'fix'});
    expect(find.text('A worker is on it'), findsOneWidget);
  });

  testWidgets('the card shows sending then being fixed', (tester) async {
    final (fixture, model) = await modelFor(tester, [record()]);
    final encoded = Uri.encodeComponent('acme/widgets#131');
    fixture.bridges['local']!.reply(
      'POST',
      '/api/pulls/$encoded/fix',
      // Held, so the dispatch can be read while it is in flight.
      accepted(delay: const Duration(milliseconds: 30)),
    );
    await pumpCard(tester, model);
    final fix = actionsOf(tester).fix!;

    await settle(tester, () async {
      fix();
      // Posted and not yet answered: the card acknowledges it at once and
      // offers no second dispatch (pr_card_test pins those words).
      expect(
        inGroup(model, PullGroup.failing).single.sending,
        isTrue,
        reason: 'a dispatch in flight is shown as sending',
      );
      await eventually(() => inGroup(model, PullGroup.beingFixed).isNotEmpty);
      return null;
    });

    final acceptedView = inGroup(model, PullGroup.beingFixed).single;
    expect(acceptedView.sending, isFalse);
    expect(acceptedView.record.fix?.id, 'f-1');
    expect(
      find.text('A worker is on it'),
      findsOneWidget,
      reason: 'accepted is not fixed: a worker is on it',
    );
    expect(find.text('Sending…'), findsNothing);
  });

  testWidgets('stop clears it', (tester) async {
    final (fixture, model) = await modelFor(tester, [
      record(
        state: contract.PullState.pullBeingFixed,
        fix: const contract.PullFix(
          id: 'f-1',
          check: 'unit',
          headSHA: 'a1b2c3d4',
          mode: 'fix',
          startedAt: 900,
        ),
      ),
    ]);
    final encoded = Uri.encodeComponent('acme/widgets#131');
    fixture.bridges['local']!
      ..reply('POST', '/api/pulls/$encoded/fix/stop', const FakeReply(200))
      ..reply(
        'GET',
        '/api/pulls/$encoded',
        FakeReply(
          200,
          headers: const {'content-type': 'application/json'},
          body: jsonEncode(record().toJson()),
        ),
      );
    await pumpCard(tester, model);
    final stop = actionsOf(tester).stop!;

    await settle(tester, () async {
      stop();
      await eventually(
        () => inGroup(model, PullGroup.beingFixed).isEmpty,
        reason: 'the fix is cleared',
      );
      return null;
    });

    expect(
      fixture
          .requests('local')
          .where((r) => r.method == 'POST' && r.uri.endsWith('/fix/stop')),
      hasLength(1),
    );
    expect(inGroup(model, PullGroup.failing), hasLength(1));
    await pumpCard(tester, model);
    expect(find.text('A worker is on it'), findsNothing);
    expect(find.text('Fix with a worker'), findsOneWidget);
  });

  testWidgets('the caret has exactly four items', (tester) async {
    final (_, model) = await modelFor(tester, [record()]);
    await pumpCard(tester, model);

    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    await tester.pump();

    expect(find.text('Investigate only'), findsOneWidget);
    expect(find.text('Fix with a note'), findsOneWidget);
    expect(find.text('Choose the worker'), findsOneWidget);
    expect(
      find.text('Always fix failures automatically in marshal'),
      findsOneWidget,
    );
    expect(
      find.text('Report the cause, change nothing'),
      findsOneWidget,
      reason: 'each item says what it does in one line',
    );
    expect(
      actionsOf(tester).fixChoices,
      hasLength(4),
      reason: 'exactly four, no more and no fewer',
    );
  });

  testWidgets('fix with a note opens a sheet with one field', (tester) async {
    final (fixture, model) = await modelFor(tester, [record()]);
    await pumpCard(tester, model);
    final withNote = actionsOf(tester).fixChoices[1].onSelect;

    withNote();
    await tester.pumpAndSettle();

    expect(find.text('Fix with a note'), findsWidgets);
    expect(
      find.byType(Field),
      findsOneWidget,
      reason: 'one field, and the sheet is where the note is typed',
    );
    expect(find.text('Send it'), findsOneWidget);

    // Closing it posts nothing: an empty note is not a dispatch.
    await tester.tap(find.byIcon(Glyphs.xmark));
    await tester.pumpAndSettle();
    expect(fixture.requests('local').where((r) => r.method == 'POST'), isEmpty);
  });

  testWidgets('auto-fix changes the project setting', (tester) async {
    final (fixture, model) = await modelFor(tester, [record()]);
    fixture.bridges['local']!
      ..reply(
        'GET',
        '/api/projects/settings',
        const FakeReply(
          200,
          headers: {'content-type': 'application/json'},
          body:
              '{"workspace":"default","automations":{"ciFixer":'
              '{"enabled":true,"repoId":"acme/widgets"}}}',
        ),
      )
      ..reply('PUT', '/api/projects/settings', const FakeReply(200));
    await pumpCard(tester, model);
    final autoFix = actionsOf(tester).fixChoices.last.onSelect;

    await settle(tester, () async {
      autoFix();
      await eventually(
        () => fixture.requests('local').any((r) => r.method == 'PUT'),
        reason: 'the setting is written',
      );
      return null;
    });
    // It says so: the item changes a project setting and confirms it.
    await tester.pumpAndSettle();
    expect(find.text('Automatic fixing is on'), findsOneWidget);

    final put = fixture.requests('local').lastWhere((r) => r.method == 'PUT');
    final body = jsonDecode(put.body) as Map<String, dynamic>;
    expect((body['automations'] as Map)['autoFix'], isTrue);
    expect(
      body['workspace'],
      'default',
      reason: 'the rest of the settings document is kept',
    );
  });
}
