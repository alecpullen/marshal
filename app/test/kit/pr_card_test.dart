import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  /// The facts one card shows, in [state]. The summary is the model's own
  /// sentence (F5 decision 11), so the test hands the card the words it would
  /// have been given rather than expecting it to re-derive them.
  PullRequestFacts pull(
    PrState state, {
    bool draft = false,
    bool sending = false,
    String? dispatchError,
  }) => PullRequestFacts(
    number: 123,
    title: 'Quiet capture mode',
    repo: 'alecpullen/marshal',
    branch: 'factory/voice-planning',
    adds: 412,
    dels: 96,
    checks: switch (state) {
      PrState.unavailable => const [],
      PrState.failing || PrState.fixing => const [
        Check('build', CheckState.passed),
        Check('test', CheckState.failed),
      ],
      PrState.inProgress => const [
        Check('build', CheckState.passed),
        Check('test', CheckState.running),
      ],
      PrState.ready => const [
        Check('build', CheckState.passed),
        Check('test', CheckState.passed),
      ],
    },
    reviews: '2 reviews passed',
    age: '26m',
    state: state,
    draft: draft,
    failureTail: '--- FAIL: TestScriptedRecorderQuietMode (4.21s)',
    checkSummary: switch (state) {
      PrState.ready => '2 of 2 checks passed',
      PrState.failing || PrState.fixing => '1 failing: test',
      PrState.inProgress => '1 of 2 checks passed, the rest running',
      PrState.unavailable =>
        'Checks unavailable: the credential is an SSH key, which cannot call '
            'the API',
    },
    sending: sending,
    dispatchError: dispatchError,
    sourceKind: ThreadKind.workItem,
    sourceTitle: 'Voice planning',
  );

  Future<void> pump(
    WidgetTester tester,
    PrState state, {
    PrActions actions = const PrActions(),
    bool draft = false,
    bool sending = false,
    String? dispatchError,
    double width = 860,
  }) => pumpKit(
    tester,
    SizedBox(
      width: width - 32,
      child: PrCard(
        pull: pull(
          state,
          draft: draft,
          sending: sending,
          dispatchError: dispatchError,
        ),
        actions: actions,
      ),
    ),
    width: width,
  );

  List<String> labels(WidgetTester tester) => [
    for (final b in tester.widgetList<Btn>(find.byType(Btn))) b.label,
  ];

  testWidgets('ready: Review first and primary, then Merge', (tester) async {
    var reviewed = 0;
    await pump(
      tester,
      PrState.ready,
      actions: PrActions(review: () => reviewed++, merge: () {}),
    );
    expect(labels(tester), ['Review', 'Merge', 'Open on forge']);
    expect(tester.widget<Btn>(find.byType(Btn).first).primary, isTrue);
    expect(tester.widget<Box>(find.byType(Box)).tone, BoxTone.plain);
    expect(find.text('2 of 2 checks passed'), findsOneWidget);
    expect(
      find.text('--- FAIL: TestScriptedRecorderQuietMode (4.21s)'),
      findsNothing,
    );
    await tester.tap(find.text('Review'));
    expect(reviewed, 1);
  });

  testWidgets('ready: a merge it cannot make is disabled and says why', (
    tester,
  ) async {
    // A merge the app has no reviewed identity for is not offered as a button
    // that would fail: it is drawn disabled with the phase that supplies it.
    const note =
        'Merging a work item’s pull request arrives with work items '
        '(F6)';
    await pump(
      tester,
      PrState.ready,
      actions: const PrActions(mergeNote: note),
    );

    final merge = tester.widget<Btn>(find.widgetWithText(Btn, 'Merge'));
    expect(merge.onTap, isNull);
    expect(
      hasTip(tester, find.widgetWithText(Btn, 'Merge'), note),
      isTrue,
      reason: 'a disabled merge says why',
    );
  });

  testWidgets('failing: failing border, the log tail, Fix with a worker', (
    tester,
  ) async {
    var fixed = 0;
    var investigated = 0;
    await pump(
      tester,
      PrState.failing,
      actions: PrActions(
        fix: () => fixed++,
        fixChoices: [
          MenuChoice(
            'Investigate only',
            'Report the cause, change nothing',
            onSelect: () => investigated++,
          ),
        ],
      ),
    );
    expect(tester.widget<Box>(find.byType(Box)).tone, BoxTone.failing);
    expect(labels(tester), ['Fix with a worker', 'Full log', 'Open on forge']);
    expect(tester.widget<Btn>(find.byType(Btn).first).primary, isTrue);
    expect(find.text('1 failing: test'), findsOneWidget);
    expect(styleOf(tester, '1 failing: test').color, e.err);
    expect(
      find.text('--- FAIL: TestScriptedRecorderQuietMode (4.21s)'),
      findsOneWidget,
    );

    await tester.tap(find.text('Fix with a worker'));
    expect(fixed, 1);

    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    await tester.tap(find.text('Investigate only'));
    await tester.pump();
    expect(investigated, 1);
  });

  testWidgets('failing and sending: the dispatch is acknowledged, not done', (
    tester,
  ) async {
    var fixed = 0;
    await pump(
      tester,
      PrState.failing,
      sending: true,
      actions: PrActions(fix: () => fixed++),
    );

    expect(find.text('Sending…'), findsOneWidget);
    expect(
      tester.widget<Btn>(find.byType(Btn).first).onTap,
      isNull,
      reason: 'one dispatch is in flight; a second is not posted',
    );
  });

  testWidgets('a refused dispatch says why, in the bridge words', (
    tester,
  ) async {
    await pump(
      tester,
      PrState.failing,
      dispatchError: 'that check is not failing',
      actions: PrActions(fix: () {}),
    );

    expect(find.text('that check is not failing'), findsOneWidget);
    expect(styleOf(tester, 'that check is not failing').color, e.err);
  });

  testWidgets('fixing: a worker is on it, with Watch and Stop', (tester) async {
    await pump(tester, PrState.fixing);
    expect(find.text('A worker is on it'), findsOneWidget);
    expect(labels(tester), ['Watch', 'Stop']);
    expect(tester.widget<Dot>(find.byType(Dot)).status, Status.running);
    expect(tester.widget<Box>(find.byType(Box)).tone, BoxTone.plain);
  });

  testWidgets('in progress: only Open on forge; a draft says so', (
    tester,
  ) async {
    await pump(tester, PrState.inProgress, draft: true);
    expect(labels(tester), ['Open on forge']);
    expect(find.text('Draft'), findsOneWidget);
    expect(find.text('1 of 2 checks passed, the rest running'), findsOneWidget);
  });

  testWidgets('checks unavailable: the reason, and never shown as passing', (
    tester,
  ) async {
    await pump(tester, PrState.unavailable);
    expect(labels(tester), ['Open on forge'], reason: 'nothing to fix');
    expect(
      find.text(
        'Checks unavailable: the credential is an SSH key, which cannot call '
        'the API',
      ),
      findsOneWidget,
    );
    expect(
      tester.widget<Box>(find.byType(Box)).tone,
      BoxTone.plain,
      reason: 'unreadable checks are not a failure',
    );
    expect(
      styleOf(
        tester,
        'Checks unavailable: the credential is an SSH key, which cannot call '
        'the API',
      ).color,
      e.muted,
    );
    expect(find.byType(CheckStrip), findsNothing);
  });

  testWidgets('every icon names itself', (tester) async {
    await pump(tester, PrState.failing);
    expect(hasTip(tester, find.byType(Icon).first, 'Checks failing'), isTrue);
    expect(
      hasTip(
        tester,
        find.byIcon(ThreadKind.workItem.icon),
        'Open the work item this came from',
      ),
      isTrue,
    );
  });

  testWidgets('the source opens its thread', (tester) async {
    var opened = 0;
    await pump(
      tester,
      PrState.ready,
      actions: PrActions(openSource: () => opened++),
    );
    await tester.tap(find.text('Voice planning'));
    expect(opened, 1);
  });

  testWidgets('on a phone the card stacks and nothing overflows', (
    tester,
  ) async {
    for (final state in PrState.values) {
      await pump(tester, state, width: 390, draft: true);
      expect(tester.takeException(), isNull, reason: '$state');
    }
  });
}
