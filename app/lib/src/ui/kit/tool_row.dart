import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';

/// What a tool call did, as the row reads it (design-system.md §5).
enum ToolOutcome {
  /// The call finished and did what it was asked.
  ok,

  /// The call failed, or the command it ran exited non-zero.
  failed,

  /// The call is still running; its result line is the latest output.
  running,
}

/// One tool call inside a step: the verb and what it acted on, in mono, with
/// its result at the right (design-system.md §5).
///
/// It is indented to the step's text, so the transcript reads as one spine:
/// a step header, then its calls, then its words. Selecting a row is the
/// caller's business; [onTap] shows the full output, as the inspector will.
class ToolRow extends StatelessWidget {
  const ToolRow({
    super.key,
    required this.verb,
    required this.target,
    this.result,
    this.outcome = ToolOutcome.ok,
    this.selected = false,
    this.onTap,
  });

  /// The word the row leads with: "read", "edit", "shell".
  final String verb;

  /// What the call acted on: a path, a command, a query. One line; longer
  /// values ellipsize.
  final String target;

  /// What came back, in one line: "148 lines", "exit 0", "6 files".
  final String? result;

  /// How the call ended, which colours the result.
  final ToolOutcome outcome;

  /// Whether this row is the one the inspector is showing.
  final bool selected;

  /// Shows the call's detail. Null leaves the row plain.
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final resultColour = switch (outcome) {
      ToolOutcome.ok => e.muted,
      ToolOutcome.failed => e.err,
      ToolOutcome.running => e.fg,
    };
    final result = this.result;
    final row = Padding(
      padding: const EdgeInsets.only(
        left: Sizes.row,
        bottom: Space.s2,
        top: Space.s2,
        right: Space.s8,
      ),
      child: Row(
        children: [
          Text(verb, style: e.mono.tint(e.muted)),
          const SizedBox(width: Space.s8),
          Expanded(
            child: Text(
              target,
              style: e.mono,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
          ),
          if (result != null) ...[
            const SizedBox(width: Space.s12),
            Text(
              result,
              style: e.monoSmall.tint(resultColour),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
          ],
        ],
      ),
    );
    if (onTap == null) return row;
    return Press(
      onTap: onTap,
      selected: selected,
      semanticLabel: '$verb $target',
      builder: (context, state) => DecoratedBox(
        decoration: BoxDecoration(
          color: selected
              ? e.selected
              : state.hovered
              ? e.hover
              : null,
          borderRadius: BorderRadius.circular(Radii.control),
        ),
        child: row,
      ),
    );
  }
}
