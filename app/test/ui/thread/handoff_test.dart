import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

import 'support.dart';

/// Task 7 of the agent-first Studio plan: a thread that handed work to Marshal
/// draws the handoff it is the source of — the link to the coordinated task, a
/// row per assignment, and the result that came back — without becoming a second
/// conversation, and without losing what the thread itself was doing.
void main() {
  final now = DateTime.utc(2026, 11, 21, 12);

  HandoffAssignmentRow row({
    required String id,
    required String project,
    HandoffAssignmentState state = HandoffAssignmentState.running,
    String instruction = '',
    String threadTitle = '',
    Place? thread,
    String? reason,
    DateTime? updatedAt,
  }) => HandoffAssignmentRow(
    id: id,
    project: project,
    state: state,
    instruction: instruction,
    threadTitle: threadTitle,
    thread: thread,
    reason: reason,
    updatedAt: updatedAt ?? now.subtract(const Duration(minutes: 2)),
  );

  HandoffView handoff({
    Place? task = const Place.marshal(host: 'local', id: 'task-1'),
    List<HandoffAssignmentRow> rows = const [],
    String? result,
    DateTime? resultAt,
    DateTime? observedAt,
    bool stale = false,
  }) => HandoffView(
    taskId: 'task-1',
    taskTitle: 'Client compatibility',
    task: task,
    rows: rows,
    result: result,
    resultAt: resultAt,
    observedAt: observedAt ?? now.subtract(const Duration(minutes: 1)),
    stale: stale,
  );

  testWidgets('the band draws the task, each project and the result', (
    tester,
  ) async {
    final view = ThreadViewState()
      ..handoff = handoff(
        rows: [
          row(
            id: 'asg-cli',
            project: 'cli-frontend',
            threadTitle: 'Accept the compat flags',
            thread: const Place(PlaceKind.thread, host: 'local', id: 'a-cli'),
          ),
          row(
            id: 'asg-api',
            project: 'api',
            state: HandoffAssignmentState.failed,
            reason: 'the task’s access to the project was revoked',
          ),
        ],
        result:
            'Marshal: 2 assignments. cli-frontend: running; api: failed: the '
            'task’s access to the project was revoked.',
        resultAt: now.subtract(const Duration(minutes: 3)),
        stale: true,
      );
    Place? openedTask;
    Place? openedThread;
    String? retried;
    await pumpThread(
      tester,
      store: fixtureStore(),
      view: view,
      now: now,
      onOpenHandoff: (place) => openedTask = place,
      onOpenAssignment: (place) => openedThread = place,
      onRetryAssignment: (id) async => retried = id,
    );

    // One group, named for what it is, with a row per project and the state in
    // words — never a bare wordless status.
    expect(find.text('HANDOFF'), findsOneWidget);
    expect(find.textContaining('cli-frontend'), findsWidgets);
    expect(find.textContaining('api'), findsWidgets);
    expect(find.textContaining('Running'), findsWidgets);
    expect(find.textContaining('Failed:'), findsOneWidget);
    expect(
      find.textContaining('thread: Accept the compat flags'),
      findsOneWidget,
    );

    // The result the daemon reported back, and when.
    expect(find.textContaining('Marshal: 2 assignments'), findsOneWidget);
    expect(find.textContaining('Reported'), findsOneWidget);

    // A view whose read failed says how old what it shows is rather than
    // presenting it as current.
    expect(find.textContaining('Last observed'), findsOneWidget);

    // The handoff link opens the coordinated task on this host.
    await tester.tap(find.text('Open Marshal task'));
    expect(openedTask, const Place.marshal(host: 'local', id: 'task-1'));

    // An assignment row opens the thread it resolved to.
    await tester.tap(find.text('cli-frontend'));
    expect(
      openedThread,
      const Place(PlaceKind.thread, host: 'local', id: 'a-cli'),
    );

    // The failure offers a retry scoped to exactly that assignment.
    expect(find.text('Retry cli-frontend'), findsNothing);
    await tester.tap(find.text('Retry api'));
    expect(retried, 'asg-api');
  });

  testWidgets('a blocked worker shows the original ask it is waiting on', (
    tester,
  ) async {
    final view = ThreadViewState()
      ..handoff = handoff(
        rows: [
          row(
            id: 'asg-cli',
            project: 'cli-frontend',
            state: HandoffAssignmentState.pending,
            instruction: 'Accept the new parser flags',
          ),
        ],
      );
    String? retried;
    await pumpThread(
      tester,
      store: fixtureStore(),
      view: view,
      now: now,
      onRetryAssignment: (id) async => retried = id,
    );

    expect(find.textContaining('Waiting for you'), findsWidgets);
    expect(
      find.textContaining('ask: Accept the new parser flags'),
      findsOneWidget,
    );
    // A submission nobody confirmed has nothing to retry: it is waiting on the
    // person, not on the daemon.
    expect(find.textContaining('Retry'), findsNothing);
    expect(retried, isNull);
  });

  testWidgets('a thread that handed nothing off draws no band', (tester) async {
    await pumpThread(
      tester,
      store: fixtureStore(),
      view: ThreadViewState(),
      now: now,
    );
    expect(find.text('HANDOFF'), findsNothing);
    expect(find.text('Open Marshal task'), findsNothing);
  });

  testWidgets('a handoff without a link draws the facts and no dead control', (
    tester,
  ) async {
    final view = ThreadViewState()
      ..handoff = handoff(
        task: null,
        rows: [row(id: 'asg-cli', project: 'cli-frontend')],
      );
    Place? openedTask;
    await pumpThread(
      tester,
      store: fixtureStore(),
      view: view,
      now: now,
      onOpenHandoff: (place) => openedTask = place,
    );
    expect(find.text('Open Marshal task'), findsNothing);
    expect(find.textContaining('cli-frontend'), findsWidgets);
    expect(openedTask, isNull);
  });

  testWidgets('a retry in flight reads as pending and cannot be asked twice', (
    tester,
  ) async {
    final view = ThreadViewState()
      ..handoff = handoff(
        rows: [
          row(
            id: 'asg-api',
            project: 'api',
            state: HandoffAssignmentState.failed,
            reason: 'the daemon refused the dispatch',
          ),
        ],
      );
    final retries = <String>[];
    final inFlight = Completer<void>();
    await pumpThread(
      tester,
      store: fixtureStore(),
      view: view,
      now: now,
      onRetryAssignment: (id) async {
        retries.add(id);
        await inFlight.future;
      },
    );

    await tester.tap(find.text('Retry api'));
    await tester.pump();
    // The action the person took is acknowledged at once: the row's button
    // names what it waits on, and no second retry can be asked while it is
    // (§9: an action taken shows as pending until the daemon confirms it).
    expect(retries, ['asg-api']);
    expect(find.text('Retrying…'), findsOneWidget);

    // A second press while the first waits asks nothing more: one retry per
    // row, whichever press it was.
    await tester.tap(find.text('Retrying…'));
    await tester.pump();
    expect(retries, ['asg-api']);

    // The answer arrives: the control is ready for the row again.
    inFlight.complete();
    await tester.pumpAndSettle();
    expect(find.text('Retry api'), findsOneWidget);
  });
}
