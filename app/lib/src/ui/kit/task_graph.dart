import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'dot.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// One node of a task graph: a task, the layer it sits in, and where it sits
/// within that layer.
@immutable
class GraphNode {
  const GraphNode({
    required this.number,
    required this.label,
    required this.layer,
    required this.index,
    this.status = Status.idle,
    this.selected = false,
    this.onTap,
  });

  /// The task's number, which the edges name.
  final int number;

  /// What the node says: "2 · Read the item".
  final String label;

  /// The column it is drawn in, left to right.
  final int layer;

  /// Its place within the column, top to bottom.
  final int index;

  final Status status;
  final bool selected;
  final VoidCallback? onTap;
}

/// One edge of a task graph: [from] must finish before [to] begins.
@immutable
class GraphEdge {
  const GraphEdge({required this.from, required this.to});

  final int from;
  final int to;
}

/// A plan's tasks as a dependency graph (design-system.md §5, F6 decision 11):
/// the tasks laid out in dependency layers, left to right, with straight edges,
/// and no free dragging.
///
/// The edges are drawn behind the nodes in `border`, so they group rather than
/// colour: a graph whose edges were coloured would say something about the work
/// the plan does not.
class TaskGraph extends StatelessWidget {
  const TaskGraph({
    super.key,
    required this.nodes,
    required this.edges,
    this.empty = 'No tasks yet',
  });

  final List<GraphNode> nodes;
  final List<GraphEdge> edges;

  /// What the graph says when the plan has no tasks.
  final String empty;

  /// The height one node box takes, and the gap between layers: both from the
  /// scale, so the graph is built from tokens like every other component.
  static const double nodeHeight = Sizes.touch;
  static const double gap = Space.s16;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    if (nodes.isEmpty) {
      return Text(empty, style: e.body.tint(e.muted));
    }
    final columns =
        nodes.map((node) => node.layer).reduce((a, b) => a > b ? a : b) + 1;
    final rows =
        nodes.map((node) => node.index).reduce((a, b) => a > b ? a : b) + 1;
    final places = {for (final node in nodes) node.number: node};
    // The graph fills the width it is given: one flexible column per layer, so
    // it never scrolls sideways (§4), and the edges are drawn between the
    // columns' own places.
    return LayoutBuilder(
      builder: (context, constraints) {
        final columnWidth =
            (constraints.maxWidth - gap * (columns - 1)) / columns;
        return SizedBox(
          height: rows * nodeHeight + (rows - 1) * gap,
          child: Stack(
            children: [
              // The edges first, so a node is drawn over the line reaching it.
              Positioned.fill(
                child: CustomPaint(
                  painter: _EdgePainter(
                    edges: edges,
                    places: places,
                    columnWidth: columnWidth,
                    colour: e.border,
                  ),
                ),
              ),
              for (final node in nodes)
                Positioned(
                  left: node.layer * (columnWidth + gap),
                  top: node.index * (nodeHeight + gap),
                  width: columnWidth,
                  height: nodeHeight,
                  child: _node(context, node),
                ),
            ],
          ),
        );
      },
    );
  }

  /// One node: a bordered label with its status dot, clickable when it opens a
  /// task's own views.
  Widget _node(BuildContext context, GraphNode node) {
    final e = Ember.of(context);
    final body = Tip(
      message: 'Task ${node.number} · ${node.status.word}',
      child: DecoratedBox(
        decoration: BoxDecoration(
          border: Border.all(
            color: node.selected ? e.accent : e.border,
            width: Sizes.border,
          ),
          borderRadius: BorderRadius.circular(Radii.control),
        ),
        child: Padding(
          padding: const EdgeInsets.symmetric(
            horizontal: Space.s8,
            vertical: Space.s6,
          ),
          child: Row(
            children: [
              Dot(node.status, tooltip: node.status.word),
              const SizedBox(width: Space.s6),
              Expanded(
                child: Text(
                  node.label,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: node.selected ? e.bodyStrong : e.small,
                ),
              ),
            ],
          ),
        ),
      ),
    );
    if (node.onTap == null) return body;
    return Press(
      onTap: node.onTap,
      selected: node.selected,
      radius: Radii.control,
      builder: (context, state) => body,
    );
  }
}

/// Draws the graph's straight edges behind its nodes.
class _EdgePainter extends CustomPainter {
  const _EdgePainter({
    required this.edges,
    required this.places,
    required this.columnWidth,
    required this.colour,
  });

  final List<GraphEdge> edges;
  final Map<int, GraphNode> places;
  final double columnWidth;
  final Color colour;

  @override
  void paint(Canvas canvas, Size size) {
    final paint = Paint()
      ..color = colour
      ..strokeWidth = Sizes.border
      ..style = PaintingStyle.stroke;
    for (final edge in edges) {
      final from = places[edge.from];
      final to = places[edge.to];
      if (from == null || to == null) continue;
      // Straight edges: from the right of the source to the left of the target.
      final start = Offset(
        from.layer * (columnWidth + TaskGraph.gap) + columnWidth,
        from.index * (TaskGraph.nodeHeight + TaskGraph.gap) +
            TaskGraph.nodeHeight / 2,
      );
      final end = Offset(
        to.layer * (columnWidth + TaskGraph.gap),
        to.index * (TaskGraph.nodeHeight + TaskGraph.gap) +
            TaskGraph.nodeHeight / 2,
      );
      canvas.drawLine(start, end, paint);
    }
  }

  @override
  bool shouldRepaint(_EdgePainter old) =>
      old.edges != edges ||
      old.places != places ||
      old.colour != colour ||
      old.columnWidth != columnWidth;
}
