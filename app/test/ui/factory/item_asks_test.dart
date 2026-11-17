import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/merge_target.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/revision_model.dart';
import 'package:marshal_app/src/model/work_item_model.dart';
import 'package:marshal_app/src/ui/factory/item_asks.dart';
import 'package:marshal_app/src/ui/factory/pulls_tab.dart';
import 'package:marshal_app/src/ui/factory/recovery_form.dart';
import 'package:marshal_app/src/ui/factory/revision_sheet.dart';
import 'package:marshal_app/src/ui/factory/work_item_screen.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';
import '../../support/kit.dart';
import '../../support/pulls.dart';

/// A work item's asks and commands (F6 decision 5): each ask posts its own
/// kind, a recovery carries its routing and rounds, stop confirms, and a
/// conflict leaves the action available. The review and completion behaviour
/// it must keep is covered at the end: every linked pull request, a check that
/// cannot be read, the states a completion is offered in, and the revision
/// sheet's unsent text.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  FactoryDecision decision() => FactoryDecision.fromJson({
    'id': 'decision-1',
    'kind': 'scope',
    'question': 'Which inspection does the inspector show?',
    'reason': 'the plan names both',
    'effectId': 'effect-1',
    'createdAt': '2026-11-17T11:00:00Z',
  });

  /// A completion the factory judged and found wanting, as it reports one.
  FactoryCompletion failedCompletion() => FactoryCompletion.fromJson({
    'status': 'failed',
    'links': [],
    'records': [],
    'stopState': 'failed',
  });

  /// A completion that already finished the work.
  FactoryCompletion completeCompletion() => FactoryCompletion.fromJson({
    'status': 'complete',
    'links': [],
    'records': [],
  });

  FactoryItem item({
    String state = 'implementing',
    FactoryDecision? pending,
    FactoryCompletion? completion,
    List<FactoryPR> prs = const [],
    List<FactoryWorker> workers = const [],
  }) => factoryItem(
    state: state,
    decision: pending,
    completion: completion,
    prs: prs,
    workers: workers,
    history: [factoryEvent(1, 'created', 'the draft was created')],
  );

  /// The fixture: one host whose item answers every command with itself.
  Future<({ModelFixture fixture, FactoryStore store})> start(
    FactoryItem subject,
  ) async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        factoryItems: [subject],
      ),
    ]);
    final store = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(store.dispose);
    await store.refresh('local');
    fixture.bridges['local']!.reply(
      'POST',
      '/api/factory/items/${subject.id}/commands',
      FakeReply(
        200,
        headers: const {'content-type': 'application/json'},
        body: jsonEncode({'item': subject.toJson()}),
      ),
    );
    return (fixture: fixture, store: store);
  }

  /// The body of the last command [host]'s bridge received.
  Map<String, dynamic> lastCommand(ModelFixture fixture) {
    final post = fixture
        .requests('local')
        .lastWhere((request) => request.method == 'POST');
    return jsonDecode(post.body) as Map<String, dynamic>;
  }

  test('each ask posts its own command kind and fields', () async {
    final subject = item(prs: [factoryPR()], workers: [factoryWorker()]);
    final started = await start(subject);
    final store = started.store;

    await store.approve('local', subject.id);
    expect(lastCommand(started.fixture)['kind'], 'approve');

    await store.answerDecision(
      'local',
      subject.id,
      decisionID: 'decision-1',
      answer: 'the rounds',
    );
    var body = lastCommand(started.fixture);
    expect(body['kind'], 'answer');
    expect(body['decisionId'], 'decision-1');
    expect(body['answer'], 'the rounds');

    await store.complete('local', subject.id);
    expect(lastCommand(started.fixture)['kind'], 'complete');

    await store.reopen('local', subject.id);
    expect(lastCommand(started.fixture)['kind'], 'reopen');

    await store.stopItem('local', subject.id);
    expect(lastCommand(started.fixture)['kind'], 'stop');

    await store.mergeAndComplete('local', subject.id);
    body = lastCommand(started.fixture);
    expect(body['kind'], 'merge_and_complete');
    final completion = body['completion'] as Map<String, dynamic>;
    final merged = (completion['prs'] as List).single as Map<String, dynamic>;
    expect(
      merged['nodeId'],
      'node-412',
      reason: 'the merge names the reviewed pull request\u2019s real node id',
    );
    expect(merged['headSha'], 'head-412');
    expect(merged['baseRef'], 'main');
    expect(merged['repoId'], 'repo-1');
    expect(completion['completeAfter'], isTrue);
    expect(
      completion['runChoice'],
      'continue',
      reason: 'the item\u2019s run is active, so the bridge demands a choice',
    );
  });

  test('a merge with no reviewed pull request posts nothing', () async {
    final subject = item(state: 'ready');
    final started = await start(subject);

    expect(
      () => started.store.mergeAndComplete('local', subject.id),
      throwsA(isA<StateError>()),
      reason: 'the item holds no reviewed identity to merge',
    );
    expect(
      started.fixture.requests('local').where((r) => r.method == 'POST'),
      isEmpty,
      reason: 'a merge the bridge would refuse is never sent',
    );
  });

  test('recovery sends routing and rounds', () async {
    final subject = item(state: 'needs_input');
    final started = await start(subject);

    await started.store.recover(
      'local',
      subject.id,
      model: 'deep',
      fresh: true,
      extraRounds: 2,
    );

    final body = lastCommand(started.fixture);
    expect(body['kind'], 'recover');
    expect(body['reason'], 'fresh');
    expect(body['additionalRounds'], 2);
    expect(body['implementerRouting'], {'model': 'deep'});
  });

  test('a recovery with no model leaves the routing alone', () async {
    final subject = item(state: 'needs_input');
    final started = await start(subject);

    await started.store.recover('local', subject.id, extraRounds: 1);

    final body = lastCommand(started.fixture);
    expect(body['reason'], 'continue');
    expect(body['additionalRounds'], 1);
    expect(body['implementerRouting'], isNull);
  });

  testWidgets('a draft approval answers through the item', (tester) async {
    var approved = 0;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item(state: 'draft')),
        now: now,
        onApprove: () async => approved++,
      ),
      width: 1000,
      height: 2000,
    );
    await tester.tap(find.text('Approve and start'));
    await tester.pump();
    expect(approved, 1);
  });

  testWidgets('a decision takes the person\u2019s answer', (tester) async {
    String? answered;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item(pending: decision())),
        now: now,
        onAnswer: (answer) async => answered = answer,
      ),
      width: 1000,
      height: 2000,
    );
    await tester.enterText(find.byType(EditableText), 'the rounds');
    await tester.pump();
    await tester.tap(find.text('Answer'));
    await tester.pump();
    expect(answered, 'the rounds');
  });

  testWidgets('a decision\u2019s answer is disabled until there are words', (
    tester,
  ) async {
    String? answered;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item(pending: decision())),
        now: now,
        onAnswer: (answer) async => answered = answer,
      ),
      width: 1000,
      height: 2000,
    );

    // An empty answer is not worth a round trip, so the control says so
    // instead of taking a tap that would post nothing.
    final answer = tester.widget<Btn>(find.widgetWithText(Btn, 'Answer'));
    expect(answer.onTap, isNull, reason: 'drawn disabled with nothing to send');

    await tester.tap(find.text('Answer'));
    await tester.pump();
    expect(answered, isNull);

    await tester.enterText(find.byType(EditableText), 'the rounds');
    await tester.pump();
    await tester.tap(find.text('Answer'));
    await tester.pump();
    expect(answered, 'the rounds');
  });

  testWidgets('a recovery submits its choice', (tester) async {
    RecoveryChoice? chosen;
    await pumpKit(
      tester,
      ItemAsks(
        item: WorkItemView.of('local', item(state: 'needs_input')),
        now: now,
        onRecover: (choice) async => chosen = choice,
      ),
      width: 900,
      height: 1600,
    );
    await tester.enterText(find.byType(EditableText).first, 'deep');
    await tester.tap(find.text('Start fresh').last);
    await tester.pump();
    await tester.tap(find.text('Start fresh').last);
    await tester.pump();
    expect(chosen, isNotNull);
    expect(chosen!.model, 'deep');
    expect(chosen!.fresh, isTrue);
  });

  testWidgets('a completion offers complete, merge and reopen', (tester) async {
    final calls = <String>[];
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of(
          'local',
          item(state: 'ready', prs: [factoryPR()]),
        ),
        now: now,
        onComplete: () async => calls.add('complete'),
        onMergeAndComplete: () async => calls.add('merge'),
        onReopen: () async => calls.add('reopen'),
      ),
      width: 1000,
      height: 2000,
    );
    expect(find.text('Complete'), findsOneWidget);
    expect(find.text('Merge and complete'), findsOneWidget);
    expect(find.text('Reopen'), findsOneWidget);
    await tester.tap(find.text('Merge and complete'));
    await tester.pump();
    expect(calls, ['merge']);
  });

  testWidgets('a merge with no reviewed pull request is disabled, with the '
      'reason, and takes no action', (tester) async {
    var merges = 0;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item(state: 'ready')),
        now: now,
        onComplete: () async {},
        onMergeAndComplete: () async => merges++,
        onReopen: () async {},
      ),
      width: 1000,
      height: 2000,
    );

    final merge = tester.widget<Btn>(
      find.widgetWithText(Btn, 'Merge and complete'),
    );
    expect(merge.onTap, isNull, reason: 'drawn disabled, not offered');
    expect(
      hasTip(
        tester,
        find.widgetWithText(Btn, 'Merge and complete'),
        noMergeTargetNote,
      ),
      isTrue,
      reason: 'a disabled merge says why it cannot be sent',
    );

    await tester.tap(find.text('Merge and complete'));
    await tester.pump();
    expect(merges, 0, reason: 'no command the bridge would refuse is sent');
  });

  testWidgets('stop confirms before it stops', (tester) async {
    var stopped = 0;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item()),
        now: now,
        onStop: () async => stopped++,
      ),
      width: 1000,
      height: 2000,
    );

    await tester.tap(find.byTooltip('More actions'));
    await tester.pump();
    await tester.pump();
    await tester.tap(find.text('Stop the work'));
    await tester.pump();
    await tester.pump();
    // The sheet asks first, and says what is lost.
    expect(find.text('Stop the work?'), findsOneWidget);
    expect(stopped, 0);

    await tester.tap(find.widgetWithText(Btn, 'Stop the work'));
    await tester.pump();
    await tester.pump();
    expect(stopped, 1);
  });

  testWidgets('a conflict keeps the action available', (tester) async {
    var approved = 0;
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', item(state: 'draft')),
        now: now,
        conflict: FactoryStore.conflictWords,
        onApprove: () async => approved++,
      ),
      width: 1000,
      height: 2000,
    );
    expect(find.text(FactoryStore.conflictWords), findsOneWidget);
    await tester.tap(find.text('Approve and start'));
    await tester.pump();
    expect(approved, 1, reason: 'the action is still there to take');
  });

  /// The thread of [subject] — the fixture item unless one is named — with
  /// [pulls], its Pull requests tab open.
  Future<void> openPulls(
    WidgetTester tester, {
    FactoryItem? subject,
    List<PullView> pulls = const [],
  }) async {
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of('local', subject ?? item()),
        now: now,
        pulls: pulls,
      ),
      width: 1000,
      height: 2200,
    );
    await tester.tap(
      find.descendant(
        of: find.byType(Tabs),
        matching: find.text('Pull requests'),
      ),
    );
    await tester.pump();
  }

  /// A host with one control that opens the revision sheet, so the sheet is a
  /// route above it rather than the root: its dismissal is a real pop.
  Widget revisionHost(RevisionModel model) => Builder(
    builder: (context) =>
        Btn('Open', onTap: () => showRevisionSheet(context, model)),
  );

  testWidgets('every linked pull request is listed', (tester) async {
    await openPulls(
      tester,
      pulls: [
        pullView(number: 412, factoryItem: 'item-1'),
        pullView(number: 413, factoryItem: 'item-1'),
      ],
    );

    // The first is summarised on the thread as well as listed in its tab;
    // the second is not dropped.
    expect(find.text('#412'), findsWidgets);
    expect(find.text('#413'), findsOneWidget);
    expect(
      find.descendant(of: find.byType(PullsTab), matching: find.byType(PrCard)),
      findsNWidgets(2),
      reason: 'the tab lists every linked pull request the item holds',
    );
  });

  testWidgets('checks that cannot be read are never drawn as passing', (
    tester,
  ) async {
    await openPulls(
      tester,
      pulls: [pullView(group: PullGroup.unavailable, factoryItem: 'item-1')],
    );

    final card = tester.widget<PrCard>(find.byType(PrCard).last);
    expect(card.pull.state, PrState.unavailable);
    expect(
      card.pull.checkSummary,
      contains('Checks unavailable'),
      reason: 'an unreadable check is its own state, never a pass',
    );
  });

  testWidgets('a stopped item with a failed completion offers no completion '
      'action', (tester) async {
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of(
          'local',
          item(state: 'stopped', completion: failedCompletion()),
        ),
        now: now,
      ),
      width: 1000,
      height: 2200,
    );

    expect(find.text('Complete'), findsNothing);
    expect(find.text('Merge and complete'), findsNothing);
    expect(find.text('Reopen'), findsNothing);
  });

  testWidgets('a failed completion stays available on a ready item', (
    tester,
  ) async {
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of(
          'local',
          item(state: 'ready', completion: failedCompletion()),
        ),
        now: now,
      ),
      width: 1000,
      height: 2200,
    );

    expect(find.text('Complete'), findsOneWidget);
    expect(find.text('Merge and complete'), findsOneWidget);
  });

  testWidgets('a completed item does not ask again', (tester) async {
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of(
          'local',
          item(state: 'ready', completion: completeCompletion()),
        ),
        now: now,
      ),
      width: 1000,
      height: 2200,
    );

    expect(find.text('Complete'), findsNothing);
    expect(find.text('Merge and complete'), findsNothing);
  });

  testWidgets('a merged pull request does not complete the item by itself', (
    tester,
  ) async {
    await pumpKit(
      tester,
      WorkItemScreen(
        item: WorkItemView.of(
          'local',
          item(
            state: 'ready',
            prs: [factoryPR(state: 'merged')],
          ),
        ),
        now: now,
      ),
      width: 1000,
      height: 2200,
    );

    // Nothing is inferred from the forge's state: the person still decides.
    expect(find.text('Complete'), findsOneWidget);
    expect(
      find.text('Merge and complete'),
      findsOneWidget,
      reason: 'a merged pull request is not a completion the app infers',
    );
  });

  testWidgets('leaving an unsent proposal asks first, in the sheet', (
    tester,
  ) async {
    final model = RevisionModel(
      approved: '## Task 1\nRead the item.\n',
      spec: 'Ship.',
      baseRevision: 1,
      baseHash: 'hash-1',
      submit: (plan) async {},
    );
    addTearDown(model.dispose);
    await pumpKit(tester, revisionHost(model), width: 900, height: 1600);

    await tester.tap(find.text('Open'));
    await tester.pumpAndSettle();

    const typed = '## Task 1\nRead the item first.\n';
    await tester.enterText(find.byType(EditableText), typed);
    await tester.pump();

    // Cancel is a way out, and it asks before the text is lost.
    await tester.tap(find.text('Cancel'));
    await tester.pump();
    expect(
      find.textContaining('Your proposal has not been sent.'),
      findsOneWidget,
    );
    expect(
      find.byType(EditableText),
      findsNothing,
      reason: 'the question replaces the editor, in the same sheet',
    );

    await tester.tap(find.text('Keep writing'));
    await tester.pump();
    expect(
      tester.widget<EditableText>(find.byType(EditableText)).controller.text,
      typed,
      reason: 'the question costs nothing that was typed',
    );

    await tester.tap(find.text('Cancel'));
    await tester.pump();
    await tester.tap(find.text('Discard the proposal'));
    await tester.pumpAndSettle();
    expect(find.byType(RevisionSheet), findsNothing);
    expect(find.text('Open'), findsOneWidget);
  });

  testWidgets('a sending proposal cannot be dismissed', (tester) async {
    final gate = Completer<void>();
    final model = RevisionModel(
      approved: '## Task 1\nRead the item.\n',
      spec: 'Ship.',
      baseRevision: 1,
      baseHash: 'hash-1',
      submit: (plan) => gate.future,
    );
    addTearDown(model.dispose);
    await pumpKit(tester, revisionHost(model), width: 900, height: 1600);

    await tester.tap(find.text('Open'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byType(EditableText),
      '## Task 1\nRead the item first.\n',
    );
    await tester.pump();
    await tester.tap(find.text('Send the proposal'));
    await tester.pump();
    expect(find.text('Sending…'), findsOneWidget);

    // A send in flight is not a loss, and it is not interrupted.
    await tester.tap(find.byTooltip('Close'));
    await tester.pump();
    expect(find.byType(RevisionSheet), findsOneWidget);

    gate.complete();
    await tester.pumpAndSettle();
    expect(find.byType(RevisionSheet), findsNothing);
  });
}
