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

  FactoryItem item({
    String state = 'reviewing',
    String stage = '',
    List<FactoryRound> rounds = const [],
  }) => factoryItem(
    state: state,
    stage: stage,
    rounds: rounds,
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
    expect(find.text('Working'), findsOneWidget);
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
    expect(find.text('Read the item again'), findsOneWidget);
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
}
