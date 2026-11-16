import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';

/// A labelled bar for capacity and quota (design-system.md §5). The bar is
/// `muted`; [Meter.fault] draws it in `err` for a meter that reports a
/// fault, whose [value] then says what the fault is in words.
class Meter extends StatelessWidget {
  const Meter({
    super.key,
    required this.label,
    required this.value,
    required this.fraction,
    this.tooltip,
  }) : fault = false;

  const Meter.fault({
    super.key,
    required this.label,
    required this.value,
    required this.fraction,
    this.tooltip,
  }) : fault = true;

  /// What is measured: a host, the disk.
  final String label;

  /// The reading in words: "6 of 8 slots", "unreachable 4m".
  final String value;

  /// How full, from 0 to 1.
  final double fraction;

  /// The bar's tooltip; by default the fraction as a percentage.
  final String? tooltip;

  final bool fault;

  /// The bar's thickness.
  static const double thickness = Space.s4;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final share = fraction.clamp(0.0, 1.0);
    final tip = tooltip ?? '${(share * 100).round()}% used';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Expanded(
              child: Text(
                label,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: e.body,
              ),
            ),
            const SizedBox(width: Space.s8),
            Text(value, style: e.monoSmall.tint(fault ? e.err : e.muted)),
          ],
        ),
        const SizedBox(height: Space.s6),
        Tip(
          message: tip,
          child: Semantics(
            label: tip,
            child: ClipRRect(
              borderRadius: BorderRadius.circular(Radii.bar),
              child: SizedBox(
                height: thickness,
                child: Stack(
                  children: [
                    Positioned.fill(child: ColoredBox(color: e.border)),
                    FractionallySizedBox(
                      widthFactor: share,
                      heightFactor: 1,
                      child: ColoredBox(color: fault ? e.err : e.muted),
                    ),
                  ],
                ),
              ),
            ),
          ),
        ),
      ],
    );
  }
}
