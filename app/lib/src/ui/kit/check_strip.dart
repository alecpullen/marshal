import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// One block per CI check (design-system.md §5), in the forge's order, so a
/// red one is visible at a glance. Each block's tooltip names its check and
/// its state, so the strip is never colour alone.
class CheckStrip extends StatelessWidget {
  const CheckStrip(this.checks, {super.key});

  final List<Check> checks;

  /// A block's size.
  static const double blockWidth = Space.s12;
  static const double blockHeight = Space.s6;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Wrap(
      spacing: Space.s2,
      runSpacing: Space.s2,
      children: [
        for (final check in checks)
          Tip(
            message: '${check.name} · ${check.state.word.toLowerCase()}',
            child: Semantics(
              label: '${check.name}: ${check.state.word}',
              child: Container(
                width: blockWidth,
                height: blockHeight,
                decoration: BoxDecoration(
                  color: check.state.color(e),
                  borderRadius: BorderRadius.circular(Radii.bar),
                ),
              ),
            ),
          ),
      ],
    );
  }
}
