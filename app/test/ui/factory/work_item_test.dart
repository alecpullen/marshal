import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/ui/factory/work_item_screen.dart';
import 'package:marshal_app/src/model/work_item_model.dart' show WorkItemView;
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../model/support.dart';
import '../../support/kit.dart';

/// A work item's thread (F6 decisions 4–6): its strip, its activity, and its
/// five tabs, each read without a bridge.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  FactoryCI ci() => FactoryCI.fromJson({
    'headSha': 'head-1',
    'checks': [
      {
        'key': {'kind': 'check_run', 'name': 'test', 'appId': 7},
        'id': 'check-test',
        'sha': 'head-1',
        'status': 'completed',
        'conclusion': 'success',
        'url': '',
        'logTail': '',
        'observedAt': '2026-11-17T11:00:00Z',
      },
      {
        'key': {'kind': 'check_run', 'name': 'build', 'appId': 7},
        'id': 'check-build',
        'sha': 'head-1',
        'status': 'completed',
        'conclusion': 'failure',
        'url': '',
        'logTail': '',
        'observedAt': '2026-11-17T11:00:00Z',
      },
    ],
    'complete': true,
    'observedAt': '2026-11-17T11:00:00Z',
  });

  FactoryReport review() => FactoryReport.fromJson({
    'operationId': 'effect-reviewer',
    'outcome': 'findings',
    'summary': 'One finding.',
    'question': '',
    'reason': '',
    'baseSha': 'base-1',
    'headSha': 'head-1',
    'inputsAccessible': true,
    'findings': [
      {
        'severity': 'minor',
        'path': 'app/lib/src/ui/factory/work_item_screen.dart',
        'line': 42,
        'title': 'Name the stage in words as well as the strip',
      },
    ],
  });

  FactoryRound round() => FactoryRound.fromJson({
    'number': 1,
    'approvalRevision': 1,
    'implementEffectId': 'effect-implementer',
    'baseSha': 'base-1',
    'headSha': 'head-1',
    'treeSha': 'tree-1',
    'ci': ci().toJson(),
    'implementation': null,
    'review': review().toJson(),
    'startedAt': '2026-11-17T11:00:00Z',
    'finishedAt': '2026-11-17T11:30:00Z',
  });

  FactoryEffect effect() => FactoryEffect.fromJson({
    'outcome': null,
    'id': 'effect-implementer',
    'kind': 'implement',
    'status': 'done',
    'workerRole': 'implementer',
    'round': 1,
    'approvalRevision': 1,
    'worker': null,
    'expectedHead': 'head-1',
    'expectedBase': 'base-1',
    'createdAt': '2026-11-17T11:00:00Z',
  });

  FactoryDecision decision() => FactoryDecision.fromJson({
    'id': 'decision-1',
    'kind': 'scope',
    'question': 'Which inspection does the inspector show?',
    'reason': 'the plan names both',
    'effectId': 'effect-1',
    'createdAt': '2026-11-17T11:00:00Z',
  });

  FactoryItem item({
    String state = 'reviewing',
    String stage = '',
    List<FactoryRound> rounds = const [],
    int version = 1,
    FactoryDecision? pending,
  }) => factoryItem(
    state: state,
    stage: stage,
    rounds: rounds,
    version: version,
    decision: pending,
    workers: [factoryWorker(agentId: 'agent-imp', role: 'implementer')],
    effects: [effect()],
    history: [
      factoryEvent(1, 'created', 'the draft was created'),
      factoryEvent(
        2,
        'implement_started',
        'the implementer started',
        effectId: 'effect-implementer',
      ),
      factoryEvent(3, 'review_completed', 'the reviewer answered'),
    ],
  );

  Future<void> pump(WidgetTester tester, FactoryItem subject) => pumpKit(
    tester,
    WorkItemScreen(
      item: WorkItemView.of('local', subject),
      now: now,
      pulls: const [],
    ),
    width: 1000,
    height: 2000,
  );

  Future<void> openTab(WidgetTester tester, String label) async {
    await tester.tap(
      find.descendant(of: find.byType(Tabs), matching: find.text(label)),
    );
    await tester.pump();
  }

  /// One pump of the screen, with the whole tree the same every time so the
  /// State survives a re-pump: [subject] and [center] vary, nothing else.
  Future<void> show(
    WidgetTester tester,
    FactoryItem subject, {
    Widget? center,
    Future<void> Function(String answer)? onAnswer,
    VoidCallback? onProposeChange,
    double height = 2000,
  }) => pumpKit(
    tester,
    WorkItemScreen(
      item: WorkItemView.of('local', subject),
      now: now,
      pulls: const [],
      center: center,
      onBack: () {},
      onAnswer: onAnswer,
      onProposeChange: onProposeChange,
    ),
    width: 1000,
    height: height,
  );

  /// The words in the answer field.
  String answerText(WidgetTester tester) =>
      tester.widget<EditableText>(find.byType(EditableText)).controller.text;

  testWidgets('strip shows the stage', (tester) async {
    await pump(tester, item(state: 'reviewing'));
    final strip = tester.widget<StageStrip>(find.byType(StageStrip));
    expect(strip.current, 2);
    expect(strip.labels, ['Plan', 'Implement', 'Review', 'Ready']);
    expect(strip.status, Status.running);
    expect(strip.endWord, isNull);
  });

  testWidgets('end state is a word, not a stage', (tester) async {
    await pump(tester, item(state: 'stopped', stage: 'ready'));
    final strip = tester.widget<StageStrip>(find.byType(StageStrip));
    // The stage the item reached is still the one it is drawn at.
    expect(strip.current, 3);
    // Stopping, stopped and closed are words beside the strip.
    expect(strip.endWord, 'Stopped');
    expect(find.text('Stopped'), findsWidgets);
  });

  testWidgets('activity names who acted', (tester) async {
    await pump(tester, item());
    expect(find.text('You'), findsWidgets);
    expect(find.text('The implementer'), findsWidgets);
    expect(find.text('the implementer started'), findsOneWidget);
    expect(find.text('the reviewer answered'), findsOneWidget);
  });

  testWidgets('the Plan tab reads the approved plan and its settings', (
    tester,
  ) async {
    await pump(tester, item());
    await openTab(tester, 'Plan');
    expect(find.textContaining('Task 1'), findsWidgets);
    expect(find.text('Target branch'), findsOneWidget);
    expect(find.text('main'), findsOneWidget);
  });

  testWidgets('the Agents tab lists each worker by role', (tester) async {
    await pump(tester, item());
    await openTab(tester, 'Agents');
    expect(find.text('agent-imp'), findsOneWidget);
    // The word is the one the item's own effect supports, not "Working" by
    // default.
    expect(find.text('Finished'), findsOneWidget);
    expect(find.text('Working'), findsNothing);
  });

  testWidgets('the Checks tab shows the observed checks and the review', (
    tester,
  ) async {
    await pump(tester, item(rounds: [round()]));
    await openTab(tester, 'Checks');
    expect(find.text('test'), findsOneWidget);
    expect(find.text('build'), findsOneWidget);
    // A finished failure is the kit's failed state, never a pending mark.
    final strip = tester.widget<CheckStrip>(find.byType(CheckStrip));
    expect(strip.checks.map((check) => check.state), [
      CheckState.passed,
      CheckState.failed,
    ]);
    expect(
      find.text('Name the stage in words as well as the strip'),
      findsOneWidget,
    );
  });

  testWidgets('the Changes tab says the change is not being read', (
    tester,
  ) async {
    await pump(tester, item());
    await openTab(tester, 'Changes');
    // The item is unavailable here, but with no read to retry the tab draws
    // no control: a button that would do nothing is not drawn (§7).
    expect(
      find.text('Read the item again'),
      findsNothing,
      reason: 'no retry handler means no retry button',
    );
    expect(
      tester.widget<StateMessage>(find.byType(StateMessage)).kind,
      StateKind.unavailable,
    );
  });

  testWidgets('the Changes tab offers the retry its host gave it', (
    tester,
  ) async {
    var retried = 0;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item()),
        now: now,
        pulls: const [],
        onRetry: () => retried++,
      ),
      width: 1000,
      height: 2000,
    );
    await openTab(tester, 'Changes');
    await tester.tap(find.text('Read the item again'));
    await tester.pump();
    expect(retried, 1);
  });

  testWidgets('the Pull requests tab says there is none yet', (tester) async {
    await pump(tester, item());
    await openTab(tester, 'Pull requests');
    expect(
      find.text(
        'No pull request yet: one appears once the work reaches a push.',
      ),
      findsOneWidget,
    );
  });

  testWidgets('the inspector offers a work item its five tabs', (tester) async {
    await pump(tester, item());
    final tabs = tester.widget<Tabs>(find.byType(Tabs));
    expect(tabs.tabs.map((tab) => tab.label), [
      'Plan',
      'Agents',
      'Checks',
      'Changes',
      'Pull requests',
    ]);
    // The tab set is the work item's, not the agent's (F6 decision 6).
    expect(
      tabs.tabs.any((tab) => tab.label == 'Files'),
      isFalse,
      reason: 'a work item has no file browser: its change is the Changes tab',
    );
  });

  testWidgets('the Plan tab and its revision entry come before the activity', (
    tester,
  ) async {
    await show(tester, item(), onProposeChange: () {}, height: 2400);

    // The label is drawn in the kit's uppercase, so it is found as the label
    // it is rather than by its text.
    final activity = find.byWidgetPredicate(
      (widget) => widget is Label && widget.text == 'Activity',
    );
    expect(activity, findsOneWidget);

    final planTab = find.descendant(
      of: find.byType(Tabs),
      matching: find.text('Plan'),
    );
    expect(planTab, findsOneWidget);
    expect(
      tester.getTopLeft(planTab).dy,
      lessThan(tester.getTopLeft(activity).dy),
      reason: 'what the item is comes before what happened to it',
    );

    await openTab(tester, 'Plan');
    final revise = find.text('Propose a change');
    expect(revise, findsOneWidget);
    expect(
      tester.getTopLeft(revise).dy,
      lessThan(tester.getTopLeft(activity).dy),
      reason: 'revising the plan is not buried below the timeline',
    );
  });

  testWidgets('a typed answer survives polling and a worker drill-in', (
    tester,
  ) async {
    var answers = 0;
    await show(
      tester,
      item(pending: decision()),
      onAnswer: (_) async => answers++,
    );
    await tester.enterText(find.byType(EditableText), 'the rounds');
    await tester.pump();
    expect(answerText(tester), 'the rounds');

    // The store polls: the item moves to a new version and the body rebuilds
    // under the field.
    await show(
      tester,
      item(version: 2, pending: decision()),
      onAnswer: (_) async => answers++,
    );
    await tester.pump();
    expect(answerText(tester), 'the rounds', reason: 'a poll keeps the draft');

    // A worker's own conversation replaces the body, then goes back.
    await show(
      tester,
      item(version: 2, pending: decision()),
      center: const Text('worker'),
      onAnswer: (_) async => answers++,
    );
    await tester.pump();
    expect(find.text('worker'), findsOneWidget);
    expect(find.byType(EditableText), findsNothing);

    await show(
      tester,
      item(version: 2, pending: decision()),
      onAnswer: (_) async => answers++,
    );
    await tester.pump();
    expect(answerText(tester), 'the rounds', reason: 'the drill-in keeps it');

    expect(answers, 0, reason: 'nothing was decided for the person');
  });

  testWidgets('a decision still posts the person\u2019s exact words', (
    tester,
  ) async {
    final posted = <String>[];
    await show(
      tester,
      item(pending: decision()),
      onAnswer: (answer) async => posted.add(answer),
    );
    expect(posted, isEmpty, reason: 'nothing is answered for the person');

    await tester.enterText(find.byType(EditableText), 'the rounds');
    await tester.pump();
    expect(posted, isEmpty, reason: 'typing is not taking the action');

    await tester.tap(find.text('Answer'));
    await tester.pump();
    expect(posted, ['the rounds'], reason: 'the words are sent as written');
  });
}
