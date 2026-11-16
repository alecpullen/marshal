import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/work_item_model.dart';
import 'package:marshal_app/src/ui/factory/item_asks.dart';
import 'package:marshal_app/src/ui/factory/recovery_form.dart';
import 'package:marshal_app/src/ui/factory/work_item_screen.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';
import '../../support/kit.dart';

/// A work item's asks and commands (F6 decision 5): each ask posts its own
/// kind, a recovery carries its routing and rounds, stop confirms, and a
/// conflict leaves the action available.
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

  FactoryItem item({String state = 'implementing', FactoryDecision? pending}) =>
      factoryItem(
        state: state,
        decision: pending,
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
    final subject = item();
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

    await store.mergeAndComplete(
      'local',
      subject.id,
      repoID: 'repo-1',
      number: 412,
      headSHA: 'head-412',
      baseRef: 'main',
    );
    body = lastCommand(started.fixture);
    expect(body['kind'], 'merge_and_complete');
    final prs = (body['completion'] as Map<String, dynamic>)['prs'] as List;
    expect((prs.single as Map<String, dynamic>)['headSha'], 'head-412');
    expect(
      (body['completion'] as Map<String, dynamic>)['completeAfter'],
      isTrue,
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
        item: WorkItemView.of('local', item(state: 'ready')),
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
}
