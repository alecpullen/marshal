import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// A small line (design-system.md §5): one series in `fg`, no axes, no
/// fill. It shows a shape, not values; [semanticLabel] says what the shape
/// is of for assistive technology.
class Spark extends StatelessWidget {
  const Spark(this.values, {super.key, required this.semanticLabel});

  final List<double> values;
  final String semanticLabel;

  /// The line's height.
  static const double height = Space.s32;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: semanticLabel,
      child: SizedBox(
        height: height,
        width: double.infinity,
        child: CustomPaint(
          painter: _SparkPainter(values, Ember.of(context).fg),
        ),
      ),
    );
  }
}

class _SparkPainter extends CustomPainter {
  _SparkPainter(this.values, this.color);

  final List<double> values;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    if (values.length < 2) return;
    var low = values.first;
    var high = values.first;
    for (final v in values) {
      if (v < low) low = v;
      if (v > high) high = v;
    }
    final span = high - low;
    // Keep the stroke inside the box: half a line's width at top and bottom.
    const inset = Space.s2;
    final path = Path();
    for (var i = 0; i < values.length; i++) {
      final x = size.width * i / (values.length - 1);
      final level = span == 0 ? 0.5 : (values[i] - low) / span;
      final y = size.height - inset - (size.height - inset * 2) * level;
      i == 0 ? path.moveTo(x, y) : path.lineTo(x, y);
    }
    canvas.drawPath(
      path,
      Paint()
        ..color = color
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1.5
        ..strokeJoin = StrokeJoin.round
        ..strokeCap = StrokeCap.round,
    );
  }

  @override
  bool shouldRepaint(_SparkPainter old) =>
      old.color != color || old.values != values;
}
