import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'dot.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// A step's header (design-system.md §5): its ordinal, its headline, how long
/// it took, and — where the step holds rows — a disclosure that folds them.
///
/// [live] draws the running dot beside the headline while the step is being
/// worked on. The duration is mono, because it is a number that lines up
/// down the transcript (§3).
class StepHeader extends StatelessWidget {
  const StepHeader({
    super.key,
    required this.number,
    required this.headline,
    this.took,
    this.live = false,
    this.foldable = false,
    this.folded = false,
    this.onTap,
  });

  /// The step's ordinal, as the agent counts steps. Drawn in mono, faint.
  final String number;

  /// The step's one-line headline. It is the tree's own, never re-derived.
  final String headline;

  /// How long the step took, e.g. "41s". Null while it is still running, or
  /// when the agent reported no end.
  final String? took;

  /// Whether the step is still being worked on.
  final bool live;

  /// Whether the step has rows to fold away.
  final bool foldable;

  /// Whether those rows are hidden.
  final bool folded;

  /// Toggles the fold, or moves the browse cursor. A step with no fold and no
  /// [onTap] is plain text.
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final took = this.took;
    final row = Row(
      children: [
        SizedBox(
          width: Sizes.row,
          child: Text(number, style: e.monoSmall.tint(e.faint)),
        ),
        if (live) ...[
          const Dot(Status.running),
          const SizedBox(width: Space.s8),
        ],
        Expanded(
          child: Text(
            headline,
            style: e.bodyStrong,
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
          ),
        ),
        if (took != null) ...[
          const SizedBox(width: Space.s8),
          Text(took, style: e.monoSmall.tint(e.faint)),
        ],
        if (foldable) ...[
          const SizedBox(width: Space.s6),
          Tip(
            message: folded ? 'Unfold this step' : 'Fold this step',
            child: Icon(
              folded ? Glyphs.chevron_right : Glyphs.chevron_down,
              size: Sizes.iconInline,
              color: e.faint,
            ),
          ),
        ],
      ],
    );

    final body = Padding(
      padding: const EdgeInsets.symmetric(vertical: Space.s6),
      child: row,
    );
    if (onTap == null) return body;
    return Press(
      onTap: onTap,
      semanticLabel: foldable
          ? (folded ? 'Unfold step $number' : 'Fold step $number')
          : 'Step $number',
      builder: (context, state) => DecoratedBox(
        decoration: BoxDecoration(
          color: state.hovered ? e.hover : null,
          borderRadius: BorderRadius.circular(Radii.control),
        ),
        child: body,
      ),
    );
  }
}
