import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/pulls_store.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/pulls/fix_control.dart';
import 'package:marshal_app/src/ui/pulls/pull_facts.dart';

import '../../model/support.dart';
import '../../support/kit.dart';

/// The Merge control (F5 decision 13) after review: an agent's pull request
/// merges through the agent's own route, and a factory item's is not offered
/// at all — the factory's command names the pull request's reviewed node id,
/// which a pull record does not carry, so the only request the app could make
/// is one the bridge answers 400.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  contract.PullRecord record({bool factory = false}) => contract.PullRecord(
    id: 'acme/widgets#131',
    repoID: 'acme/widgets',
    projectRoot: '/home/u/marshal',
    number: 131,
    url: 'https://forge.example/acme/widgets/pull/131',
    title: 'Quiet capture mode',
    headRef: 'factory/ci-first-review',
    baseRef: 'main',
    headSHA: 'a1b2c3d4',
    draft: false,
    additions: 412,
    deletions: 96,
    origin: factory
        ? contract.PullOrigin.pullOriginFactory
        : contract.PullOrigin.pullOriginAgent,
    agentID: factory ? null : 'a-1',
    factoryItem: factory ? 'item-7' : null,
    state: contract.PullState.pullReady,
    checks: const [
      contract.PullCheck(name: 'build', state: contract.CheckState.checkPassed),
      contract.PullCheck(name: 'unit', state: contract.CheckState.checkPassed),
    ],
    reviews: '2 reviews passed',
    observedAt: 1000,
    updatedAt: 1000,
  );

  /// A model over one host serving [pull], and the client it posts through.
  Future<(ModelFixture, PullsModel)> modelFor(
    WidgetTester tester,
    contract.PullRecord pull,
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
        'pulls': [pull.toJson()],
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

  /// The card for the single pull request, wired the way a screen wires it.
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

  testWidgets('a factory item’s merge is disabled, with the reason, and posts '
      'nothing', (tester) async {
    final (fixture, model) = await modelFor(tester, record(factory: true));
    await pumpCard(tester, model);

    final actions = tester.widget<PrCard>(find.byType(PrCard)).actions;
    expect(actions.merge, isNull, reason: 'there is no route to post');
    expect(actions.mergeNote, workItemMergeNote);

    final merge = tester.widget<Btn>(find.widgetWithText(Btn, 'Merge'));
    expect(merge.onTap, isNull, reason: 'drawn disabled, not offered');
    expect(
      hasTip(tester, find.widgetWithText(Btn, 'Merge'), workItemMergeNote),
      isTrue,
      reason: 'a disabled merge says which phase brings it',
    );

    // A tap on a disabled control takes no action, and nothing is posted: the
    // request that would have been sent is one the bridge always refuses.
    await tester.tap(find.widgetWithText(Btn, 'Merge'));
    await tester.pump();
    expect(
      fixture.requests('local').where((request) => request.method != 'GET'),
      isEmpty,
      reason: 'no merge is posted for a work item’s pull request',
    );
  });

  testWidgets('an agent’s merge is still offered', (tester) async {
    final (_, model) = await modelFor(tester, record());
    await pumpCard(tester, model);

    final actions = tester.widget<PrCard>(find.byType(PrCard)).actions;
    expect(actions.merge, isNotNull, reason: 'the agent route still posts');
    expect(actions.mergeNote, isNull);
    expect(
      tester.widget<Btn>(find.widgetWithText(Btn, 'Merge')).onTap,
      isNotNull,
    );
    // What it posts is `pulls_model_test`'s: the agent's own merge route.
  });
}
