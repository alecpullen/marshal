import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'box.dart';
import 'dot.dart';
import 'hairline.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// One cell of a lane: what a role did for one task, named in words and marked
/// with the one status it is at.
@immutable
class LaneCell {
  const LaneCell({
    required this.status,
    required this.words,
    this.note,
    this.selected = false,
    this.onTap,
  });

  /// The status the cell is at: passed, failed, running or idle.
  final Status status;

  /// The cell's own word: "Passed", "Running", or the state a newer bridge
  /// sent, so a stage is never hidden.
  final String words;

  /// What the stage said about itself, for the tooltip and the small line.
  final String? note;

  /// Whether this is the cell the screen is showing.
  final bool selected;

  /// Opens the role's conversation for this task. Null leaves the cell a fact.
  final VoidCallback? onTap;
}

/// One row of lanes: a task, and what each of its roles did.
@immutable
class LaneRow {
  const LaneRow({required this.title, required this.cells, this.detail});

  /// The task, as its row names it: "Task 2 · Read the item".
  final String title;

  /// One cell per lane, in the lanes' own order.
  final List<LaneCell> cells;

  /// A line beneath the title: what the task is doing, when it says.
  final String? detail;
}

/// A run's tasks by role (design-system.md §5, F6 decision 11): one row per
/// task, one column per lane — Implement, Verify, Review, Commit — with each
/// cell marked by the one status it is at and named in words beside the mark,
/// so a cell is never colour alone.
///
/// It is a grid of facts, not a control: a cell with [LaneCell.onTap] is
/// clickable to open that role's conversation, and one without is read only.
class Lanes extends StatelessWidget {
  const Lanes({super.key, required this.lanes, required this.rows});

  /// The lane names, in the order the cells are given.
  final List<String> lanes;

  final List<LaneRow> rows;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _header(context),
        const Hairline(),
        for (final (index, row) in rows.indexed) ...[
          if (index > 0) const Hairline(),
          _row(context, row),
        ],
      ],
    );
  }

  /// The lane names, above their columns.
  Widget _header(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: Space.s6),
      child: Row(
        children: [
          const Expanded(flex: 3, child: SizedBox.shrink()),
          for (final lane in lanes)
            Expanded(flex: 2, child: Text(lane, style: e.label.tint(e.muted))),
        ],
      ),
    );
  }

  Widget _row(BuildContext context, LaneRow row) {
    final e = Ember.of(context);
    final detail = row.detail;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: Space.s8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            flex: 3,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(row.title, style: e.bodyStrong),
                if (detail != null && detail.isNotEmpty) ...[
                  const SizedBox(height: Space.s2),
                  Text(detail, style: e.small.tint(e.muted)),
                ],
              ],
            ),
          ),
          for (final cell in row.cells)
            Expanded(flex: 2, child: _cell(context, cell)),
        ],
      ),
    );
  }

  /// One cell: the status dot, the word, and — when it has a note — the note
  /// small beneath. The whole cell is one clickable when it opens a role.
  Widget _cell(BuildContext context, LaneCell cell) {
    final e = Ember.of(context);
    final note = cell.note;
    final body = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Dot(cell.status, tooltip: cell.status.word),
            const SizedBox(width: Space.s6),
            Flexible(
              child: Text(
                cell.words,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: cell.selected ? e.bodyStrong : e.small,
              ),
            ),
          ],
        ),
        if (note != null && note.isNotEmpty) ...[
          const SizedBox(height: Space.s2),
          Text(
            note,
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: e.monoSmall.tint(e.muted),
          ),
        ],
      ],
    );
    final tip = Tip(
      message: note == null || note.isEmpty
          ? '${cell.words} · ${cell.status.word}'
          : '${cell.words} · ${cell.status.word} · $note',
      child: body,
    );
    if (cell.onTap == null) return tip;
    return Press(
      onTap: cell.onTap,
      selected: cell.selected,
      radius: Radii.control,
      builder: (context, state) =>
          Padding(padding: const EdgeInsets.all(Space.s4), child: tip),
    );
  }
}

/// A boxed group around the lanes, so the grid reads as one thing.
Box lanesBox(Widget child) => Box.flush(child: child);
