import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart';
import 'package:marshal_app/src/model/work_item_model.dart';

import '../../model/support.dart';
import '../../support/fake_bridge.dart';

/// F6 plan 1's end-to-end flow: a draft is approved, raises a decision that is
/// answered, repairs once and reaches ready — driven through the store the
/// thread reads, with every step asserted on the view the thread draws.
///
/// It is a model-level end-to-end rather than `integration_test`: this machine
/// has no Linux toolchain for a device app (recorded in
/// `docs/studio-flutter/f6-1-work-items.md`), and a widget test cannot host
/// this flow — a command posted from a tapped callback runs in the test's
/// fake-async zone, where the fixture bridge's real HTTP never completes. The
/// thread's drawing and its actions are covered by `work_item_test.dart`,
/// `item_asks_test.dart` and `work_item_drill_in_test.dart`; this file covers
/// the flow itself.
void main() {
  FactoryDecision decision() => FactoryDecision.fromJson({
    'id': 'decision-1',
    'kind': 'scope',
    'question': 'Which inspection does the inspector show?',
    'reason': 'the plan names both',
    'effectId': 'effect-1',
    'createdAt': '2026-11-17T11:30:00Z',
  });

  FactoryCompletion complete() => FactoryCompletion.fromJson({
    'status': 'complete',
    'links': [],
    'records': [],
  });

  FactoryItem item({
    required String state,
    required int version,
    FactoryDecision? pending,
    FactoryCompletion? completion,
  }) => factoryItem(
    id: 'item-1',
    state: state,
    version: version,
    decision: pending,
    completion: completion,
    workers: [factoryWorker(agentId: 'agent-imp', sessionId: 'sess-imp')],
    history: [
      factoryEvent(1, 'created', 'the draft was created'),
      factoryEvent(2, 'approved', 'revision 1 was approved'),
    ],
  );

  test('a work item goes from draft to ready in its thread', () async {
    final draft = item(state: 'draft', version: 1);
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        agents: [agent(id: 'agent-imp')],
        projects: [project('/home/u/alpha')],
        factoryItems: [draft],
      ),
    ]);
    final store = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(store.dispose);
    await store.refresh('local');

    final bridge = fixture.bridges['local']!;
    void answersWith(FactoryItem next) => bridge.reply(
      'POST',
      '/api/factory/items/item-1/commands',
      FakeReply(
        200,
        headers: const {'content-type': 'application/json'},
        body: jsonEncode({'item': next.toJson()}),
      ),
    );

    WorkItemView view() =>
        WorkItemView.of('local', store.byId('local', 'item-1')!);
    List<AskKind> asks() => [for (final ask in view().asks) ask.kind];
    ThreadRef thread() => threadOfItem('local', store.byId('local', 'item-1')!);

    // A draft: it is on the Plan stage and waits for its approval.
    expect(view().stage, Stage.plan);
    expect(asks(), [AskKind.approvalDraft]);
    expect(thread().status, ThreadStatus.needsYou);

    // Approved: the work starts, and it stops on a decision.
    answersWith(item(state: 'implementing', version: 2, pending: decision()));
    await store.approve('local', 'item-1');
    expect(view().stage, Stage.implement);
    expect(view().decisionID, 'decision-1');
    expect(asks(), [AskKind.decision]);
    expect(view().status, ThreadStatus.needsYou);

    // Answered: it goes into one repair, and the decision is gone.
    answersWith(item(state: 'repairing', version: 3));
    await store.answerDecision(
      'local',
      'item-1',
      decisionID: view().decisionID,
      answer: 'the rounds',
    );
    expect(view().stage, Stage.implement);
    expect(asks(), isEmpty);
    final last = jsonDecode(
      fixture
          .requests('local')
          .lastWhere((request) => request.method == 'POST')
          .body,
    ) as Map<String, dynamic>;
    expect(last['kind'], 'answer');
    expect(last['decisionId'], 'decision-1');
    expect(last['answer'], 'the rounds');

    // Repaired: it is ready, and finishing it is a person's decision in the
    // same thread.
    fixture.serve('local', '/api/factory/items', {
      'items': [item(state: 'ready', version: 4).toJson()],
    });
    await store.refresh('local');
    expect(view().stage, Stage.ready);
    expect(asks(), [AskKind.completion]);
    expect(view().status, ThreadStatus.ready);

    // Completed: the item is done, and its thread says so rather than asking.
    answersWith(item(state: 'closed', version: 5, completion: complete()));
    await store.complete('local', 'item-1');
    expect(view().endWord, 'Closed');
    expect(asks(), isEmpty);
    expect(view().status, ThreadStatus.idle);

    // One item, one thread, and no other page was needed.
    expect(store.items(host: 'local').map((each) => each.id), ['item-1']);
  });
}
