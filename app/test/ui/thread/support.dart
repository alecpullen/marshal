import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/inbox/answerable_ask.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

/// The thread the thread-screen tests draw.
model.ThreadRef threadRef({
  model.ThreadStatus status = model.ThreadStatus.running,
}) => model.ThreadRef(
  host: 'local',
  id: 'a-1',
  project: '/home/u/marshal',
  title: 'Clean up the fixtures',
  kind: model.ThreadKind.agent,
  status: status,
  headline: 'Removing the fixtures nobody reads',
  lastActive: DateTime.utc(2026, 11, 17, 4, 10),
);

/// A turn holding [steps] steps, each with one tool call, as the agent
/// projects it. More steps than fit make the list scroll, which the following
/// tests need.
StackSnapshot transcriptFixture({int steps = 3}) => StackSnapshot(
  sessionID: 's-1',
  rev: 1,
  roots: const ['turn:1'],
  nodes: [
    WireNode(
      id: 'turn:1',
      kind: 'turn',
      children: [
        'msg:1',
        for (var i = 1; i <= steps; i++) 'step:$i',
        'receipt:1',
      ],
    ),
    const WireNode(
      id: 'msg:1',
      kind: 'message',
      parent: 'turn:1',
      message: WireMessage(
        role: 'user',
        content: 'Clean up the test fixtures nobody uses any more.',
      ),
    ),
    for (var i = 1; i <= steps; i++) ...[
      WireNode(
        id: 'step:$i',
        kind: 'step',
        parent: 'turn:1',
        children: ['tool:$i'],
        step: WireStep(
          headline: 'Step number $i mapped the fixtures',
          startedAt: 1000 + i * 1000,
          endedAt: 40000 + i * 1000,
        ),
      ),
      WireNode(
        id: 'tool:$i',
        kind: 'tool',
        parent: 'turn:1',
        tool: WireTool(
          name: 'file.read',
          display: 'Read file',
          verb: 'read',
          calls: [
            WireCall(
              callID: 'c$i',
              target: 'internal/acp/stack.go',
              summaryLine: '${100 + i} lines',
            ),
          ],
        ),
      ),
    ],
    const WireNode(
      id: 'receipt:1',
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
);

/// A store holding [transcriptFixture].
StackStore fixtureStore({int steps = 3}) =>
    StackStore.seeded(transcriptFixture(steps: steps));

/// The screen, pumped under the kit's root at a window the transcript scrolls
/// in.
Future<void> pumpThread(
  WidgetTester tester, {
  required StackStore store,
  required ThreadViewState view,
  model.ThreadRef? thread,
  StateKind? state,
  VoidCallback? onNewWork,
  void Function(String nodeId)? onCopyRow,
  Widget? composer,
  List<model.Ask> asks = const [],
  AnswerAsk? onAnswer,
  DateTime? now,
  void Function(Place place)? onOpenHandoff,
  void Function(Place place)? onOpenAssignment,
  void Function(String assignmentId)? onRetryAssignment,
}) async {
  tester.view.physicalSize = const Size(900, 700);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  // A live step's dot pulses forever, which pumpAndSettle never settles. The
  // app honours the reduced-motion preference (§9), so a test sets it.
  tester.platformDispatcher.accessibilityFeaturesTestValue =
      const FakeAccessibilityFeatures(disableAnimations: true);
  addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);
  await tester.pumpWidget(
    EmberApp(
      brightness: Brightness.light,
      home: ThreadScreen(
        thread: thread ?? threadRef(),
        store: store,
        view: view,
        state: state,
        onNewWork: onNewWork,
        onCopyRow: onCopyRow,
        composer: composer,
        asks: asks,
        onAnswer: onAnswer,
        now: now,
        onOpenHandoff: onOpenHandoff,
        onOpenAssignment: onOpenAssignment,
        onRetryAssignment: onRetryAssignment,
      ),
    ),
  );
  await tester.pumpAndSettle();
}
