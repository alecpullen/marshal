import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/marshal_model.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

/// One coordinated task, as each fixture names it.
CoordinatorTask task(String id, {String status = 'open', String title = ''}) =>
    CoordinatorTask(
      id: id,
      sessionID: 's-$id',
      title: title.isEmpty ? null : title,
      status: CoordinatorTaskStatus.fromWire(status),
      revision: 1,
      projects: const [],
      createdAt: '2026-11-21T10:00:00Z',
      updatedAt: '2026-11-21T10:00:00Z',
    );

/// One attention read, with one entry whose text names where it came from, so
/// a test can say whose answer is on screen.
CoordinatorAttention attention(String taskId, String from) =>
    CoordinatorAttention(
      taskID: taskId,
      entries: [
        CoordinatorAttentionEntry(
          source: const CoordinatorThreadRef(
            projectID: '/home/u/alpha',
            kind: 'agent',
            id: 'a-1',
          ),
          kind: CoordinatorAttentionKind.coordinatorAttentionAsk,
          text: 'answer from $from',
          ageSeconds: 5,
          destination: '/thread/a-1',
        ),
      ],
      sources: const [],
      allClear: false,
      partial: false,
      observedAt: '2026-11-21T10:00:00Z',
    );

/// One assignment in a handoff read, with the state a test names by wire.
CoordinatorAssignment assignment(String id, {String state = 'running'}) =>
    CoordinatorAssignment(
      id: id,
      taskID: 'a',
      projectID: '/home/u/alpha',
      projectName: 'alpha',
      relationship: CoordinatorAssignmentRelationship.fromWire('worker'),
      state: CoordinatorAssignmentState.fromWire(state),
      resultRevision: 0,
      createdAt: '2026-11-21T10:00:00Z',
      updatedAt: '2026-11-21T10:00:00Z',
    );

/// One handoff read: the assignments it carries, and an optional delivery.
CoordinatorAssignmentList handoff(
  List<CoordinatorAssignment> assignments, {
  String taskId = 'a',
  CoordinatorDelivery? delivery,
}) => CoordinatorAssignmentList(
  taskID: taskId,
  assignments: assignments,
  delivery: delivery,
);

CoordinatorDelivery delivery(String text) => CoordinatorDelivery(
  taskID: 'a',
  eventID: 'e-1',
  text: text,
  at: '2026-11-21T10:00:00Z',
);

/// Routes that record what they were asked and stay silent until a test
/// answers them, so a test controls which answer lands when — the shape of a
/// busy host, where answers come home after the person has moved on.
class _Routes {
  final attentionCalls = <String>[];
  final attentionJobs = <Completer<CoordinatorAttention>>[];
  final assignmentCalls = <String>[];
  final assignmentJobs = <Completer<CoordinatorAssignmentList>>[];
  final creates = <CoordinatorCreateRequest>[];
  final createJobs = <Completer<CoordinatorCreateResult>>[];
  final retryCalls = <List<String>>[];
  final retryJobs = <Completer<CoordinatorAssignmentList>>[];
  final stopCalls = <String>[];
  final stopJobs = <Completer<CoordinatorStopResult>>[];
  final saved = <String, String?>{};

  MarshalModel model({
    List<CoordinatorTask> tasks = const [],
    bool assignments = false,
    bool retry = false,
    bool stop = false,
  }) => MarshalModel(
    host: 'local',
    tasks: () async => CoordinatorTaskList(tasks: tasks),
    attention: (taskId) {
      attentionCalls.add(taskId);
      final job = Completer<CoordinatorAttention>();
      attentionJobs.add(job);
      return job.future;
    },
    create: (request) {
      creates.add(request);
      final job = Completer<CoordinatorCreateResult>();
      createJobs.add(job);
      return job.future;
    },
    assignments: assignments || retry
        ? (taskId) {
            assignmentCalls.add(taskId);
            final job = Completer<CoordinatorAssignmentList>();
            assignmentJobs.add(job);
            return job.future;
          }
        : null,
    retryAssignment: retry
        ? (taskId, assignmentId) {
            retryCalls.add([taskId, assignmentId]);
            final job = Completer<CoordinatorAssignmentList>();
            retryJobs.add(job);
            return job.future;
          }
        : null,
    stopTask: stop
        ? (taskId) {
            stopCalls.add(taskId);
            final job = Completer<CoordinatorStopResult>();
            stopJobs.add(job);
            return job.future;
          }
        : null,
    read: (key) => saved[key],
    save: (key, value) => saved[key] = value,
    now: () => DateTime(2026, 11, 21, 10),
  );
}

