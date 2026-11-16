import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// Progress as steps (design-system.md §5): done steps are `ok`, the
/// current one is the work's status colour, pending ones are `border`. Its
/// tooltip says how far along it is, so the bar is never colour alone.
class Segments extends StatelessWidget {
  const Segments({
    super.key,
    required this.done,
    required this.total,
    required this.status,
  });

  final int done;
  final int total;

  /// The status of the work, which colours the current step.
  final Status status;

  /// A segment's thickness.
  static const double thickness = Space.s4;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    Color colour(int i) => i < done
        ? e.ok
        : i == done
        ? status.color(e)
        : e.border;
    return Tip(
      message: '$done of $total steps done',
      child: Semantics(
        label: '$done of $total steps done',
        child: Row(
          children: [
            for (var i = 0; i < total; i++) ...[
              if (i > 0) const SizedBox(width: Space.s4),
              Expanded(
                child: Container(
                  height: thickness,
                  decoration: BoxDecoration(
                    color: colour(i),
                    borderRadius: BorderRadius.circular(Radii.bar),
                  ),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}
