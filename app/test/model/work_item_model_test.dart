import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart';
import 'package:marshal_app/src/model/work_item_model.dart';

import 'support.dart';

void main() {
  test('stage and status for every state', () {
    // state, the stage it is drawn on, the dot, and the end word beside the
    // strip. An interrupting state keeps the stage the bridge recorded.
    const table = <String, (Stage, ThreadStatus, String?)>{
      'draft': (Stage.plan, ThreadStatus.needsYou, null),
      'queued': (Stage.plan, ThreadStatus.running, null),
      'preparing': (Stage.plan, ThreadStatus.running, null),
      'implementing': (Stage.implement, ThreadStatus.running, null),
      'repairing': (Stage.implement, ThreadStatus.running, null),
      'reviewing': (Stage.review, ThreadStatus.running, null),
      'checking': (Stage.review, ThreadStatus.running, null),
      'needs_input': (Stage.implement, ThreadStatus.needsYou, null),
      'recovering': (Stage.implement, ThreadStatus.running, null),
      'ready': (Stage.ready, ThreadStatus.ready, null),
      'stopping': (Stage.plan, ThreadStatus.running, 'Stopping'),
      'stopped': (Stage.plan, ThreadStatus.idle, 'Stopped'),
      'closed': (Stage.plan, ThreadStatus.idle, 'Closed'),
    };
    for (final MapEntry(key: state, value: want) in table.entries) {
      final item = factoryItem(state: state);
      expect(stageOf(item), want.$1, reason: state);
      expect(statusOfItem(item), want.$2, reason: state);
      expect(endStateWord(item), want.$3, reason: state);
    }
  });

  test('a state this build does not know is shown under Plan', () {
    final item = factoryItem(state: 'quarantined');
    expect(stageOf(item), Stage.plan);
    // It is not hidden: an item nothing lists is work nobody can find.
    expect(filterItems([item], ItemFilter.current), [item]);
    expect(endStateWord(item), isNull);
  });

  test('an interrupting state keeps the stage the bridge recorded', () {
    expect(
      stageOf(factoryItem(state: 'needs_input', stage: 'review')),
      Stage.review,
    );
    expect(
      stageOf(factoryItem(state: 'recovering', stage: 'plan')),
      Stage.plan,
    );
    // Without a recorded stage, an item with a round was implementing.
    final round = FactoryRound.fromJson({
      'number': 1,
      'approvalRevision': 1,
      'implementEffectId': 'effect-1',
      'baseSha': 'base-1',
      'headSha': 'head-1',
      'treeSha': 'tree-1',
      'ci': null,
      'implementation': null,
      'review': null,
      'startedAt': '2026-11-17T09:10:00Z',
      'finishedAt': '0001-01-01T00:00:00Z',
    });
    expect(
      stageOf(factoryItem(state: 'needs_input', rounds: [round])),
      Stage.implement,
    );
  });

  test('the filters keep the list they name', () {
    final decision = FactoryDecision.fromJson({
      'id': 'decision-1',
      'kind': 'scope',
      'question': 'Which inspection?',
      'reason': 'the plan names two',
      'effectId': 'effect-1',
      'createdAt': '2026-11-17T09:40:00Z',
    });
    final items = [
      factoryItem(id: 'a', state: 'draft'),
      factoryItem(id: 'b', state: 'implementing'),
      factoryItem(id: 'c', state: 'ready'),
      factoryItem(id: 'd', state: 'closed'),
      factoryItem(id: 'e', state: 'needs_input'),
      factoryItem(id: 'f', state: 'implementing', decision: decision),
    ];
    List<String> ids(ItemFilter filter) => [
      for (final item in filterItems(items, filter)) item.id,
    ];
    expect(ids(ItemFilter.current), ['a', 'e', 'f', 'c', 'b']);
    expect(ids(ItemFilter.needsYou), ['a', 'e', 'f']);
    expect(ids(ItemFilter.ready), ['c']);
    expect(ids(ItemFilter.history), ['d']);
  });

  test('a worker is named in words in the item\u2019s activity', () {
    final item = factoryItem(
      effects: [
        FactoryEffect.fromJson({
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
          'createdAt': '2026-11-17T09:10:00Z',
        }),
      ],
      history: [
        factoryEvent(1, 'created', 'the draft was created'),
        factoryEvent(
          2,
          'implement_started',
          'the implementer started',
          effectId: 'effect-implementer',
        ),
        factoryEvent(3, 'approved', 'revision 1 was approved'),
      ],
    );
    final activity = activityOf(item);
    expect(activity.map((entry) => entry.who), [
      'You',
      'The implementer',
      'You',
    ]);
    expect(activity[1].text, 'the implementer started');
    expect(activity[1].detail, 'implement_started');
  });

  /// One effect as a worker-state test writes one: only the fields the state
  /// is read from are varied.
  FactoryEffect effect({
    String id = 'effect-implementer',
    String status = 'dispatched',
    Map<String, Object?>? outcome,
  }) => FactoryEffect.fromJson({
    'id': id,
    'kind': 'implement',
    'status': status,
    'workerRole': 'implementer',
    'round': 1,
    'approvalRevision': 1,
    'worker': null,
    'expectedHead': 'head-1',
    'expectedBase': 'base-1',
    'createdAt': '2026-11-17T09:10:00Z',
    'outcome': ?outcome,
  });

  Map<String, Object?> outcome({String error = '', String outcome = ''}) => {
    'effectId': 'effect-implementer',
    'stopReason': 'completed',
    'error': error,
    'headSha': 'head-1',
    'treeSha': 'tree-1',
    'baseSha': 'base-1',
    'worker': null,
    'pr': null,
    'report': outcome.isEmpty
        ? null
        : {
            'operationId': 'effect-implementer',
            'outcome': outcome,
            'summary': '',
            'question': '',
            'reason': '',
            'baseSha': 'base-1',
            'headSha': 'head-1',
            'inputsAccessible': true,
            'findings': <Object?>[],
          },
    'confirmed': true,
  };

  WorkerState stateOf(String status, {Map<String, Object?>? result}) {
    final worker = factoryWorker();
    return workerStateOf(
      factoryItem(
        effects: [
          effect(id: worker.spawnEffectID, status: status, outcome: result),
        ],
      ),
      worker,
    );
  }

  test('a worker\u2019s state is what its own effect supports', () {
    // Parked wins over any effect: the worker is not running.
    final parked = factoryWorker(parked: true);
    expect(
      workerStateOf(
        factoryItem(effects: [effect(id: parked.spawnEffectID)]),
        parked,
      ),
      WorkerState.parked,
    );
    expect(stateOf('dispatched'), WorkerState.working);
    expect(stateOf('completed'), WorkerState.finished);
    // The fixture's own word for a finished effect.
    expect(stateOf('done'), WorkerState.finished);
    // Failure is the outcome's, not a status this build invents.
    expect(
      stateOf('completed', result: outcome(error: 'the worker exited 1')),
      WorkerState.failed,
    );
    expect(
      stateOf('completed', result: outcome(outcome: 'failed')),
      WorkerState.failed,
    );
    // Prepared and uncertain are not proof of anything: unknown, never
    // working.
    expect(stateOf('prepared'), WorkerState.unknown);
    expect(stateOf('uncertain'), WorkerState.unknown);
  });

  test('a worker with no effect of its own is unknown', () {
    final worker = factoryWorker(spawnEffectId: 'effect-missing');
    expect(
      workerStateOf(
        factoryItem(effects: [effect(id: 'effect-implementer')]),
        worker,
      ),
      WorkerState.unknown,
    );
    expect(workerStateOf(factoryItem(), worker), WorkerState.unknown);
  });

  test('an unreadable time is unknown, not an epoch', () {
    List<ActivityEntry> activity(String at) => activityOf(
      factoryItem(history: [factoryEvent(1, 'created', 'created', at: at)]),
    );
    expect(
      activity('2026-11-17T09:30:00Z').single.at,
      DateTime.utc(2026, 11, 17, 9, 30),
    );
    expect(activity('').single.at, isNull);
    expect(activity('not-a-time').single.at, isNull);
  });

  test('an exact time names itself in UTC', () {
    expect(
      exactTimeLabel(DateTime.utc(2026, 11, 17, 9, 30)),
      '2026-11-17 09:30 UTC',
    );
  });

  test('one ask per pending draft, decision, recovery, completion', () {
    final decision = FactoryDecision.fromJson({
      'id': 'decision-1',
      'kind': 'scope',
      'question': 'Which inspection?',
      'reason': 'the plan names two',
      'effectId': 'effect-1',
      'createdAt': '2026-11-17T09:40:00Z',
    });
    List<AskKind> kinds(FactoryItem item) => [
      for (final ask in asksOf('local', item)) ask.kind,
    ];
    expect(kinds(factoryItem(state: 'draft')), [AskKind.approvalDraft]);
    expect(kinds(factoryItem(state: 'draft', decision: decision)), [
      AskKind.approvalDraft,
      AskKind.decision,
    ]);
    expect(kinds(factoryItem(state: 'implementing', decision: decision)), [
      AskKind.decision,
    ]);
    expect(kinds(factoryItem(state: 'needs_input')), [AskKind.recovery]);
    expect(kinds(factoryItem(state: 'ready')), [AskKind.completion]);
    expect(kinds(factoryItem(state: 'implementing')), isEmpty);
  });

  test('a finished item asks for nothing', () {
    final item = factoryItem(
      state: 'closed',
      completion: FactoryCompletion.fromJson({
        'status': 'complete',
        'links': [],
        'records': [],
      }),
    );
    expect(asksOf('local', item), isEmpty);
  });
}