void main() {
  test('a late attention answer never lands under another task', () async {
    final routes = _Routes();
    final model = routes.model(tasks: [task('a'), task('b')]);
    final opening = model.open(taskId: 'a');
    await pumpEventQueue();
    expect(routes.attentionCalls, ['a']);
    final switching = model.selectTask('b');
    expect(routes.attentionCalls, ['a', 'b']);
    routes.attentionJobs[1].complete(attention('b', 'b'));
    await switching;
    expect(model.task!.id, 'b');
    expect(model.read!.entries.single.text, 'answer from b');
    // Task a's read was still in flight; its answer came home late, after the
    // person had already moved to b.
    routes.attentionJobs[0].complete(attention('a', 'a'));
    await pumpEventQueue();
    expect(model.read!.entries.single.text, 'answer from b');
    expect(model.reading, isFalse);
    await opening;
  });

  test('a late handoff answer never lands under another task', () async {
    final routes = _Routes();
    final model = routes.model(
      tasks: [task('a'), task('b')],
      assignments: true,
    );
    final opening = model.open(taskId: 'a');
    await pumpEventQueue();
    routes.attentionJobs[0].complete(attention('a', 'a'));
    await pumpEventQueue();
    expect(routes.assignmentCalls, ['a']);
    final switching = model.selectTask('b');
    routes.attentionJobs[1].complete(attention('b', 'b'));
    await pumpEventQueue();
    expect(routes.assignmentCalls, ['a', 'b']);
    routes.assignmentJobs[1].complete(handoff([assignment('b-1')]));
    await switching;
    expect(model.assignments.single.id, 'b-1');
    // Task a's handoff read was still in flight; it answers too late.
    routes.assignmentJobs[0].complete(handoff([assignment('a-1')]));
    await pumpEventQueue();
    expect(model.assignments.single.id, 'b-1');
    expect(model.readingAssignments, isFalse);
    await opening;
  });

  test('a scoped retry answers over a read still in flight', () async {
    final routes = _Routes();
    final model = routes.model(
      tasks: [task('a')],
      assignments: true,
      retry: true,
    );
    final opening = model.open(taskId: 'a');
    await pumpEventQueue();
    routes.attentionJobs[0].complete(attention('a', 'a'));
    await pumpEventQueue();
    routes.assignmentJobs[0].complete(
      handoff([assignment('x', state: 'failed')]),
    );
    await opening;
    expect(model.assignments.single.state, MarshalAssignmentState.failed);
    // A re-read starts; before it answers, the failed row is retried.
    final reread = model.refreshAssignments();
    expect(routes.assignmentCalls.length, 2);
    final retried = model.retryAssignment('x');
    expect(routes.retryCalls.single, ['a', 'x']);
    routes.retryJobs.single.complete(
      handoff([assignment('x', state: 'running')]),
    );
    await retried;
    expect(model.assignments.single.state, MarshalAssignmentState.running);
    expect(model.retrying, isNull);
    // The read asked before the retry finally answers, too late: what the
    // retry settled is what stays on screen.
    routes.assignmentJobs[1].complete(
      handoff([assignment('x', state: 'failed')]),
    );
    await pumpEventQueue();
    expect(model.assignments.single.state, MarshalAssignmentState.running);
    await reread;
    expect(model.readingAssignments, isFalse);
  });

  test(
    'a stop settles the handoff, and a stale read cannot overwrite it',
    () async {
      final routes = _Routes();
      final model = routes.model(
        tasks: [task('a')],
        assignments: true,
        stop: true,
      );
      final opening = model.open(taskId: 'a');
      await pumpEventQueue();
      routes.attentionJobs[0].complete(attention('a', 'a'));
      await pumpEventQueue();
      routes.assignmentJobs[0].complete(
        handoff([assignment('x', state: 'running')]),
      );
      await opening;
      final reread = model.refreshAssignments();
      final stopping = model.stopTask();
      expect(routes.stopCalls.single, 'a');
      routes.stopJobs.single.complete(
        CoordinatorStopResult(
          task: task('a', status: 'stopped'),
          assignments: [assignment('x', state: 'stopped')],
          delivery: delivery('delivered the result'),
        ),
      );
      await stopping;
      expect(model.task!.state, MarshalTaskState.stopped);
      expect(model.stopFailure, isNull);
      expect(model.assignments.single.state, MarshalAssignmentState.stopped);
      expect(model.resultText, 'delivered the result');
      // The read asked before the stop finally answers, too late.
      routes.assignmentJobs[1].complete(
        handoff([assignment('x', state: 'running')]),
      );
      await pumpEventQueue();
      expect(model.assignments.single.state, MarshalAssignmentState.stopped);
      expect(model.task!.state, MarshalTaskState.stopped);
      await reread;
      expect(model.readingAssignments, isFalse);
    },
  );

  test('a failed read is behind, not on, a draft', () async {
    final routes = _Routes();
    final model = routes.model(tasks: [task('a')]);
    final opening = model.open(taskId: 'a');
    await pumpEventQueue();
    routes.attentionJobs[0].completeError(BridgeFailure(503, 'down'));
    await opening;
    expect(model.status, MarshalStatus.unavailable);
    expect(model.task!.id, 'a');
    // The draft is a fresh destination: the failed read belongs to the task
    // that was left, and the composer must be reachable here rather than the
    // page being replaced by the old task's failure.
    await model.open(draft: 'd-1');
    expect(model.draftId, 'd-1');
    expect(model.task, isNull);
    expect(model.status, MarshalStatus.ready);
    expect(model.failure, isNull);
    expect(model.read, isNull);
  });

  test(
    'an attention answer still in flight cannot land after a draft opened',
    () async {
      final routes = _Routes();
      final model = routes.model(tasks: [task('a')]);
      final opening = model.open(taskId: 'a');
      await pumpEventQueue();
      await model.open(draft: 'd-1');
      routes.attentionJobs[0].complete(attention('a', 'a'));
      await pumpEventQueue();
      expect(model.read, isNull);
      expect(model.limited, isFalse);
      await opening;
    },
  );

  test('the read that failed for one task does not frame the next', () async {
    final routes = _Routes();
    final model = routes.model(tasks: [task('a'), task('b')]);
    final opening = model.open(taskId: 'a');
    await pumpEventQueue();
    routes.attentionJobs[0].completeError(BridgeFailure(503, 'down'));
    await opening;
    expect(model.status, MarshalStatus.unavailable);
    final switching = model.selectTask('b');
    // While b's read is in flight, a's failure is not b's page.
    expect(model.task!.id, 'b');
    expect(model.failure, isNull);
    expect(model.status, MarshalStatus.ready);
    routes.attentionJobs[1].complete(attention('b', 'b'));
    await switching;
    expect(model.read!.entries.single.text, 'answer from b');
    expect(model.status, MarshalStatus.ready);
    await opening;
  });

  test('the first send creates once and opens the task it made', () async {
    final routes = _Routes();
    final model = routes.model();
    final first = model.start('d-1', 'Design the flange');
    expect(routes.creates.single.requestID, 'd-1');
    expect(routes.creates.single.title, 'Design the flange');
    // A second press while the first send is in flight asks for nothing.
    expect(await model.start('d-1', 'Design the flange'), isNull);
    expect(routes.creates, hasLength(1));
    routes.createJobs.single.complete(
      CoordinatorCreateResult(
        task: task('t-1', title: 'Design the flange'),
        created: true,
      ),
    );
    await pumpEventQueue();
    routes.attentionJobs.single.complete(attention('t-1', 't-1'));
    final made = await first;
    expect(made!.id, 't-1');
    expect(model.task!.id, 't-1');
    expect(model.draftId, isNull);
    expect(model.tasks.single.id, 't-1');
    expect(model.starting, isFalse);
    expect(routes.saved['marshal.task.local'], 't-1');
  });

  test('a replayed create still opens the one task it made', () async {
    final routes = _Routes();
    final model = routes.model();
    final first = model.start('d-1', 'Design the flange');
    routes.createJobs.single.complete(
      CoordinatorCreateResult(task: task('t-1'), created: false),
    );
    await pumpEventQueue();
    routes.attentionJobs.single.complete(attention('t-1', 't-1'));
    expect((await first)!.id, 't-1');
    expect(model.task!.id, 't-1');
    expect(routes.creates, hasLength(1));
  });

  test('a failed create keeps the draft and the request id it sent', () async {
    final routes = _Routes();
    final model = routes.model();
    await model.open(draft: 'd-1');
    final first = model.start('d-1', 'Design the flange');
    routes.createJobs[0].completeError(BridgeFailure(500, 'boom'));
    expect(await first, isNull);
    expect(model.draftId, 'd-1');
    expect(model.failure, 'boom');
    expect(model.task, isNull);
    expect(model.starting, isFalse);
    // The retry of the same draft sends the id the first send carried, so the
    // bridge resolves the original request instead of minting a second task.
    final again = model.start('d-1', 'Design the flange');
    expect(routes.creates[1].requestID, 'd-1');
    routes.createJobs[1].complete(
      CoordinatorCreateResult(task: task('t-1'), created: false),
    );
    await pumpEventQueue();
    routes.attentionJobs.single.complete(attention('t-1', 't-1'));
    expect((await again)!.id, 't-1');
    expect(model.draftId, isNull);
  });

  test('a first send that outlives its destination starts nothing', () async {
    final routes = _Routes();
    final model = routes.model();
    final first = model.start('d-1', 'Design the flange');
    model.dispose();
    routes.createJobs.single.complete(
      CoordinatorCreateResult(task: task('t-1'), created: true),
    );
    expect(await first, isNull);
    // A model that is gone reads nothing: no attention read is fired for a
    // screen that will never draw it.
    expect(routes.attentionCalls, isEmpty);
  });

  test('a refused token is unauthorized, not an error to retry', () async {
    final routes = _Routes();
    final model = routes.model(tasks: [task('a')]);
    final opening = model.open(taskId: 'a');
    await pumpEventQueue();
    routes.attentionJobs[0].completeError(BridgeFailure(401, 'no'));
    await opening;
    expect(model.status, MarshalStatus.unauthorized);
    expect(model.cachedOnly, isFalse);
  });

  test('a plain refusal is an error, with the bridge\'s words', () async {
    final routes = _Routes();
    final model = routes.model(tasks: [task('a')]);
    final opening = model.open(taskId: 'a');
    await pumpEventQueue();
    routes.attentionJobs[0].completeError(BridgeFailure(404, 'gone'));
    await opening;
    expect(model.status, MarshalStatus.error);
    expect(model.failure, 'gone');
  });

  test('a scoped retry keeps the task the source view read', () async {
    final viewJob = Completer<CoordinatorSourceView>();
    final retryJob = Completer<CoordinatorAssignmentList>();
    final model = SourceHandoffModel(
      host: 'local',
      projectId: '/home/u/alpha',
      threadId: 'a-1',
      read: (projectId, threadId) => viewJob.future,
      retry: (taskId, assignmentId) => retryJob.future,
      now: () => DateTime(2026, 11, 21, 10),
    );
    final starting = model.start();
    viewJob.complete(
      CoordinatorSourceView(
        task: task('t-1', title: 'Design the flange'),
        assignments: [assignment('x', state: 'failed')],
      ),
    );
    await starting;
    expect(model.failure, isNull);
    expect(model.handoff!.taskTitle, 'Design the flange');
    expect(model.handoff!.rows.single.state, HandoffAssignmentState.failed);
    final retried = model.retryAssignment('x');
    retryJob.complete(
      handoff([assignment('x', state: 'running')], taskId: 't-1'),
    );
    await retried;
    // The retry never moves the task, so the band keeps the name the source
    // view read rather than falling back to a task the retry answer invented.
    expect(model.handoff!.taskId, 't-1');
    expect(model.handoff!.taskTitle, 'Design the flange');
    expect(model.handoff!.rows.single.state, HandoffAssignmentState.running);
    model.dispose();
  });

  test('a refresh asked before a retry cannot bury the retry', () async {
    final viewJobs = <Completer<CoordinatorSourceView>>[];
    final retryJob = Completer<CoordinatorAssignmentList>();
    final model = SourceHandoffModel(
      host: 'local',
      projectId: '/home/u/alpha',
      threadId: 'a-1',
      read: (projectId, threadId) {
        final job = Completer<CoordinatorSourceView>();
        viewJobs.add(job);
        return job.future;
      },
      retry: (taskId, assignmentId) => retryJob.future,
      now: () => DateTime(2026, 11, 21, 10),
    );
    final starting = model.start();
    viewJobs[0].complete(
      CoordinatorSourceView(
        task: task('t-1', title: 'Design the flange'),
        assignments: [assignment('x', state: 'failed')],
      ),
    );
    await starting;
    // A reconnect refreshes; before it answers, the failed row is retried.
    final resumed = model.refresh();
    final retried = model.retryAssignment('x');
    retryJob.complete(
      handoff([assignment('x', state: 'running')], taskId: 't-1'),
    );
    await retried;
    expect(model.handoff!.rows.single.state, HandoffAssignmentState.running);
    // The refresh asked before the retry finally answers, too late.
    viewJobs[1].complete(
      CoordinatorSourceView(
        task: task('t-1', title: 'Design the flange'),
        assignments: [assignment('x', state: 'failed')],
      ),
    );
    await pumpEventQueue();
    expect(model.handoff!.rows.single.state, HandoffAssignmentState.running);
    expect(model.failure, isNull);
    await resumed;
    model.dispose();
  });
}
