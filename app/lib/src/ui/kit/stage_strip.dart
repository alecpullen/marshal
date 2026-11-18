import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'segments.dart';
import 'vocabulary.dart';

/// The stages of a work item, with the one it is at marked (design-system.md
/// §5, F6 decision 2).
///
/// The strip is a fact about the item, not a control: the stages are drawn as
/// a progress bar and as words, so a stage is never colour alone. A stage an
/// item has left is `muted`, the one it is at is the status colour in words,
/// and the ones ahead are `faint`.
///
/// The strip has no fifth stage. An item that is ending says so in [endWord],
/// beside it, because stopping, stopped and closed are states of the work
/// rather than stages of it.
class StageStrip extends StatelessWidget {
  const StageStrip({
    super.key,
    required this.labels,
    required this.current,
    required this.status,
    this.endWord,
  });

  /// The stages, in order, as the product names them.
  final List<String> labels;

  /// The index of the stage the item is at.
  final int current;

  /// The thread's status: the colour the current stage and the bar take.
  final Status status;

  /// A word beside the strip for an item that is ending, such as "Stopped".
  final String? endWord;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final end = endWord;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Segments(done: current, total: labels.length, status: status),
        const SizedBox(height: Space.s6),
        // One word per segment, with the same gaps the bar draws, so a stage
        // is named under the segment it is.
        Row(
          children: [
            for (final (index, label) in labels.indexed) ...[
              if (index > 0) const SizedBox(width: Space.s4),
              Expanded(
                child: Text(
                  label,
                  style: index == current
                      ? e.bodyStrong
                      : e.small.tint(e.faint),
                ),
              ),
            ],
          ],
        ),
        if (end != null && end.isNotEmpty) ...[
          const SizedBox(height: Space.s6),
          Text(end, style: e.label.tint(e.muted)),
        ],
      ],
    );
  }
}
