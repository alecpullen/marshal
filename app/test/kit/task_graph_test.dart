import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

/// `TaskGraph` (design-system.md §5, §10): a plan's tasks as a dependency
/// graph — one column per dependency layer, left to right, a place within it
/// top to bottom, straight edges in `border` behind the nodes.
///
/// It fills the width it is given and never scrolls sideways (§4); a node is
/// named by its tooltip and opens its task only when the screen says it can.
void main() {
  final e = tokens();

  const nodes = [
    GraphNode(
      number: 1,
      label: '1 · Read',
      layer: 0,
      index: 0,
      status: Status.ready,
    ),
    GraphNode(
      number: 2,
      label: '2 · Approve',
      layer: 1,
      index: 0,
      status: Status.running,
    ),
    GraphNode(number: 3, label: '3 · Ship', layer: 1, index: 1),
  ];
  const edges = [GraphEdge(from: 1, to: 2), GraphEdge(from: 2, to: 3)];

  /// The node's own box: the one `DecoratedBox` the node draws itself in.
  BoxDecoration boxOf(WidgetTester tester, String label) {
    final decoration = tester
        .widgetList<DecoratedBox>(
          find.ancestor(
            of: find.text(label),
            matching: find.byType(DecoratedBox),
          ),
        )
        .map((box) => box.decoration)
        .whereType<BoxDecoration>()
        .first;
    return decoration;
  }

  testWidgets('an empty plan says so, in the words it is given', (
    tester,
  ) async {
    await pumpKit(tester, const TaskGraph(nodes: [], edges: []));
    expect(find.text('No tasks yet'), findsOneWidget);
    // Nothing is drawn to stand for a task the plan does not have.
    expect(find.byType(Dot), findsNothing);

    await pumpKit(
      tester,
      const TaskGraph(nodes: [], edges: [], empty: 'Nothing planned'),
    );
    expect(find.text('Nothing planned'), findsOneWidget);
  });

  testWidgets('tasks sit in their layer, left to right, and their place', (
    tester,
  ) async {
    await pumpKit(tester, const TaskGraph(nodes: nodes, edges: edges));

    final read = tester.getTopLeft(find.text('1 · Read'));
    final approve = tester.getTopLeft(find.text('2 · Approve'));
    final ship = tester.getTopLeft(find.text('3 · Ship'));
    expect(approve.dx, greaterThan(read.dx), reason: 'layer 1 is right of 0');
    expect(ship.dx, approve.dx, reason: 'the same layer is one column');
    expect(read.dy, approve.dy, reason: 'the first place is the top row');
    expect(
      ship.dy,
      greaterThan(approve.dy),
      reason: 'place 1 is below place 0',
    );

    // The graph fills the width it is given: two layers and their gap, and
    // two places and their gap for its height.
    final whole = tester.getSize(find.byType(TaskGraph));
    expect(whole.height, 2 * TaskGraph.nodeHeight + TaskGraph.gap);
    final columnWidth = (whole.width - TaskGraph.gap) / 2;
    final first = tester.getSize(
      find
          .ancestor(
            of: find.text('1 · Read'),
            matching: find.byType(DecoratedBox),
          )
          .first,
    );
    expect(first.width, closeTo(columnWidth, 0.01));
    expect(first.height, TaskGraph.nodeHeight);

    // A graph never scrolls sideways: it is laid out to the width it has.
    expect(find.byType(Scrollable), findsNothing);
  });

  testWidgets('each node is named by its tooltip and its mark', (tester) async {
    await pumpKit(tester, const TaskGraph(nodes: nodes, edges: edges));

    expect(hasTip(tester, find.text('1 · Read'), 'Task 1 · Ready'), isTrue);
    expect(hasTip(tester, find.text('3 · Ship'), 'Task 3 · Idle'), isTrue);
    expect(
      tester.widget<Dot>(find.byType(Dot).first).tooltip,
      'Ready',
      reason: 'the status is spelled out, never colour alone (§6)',
    );
  });

  testWidgets('a node that opens its task takes a tap; one without is a fact', (
    tester,
  ) async {
    var opened = 0;
    await pumpKit(
      tester,
      TaskGraph(
        nodes: [
          GraphNode(
            number: 1,
            label: '1 · Read',
            layer: 0,
            index: 0,
            onTap: () => opened++,
          ),
          const GraphNode(number: 2, label: '2 · Approve', layer: 1, index: 0),
        ],
        edges: const [GraphEdge(from: 1, to: 2)],
      ),
    );

    await tester.tap(find.text('1 · Read'));
    await tester.pump();
    expect(opened, 1);

    await tester.tap(find.text('2 · Approve'), warnIfMissed: false);
    await tester.pump();
    expect(opened, 1, reason: 'a node with no task behind it is read only');
    expect(tester.takeException(), isNull);
  });

  testWidgets('the selected node borders in accent, the rest in border', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const TaskGraph(
        nodes: [
          GraphNode(
            number: 1,
            label: '1 · Read',
            layer: 0,
            index: 0,
            selected: true,
          ),
          GraphNode(number: 2, label: '2 · Approve', layer: 1, index: 0),
        ],
        edges: [],
      ),
    );

    expect(borderColour(boxOf(tester, '1 · Read')), e.accent);
    expect(borderColour(boxOf(tester, '2 · Approve')), e.border);
    expect(styleOf(tester, '1 · Read'), e.bodyStrong);
    expect(styleOf(tester, '2 · Approve'), e.small);
  });

  testWidgets('edges are drawn behind the nodes, unknown tasks ignored', (
    tester,
  ) async {
    // The painter is behind the nodes, over the whole graph, and an edge that
    // names a task the plan does not have draws nothing.
    await pumpKit(
      tester,
      const TaskGraph(
        nodes: nodes,
        edges: [GraphEdge(from: 1, to: 2), GraphEdge(from: 2, to: 999)],
      ),
    );

    expect(tester.takeException(), isNull);
    final whole = tester.getSize(find.byType(TaskGraph));
    final painters = tester
        .widgetList<CustomPaint>(
          find.descendant(
            of: find.byType(TaskGraph),
            matching: find.byType(CustomPaint),
          ),
        )
        .where((paint) => paint.painter != null)
        .toList();
    expect(painters, isNotEmpty, reason: 'the edges are painted');
    expect(
      tester.getSize(find.byWidget(painters.first)),
      whole,
      reason: 'the edges span the graph, so they meet the nodes at its places',
    );
  });

  testWidgets('a long label wraps to two lines and ellipsizes', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 240,
        child: TaskGraph(
          nodes: [
            GraphNode(
              number: 1,
              label:
                  '1 · A task whose label runs on for a very long while '
                  'indeed and then some',
              layer: 0,
              index: 0,
            ),
          ],
          edges: [],
        ),
      ),
    );

    expect(tester.takeException(), isNull);
    final label = tester.widget<Text>(
      find.textContaining('A task whose label'),
    );
    expect(label.maxLines, 2);
    expect(label.overflow, TextOverflow.ellipsis);
  });
}
