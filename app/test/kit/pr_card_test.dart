import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  PullRequestFacts pull(PrState state, {bool draft = false}) =>
      PullRequestFacts(
        number: 123,
        title: 'Quiet capture mode',
        repo: 'alecpullen/marshal',
        branch: 'factory/voice-planning',
        adds: 412,
        dels: 96,
        checks: [
          const Check('build', CheckState.passed),
          Check('test', switch (state) {
            PrState.failing || PrState.fixing => CheckState.failed,
            PrState.inProgress => CheckState.running,
            PrState.ready => CheckState.passed,
          }),
        ],
        reviews: '2 reviews passed',
        age: '26m',
        state: state,
        draft: draft,
        failureTail: '--- FAIL: TestScriptedRecorderQuietMode (4.21s)',
        sourceKind: ThreadKind.workItem,
        sourceTitle: 'Voice planning',
      );

  Future<void> pump(
    WidgetTester tester,
    PrState state, {
    PrActions actions = const PrActions(),
    bool draft = false,
    double width = 860,
  }) => pumpKit(
    tester,
    SizedBox(
      width: width - 32,
      child: PrCard(
        pull: pull(state, draft: draft),
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
