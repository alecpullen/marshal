import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/transcript.dart';

/// The performance budget this phase sets (f3 plan, decisions): scrolling a
/// 5,000-node transcript builds no frame over 32 ms on the CI runner.
const frameBudget = Duration(milliseconds: 32);

/// A transcript of [nodes] nodes: steps, each with one tool call, until the
/// count is reached.
StackSnapshot _largeTranscript(int nodes) {
  final children = <String>[];
  final wire = <WireNode>[];
  for (var i = 1; i * 2 <= nodes; i++) {
    children.add('step:$i');
    wire
      ..add(
        WireNode(
          id: 'step:$i',
          kind: 'step',
          parent: 'turn:1',
          children: ['tool:$i'],
          step: WireStep(
            headline: 'Step $i did one thing',
            startedAt: 1000 + i * 100,
            endedAt: 1000 + i * 100 + 50,
          ),
        ),
      )
      ..add(
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
                target: 'internal/acp/package_$i/stack.go',
                summaryLine: '${100 + i} lines',
              ),
            ],
          ),
        ),
      );
  }
  return StackSnapshot(
    sessionID: 's-1',
    rev: 1,
    roots: const ['turn:1'],
    nodes: [
      WireNode(id: 'turn:1', kind: 'turn', children: children),
      ...wire,
    ],
  );
}

void main() {
  testWidgets('a 5,000-node transcript scrolls inside the frame budget', (
    tester,
  ) async {
    final store = StackStore.seeded(_largeTranscript(5000));
    final view = ThreadViewState();
    expect(store.length, greaterThanOrEqualTo(5000));

    tester.view.physicalSize = const Size(900, 700);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    tester.platformDispatcher.accessibilityFeaturesTestValue =
        const FakeAccessibilityFeatures(disableAnimations: true);
    addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);

    await tester.pumpWidget(
      EmberApp(
        home: Transcript(store: store, view: view),
      ),
    );
    await tester.pumpAndSettle();

    // A builder list draws what the window needs, never the whole tree.
    expect(find.byType(StepHeader).evaluate().length, lessThan(60));

    final worst = <Duration>[];
    for (var i = 0; i < 40; i++) {
      await tester.drag(find.byType(ListView), const Offset(0, -120));
      // The frame's own work: build, layout and paint of the window that
      // moved into view. The gesture that moved it is the test harness's.
      final watch = Stopwatch()..start();
      await tester.pump();
      watch.stop();
      worst.add(watch.elapsed);
    }

    worst.sort();
    final slowest = worst.last;
    expect(
      slowest,
      lessThan(frameBudget),
      reason:
          'a frame took ${slowest.inMilliseconds} ms; the budget is '
          '${frameBudget.inMilliseconds} ms',
    );

    store.dispose();
    view.dispose();
  });

  testWidgets('every row is reachable: the list ends at the last row', (
    tester,
  ) async {
    final store = StackStore.seeded(_largeTranscript(5000));
    final view = ThreadViewState();
    tester.view.physicalSize = const Size(900, 700);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    tester.platformDispatcher.accessibilityFeaturesTestValue =
        const FakeAccessibilityFeatures(disableAnimations: true);
    addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);

    await tester.pumpWidget(
      EmberApp(
        home: Transcript(store: store, view: view),
      ),
    );
    await tester.pumpAndSettle();

    final rows = flattenRows(store, view);
    expect(rows.length, 5000);
    expect(rows.last.id, 'tool:2500');

    store.dispose();
    view.dispose();
  });
}
