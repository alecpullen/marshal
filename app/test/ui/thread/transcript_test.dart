import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/transcript.dart';

import 'support.dart';

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
    expect(find.byType(Narration), findsNWidgets(4));
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
}
