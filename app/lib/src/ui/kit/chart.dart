import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// One bar of a [Chart]: what it stands for, how much, and — when the height's
/// meaning is a status — that status. A bar with no [tone] is drawn in `fg`.
@immutable
class ChartBar {
  const ChartBar({required this.label, required this.value, this.tone});

  final String label;
  final double value;

  /// `ok`, `warn` or `err`, when the value is a status. Null draws `fg`.
  final Status? tone;
}

/// The Studio's one chart (design-system.md §5, §10): a row of bars, each
/// labelled directly with its own name and value.
///
/// A single series is `fg`; a bar whose value is a status takes that status's
/// colour, and never more than `ok`, `warn` and `err`. Every bar is labelled
/// and tipped, so there is no legend and the numbers are readable without the
/// shape. Text scaled up grows the chart rather than being clipped.
class Chart extends StatelessWidget {
  const Chart({
    super.key,
    required this.bars,
    this.unit = '',
    this.emptyMessage = 'Nothing to show yet.',
  });

  final List<ChartBar> bars;

  /// What the values are of, appended to a bar's tooltip: "usd", "tokens".
  final String unit;

  /// What the chart says when it has no bars.
  final String emptyMessage;

  /// How tall the bars stand; the labels are outside it.
  static const double height = Space.s32 * 4;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    if (bars.isEmpty) {
      return Text(emptyMessage, style: e.body.tint(e.muted));
    }
    var max = 0.0;
    for (final bar in bars) {
      if (bar.value > max) max = bar.value;
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        SizedBox(
          height: height,
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              for (final (index, bar) in bars.indexed) ...[
                if (index > 0) const SizedBox(width: Space.s8),
                Expanded(
                  child: Tip(
                    message: _tip(bar),
                    child: Semantics(
                      label: _tip(bar),
                      child: Align(
                        alignment: Alignment.bottomCenter,
                        child: FractionallySizedBox(
                          heightFactor: max == 0
                              ? 0
                              : (bar.value / max).clamp(
                                  bar.value > 0 ? 0.02 : 0,
                                  1,
                                ),
                          child: _Bar(
                            colour: bar.tone == null
                                ? e.fg
                                : bar.tone!.color(e),
                          ),
                        ),
                      ),
                    ),
                  ),
                ),
              ],
            ],
          ),
        ),
        const SizedBox(height: Space.s8),
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            for (final (index, bar) in bars.indexed) ...[
              if (index > 0) const SizedBox(width: Space.s8),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(bar.label, style: e.small, maxLines: 2),
                    Text(_value(bar), style: e.small.tint(e.muted)),
                  ],
                ),
              ),
            ],
          ],
        ),
      ],
    );
  }

  /// The value as the chart writes it: a whole number, or dollars to the cent.
  String _value(ChartBar bar) {
    final text = unit == 'usd'
        ? '\$${bar.value.toStringAsFixed(2)}'
        : bar.value == bar.value.roundToDouble()
        ? '${bar.value.round()}'
        : bar.value.toStringAsFixed(1);
    return unit.isEmpty || unit == 'usd' ? text : '$text $unit';
  }

  String _tip(ChartBar bar) => unit.isEmpty
      ? '${bar.label}: ${_value(bar)}'
      : '${bar.label}: ${_value(bar)}';
}

/// One bar: a filled rectangle whose ends are as round as a bar's are.
class _Bar extends StatelessWidget {
  const _Bar({required this.colour});

  final Color colour;

  @override
  Widget build(BuildContext context) {
    return DecoratedBox(
      decoration: BoxDecoration(
        color: colour,
        borderRadius: const BorderRadius.vertical(
          top: Radius.circular(Radii.bar),
        ),
      ),
      child: const SizedBox.expand(),
    );
  }
}
