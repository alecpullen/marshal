import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/marshal_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/marshal/marshal_place.dart';

/// Task 5 of the agent-first Studio plan: Marshal's destination.
///
/// The cases are the ones the plan names — two tasks with independent drafts
/// and their own answer, an ask that links to its own project's thread across
/// two projects, and a host that cannot be reached — plus the claim the read
/// has to keep: an answer this build could not refresh is never an all-clear.
void main() {
  final now = DateTime.utc(2026, 11, 21, 12);

  CoordinatorTask task(String id) => CoordinatorTask(
    id: id,
    sessionID: 'coord-$id',
    title: 'Task $id',
    status: CoordinatorTaskStatus.coordinatorTaskOpen,
    revision: 1,
    projects: const [],
    createdAt: now.toIso8601String(),
    updatedAt: now.toIso8601String(),
  );

  CoordinatorAttentionEntry entry({
    required String text,
    required String project,
    required String id,
    String? destination,
    String kind = 'ask',
    String sourceKind = 'agent',
    int ageSeconds = 120,
  }) => CoordinatorAttentionEntry(
    source: CoordinatorThreadRef(projectID: project, kind: sourceKind, id: id),
    kind: kind == 'review'
        ? CoordinatorAttentionKind.coordinatorAttentionReview
        : CoordinatorAttentionKind.coordinatorAttentionAsk,
    text: text,
    ageSeconds: ageSeconds,
    destination: destination,
  );

  CoordinatorSource fresh(String project) => CoordinatorSource(
    projectID: project,
    state: CoordinatorSourceState.coordinatorSourceFresh,
  );

  CoordinatorAttention attention({
    required String taskID,
    List<CoordinatorAttentionEntry> entries = const [],
    List<CoordinatorSource> sources = const [],
    bool allClear = false,
    bool partial = false,
  }) => CoordinatorAttention(
    taskID: taskID,
    host: 'local',
    entries: entries,
    sources: sources,
    allClear: allClear,
    partial: partial,
    observedAt: now.toIso8601String(),
  );

  /// A model over two tasks, each with its own attention result, driven by the
  /// routes a host would answer with.
  MarshalModel modelFor({
    required Map<String, CoordinatorAttention> answers,
    Future<CoordinatorAttention> Function(String taskId)? attentionRoute,
    Future<CoordinatorAssignmentList> Function(String taskId)? assignments,
    Future<CoordinatorAssignmentList> Function(
      String taskId,
      String assignmentId,
    )?
    retryAssignment,
    String? remembered,
    void Function(String key, String value)? save,
    DateTime Function()? clock,
  }) => MarshalModel(
    host: 'local',
    tasks: () async =>
        CoordinatorTaskList(tasks: [task('a'), task('b')], host: 'local'),
    attention:
        attentionRoute ?? (id) async => answers[id] ?? attention(taskID: id),
    create: (request) async => throw StateError('this test creates nothing'),
    assignments: assignments,
    retryAssignment: retryAssignment,
    read: remembered == null ? null : (key) => remembered,
    save: save,
    now: clock ?? () => now,
  );

  test('two tasks keep their own draft and their own answer', () async {
    final model = modelFor(
      answers: {
        'a': attention(
          taskID: 'a',
          entries: [
            entry(text: 'Recover the session', project: 'p-a', id: 'a-1'),
          ],
          sources: [fresh('p-a')],
        ),
        'b': attention(
          taskID: 'b',
          entries: [
            entry(
              text: 'Review the CLI',
              project: 'p-b',
              id: 'pr-2',
              kind: 'review',
              sourceKind: 'review',
              destination: '/pulls',
            ),
          ],
          sources: [fresh('p-b')],
        ),
      },
    );

    await model.load();
    await model.selectTask('a');
    expect(model.read!.entries.single.text, 'Recover the session');

    // Each view's draft is its own: switching tasks is not a way to lose what
    // was typed in the one a person left.
    model.viewFor(MarshalModel.viewKeyForTask('a')).draft = 'What needs me?';
    await model.selectTask('b');
    expect(model.viewFor(MarshalModel.viewKeyForTask('b')).draft, isEmpty);
    model.viewFor(MarshalModel.viewKeyForTask('b')).draft =
        'Anything to review?';
    expect(
      model.viewFor(MarshalModel.viewKeyForTask('a')).draft,
      'What needs me?',
    );

    // History is the answer's, too: task B's read is not task A's.
    expect(model.read!.entries.single.text, 'Review the CLI');
    await model.selectTask('a');
    expect(model.read!.entries.single.text, 'Recover the session');

    // New opens a distinct draft every time: a reused id would replay the task
    // it first created.
    final first = model.newDraft();
    final second = model.newDraft();
    expect(first, isNot(second));
    expect(
      MarshalModel.viewKeyForDraft(first),
      isNot(MarshalModel.viewKeyForDraft(second)),
    );
    model.dispose();
  });

  test('an ask links to the host and thread it came from', () async {
    final model = modelFor(
      answers: {
        'a': attention(
          taskID: 'a',
          entries: [
            // Two projects, two shapes of link: a fleet thread and a ready
            // review. The thread is host-qualified because a task is
            // host-local: the same id elsewhere is a different thread.
            entry(
              text: 'Recover the session',
              project: 'p-a',
              id: 'a-1',
              destination: '/thread/a-1',
            ),
            entry(
              text: 'Review the CLI',
              project: 'p-b',
              id: 'pr-2',
              kind: 'review',
              sourceKind: 'review',
              destination: '/pulls',
            ),
            // A submission opens Needs you, and an entry the daemon gave no
            // destination is a fact without a link — never a link to the
            // Overview.
            entry(
              text: 'Confirm the fix',
              project: 'p-a',
              id: 's-1',
              destination: '/inbox',
            ),
            entry(
              text: 'An entry with no destination',
              project: 'p-b',
              id: 'x-1',
            ),
          ],
          sources: [fresh('p-a'), fresh('p-b')],
        ),
      },
    );

    await model.load();
    await model.selectTask('a');
    final places = [for (final each in model.read!.entries) each.place];
    expect(places[0], const Place(PlaceKind.thread, host: 'local', id: 'a-1'));
    expect(places[1], const Place(PlaceKind.pulls));
    expect(places[2], const Place.inbox());
    expect(places[3], isNull);
    model.dispose();
  });

  test('a read that cannot be refreshed is limited, never an all-clear', () async {
    var reads = 0;
    final model = modelFor(
      answers: const {},
      attentionRoute: (id) async {
        reads++;
        if (reads > 1) {
          throw BridgeFailure(503, '{"error":"unavailable"}');
        }
        return attention(taskID: id, sources: [fresh('p-a')], allClear: true);
      },
    );

    await model.load();
    await model.selectTask('a');
    expect(model.read, isNotNull);
    expect(
      model.cleared,
      isTrue,
      reason: 'a whole, fresh, empty read is clear',
    );

    // The host goes away between two queries: what was read is kept, and it is
    // never presented as today's all-clear.
    await model.refresh();
    expect(model.failure, isNotNull);
    expect(model.limited, isTrue);
    expect(model.cachedOnly, isTrue);
    expect(model.cleared, isFalse);
    expect(model.cachedAge, isNotNull);
    expect(model.status, MarshalStatus.ready, reason: 'the last answer stands');
    model.dispose();
  });

  testWidgets('the age of a cached answer is shown, and no all-clear', (
    tester,
  ) async {
    var reads = 0;
    final model = modelFor(
      answers: const {},
      attentionRoute: (id) async {
        reads++;
        if (reads > 1) throw BridgeFailure(503, '{"error":"unavailable"}');
        return attention(taskID: id, sources: [fresh('p-a')], allClear: true);
      },
    );
    await model.load();
    await model.selectTask('a');
    await model.refresh();

    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: MarshalPlace(
          model: model,
          host: 'local',
          now: now,
          onRetry: () {},
        ),
      ),
    );

    expect(find.textContaining('could not be reached'), findsOneWidget);
    expect(find.textContaining('Nothing needs you'), findsNothing);
  });

  testWidgets('an ask opens the place its own project lives in', (
    tester,
  ) async {
    final model = modelFor(
      answers: {
        'a': attention(
          taskID: 'a',
          entries: [
            entry(
              text: 'Recover the session',
              project: 'p-a',
              id: 'a-1',
              destination: '/thread/a-1',
            ),
            entry(
              text: 'Review the CLI',
              project: 'p-b',
              id: 'pr-2',
              kind: 'review',
              sourceKind: 'review',
              destination: '/pulls',
            ),
          ],
          sources: [fresh('p-a'), fresh('p-b')],
        ),
      },
    );
    await model.load();
    await model.selectTask('a');

    Place? opened;
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: MarshalPlace(
          model: model,
          host: 'local',
          now: now,
          onOpen: (place) => opened = place,
          onRetry: () {},
        ),
      ),
    );

    // Each row says what opening it does, and opens its own source: the thread
    // on the host that served the read, and the reviews' own screen.
    expect(find.text('Ask · opens the thread'), findsOneWidget);
    expect(find.text('Review · opens Pull requests'), findsOneWidget);

    await tester.tap(find.text('Recover the session'));
    expect(opened, const Place(PlaceKind.thread, host: 'local', id: 'a-1'));

    await tester.tap(find.text('Review the CLI'));
    expect(opened, const Place(PlaceKind.pulls));
  });

  testWidgets('the recipient is named above the composer', (tester) async {
    final model = MarshalModel.seeded(
      host: 'local',
      tasks: [
        MarshalTask(
          id: 'a',
          sessionId: 'coord-a',
          title: 'Task a',
          state: MarshalTaskState.open,
          updatedAt: now,
        ),
      ],
      draft: 'draft-1',
      now: () => now,
    );
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: MarshalPlace(model: model, host: 'local', now: now),
      ),
    );
    expect(find.text('To Marshal'), findsOneWidget);
    expect(find.text('on local'), findsOneWidget);
    // A draft creates nothing until its first send, and no mode is claimed for
    // a session that does not exist yet.
    expect(find.textContaining('Ask Marshal what needs you'), findsOneWidget);
  });

  // ── The handoff (task 7) ───────────────────────────────────────────────
  //
  // One task's assignments and the result it reported back, as the destination
  // draws them: each project with its state and thread link, a scoped retry for
  // the one that failed, the original ask for one blocked on a person, and the
  // age of a read this build could not refresh.

  CoordinatorAssignment assignment({
    required String id,
    required String project,
    String projectName = '',
    CoordinatorAssignmentState state =
        CoordinatorAssignmentState.coordinatorAssignmentRunning,
    String instruction = '',
    String? workerID,
    String? threadID,
    String? reason,
    int resultRevision = 1,
  }) => CoordinatorAssignment(
    id: id,
    taskID: 'a',
    projectID: project,
    projectName: projectName,
    instruction: instruction,
    relationship: threadID == null
        ? CoordinatorAssignmentRelationship.coordinatorAssignmentWorker
        : CoordinatorAssignmentRelationship.coordinatorAssignmentThread,
    state: state,
    worker: workerID == null
        ? null
        : CoordinatorWorkerRef(agentID: workerID, sessionID: 's-$workerID'),
    thread: threadID == null
        ? null
        : CoordinatorThreadRef(projectID: project, kind: 'agent', id: threadID),
    resultRevision: resultRevision,
    reason: reason,
    createdAt: now.toIso8601String(),
    updatedAt: now.subtract(const Duration(minutes: 2)).toIso8601String(),
  );

  CoordinatorAssignmentList assignmentList(
    List<CoordinatorAssignment> rows, {
    String? result,
  }) => CoordinatorAssignmentList(
    taskID: 'a',
    host: 'local',
    assignments: rows,
    delivery: result == null
        ? null
        : CoordinatorDelivery(
            taskID: 'a',
            eventID: 'coord-delivery-1',
            text: result,
            at: now.subtract(const Duration(minutes: 3)).toIso8601String(),
          ),
  );

  test('the task draws each assignment, its thread and the result', () async {
    final model = modelFor(
      answers: {
        'a': attention(taskID: 'a', sources: [fresh('p-a')]),
      },
      assignments: (id) async => assignmentList([
        assignment(
          id: 'asg-cli',
          project: 'p-cli',
          projectName: 'cli-frontend',
          instruction: 'Accept the new parser flags',
          workerID: 'a-cli',
        ),
        assignment(
          id: 'asg-api',
          project: 'p-api',
          projectName: 'api',
          state: CoordinatorAssignmentState.coordinatorAssignmentFailed,
          reason: 'the task’s access to the project was revoked',
        ),
      ], result: 'Marshal: 2 assignments. cli-frontend: running; api: failed.'),
    );
    await model.load();
    await model.selectTask('a');

    expect(model.assignments.length, 2);
    final handoff = model.handoff!;
    expect(handoff.taskId, 'a');
    expect(handoff.task, const Place.marshal(host: 'local', id: 'a'));
    expect(handoff.rows[0].project, 'cli-frontend');
    expect(handoff.rows[0].state, HandoffAssignmentState.running);
    expect(handoff.rows[0].instruction, 'Accept the new parser flags');
    // The worker's agent id is what a thread link names, and the host is this
    // destination's own: a task is host-local.
    expect(
      handoff.rows[0].thread,
      const Place(PlaceKind.thread, host: 'local', id: 'a-cli'),
    );
    expect(handoff.rows[1].state, HandoffAssignmentState.failed);
    expect(handoff.rows[1].retryable, isTrue);
    expect(handoff.rows[1].stateWords, contains('revoked'));
    expect(handoff.result, contains('cli-frontend: running'));
    expect(handoff.resultAt, isNotNull);
    expect(handoff.stale, isFalse);
    model.dispose();
  });

  test('a failed assignment retries only itself', () async {
    final calls = <String>[];
    final model = modelFor(
      answers: {
        'a': attention(taskID: 'a', sources: [fresh('p-a')]),
      },
      assignments: (id) async => assignmentList([
        assignment(
          id: 'asg-api',
          project: 'p-api',
          projectName: 'api',
          state: CoordinatorAssignmentState.coordinatorAssignmentFailed,
          reason: 'the host could not start the worker',
        ),
      ]),
      retryAssignment: (taskID, assignmentID) async {
        calls.add('$taskID/$assignmentID');
        return assignmentList([
          assignment(
            id: 'asg-api',
            project: 'p-api',
            projectName: 'api',
            workerID: 'a-api',
          ),
        ], result: 'Marshal: 1 assignment. api: running.');
      },
    );
    await model.load();
    await model.selectTask('a');
    await model.retryAssignment('asg-api');

    // Exactly one assignment was retried, by its own identity, on this task.
    expect(calls.single, 'a/asg-api');
    expect(model.assignments.single.state, MarshalAssignmentState.running);
    expect(
      model.handoff!.rows.single.thread,
      const Place(PlaceKind.thread, host: 'local', id: 'a-api'),
    );
    expect(model.handoff!.result, contains('api: running'));
    model.dispose();
  });

  test(
    'a handoff read that failed keeps what it showed and says its age',
    () async {
      var reads = 0;
      final model = modelFor(
        answers: {
          'a': attention(taskID: 'a', sources: [fresh('p-a')]),
        },
        assignments: (id) async {
          reads++;
          if (reads > 1) throw BridgeFailure(503, '{"error":"unavailable"}');
          return assignmentList([
            assignment(
              id: 'asg-cli',
              project: 'p-cli',
              projectName: 'cli-frontend',
              workerID: 'a-cli',
            ),
          ]);
        },
      );
      await model.load();
      await model.selectTask('a');
      final first = model.handoff!;
      expect(first.stale, isFalse);

      // The host goes away between two reads: what was read stands, marked as
      // possibly behind rather than presented as current.
      await model.refreshAssignments();
      expect(model.assignmentsFailure, isNotNull);
      final stale = model.handoff!;
      expect(stale.stale, isTrue);
      expect(stale.rows.length, first.rows.length);
      expect(stale.observedAt, isNotNull);
      model.dispose();
    },
  );

  test('each composer keeps its draft across a handoff read', () async {
    final model = modelFor(
      answers: {
        'a': attention(taskID: 'a', sources: [fresh('p-a')]),
      },
      assignments: (id) async => assignmentList([
        assignment(
          id: 'asg-cli',
          project: 'p-cli',
          projectName: 'cli-frontend',
          workerID: 'a-cli',
        ),
      ]),
    );
    await model.load();
    await model.selectTask('a');
    model.viewFor(MarshalModel.viewKeyForTask('a')).draft = 'What needs me?';
    final draft = model.newDraft();
    model.viewFor(MarshalModel.viewKeyForDraft(draft)).draft = 'Start a task';

    await model.refreshAssignments();

    // Reading the handoff is not a way to lose what either composer holds.
    expect(
      model.viewFor(MarshalModel.viewKeyForTask('a')).draft,
      'What needs me?',
    );
    expect(
      model.viewFor(MarshalModel.viewKeyForDraft(draft)).draft,
      'Start a task',
    );
    model.dispose();
  });

  testWidgets('the destination draws the handoff its task dispatched', (
    tester,
  ) async {
    final model = MarshalModel.seeded(
      host: 'local',
      task: MarshalTask(
        id: 'a',
        sessionId: 'coord-a',
        title: 'Task a',
        state: MarshalTaskState.open,
        updatedAt: now,
      ),
      assignments: [
        MarshalAssignment(
          id: 'asg-cli',
          projectId: 'p-cli',
          projectName: 'cli-frontend',
          state: MarshalAssignmentState.running,
          instruction: 'Accept the new parser flags',
          threadId: 'a-cli',
          updatedAt: now,
        ),
        MarshalAssignment(
          id: 'asg-api',
          projectId: 'p-api',
          projectName: 'api',
          state: MarshalAssignmentState.failed,
          reason: 'the task’s access to the project was revoked',
          updatedAt: now,
        ),
      ],
      result: 'Marshal: 2 assignments. cli-frontend: running; api: failed.',
      resultAt: now.subtract(const Duration(minutes: 3)),
      observedAt: now,
      now: () => now,
    );
    Place? opened;
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: MarshalPlace(
          model: model,
          host: 'local',
          now: now,
          onOpen: (place) => opened = place,
          onRetry: () {},
        ),
      ),
    );

    expect(find.text('HANDOFF'), findsOneWidget);
    expect(find.textContaining('cli-frontend'), findsWidgets);
    expect(find.textContaining('Waiting for you'), findsNothing);
    expect(find.textContaining('Marshal: 2 assignments'), findsOneWidget);
    // The one failed assignment is the one retry offered.
    expect(find.text('Retry api'), findsOneWidget);
    expect(find.text('Retry cli-frontend'), findsNothing);

    await tester.tap(find.text('Open Marshal task'));
    expect(opened, const Place.marshal(host: 'local', id: 'a'));

    // A seeded model holds no retry route, so pressing it cannot invent an
    // answer: the rows it showed stand.
    await tester.tap(find.text('Retry api'));
    expect(model.assignments.length, 2);
  });
}
