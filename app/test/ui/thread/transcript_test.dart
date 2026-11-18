import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/transcript.dart';

import 'support.dart';

/// A transport no test here uses: each injects the store's fetcher, so no
/// request is sent and no socket is opened.
class _UnusedTransport implements Transport {
  @override
  Future<Response> send(Request request) async =>
      throw StateError('the transcript test does not send requests');

  @override
  Stream<String> lines(Request request) =>
      throw StateError('the transcript test does not open streams');
}

void main() {
  testWidgets('renders each node kind with the row for it', (tester) async {
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(
            id: 'turn:1',
            kind: 'turn',
            children: [
              'msg:user',
              'msg:agent',
              'step:1',
              'think:1',
              'run:1',
              'plan:1',
              'done:1',
              'rcpt:1',
            ],
          ),
          WireNode(
            id: 'msg:user',
            kind: 'message',
            parent: 'turn:1',
            message: WireMessage(role: 'user', content: 'Do the thing'),
          ),
          WireNode(
            id: 'msg:agent',
            kind: 'message',
            parent: 'turn:1',
            message: WireMessage(role: 'assistant', content: 'Doing it now'),
          ),
          WireNode(
            id: 'step:1',
            kind: 'step',
            parent: 'turn:1',
            step: WireStep(headline: 'Mapped the fixtures'),
          ),
          WireNode(
            id: 'think:1',
            kind: 'thinking',
            parent: 'turn:1',
            thinking: WireThinking(text: 'The parser is the first stop'),
          ),
          WireNode(
            id: 'run:1',
            kind: 'runEvent',
            parent: 'turn:1',
            runEvent: WireRunEvent(kind: 'review', title: 'Reviewed task 2'),
          ),
          WireNode(
            id: 'plan:1',
            kind: 'plan',
            parent: 'turn:1',
            plan: WirePlan(done: 1, total: 3, activeText: 'Write the rows'),
          ),
          WireNode(
            id: 'done:1',
            kind: 'completion',
            parent: 'turn:1',
            completion: WireCompletion(
              source: 'job',
              id: 'job-3',
              label: 'shell.run',
              outcome: 'done',
              summary: 'the suite passed',
            ),
          ),
          WireNode(
            id: 'rcpt:1',
            kind: 'receipt',
            parent: 'turn:1',
            receipt: WireReceipt(
              durationMs: 95000,
              tasks: 2,
              steps: 4,
              tools: 3,
              files: 2,
            ),
          ),
        ],
      ),
    );
    final view = ThreadViewState()..density = Density.full;
    await pumpThread(tester, store: store, view: view);

    expect(find.byType(TurnPrompt), findsOneWidget);
    expect(find.text('Do the thing'), findsOneWidget);
    // The agent's reply is the only narration here: thinking, a run event and
    // a finished job are notices, not the agent talking to the person.
    expect(find.byType(Narration), findsOneWidget);
    expect(find.byType(Notice), findsNWidgets(3));
    expect(find.text('Doing it now'), findsOneWidget);
    expect(find.byType(StepHeader), findsOneWidget);
    expect(find.text('Mapped the fixtures'), findsOneWidget);
    expect(find.textContaining('shell.run'), findsOneWidget);
    expect(find.textContaining('4 steps'), findsOneWidget);

    store.dispose();
    view.dispose();
  });

  testWidgets('a folded step hides its children', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    expect(find.text('internal/acp/stack.go'), findsNWidgets(3));

    view.toggleFold('step:1');
    await tester.pumpAndSettle();

    expect(find.text('internal/acp/stack.go'), findsNWidgets(2));
    expect(find.text('Step number 1 mapped the fixtures'), findsOneWidget);

    store.dispose();
    view.dispose();
  });

  testWidgets('outline is headlines only; full adds the output', (
    tester,
  ) async {
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(id: 'turn:1', kind: 'turn', children: ['msg:1', 'step:1']),
          WireNode(
            id: 'msg:1',
            kind: 'message',
            parent: 'turn:1',
            message: WireMessage(
              role: 'assistant',
              content: 'There are 96 files',
            ),
          ),
          WireNode(
            id: 'step:1',
            kind: 'step',
            parent: 'turn:1',
            step: WireStep(headline: 'Mapped the fixtures'),
          ),
        ],
      ),
    );
    final view = ThreadViewState()..density = Density.outline;
    await pumpThread(tester, store: store, view: view);
    expect(find.text('There are 96 files'), findsNothing);
    expect(find.text('Mapped the fixtures'), findsOneWidget);

    view.density = Density.comfortable;
    await tester.pumpAndSettle();
    expect(find.text('There are 96 files'), findsOneWidget);

    store.dispose();
    view.dispose();
  });

  testWidgets('a lazily built list does not build every row at once', (
    tester,
  ) async {
    final store = fixtureStore(steps: 200);
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    expect(flattenRows(store, view).length, 402);
    // A builder list draws what the window needs, not the whole transcript.
    expect(find.byType(StepHeader).evaluate().length, lessThan(60));

    store.dispose();
    view.dispose();
  });

  testWidgets('a long path and a long title stay readable in full', (
    tester,
  ) async {
    const project =
        '/home/u/projects/a-rather-long-repository-name/app/lib/src/ui/thread';
    const title =
        'Rework the transcript grouping so delegated work reads apart from '
        'the agent narrating its own steps, without losing the density '
        'controls the TUI already has';
    final store = fixtureStore();
    final view = ThreadViewState();
    final thread = model.ThreadRef(
      host: 'local',
      id: 'a-1',
      project: project,
      title: title,
      kind: model.ThreadKind.agent,
      status: model.ThreadStatus.running,
      headline: 'Removing the fixtures nobody reads',
      lastActive: DateTime.utc(2026, 11, 17, 4, 10),
    );
    await pumpThread(tester, store: store, view: view, thread: thread);

    // A narrow window elides both lines; the tooltip carries the whole of
    // each, so neither path nor title needs the header widened.
    tester.view.physicalSize = const Size(390, 700);
    await tester.pumpAndSettle();

    final tips = [
      for (final element in find.byType(Tip).evaluate())
        (element.widget as Tip).message,
    ];
    expect(
      tips.where((message) => message.startsWith(project)),
      isNotEmpty,
      reason: 'the identity line is not readable in full',
    );
    expect(tips, contains(title));

    // The tooltip exists because the line is elided: the header still draws
    // one line of each, so the full text is what the tooltip adds.
    final elided = tester.widgetList<Text>(
      find.descendant(of: find.byType(Tip), matching: find.byType(Text)),
    );
    expect(elided, hasLength(2));
    for (final text in elided) {
      expect(text.maxLines, 1);
      expect(text.overflow, TextOverflow.ellipsis);
    }
    expect(tester.takeException(), isNull);

    store.dispose();
    view.dispose();
  });

  testWidgets('a finished job is a notice with its error second', (
    tester,
  ) async {
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(id: 'turn:1', kind: 'turn', children: ['done:1', 'done:2']),
          WireNode(
            id: 'done:1',
            kind: 'completion',
            parent: 'turn:1',
            completion: WireCompletion(
              source: 'job',
              id: 'job-1',
              label: 'shell.run',
              outcome: 'done',
            ),
          ),
          WireNode(
            id: 'done:2',
            kind: 'completion',
            parent: 'turn:1',
            completion: WireCompletion(
              source: 'job',
              id: 'job-2',
              label: 'shell.run',
              outcome: 'failed',
              error: 'the suite failed at stack_test.go:41',
            ),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // A finished job is not the agent talking: it is a notice, one per job.
    expect(find.byType(Narration), findsNothing);
    expect(find.byType(Notice), findsNWidgets(2));
    // The label and the outcome lead the line.
    expect(find.text('shell.run done'), findsOneWidget);
    expect(find.text('shell.run failed'), findsOneWidget);
    // A failed one says why on the second line, folded, and one that
    // finished says nothing about failing at all.
    expect(find.textContaining('the suite failed'), findsNothing);
    final notices = [
      for (final element in find.byType(Notice).evaluate())
        (element.widget as Notice).text,
    ];
    expect(notices.last.split('\n'), [
      'shell.run failed',
      'the suite failed at stack_test.go:41',
    ]);
    expect(notices.first, 'shell.run done');

    await tester.tap(find.text('Details').last);
    await tester.pumpAndSettle();
    expect(find.textContaining('the suite failed'), findsOneWidget);

    store.dispose();
    view.dispose();
  });

  testWidgets('a run event leads with its kind as a notice', (tester) async {
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(id: 'turn:1', kind: 'turn', children: ['run:1']),
          WireNode(
            id: 'run:1',
            kind: 'runEvent',
            parent: 'turn:1',
            runEvent: WireRunEvent(
              kind: 'review',
              title: 'Reviewed task 3',
              body: 'The reviewer read four files.',
              detail: 'task 3 of 4',
            ),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // A run event is not the agent narrating its steps: it is a notice.
    expect(find.byType(Narration), findsNothing);
    expect(find.byType(Notice), findsOneWidget);
    // The kind, led by a capital, is the notice's leading word, the title
    // stays beside it, and the detail follows the body in the order the
    // event holds them — folded, so one line is read, not four.
    final notice = tester.widget<Notice>(find.byType(Notice)).text;
    expect(notice.split('\n').first, 'Review · Reviewed task 3');
    expect(notice.split('\n').last, 'task 3 of 4');
    expect(find.text('Review · Reviewed task 3'), findsOneWidget);
    expect(find.textContaining('task 3 of 4'), findsNothing);
    // Body stays the full-density addition it was.
    expect(find.textContaining('The reviewer read four files.'), findsNothing);

    view.density = Density.full;
    await tester.pumpAndSettle();
    expect(
      tester.widget<Notice>(find.byType(Notice)).text,
      contains('The reviewer read four files.'),
    );
    expect(find.textContaining('The reviewer read four files.'), findsNothing);

    // Details unfolds what the event went on to say, body first.
    await tester.tap(find.text('Details'));
    await tester.pumpAndSettle();
    expect(
      find.textContaining('The reviewer read four files.'),
      findsOneWidget,
    );
    expect(find.textContaining('task 3 of 4'), findsOneWidget);

    store.dispose();
    view.dispose();
  });

  testWidgets('thinking is a folded notice', (tester) async {
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(id: 'turn:1', kind: 'turn', children: ['think:1']),
          WireNode(
            id: 'think:1',
            kind: 'thinking',
            parent: 'turn:1',
            thinking: WireThinking(
              text:
                  'The parser is the first stop.\n'
                  'The fixtures it reads matter more than the rest.',
            ),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // Thinking is not the agent's reply: it names itself and starts folded,
    // so one quiet line is read and the rest waits behind Details.
    expect(find.byType(Narration), findsNothing);
    expect(find.byType(Notice), findsOneWidget);
    expect(find.text('Thinking'), findsOneWidget);
    expect(find.textContaining('The parser is the first stop.'), findsNothing);

    await tester.tap(find.text('Details'));
    await tester.pumpAndSettle();
    expect(
      find.textContaining('The parser is the first stop.'),
      findsOneWidget,
    );
    expect(
      find.textContaining('The fixtures it reads matter more'),
      findsOneWidget,
    );

    // Outline density is headlines only, so thinking is not drawn at all.
    view.density = Density.outline;
    await tester.pumpAndSettle();
    expect(find.byType(Notice), findsNothing);
    expect(find.textContaining('The parser is the first stop.'), findsNothing);

    store.dispose();
    view.dispose();
  });

  testWidgets('a long message renders whole at full density', (tester) async {
    final long = [
      'The parser is the first stop, so the fixtures it reads matter more '
          'than the fixtures nobody uses any more.',
      for (var i = 0; i < 12; i++)
        'Paragraph $i keeps the message long without repeating a word.',
    ].join('\n\n');
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: [
          const WireNode(id: 'turn:1', kind: 'turn', children: ['msg:1']),
          WireNode(
            id: 'msg:1',
            kind: 'message',
            parent: 'turn:1',
            message: WireMessage(role: 'assistant', content: long),
          ),
        ],
      ),
    );
    final view = ThreadViewState()..density = Density.full;
    await pumpThread(tester, store: store, view: view);

    tester.view.physicalSize = const Size(390, 700);
    await tester.pumpAndSettle();

    expect(find.textContaining('Paragraph 11 keeps'), findsOneWidget);
    expect(tester.takeException(), isNull);

    store.dispose();
    view.dispose();
  });

  testWidgets('a system message is a notice, not narration', (tester) async {
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(
            id: 'turn:1',
            kind: 'turn',
            children: ['msg:user', 'msg:agent', 'msg:system'],
          ),
          WireNode(
            id: 'msg:user',
            kind: 'message',
            parent: 'turn:1',
            message: WireMessage(role: 'user', content: 'Do the thing'),
          ),
          WireNode(
            id: 'msg:agent',
            kind: 'message',
            parent: 'turn:1',
            message: WireMessage(role: 'assistant', content: 'Doing it now'),
          ),
          WireNode(
            id: 'msg:system',
            kind: 'message',
            parent: 'turn:1',
            message: WireMessage(
              role: 'system',
              content:
                  'Snapshot not recorded for acme-app (unrecorded).\n'
                  'No rollback snapshot was recorded.',
            ),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // The harness speaks in the notice's own voice, not the agent's.
    expect(find.byType(TurnPrompt), findsOneWidget);
    expect(find.byType(Narration), findsOneWidget);
    expect(find.byType(Notice), findsOneWidget);
    expect(find.textContaining('No rollback snapshot'), findsNothing);

    store.dispose();
    view.dispose();
  });

  testWidgets('the plan row counts while a task is active', (tester) async {
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(id: 'turn:1', kind: 'turn', children: ['plan:1', 'plan:2']),
          WireNode(
            id: 'plan:1',
            kind: 'plan',
            parent: 'turn:1',
            plan: WirePlan(done: 1, total: 3, activeText: 'Write the rows'),
          ),
          WireNode(
            id: 'plan:2',
            kind: 'plan',
            parent: 'turn:1',
            plan: WirePlan(done: 3, total: 3),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // The count leads whether or not a task is active: the active task is
    // what the plan is on, not a replacement for how far it has got.
    expect(find.text('1 of 3 · Write the rows'), findsOneWidget);
    expect(find.text('3 of 3'), findsOneWidget);
    expect(find.textContaining('done'), findsNothing);

    store.dispose();
    view.dispose();
  });

  testWidgets('the receipt stands apart', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // The row above the receipt in this fixture is the turn's last tool row:
    // the receipt carries its own space on top of the row gap, so it does
    // not sit against the last thing the agent did.
    final above = tester.getBottomLeft(find.byType(ToolRow).last).dy;
    final receipt = tester.getTopLeft(find.textContaining('4 steps')).dy;
    expect(receipt - above, greaterThanOrEqualTo(Space.s8));

    store.dispose();
    view.dispose();
  });

  testWidgets('a session with no stack says it is unavailable', (tester) async {
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      fetcher: (sessionId, subagentId) async =>
          throw BridgeFailure(501, 'the agent has no stack'),
    );
    await store.refresh();
    expect(store.status, StackStatus.unsupported);

    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // Unknown access is said, not guessed: no transcript and no header claim
    // a session that is not there.
    expect(find.byType(Transcript), findsNothing);
    expect(find.textContaining('is unreachable'), findsOneWidget);
    expect(tester.takeException(), isNull);

    store.dispose();
    view.dispose();
  });
}
