import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/pulls/pulls_place.dart';

import '../../model/support.dart';
import '../../support/kit.dart';

/// The Pull requests place's rules (F5 decision 10): the groups in the spec's
/// order, each under its own label and count, and no group drawn when it holds
/// nothing. The sidebar's row is the ready count (decision 14).
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  PullView pull(int number, PullGroup group) => PullView(
    record: contract.PullRecord(
      id: 'acme/widgets#$number',
      repoID: 'acme/widgets',
      projectRoot: '/home/u/marshal',
      number: number,
      url: 'https://forge.example/acme/widgets/pull/$number',
      title: 'Quiet capture mode',
      headRef: 'agent/quiet-capture',
      baseRef: 'main',
      headSHA: 'a1b2c3d4',
      draft: false,
      additions: 412,
      deletions: 96,
      origin: contract.PullOrigin.pullOriginAgent,
      agentID: 'a-1',
      state: switch (group) {
        PullGroup.ready => contract.PullState.pullReady,
        PullGroup.failing => contract.PullState.pullFailing,
        PullGroup.beingFixed => contract.PullState.pullBeingFixed,
        PullGroup.inProgress => contract.PullState.pullInProgress,
        PullGroup.unavailable => contract.PullState.pullChecksUnavailable,
      },
      checks: const [
        contract.PullCheck(
          name: 'unit',
          state: contract.CheckState.checkPassed,
        ),
      ],
      reviews: '2 reviews passed',
      observedAt: 0,
      updatedAt: 0,
    ),
    thread: null,
    group: group,
    checkSummary: '1 of 1 checks passed',
    checks: const [PullCheckView('unit', PullCheckState.passed)],
  );

  Future<void> pump(
    WidgetTester tester,
    Map<PullGroup, List<PullView>> views,
  ) => pumpKit(
    tester,
    SizedBox(
      width: 860,
      child: PullsPlace(groups: views, now: now, onRetry: () {}),
    ),
    width: 900,
    height: 1400,
  );

  List<String> labels(WidgetTester tester) => [
    for (final label in tester.widgetList<Label>(find.byType(Label)))
      label.text,
  ];

  List<int?> counts(WidgetTester tester) => [
    for (final label in tester.widgetList<Label>(find.byType(Label)))
      label.count,
  ];

  testWidgets('groups in order', (tester) async {
    await pump(tester, {
      for (final group in PullGroup.values) group: [pull(group.index, group)],
    });

    expect(labels(tester), [
      'Ready for review',
      'Checks failing',
      'Being fixed by a worker',
      'In progress',
      'Checks unavailable',
    ]);
    expect(counts(tester), [
      1,
      1,
      1,
      1,
      1,
    ], reason: 'each label carries its own count');
  });

  testWidgets('empty groups are omitted', (tester) async {
    await pump(tester, {
      PullGroup.ready: [pull(1, PullGroup.ready), pull(2, PullGroup.ready)],
      PullGroup.failing: [pull(3, PullGroup.failing)],
      PullGroup.beingFixed: const [],
      PullGroup.inProgress: const [],
      PullGroup.unavailable: const [],
    });

    expect(labels(tester), [
      'Ready for review',
      'Checks failing',
    ], reason: 'a heading with nothing under it says nothing');
    expect(counts(tester), [2, 1]);
    expect(find.text('BEING FIXED BY A WORKER'), findsNothing);
  });

  test('sidebar count is the ready count', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
    ], now: now);
    final nav = buildNavModel(
      const Place(PlaceKind.pulls),
      fixture.store,
      pullsReady: 2,
    );

    final row = nav.fixed.singleWhere((place) => place.kind == PlaceKind.pulls);
    expect(row.label, 'Pull requests');
    expect(row.count, 2);
    expect(
      row.planned,
      isFalse,
      reason: 'the place exists, so its row opens it',
    );
    expect(row.active, isTrue);
  });
}
